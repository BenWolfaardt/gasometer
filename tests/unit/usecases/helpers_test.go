package usecases_test

import (
	"math/big"
	"time"

	"github.com/rs/zerolog"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/tests/mocks"
)

var (
	t0      = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	nopLog  = zerolog.Nop()
	refTxs  = []entities.ReferenceTx{{Name: "native", Gas: 21_000}, {Name: "erc20", Gas: 65_000, DataSize: 68}}
	jovian  = []byte{0x01, 0, 0, 0, 100, 0, 0, 0, 5, 0, 0, 0, 0, 0, 0x4c, 0x4b, 0x40}
	baseNet = entities.NetworkConfig{
		Network:               entities.Network{Name: "base", ChainID: 8453, Kind: entities.NetworkKindOPStack, Coin: "ETH"},
		PriceMaxAge:           time.Hour,
		RequiredTipPercentile: 5,
		HistoryDepth:          10_000,
		FeeHistoryBatch:       1024,
	}
)

func wei(v int64) *big.Int { return big.NewInt(v) }

func clock(at time.Time) func() time.Time { return func() time.Time { return at } }

// feeSource serves a chain at head whose blocks all have base fee 10,000,000 and rewards {1, 5}, and whose next
// base fee is next.
func feeSource(head uint64, next int64) *mocks.FeeSource {
	return &mocks.FeeSource{
		HeadFeeFn: func() (entities.HeadFee, error) {
			return entities.HeadFee{
				Number: head, BaseFee: wei(10_000_000), GasUsed: 60_000_000, GasLimit: 300_000_000, ExtraData: jovian,
			}, nil
		},
		FeeHistoryFn: func(count, newest uint64, _ []float64) (entities.FeeHistory, error) {
			h := entities.FeeHistory{OldestBlock: newest - count + 1}
			for range count {
				h.BaseFees = append(h.BaseFees, wei(10_000_000))
				h.GasUsedRatios = append(h.GasUsedRatios, 0.2)
				h.Rewards = append(h.Rewards, []*big.Int{wei(1), wei(5)})
			}
			h.BaseFees = append(h.BaseFees, wei(next))
			return h, nil
		},
		NodeTipFn: func() (*big.Int, error) { return wei(1_000), nil },
	}
}

func eqWei(a *big.Int, b int64) bool { return a != nil && a.Cmp(wei(b)) == 0 }
