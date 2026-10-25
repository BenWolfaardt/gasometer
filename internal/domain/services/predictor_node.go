package services

import (
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

type nodePredictor struct{}

// NewNodePredictor bids the node's tip with maxFeePerGas = next base fee + tip.
func NewNodePredictor() Predictor {
	return nodePredictor{}
}

func (nodePredictor) Name() string { return "node" }

func (nodePredictor) Predict(in PredictionInput) (entities.Bid, error) {
	return baselineBid(in.Snapshot, 1)
}

// baselineBid bids the node's tip with maxFeePerGas = multiplier × next base fee + tip.
func baselineBid(s entities.FeeSnapshot, multiplier int64) (entities.Bid, error) {
	if s.NextBaseFee == nil || s.NodeTip == nil {
		return entities.Bid{}, entities.ErrInvalidBid
	}
	maxFee := new(big.Int).Mul(s.NextBaseFee, big.NewInt(multiplier))
	return entities.Bid{
		Tip:             new(big.Int).Set(s.NodeTip),
		MaxFee:          maxFee.Add(maxFee, s.NodeTip),
		ExpectedBaseFee: new(big.Int).Set(s.NextBaseFee),
	}, nil
}
