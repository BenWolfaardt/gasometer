// Package predict holds the fee predictors gasometer scores, and the base fee projectors and quantiles they use.
package predict

import (
	"fmt"
	"math/big"
	"time"

	"github.com/BenWolfaardt/gasometer"
)

var (
	_ gasometer.Predictor = (*Node)(nil)
	_ gasometer.Predictor = (*Wallet)(nil)
	_ gasometer.Predictor = (*Market)(nil)
	_ gasometer.Predictor = (*Gasometer)(nil)
)

// Node bids the node's tip with maxFeePerGas = next base fee + tip.
type Node struct{}

// NewNode builds the node predictor.
func NewNode() *Node {
	return &Node{}
}

// Name is the predictor label.
func (*Node) Name() string { return "node" }

// Predict bids for a horizon.
func (*Node) Predict(in gasometer.PredictionInput) (gasometer.Bid, error) {
	return baselineBid(in.Snapshot.NextBaseFee, in.Snapshot.NodeTip, 1)
}

// Wallet bids the node's tip with maxFeePerGas = 2 × next base fee + tip, the ethers and MetaMask default.
type Wallet struct{}

// NewWallet builds the wallet predictor.
func NewWallet() *Wallet {
	return &Wallet{}
}

// Name is the predictor label.
func (*Wallet) Name() string { return "wallet" }

// Predict bids for a horizon.
func (*Wallet) Predict(in gasometer.PredictionInput) (gasometer.Bid, error) {
	return baselineBid(in.Snapshot.NextBaseFee, in.Snapshot.NodeTip, 2)
}

// Market bids the latest block's median tip by gas, what senders are paying, with the wallet's
// maxFeePerGas = 2 × next base fee + tip.
type Market struct{}

// NewMarket builds the market predictor.
func NewMarket() *Market {
	return &Market{}
}

// Name is the predictor label.
func (*Market) Name() string { return "market" }

// Predict bids for a horizon.
func (*Market) Predict(in gasometer.PredictionInput) (gasometer.Bid, error) {
	if len(in.History) == 0 {
		return gasometer.Bid{}, fmt.Errorf("%w: no blocks for the market tip", gasometer.ErrInsufficientHistory)
	}
	return baselineBid(in.Snapshot.NextBaseFee, in.History[len(in.History)-1].MedianTip, 2)
}

// baselineBid bids tip with maxFeePerGas = multiplier × next base fee + tip.
func baselineBid(nextBaseFee, tip *big.Int, multiplier int64) (gasometer.Bid, error) {
	if nextBaseFee == nil || tip == nil {
		return gasometer.Bid{}, gasometer.ErrInvalidBid
	}
	maxFee := new(big.Int).Mul(nextBaseFee, big.NewInt(multiplier))
	return gasometer.Bid{
		Tip:             new(big.Int).Set(tip),
		MaxFee:          maxFee.Add(maxFee, tip),
		ExpectedBaseFee: new(big.Int).Set(nextBaseFee),
	}, nil
}

// Params tune the gasometer predictor and the history the forecaster loads for it.
type Params struct {
	TipQuantile    float64
	TipWindow      int // horizon-long windows, one ending at each of this many recent blocks
	GrowthQuantile float64
	GrowthWindow   time.Duration
}

// Gasometer bids a quantile of recent window-cheapest required tips on top of a projected upper base fee. A bid
// lands if it beats the required tip of any one block in its horizon, so the tip it needs is the cheapest one in a
// horizon-long window, not a typical block's.
type Gasometer struct {
	tipQuantile float64
	tipWindow   int
	projector   BaseFeeProjector
}

// NewGasometer builds the gasometer predictor.
func NewGasometer(tipQuantile float64, tipWindow int, projector BaseFeeProjector) *Gasometer {
	return &Gasometer{tipQuantile: tipQuantile, tipWindow: tipWindow, projector: projector}
}

// Name is the predictor label.
func (p *Gasometer) Name() string { return "gasometer" }

// Predict bids for a horizon.
func (p *Gasometer) Predict(in gasometer.PredictionInput) (gasometer.Bid, error) {
	if in.Snapshot.NextBaseFee == nil {
		return gasometer.Bid{}, gasometer.ErrInvalidBid
	}
	if p.tipWindow < 1 {
		return gasometer.Bid{}, fmt.Errorf("%w: tip window %d", gasometer.ErrInvalidConfig, p.tipWindow)
	}
	span := max(1, in.Horizon.Blocks)
	need := p.tipWindow + span - 1
	if len(in.History) < need {
		return gasometer.Bid{}, fmt.Errorf("%w: %d blocks, tip window %d at %d blocks per horizon",
			gasometer.ErrInsufficientHistory, len(in.History), p.tipWindow, span)
	}
	tip := quantileInt(windowMinima(in.History[len(in.History)-need:], span), p.tipQuantile)

	proj, err := p.projector.Project(ProjectionInput{
		NextBaseFee: in.Snapshot.NextBaseFee,
		History:     in.History,
		Blocks:      in.Horizon.Blocks,
		Ceiling:     in.Ceiling,
	})
	if err != nil {
		return gasometer.Bid{}, fmt.Errorf("project base fee: %w", err)
	}
	return gasometer.Bid{
		Tip:             tip,
		MaxFee:          new(big.Int).Add(proj.Upper, tip),
		ExpectedBaseFee: proj.Expected,
	}, nil
}

// windowMinima is the cheapest required tip of each span-block window in blocks.
func windowMinima(blocks []gasometer.BlockFee, span int) []*big.Int {
	out := make([]*big.Int, 0, len(blocks)-span+1)
	for end := span; end <= len(blocks); end++ {
		cheapest := blocks[end-span].RequiredTip
		for _, b := range blocks[end-span+1 : end] {
			cheapest = minInt(cheapest, b.RequiredTip)
		}
		out = append(out, cheapest)
	}
	return out
}
