package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.uber.org/goleak"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/scheduler"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
	"github.com/BenWolfaardt/gasometer/tests/mocks"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestLoopTicksImmediatelyThenOnInterval(t *testing.T) {
	var ticks atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		scheduler.NewLoop(20*time.Millisecond, time.Second, func(context.Context) { ticks.Add(1) }).Run(ctx)
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
	scheduler.NewLoop(time.Hour, time.Second, func(context.Context) { ticks.Add(1) }).Run(ctx)
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
		scheduler.NewLoop(time.Hour, 100*time.Millisecond, tick).Run(ctx)
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
		scheduler.NewLoop(time.Hour, time.Second, tick).Run(ctx)
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
	scheduler.NewLoop(time.Minute, time.Second, tick).Run(ctx)
	if d := <-deadline; d <= 50*time.Second || d > time.Minute {
		t.Fatalf("tick deadline %s, want about the 1m interval", d)
	}
}

type fakeCollect struct {
	obs usecases.Observation
	err error
}

func (f *fakeCollect) Execute(context.Context) (usecases.Observation, error) { return f.obs, f.err }

type fakePredict struct{ calls atomic.Int32 }

func (f *fakePredict) Execute(context.Context, usecases.Observation) (int, error) {
	f.calls.Add(1)
	return 3, errors.New("predictor trouble")
}

type fakeScore struct{ head atomic.Uint64 }

func (f *fakeScore) Execute(_ context.Context, head uint64) (int, error) {
	f.head.Store(head)
	return 1, nil
}

func TestNetworkTick(t *testing.T) {
	collect := &fakeCollect{obs: usecases.Observation{Head: entities.HeadFee{Number: 42}}}
	predict, score, rec := &fakePredict{}, &fakeScore{}, mocks.NewMetricsRecorder()
	tick := scheduler.NewNetworkTick("base", scheduler.NetworkTickDeps{
		Collect: collect, Predict: predict, Score: score, Metrics: rec, Now: time.Now, Log: zerolog.Nop(),
	})

	tick(context.Background())
	if predict.calls.Load() != 1 || score.head.Load() != 42 || rec.PollCount("base") != 1 {
		t.Fatalf("predict %d, score head %d, polls %d", predict.calls.Load(), score.head.Load(), rec.PollCount("base"))
	}

	collect.err = errors.New("rpc down")
	tick(context.Background())
	if predict.calls.Load() != 1 || rec.PollCount("base") != 1 {
		t.Fatal("a failed collect must skip predict, score and the poll metric")
	}
}

type fakePrune struct{ calls atomic.Int32 }

func (f *fakePrune) Execute(context.Context) (int64, error) {
	f.calls.Add(1)
	return 0, nil
}

func TestPruneTick(t *testing.T) {
	prune := &fakePrune{}
	scheduler.NewPruneTick(prune, zerolog.Nop())(context.Background())
	if prune.calls.Load() != 1 {
		t.Fatal("prune not run")
	}
}
