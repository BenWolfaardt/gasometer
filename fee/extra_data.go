package fee

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer"
)

const (
	holoceneExtraDataVersion = 0x00
	holoceneExtraDataLen     = 9
	jovianExtraDataVersion   = 0x01
	jovianExtraDataLen       = 17
)

// DecodeOPStackExtraData reads the EIP-1559 params an OP Stack header sets for its child: Holocene (version 0) or
// Jovian (version 1, adds the minimum base fee). Denominator and elasticity must be non-zero, as in op-geth.
func DecodeOPStackExtraData(extra []byte) (gasometer.FeeParams, error) {
	if len(extra) == 0 {
		return gasometer.FeeParams{}, fmt.Errorf("%w: empty", gasometer.ErrInvalidExtraData)
	}
	var wantLen int
	switch extra[0] {
	case holoceneExtraDataVersion:
		wantLen = holoceneExtraDataLen
	case jovianExtraDataVersion:
		wantLen = jovianExtraDataLen
	default:
		return gasometer.FeeParams{}, fmt.Errorf("%w: %d", gasometer.ErrUnknownExtraDataVersion, extra[0])
	}
	if len(extra) != wantLen {
		return gasometer.FeeParams{}, fmt.Errorf("%w: version %d is %d bytes, got %d",
			gasometer.ErrInvalidExtraData, extra[0], wantLen, len(extra))
	}
	params := gasometer.FeeParams{
		Denominator: uint64(binary.BigEndian.Uint32(extra[1:5])),
		Elasticity:  uint64(binary.BigEndian.Uint32(extra[5:9])),
	}
	if params.Denominator == 0 || params.Elasticity == 0 {
		return gasometer.FeeParams{}, fmt.Errorf("%w: denominator %d, elasticity %d",
			gasometer.ErrInvalidExtraData, params.Denominator, params.Elasticity)
	}
	if extra[0] == jovianExtraDataVersion {
		params.MinBaseFee = new(big.Int).SetUint64(binary.BigEndian.Uint64(extra[9:17]))
	}
	return params, nil
}
