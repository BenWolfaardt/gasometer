package sae_test

import (
	"encoding/json"
	"errors"
	"math/big"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/ava-labs/avalanchego/vms/components/gas"
	"github.com/ava-labs/avalanchego/vms/saevm/cchain/dynamic"
	"github.com/ava-labs/avalanchego/vms/saevm/gastime"
	"github.com/ava-labs/avalanchego/vms/saevm/worstcase"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/sae"
)

type fixture struct {
	NextBaseFee *big.Int             `json:"next_base_fee"`
	Blocks      []gasometer.SAEBlock `json:"blocks"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// window is the blocks from the settled block of blocks[i] through blocks[i]; ok is false when it starts before
// the fixture.
func window(blocks []gasometer.SAEBlock, i int) ([]gasometer.SAEBlock, bool) {
	first := blocks[0].Number
	settled := blocks[i].Settled.Height
	if settled < first {
		return nil, false
	}
	return blocks[settled-first : i+1], true
}

// cloneBlocks deep-copies blocks so a test can change one without touching the fixture.
func cloneBlocks(blocks []gasometer.SAEBlock) []gasometer.SAEBlock {
	out := make([]gasometer.SAEBlock, len(blocks))
	for i, b := range blocks {
		out[i] = b
		if b.Settled != nil {
			s := *b.Settled
			out[i].Settled = &s
		}
	}
	return out
}

func TestBaseFeeMatchesEveryRecordedHeader(t *testing.T) {
	blocks := loadFixture(t).Blocks
	checked, atomic, distinct := 0, 0, map[string]bool{}
	for i := range blocks {
		w, ok := window(blocks, i)
		if !ok {
			continue
		}
		got, err := sae.BaseFee(w)
		if err != nil {
			t.Fatalf("block %d: %v", blocks[i].Number, err)
		}
		if got.Cmp(blocks[i].BaseFee) != 0 {
			t.Fatalf("block %d: computed %s, header %s", blocks[i].Number, got, blocks[i].BaseFee)
		}
		checked++
		distinct[got.String()] = true
		for _, b := range w[1:] {
			if b.AtomicGas > 0 {
				atomic++
				break
			}
		}
	}
	if checked < 900 || atomic == 0 || len(distinct) < 10 {
		t.Fatalf("checked %d blocks, %d with atomic ops in the window, %d distinct fees", checked, atomic, len(distinct))
	}
}

func TestNextBaseFeeMatchesRecordedRPC(t *testing.T) {
	f := loadFixture(t)
	w, ok := window(f.Blocks, len(f.Blocks)-1)
	if !ok {
		t.Fatal("fixture head's settled block is missing")
	}
	got, err := sae.NextBaseFee(w)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(f.NextBaseFee) != 0 {
		t.Fatalf("next base fee %s, RPC %s", got, f.NextBaseFee)
	}
}

func TestMaxBaseFeeAfter(t *testing.T) {
	f := loadFixture(t)
	w, _ := window(f.Blocks, len(f.Blocks)-1)
	next, err := sae.NextBaseFee(w)
	if err != nil {
		t.Fatal(err)
	}
	prev := next
	for k := 0; k <= 10; k++ {
		got, err := sae.MaxBaseFeeAfter(w, k)
		if err != nil {
			t.Fatal(err)
		}
		if k == 0 && got.Cmp(next) != 0 {
			t.Fatalf("k=0: %s, next %s", got, next)
		}
		if k > 0 && got.Cmp(prev) <= 0 {
			t.Fatalf("k=%d: %s not above k=%d's %s", k, got, k-1, prev)
		}
		prev = got
	}
	ceiling, err := sae.Ceiling(w).MaxBaseFeeAfter(10)
	if err != nil || ceiling.Cmp(prev) != 0 {
		t.Fatalf("Ceiling: %v, %v; MaxBaseFeeAfter %s", ceiling, err, prev)
	}
	if _, err := sae.MaxBaseFeeAfter(w, -1); !errors.Is(err, gasometer.ErrInvalidHorizon) {
		t.Fatalf("k=-1: %v", err)
	}
}

// priceExponentE multiplies the minimum price by e (dynamic.PriceExponent's conversion rate).
const priceExponentE = 415_828_534_307_635_077

// TestGasConfigIsReadPerBlock pins GasConfigAfter's timing: a block's config applies after it, so the block before
// the head sets the head's minimum price and the head's own sets only the next.
func TestGasConfigIsReadPerBlock(t *testing.T) {
	f := loadFixture(t)
	w, _ := window(f.Blocks, len(f.Blocks)-1)
	headFee, _ := sae.BaseFee(w)
	nextFee, _ := sae.NextBaseFee(w)

	parent := cloneBlocks(w)
	parent[len(parent)-2].MinPriceExponent += priceExponentE
	if got, _ := sae.BaseFee(parent); got.Cmp(headFee) <= 0 {
		t.Fatalf("raising the parent's minimum price left the head's base fee at %s, was %s", got, headFee)
	}

	head := cloneBlocks(w)
	head[len(head)-1].MinPriceExponent += priceExponentE
	if got, _ := sae.BaseFee(head); got.Cmp(headFee) != 0 {
		t.Fatalf("the head's own minimum price changed its base fee: %s, was %s", got, headFee)
	}
	if got, _ := sae.NextBaseFee(head); got.Cmp(nextFee) <= 0 {
		t.Fatalf("raising the head's minimum price left the next base fee at %s, was %s", got, nextFee)
	}
}

// aboveFloor is the latest window whose head is priced above its minimum, so lifting the minimum leaves its fee, and
// time and the target move it.
func aboveFloor(t *testing.T, blocks []gasometer.SAEBlock) []gasometer.SAEBlock {
	t.Helper()
	for i := len(blocks) - 1; i >= 0; i-- {
		w, ok := window(blocks, i)
		if !ok || len(w) < 3 {
			continue
		}
		floor := cloneBlocks(w)
		floor[len(floor)-2].MinPriceExponent = 0
		want, _ := sae.BaseFee(w)
		if atFloor, _ := sae.BaseFee(floor); atFloor.Cmp(want) == 0 {
			return w // the minimum does not bind
		}
	}
	t.Fatal("no window priced above the minimum")
	return nil
}

// TestTargetIsReadPerBlock raises the target of every block before the head. A target change alone keeps the price
// (gastime scales excess with it), but each block's gas then raises the fee less, so the head's fee falls; reading
// the head's config for every block would leave it unchanged.
func TestTargetIsReadPerBlock(t *testing.T) {
	w := aboveFloor(t, loadFixture(t).Blocks)
	want, _ := sae.BaseFee(w)
	changed := cloneBlocks(w)
	for j := range changed[:len(changed)-1] {
		changed[j].TargetExponent += 1 << 22
	}
	if got, _ := sae.BaseFee(changed); got.Cmp(want) >= 0 {
		t.Fatalf("block %d: raising the target left the base fee at %s, was %s", w[len(w)-1].Number, got, want)
	}
}

// TestBlockTimeTakesSecondsFromTime pins cchain's blockTime: the millisecond field refines Time below the second only.
func TestBlockTimeTakesSecondsFromTime(t *testing.T) {
	w := aboveFloor(t, loadFixture(t).Blocks)
	want, _ := sae.BaseFee(w)
	shifted := cloneBlocks(w)
	shifted[len(shifted)-1].TimeMilliseconds += 60_000
	if got, _ := sae.BaseFee(shifted); got.Cmp(want) != 0 {
		t.Fatalf("whole seconds of the millisecond field moved the base fee: %s, was %s", got, want)
	}
	later := cloneBlocks(w)
	later[len(later)-1].Time += 60
	if got, _ := sae.BaseFee(later); got.Cmp(want) >= 0 {
		t.Fatalf("a minute later the base fee did not fall: %s, was %s", got, want)
	}
}

func TestInvalidBlocks(t *testing.T) {
	f := loadFixture(t)
	w, _ := window(f.Blocks, len(f.Blocks)-1)

	noSettled := cloneBlocks(w)
	noSettled[len(noSettled)-1].Settled = nil

	gap := cloneBlocks(w)
	gap = append(gap[:1], gap[2:]...)
	gap[len(gap)-1].Settled.Height = gap[0].Number

	for _, tc := range []struct {
		name   string
		blocks []gasometer.SAEBlock
		want   error
	}{
		{"empty", nil, gasometer.ErrInvalidSAEBlocks},
		{"head without settled fields", noSettled, gasometer.ErrInvalidSAEBlocks},
		{"starts after the settled block", w[1:], gasometer.ErrInvalidSAEBlocks},
		{"settles itself", cloneSettlingItself(w), gasometer.ErrInvalidSAEBlocks},
		{"gap", gap, gasometer.ErrBlockGap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sae.BaseFee(tc.blocks); !errors.Is(err, tc.want) {
				t.Fatalf("BaseFee: %v, want %v", err, tc.want)
			}
			if _, err := sae.NextBaseFee(tc.blocks); !errors.Is(err, tc.want) {
				t.Fatalf("NextBaseFee: %v, want %v", err, tc.want)
			}
			if _, err := sae.MaxBaseFeeAfter(tc.blocks, 1); !errors.Is(err, tc.want) {
				t.Fatalf("MaxBaseFeeAfter: %v, want %v", err, tc.want)
			}
		})
	}
}

func cloneSettlingItself(w []gasometer.SAEBlock) []gasometer.SAEBlock {
	head := cloneBlocks(w[len(w)-1:])
	head[0].Settled.Height = head[0].Number
	return head
}

// futureBlock is what a step of the walk sets for the next block: its time and config, and the gas it charges.
type futureBlock struct {
	advanceMs        uint64
	targetExponent   dynamic.TargetExponent
	minPriceExponent dynamic.PriceExponent
	gas              func(rate gas.Gas) gas.Gas
}

// extend appends blocks n+1 to n+1+k to w, the first k charging gas and the last only advancing time, so the result's
// BaseFee is block n+1+k's base fee. step chooses each block from its parent's config.
func extend(w []gasometer.SAEBlock, k int, step func(parent gasometer.SAEBlock) futureBlock) []gasometer.SAEBlock {
	out := cloneBlocks(w)
	head := out[len(out)-1]
	for j := 0; j <= k; j++ {
		parent := out[len(out)-1]
		f := step(parent)
		b := gasometer.SAEBlock{
			Number:           parent.Number + 1,
			TimeMilliseconds: parent.TimeMilliseconds + f.advanceMs,
			TargetExponent:   uint64(f.targetExponent),
			MinPriceExponent: uint64(f.minPriceExponent),
			Settled:          head.Settled,
		}
		b.Time = b.TimeMilliseconds / 1000
		if j < k {
			rate := gastime.SafeRateOfTarget(dynamic.TargetExponent(parent.TargetExponent).Target())
			b.TxGasLimit = uint64(f.gas(rate))
		}
		out = append(out, b)
	}
	return out
}

func fullBlock(rate gas.Gas) gas.Gas { return worstcase.SafeMaxBlockSize(rate) }

// TestMaxBaseFeeAfterTargetWalks is the adversarial case: full blocks while the target walks at fixed rates up to
// ACP-176's cap. The fee's exponent rises by the same 10/87 per block at any target, so only rounding separates these
// futures from the cap: at the maximum fall a fixed config is exceeded at k=1, and slower walks exceed the cap's own
// walk without its slack.
func TestMaxBaseFeeAfterTargetWalks(t *testing.T) {
	w := aboveFloor(t, loadFixture(t).Blocks)
	for _, rate := range []int64{-1 << 15, -1 << 14, -1000, -1, 1 << 15} {
		for _, k := range []int{1, 2, 30, 120} {
			future := extend(w, k, func(parent gasometer.SAEBlock) futureBlock {
				desired := dynamic.TargetExponent(max(0, int64(parent.TargetExponent)+rate)) //nolint:gosec // exponents fit
				return futureBlock{
					targetExponent:   dynamic.TargetExponent(parent.TargetExponent).Toward(&desired),
					minPriceExponent: dynamic.PriceExponent(parent.MinPriceExponent),
					gas:              fullBlock,
				}
			})
			realised, err := sae.BaseFee(future)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := sae.MaxBaseFeeAfter(w, k)
			if err != nil {
				t.Fatal(err)
			}
			if realised.Cmp(bound) > 0 {
				t.Errorf("target %+d per block, k=%d: block n+1+k's base fee %s above the cap %s", rate, k, realised, bound)
			}
		}
	}
}

// TestMaxBaseFeeAfterBoundsRandomFutures checks the cap against random futures: settled clocks, config walks within
// Toward's per-block caps in either direction, block gas up to the limit and time advancing.
func TestMaxBaseFeeAfterBoundsRandomFutures(t *testing.T) {
	blocks := loadFixture(t).Blocks
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // reproducible test input
	const targetStep, priceStep = 1 << 15, 288_230_376_151_711_744 / 3_600
	for range 300 {
		w, ok := window(blocks, len(blocks)/2+rng.IntN(len(blocks)/2))
		if !ok {
			continue
		}
		w = cloneBlocks(w)
		settled := *w[len(w)-1].Settled
		settled.Excess = settled.Excess / 2 * uint64(1+rng.IntN(4)) //nolint:gosec // IntN is non-negative
		w[len(w)-1].Settled = &settled
		k := rng.IntN(131)
		future := extend(w, k, func(parent gasometer.SAEBlock) futureBlock {
			target := dynamic.TargetExponent(parent.TargetExponent)
			desiredTarget := dynamic.TargetExponent(uint64(target) + uint64(rng.IntN(4*targetStep))) //nolint:gosec
			if desiredTarget >= 2*targetStep {
				desiredTarget -= 2 * targetStep
			}
			price := dynamic.PriceExponent(parent.MinPriceExponent)
			desiredPrice := dynamic.PriceExponent(uint64(price) + uint64(rng.Int64N(2*priceStep))) //nolint:gosec
			if rng.IntN(2) == 0 && uint64(price) >= priceStep {
				desiredPrice = price - priceStep
			}
			full := rng.IntN(2) == 0
			return futureBlock{
				advanceMs:        uint64(rng.IntN(3000)), //nolint:gosec // IntN is non-negative
				targetExponent:   target.Toward(&desiredTarget),
				minPriceExponent: price.Toward(&desiredPrice),
				gas: func(rate gas.Gas) gas.Gas {
					if full {
						return fullBlock(rate)
					}
					return gas.Gas(rng.Uint64N(uint64(fullBlock(rate)) + 1))
				},
			}
		})
		realised, err := sae.BaseFee(future)
		if err != nil {
			t.Fatal(err)
		}
		bound, err := sae.MaxBaseFeeAfter(w, k)
		if err != nil {
			t.Fatal(err)
		}
		if realised.Cmp(bound) > 0 {
			t.Fatalf("block %d, k=%d: block n+1+k's base fee %s above the cap %s", w[len(w)-1].Number, k, realised, bound)
		}
	}
}
