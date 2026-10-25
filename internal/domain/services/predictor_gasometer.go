package services

import (
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

type gasometerPredictor struct {
	tipQuantile float64
	tipWindow   int
	projector   BaseFeeProjector
}

// NewGasometerPredictor bids a quantile of recent required tips on top of a projected upper base fee.
func NewGasometerPredictor(tipQuantile float64, tipWindow int, projector BaseFeeProjector) Predictor {
	return &gasometerPredictor{tipQuantile: tipQuantile, tipWindow: tipWindow, projector: projector}
}

func (p *gasometerPredictor) Name() string { return "gasometer" }

func (p *gasometerPredictor) Predict(in PredictionInput) (entities.Bid, error) {
	if in.Snapshot.NextBaseFee == nil {
		return entities.Bid{}, entities.ErrInvalidBid
	}
	if p.tipWindow < 1 {
		return entities.Bid{}, fmt.Errorf("%w: tip window %d", entities.ErrInvalidConfig, p.tipWindow)
	}
	if len(in.History) < p.tipWindow {
		return entities.Bid{}, fmt.Errorf("%w: %d blocks, tip window %d",
			entities.ErrInsufficientHistory, len(in.History), p.tipWindow)
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
		return entities.Bid{}, fmt.Errorf("project base fee: %w", err)
	}
	return entities.Bid{
		Tip:             tip,
		MaxFee:          new(big.Int).Add(proj.Upper, tip),
		ExpectedBaseFee: proj.Expected,
	}, nil
}
