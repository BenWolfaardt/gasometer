package services

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

const (
	holoceneExtraDataVersion = 0x00
	holoceneExtraDataLen     = 9
	jovianExtraDataVersion   = 0x01
	jovianExtraDataLen       = 17
)

// DecodeOPStackExtraData reads the EIP-1559 params an OP Stack header sets for its child: Holocene (version 0) or
// Jovian (version 1, adds the minimum base fee). Denominator and elasticity must be non-zero, as in op-geth.
func DecodeOPStackExtraData(extra []byte) (entities.FeeParams, error) {
	if len(extra) == 0 {
		return entities.FeeParams{}, fmt.Errorf("%w: empty", entities.ErrInvalidExtraData)
	}
	var wantLen int
	switch extra[0] {
	case holoceneExtraDataVersion:
		wantLen = holoceneExtraDataLen
	case jovianExtraDataVersion:
		wantLen = jovianExtraDataLen
	default:
		return entities.FeeParams{}, fmt.Errorf("%w: %d", entities.ErrUnknownExtraDataVersion, extra[0])
	}
	if len(extra) != wantLen {
		return entities.FeeParams{}, fmt.Errorf("%w: version %d is %d bytes, got %d",
			entities.ErrInvalidExtraData, extra[0], wantLen, len(extra))
	}
	params := entities.FeeParams{
		Denominator: uint64(binary.BigEndian.Uint32(extra[1:5])),
		Elasticity:  uint64(binary.BigEndian.Uint32(extra[5:9])),
	}
	if params.Denominator == 0 || params.Elasticity == 0 {
		return entities.FeeParams{}, fmt.Errorf("%w: denominator %d, elasticity %d",
			entities.ErrInvalidExtraData, params.Denominator, params.Elasticity)
	}
	if extra[0] == jovianExtraDataVersion {
		params.MinBaseFee = new(big.Int).SetUint64(binary.BigEndian.Uint64(extra[9:17]))
	}
	return params, nil
}
