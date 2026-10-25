package services

import "github.com/BenWolfaardt/gasometer/internal/domain/entities"

type walletPredictor struct{}

// NewWalletPredictor bids the node's tip with maxFeePerGas = 2 × next base fee + tip, the ethers and MetaMask default.
func NewWalletPredictor() Predictor {
	return walletPredictor{}
}

func (walletPredictor) Name() string { return "wallet" }

func (walletPredictor) Predict(in PredictionInput) (entities.Bid, error) {
	return baselineBid(in.Snapshot, 2)
}
