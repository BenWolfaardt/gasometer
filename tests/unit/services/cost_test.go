package services_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

func TestTxCostsMissedIsNil(t *testing.T) {
	score := entities.Score{RequiredBaseFee: wei(1), RequiredTip: wei(1)}
	if got := services.TxCosts(score, entities.FeeSnapshot{}, entities.PriceQuote{Answer: wei(1)}, nil); got != nil {
		t.Fatalf("got %+v", got)
	}
	if services.OverpayRatio(score) != nil {
		t.Fatal("missed bid has an overpay ratio")
	}
}

func TestOverpayRatioZeroRequired(t *testing.T) {
	score := entities.Score{Included: true, PaidBaseFee: wei(0), PaidTip: wei(3), RequiredBaseFee: wei(0), RequiredTip: wei(0)}
	if services.OverpayRatio(score) != nil {
		t.Fatal("zero required must have no ratio")
	}
}

func TestTxCostsWithoutL1Fee(t *testing.T) {
	score := entities.Score{Included: true, PaidBaseFee: wei(1e9), PaidTip: wei(1e9), RequiredBaseFee: wei(1e9), RequiredTip: wei(0)}
	price := entities.PriceQuote{Answer: wei(25_00000000), Decimals: 8}
	got := services.TxCosts(score, entities.FeeSnapshot{}, price, []entities.ReferenceTx{{Name: "native", Gas: 21_000}})
	// 21,000 gas × 1 gwei = 2.1e-5 AVAX, at 25 USD.
	if len(got) != 1 || got[0].Paid.BaseFee != 25*2.1e-5 || got[0].Paid.L1Fee != 0 || got[0].Required.Tip != 0 {
		t.Fatalf("got %+v", got)
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
			got, err := services.Horizons([]int{2, 30, 120}, tt.avg)
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
	if _, err := services.Horizons([]int{2}, 0); !errors.Is(err, entities.ErrInvalidHorizon) {
		t.Fatalf("zero block time: %v", err)
	}
	if _, err := services.BlocksIn(0, time.Second); !errors.Is(err, entities.ErrInvalidHorizon) {
		t.Fatalf("zero duration: %v", err)
	}
}
