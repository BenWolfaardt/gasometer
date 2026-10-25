package usecases_test

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
	"github.com/BenWolfaardt/gasometer/tests/mocks"
)

// A 20% full Base block at 10 gwei-ish with denominator 100 and elasticity 5 leaves the base fee unchanged.
const modelNext = 10_000_000

type collectFixture struct {
	fees      *mocks.FeeSource
	prices    *mocks.PriceSource
	l1        *mocks.L1FeeSource
	blocks    *mocks.BlockStore
	snapshots *mocks.SnapshotStore
	metrics   *mocks.MetricsRecorder
}

func newCollectFixture(head uint64, next int64) *collectFixture {
	return &collectFixture{
		fees: feeSource(head, next),
		prices: &mocks.PriceSource{PriceFn: func() (entities.PriceQuote, error) {
			return entities.PriceQuote{Answer: wei(300_000_000_000), Decimals: 8, UpdatedAt: t0.Add(-time.Minute)}, nil
		}},
		l1: &mocks.L1FeeSource{L1FeesFn: func(txs []entities.ReferenceTx) (map[string]*big.Int, error) {
			out := map[string]*big.Int{}
			for _, tx := range txs {
				out[tx.Name] = wei(int64(1_000 + tx.DataSize))
			}
			return out, nil
		}},
		blocks:    mocks.NewBlockStore(),
		snapshots: mocks.NewSnapshotStore(),
		metrics:   mocks.NewMetricsRecorder(),
	}
}

func (f *collectFixture) collector(network entities.NetworkConfig) usecases.CollectFees {
	return usecases.NewCollectFees(network, refTxs, usecases.CollectDeps{
		Fees: f.fees, Prices: f.prices, L1Fees: f.l1, Blocks: f.blocks, Snapshots: f.snapshots,
		Metrics: f.metrics, Now: clock(t0), Log: nopLog,
	})
}

