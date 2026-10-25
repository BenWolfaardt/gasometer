package services_test

import (
	"errors"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

func TestRewardPercentiles(t *testing.T) {
	if got := services.RewardPercentiles(5); !slices.Equal(got, []float64{5, 50}) {
		t.Fatalf("got %v", got)
	}
}

func TestBlockFees(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	history := entities.FeeHistory{
		OldestBlock:   100,
		BaseFees:      []*big.Int{wei(10), wei(11), wei(12), wei(13), wei(14)},
		GasUsedRatios: []float64{0.3, 0, 0.2, 0.4},
		Rewards: [][]*big.Int{
			{wei(1), wei(5)},
			{},
			{wei(2)},
		},
	}
	got, missing, err := services.BlockFees(history, at)
	if err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Fatalf("missing rewards %d, want 2: short and absent, not the empty block", missing)
	}
	want := []struct {
		number            uint64
		base, req, median int64
	}{
		{100, 10, 1, 5},
		{101, 11, 0, 0},
		{102, 12, 0, 0},
		{103, 13, 0, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("%d blocks, want %d", len(got), len(want))
	}
	for i, w := range want {
		b := got[i]
		if b.Number != w.number || !eqWei(b.BaseFee, w.base) || !eqWei(b.RequiredTip, w.req) ||
			!eqWei(b.MedianTip, w.median) || !b.ObservedAt.Equal(at) {
			t.Fatalf("block %d: %+v, want %+v", i, b, w)
		}
	}
	if got[0].GasUsedRatio != 0.3 {
		t.Fatalf("ratio %v", got[0].GasUsedRatio)
	}
}

func TestBlockFeesCopiesValues(t *testing.T) {
	history := entities.FeeHistory{
		OldestBlock:   1,
		BaseFees:      []*big.Int{wei(10), wei(11)},
		GasUsedRatios: []float64{0.5},
		Rewards:       [][]*big.Int{{wei(1), wei(2)}},
	}
	got, _, err := services.BlockFees(history, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	history.BaseFees[0].SetInt64(99)
	history.Rewards[0][0].SetInt64(99)
	if !eqWei(got[0].BaseFee, 10) || !eqWei(got[0].RequiredTip, 1) {
		t.Fatalf("block aliases the history: %+v", got[0])
	}
}

func TestBlockFeesRejectsMalformedHistory(t *testing.T) {
	_, _, err := services.BlockFees(entities.FeeHistory{
		BaseFees:      []*big.Int{wei(1)},
		GasUsedRatios: []float64{0.5},
	}, time.Time{})
	if !errors.Is(err, entities.ErrMalformedFeeHistory) {
		t.Fatalf("got %v", err)
	}
}
