package poll_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"

	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/poll"
	"github.com/BenWolfaardt/gasometer/predict"
)

var gasometerParams = predict.Params{TipQuantile: 0.9, TipWindow: 30, GrowthQuantile: 0.99, GrowthWindow: time.Hour}

func observation(atBlock uint64) poll.Observation {
	return poll.Observation{Snapshot: gasometer.FeeSnapshot{
		ID: 7, Network: "base", AtBlock: atBlock, ObservedAt: t0, NextBaseFee: wei(10_000_000), NodeTip: wei(1_000),
	}}
}

func storeBlocks(t *testing.T, store *mock.BlockStore, from, to uint64) {
	t.Helper()
	var blocks []gasometer.BlockFee
	for n := from; n <= to; n++ {
		blocks = append(blocks, gasometer.BlockFee{Number: n, BaseFee: wei(10_000_000), RequiredTip: wei(int64(n % 7)), ObservedAt: t0})
	}
	if err := store.Upsert(t.Context(), "base", blocks); err != nil {
		t.Fatal(err)
	}
}

type recordingPredictor struct {
	gasometer.Predictor
	inputs []gasometer.PredictionInput
}

func (r *recordingPredictor) Predict(in gasometer.PredictionInput) (gasometer.Bid, error) {
	r.inputs = append(r.inputs, in)
	return r.Predictor.Predict(in)
}

func TestForecaster(t *testing.T) {
	blocks := mock.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	storeBlocks(t, blocks, 1, 5_000)
	predictions := &mock.PredictionStore{}
	recorded := &recordingPredictor{Predictor: predict.NewGasometer(0.9, 30, predict.NewEmpiricalProjector(0.99))}
	uc := poll.NewForecaster("base", []int{2, 30, 120}, gasometerParams, poll.ForecasterDeps{
		Predictors:  []gasometer.Predictor{predict.NewNode(), predict.NewWallet(), recorded},
		Blocks:      blocks,
		Predictions: predictions,
		Log:         nopLog,
	})
	n, err := uc.Forecast(t.Context(), observation(5_000))
	if err != nil {
		t.Fatal(err)
	}
	if n != 9 || len(predictions.Predictions) != 9 {
		t.Fatalf("made %d, stored %d, want 9", n, len(predictions.Predictions))
	}
	wantBlocks := map[int]int{2: 1, 30: 15, 120: 60}
	for _, p := range predictions.Predictions {
		if p.Horizon.Blocks != wantBlocks[p.Horizon.Seconds] || p.SnapshotID != 7 || p.AtBlock != 5_000 ||
			p.Status != gasometer.PredictionPending || !p.MadeAt.Equal(t0) || p.Network != "base" {
			t.Fatalf("prediction %+v", p)
		}
	}
	// One hour at 2 s per block is 1,800 blocks.
	h := recorded.inputs[0].History
	if len(h) != 1_800 || h[0].Number != 3_201 || h[len(h)-1].Number != 5_000 {
		t.Fatalf("history %d blocks from %d", len(h), h[0].Number)
	}
}

func TestForecasterLoadsTipWindowsOfLongestHorizon(t *testing.T) {
	blocks := mock.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	storeBlocks(t, blocks, 1, 5_000)
	params := gasometerParams
	params.TipWindow = 1_800
	recorded := &recordingPredictor{Predictor: predict.NewGasometer(0.99, 1_800, predict.NewEmpiricalProjector(0.99))}
	uc := poll.NewForecaster("base", []int{2, 30, 120}, params, poll.ForecasterDeps{
		Predictors:  []gasometer.Predictor{recorded},
		Blocks:      blocks,
		Predictions: &mock.PredictionStore{},
		Log:         nopLog,
	})
	if n, err := uc.Forecast(t.Context(), observation(5_000)); err != nil || n != 3 {
		t.Fatalf("made %d: %v", n, err)
	}
	// 1,800 windows of the 60-block horizon reach 1,859 blocks back, past the hour of growth history.
	if h := recorded.inputs[0].History; len(h) != 1_859 || h[0].Number != 3_142 {
		t.Fatalf("history %d blocks from %d", len(h), h[0].Number)
	}
}

func TestForecasterWaitsForBlockTime(t *testing.T) {
	predictions := &mock.PredictionStore{}
	n, err := poll.NewForecaster("base", []int{2}, gasometerParams, poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{predict.NewNode()}, Blocks: mock.NewBlockStore(),
		Predictions: predictions, Log: nopLog,
	}).Forecast(t.Context(), observation(5_000))
	if err != nil || n != 0 || len(predictions.Predictions) != 0 {
		t.Fatalf("n %d, err %v", n, err)
	}
}

func TestForecasterSkipsPredictorsWithoutHistory(t *testing.T) {
	blocks := mock.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	storeBlocks(t, blocks, 4_990, 5_000)
	predictions := &mock.PredictionStore{}
	n, err := poll.NewForecaster("base", []int{30}, gasometerParams, poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{
			predict.NewGasometer(0.9, 30, predict.NewEmpiricalProjector(0.99)), predict.NewNode(),
		},
		Blocks: blocks, Predictions: predictions, Log: nopLog,
	}).Forecast(t.Context(), observation(5_000))
	if err != nil || n != 1 || predictions.Predictions[0].Predictor != "node" {
		t.Fatalf("n %d, err %v, %+v", n, err, predictions.Predictions)
	}
}

func TestForecasterStoreErrors(t *testing.T) {
	boom := errors.New("boom")
	blocks := mock.NewBlockStore()
	blocks.AvgTime["base"] = 2 * time.Second
	_, err := poll.NewForecaster("base", []int{2}, gasometerParams, poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{predict.NewNode()}, Blocks: blocks,
		Predictions: &mock.PredictionStore{Err: boom}, Log: nopLog,
	}).Forecast(t.Context(), observation(5_000))
	if !errors.Is(err, boom) {
		t.Fatalf("prediction store: %v", err)
	}
	blocks.Err = boom
	_, err = poll.NewForecaster("base", []int{2}, gasometerParams, poll.ForecasterDeps{
		Predictors: []gasometer.Predictor{predict.NewNode()}, Blocks: blocks,
		Predictions: &mock.PredictionStore{}, Log: nopLog,
	}).Forecast(t.Context(), observation(5_000))
	if !errors.Is(err, boom) {
		t.Fatalf("block store: %v", err)
	}
}

func TestHorizons(t *testing.T) {
	tests := []struct {
		name string
		avg  time.Duration
		want []int
	}{
		{"base and op", 2 * time.Second, []int{1, 15, 60}},
		{"c-chain", 940 * time.Millisecond, []int{2, 32, 128}},
		{"slow chain floors at one block", 12 * time.Second, []int{1, 3, 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := poll.Horizons([]int{2, 30, 120}, tt.avg)
			if err != nil {
				t.Fatal(err)
			}
			for i, h := range got {
				if h.Blocks != tt.want[i] || h.Seconds != []int{2, 30, 120}[i] {
					t.Fatalf("got %+v, want blocks %v", got, tt.want)
				}
			}
		})
	}
	if _, err := poll.Horizons([]int{2}, 0); !errors.Is(err, gasometer.ErrInvalidHorizon) {
		t.Fatalf("zero block time: %v", err)
	}
	if _, err := poll.BlocksIn(0, time.Second); !errors.Is(err, gasometer.ErrInvalidHorizon) {
		t.Fatalf("zero duration: %v", err)
	}
}
