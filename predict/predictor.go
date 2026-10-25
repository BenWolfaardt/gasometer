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
	return baselineBid(in.Snapshot, 1)
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
	return baselineBid(in.Snapshot, 2)
}

// baselineBid bids the node's tip with maxFeePerGas = multiplier × next base fee + tip.
func baselineBid(s gasometer.FeeSnapshot, multiplier int64) (gasometer.Bid, error) {
	if s.NextBaseFee == nil || s.NodeTip == nil {
		return gasometer.Bid{}, gasometer.ErrInvalidBid
	}
	maxFee := new(big.Int).Mul(s.NextBaseFee, big.NewInt(multiplier))
	return gasometer.Bid{
		Tip:             new(big.Int).Set(s.NodeTip),
		MaxFee:          maxFee.Add(maxFee, s.NodeTip),
		ExpectedBaseFee: new(big.Int).Set(s.NextBaseFee),
	}, nil
}

// Params tune the gasometer predictor and the history the forecaster loads for it.
type Params struct {
	TipQuantile    float64
	TipWindow      int // blocks
	GrowthQuantile float64
	GrowthWindow   time.Duration
}

// Gasometer bids a quantile of recent required tips on top of a projected upper base fee.
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
	if len(in.History) < p.tipWindow {
		return gasometer.Bid{}, fmt.Errorf("%w: %d blocks, tip window %d",
			gasometer.ErrInsufficientHistory, len(in.History), p.tipWindow)
	}
	tips := make([]*big.Int, 0, p.tipWindow)
	for _, b := range in.History[len(in.History)-p.tipWindow:] {
		tips = append(tips, b.RequiredTip)
	}
	tip := quantileInt(tips, p.tipQuantile)

	proj, err := p.projector.Project(ProjectionInput{
		NextBaseFee: in.Snapshot.NextBaseFee,
		History:     in.History,
		Blocks:      in.Horizon.Blocks,
		Params:      in.Params,
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
