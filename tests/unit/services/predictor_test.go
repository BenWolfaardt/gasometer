package services_test

import (
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

func snapshot(next, nodeTip int64) entities.FeeSnapshot {
	return entities.FeeSnapshot{NextBaseFee: wei(next), NodeTip: wei(nodeTip)}
}

func TestBaselinePredictors(t *testing.T) {
	tests := []struct {
		predictor services.Predictor
		name      string
		maxFee    int64
	}{
		{services.NewNodePredictor(), "node", 1_000 + 7},
		{services.NewWalletPredictor(), "wallet", 2_000 + 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.predictor.Name() != tt.name {
				t.Fatalf("name %q", tt.predictor.Name())
			}
			bid, err := tt.predictor.Predict(services.PredictionInput{Snapshot: snapshot(1_000, 7)})
			if err != nil {
				t.Fatal(err)
			}
			if !eqWei(bid.Tip, 7) || !eqWei(bid.MaxFee, tt.maxFee) || !eqWei(bid.ExpectedBaseFee, 1_000) {
				t.Fatalf("got %+v", bid)
			}
			if _, err := tt.predictor.Predict(services.PredictionInput{}); !errors.Is(err, entities.ErrInvalidBid) {
				t.Fatalf("empty snapshot: %v", err)
			}
		})
	}
}

type fixedProjector struct {
	proj services.BaseFeeProjection
	err  error
	got  services.ProjectionInput
}

func (p *fixedProjector) Project(in services.ProjectionInput) (services.BaseFeeProjection, error) {
	p.got = in
	return p.proj, p.err
}

func TestGasometerPredictor(t *testing.T) {
	// Required tips 1..10 over the last 10 blocks; older blocks carry a tip of 100 outside the window.
	tips := append(repeat(100, 5), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	history := blocks(1, repeat(1_000, len(tips)), tips)
	projector := &fixedProjector{proj: services.BaseFeeProjection{Expected: wei(1_050), Upper: wei(1_200)}}
	p := services.NewGasometerPredictor(0.9, 10, projector)
	if p.Name() != "gasometer" {
		t.Fatalf("name %q", p.Name())
	}
	in := services.PredictionInput{
		Snapshot: snapshot(1_000, 999),
		History:  history,
		Horizon:  entities.Horizon{Seconds: 30, Blocks: 15},
		Params:   &baseParams,
	}
	bid, err := p.Predict(in)
	if err != nil {
		t.Fatal(err)
	}
	if !eqWei(bid.Tip, 9) || !eqWei(bid.MaxFee, 1_209) || !eqWei(bid.ExpectedBaseFee, 1_050) {
		t.Fatalf("got tip %s max %s expected %s", bid.Tip, bid.MaxFee, bid.ExpectedBaseFee)
	}
	if projector.got.Blocks != 15 || projector.got.Params != &baseParams || !eqWei(projector.got.NextBaseFee, 1_000) ||
		len(projector.got.History) != len(history) {
		t.Fatalf("projector saw %+v", projector.got)
	}
}

func TestGasometerPredictorErrors(t *testing.T) {
	ok := &fixedProjector{proj: services.BaseFeeProjection{Expected: wei(1), Upper: wei(1)}}
	history := blocks(1, repeat(1, 5), repeat(0, 5))
	tests := []struct {
		name      string
		predictor services.Predictor
		in        services.PredictionInput
		want      error
	}{
		{"short history", services.NewGasometerPredictor(0.9, 6, ok),
			services.PredictionInput{Snapshot: snapshot(1, 1), History: history}, entities.ErrInsufficientHistory},
		{"zero tip window", services.NewGasometerPredictor(0.9, 0, ok),
			services.PredictionInput{Snapshot: snapshot(1, 1), History: history}, entities.ErrInvalidConfig},
		{"missing next base fee", services.NewGasometerPredictor(0.9, 5, ok),
			services.PredictionInput{History: history}, entities.ErrInvalidBid},
		{"projector error", services.NewGasometerPredictor(0.9, 5, &fixedProjector{err: entities.ErrInsufficientHistory}),
			services.PredictionInput{Snapshot: snapshot(1, 1), History: history}, entities.ErrInsufficientHistory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.predictor.Predict(tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}
