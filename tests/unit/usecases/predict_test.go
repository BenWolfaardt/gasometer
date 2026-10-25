package usecases_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
	"github.com/BenWolfaardt/gasometer/internal/usecases"
	"github.com/BenWolfaardt/gasometer/tests/mocks"
)

var gasometerParams = entities.GasometerParams{TipQuantile: 0.9, TipWindow: 30, GrowthQuantile: 0.99, GrowthWindow: time.Hour}

func observation(atBlock uint64) usecases.Observation {
	return usecases.Observation{Snapshot: entities.FeeSnapshot{
		ID: 7, Network: "base", AtBlock: atBlock, ObservedAt: t0, NextBaseFee: wei(10_000_000), NodeTip: wei(1_000),
	}}
}

func storeBlocks(t *testing.T, store *mocks.BlockStore, from, to uint64) {
	t.Helper()
	var blocks []entities.BlockFee
	for n := from; n <= to; n++ {
		blocks = append(blocks, entities.BlockFee{Number: n, BaseFee: wei(10_000_000), RequiredTip: wei(int64(n % 7)), ObservedAt: t0})
	}
	if err := store.Upsert(t.Context(), "base", blocks); err != nil {
		t.Fatal(err)
	}
}

type recordingPredictor struct {
	services.Predictor
	inputs []services.PredictionInput
}

func (r *recordingPredictor) Predict(in services.PredictionInput) (entities.Bid, error) {
	r.inputs = append(r.inputs, in)
	return r.Predictor.Predict(in)
}

func TestPredict(t *testing.T) {
	blocks := mocks.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	storeBlocks(t, blocks, 1, 5_000)
	predictions := &mocks.PredictionStore{}
	gasometer := &recordingPredictor{Predictor: services.NewGasometerPredictor(0.9, 30, services.NewEmpiricalProjector(0.99))}
	uc := usecases.NewPredict("base", []int{2, 30, 120}, gasometerParams, usecases.PredictDeps{
		Predictors:  []services.Predictor{services.NewNodePredictor(), services.NewWalletPredictor(), gasometer},
		Blocks:      blocks,
		Predictions: predictions,
		Log:         nopLog,
	})
	n, err := uc.Execute(t.Context(), observation(5_000))
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 || len(predictions.Predictions) != 9 {
		t.Fatalf("made %d, stored %d, want 9", n, len(predictions.Predictions))
	}
	wantBlocks := map[int]int{2: 1, 30: 15, 120: 60}
	for _, p := range predictions.Predictions {
		if p.Horizon.Blocks != wantBlocks[p.Horizon.Seconds] || p.SnapshotID != 7 || p.AtBlock != 5_000 ||
			p.Status != entities.PredictionPending || !p.MadeAt.Equal(t0) || p.Network != "base" {
			t.Fatalf("prediction %+v", p)
		}
	}
	// One hour at 2 s per block is 1,800 blocks.
	h := gasometer.inputs[0].History
	if len(h) != 1_800 || h[0].Number != 3_201 || h[len(h)-1].Number != 5_000 {
		t.Fatalf("history %d blocks from %d", len(h), h[0].Number)
	}
}

func TestPredictWaitsForBlockTime(t *testing.T) {
	predictions := &mocks.PredictionStore{}
	n, err := usecases.NewPredict("base", []int{2}, gasometerParams, usecases.PredictDeps{
		Predictors: []services.Predictor{services.NewNodePredictor()}, Blocks: mocks.NewBlockStore(),
		Predictions: predictions, Log: nopLog,
	}).Execute(t.Context(), observation(5_000))
	if err != nil || n != 0 || len(predictions.Predictions) != 0 {
		t.Fatalf("n %d, err %v", n, err)
	}
}

func TestPredictSkipsPredictorsWithoutHistory(t *testing.T) {
	blocks := mocks.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	storeBlocks(t, blocks, 4_990, 5_000)
	predictions := &mocks.PredictionStore{}
	n, err := usecases.NewPredict("base", []int{30}, gasometerParams, usecases.PredictDeps{
		Predictors: []services.Predictor{
			services.NewGasometerPredictor(0.9, 30, services.NewEmpiricalProjector(0.99)), services.NewNodePredictor(),
		},
		Blocks: blocks, Predictions: predictions, Log: nopLog,
	}).Execute(t.Context(), observation(5_000))
	if err != nil || n != 1 || predictions.Predictions[0].Predictor != "node" {
		t.Fatalf("n %d, err %v, %+v", n, err, predictions.Predictions)
	}
}

func TestPredictStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	blocks := mocks.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	_, err := usecases.NewPredict("base", []int{2}, gasometerParams, usecases.PredictDeps{
		Predictors: []services.Predictor{services.NewNodePredictor()}, Blocks: blocks,
		Predictions: &mocks.PredictionStore{Err: boom}, Log: nopLog,
	}).Execute(t.Context(), observation(5_000))
	if !errors.Is(err, boom) {
		t.Fatalf("prediction store: %v", err)
	}
	blocks.Err = boom
	_, err = usecases.NewPredict("base", []int{2}, gasometerParams, usecases.PredictDeps{
		Predictors: []services.Predictor{services.NewNodePredictor()}, Blocks: blocks,
		Predictions: &mocks.PredictionStore{}, Log: nopLog,
	}).Execute(t.Context(), observation(5_000))
	if !errors.Is(err, boom) {
		t.Fatalf("block store: %v", err)
	}
}
