package gasometer

import (
	"math/big"
	"time"
)

// FeeParams are the EIP-1559 parameters behind the next base fee.
type FeeParams struct {
	Denominator uint64
	Elasticity  uint64
	MinBaseFee  *big.Int // nil when the network has no minimum
}

// FeeHistory is one eth_feeHistory response.
type FeeHistory struct {
	OldestBlock   uint64
	BaseFees      []*big.Int // one per block, plus the block after the newest
	GasUsedRatios []float64
	Rewards       [][]*big.Int // per block, tips at the requested percentiles
}

// Len is the number of blocks covered.
func (h FeeHistory) Len() int {
	return len(h.GasUsedRatios)
}

// NextBaseFee is the base fee of the block after the newest.
func (h FeeHistory) NextBaseFee() *big.Int {
	if len(h.BaseFees) == 0 {
		return nil
	}
	return h.BaseFees[len(h.BaseFees)-1]
}

// HeadFee is the latest header's fee fields.
type HeadFee struct {
	Number      uint64
	BaseFee     *big.Int
	GasUsed     uint64
	BlobGasUsed uint64 // DA footprint under Jovian
	GasLimit    uint64
	ExtraData   []byte
}

// GasMetered is the gas the base fee update reads: max(gasUsed, blobGasUsed).
func (h HeadFee) GasMetered() uint64 {
	return max(h.GasUsed, h.BlobGasUsed)
}

// BlockFee is the stored fee summary of one block.
type BlockFee struct {
	Number       uint64
	BaseFee      *big.Int
	GasUsedRatio float64
	RequiredTip  *big.Int
	MedianTip    *big.Int
	ObservedAt   time.Time
}

// FeeSnapshot is one poll's view of the next block.
type FeeSnapshot struct {
	ID          int64
	Network     string
	AtBlock     uint64
	ObservedAt  time.Time
	NextBaseFee *big.Int
	NodeTip     *big.Int
	Price       *PriceQuote         // nil when the feed could not be read
	L1Fees      map[string]*big.Int // reference tx name to L1 data fee
}

// BaseFeeCeiling bounds the base fee across a horizon.
type BaseFeeCeiling interface {
	// MaxBaseFeeAfter is the highest base fee reachable after k full blocks following the next one.
	MaxBaseFeeAfter(k int) (*big.Int, error)
}

// SAESettled is the gas clock after executing the block a header settles.
type SAESettled struct {
	Height       uint64
	GasUnix      uint64
	GasNumerator uint64
	Excess       uint64
}

// SAEBlock is one Avalanche SAE block's gas clock inputs.
type SAEBlock struct {
	Number           uint64
	Time             uint64 // seconds
	TimeMilliseconds uint64 // only its sub-second part refines Time
	BaseFee          *big.Int
	Settled          *SAESettled // nil for a synchronous, pre-SAE block
	TargetExponent   uint64
	MinPriceExponent uint64
	MinDelayExcess   uint64
	TxGasLimit       uint64 // sum of the transactions' gas limits
	AtomicGas        uint64 // gas of the atomic import and export ops
}
