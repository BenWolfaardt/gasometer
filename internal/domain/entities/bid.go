package entities

import "math/big"

// Bid is a predictor's fee, per gas in wei.
type Bid struct {
	Tip             *big.Int
	MaxFee          *big.Int
	ExpectedBaseFee *big.Int
}
