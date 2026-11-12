package poll_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/poll"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/predict"
	"github.com/BenWolfaardt/gasometer/internal/mock"
)

// pollFixture wires a real collector, forecaster, settler and backfiller onto mocked ports.
func pollFixture(fees *mock.FeeSource, blocks *mock.BlockStore) (poll.Tick, *mock.MetricsRecorder, *mock.SnapshotStore) {
	snapshots, predictions, rec := mock.NewSnapshotStore(), &mock.PredictionStore{}, mock.NewMetricsRecorder()
	collector := poll.NewCollector(baseNet, collectorParams, refTxs, poll.CollectorDeps{
		Fees: fees, Blocks: blocks, Snapshots: snapshots, Metrics: rec, Now: clock(t0), Log: nopLog,
	})
	forecaster := poll.NewForecaster("base", forecasterParams(2), poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{predict.NewNode()}, Blocks: blocks, Predictions: predictions, Log: nopLog,
	})
	backfiller := poll.NewBackfiller("base", poll.BackfillerParams{RequiredTipPercentile: 5, Chunk: 1024}, poll.BackfillerDeps{
		Fees: fees, Blocks: blocks, Metrics: rec, Now: clock(t0), Log: nopLog,
	})
	settler := poll.NewSettler("base", poll.SettlerParams{ExpiryFactor: 10, PriceMaxAge: time.Hour}, refTxs,
		poll.SettlerDeps{Blocks: blocks, Snapshots: snapshots, Predictions: predictions, Metrics: rec,
			Covered: backfiller.Covered, Now: clock(t0), Log: nopLog})
	tick := poll.NewTick("base", poll.TickDeps{
		Collector: collector, Forecaster: forecaster, Settler: settler, Backfiller: backfiller,
		Metrics: rec, Now: time.Now, Log: nopLog,
	})
	return tick, rec, snapshots
}

func TestPollTick(t *testing.T) {
	tick, rec, snapshots := pollFixture(feeSource(5_000, 10_000_000), mock.NewBlockStore())
	tick(context.Background())
	if rec.PollCount("base") != 1 || len(snapshots.Snapshots) != 1 {
		t.Fatalf("polls %d, snapshots %d", rec.PollCount("base"), len(snapshots.Snapshots))
	}
}

func TestPollTickBackfillsAfterTheLiveChunk(t *testing.T) {
	// 2,000 blocks since the last stored: the live chunk ends at head, the rest is backfilled last.
	blocks := mock.NewBlockStore()
	_ = blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{{Number: 3_000, BaseFee: wei(1), ObservedAt: t0}})
	fees := feeSource(5_000, 10_000_000)
	tick, rec, snapshots := pollFixture(fees, blocks)
	live := fees.FeeHistoryFn
	pollsAtBackfill := -1
	fees.FeeHistoryFn = func(from, to uint64, p []float64) (gasometer.FeeHistory, error) {
		if to < 5_000 {
			pollsAtBackfill = rec.PollCount("base")
		}
		return live(from, to, p)
	}
	tick(context.Background())
	if pollsAtBackfill != 1 {
		t.Fatalf("polls completed when backfill began: %d; backfill must follow forecast, settle and the poll metric", pollsAtBackfill)
	}
	want := [][2]uint64{{3_977, 5_000}, {3_001, 3_976}}
	if got := callRanges(fees.Calls()); len(got) < 2 || got[0] != want[0] || got[len(got)-1] != want[1] {
		t.Fatalf("calls %v; want the live chunk %v first and the backfill %v last", got, want[0], want[1])
	}
	if len(blocks.Blocks["base"]) != 2_001 || rec.Backfilled["base"] != 976 || len(snapshots.Snapshots) != 1 {
		t.Fatalf("stored %d, backfilled %d", len(blocks.Blocks["base"]), rec.Backfilled["base"])
	}
}

func TestPollTickSkipsAfterFailedCollect(t *testing.T) {
	fees := feeSource(5_000, 10_000_000)
	fees.HeadFeeFn = func() (gasometer.HeadFee, error) { return gasometer.HeadFee{}, errors.New("rpc down") }
	blocks := mock.NewBlockStore()
	_ = blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{{Number: 100, BaseFee: wei(1)}, {Number: 200, BaseFee: wei(1)}})
	tick, rec, snapshots := pollFixture(fees, blocks)
	tick(context.Background())
	if rec.PollCount("base") != 0 || len(snapshots.Snapshots) != 0 || len(fees.Calls()) != 0 {
		t.Fatal("a failed collect must skip forecasting, settling, backfilling and the poll metric")
	}
}

func TestPruneTickCutsAtRetention(t *testing.T) {
	blocks := mock.NewBlockStore()
	poll.NewPruneTick(blocks, time.Hour, clock(t0), nopLog)(context.Background())
	if len(blocks.PruneCuts) != 1 || !blocks.PruneCuts[0].Equal(t0.Add(-time.Hour)) {
		t.Fatalf("prune cuts %v", blocks.PruneCuts)
	}
}

func TestPruneTickLogsTheCount(t *testing.T) {
	blocks := mock.NewBlockStore()
	_ = blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{
		{Number: 1, ObservedAt: t0.Add(-49 * time.Hour)}, {Number: 2, ObservedAt: t0.Add(-47 * time.Hour)},
	})
	var logged bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	poll.NewPruneTick(blocks, 48*time.Hour, clock(t0), log)(t.Context())
	if !strings.Contains(logged.String(), "msg=pruned blocks=1") {
		t.Fatalf("log %q", logged.String())
	}
	if got, _ := blocks.Range(t.Context(), "base", 0, 10); len(got) != 1 || got[0].Number != 2 {
		t.Fatalf("left %+v", got)
	}
}