func TestCollectFeesFirstRun(t *testing.T) {
	f := newCollectFixture(5_000, modelNext)
	network := baseNet
	network.FeeHistoryBatch = 100
	obs, err := f.collector(network).Execute(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := f.fees.Calls()
	if len(calls) != 1 || calls[0].BlockCount != 100 || calls[0].Newest != 5_000 {
		t.Fatalf("fee history calls %+v, want one batch of 100 ending at head", calls)
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

func TestCollectFeesFetchRange(t *testing.T) {
	tests := []struct {
		name        string
		stored      uint64
		depth       uint64
		batch       uint64
		wantBatches []mocks.FeeHistoryCall
	}{
		{"continues after the latest stored", 4_997, 10_000, 1024, []mocks.FeeHistoryCall{{BlockCount: 3, Newest: 5_000}}},
		{"no new blocks refetches head", 5_000, 10_000, 1024, []mocks.FeeHistoryCall{{BlockCount: 1, Newest: 5_000}}},
		{"splits into batches", 4_990, 10_000, 4, []mocks.FeeHistoryCall{
			{BlockCount: 4, Newest: 4_994}, {BlockCount: 4, Newest: 4_998}, {BlockCount: 2, Newest: 5_000},
		}},
		{"gap beyond history depth starts at the depth", 100, 50, 1024, []mocks.FeeHistoryCall{{BlockCount: 50, Newest: 5_000}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCollectFixture(5_000, modelNext)
			_ = f.blocks.Upsert(t.Context(), "base", []entities.BlockFee{{Number: tt.stored, BaseFee: wei(1)}})
			network := baseNet
			network.HistoryDepth, network.FeeHistoryBatch = tt.depth, tt.batch
			if _, err := f.collector(network).Execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			calls := f.fees.Calls()
			if len(calls) != len(tt.wantBatches) {
				t.Fatalf("calls %+v, want %+v", calls, tt.wantBatches)
			}
			for i, w := range tt.wantBatches {
				if calls[i].BlockCount != w.BlockCount || calls[i].Newest != w.Newest {
					t.Fatalf("call %d: %+v, want %+v", i, calls[i], w)
				}
			}
		})
	}
}

func TestCollectFeesSelfCheck(t *testing.T) {
	t.Run("mismatch is counted and collection continues", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext+1)
		obs, err := f.collector(baseNet).Execute(t.Context())
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
		f.fees.HeadFeeFn = func() (entities.HeadFee, error) {
			h, err := head()
			h.BlobGasUsed = 300_000_000
			return h, err
		}
		if _, err := f.collector(baseNet).Execute(t.Context()); err != nil {
			t.Fatal(err)
		}
		if f.metrics.Mismatches["base"] != 0 {
			t.Fatal("model ignored the DA footprint")
		}
	})
	t.Run("unknown extraData version leaves params unknown", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext+1)
		f.fees.HeadFeeFn = func() (entities.HeadFee, error) {
			return entities.HeadFee{Number: 5_000, BaseFee: wei(1), GasLimit: 30_000_000, ExtraData: []byte{0x07}}, nil
		}
		collector := f.collector(baseNet)
		for range 2 {
			obs, err := collector.Execute(t.Context())
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
		network := baseNet
		network.Network = entities.Network{Name: "cchain", Kind: entities.NetworkKindAvalancheSAE, Coin: "AVAX"}
		obs, err := usecases.NewCollectFees(network, refTxs, usecases.CollectDeps{
			Fees: f.fees, Prices: f.prices, Blocks: f.blocks, Snapshots: f.snapshots,
			Metrics: f.metrics, Now: clock(t0), Log: nopLog,
		}).Execute(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Params != nil || f.metrics.Mismatches["cchain"] != 0 || len(obs.Snapshot.L1Fees) != 0 {
			t.Fatalf("params %+v, l1 %v", obs.Params, obs.Snapshot.L1Fees)
		}
	})
}

func TestCollectFeesDegradedSources(t *testing.T) {
	t.Run("stale price is stored but not exported", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext)
		f.prices.PriceFn = func() (entities.PriceQuote, error) {
			return entities.PriceQuote{Answer: wei(1), Decimals: 8, UpdatedAt: t0.Add(-2 * time.Hour)}, nil
		}
		obs, err := f.collector(baseNet).Execute(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Snapshot.Price == nil || f.metrics.Snapshots[0].Price != nil {
			t.Fatalf("snapshot price %+v, metrics price %+v", obs.Snapshot.Price, f.metrics.Snapshots[0].Price)
		}
	})
	t.Run("price and L1 errors still store a snapshot", func(t *testing.T) {
		f := newCollectFixture(5_000, modelNext)
		f.prices.PriceFn = func() (entities.PriceQuote, error) { return entities.PriceQuote{}, errors.New("feed down") }
		f.l1.L1FeesFn = func([]entities.ReferenceTx) (map[string]*big.Int, error) { return nil, errors.New("oracle down") }
		obs, err := f.collector(baseNet).Execute(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if obs.Snapshot.Price != nil || len(obs.Snapshot.L1Fees) != 0 || len(f.snapshots.Snapshots) != 1 {
			t.Fatalf("snapshot %+v", obs.Snapshot)
		}
	})
}

func TestCollectFeesFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name   string
		break_ func(f *collectFixture)
		want   error
	}{
		{"head", func(f *collectFixture) {
			f.fees.HeadFeeFn = func() (entities.HeadFee, error) { return entities.HeadFee{}, boom }
		}, boom},
		{"fee history", func(f *collectFixture) {
			f.fees.FeeHistoryFn = func(uint64, uint64, []float64) (entities.FeeHistory, error) { return entities.FeeHistory{}, boom }
		}, boom},
		{"short fee history", func(f *collectFixture) {
			full := f.fees.FeeHistoryFn
			f.fees.FeeHistoryFn = func(count, newest uint64, p []float64) (entities.FeeHistory, error) {
				return full(count-1, newest, p)
			}
		}, entities.ErrMalformedFeeHistory},
		{"node tip", func(f *collectFixture) {
			f.fees.NodeTipFn = func() (*big.Int, error) { return nil, boom }
		}, boom},
		{"block store", func(f *collectFixture) { f.blocks.Err = boom }, boom},
		{"snapshot store", func(f *collectFixture) { f.snapshots.Err = boom }, boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCollectFixture(5_000, modelNext)
			tt.break_(f)
			if _, err := f.collector(baseNet).Execute(t.Context()); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			if len(f.metrics.Snapshots) != 0 {
				t.Fatal("failed poll exported a snapshot")
			}
		})
	}
}
