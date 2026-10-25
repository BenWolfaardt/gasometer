// Package score measures a resolved bid: whether and where it landed, what it paid against the cheapest price that
// would have landed, and what that cost in USD.
package score

import (
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer"
)

// Bid measures a bid made at block atBlock against the blocks (atBlock, atBlock+k]. A block includes the bid
// when its effective tip, min(tip, maxFee - baseFee), covers the block's required tip.
func Bid(bid gasometer.Bid, atBlock uint64, k int, window []gasometer.BlockFee) (gasometer.Score, error) {
	if bid.Tip == nil || bid.MaxFee == nil || bid.ExpectedBaseFee == nil {
		return gasometer.Score{}, gasometer.ErrInvalidBid
	}
	if k < 1 {
		return gasometer.Score{}, fmt.Errorf("%w: %d blocks", gasometer.ErrInvalidHorizon, k)
	}
	if len(window) != k {
		return gasometer.Score{}, fmt.Errorf("%w: %d of %d blocks", gasometer.ErrBlockGap, len(window), k)
	}
	var score gasometer.Score
	var cheapest *big.Int
	for i, b := range window {
		if b.Number != atBlock+uint64(i)+1 {
			return gasometer.Score{}, fmt.Errorf("%w: expected block %d, got %d", gasometer.ErrBlockGap, atBlock+uint64(i)+1, b.Number)
		}
		price := new(big.Int).Add(b.BaseFee, b.RequiredTip)
		if cheapest == nil || price.Cmp(cheapest) < 0 {
			cheapest = price
			score.RequiredBaseFee = new(big.Int).Set(b.BaseFee)
			score.RequiredTip = new(big.Int).Set(b.RequiredTip)
		}
		if !score.Included && bid.MaxFee.Cmp(price) >= 0 && bid.Tip.Cmp(b.RequiredTip) >= 0 {
			score.Included = true
			score.InclusionBlock = b.Number
			score.PaidBaseFee = new(big.Int).Set(b.BaseFee)
			score.PaidTip = minInt(new(big.Int).Set(bid.Tip), new(big.Int).Sub(bid.MaxFee, b.BaseFee))
		}
	}
	if score.Included {
		score.EstimateError = estimateError(bid, score.Paid())
	}
	return score, nil
}

// estimateError is |(expected base fee + tip) - paid| / paid, with a 1 wei floor on paid.
func estimateError(bid gasometer.Bid, paid *big.Int) float64 {
	expected := new(big.Int).Add(bid.ExpectedBaseFee, bid.Tip)
	diff := expected.Sub(expected, paid)
	denom := paid
	if denom.Sign() == 0 {
		denom = bigOne
	}
	ratio, _ := new(big.Rat).SetFrac(diff.Abs(diff), denom).Float64()
	return ratio
}

var bigOne = big.NewInt(1)

func minInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}
