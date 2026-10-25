package poll_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/BenWolfaardt/gasometer/poll"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestLoopTicksImmediatelyThenOnInterval(t *testing.T) {
	var ticks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poll.NewLoop(20*time.Millisecond, time.Second, func(context.Context) { ticks.Add(1) }).Run(ctx)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for ticks.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d ticks", ticks.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestLoopDoesNotTickAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ticks atomic.Int32
	poll.NewLoop(time.Hour, time.Second, func(context.Context) { ticks.Add(1) }).Run(ctx)
	if ticks.Load() != 0 {
		t.Fatalf("%d ticks after cancel", ticks.Load())
	}
}

func TestLoopGivesInFlightTickAGracePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var cancelledAfter atomic.Int64
	tick := func(tickCtx context.Context) {
		close(started)
		begin := time.Now()
		<-tickCtx.Done()
		cancelledAfter.Store(int64(time.Since(begin)))
	}
	done := make(chan struct{})
	go func() {
		poll.NewLoop(time.Hour, 100*time.Millisecond, tick).Run(ctx)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop after the grace period")
	}
	if got := time.Duration(cancelledAfter.Load()); got < 100*time.Millisecond {
		t.Fatalf("tick cancelled after %s, before the grace period", got)
	}
}

func TestLoopTickLetsQuickWritesFinish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var finished atomic.Bool
	started := make(chan struct{})
	tick := func(tickCtx context.Context) {
		close(started)
		select {
		case <-time.After(30 * time.Millisecond):
			finished.Store(tickCtx.Err() == nil)
		case <-tickCtx.Done():
		}
	}
	done := make(chan struct{})
	go func() {
		poll.NewLoop(time.Hour, time.Second, tick).Run(ctx)
		close(done)
	}()
	<-started
	cancel()
	<-done
	if !finished.Load() {
		t.Fatal("in-flight tick was cancelled before it could finish")
	}
}

func TestLoopBoundsTickByInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadline := make(chan time.Duration, 1)
	tick := func(tickCtx context.Context) {
		d, _ := tickCtx.Deadline()
		select {
		case deadline <- time.Until(d):
		default:
		}
		cancel()
	}
	poll.NewLoop(time.Minute, time.Second, tick).Run(ctx)
	if d := <-deadline; d <= 50*time.Second || d > time.Minute {
		t.Fatalf("tick deadline %s, want about the 1m interval", d)
	}
}
