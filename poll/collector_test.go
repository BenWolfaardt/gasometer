package poll_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
)

// A 20% full Base block with denominator 100 and elasticity 5 leaves the base fee of 10,000,000 unchanged.
const modelNext = 10_000_000

type collectFixture struct {
	fees      *mock.FeeSource
	prices    *mock.PriceSource
	l1        *mock.L1FeeSource
	blocks    *mock.BlockStore
	snapshots *mock.SnapshotStore
	metrics   *mock.MetricsRecorder
}

func newCollectFixture(head uint64, next int64) *collectFixture {
	return &collectFixture{
		fees: feeSource(head, next),
		prices: &mock.PriceSource{PriceFn: func() (gasometer.PriceQuote, error) {
			return gasometer.PriceQuote{Answer: wei(300_000_000_000), Decimals: 8, UpdatedAt: t0.Add(-time.Minute)}, nil
		}},
		l1: &mock.L1FeeSource{L1FeesFn: func(txs []gasometer.ReferenceTx) (map[string]*big.Int, error) {
			out := map[string]*big.Int{}
			for _, tx := range txs {
				out[tx.Name] = wei(int64(1_000 + tx.DataSize))
			}
			return out, nil
		}},
		blocks:    mock.NewBlockStore(),
		snapshots: mock.NewSnapshotStore(),
		metrics:   mock.NewMetricsRecorder(),
	}
}

func (f *collectFixture) collector(network gasometer.Network, params poll.CollectorParams) *poll.Collector {
	deps := poll.CollectorDeps{
		Fees: f.fees, Prices: f.prices, Blocks: f.blocks, Snapshots: f.snapshots,
		Metrics: f.metrics, Now: clock(t0), Log: nopLog,
	}
	if network.HasL1Fee() {
		deps.L1Fees = f.l1
	}
	return poll.NewCollector(network, params, refTxs, deps)
}

func TestCollectorFirstRun(t *testing.T) {
	f := newCollectFixture(5_000, modelNext)
	params := collectorParams
	params.InitialBackfill = 100
	obs, err := f.collector(baseNet, params).Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := f.fees.Calls()
	if len(calls) != 1 || calls[0].From != 4_901 || calls[0].To != 5_000 {
		t.Fatalf("fee history calls %+v, want the 100 blocks ending at head", calls)
	}
	if got := calls[0].Percentiles; len(got) != 2 || got[0] != 5 || got[1] != 50 {
		t.Fatalf("percentiles %v", got)
	}
	if n := len(f.blocks.Blocks["base"]); n != 100 {
		t.Fatalf("stored %d blocks", n)
	}
	s := obs.Snapshot
	if s.ID == 0 || s.AtBlock != 5_000 || !eqWei(s.NextBaseFee, modelNext) || !eqWei(s.NodeTip, 1_000) ||
		!s.ObservedAt.Equal(t0) || s.Price == nil || !eqWei(s.L1Fees["erc20"], 1_068) {
		t.Fatalf("snapshot %+v", s)
	}
	if _, ok := f.snapshots.Snapshots[s.ID]; !ok {
		t.Fatal("snapshot not stored")
	}
	if obs.Params == nil || obs.Params.Denominator != 100 || obs.Params.Elasticity != 5 {
		t.Fatalf("params %+v", obs.Params)
	}
	if len(f.metrics.Snapshots) != 1 || f.metrics.Snapshots[0].Price == nil || f.metrics.Snapshots[0].Latest.Number != 5_000 {
		t.Fatalf("metrics %+v", f.metrics.Snapshots)
	}
	if f.metrics.Mismatches["base"] != 0 {
		t.Fatal("unexpected model mismatch")
	}
}

func TestCollectorFetchRange(t *testing.T) {
	tests := []struct {
		name     string
		stored   uint64
		from, to uint64
	}{
		{"continues after the latest stored", 4_997, 4_998, 5_000},
		{"no new blocks refetches head", 5_000, 5_000, 5_000},
		{"long gap asks for everything since", 100, 101, 5_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCollectFixture(5_000, modelNext)
			_ = f.blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{{Number: tt.stored, BaseFee: wei(1)}})
			if _, err := f.collector(baseNet, collectorParams).Collect(t.Context()); err != nil {
				t.Fatal(err)
			}
			calls := f.fees.Calls()
			if len(calls) != 1 || calls[0].From != tt.from || calls[0].To != tt.to {
				t.Fatalf("calls %+v, want %d to %d", calls, tt.from, tt.to)
			}
		})
	}
}

func TestCollectorAcceptsAClampedHistory(t *testing.T) {
	// The source reaches back only to 4,951; the collector stores what it got.
	f := newCollectFixture(5_000, modelNext)
	f.fees.FeeHistoryFn = func(_, to uint64, _ []float64) (gasometer.FeeHistory, error) {
		return syntheticHistory(4_951, to, modelNext), nil
	}
	_ = f.blocks.Upsert(t.Context(), "base", []gasometer.BlockFee{{Number: 100, BaseFee: wei(1)}})
	if _, err := f.collector(baseNet, collectorParams).Collect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.blocks.Blocks["base"]); n != 51 {
		t.Fatalf("stored %d blocks, want the 50 reachable plus the old one", n)
	}
}

