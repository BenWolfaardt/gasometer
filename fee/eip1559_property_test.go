package fee_test

import (
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
)

const propertyRuns = 20_000

type feeCase struct {
	params   gasometer.FeeParams
	parent   *big.Int
	gasUsed  uint64
	gasLimit uint64
}

// randomFeeCase draws parameters across their whole range, including base fees far beyond uint256 and gas limits
// that are always a multiple of the elasticity, so the full-block bound is exact.
func randomFeeCase(r *rand.Rand) feeCase {
	params := gasometer.FeeParams{
		Denominator: 1 + r.Uint64N(1_000),
		Elasticity:  1 + r.Uint64N(20),
	}
	if r.IntN(2) == 0 {
		params.MinBaseFee = new(big.Int).SetUint64(r.Uint64N(1 << 40))
	}
	parent := randomBig(r, r.IntN(300))
	gasLimit := params.Elasticity * (1 + r.Uint64N(1<<40))
	return feeCase{params: params, parent: parent, gasUsed: r.Uint64N(gasLimit + 1), gasLimit: gasLimit}
}

// randomBig is a uniformly random integer below 2^bits.
func randomBig(r *rand.Rand, bits int) *big.Int {
	buf := make([]byte, (bits+7)/8)
	for i := range buf {
		buf[i] = byte(r.Uint32())
	}
	n := new(big.Int).SetBytes(buf)
	return n.Rsh(n, uint(len(buf)*8-bits))
}

func maxBig(a, b *big.Int) *big.Int {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

func TestPropertyNextBaseFeeBoundedByPerBlockLimit(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range propertyRuns {
		c := randomFeeCase(r)
		next, err := fee.NextBaseFee(c.params, c.parent, c.gasUsed, c.gasLimit)
		if err != nil {
			t.Fatal(err)
		}
		upper, err := fee.MaxBaseFeeAfter(c.params, c.parent, 1)
		if err != nil {
			t.Fatal(err)
		}
		if next.Cmp(upper) > 0 {
			t.Fatalf("%+v: next %s above one-block bound %s", c, next, upper)
		}
		// An empty block lowers the fee by at most parent/denominator.
		lower := new(big.Int).Sub(c.parent, new(big.Int).Quo(c.parent, new(big.Int).SetUint64(c.params.Denominator)))
		if c.params.MinBaseFee != nil {
			lower = maxBig(lower, c.params.MinBaseFee)
		}
		if next.Cmp(lower) < 0 {
			t.Fatalf("%+v: next %s below one-block floor %s", c, next, lower)
		}
	}
}

func TestPropertyNextBaseFeeNeverBelowMinimum(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for range propertyRuns {
		c := randomFeeCase(r)
		c.params.MinBaseFee = new(big.Int).SetUint64(r.Uint64())
		next, err := fee.NextBaseFee(c.params, c.parent, c.gasUsed, c.gasLimit)
		if err != nil {
			t.Fatal(err)
		}
		if next.Cmp(c.params.MinBaseFee) < 0 {
			t.Fatalf("%+v: next %s below minimum %s", c, next, c.params.MinBaseFee)
		}
	}
}

func TestPropertyNextBaseFeeMonotonicInGasUsed(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for range propertyRuns {
		c := randomFeeCase(r)
		more := c.gasUsed + r.Uint64N(c.gasLimit-c.gasUsed+1)
		low, err := fee.NextBaseFee(c.params, c.parent, c.gasUsed, c.gasLimit)
		if err != nil {
			t.Fatal(err)
		}
		high, err := fee.NextBaseFee(c.params, c.parent, more, c.gasLimit)
		if err != nil {
			t.Fatal(err)
		}
		if low.Cmp(high) > 0 {
			t.Fatalf("%+v: gas %d gives %s, more gas %d gives %s", c, c.gasUsed, low, more, high)
		}
	}
}

func TestPropertyMaxBaseFeeAfterMonotonicInBlocks(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	for range propertyRuns / 10 {
		c := randomFeeCase(r)
		k := r.IntN(100)
		a, err := fee.MaxBaseFeeAfter(c.params, c.parent, k)
		if err != nil {
			t.Fatal(err)
		}
		b, err := fee.MaxBaseFeeAfter(c.params, c.parent, k+1)
		if err != nil {
			t.Fatal(err)
		}
		if a.Cmp(b) > 0 {
			t.Fatalf("%+v: k=%d gives %s, k+1 gives %s", c, k, a, b)
		}
	}
}
