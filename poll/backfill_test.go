package poll_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
)

// recovered is when the poll after the outage stored the blocks above the hole; the backfill runs later still.
var recovered, backfilling = t0.Add(time.Hour), t0.Add(2 * time.Hour)

// backfillFixture stores 100 to 110 at t0 and 200 to 210 at recovered on base, leaving the hole 111 to 199.
type backfillFixture struct {
	fees    *mock.FeeSource
	blocks  *mock.BlockStore
	metrics *mock.MetricsRecorder
}

func newBackfillFixture(t *testing.T) *backfillFixture {
	t.Helper()
	f := &backfillFixture{fees: feeSource(5_000, modelNext), blocks: mock.NewBlockStore(), metrics: mock.NewMetricsRecorder()}
	storeBlocks(t, f.blocks, 100, 110)
	var above []gasometer.BlockFee
	for n := uint64(200); n <= 210; n++ {
		above = append(above, gasometer.BlockFee{Number: n, BaseFee: wei(10_000_000), RequiredTip: wei(1), ObservedAt: recovered})
	}
	if err := f.blocks.Upsert(t.Context(), "base", above); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *backfillFixture) backfiller() *poll.Backfiller {
	return poll.NewBackfiller("base", poll.BackfillerParams{RequiredTipPercentile: 5, Chunk: 30}, poll.BackfillerDeps{
		Fees: f.fees, Blocks: f.blocks, Metrics: f.metrics, Now: clock(backfilling), Log: nopLog,
	})
}

// servedFrom makes the source serve nothing below lowest: a range wholly below it is beyond history, and a range
// reaching it is clamped.
func (f *backfillFixture) servedFrom(lowest uint64) {
	f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
		if to < lowest {
			return gasometer.FeeHistory{}, fmt.Errorf("%w: %d to %d", gasometer.ErrBeyondHistory, from, to)
		}
		return syntheticHistory(max(from, lowest), to, modelNext), nil
	}
}

func (f *backfillFixture) stored(from, to uint64) bool {
	for n := from; n <= to; n++ {
		if _, ok := f.blocks.Blocks["base"][n]; !ok {
			return false
		}
	}
	return true
}

func callRanges(calls []mock.FeeHistoryCall) [][2]uint64 {
	out := make([][2]uint64, len(calls))
	for i, c := range calls {
		out[i] = [2]uint64{c.From, c.To}
	}
	return out
}

func TestBackfillFillsAHoleAcrossChunks(t *testing.T) {
	f := newBackfillFixture(t)
	n, err := f.backfiller().Backfill(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]uint64{{111, 140}, {141, 170}, {171, 199}}
	if got := callRanges(f.fees.Calls()); !slices.Equal(got, want) {
		t.Fatalf("calls %v, want %v", got, want)
	}
	if n != 89 || !f.stored(100, 210) || f.metrics.Backfilled["base"] != 89 || f.metrics.Lost["base"] != 0 {
		t.Fatalf("stored %d, backfilled %d, lost %d", n, f.metrics.Backfilled["base"], f.metrics.Lost["base"])
	}
	if got := f.blocks.Blocks["base"][150].ObservedAt; !got.Equal(recovered) {
		t.Fatalf("backfilled block observed at %s, want %s: the block above the hole", got, recovered)
	}
}