func TestCollectorSelfCheck(t *testing.T) {
	t.Run("mismatch is counted and collection continues", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext+1)
		obs, err := f.collector(baseNet, collectorParams).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if f.metrics.Mismatches["base"] != 1 || obs.Params == nil || !eqWei(obs.Snapshot.NextBaseFee, modelNext+1) {
			t.Fatalf("mismatches %d, params %+v", f.metrics.Mismatches["base"], obs.Params)
		}
	})
	t.Run("DA footprint drives the model", func(t *testing.T) {
		// 300M of DA footprint fills the block: +4% on 10,000,000.
		f := newCollectFixture(5_000, 10_400_000)
		head := f.fees.HeadFeeFn
		f.fees.HeadFeeFn = func() (gasometer.HeadFee, error) {
			h, err := head()
			h.BlobGasUsed = 300_000_000
			return h, err
		}
		if _, err := f.collector(baseNet, collectorParams).Collect(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.metrics.Mismatches["base"] != 0 {
			t.Fatal("model ignored the DA footprint")
		}
	})
	t.Run("unknown extraData version leaves params unknown", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext+1)
		f.fees.HeadFeeFn = func() (gasometer.HeadFee, error) {
			return gasometer.HeadFee{Number: 5_000, BaseFee: wei(1), GasLimit: 30_000_000, ExtraData: []byte{0x07}}, nil
		}
		collector := f.collector(baseNet, collectorParams)
		for range 2 {
			obs, err := collector.Collect(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if obs.Params != nil || f.metrics.Mismatches["base"] != 0 {
				t.Fatalf("params %+v, mismatches %d", obs.Params, f.metrics.Mismatches["base"])
			}
		}
	})
	t.Run("avalanche has no model", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext+1)
		cchain := gasometer.Network{Name: "cchain", ChainID: 43114, Kind: gasometer.NetworkKindAvalancheSAE, Coin: "AVAX"}
		obs, err := f.collector(cchain, collectorParams).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Params != nil || f.metrics.Mismatches["cchain"] != 0 || len(obs.Snapshot.L1Fees) != 0 {
			t.Fatalf("params %+v, l1 %v", obs.Params, obs.Snapshot.L1Fees)
		}
	})
}

func TestCollectorDegradedSources(t *testing.T) {
	t.Run("stale price is stored but not exported", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext)
		f.prices.PriceFn = func() (gasometer.PriceQuote, error) {
			return gasometer.PriceQuote{Answer: wei(1), Decimals: 8, UpdatedAt: t0.Add(-2 * time.Hour)}, nil
		}
		obs, err := f.collector(baseNet, collectorParams).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Snapshot.Price == nil || f.metrics.Snapshots[0].Price != nil {
			t.Fatalf("snapshot price %+v, metrics price %+v", obs.Snapshot.Price, f.metrics.Snapshots[0].Price)
		}
	})
	t.Run("price and L1 errors still store a snapshot", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext)
		f.prices.PriceFn = func() (gasometer.PriceQuote, error) { return gasometer.PriceQuote{}, errors.New("feed down") }
		f.l1.L1FeesFn = func([]gasometer.ReferenceTx) (map[string]*big.Int, error) { return nil, errors.New("oracle down") }
		obs, err := f.collector(baseNet, collectorParams).Collect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Snapshot.Price != nil || len(obs.Snapshot.L1Fees) != 0 || len(f.snapshots.Snapshots) != 1 {
			t.Fatalf("snapshot %+v", obs.Snapshot)
		}
	})
}

func TestCollectorFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		breaks func(f *collectFixture)
		want   error
	}{
		{"head", func(f *collectFixture) {
			f.fees.HeadFeeFn = func() (gasometer.HeadFee, error) { return gasometer.HeadFee{}, boom }
		}, boom},
		{"fee history", func(f *collectFixture) {
			f.fees.FeeHistoryFn = func(uint64, uint64, []float64) (gasometer.FeeHistory, error) { return gasometer.FeeHistory{}, boom }
		}, boom},
		{"history not ending at head", func(f *collectFixture) {
			f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
				return syntheticHistory(from, to-1, modelNext), nil
			}
		}, gasometer.ErrMalformedFeeHistory},
		{"history starting before the request", func(f *collectFixture) {
			f.fees.FeeHistoryFn = func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
				return syntheticHistory(from-1, to, modelNext), nil
			}
		}, gasometer.ErrMalformedFeeHistory},
		{"node tip", func(f *collectFixture) {
			f.fees.NodeTipFn = func() (*big.Int, error) { return nil, boom }
		}, boom},
		{"block store", func(f *collectFixture) { f.blocks.Err = boom }, boom},
		{"snapshot store", func(f *collectFixture) { f.snapshots.Err = boom }, boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCollectFixture(5_000, modelNext)
			tt.breaks(f)
			if _, err := f.collector(baseNet, collectorParams).Collect(t.Context()); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if len(f.metrics.Snapshots) != 0 {
				t.Fatal("failed poll exported a snapshot")
			}
		})
	}
}
