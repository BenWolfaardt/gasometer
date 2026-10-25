package entities

import (
	"math/big"
	"time"
)

// PriceQuote is a native token price in USD.
type PriceQuote struct {
	Answer    *big.Int // USD × 10^Decimals
	Decimals  uint8
	UpdatedAt time.Time
}

// Stale reports whether the quote is older than maxAge.
func (q PriceQuote) Stale(now time.Time, maxAge time.Duration) bool {
	return now.Sub(q.UpdatedAt) > maxAge
}

// USD is the price as a float, for metrics only.
func (q PriceQuote) USD() float64 {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(q.Decimals)), nil)
	f, _ := new(big.Rat).SetFrac(q.Answer, scale).Float64()
	return f
}
