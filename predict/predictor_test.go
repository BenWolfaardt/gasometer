package predict_test

import (
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/predict"
)

func snapshot(next, nodeTip int64) gasometer.FeeSnapshot {
	return gasometer.FeeSnapshot{NextBaseFee: wei(next), NodeTip: wei(nodeTip)}
}

func TestBaselinePredictors(t *testing.T) {
	tests := []struct {
		predictor gasometer.Predictor
		name      string
		maxFee    int64
	}{
		{predict.NewNode(), "node", 1_000 + 7},
		{predict.NewWallet(), "wallet", 2_000 + 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.predictor.Name() != tt.name {
				t.Fatalf("name %q", tt.predictor.Name())
			}
			bid, err := tt.predictor.Predict(gasometer.PredictionInput{Snapshot: snapshot(1_000, 7)})
			if err != nil {
				t.Fatal(err)
			}
			if !eqWei(bid.Tip, 7) || !eqWei(bid.MaxFee, tt.maxFee) || !eqWei(bid.ExpectedBaseFee, 1_000) {
				t.Fatalf("got %+v", bid)
			}
			if _, err := tt.predictor.Predict(gasometer.PredictionInput{}); !errors.Is(err, gasometer.ErrInvalidBid) {
				t.Fatalf("empty snapshot: %v", err)
			}
		})
	}
}

func TestMarketPredictor(t *testing.T) {
	p := predict.NewMarket()
	if p.Name() != "market" {
		t.Fatalf("name %q", p.Name())
	}
	history := blocks(1, repeat(1_000, 3), repeat(1, 3))
	history[1].MedianTip, history[2].MedianTip = wei(500), wei(40)
	bid, err := p.Predict(gasometer.PredictionInput{Snapshot: snapshot(1_000, 7), History: history})
	if err != nil {
		t.Fatal(err)
	}
	if !eqWei(bid.Tip, 40) || !eqWei(bid.MaxFee, 2_040) || !eqWei(bid.ExpectedBaseFee, 1_000) {
		t.Fatalf("got %+v", bid)
	}
	if _, err := p.Predict(gasometer.PredictionInput{Snapshot: snapshot(1_000, 7)}); !errors.Is(err, gasometer.ErrInsufficientHistory) {
		t.Fatalf("no history: %v", err)
	}
	if _, err := p.Predict(gasometer.PredictionInput{History: history}); !errors.Is(err, gasometer.ErrInvalidBid) {
		t.Fatalf("empty snapshot: %v", err)
	}
}

type fixedProjector struct {
	proj predict.BaseFeeProjection
	err  error
	got  predict.ProjectionInput
}

func (p *fixedProjector) Project(in predict.ProjectionInput) (predict.BaseFeeProjection, error) {
	p.got = in
	return p.proj, p.err
}

func TestGasometerPredictor(t *testing.T) {
	// Required tips 1..10 over the last 10 blocks; older blocks carry a tip of 100.
	tips := append(repeat(100, 5), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	history := blocks(1, repeat(1_000, len(tips)), tips)
	tests := []struct {
		name   string
		blocks int
		tip    int64
	}{
		// The 0.9 quantile of 1..10.
		{"one block", 1, 9},
		// Three-block windows ending at each of the last 10 blocks have minima 1, 1, 1, 2, 3, ..., 8.
		{"three blocks", 3, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projector := &fixedProjector{proj: predict.BaseFeeProjection{Expected: wei(1_050), Upper: wei(1_200)}}
			p := predict.NewGasometer(0.9, 10, projector)
			if p.Name() != "gasometer" {
				t.Fatalf("name %q", p.Name())
			}
			bid, err := p.Predict(gasometer.PredictionInput{
				Snapshot: snapshot(1_000, 999),
				History:  history,
				Horizon:  gasometer.Horizon{Seconds: 30, Blocks: tt.blocks},
				Params:   &baseParams,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !eqWei(bid.Tip, tt.tip) || !eqWei(bid.MaxFee, 1_200+tt.tip) || !eqWei(bid.ExpectedBaseFee, 1_050) {
				t.Fatalf("got tip %s max %s expected %s", bid.Tip, bid.MaxFee, bid.ExpectedBaseFee)
			}
			if projector.got.Blocks != tt.blocks || projector.got.Params != &baseParams ||
				!eqWei(projector.got.NextBaseFee, 1_000) || len(projector.got.History) != len(history) {
				t.Fatalf("projector saw %+v", projector.got)
			}
		})
	}
}

func TestGasometerPredictorErrors(t *testing.T) {
	ok := &fixedProjector{proj: predict.BaseFeeProjection{Expected: wei(1), Upper: wei(1)}}
	history := blocks(1, repeat(1, 5), repeat(0, 5))
	tests := []struct {
		name      string
		predictor gasometer.Predictor
		in        gasometer.PredictionInput
		want      error
	}{
		{"short history", predict.NewGasometer(0.9, 6, ok),
			gasometer.PredictionInput{Snapshot: snapshot(1, 1), History: history}, gasometer.ErrInsufficientHistory},
		{"short history for the horizon", predict.NewGasometer(0.9, 4, ok),
			gasometer.PredictionInput{Snapshot: snapshot(1, 1), History: history, Horizon: gasometer.Horizon{Blocks: 3}},
			gasometer.ErrInsufficientHistory},
		{"zero tip window", predict.NewGasometer(0.9, 0, ok),
			gasometer.PredictionInput{Snapshot: snapshot(1, 1), History: history}, gasometer.ErrInvalidConfig},
		{"missing next base fee", predict.NewGasometer(0.9, 5, ok),
			gasometer.PredictionInput{History: history}, gasometer.ErrInvalidBid},
		{"projector error", predict.NewGasometer(0.9, 5, &fixedProjector{err: gasometer.ErrInsufficientHistory}),
			gasometer.PredictionInput{Snapshot: snapshot(1, 1), History: history}, gasometer.ErrInsufficientHistory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.predictor.Predict(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}
