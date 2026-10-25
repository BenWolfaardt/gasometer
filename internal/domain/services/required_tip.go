package services

import (
	"fmt"
	"math/big"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

const medianPercentile = 50

// RewardPercentiles are the eth_feeHistory percentiles BlockFees expects, in order.
func RewardPercentiles(requiredTipPercentile float64) []float64 {
	return []float64{requiredTipPercentile, medianPercentile}
}

// BlockFees turns a fee history fetched with RewardPercentiles into per-block summaries. A block with no gas used
// requires no tip. A block with gas used but missing rewards is treated the same and counted in missingRewards.
func BlockFees(history entities.FeeHistory, observedAt time.Time) (blocks []entities.BlockFee, missingRewards int, err error) {
	n := history.Len()
	if len(history.BaseFees) != n+1 {
		return nil, 0, fmt.Errorf("%w: %d base fees for %d blocks", entities.ErrMalformedFeeHistory, len(history.BaseFees), n)
	}
	blocks = make([]entities.BlockFee, n)
	for i := range n {
		required, median := new(big.Int), new(big.Int)
		ratio := history.GasUsedRatios[i]
		switch {
		case ratio == 0:
		case i >= len(history.Rewards) || len(history.Rewards[i]) < 2 ||
			history.Rewards[i][0] == nil || history.Rewards[i][1] == nil:
			missingRewards++
		default:
			required.Set(history.Rewards[i][0])
			median.Set(history.Rewards[i][1])
		}
		blocks[i] = entities.BlockFee{
			Number:       history.OldestBlock + uint64(i),
			BaseFee:      new(big.Int).Set(history.BaseFees[i]),
			GasUsedRatio: ratio,
			RequiredTip:  required,
			MedianTip:    median,
			ObservedAt:   observedAt,
		}
	}
	return blocks, missingRewards, nil
}
