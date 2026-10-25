package poll

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenWolfaardt/gasometer"
)

// TickDeps are the steps one network's poll runs.
type TickDeps struct {
	Collector  *Collector
	Forecaster *Forecaster
	Settler    *Settler
	Metrics    gasometer.MetricsRecorder
	Now        func() time.Time
	Log        *slog.Logger
}

// NewTick collects, forecasts and settles one network. A failed collect skips the rest of the tick.
func NewTick(network string, deps TickDeps) Tick {
	log := deps.Log.With("network", network)
	return func(ctx context.Context) {
		start := deps.Now()
		obs, err := deps.Collector.Collect(ctx)
		if err != nil {
			log.Warn("collect failed; skipping tick", "err", err)
			return
		}
		made, err := deps.Forecaster.Forecast(ctx, obs)
		if err != nil {
			log.Warn("forecast failed", "err", err)
		}
		settled, err := deps.Settler.Settle(ctx, obs.Head.Number)
		if err != nil {
			log.Warn("settle failed", "err", err)
		}
		end := deps.Now()
		deps.Metrics.PollCompleted(network, end.Sub(start), end)
		log.Debug("tick", "block", obs.Head.Number, "predictions", made, "settled", settled)
	}
}

// NewPruneTick drops block rows past retention.
func NewPruneTick(pruner *Pruner, log *slog.Logger) Tick {
	return func(ctx context.Context) {
		n, err := pruner.Prune(ctx)
		if err != nil {
			log.Warn("prune failed", "err", err)
			return
		}
		log.Debug("pruned", "blocks", n)
	}
}

// Pruner drops block rows past retention.
type Pruner struct {
	blocks    gasometer.BlockStore
	retention time.Duration
	now       func() time.Time
}

// NewPruner builds the pruner.
func NewPruner(blocks gasometer.BlockStore, retention time.Duration, now func() time.Time) *Pruner {
	return &Pruner{blocks: blocks, retention: retention, now: now}
}

// Prune deletes blocks observed before now minus retention and returns how many.
func (p *Pruner) Prune(ctx context.Context) (int64, error) {
	n, err := p.blocks.Prune(ctx, p.now().Add(-p.retention))
	if err != nil {
		return 0, fmt.Errorf("prune blocks: %w", err)
	}
	return n, nil
}
