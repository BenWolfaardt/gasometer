package entities

import "math/big"

// Score is a resolved prediction, per gas in wei.
type Score struct {
	Included        bool
	InclusionBlock  uint64   // when included
	PaidBaseFee     *big.Int // when included
	PaidTip         *big.Int // when included, effective tip
	RequiredBaseFee *big.Int // at the cheapest block in the window
	RequiredTip     *big.Int // at the cheapest block in the window
	EstimateError   float64  // when included
}

// Paid is the per-gas price paid, or nil when missed.
func (s Score) Paid() *big.Int {
	if !s.Included {
		return nil
	}
	return new(big.Int).Add(s.PaidBaseFee, s.PaidTip)
}

// Required is the cheapest per-gas price that would have landed in the window.
func (s Score) Required() *big.Int {
	return new(big.Int).Add(s.RequiredBaseFee, s.RequiredTip)
}

// Overpay is paid minus required, or nil when missed.
func (s Score) Overpay() *big.Int {
	paid := s.Paid()
	if paid == nil {
		return nil
	}
	return paid.Sub(paid, s.Required())
}
