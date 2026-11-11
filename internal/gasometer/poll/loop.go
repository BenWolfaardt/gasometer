package poll

import (
	"context"
	"time"
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
