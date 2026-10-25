package services_test

import (
	"errors"
	"math/big"
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

var (
	ethereumParams = entities.FeeParams{Denominator: 8, Elasticity: 2}
	baseParams     = entities.FeeParams{Denominator: 100, Elasticity: 5, MinBaseFee: big.NewInt(5_000_000)}
	opParams       = entities.FeeParams{Denominator: 250, Elasticity: 2, MinBaseFee: big.NewInt(0)}
)

func TestNextBaseFee(t *testing.T) {
	tests := []struct {
		name     string
		params   entities.FeeParams
		parent   int64
		gasUsed  uint64
		gasLimit uint64
		want     int64
	}{
		{"at target is unchanged", ethereumParams, 1_000_000_000, 15_000_000, 30_000_000, 1_000_000_000},
		{"full block rises 12.5%", ethereumParams, 1_000_000_000, 30_000_000, 30_000_000, 1_125_000_000},
		{"empty block falls 12.5%", ethereumParams, 1_000_000_000, 0, 30_000_000, 875_000_000},
		{"tiny rise rounds up to 1 wei", ethereumParams, 7, 15_000_001, 30_000_000, 8},
		{"tiny fall rounds to zero change", ethereumParams, 7, 14_999_999, 30_000_000, 7},
		{"zero base fee stays zero below target", ethereumParams, 0, 0, 30_000_000, 0},
		{"zero base fee rises 1 wei above target", ethereumParams, 0, 30_000_000, 30_000_000, 1},
		{"base full block rises 4%", entities.FeeParams{Denominator: 100, Elasticity: 5}, 10_000_000, 300_000_000, 300_000_000, 10_400_000},
		{"base empty block falls to the minimum", baseParams, 5_000_000, 0, 300_000_000, 5_000_000},
		{"base below the minimum is lifted", baseParams, 1_000_000, 60_000_000, 300_000_000, 5_000_000},
		{"op full block rises 0.4%", opParams, 1_000_000, 40_000_000, 40_000_000, 1_004_000},
		{"metered gas above the limit still applies", ethereumParams, 1_000_000_000, 45_000_000, 30_000_000, 1_250_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := services.NextBaseFee(tt.params, big.NewInt(tt.parent), tt.gasUsed, tt.gasLimit)
			if err != nil {
				t.Fatalf("NextBaseFee: %v", err)
			}
			if got.Cmp(big.NewInt(tt.want)) != 0 {
				t.Fatalf("got %s, want %d", got, tt.want)
			}
		})
	}
}

func TestNextBaseFeeRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		params   entities.FeeParams
		parent   *big.Int
		gasLimit uint64
	}{
		{"zero denominator", entities.FeeParams{Elasticity: 2}, big.NewInt(1), 30_000_000},
		{"zero elasticity", entities.FeeParams{Denominator: 8}, big.NewInt(1), 30_000_000},
		{"negative minimum", entities.FeeParams{Denominator: 8, Elasticity: 2, MinBaseFee: big.NewInt(-1)}, big.NewInt(1), 30_000_000},
		{"nil parent", ethereumParams, nil, 30_000_000},
		{"negative parent", ethereumParams, big.NewInt(-1), 30_000_000},
		{"gas limit below elasticity", ethereumParams, big.NewInt(1), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := services.NextBaseFee(tt.params, tt.parent, 0, tt.gasLimit)
			if !errors.Is(err, entities.ErrInvalidFeeParams) {
				t.Fatalf("got %v, want ErrInvalidFeeParams", err)
			}
		})
	}
}

func TestMaxBaseFeeAfter(t *testing.T) {
	tests := []struct {
		name   string
		params entities.FeeParams
		fee    int64
		k      int
		want   int64
	}{
		{"zero blocks is the input", baseParams, 10_000_000, 0, 10_000_000},
		{"one block on base is 4%", baseParams, 10_000_000, 1, 10_400_000},
		{"two blocks compound", baseParams, 10_000_000, 2, 10_816_000},
		{"one block on op is 0.4%", opParams, 1_000_000, 1, 1_004_000},
		{"one block on ethereum is 12.5%", ethereumParams, 1_000_000_000, 1, 1_125_000_000},
		{"rounding floor is 1 wei per block", opParams, 10, 3, 13},
		{"starts from the minimum", baseParams, 0, 1, 5_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := services.MaxBaseFeeAfter(tt.params, big.NewInt(tt.fee), tt.k)
			if err != nil {
				t.Fatalf("MaxBaseFeeAfter: %v", err)
			}
			if got.Cmp(big.NewInt(tt.want)) != 0 {
				t.Fatalf("got %s, want %d", got, tt.want)
			}
		})
	}
}

func TestMaxBaseFeeAfterMatchesFullBlocks(t *testing.T) {
	const gasLimit = 300_000_000
	fee := big.NewInt(123_456_789)
	for k := 1; k <= 60; k++ {
		next, err := services.NextBaseFee(baseParams, fee, gasLimit, gasLimit)
		if err != nil {
			t.Fatal(err)
		}
		fee = next
		bound, err := services.MaxBaseFeeAfter(baseParams, big.NewInt(123_456_789), k)
		if err != nil {
			t.Fatal(err)
		}
		if bound.Cmp(fee) != 0 {
			t.Fatalf("k=%d: bound %s, full blocks %s", k, bound, fee)
		}
	}
}

func TestMaxBaseFeeAfterRejectsInvalidInput(t *testing.T) {
	if _, err := services.MaxBaseFeeAfter(baseParams, big.NewInt(1), -1); !errors.Is(err, entities.ErrInvalidHorizon) {
		t.Fatalf("negative k: got %v", err)
	}
	if _, err := services.MaxBaseFeeAfter(entities.FeeParams{}, big.NewInt(1), 1); !errors.Is(err, entities.ErrInvalidFeeParams) {
		t.Fatalf("zero params: got %v", err)
	}
	if _, err := services.MaxBaseFeeAfter(baseParams, nil, 1); !errors.Is(err, entities.ErrInvalidFeeParams) {
		t.Fatalf("nil fee: got %v", err)
	}
}

func BenchmarkNextBaseFee(b *testing.B) {
	parent := big.NewInt(1_000_000_000)
	for b.Loop() {
		_, _ = services.NextBaseFee(baseParams, parent, 200_000_000, 300_000_000)
	}
}

func BenchmarkMaxBaseFeeAfter60(b *testing.B) {
	fee := big.NewInt(1_000_000_000)
	for b.Loop() {
		_, _ = services.MaxBaseFeeAfter(baseParams, fee, 60)
	}
}
