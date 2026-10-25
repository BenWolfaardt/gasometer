package entities

import "errors"

var (
	ErrInvalidFeeParams        = errors.New("invalid fee params")
	ErrInvalidExtraData        = errors.New("invalid extraData")
	ErrUnknownExtraDataVersion = errors.New("unknown extraData version")
	ErrInsufficientHistory     = errors.New("insufficient block history")
	ErrBlockGap                = errors.New("blocks missing from window")
	ErrInvalidHorizon          = errors.New("invalid horizon")
	ErrInvalidBid              = errors.New("invalid bid")
	ErrInvalidConfig           = errors.New("invalid config")
	ErrModelMismatch           = errors.New("fee model mismatch")
	ErrHistoryHeadMismatch     = errors.New("fee history does not end at head")
	ErrMalformedFeeHistory     = errors.New("malformed fee history")
)
