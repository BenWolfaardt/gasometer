// Package scheduler runs gasometer's periodic work.
package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
)

// ShutdownGrace is how long an in-flight tick may run after shutdown begins before it is cancelled.
const ShutdownGrace = 5 * time.Second

// Tick is one round of work.
type Tick func(ctx context.Context)

// Loop runs a tick immediately and then every interval until its context ends.
type Loop struct {
	interval time.Duration
	grace    time.Duration
	tick     Tick
}

// NewLoop builds a loop. A tick is bounded by the interval and survives shutdown for grace.
func NewLoop(interval, grace time.Duration, tick Tick) *Loop {
	return &Loop{interval: interval, grace: grace, tick: tick}
}

// Run blocks until ctx ends and the in-flight tick, if any, has returned.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		l.runTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runTick detaches the tick from ctx so a shutdown lets its writes finish, then cancels it after the grace period.
func (l *Loop) runTick(parent context.Context) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), l.interval)
	defer cancel()
	stop := context.AfterFunc(parent, func() {
		grace := time.NewTimer(l.grace)
		defer grace.Stop()
		select {
		case <-grace.C:
			cancel()
		case <-ctx.Done():
		}
	})
	defer stop()
	l.tick(ctx)
}

// NetworkTickDeps are the use cases one network's tick runs.
type NetworkTickDeps struct {
	Collect usecases.CollectFees
	Predict usecases.Predict
	Score   usecases.ScorePredictions
	Metrics repositories.MetricsRecorder
	Now     func() time.Time
	Log     zerolog.Logger
}

// NewNetworkTick collects, predicts and scores one network. A failed collect skips the rest of the tick.
func NewNetworkTick(network string, deps NetworkTickDeps) Tick {
	log := deps.Log.With().Str("network", network).Logger()
	return func(ctx context.Context) {
		start := deps.Now()
		obs, err := deps.Collect.Execute(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("collect failed; skipping tick")
			return
		}
		made, err := deps.Predict.Execute(ctx, obs)
		if err != nil {
			log.Warn().Err(err).Msg("predict failed")
		}
		settled, err := deps.Score.Execute(ctx, obs.Head.Number)
		if err != nil {
			log.Warn().Err(err).Msg("score failed")
		}
		end := deps.Now()
		deps.Metrics.PollCompleted(network, end.Sub(start), end)
		log.Debug().Uint64("block", obs.Head.Number).Int("predictions", made).Int("settled", settled).Msg("tick")
	}
}

// NewPruneTick drops block rows past retention.
func NewPruneTick(prune usecases.Prune, log zerolog.Logger) Tick {
	return func(ctx context.Context) {
		n, err := prune.Execute(ctx)
		if err != nil {
			log.Warn().Err(err).Msg("prune failed")
			return
		}
		log.Debug().Int64("blocks", n).Msg("pruned")
	}
}
