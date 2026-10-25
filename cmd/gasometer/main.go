// Command gasometer measures how much EVM fee estimators overpay and how often their bids land.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/http"
	"github.com/BenWolfaardt/gasometer/poll"
	"github.com/BenWolfaardt/gasometer/postgres"
	"github.com/BenWolfaardt/gasometer/predict"
	"github.com/BenWolfaardt/gasometer/prometheus"
	"github.com/BenWolfaardt/gasometer/rpc"
)

const pruneInterval = time.Hour

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "gasometer:", err)
		os.Exit(1)
	}
}

// run wires every dependency and blocks until ctx ends. Shutdown order: the loops finish their ticks, the HTTP
// server drains, then the RPC clients and the database close.
func run(ctx context.Context, args []string, stderr io.Writer) error {
	v, err := buildViper(buildFlagSet(), args, stderr)
	if errors.Is(err, pflag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := getConfig(v)
	if err != nil {
		return err
	}
	log, err := newLogger(cfg.Log.Level, cfg.Log.Format, stderr)
	if err != nil {
		return err
	}

	if err := postgres.Migrate(cfg.Database.URL); err != nil {
		return err
	}
	db, err := postgres.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	recorder := prometheus.NewRecorder()
	stores := stores{
		blocks:      postgres.NewBlockStore(db),
		snapshots:   postgres.NewSnapshotStore(db),
		predictions: postgres.NewPredictionStore(db),
		metrics:     recorder,
	}

	var loops []*poll.Loop
	for _, n := range cfg.Networks {
		recorder.InitNetwork(n.Name)
		client, err := rpc.NewClient(ctx, n.Name, rpc.Config(n.RPC), recorder)
		if err != nil {
			return err
		}
		defer client.Close()
		tick, err := pollTick(cfg, n, client, stores, log)
		if err != nil {
			return err
		}
		loops = append(loops, poll.NewLoop(cfg.Schedule.PollInterval, poll.ShutdownGrace, tick))
	}
	pruner := poll.NewPruner(stores.blocks, cfg.Schedule.BlockRetention, time.Now)
	loops = append(loops, poll.NewLoop(pruneInterval, poll.ShutdownGrace, poll.NewPruneTick(pruner, log)))

	listener, err := net.Listen("tcp", cfg.HTTP.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.ListenAddress, err)
	}
	server := http.NewServer(recorder.Gatherer(), db.PingContext, cfg.HTTP.ShutdownTimeout)

	log.Info("gasometer started", "networks", len(cfg.Networks), "listen", listener.Addr().String())
	g, gctx := errgroup.WithContext(ctx)
	for _, loop := range loops {
		g.Go(func() error {
			loop.Run(gctx)
			return nil
		})
	}
	g.Go(func() error { return server.Serve(gctx, listener) })
	err = g.Wait()
	log.Info("gasometer stopped", "err", err)
	return err
}

type stores struct {
	blocks      *postgres.BlockStore
	snapshots   *postgres.SnapshotStore
	predictions *postgres.PredictionStore
	metrics     *prometheus.Recorder
}

func pollTick(cfg fileConfig, n networkConfig, client *rpc.Client, s stores, log *slog.Logger) (poll.Tick, error) {
	network := gasometer.Network{Name: n.Name, ChainID: n.ChainID, Kind: gasometer.NetworkKind(n.Kind), Coin: n.Coin}
	predictors, err := buildPredictors(cfg, network)
	if err != nil {
		return nil, err
	}
	txs := referenceTxs(cfg)
	gasometerParams := predict.Params(cfg.Gasometer)

	collectorDeps := poll.CollectorDeps{
		Fees:      rpc.NewFeeSource(client, rpc.FeeSourceLimits{HistoryDepth: n.HistoryDepth, Batch: n.FeeHistoryBatch}),
		Prices:    rpc.NewChainlinkPriceSource(client, n.PriceFeed),
		Blocks:    s.blocks,
		Snapshots: s.snapshots,
		Metrics:   s.metrics,
		Now:       time.Now,
		Log:       log,
	}
	if network.HasL1Fee() {
		collectorDeps.L1Fees = rpc.NewL1FeeSource(client, network.ChainID)
	}
	collectorParams := poll.CollectorParams{
		RequiredTipPercentile: n.RequiredTipPercentile,
		PriceMaxAge:           n.PriceMaxAge,
		InitialBackfill:       n.InitialBackfill,
	}
	settlerParams := poll.SettlerParams{ExpiryFactor: cfg.Scoring.ExpiryFactor, PriceMaxAge: n.PriceMaxAge}

	return poll.NewTick(n.Name, poll.TickDeps{
		Collector: poll.NewCollector(network, collectorParams, txs, collectorDeps),
		Forecaster: poll.NewForecaster(n.Name, cfg.Scoring.Horizons, gasometerParams, poll.ForecasterDeps{
			Predictors: predictors, Blocks: s.blocks, Predictions: s.predictions, Log: log,
		}),
		Settler: poll.NewSettler(n.Name, settlerParams, txs, poll.SettlerDeps{
			Blocks: s.blocks, Snapshots: s.snapshots, Predictions: s.predictions, Metrics: s.metrics, Now: time.Now, Log: log,
		}),
		Metrics: s.metrics,
		Now:     time.Now,
		Log:     log,
	}), nil
}

func referenceTxs(cfg fileConfig) []gasometer.ReferenceTx {
	txs := make([]gasometer.ReferenceTx, 0, len(cfg.ReferenceTxs))
	for _, tx := range cfg.ReferenceTxs {
		txs = append(txs, gasometer.ReferenceTx(tx))
	}
	return txs
}

// buildPredictors wires the configured predictors. OP Stack networks cap the gasometer projection with the fee
// model; Avalanche uses the empirical projection alone until an SAE projector exists.
func buildPredictors(cfg fileConfig, network gasometer.Network) ([]gasometer.Predictor, error) {
	var projector predict.BaseFeeProjector = predict.NewEmpiricalProjector(cfg.Gasometer.GrowthQuantile)
	if network.Kind == gasometer.NetworkKindOPStack {
		projector = predict.NewWorstCaseProjector(projector)
	}
	var out []gasometer.Predictor
	for _, name := range cfg.Predictors {
		switch name {
		case "node":
			out = append(out, predict.NewNode())
		case "wallet":
			out = append(out, predict.NewWallet())
		case "gasometer":
			out = append(out, predict.NewGasometer(cfg.Gasometer.TipQuantile, cfg.Gasometer.TipWindow, projector))
		default:
			return nil, fmt.Errorf("%w: unknown predictor %q", gasometer.ErrInvalidConfig, name)
		}
	}
	return out, nil
}
