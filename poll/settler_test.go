package poll_test

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"

	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
)

type scoreFixture struct {
	blocks      *mock.BlockStore
	snapshots   *mock.SnapshotStore
	predictions *mock.PredictionStore
	metrics     *mock.MetricsRecorder
}

func newScoreFixture(t *testing.T, price gasometer.PriceQuote) *scoreFixture {
	f := &scoreFixture{
		blocks: mock.NewBlockStore(), snapshots: mock.NewSnapshotStore(),
		predictions: &mock.PredictionStore{}, metrics: mock.NewMetricsRecorder(),
	}
	// Blocks 101..110 at base fee 100 needing a tip of 5.
	var blocks []gasometer.BlockFee
	for n := uint64(101); n <= 110; n++ {
		blocks = append(blocks, gasometer.BlockFee{Number: n, BaseFee: wei(100), RequiredTip: wei(5), ObservedAt: t0})
	}
	_ = f.blocks.Upsert(t.Context(), "base", blocks)
	_, _ = f.snapshots.Insert(t.Context(), gasometer.FeeSnapshot{
		Network: "base", AtBlock: 100, ObservedAt: t0, Price: &price,
		L1Fees: map[string]*big.Int{"native": wei(1e12)},
	})
	return f
}

func (f *scoreFixture) add(t *testing.T, predictor string, seconds, blocks int, atBlock uint64, tip, maxFee int64) {
	err := f.predictions.Insert(t.Context(), []gasometer.Prediction{{
		SnapshotID: 1, Network: "base", Predictor: predictor, AtBlock: atBlock, MadeAt: t0,
		Horizon: gasometer.Horizon{Seconds: seconds, Blocks: blocks},
		Bid:     gasometer.Bid{Tip: wei(tip), MaxFee: wei(maxFee), ExpectedBaseFee: wei(100)},
		Status:  gasometer.PredictionPending,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *scoreFixture) scorer(now time.Time) *poll.Settler {
	params := poll.SettlerParams{ExpiryFactor: 10, PriceMaxAge: time.Hour}
	return poll.NewSettler("base", params, refTxs[:1], poll.SettlerDeps{
		Blocks: f.blocks, Snapshots: f.snapshots, Predictions: f.predictions, Metrics: f.metrics,
		Now: clock(now), Log: nopLog,
	})
}

func freshPrice() gasometer.PriceQuote {
	return gasometer.PriceQuote{Answer: wei(2_000_00000000), Decimals: 8, UpdatedAt: t0}
}

func TestSettlerOutcomes(t *testing.T) {
	f := newScoreFixture(t, freshPrice())
	f.add(t, "included", 30, 5, 100, 10, 200)
	f.add(t, "missed", 30, 5, 100, 4, 200)
	f.add(t, "pending", 30, 20, 100, 10, 200)
	f.add(t, "expired", 2, 20, 100, 10, 200)
	f.add(t, "gap", 30, 5, 107, 10, 200)

	// Head 112, a minute on. Stored blocks stop at 110, so the window 108..112 has a gap. The 2 s prediction is past
	// 10 × its horizon; the 30 s one ending at 120 is not.
	n, err := f.scorer(t0.Add(time.Minute)).Settle(t.Context(), 112)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]gasometer.PredictionStatus{}
	for _, p := range f.predictions.Predictions {
		status[p.Predictor] = p.Status
	}
	want := map[string]gasometer.PredictionStatus{
		"included": gasometer.PredictionResolved,
		"missed":   gasometer.PredictionResolved,
		"pending":  gasometer.PredictionPending,
		"expired":  gasometer.PredictionExpired,
		"gap":      gasometer.PredictionUnresolvable,
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

func TestSettlerCosts(t *testing.T) {
	f := newScoreFixture(t, freshPrice())
	f.add(t, "gasometer", 30, 5, 100, 10, 200)
	if _, err := f.scorer(t0).Settle(t.Context(), 110); err != nil {
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

func TestSettlerStalePriceSkipsCosts(t *testing.T) {
	stale := freshPrice()
	stale.UpdatedAt = t0.Add(-2 * time.Hour)
	f := newScoreFixture(t, stale)
	f.add(t, "gasometer", 30, 5, 100, 10, 200)
	if _, err := f.scorer(t0).Settle(t.Context(), 110); err != nil {
		t.Fatal(err)
	}
	if s := f.metrics.Settlements[0]; s.Outcome != "included" || s.Costs != nil || s.OverpayRatio == nil {
		t.Fatalf("settlement %+v", s)
	}
}

func TestSettlerErrors(t *testing.T) {
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
			if _, err := f.scorer(t0).Settle(t.Context(), 110); !errors.Is(err, boom) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
