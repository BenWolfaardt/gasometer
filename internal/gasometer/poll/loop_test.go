package poll_test

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/BenWolfaardt/gasometer/internal/gasometer/poll"
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

// A tick that returns just after its parent is cancelled races runTick's shutdown callback, which context.AfterFunc
// starts on its own goroutine. goleak's retries hide one that outlives Run, so this checks the stacks the moment Run
// returns. A callback parked on its closing line has signalled and is only returning, which no join can rule out; one
// anywhere else has not finished, and Run returned without waiting for it.
func TestRunJoinsShutdownCallback(t *testing.T) {
	const trials = 200
	done := callbackClosingLine(t)
	frame := regexp.MustCompile(`poll\.\(\*Loop\)\.runTick\.func1\(\)\n\t[^\n]*/loop\.go:(\d+)`)
	stacks := make([]byte, 1<<20)
	for trial := range trials {
		ctx, cancel := context.WithCancel(context.Background())
		poll.NewLoop(time.Hour, time.Hour, func(context.Context) { cancel() }).Run(ctx)
		n := runtime.Stack(stacks, true)
		for _, m := range frame.FindAllSubmatch(stacks[:n], -1) {
			if line, _ := strconv.Atoi(string(m[1])); line != done {
				t.Fatalf("trial %d: a shutdown callback at loop.go:%d, not its closing line %d, outlived Run:\n%s",
					trial, line, done, stacks[:n])
			}
		}
	}
}

// callbackClosingLine is the line of loop.go that closes the function passed to context.AfterFunc in runTick.
func callbackClosingLine(t *testing.T) int {
	t.Helper()
	src, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		if !strings.Contains(l, "context.AfterFunc(parent, func() {") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "\t})" {
				return j + 1
			}
		}
	}
	t.Fatal("loop.go has no AfterFunc callback in runTick")
	return 0
}
