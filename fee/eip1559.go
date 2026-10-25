// Package fee is the protocol fee model: the EIP-1559 base fee update and its worst case, the OP Stack fee
// parameters in a header's extraData, and per-block fee summaries from eth_feeHistory.
package fee

import (
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer"
)

var bigOne = big.NewInt(1)

// NextBaseFee is the EIP-1559 base fee of a parent's child, following op-geth's CalcBaseFee: the minimum applies
// after the update, and gasMetered is max(gasUsed, blobGasUsed) under Jovian.
func NextBaseFee(params gasometer.FeeParams, parentBaseFee *big.Int, gasMetered, gasLimit uint64) (*big.Int, error) {
	if err := validateFeeParams(params); err != nil {
		return nil, err
	}
	if parentBaseFee == nil || parentBaseFee.Sign() < 0 {
		return nil, fmt.Errorf("%w: parent base fee %v", gasometer.ErrInvalidFeeParams, parentBaseFee)
	}
	target := gasLimit / params.Elasticity
	if target == 0 {
		return nil, fmt.Errorf("%w: gas target is zero (limit %d, elasticity %d)",
			gasometer.ErrInvalidFeeParams, gasLimit, params.Elasticity)
	}
	return applyMinBaseFee(params, calcBaseFee(params, parentBaseFee, gasMetered, target)), nil
}

// MaxBaseFeeAfter is the base fee after k full blocks. Each raises it by max(1, fee*(elasticity-1)/denominator),
// exactly NextBaseFee's rise when the gas limit is a multiple of the elasticity.
func MaxBaseFeeAfter(params gasometer.FeeParams, baseFee *big.Int, k int) (*big.Int, error) {
	if err := validateFeeParams(params); err != nil {
		return nil, err
	}
	if baseFee == nil || baseFee.Sign() < 0 {
		return nil, fmt.Errorf("%w: base fee %v", gasometer.ErrInvalidFeeParams, baseFee)
	}
	if k < 0 {
		return nil, fmt.Errorf("%w: %d blocks", gasometer.ErrInvalidHorizon, k)
	}
	growth := new(big.Int).SetUint64(params.Elasticity - 1)
	denominator := new(big.Int).SetUint64(params.Denominator)
	fee := new(big.Int).Set(baseFee)
	delta := new(big.Int)
	for range k {
		delta.Mul(fee, growth)
		delta.Quo(delta, denominator)
		if delta.Cmp(bigOne) < 0 {
			delta.Set(bigOne)
		}
		fee.Add(fee, delta)
		fee = applyMinBaseFee(params, fee)
	}
	return fee, nil
}

func validateFeeParams(params gasometer.FeeParams) error {
	if params.Denominator == 0 || params.Elasticity == 0 {
		return fmt.Errorf("%w: denominator %d, elasticity %d",
			gasometer.ErrInvalidFeeParams, params.Denominator, params.Elasticity)
	}
	if params.MinBaseFee != nil && params.MinBaseFee.Sign() < 0 {
		return fmt.Errorf("%w: negative minimum base fee", gasometer.ErrInvalidFeeParams)
	}
	return nil
}

func calcBaseFee(params gasometer.FeeParams, parentBaseFee *big.Int, gasMetered, target uint64) *big.Int {
	if gasMetered == target {
		return new(big.Int).Set(parentBaseFee)
	}
	targetBig := new(big.Int).SetUint64(target)
	denominator := new(big.Int).SetUint64(params.Denominator)
	delta := new(big.Int)
	if gasMetered > target {
		delta.SetUint64(gasMetered - target)
		delta.Mul(delta, parentBaseFee)
		delta.Quo(delta, targetBig)
		delta.Quo(delta, denominator)
		if delta.Cmp(bigOne) < 0 {
			delta.Set(bigOne)
		}
		return delta.Add(parentBaseFee, delta)
	}
	delta.SetUint64(target - gasMetered)
	delta.Mul(delta, parentBaseFee)
	delta.Quo(delta, targetBig)
	delta.Quo(delta, denominator)
	// The fall is at most parentBaseFee/denominator, so the result is never negative.
	return delta.Sub(parentBaseFee, delta)
}

func applyMinBaseFee(params gasometer.FeeParams, fee *big.Int) *big.Int {
	if params.MinBaseFee != nil && fee.Cmp(params.MinBaseFee) < 0 {
		return new(big.Int).Set(params.MinBaseFee)
	}
	return fee
}
