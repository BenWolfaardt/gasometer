package gasometer

import "errors"

var (
	// ErrInvalidFeeParams is returned by BaseFeeCeiling.MaxBaseFeeAfter and fee.NextBaseFee for EIP-1559 parameters,
	// a gas limit or a base fee they cannot compute from.
	ErrInvalidFeeParams = errors.New("invalid fee params")
	// ErrInvalidExtraData is returned by fee.DecodeOPStackExtraData for a header extraData of the wrong length or with
	// zero parameters.
	ErrInvalidExtraData = errors.New("invalid extraData")
	// ErrUnknownExtraDataVersion is returned by fee.DecodeOPStackExtraData for an extraData version it does not know.
	ErrUnknownExtraDataVersion = errors.New("unknown extraData version")
	// ErrInsufficientHistory is returned by BlockStore.AverageBlockTime before two blocks are stored, and by a
	// Predictor or projector given fewer blocks than its window.
	ErrInsufficientHistory = errors.New("insufficient block history")
	// ErrBlockGap is returned by SAEModel for non-contiguous blocks, and by score.Bid for a window missing blocks.
	ErrBlockGap = errors.New("blocks missing from window")
	// ErrInvalidHorizon is returned by BaseFeeCeiling.MaxBaseFeeAfter for k below 0, by the projectors and score.Bid
	// for fewer than 1 block, and by poll.BlocksIn for a duration or block time that is not positive.
	ErrInvalidHorizon = errors.New("invalid horizon")
	// ErrInvalidBid is returned by a Predictor whose snapshot lacks the next base fee or node tip it needs, and by
	// score.Bid for a bid missing a field.
	ErrInvalidBid = errors.New("invalid bid")
	// ErrMalformedFeeHistory is returned by FeeSource.FeeHistory for a range with from after to or an answer covering
	// another range, and by fee.BlockFees for a history whose base fees do not number one more than its blocks.
	ErrMalformedFeeHistory = errors.New("malformed fee history")
	// ErrBeyondHistory is returned by FeeSource.FeeHistory when the source no longer serves the range's last block.
	ErrBeyondHistory = errors.New("blocks beyond the source's history")
	// ErrInvalidSAEBlocks is returned by SAESource.SAEBlocks for a head that settles itself or a later block, and by
	// SAEModel for an empty window, a head without settled fields, a window not starting at the settled block, or a
	// head that settles itself.
	ErrInvalidSAEBlocks = errors.New("invalid SAE blocks")
	// ErrTxRejected is returned by ProbeChain.Send when the node refuses the transaction.
	ErrTxRejected = errors.New("transaction rejected")
)
