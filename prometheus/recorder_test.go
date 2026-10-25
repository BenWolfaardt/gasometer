package prometheus_test

import (
	"math/big"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/prometheus"
)

type gatherer interface {
	Gather() ([]*dto.MetricFamily, error)
}

func newRecorder(t *testing.T) (*prometheus.Recorder, gatherer) {
	t.Helper()
	r := prometheus.NewRecorder()
	return r, r.Gatherer()
}

func family(t *testing.T, reg gatherer, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

// histogram finds the series of name whose labels include every pair in want.
func histogram(t *testing.T, reg gatherer, name string, want map[string]string) *dto.Histogram {
	t.Helper()
	f := family(t, reg, name)
	if f == nil {
		return nil
	}
	for _, m := range f.GetMetric() {
		matched := 0
		for _, l := range m.GetLabel() {
			if v, ok := want[l.GetName()]; ok && v == l.GetValue() {
				matched++
			}
		}
		if matched == len(want) {
			return m.GetHistogram()
		}
	}
	return nil
}

func includedSettlement() gasometer.Settlement {
	ratio := 0.25
	return gasometer.Settlement{
		Network: "base", Predictor: "gasometer", HorizonSeconds: 30, Outcome: gasometer.OutcomeIncluded,
		OverpayRatio: &ratio, EstimateError: 0.05,
		Costs: []gasometer.TxCost{{
			Tx:       "native",
			Paid:     gasometer.CostParts{BaseFee: 3e-4, Tip: 2e-5, L1Fee: 1e-6},
			Required: gasometer.CostParts{BaseFee: 3e-4, Tip: 1e-5, L1Fee: 1e-6},
		}},
	}
}

func TestSettledIncluded(t *testing.T) {
	r, reg := newRecorder(t)
	r.Settled(includedSettlement())
	labels := map[string]string{"network": "base", "predictor": "gasometer", "horizon": "30s", "tx": "native"}

	if got := family(t, reg, "gasometer_predictions_resolved_total").GetMetric()[0].GetCounter().GetValue(); got != 1 {
		t.Fatalf("resolved %v", got)
	}
	for _, c := range []struct {
		name, component string
		sum             float64
	}{
		{"gasometer_paid_usd", "base_fee", 3e-4},
		{"gasometer_paid_usd", "tip", 2e-5},
		{"gasometer_paid_usd", "l1_fee", 1e-6},
		{"gasometer_required_usd", "tip", 1e-5},
	} {
		want := map[string]string{"component": c.component}
		for k, v := range labels {
			want[k] = v
		}
		h := histogram(t, reg, c.name, want)
		if h == nil || h.GetSampleCount() != 1 || h.GetSampleSum() != c.sum {
			t.Fatalf("%s %s: %+v", c.name, c.component, h)
		}
	}
	if h := histogram(t, reg, "gasometer_overpay_usd", labels); h == nil || h.GetSampleSum() != 1e-5 {
		t.Fatalf("overpay %+v", h)
	}
	if h := histogram(t, reg, "gasometer_overpay_ratio", map[string]string{"horizon": "30s"}); h == nil || h.GetSampleSum() != 0.25 {
		t.Fatalf("overpay ratio %+v", h)
	}
	if h := histogram(t, reg, "gasometer_estimate_error_ratio", map[string]string{"horizon": "30s"}); h == nil || h.GetSampleSum() != 0.05 {
		t.Fatalf("estimate error %+v", h)
	}
}

func TestSettledMissedCountsOnly(t *testing.T) {
	r, reg := newRecorder(t)
	r.Settled(gasometer.Settlement{Network: "op", Predictor: "node", HorizonSeconds: 2, Outcome: gasometer.OutcomeMissed})
	f := family(t, reg, "gasometer_predictions_resolved_total")
	if f == nil || len(f.GetMetric()) != 1 {
		t.Fatalf("resolved %+v", f)
	}
	for _, name := range []string{"gasometer_paid_usd", "gasometer_overpay_usd", "gasometer_estimate_error_ratio"} {
		if family(t, reg, name) != nil {
			t.Fatalf("%s recorded for a missed bid", name)
		}
	}
}

func gaugeValue(t *testing.T, reg gatherer, name string) (float64, bool) {
	t.Helper()
	f := family(t, reg, name)
	if f == nil {
		return 0, false
	}
	return f.GetMetric()[0].GetGauge().GetValue(), true
}

func TestSnapshotGauges(t *testing.T) {
	r, reg := newRecorder(t)
	r.Snapshot("base",
		gasometer.FeeSnapshot{NextBaseFee: big.NewInt(5_000_100), NodeTip: big.NewInt(1_000_000), L1Fees: map[string]*big.Int{"native": big.NewInt(491)}},
		gasometer.BlockFee{BaseFee: big.NewInt(5_000_000), RequiredTip: big.NewInt(7)},
		&gasometer.PriceQuote{Answer: big.NewInt(312_345_000_000), Decimals: 8})
	for name, want := range map[string]float64{
		"gasometer_base_fee_wei": 5_000_000, "gasometer_next_base_fee_wei": 5_000_100, "gasometer_node_tip_wei": 1_000_000,
		"gasometer_required_tip_wei": 7, "gasometer_l1_fee_wei": 491, "gasometer_native_usd": 3_123.45,
	} {
		if got, ok := gaugeValue(t, reg, name); !ok || got != want {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

func TestSnapshotWithoutPriceLeavesUSDUnset(t *testing.T) {
	r, reg := newRecorder(t)
	r.Snapshot("cchain", gasometer.FeeSnapshot{NextBaseFee: big.NewInt(1), NodeTip: big.NewInt(1)},
		gasometer.BlockFee{BaseFee: big.NewInt(1), RequiredTip: big.NewInt(0)}, nil)
	if _, ok := gaugeValue(t, reg, "gasometer_native_usd"); ok {
		t.Fatal("native_usd set without a fresh price")
	}
}

func TestInitNetworkExportsZeroMismatches(t *testing.T) {
	r, reg := newRecorder(t)
	r.InitNetwork("base")
	f := family(t, reg, "gasometer_model_mismatch_total")
	if f == nil || len(f.GetMetric()) != 1 || f.GetMetric()[0].GetCounter().GetValue() != 0 {
		t.Fatalf("mismatch counter %+v", f)
	}
}

func TestOperationalMetrics(t *testing.T) {
	r, reg := newRecorder(t)
	r.ModelMismatch("op")
	r.ObserveRPC("op", "eth_feeHistory", "mainnet.optimism.io", "ok", 120*time.Millisecond)
	at := time.Unix(1_800_000_000, 0)
	r.PollCompleted("op", 2*time.Second, at)
	if f := family(t, reg, "gasometer_model_mismatch_total"); f.GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Fatal("mismatch not counted")
	}
	if f := family(t, reg, "gasometer_rpc_requests_total"); f == nil || f.GetMetric()[0].GetCounter().GetValue() != 1 {
		t.Fatal("rpc request not counted")
	}
	if h := histogram(t, reg, "gasometer_rpc_duration_seconds", map[string]string{"method": "eth_feeHistory"}); h.GetSampleSum() != 0.12 {
		t.Fatalf("rpc duration %+v", h)
	}
	if got, _ := gaugeValue(t, reg, "gasometer_last_poll_timestamp_seconds"); got != 1_800_000_000 {
		t.Fatalf("last poll %v", got)
	}
	if h := histogram(t, reg, "gasometer_poll_duration_seconds", map[string]string{"network": "op"}); h.GetSampleSum() != 2 {
		t.Fatalf("poll duration %+v", h)
	}
}

// TestSeriesBudget fills every label combination the default config can produce and keeps the total well under
// Grafana Cloud's 10,000-series free tier.
func TestSeriesBudget(t *testing.T) {
	const budget = 7_000
	r, reg := newRecorder(t)
	networks := []string{"base", "op", "cchain"}
	methods := []string{"eth_feeHistory", "eth_getBlockByNumber", "eth_maxPriorityFeePerGas", "eth_call", "eth_blockNumber"}
	for _, n := range networks {
		for _, p := range []string{"node", "wallet", "gasometer"} {
			for _, h := range []int{2, 30, 120} {
				for _, outcome := range []string{gasometer.OutcomeMissed, gasometer.OutcomeExpired, gasometer.OutcomeUnresolvable} {
					r.Settled(gasometer.Settlement{Network: n, Predictor: p, HorizonSeconds: h, Outcome: outcome})
				}
				s := includedSettlement()
				s.Network, s.Predictor, s.HorizonSeconds = n, p, h
				s.Costs = append(s.Costs, gasometer.TxCost{Tx: "erc20"})
				r.Settled(s)
			}
		}
		r.Snapshot(n, gasometer.FeeSnapshot{NextBaseFee: big.NewInt(1), NodeTip: big.NewInt(1),
			L1Fees: map[string]*big.Int{"native": big.NewInt(1), "erc20": big.NewInt(1)}},
			gasometer.BlockFee{BaseFee: big.NewInt(1), RequiredTip: big.NewInt(1)}, &gasometer.PriceQuote{Answer: big.NewInt(1)})
		r.ModelMismatch(n)
		r.PollCompleted(n, time.Second, time.Now())
		for _, m := range methods {
			for _, e := range []string{"primary", "fallback"} {
				for _, o := range []string{"ok", "retryable_error", "error"} {
					r.ObserveRPC(n, m, e, o, time.Millisecond)
				}
			}
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range families {
		for _, m := range f.GetMetric() {
			if h := m.GetHistogram(); h != nil {
				total += len(h.GetBucket()) + 3 // buckets, +Inf, _sum, _count
				continue
			}
			total++
		}
	}
	t.Logf("%d series at full label coverage", total)
	if total > budget {
		t.Fatalf("%d series exceeds the %d budget", total, budget)
	}
}
