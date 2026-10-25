package usecases_test

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
	"github.com/BenWolfaardt/gasometer/tests/mocks"
)

type scoreFixture struct {
	blocks      *mocks.BlockStore
	snapshots   *mocks.SnapshotStore
	predictions *mocks.PredictionStore
	metrics     *mocks.MetricsRecorder
}

func newScoreFixture(t *testing.T, price entities.PriceQuote) *scoreFixture {
	f := &scoreFixture{
		blocks: mocks.NewBlockStore(), snapshots: mocks.NewSnapshotStore(),
		predictions: &mocks.PredictionStore{}, metrics: mocks.NewMetricsRecorder(),
	}
	// Blocks 101..110 at base fee 100 needing a tip of 5.
	var blocks []entities.BlockFee
	for n := uint64(101); n <= 110; n++ {
		blocks = append(blocks, entities.BlockFee{Number: n, BaseFee: wei(100), RequiredTip: wei(5), ObservedAt: t0})
	}
	_ = f.blocks.Upsert(t.Context(), "base", blocks)
	_, _ = f.snapshots.Insert(t.Context(), entities.FeeSnapshot{
		Network: "base", AtBlock: 100, ObservedAt: t0, Price: &price,
		L1Fees: map[string]*big.Int{"native": wei(1e12)},
	})
	return f
}

func (f *scoreFixture) add(t *testing.T, predictor string, seconds, blocks int, atBlock uint64, tip, maxFee int64) {
	err := f.predictions.Insert(t.Context(), []entities.Prediction{{
		SnapshotID: 1, Network: "base", Predictor: predictor, AtBlock: atBlock, MadeAt: t0,
		Horizon: entities.Horizon{Seconds: seconds, Blocks: blocks},
		Bid:     entities.Bid{Tip: wei(tip), MaxFee: wei(maxFee), ExpectedBaseFee: wei(100)},
		Status:  entities.PredictionPending,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *scoreFixture) scorer(now time.Time) usecases.ScorePredictions {
	return usecases.NewScorePredictions(baseNet, 10, refTxs[:1], usecases.ScoreDeps{
		Blocks: f.blocks, Snapshots: f.snapshots, Predictions: f.predictions, Metrics: f.metrics,
		Now: clock(now), Log: nopLog,
	})
}

func freshPrice() entities.PriceQuote {
	return entities.PriceQuote{Answer: wei(2_000_00000000), Decimals: 8, UpdatedAt: t0}
}

func TestScorePredictionsOutcomes(t *testing.T) {
	f := newScoreFixture(t, freshPrice())
	f.add(t, "included", 30, 5, 100, 10, 200)
	f.add(t, "missed", 30, 5, 100, 4, 200)
	f.add(t, "pending", 30, 20, 100, 10, 200)
	f.add(t, "expired", 2, 20, 100, 10, 200)
	f.add(t, "gap", 30, 5, 107, 10, 200)

	// Head 112, a minute on. Stored blocks stop at 110, so the window 108..112 has a gap. The 2 s prediction is past
	// 10 × its horizon; the 30 s one ending at 120 is not.
	n, err := f.scorer(t0.Add(time.Minute)).Execute(t.Context(), 112)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]entities.PredictionStatus{}
	for _, p := range f.predictions.Predictions {
		status[p.Predictor] = p.Status
	}
	want := map[string]entities.PredictionStatus{
		"included": entities.PredictionResolved,
		"missed":   entities.PredictionResolved,
		"pending":  entities.PredictionPending,
		"expired":  entities.PredictionExpired,
		"gap":      entities.PredictionUnresolvable,
	}
	for k, w := range want {
		if status[k] != w {
			t.Fatalf("%s: %s, want %s", k, status[k], w)
		}
	}
	if n != 4 || len(f.metrics.Settlements) != 4 {
		t.Fatalf("settled %d, recorded %d", n, len(f.metrics.Settlements))
	}
	outcomes := map[string]string{}
	for _, s := range f.metrics.Settlements {
		outcomes[s.Predictor] = s.Outcome
	}
	if outcomes["included"] != "included" || outcomes["missed"] != "missed" || outcomes["expired"] != "expired" ||
		outcomes["gap"] != "unresolvable" {
		t.Fatalf("outcomes %v", outcomes)
	}
}

func TestScorePredictionsCosts(t *testing.T) {
	f := newScoreFixture(t, freshPrice())
	f.add(t, "gasometer", 30, 5, 100, 10, 200)
	if _, err := f.scorer(t0).Execute(t.Context(), 110); err != nil {
		t.Fatal(err)
	}
	s := f.metrics.Settlements[0]
	// Paid 100 + 10, required 100 + 5, per gas; 21,000 gas at 2,000 USD per 1e18 wei.
	usd := func(perGas float64) float64 { return perGas * 21_000 * 2_000 / 1e18 }
	if len(s.Costs) != 1 || s.OverpayRatio == nil || math.Abs(*s.OverpayRatio-5.0/105) > 1e-12 {
		t.Fatalf("settlement %+v", s)
	}
	c := s.Costs[0]
	close := func(a, b float64) bool { return math.Abs(a-b) < 1e-24 }
	if c.Tx != "native" || !close(c.Paid.BaseFee, usd(100)) || !close(c.Paid.Tip, usd(10)) ||
		!close(c.Required.Tip, usd(5)) || !close(c.Paid.L1Fee, 1e12*2_000/1e18) || !close(c.Overpay(), usd(5)) {
		t.Fatalf("costs %+v", c)
	}
}

func TestScorePredictionsStalePriceSkipsCosts(t *testing.T) {
	stale := freshPrice()
	stale.UpdatedAt = t0.Add(-2 * time.Hour)
	f := newScoreFixture(t, stale)
	f.add(t, "gasometer", 30, 5, 100, 10, 200)
	if _, err := f.scorer(t0).Execute(t.Context(), 110); err != nil {
		t.Fatal(err)
	}
	if s := f.metrics.Settlements[0]; s.Outcome != "included" || s.Costs != nil || s.OverpayRatio == nil {
		t.Fatalf("settlement %+v", s)
	}
}

func TestScorePredictionsErrors(t *testing.T) {
	boom := errors.New("boom")
	for name, breakIt := range map[string]func(f *scoreFixture){
		"pending":   func(f *scoreFixture) { f.predictions.Err = boom },
		"blocks":    func(f *scoreFixture) { f.blocks.Err = boom },
		"snapshots": func(f *scoreFixture) { f.snapshots.Err = boom },
	} {
		t.Run(name, func(t *testing.T) {
			f := newScoreFixture(t, freshPrice())
			f.add(t, "gasometer", 30, 5, 100, 10, 200)
			breakIt(f)
			if _, err := f.scorer(t0).Execute(t.Context(), 110); !errors.Is(err, boom) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPrune(t *testing.T) {
	blocks := mocks.NewBlockStore()
	_ = blocks.Upsert(t.Context(), "base", []entities.BlockFee{
		{Number: 1, ObservedAt: t0.Add(-49 * time.Hour)}, {Number: 2, ObservedAt: t0.Add(-47 * time.Hour)},
	})
	n, err := usecases.NewPrune(blocks, 48*time.Hour, clock(t0)).Execute(t.Context())
	if err != nil || n != 1 || !blocks.PruneCuts[0].Equal(t0.Add(-48*time.Hour)) {
		t.Fatalf("n %d, err %v, cuts %v", n, err, blocks.PruneCuts)
	}
}
