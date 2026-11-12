package poll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
)

// ForecasterParams are a forecaster's decisions for one network.
type ForecasterParams struct {
	HorizonSeconds []int
	GrowthWindow   time.Duration // base fee history the projections read
	TipWindow      int           // horizon-long windows of required tips, one ending at each of this many recent blocks
}

// ForecasterDeps are a forecaster's collaborators.
type ForecasterDeps struct {
	Predictors  []gasometer.Predictor
	Blocks      gasometer.BlockStore
	Predictions gasometer.PredictionStore
	Log         *slog.Logger
}

// Forecaster runs every predictor at every horizon against an observation and stores the predictions.
type Forecaster struct {
	network string
	params  ForecasterParams
	deps    ForecasterDeps
	log     *slog.Logger
}

// NewForecaster builds the forecaster for one network.
func NewForecaster(network string, params ForecasterParams, deps ForecasterDeps) *Forecaster {
	return &Forecaster{network: network, params: params, deps: deps, log: deps.Log.With("network", network)}
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
	horizons, err := Horizons(f.params.HorizonSeconds, avg)
	if err != nil {
		return 0, err
	}
	history, err := f.history(ctx, obs.Snapshot.AtBlock, avg, horizons)
	if err != nil {
		return 0, err
	}
	history = unbroken(history, obs.Snapshot.AtBlock)

	var made []gasometer.Prediction
	for _, predictor := range f.deps.Predictors {
		for _, h := range horizons {
			bid, err := predictor.Predict(gasometer.PredictionInput{
				Snapshot: obs.Snapshot, History: history, Horizon: h, Ceiling: obs.Ceiling,
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

// history loads the growth window, or the tip windows of the longest horizon if longer, ending at atBlock.
func (f *Forecaster) history(ctx context.Context, atBlock uint64, avg time.Duration, horizons []gasometer.Horizon) (
	[]gasometer.BlockFee, error) {
	growth, err := BlocksIn(f.params.GrowthWindow, avg)
	if err != nil {
		return nil, err
	}
	longest := 1
	for _, h := range horizons {
		longest = max(longest, h.Blocks)
	}
	span := uint64(max(growth, f.params.TipWindow+longest-1))
	blocks, err := f.deps.Blocks.Range(ctx, f.network, blocksBack(atBlock, span), atBlock)
	if err != nil {
		return nil, fmt.Errorf("load history: %w", err)
	}
	return blocks, nil
}

// unbroken trims history to the run of consecutive blocks ending at atBlock, so no predictor computes across a hole.
// A predictor that needs more than the run holds refuses, as on a first run, until the backfill fills the hole.
func unbroken(history []gasometer.BlockFee, atBlock uint64) []gasometer.BlockFee {
	if len(history) == 0 || history[len(history)-1].Number != atBlock {
		return nil
	}
	start := len(history) - 1
	for start > 0 && history[start-1].Number+1 == history[start].Number {
		start--
	}
	return history[start:]
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
