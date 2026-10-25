package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

// Predict runs every predictor at every horizon against an observation and stores the predictions.
type Predict interface {
	Execute(ctx context.Context, obs Observation) (int, error)
}

// PredictDeps are Predict's collaborators.
type PredictDeps struct {
	Predictors  []services.Predictor
	Blocks      repositories.BlockStore
	Predictions repositories.PredictionStore
	Log         zerolog.Logger
}

type predict struct {
	network        string
	horizonSeconds []int
	params         entities.GasometerParams
	deps           PredictDeps
}

// NewPredict builds the predict step for one network.
func NewPredict(network string, horizonSeconds []int, params entities.GasometerParams, deps PredictDeps) Predict {
	deps.Log = deps.Log.With().Str("network", network).Logger()
	return &predict{network: network, horizonSeconds: horizonSeconds, params: params, deps: deps}
}

func (p *predict) Execute(ctx context.Context, obs Observation) (int, error) {
	avg, err := p.deps.Blocks.AverageBlockTime(ctx, p.network)
	if errors.Is(err, entities.ErrInsufficientHistory) {
		p.deps.Log.Debug().Msg("block time not yet measurable; no predictions this tick")
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("measure block time: %w", err)
	}
	horizons, err := services.Horizons(p.horizonSeconds, avg)
	if err != nil {
		return 0, err
	}
	history, err := p.history(ctx, obs.Snapshot.AtBlock, avg)
	if err != nil {
		return 0, err
	}

	var made []entities.Prediction
	for _, predictor := range p.deps.Predictors {
		for _, h := range horizons {
			bid, err := predictor.Predict(services.PredictionInput{
				Snapshot: obs.Snapshot, History: history, Horizon: h, Params: obs.Params,
			})
			if err != nil {
				level := zerolog.WarnLevel
				if errors.Is(err, entities.ErrInsufficientHistory) {
					level = zerolog.DebugLevel
				}
				p.deps.Log.WithLevel(level).Err(err).Str("predictor", predictor.Name()).Int("horizon", h.Seconds).
					Msg("no prediction")
				continue
			}
			made = append(made, entities.Prediction{
				SnapshotID: obs.Snapshot.ID,
				Network:    p.network,
				Predictor:  predictor.Name(),
				Horizon:    h,
				AtBlock:    obs.Snapshot.AtBlock,
				MadeAt:     obs.Snapshot.ObservedAt,
				Bid:        bid,
				Status:     entities.PredictionPending,
			})
		}
	}
	if len(made) == 0 {
		return 0, nil
	}
	if err := p.deps.Predictions.Insert(ctx, made); err != nil {
		return 0, fmt.Errorf("store predictions: %w", err)
	}
	return len(made), nil
}

// history loads the growth window, or the tip window if longer, ending at atBlock.
func (p *predict) history(ctx context.Context, atBlock uint64, avg time.Duration) ([]entities.BlockFee, error) {
	growth, err := services.BlocksIn(p.params.GrowthWindow, avg)
	if err != nil {
		return nil, err
	}
	span := uint64(max(growth, p.params.TipWindow))
	blocks, err := p.deps.Blocks.Range(ctx, p.network, blocksBack(atBlock, span), atBlock)
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	return blocks, nil
}
