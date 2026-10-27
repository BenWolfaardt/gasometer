package predict_test

import (
	"errors"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/predict"
)

func TestEmpiricalProjector(t *testing.T) {
	// A 3-block horizon spans 2 blocks of growth: 121/100, 100/110, 100/121, 100/100, sorted 0.826, 0.909, 1, 1.21.
	history := blocks(1, []int64{100, 110, 121, 100, 100, 100}, repeat(0, 6))
	tests := []struct {
		name            string
		upperQuantile   float64
		k               int
		next            int64
		expected, upper int64
	}{
		{"one block is the next base fee", 0.99, 1, 1_000, 1_000, 1_000},
		{"median and top growth", 0.99, 3, 1_000, 909, 1_210},
		{"lowest quantile rounds up", 0.01, 3, 1_000, 909, 827},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proj, err := predict.NewEmpiricalProjector(tt.upperQuantile).Project(predict.ProjectionInput{
				NextBaseFee: wei(tt.next), History: history, Blocks: tt.k,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !eqWei(proj.Expected, tt.expected) || !eqWei(proj.Upper, tt.upper) {
				t.Fatalf("got expected %s upper %s, want %d %d", proj.Expected, proj.Upper, tt.expected, tt.upper)
			}
		})
	}
}

func TestEmpiricalProjectorRoundsUpperUp(t *testing.T) {
	history := blocks(1, []int64{3, 4}, repeat(0, 2))
	proj, err := predict.NewEmpiricalProjector(0.99).Project(predict.ProjectionInput{
		NextBaseFee: wei(10), History: history, Blocks: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 10 × 4/3 = 13.33: expected floors, upper ceils.
	if !eqWei(proj.Expected, 13) || !eqWei(proj.Upper, 14) {
		t.Fatalf("got %s %s", proj.Expected, proj.Upper)
	}
}

func TestEmpiricalProjectorSkipsGapsAndZeroFees(t *testing.T) {
	history := append(blocks(1, []int64{0, 100}, repeat(0, 2)), blocks(10, []int64{100, 200}, repeat(0, 2))...)
	proj, err := predict.NewEmpiricalProjector(0.99).Project(predict.ProjectionInput{
		NextBaseFee: wei(10), History: history, Blocks: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only 10→11 counts: 1→2 starts at zero and 2→10 is not one block apart.
	if !eqWei(proj.Upper, 20) || !eqWei(proj.Expected, 20) {
		t.Fatalf("got %s %s", proj.Expected, proj.Upper)
	}
}

func TestEmpiricalProjectorErrors(t *testing.T) {
	p := predict.NewEmpiricalProjector(0.99)
	if _, err := p.Project(predict.ProjectionInput{NextBaseFee: wei(1), Blocks: 0}); !errors.Is(err, gasometer.ErrInvalidHorizon) {
		t.Fatalf("zero blocks: %v", err)
	}
	short := blocks(1, []int64{1, 1}, repeat(0, 2))
	if _, err := p.Project(predict.ProjectionInput{NextBaseFee: wei(1), History: short, Blocks: 3}); !errors.Is(err, gasometer.ErrInsufficientHistory) {
		t.Fatalf("short history: %v", err)
	}
}

func TestWorstCaseProjector(t *testing.T) {
	// Growth of 2× over one block, far above Base's 4% ceiling.
	history := blocks(1, []int64{100, 200, 400}, repeat(0, 3))
	in := predict.ProjectionInput{NextBaseFee: wei(10_000_000), History: history, Blocks: 2}
	p := predict.NewWorstCaseProjector(predict.NewEmpiricalProjector(0.99))

	in.Ceiling = baseCeiling
	proj, err := p.Project(in)
	if err != nil {
		t.Fatal(err)
	}
	if !eqWei(proj.Upper, 10_400_000) || !eqWei(proj.Expected, 10_400_000) {
		t.Fatalf("capped: got %s %s", proj.Expected, proj.Upper)
	}

	in.Ceiling = nil
	proj, err = p.Project(in)
	if err != nil {
		t.Fatal(err)
	}
	if !eqWei(proj.Upper, 20_000_000) {
		t.Fatalf("no ceiling must pass through uncapped: got %s", proj.Upper)
	}
}

func TestWorstCaseProjectorKeepsLowerProjection(t *testing.T) {
	history := blocks(1, []int64{100, 101}, repeat(0, 2))
	proj, err := predict.NewWorstCaseProjector(predict.NewEmpiricalProjector(0.99)).Project(predict.ProjectionInput{
		NextBaseFee: wei(10_000_000), History: history, Blocks: 2, Ceiling: baseCeiling,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !eqWei(proj.Upper, 10_100_000) {
		t.Fatalf("got %s", proj.Upper)
	}
}

func TestWorstCaseProjectorPropagatesErrors(t *testing.T) {
	_, err := predict.NewWorstCaseProjector(predict.NewEmpiricalProjector(0.99)).Project(predict.ProjectionInput{
		NextBaseFee: wei(1), Blocks: 3, Ceiling: baseCeiling,
	})
	if !errors.Is(err, gasometer.ErrInsufficientHistory) {
		t.Fatalf("got %v", err)
	}
}
