package services_test

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/ava-labs/libevm/consensus/misc/eip1559"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/params"

	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

// libevmNextBaseFee is the reference, at Ethereum's fixed parameters: denominator 8, elasticity 2, no minimum.
func libevmNextBaseFee(parent *big.Int, gasUsed, gasLimit uint64) *big.Int {
	return eip1559.CalcBaseFee(params.TestChainConfig, &types.Header{
		Number:   big.NewInt(1),
		BaseFee:  parent,
		GasUsed:  gasUsed,
		GasLimit: gasLimit,
	})
}

func TestDifferentialNextBaseFeeMatchesLibevm(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	for range propertyRuns {
		parent := randomBig(r, r.IntN(260))
		gasLimit := 2 + r.Uint64N(1<<45)
		gasUsed := r.Uint64N(gasLimit + 1)
		assertMatchesLibevm(t, parent, gasUsed, gasLimit)
	}
}

func FuzzNextBaseFee(f *testing.F) {
	f.Add([]byte{0x3b, 0x9a, 0xca, 0x00}, uint64(30_000_000), uint64(30_000_000))
	f.Add([]byte{0x07}, uint64(15_000_001), uint64(30_000_000))
	f.Add([]byte{}, uint64(0), uint64(2))
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, uint64(1<<63), uint64(1<<64-1))
	f.Fuzz(func(t *testing.T, parentBytes []byte, gasUsed, gasLimit uint64) {
		if len(parentBytes) > 64 {
			parentBytes = parentBytes[:64]
		}
		parent := new(big.Int).SetBytes(parentBytes)
		if gasLimit < ethereumParams.Elasticity {
			if _, err := services.NextBaseFee(ethereumParams, parent, gasUsed, gasLimit); err == nil {
				t.Fatalf("gas limit %d: want error", gasLimit)
			}
			return
		}
		assertMatchesLibevm(t, parent, gasUsed, gasLimit)
	})
}

func assertMatchesLibevm(t *testing.T, parent *big.Int, gasUsed, gasLimit uint64) {
	t.Helper()
	got, err := services.NextBaseFee(ethereumParams, new(big.Int).Set(parent), gasUsed, gasLimit)
	if err != nil {
		t.Fatalf("NextBaseFee(%s, %d, %d): %v", parent, gasUsed, gasLimit, err)
	}
	want := libevmNextBaseFee(new(big.Int).Set(parent), gasUsed, gasLimit)
	if got.Cmp(want) != 0 {
		t.Fatalf("NextBaseFee(%s, %d, %d) = %s, libevm %s", parent, gasUsed, gasLimit, got, want)
	}
}
