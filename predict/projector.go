package predict

import (
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer"
)

// ProjectionInput is what a projector sees for one horizon.
type ProjectionInput struct {
	NextBaseFee *big.Int
	History     []gasometer.BlockFee     // ascending, the growth window
	Blocks      int                      // horizon in blocks
	Ceiling     gasometer.BaseFeeCeiling // nil when no model applies
}

// BaseFeeProjection is the base fee expected, and a high quantile of it, at the last block of a horizon.
type BaseFeeProjection struct {
	Expected *big.Int
	Upper    *big.Int
}

// BaseFeeProjector projects the base fee across a horizon. The horizon's first block has the known next base fee,
// so a k-block horizon spans k-1 blocks of growth.
type BaseFeeProjector interface {
	Project(in ProjectionInput) (BaseFeeProjection, error)
}

var (
	_ BaseFeeProjector = (*EmpiricalProjector)(nil)
	_ BaseFeeProjector = (*WorstCaseProjector)(nil)
)

// EmpiricalProjector projects from observed base fee growth.
type EmpiricalProjector struct {
	expectedQuantile float64
	upperQuantile    float64
}

// NewEmpiricalProjector projects from the base fee growth observed over the same span in history.
func NewEmpiricalProjector(upperQuantile float64) *EmpiricalProjector {
	return &EmpiricalProjector{expectedQuantile: 0.5, upperQuantile: upperQuantile}
}

// Project projects the base fee across in's horizon.
func (p *EmpiricalProjector) Project(in ProjectionInput) (BaseFeeProjection, error) {
	if in.Blocks < 1 {
		return BaseFeeProjection{}, fmt.Errorf("%w: %d blocks", gasometer.ErrInvalidHorizon, in.Blocks)
	}
	span := in.Blocks - 1
	if span == 0 {
		return BaseFeeProjection{Expected: new(big.Int).Set(in.NextBaseFee), Upper: new(big.Int).Set(in.NextBaseFee)}, nil
	}
	growth := growthRatios(in.History, span)
	if len(growth) == 0 {
		return BaseFeeProjection{}, fmt.Errorf("%w: no %d-block spans in %d blocks",
			gasometer.ErrInsufficientHistory, span, len(in.History))
	}
	return BaseFeeProjection{
		Expected: scale(in.NextBaseFee, quantileRat(growth, p.expectedQuantile), false),
		Upper:    scale(in.NextBaseFee, quantileRat(growth, p.upperQuantile), true),
	}, nil
}

// growthRatios are baseFee[i+span]/baseFee[i] over every contiguous pair in history with a non-zero start.
func growthRatios(history []gasometer.BlockFee, span int) []*big.Rat {
	var ratios []*big.Rat
	for i := 0; i+span < len(history); i++ {
		from, to := history[i], history[i+span]
		if to.Number-from.Number != uint64(span) || from.BaseFee.Sign() == 0 {
			continue
		}
		ratios = append(ratios, new(big.Rat).SetFrac(to.BaseFee, from.BaseFee))
	}
	return ratios
}

func scale(fee *big.Int, ratio *big.Rat, roundUp bool) *big.Int {
	num := new(big.Int).Mul(fee, ratio.Num())
	quo, rem := new(big.Int).QuoRem(num, ratio.Denom(), new(big.Int))
	if roundUp && rem.Sign() > 0 {
		quo.Add(quo, bigOne)
	}
	return quo
}

// WorstCaseProjector caps another projector at the fee model's worst case.
type WorstCaseProjector struct {
	inner BaseFeeProjector
}

// NewWorstCaseProjector caps inner's projection at the fee model's ceiling when one is known.
func NewWorstCaseProjector(inner BaseFeeProjector) *WorstCaseProjector {
	return &WorstCaseProjector{inner: inner}
}

// Project projects the base fee across in's horizon.
func (p *WorstCaseProjector) Project(in ProjectionInput) (BaseFeeProjection, error) {
	proj, err := p.inner.Project(in)
	if err != nil || in.Ceiling == nil {
		return proj, err
	}
	ceiling, err := in.Ceiling.MaxBaseFeeAfter(in.Blocks - 1)
	if err != nil {
		return BaseFeeProjection{}, err
	}
	proj.Upper = minInt(proj.Upper, ceiling)
	proj.Expected = minInt(proj.Expected, proj.Upper)
	return proj, nil
}
