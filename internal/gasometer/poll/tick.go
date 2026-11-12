package poll

import (
	"context"
	"log/slog"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
)

// TickDeps are the steps one network's poll runs.
type TickDeps struct {
	Collector  *Collector
	Forecaster *Forecaster
	Settler    *Settler
	Backfiller *Backfiller
	Metrics    gasometer.MetricsRecorder
	Now        func() time.Time
	Log        *slog.Logger
}

// NewTick collects, forecasts and settles one network, then backfills with whatever is left of the tick. A failed
// collect skips the rest of the tick. The poll duration excludes the backfill, which may run to the deadline.
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
		backfilled, err := deps.Backfiller.Backfill(ctx)
		if err != nil {
			log.Warn("backfill failed; retrying next tick", "err", err)
		}
		log.Debug("tick", "block", obs.Head.Number, "predictions", made, "settled", settled, "backfilled", backfilled)
	}
}

// NewPruneTick deletes the blocks of every network observed more than retention before now.
func NewPruneTick(blocks gasometer.BlockStore, retention time.Duration, now func() time.Time, log *slog.Logger) Tick {
	return func(ctx context.Context) {
		n, err := blocks.Prune(ctx, now().Add(-retention))
		if err != nil {
			log.Warn("prune failed", "err", err)
			return
		}
		log.Debug("pruned", "blocks", n)
	}
}
