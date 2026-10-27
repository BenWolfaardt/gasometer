package predict_test

import (
	"math/big"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
)

func wei(v int64) *big.Int { return big.NewInt(v) }

// blocks builds contiguous blocks from first with the given base fees and required tips.
func blocks(first uint64, baseFees, requiredTips []int64) []gasometer.BlockFee {
	out := make([]gasometer.BlockFee, len(baseFees))
	for i := range baseFees {
		out[i] = gasometer.BlockFee{
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

var baseParams = gasometer.FeeParams{Denominator: 100, Elasticity: 5, MinBaseFee: big.NewInt(5_000_000)}

// baseCeiling is Base's ceiling from a next base fee of 10,000,000.
var baseCeiling = fee.Ceiling{Params: baseParams, NextBaseFee: big.NewInt(10_000_000)}
