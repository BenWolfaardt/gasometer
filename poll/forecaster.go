package poll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/predict"
)

// ForecasterDeps are a forecaster's collaborators.
type ForecasterDeps struct {
	Predictors  []gasometer.Predictor
	Blocks      gasometer.BlockStore
	Predictions gasometer.PredictionStore
	Log         *slog.Logger
}

// Forecaster runs every predictor at every horizon against an observation and stores the predictions.
type Forecaster struct {
	network        string
	horizonSeconds []int
	params         predict.Params
	deps           ForecasterDeps
	log            *slog.Logger
}

// NewForecaster builds the forecaster for one network.
func NewForecaster(network string, horizonSeconds []int, params predict.Params, deps ForecasterDeps) *Forecaster {
	return &Forecaster{
		network:        network,
		horizonSeconds: horizonSeconds,
		params:         params,
		deps:           deps,
		log:            deps.Log.With("network", network),
	}
}

// Forecast stores one prediction per predictor and horizon and returns how many it made.
func (f *Forecaster) Forecast(ctx context.Context, obs Observation) (int, error) {
	avg, err := f.deps.Blocks.AverageBlockTime(ctx, f.network)
	if errors.Is(err, gasometer.ErrInsufficientHistory) {
		f.log.Debug("block time not yet measurable; no predictions this tick")
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("measure block time: %w", err)
	}
	horizons, err := Horizons(f.horizonSeconds, avg)
	if err != nil {
		return 0, err
	}
	history, err := f.history(ctx, obs.Snapshot.AtBlock, avg)
	if err != nil {
		return 0, err
	}

	var made []gasometer.Prediction
	for _, predictor := range f.deps.Predictors {
		for _, h := range horizons {
			bid, err := predictor.Predict(gasometer.PredictionInput{
				Snapshot: obs.Snapshot, History: history, Horizon: h, Params: obs.Params,
			})
			if err != nil {
				level := slog.LevelWarn
				if errors.Is(err, gasometer.ErrInsufficientHistory) {
					level = slog.LevelDebug
				}
				f.log.Log(ctx, level, "no prediction", "err", err, "predictor", predictor.Name(), "horizon", h.Seconds)
				continue
			}
			made = append(made, gasometer.Prediction{
				SnapshotID: obs.Snapshot.ID,
				Network:    f.network,
				Predictor:  predictor.Name(),
				Horizon:    h,
				AtBlock:    obs.Snapshot.AtBlock,
				MadeAt:     obs.Snapshot.ObservedAt,
				Bid:        bid,
				Status:     gasometer.PredictionPending,
			})
		}
	}
	if len(made) == 0 {
		return 0, nil
	}
	if err := f.deps.Predictions.Insert(ctx, made); err != nil {
		return 0, fmt.Errorf("store predictions: %w", err)
	}
	return len(made), nil
}

// history loads the growth window, or the tip window if longer, ending at atBlock.
func (f *Forecaster) history(ctx context.Context, atBlock uint64, avg time.Duration) ([]gasometer.BlockFee, error) {
	growth, err := BlocksIn(f.params.GrowthWindow, avg)
	if err != nil {
		return nil, err
	}
	span := uint64(max(growth, f.params.TipWindow))
	blocks, err := f.deps.Blocks.Range(ctx, f.network, blocksBack(atBlock, span), atBlock)
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	return blocks, nil
}

// BlocksIn converts a duration to a whole number of blocks at the given average block time, at least one.
func BlocksIn(d, avgBlockTime time.Duration) (int, error) {
	if avgBlockTime <= 0 || d <= 0 {
		return 0, fmt.Errorf("%w: %s at %s per block", gasometer.ErrInvalidHorizon, d, avgBlockTime)
	}
	return max(1, int(math.Round(float64(d)/float64(avgBlockTime)))), nil
}

// Horizons converts horizon seconds to block counts at the given average block time.
func Horizons(seconds []int, avgBlockTime time.Duration) ([]gasometer.Horizon, error) {
	out := make([]gasometer.Horizon, 0, len(seconds))
	for _, s := range seconds {
		n, err := BlocksIn(time.Duration(s)*time.Second, avgBlockTime)
		if err != nil {
			return nil, err
		}
		out = append(out, gasometer.Horizon{Seconds: s, Blocks: n})
	}
	return out, nil
}
