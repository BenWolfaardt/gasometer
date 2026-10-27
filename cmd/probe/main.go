// Command probe measures the tip C-Chain inclusion needs by sending a ladder of transactions at fixed tips. It spends
// money, so it runs as a campaign from an operator's machine rather than inside the gasometer service.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/BenWolfaardt/gasometer/postgres"
	"github.com/BenWolfaardt/gasometer/probe"
	"github.com/BenWolfaardt/gasometer/rpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	cfg, err := parseConfig(args, getenv, stderr)
	if errors.Is(err, pflag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.logLevel}))

	// One endpoint only: a fallback would change which mempool sees a transaction.
	client, err := rpc.NewClient(ctx, cfg.network, rpc.Config{
		URLs: []string{cfg.rpcURL}, RateLimit: cfg.rateLimit, Burst: 1, Timeout: 20 * time.Second,
		MaxRetries: 3, InitialBackoff: time.Second,
	}, nopObserver{})
	if err != nil {
		return err
	}
	defer client.Close()
	chain, err := rpc.NewTxSender(client, cfg.chainID, cfg.seed, len(probe.Ladder)+1)
	if err != nil {
		return err
	}
	params := probe.Params{Network: cfg.network, Timeout: cfg.timeout, PollInterval: cfg.pollInterval}

	switch cfg.command {
	case commandFund:
		log.Info("funding rungs", "from", chain.Address(probe.FundingAccount), "target_wei", cfg.fundWei)
		return probe.NewCampaign(chain, nil, log, params).Fund(ctx, cfg.fundWei)
	case commandSweep:
		log.Info("sweeping rungs", "to", chain.Address(probe.FundingAccount))
		return probe.NewCampaign(chain, nil, log, params).Sweep(ctx)
	}

	db, err := postgres.Open(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store := postgres.NewProbeStore(db)
	if err := store.Check(ctx); err != nil {
		return err
	}
	log.Info("probe campaign started", "interval", cfg.interval, "duration", cfg.duration, "rungs", len(probe.Ladder))
	err = probe.NewCampaign(chain, store, log, params).Run(ctx, cfg.interval, cfg.duration)
	if errors.Is(err, context.Canceled) {
		log.Info("probe campaign stopped")
		return nil
	}
	return err
}

// nopObserver discards per-request RPC metrics; a campaign exports none.
type nopObserver struct{}

func (nopObserver) ObserveRPC(string, string, string, string, time.Duration) {}