func TestBackfilledBlocksPruneFromTheBottom(t *testing.T) {
	f := newBackfillFixture(t)
	if _, err := f.backfiller().Backfill(t.Context()); err != nil {
		t.Fatal(err)
	}
	var later []gasometer.BlockFee
	for n := uint64(211); n <= 220; n++ {
		later = append(later, gasometer.BlockFee{Number: n, BaseFee: wei(10_000_000), ObservedAt: backfilling.Add(time.Hour)})
	}
	if err := f.blocks.Upsert(t.Context(), "base", later); err != nil {
		t.Fatal(err)
	}
	// A cutoff between the recovery poll and the backfill must not keep backfilled blocks below pruned ones.
	if _, err := f.blocks.Prune(t.Context(), recovered.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	holes, err := f.blocks.Holes(t.Context(), "base", 0)
	if err != nil || len(holes) != 0 || !f.stored(211, 220) {
		t.Fatalf("holes %v, err %v after prune", holes, err)
	}
}

func TestBackfillStopsAtDeadlineAndResumes(t *testing.T) {
	f := newBackfillFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
		if from == 141 {
			cancel()
			return gasometer.FeeHistory{}, context.DeadlineExceeded
		}
		return syntheticHistory(from, to, modelNext), nil
	}
	b := f.backfiller()
	n, err := b.Backfill(ctx)
	if err != nil || n != 30 || !f.stored(111, 140) || f.stored(141, 141) {
		t.Fatalf("first pass stored %d, err %v; want the first chunk kept and no error", n, err)
	}

	f.fees.FeeHistoryFn = feeSource(5_000, modelNext).FeeHistoryFn
	if _, err := b.Backfill(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := callRanges(f.fees.Calls()[2:]); !slices.Equal(got, [][2]uint64{{141, 170}, {171, 199}}) {
		t.Fatalf("resumed with calls %v", got)
	}
	if !f.stored(100, 210) || f.metrics.Backfilled["base"] != 89 {
		t.Fatalf("backfilled %d", f.metrics.Backfilled["base"])
	}
	if want := []uint64{0, 140}; !slices.Equal(f.blocks.HolesAbove, want) {
		t.Fatalf("holes looked for above %v, want %v: the resumed pass starts after the stored chunk", f.blocks.HolesAbove, want)
	}
}

func TestBackfillWarnsWhenTheDeadlineLeavesNoProgress(t *testing.T) {
	for _, tt := range []struct {
		name     string
		failFrom uint64
		warn     bool
	}{
		{"nothing stored", 111, true},
		{"one chunk stored", 141, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newBackfillFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
				if from == tt.failFrom {
					cancel()
					return gasometer.FeeHistory{}, errors.New("eth_feeHistory on mainnet.base.org: 503")
				}
				return syntheticHistory(from, to, modelNext), nil
			}
			var logs bytes.Buffer
			b := poll.NewBackfiller("base", poll.BackfillerParams{RequiredTipPercentile: 5, Chunk: 30}, poll.BackfillerDeps{
				Fees: f.fees, Blocks: f.blocks, Metrics: f.metrics, Now: clock(backfilling), Log: slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if _, err := b.Backfill(ctx); err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(logs.String(), "level=WARN") && strings.Contains(logs.String(), "503")
			if warned != tt.warn {
				t.Fatalf("warned %v, want %v: %s", warned, tt.warn, logs.String())
			}
		})
	}
}

func TestBackfillFillsEveryHoleInAPass(t *testing.T) {
	// 111 to 199 is wholly past depth; 211 to 299 is served.
	f := newBackfillFixture(t)
	storeBlocks(t, f.blocks, 300, 310)
	f.servedFrom(211)
	if _, err := f.backfiller().Backfill(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.metrics.Lost["base"] != 89 || !f.stored(200, 310) || f.metrics.Backfilled["base"] != 89 {
		t.Fatalf("lost %d, backfilled %d", f.metrics.Lost["base"], f.metrics.Backfilled["base"])
	}
}

func TestBackfillUpsertErrorEndsThePass(t *testing.T) {
	f := newBackfillFixture(t)
	f.blocks.UpsertErr = errors.New("db down")
	b := f.backfiller()
	if _, err := b.Backfill(t.Context()); err == nil {
		t.Fatal("an upsert error must end the pass with an error")
	}
	if f.metrics.Backfilled["base"] != 0 || len(f.fees.Calls()) != 1 {
		t.Fatalf("backfilled %d after %d calls", f.metrics.Backfilled["base"], len(f.fees.Calls()))
	}
	f.blocks.UpsertErr = nil
	if _, err := b.Backfill(t.Context()); err != nil || !f.stored(100, 210) {
		t.Fatalf("retry: err %v", err)
	}
}

func TestBackfillCountsLostBlocksOnce(t *testing.T) {
	t.Run("wholly and partly beyond history in one hole", func(t *testing.T) {
		f := newBackfillFixture(t)
		f.servedFrom(150)
		b := f.backfiller()
		for range 3 {
			if _, err := b.Backfill(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if f.metrics.Lost["base"] != 39 || !f.stored(150, 210) || f.metrics.Backfilled["base"] != 50 {
			t.Fatalf("lost %d, backfilled %d; want 111 to 149 lost once and 150 to 199 stored",
				f.metrics.Lost["base"], f.metrics.Backfilled["base"])
		}
	})
	t.Run("deadline after a lost step", func(t *testing.T) {
		f := newBackfillFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
			switch {
			case to < 171:
				return gasometer.FeeHistory{}, gasometer.ErrBeyondHistory
			case ctx.Err() == nil:
				cancel()
				return gasometer.FeeHistory{}, context.Canceled
			}
			return syntheticHistory(from, to, modelNext), nil
		}
		b := f.backfiller()
		if _, err := b.Backfill(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Backfill(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.metrics.Lost["base"] != 60 || !f.stored(171, 199) {
			t.Fatalf("lost %d; want 111 to 170 counted once and 171 to 199 filled on the next pass", f.metrics.Lost["base"])
		}
	})
}

func TestBackfillCursor(t *testing.T) {
	f := newBackfillFixture(t)
	fill := f.fees.FeeHistoryFn
	f.fees.FeeHistoryFn = func(uint64, uint64, []float64) (gasometer.FeeHistory, error) {
		return gasometer.FeeHistory{}, errors.New("rpc down")
	}
	b := f.backfiller()
	if _, err := b.Backfill(t.Context()); err == nil {
		t.Fatal("an RPC error must end the pass with an error")
	}
	f.fees.FeeHistoryFn = fill
	for range 2 {
		if _, err := b.Backfill(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if want := []uint64{0, 110, 210}; !slices.Equal(f.blocks.HolesAbove, want) {
		t.Fatalf("holes looked for above %v, want %v: an RPC error leaves the cursor before the failing hole", f.blocks.HolesAbove, want)
	}
	if got := b.Covered(); got != 210 {
		t.Fatalf("covered %d, want the latest stored block 210", got)
	}
}

func TestBackfillStoreErrorEndsThePass(t *testing.T) {
	f := newBackfillFixture(t)
	f.blocks.Err = errors.New("db down")
	if _, err := f.backfiller().Backfill(t.Context()); err == nil {
		t.Fatal("a store error must end the pass with an error")
	}
}
