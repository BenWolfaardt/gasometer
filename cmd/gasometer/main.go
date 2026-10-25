// Command gasometer measures how much EVM fee estimators overpay and how often their bids land.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/config"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/http"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/logging"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/metrics"
	postgres "github.com/BenWolfaardt/gasometer/internal/infrastructure/repositories"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/rpc"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/scheduler"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
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
	v, err := config.BuildViper(config.BuildFlagSet(), args, stderr)
	if errors.Is(err, pflag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := config.GetConfig(v)
	if err != nil {
		return err
	}
	log, err := logging.New(cfg.Log.Level, cfg.Log.Format, stderr)
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

	registry := prometheus.NewRegistry()
	recorder := metrics.NewRecorder(registry)
	blocks := postgres.NewPostgresBlockStore(db)
	snapshots := postgres.NewPostgresSnapshotStore(db)
	predictions := postgres.NewPostgresPredictionStore(db)
	stores := stores{blocks: blocks, snapshots: snapshots, predictions: predictions, metrics: recorder}

	var loops []*scheduler.Loop
	for _, network := range cfg.Networks {
		recorder.InitNetwork(network.Network.Name)
		client, err := rpc.NewClient(ctx, network.Network.Name, network.RPC, recorder)
		if err != nil {
			return err
		}
		defer client.Close()
		tick, err := networkTick(cfg, network, client, stores, log)
		if err != nil {
			return err
		}
		loops = append(loops, scheduler.NewLoop(cfg.Schedule.PollInterval, scheduler.ShutdownGrace, tick))
	}
	prune := usecases.NewPrune(blocks, cfg.Schedule.BlockRetention, time.Now)
	loops = append(loops, scheduler.NewLoop(pruneInterval, scheduler.ShutdownGrace, scheduler.NewPruneTick(prune, log)))

	listener, err := net.Listen("tcp", cfg.HTTP.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTP.ListenAddress, err)
	}
	server := http.NewServer(registry, db.PingContext, cfg.HTTP.ShutdownTimeout)

	log.Info().Int("networks", len(cfg.Networks)).Str("listen", listener.Addr().String()).Msg("gasometer started")
	g, gctx := errgroup.WithContext(ctx)
	for _, loop := range loops {
		g.Go(func() error {
			loop.Run(gctx)
			return nil
		})
	}
	g.Go(func() error { return server.Serve(gctx, listener) })
	err = g.Wait()
	log.Info().Err(err).Msg("gasometer stopped")
	return err
}

type stores struct {
	blocks      repositories.BlockStore
	snapshots   repositories.SnapshotStore
	predictions repositories.PredictionStore
	metrics     *metrics.Recorder
}

func networkTick(cfg entities.Config, network entities.NetworkConfig, client *rpc.Client, s stores, log zerolog.Logger) (scheduler.Tick, error) {
	name := network.Network.Name
	predictors, err := buildPredictors(cfg, network.Network)
	if err != nil {
		return nil, err
	}
	collectDeps := usecases.CollectDeps{
		Fees:      rpc.NewEVMFeeSource(client),
		Prices:    rpc.NewChainlinkPriceSource(client, network.PriceFeed),
		Blocks:    s.blocks,
		Snapshots: s.snapshots,
		Metrics:   s.metrics,
		Now:       time.Now,
		Log:       log,
	}
	if network.Network.HasL1Fee() {
		collectDeps.L1Fees = rpc.NewOPStackL1FeeSource(client, network.Network.ChainID)
	}
	return scheduler.NewNetworkTick(name, scheduler.NetworkTickDeps{
		Collect: usecases.NewCollectFees(network, cfg.ReferenceTxs, collectDeps),
		Predict: usecases.NewPredict(name, cfg.Scoring.HorizonSeconds, cfg.Gasometer, usecases.PredictDeps{
			Predictors: predictors, Blocks: s.blocks, Predictions: s.predictions, Log: log,
		}),
		Score: usecases.NewScorePredictions(network, cfg.Scoring.ExpiryFactor, cfg.ReferenceTxs, usecases.ScoreDeps{
			Blocks: s.blocks, Snapshots: s.snapshots, Predictions: s.predictions, Metrics: s.metrics, Now: time.Now, Log: log,
		}),
		Metrics: s.metrics,
		Now:     time.Now,
		Log:     log,
	}), nil
}

// buildPredictors wires the configured predictors. OP Stack networks cap the gasometer projection with the fee
// model; Avalanche uses the empirical projection alone until an SAE projector exists.
func buildPredictors(cfg entities.Config, network entities.Network) ([]services.Predictor, error) {
	projector := services.NewEmpiricalProjector(cfg.Gasometer.GrowthQuantile)
	if network.Kind == entities.NetworkKindOPStack {
		projector = services.NewWorstCaseProjector(projector)
	}
	var out []services.Predictor
	for _, name := range cfg.Predictors {
		switch name {
		case "node":
			out = append(out, services.NewNodePredictor())
		case "wallet":
			out = append(out, services.NewWalletPredictor())
		case "gasometer":
			out = append(out, services.NewGasometerPredictor(cfg.Gasometer.TipQuantile, cfg.Gasometer.TipWindow, projector))
		default:
			return nil, fmt.Errorf("%w: unknown predictor %q", entities.ErrInvalidConfig, name)
		}
	}
	return out, nil
}
