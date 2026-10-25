package services

import (
	"math"
	"math/big"
	"slices"
)

// quantileIndex is the nearest-rank index of quantile q in n sorted values.
func quantileIndex(n int, q float64) int {
	return min(n-1, max(0, int(math.Ceil(q*float64(n)))-1))
}

func quantileInt(values []*big.Int, q float64) *big.Int {
	sorted := slices.SortedFunc(slices.Values(values), (*big.Int).Cmp)
	return new(big.Int).Set(sorted[quantileIndex(len(sorted), q)])
}

func quantileRat(values []*big.Rat, q float64) *big.Rat {
	sorted := slices.SortedFunc(slices.Values(values), (*big.Rat).Cmp)
	return new(big.Rat).Set(sorted[quantileIndex(len(sorted), q)])
}

func minInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}
