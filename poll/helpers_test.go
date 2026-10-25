package poll_test

import (
	"log/slog"
	"math/big"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
)

var (
	t0              = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	nopLog          = slog.New(slog.DiscardHandler)
	refTxs          = []gasometer.ReferenceTx{{Name: "native", Gas: 21_000}, {Name: "erc20", Gas: 65_000, DataSize: 68}}
	jovian          = []byte{0x01, 0, 0, 0, 100, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0x4c, 0x4b, 0x40}
	baseNet         = gasometer.Network{Name: "base", ChainID: 8453, Kind: gasometer.NetworkKindOPStack, Coin: "ETH"}
	collectorParams = poll.CollectorParams{RequiredTipPercentile: 5, PriceMaxAge: time.Hour, InitialBackfill: 1024}
)

func clock(at time.Time) func() time.Time { return func() time.Time { return at } }

// feeSource serves a network at head whose blocks all have base fee 10,000,000 and rewards {1, 5}, and whose next
// base fee is next. It returns every block asked for.
func feeSource(head uint64, next int64) *mock.FeeSource {
	return &mock.FeeSource{
		HeadFeeFn: func() (gasometer.HeadFee, error) {
			return gasometer.HeadFee{
				Number: head, BaseFee: wei(10_000_000), GasUsed: 60_000_000, GasLimit: 300_000_000, ExtraData: jovian,
			}, nil
		},
		FeeHistoryFn: func(from, to uint64, _ []float64) (gasometer.FeeHistory, error) {
			return syntheticHistory(from, to, next), nil
		},
		NodeTipFn: func() (*big.Int, error) { return wei(1_000), nil },
	}
}

func syntheticHistory(from, to uint64, next int64) gasometer.FeeHistory {
	h := gasometer.FeeHistory{OldestBlock: from}
	for range to - from + 1 {
		h.BaseFees = append(h.BaseFees, wei(10_000_000))
		h.GasUsedRatios = append(h.GasUsedRatios, 0.2)
		h.Rewards = append(h.Rewards, []*big.Int{wei(1), wei(5)})
	}
	h.BaseFees = append(h.BaseFees, wei(next))
	return h
}

func wei(v int64) *big.Int { return big.NewInt(v) }

func eqWei(a *big.Int, b int64) bool { return a != nil && a.Cmp(wei(b)) == 0 }
