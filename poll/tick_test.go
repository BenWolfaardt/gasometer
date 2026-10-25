package poll_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
	"github.com/BenWolfaardt/gasometer/predict"
)

// pollFixture wires a real collector, forecaster and settler onto mocked ports.
func pollFixture(fees *mock.FeeSource) (poll.Tick, *mock.MetricsRecorder, *mock.SnapshotStore) {
	blocks, snapshots, predictions, rec := mock.NewBlockStore(), mock.NewSnapshotStore(), &mock.PredictionStore{}, mock.NewMetricsRecorder()
	collector := poll.NewCollector(baseNet, collectorParams, refTxs, poll.CollectorDeps{
		Fees: fees, Blocks: blocks, Snapshots: snapshots, Metrics: rec, Now: clock(t0), Log: nopLog,
	})
	forecaster := poll.NewForecaster("base", []int{2}, gasometerParams, poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{predict.NewNode()}, Blocks: blocks, Predictions: predictions, Log: nopLog,
	})
	settler := poll.NewSettler("base", poll.SettlerParams{ExpiryFactor: 10, PriceMaxAge: time.Hour}, refTxs,
		poll.SettlerDeps{Blocks: blocks, Snapshots: snapshots, Predictions: predictions, Metrics: rec, Now: clock(t0), Log: nopLog})
	tick := poll.NewTick("base", poll.TickDeps{
		Collector: collector, Forecaster: forecaster, Settler: settler, Metrics: rec, Now: time.Now, Log: nopLog,
	})
	return tick, rec, snapshots
}

func TestPollTick(t *testing.T) {
	tick, rec, snapshots := pollFixture(feeSource(5_000, 10_000_000))
	tick(context.Background())
	if rec.PollCount("base") != 1 || len(snapshots.Snapshots) != 1 {
		t.Fatalf("polls %d, snapshots %d", rec.PollCount("base"), len(snapshots.Snapshots))
	}
}

func TestPollTickSkipsAfterFailedCollect(t *testing.T) {
	fees := feeSource(5_000, 10_000_000)
	fees.HeadFeeFn = func() (gasometer.HeadFee, error) { return gasometer.HeadFee{}, errors.New("rpc down") }
	tick, rec, snapshots := pollFixture(fees)
	tick(context.Background())
	if rec.PollCount("base") != 0 || len(snapshots.Snapshots) != 0 {
		t.Fatal("a failed collect must skip forecasting, settling and the poll metric")
	}
}

func TestPruneTick(t *testing.T) {
	blocks := mock.NewBlockStore()
	poll.NewPruneTick(poll.NewPruner(blocks, time.Hour, clock(t0)), nopLog)(context.Background())
	if len(blocks.PruneCuts) != 1 || !blocks.PruneCuts[0].Equal(t0.Add(-time.Hour)) {
		t.Fatalf("prune cuts %v", blocks.PruneCuts)
	}
}

func TestPrune(t *testing.T) {
	blocks := mock.NewBlockStore()
	_ = blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{
		{Number: 1, ObservedAt: t0.Add(-49 * time.Hour)}, {Number: 2, ObservedAt: t0.Add(-47 * time.Hour)},
	})
	n, err := poll.NewPruner(blocks, 48*time.Hour, clock(t0)).Prune(t.Context())
	if err != nil || n != 1 || !blocks.PruneCuts[0].Equal(t0.Add(-48*time.Hour)) {
		t.Fatalf("n %d, err %v, cuts %v", n, err, blocks.PruneCuts)
	}
}
