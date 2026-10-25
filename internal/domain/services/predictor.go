package services

import "github.com/BenWolfaardt/gasometer/internal/domain/entities"

// PredictionInput is what a predictor sees for one horizon.
type PredictionInput struct {
	Snapshot entities.FeeSnapshot
	History  []entities.BlockFee // ascending, the growth window, ending at Snapshot.AtBlock
	Horizon  entities.Horizon
	Params   *entities.FeeParams // nil when no model applies
}

// Predictor proposes a bid for a horizon.
type Predictor interface {
	Name() string
	Predict(in PredictionInput) (entities.Bid, error)
}
