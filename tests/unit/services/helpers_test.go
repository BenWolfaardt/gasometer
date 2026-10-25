package services_test

import (
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

func wei(v int64) *big.Int { return big.NewInt(v) }

// blocks builds contiguous blocks from first with the given base fees and required tips.
func blocks(first uint64, baseFees, requiredTips []int64) []entities.BlockFee {
	out := make([]entities.BlockFee, len(baseFees))
	for i := range baseFees {
		out[i] = entities.BlockFee{
			Number:      first + uint64(i),
			BaseFee:     wei(baseFees[i]),
			RequiredTip: wei(requiredTips[i]),
			MedianTip:   wei(requiredTips[i]),
		}
	}
	return out
}

func repeat(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func eqWei(a *big.Int, b int64) bool { return a != nil && a.Cmp(wei(b)) == 0 }
