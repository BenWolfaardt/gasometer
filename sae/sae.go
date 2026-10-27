// Package sae computes Avalanche C-Chain base fees with avalanchego's SAE gas clock. Each function replays the clock
// as vms/saevm/worstcase.State does when a block is built: from the settled block's post-execution clock, through
// every block after it.
package sae

import (
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ava-labs/avalanchego/vms/components/gas"
	"github.com/ava-labs/avalanchego/vms/saevm/cchain/dynamic"
	"github.com/ava-labs/avalanchego/vms/saevm/gastime"
	"github.com/ava-labs/avalanchego/vms/saevm/proxytime"
	"github.com/ava-labs/avalanchego/vms/saevm/worstcase"

	"github.com/BenWolfaardt/gasometer"
)

// targetToExcessScaling is cchain/hooks.go GasConfigAfter's K/T ratio, 87 ~= 60 / ln(2).
const targetToExcessScaling = 87

// BaseFee is the base fee of the last block, from the block it settles through itself.
func BaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) {
	clock, err := clockAtHead(blocks)
	if err != nil {
		return nil, err
	}
	return price(clock), nil
}

// NextBaseFee is the upper bound on the next block's base fee that the RPC serves: the clock after the last block,
// with no further time advance.
func NextBaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) {
	clock, err := clockAfterHead(blocks)
	if err != nil {
		return nil, err
	}
	return price(clock), nil
}

// MaxBaseFeeAfter bounds the base fee after k blocks following the next one. Each block is full, Tau·Lambda seconds
// of gas, so the clock runs ahead of any minimum block delay and time never lowers it, and each block's config is the
// worst its parent's can step to under cchain/dynamic's Toward caps: the minimum price up, the target down. A full
// block raises the fee's exponent by 10/87 at any target, the minimum price rises at most 2^(1/3600) per block so it
// never catches the clock, and gastime.go scaleExcess rounds a target change up by at most one unit of excess; the
// factor 1 + k·10⁻⁶ covers that rounding, which is below 1.2·10⁻⁸ per block.
func MaxBaseFeeAfter(blocks []gasometer.SAEBlock, k int) (*big.Int, error) {
	if k < 0 {
		return nil, fmt.Errorf("%w: %d blocks", gasometer.ErrInvalidHorizon, k)
	}
	clock, err := clockAfterHead(blocks)
	if err != nil {
		return nil, err
	}
	b := blocks[len(blocks)-1]
	for range k {
		b.TargetExponent = uint64(dynamic.TargetExponent(b.TargetExponent).Toward(new(dynamic.TargetExponent)))
		b.MinPriceExponent = uint64(dynamic.PriceExponent(b.MinPriceExponent).Toward(new(dynamic.PriceExponent(math.MaxUint64))))
		target, cfg := gasConfig(b)
		if err := clock.AfterBlock(worstcase.SafeMaxBlockSize(clock.Rate()), target, cfg); err != nil {
			return nil, fmt.Errorf("full block: %w", err)
		}
	}
	return withRoundingSlack(price(clock), k), nil
}

// roundingSlackPerMillion is the cap's allowance for integer rounding, per block, in millionths.
const roundingSlackPerMillion = 1

// withRoundingSlack is fee·(1 + k·10⁻⁶), rounded up.
func withRoundingSlack(fee *big.Int, k int) *big.Int {
	const million = 1_000_000
	out := new(big.Int).Mul(fee, big.NewInt(million+int64(k)*roundingSlackPerMillion))
	out.Add(out, big.NewInt(million-1))
	return out.Quo(out, big.NewInt(million))
}

// Model is the gasometer.SAEModel of this package's functions.
type Model struct{}

var _ gasometer.SAEModel = Model{}

// BaseFee implements gasometer.SAEModel.
func (Model) BaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) { return BaseFee(blocks) }

// NextBaseFee implements gasometer.SAEModel.
func (Model) NextBaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) { return NextBaseFee(blocks) }

// Ceiling implements gasometer.SAEModel.
func (Model) Ceiling(blocks []gasometer.SAEBlock) gasometer.BaseFeeCeiling { return Ceiling(blocks) }

// Ceiling is the SAE base fee ceiling after a block, from the block it settles through itself.
type Ceiling []gasometer.SAEBlock

var _ gasometer.BaseFeeCeiling = Ceiling(nil)

// MaxBaseFeeAfter implements gasometer.BaseFeeCeiling.
func (c Ceiling) MaxBaseFeeAfter(k int) (*big.Int, error) {
	return MaxBaseFeeAfter(c, k)
}

func clockAfterHead(blocks []gasometer.SAEBlock) (*gastime.Time, error) {
	clock, err := clockAtHead(blocks)
	if err != nil {
		return nil, err
	}
	if err := afterBlock(clock, blocks[len(blocks)-1]); err != nil {
		return nil, err
	}
	return clock, nil
}

// clockAtHead is the clock at the start of the last block: settled, then each block after it run through, then
// advanced to the last block's time.
func clockAtHead(blocks []gasometer.SAEBlock) (*gastime.Time, error) {
	if err := validate(blocks); err != nil {
		return nil, err
	}
	head := blocks[len(blocks)-1]
	target, cfg := gasConfig(blocks[0])
	pt := proxytime.New(head.Settled.GasUnix, gas.Gas(head.Settled.GasNumerator), gastime.SafeRateOfTarget(target))
	clock, err := gastime.FromProxyTime(pt, gas.Gas(head.Settled.Excess), cfg)
	if err != nil {
		return nil, fmt.Errorf("settled clock at %d: %w", blocks[0].Number, err)
	}
	for _, b := range blocks[1 : len(blocks)-1] {
		clock.BeforeBlock(blockTime(b))
		if err := afterBlock(clock, b); err != nil {
			return nil, err
		}
	}
	clock.BeforeBlock(blockTime(head))
	return clock, nil
}

// validate checks blocks run consecutively from the last block's settled block to the last block.
func validate(blocks []gasometer.SAEBlock) error {
	if len(blocks) == 0 {
		return fmt.Errorf("%w: none", gasometer.ErrInvalidSAEBlocks)
	}
	head := blocks[len(blocks)-1]
	if head.Settled == nil {
		return fmt.Errorf("%w: block %d has no settled fields", gasometer.ErrInvalidSAEBlocks, head.Number)
	}
	if blocks[0].Number != head.Settled.Height {
		return fmt.Errorf("%w: block %d settles %d, blocks start at %d",
			gasometer.ErrInvalidSAEBlocks, head.Number, head.Settled.Height, blocks[0].Number)
	}
	if len(blocks) == 1 {
		return fmt.Errorf("%w: block %d settles itself", gasometer.ErrInvalidSAEBlocks, head.Number)
	}
	for i := 1; i < len(blocks); i++ {
		if blocks[i].Number != blocks[i-1].Number+1 {
			return fmt.Errorf("%w: block %d follows %d", gasometer.ErrBlockGap, blocks[i].Number, blocks[i-1].Number)
		}
	}
	return nil
}

// afterBlock charges b's worst-case gas, its transactions' gas limits and its atomic ops, as worstcase.State does.
func afterBlock(clock *gastime.Time, b gasometer.SAEBlock) error {
	target, cfg := gasConfig(b)
	if err := clock.AfterBlock(gas.Gas(b.TxGasLimit+b.AtomicGas), target, cfg); err != nil {
		return fmt.Errorf("after block %d: %w", b.Number, err)
	}
	return nil
}

// gasConfig is cchain/hooks.go GasConfigAfter: the target and gas price config in effect after b.
func gasConfig(b gasometer.SAEBlock) (gas.Gas, gastime.GasPriceConfig) {
	return dynamic.TargetExponent(b.TargetExponent).Target(), gastime.GasPriceConfig{
		TargetToExcessScaling: targetToExcessScaling,
		MinPrice:              dynamic.PriceExponent(b.MinPriceExponent).Price(),
	}
}

// blockTime is cchain/hooks.go blockTime: whole seconds from Time, refined by the millisecond field's remainder.
func blockTime(b gasometer.SAEBlock) time.Time {
	ms := b.TimeMilliseconds % 1000
	return time.Unix(int64(b.Time), int64(ms)*int64(time.Millisecond)) //nolint:gosec // block times fit in int64
}

func price(clock *gastime.Time) *big.Int {
	return clock.BaseFee().ToBig()
}
