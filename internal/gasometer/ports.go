package gasometer

import (
	"context"
	"math/big"
	"time"
)

// FeeSource reads one network's fee data.
//
// Guarantees: FeeHistory ends at to and starts at from, or later when the source no longer serves from, with
// OldestBlock saying where; its BaseFees has Len()+1 entries and its Rewards Len() rows, a row nil where the source
// sent none. HeadFee is the latest header, with its number and base fee set. NodeTip is the source's suggested tip.
// Ordering: none.
// Errors: ErrBeyondHistory from FeeHistory when the source no longer serves to; ErrMalformedFeeHistory when from is
// after to or the source answers for another range. Others are the source's, including a header without a number or
// base fee.
// Concurrency: safe for concurrent use.
type FeeSource interface {
	// FeeHistory covers [from, to].
	FeeHistory(ctx context.Context, from, to uint64, percentiles []float64) (FeeHistory, error)
	HeadFee(ctx context.Context) (HeadFee, error)
	NodeTip(ctx context.Context) (*big.Int, error)
}

// SAESource reads Avalanche SAE blocks.
//
// Guarantees: SAEBlocks returns the contiguous blocks from the one head settles through head; a head with no settled
// fields comes back alone.
// Ordering: ascending.
// Errors: ErrInvalidSAEBlocks when head settles itself or a later block. Others are the source's.
// Concurrency: safe for concurrent use.
type SAESource interface {
	// SAEBlocks returns the block head settles through head, ascending.
	SAEBlocks(ctx context.Context, head uint64) ([]SAEBlock, error)
}

// SAEModel computes Avalanche SAE base fees over a block's window: its settled block through itself, ascending. It is
// an interface because the computation runs avalanchego's gas clock, and the domain must not import avalanchego.
//
// Guarantees: BaseFee is the last block's own base fee; NextBaseFee is the upper bound on the next block's. Ceiling
// never fails itself: an invalid window's error surfaces from MaxBaseFeeAfter.
// Ordering: the window is ascending.
// Errors: ErrInvalidSAEBlocks for an empty window, a head with no settled fields, a window not starting at the block
// the head settles, or a head that settles itself; ErrBlockGap for blocks that are not contiguous.
// Concurrency: safe for concurrent use.
type SAEModel interface {
	BaseFee(blocks []SAEBlock) (*big.Int, error)
	NextBaseFee(blocks []SAEBlock) (*big.Int, error)
	Ceiling(blocks []SAEBlock) BaseFeeCeiling
}

// BlockStore persists per-block fee summaries, per network.
//
// Guarantees: Upsert stores every block or none, overwriting every field of a block already stored; upserting no
// blocks is a no-op. Range and Holes leave missing blocks out. Latest's ok is false while the network has no blocks.
// Prune deletes across every network and returns the count it deleted.
// Ordering: Range and Holes return ascending results.
// Errors: ErrInsufficientHistory from AverageBlockTime until blocks from two polls are stored. Others are the store's.
// Concurrency: safe for concurrent use.
type BlockStore interface {
	Upsert(ctx context.Context, network string, blocks []BlockFee) error
	// Range returns stored blocks in [from, to], ascending; missing blocks are absent.
	Range(ctx context.Context, network string, from, to uint64) ([]BlockFee, error)
	// Latest is the highest stored block number; ok is false when none is stored.
	Latest(ctx context.Context, network string) (number uint64, ok bool, err error)
	// AverageBlockTime is measured over every stored block; ErrInsufficientHistory before two polls.
	AverageBlockTime(ctx context.Context, network string) (time.Duration, error)
	// Prune deletes blocks observed before cutoff and returns how many.
	Prune(ctx context.Context, cutoff time.Time) (int64, error)
	// Holes returns the missing blocks numbered above above that lie between two stored blocks, ascending.
	Holes(ctx context.Context, network string, above uint64) ([]BlockRange, error)
}

// L1FeeSource reads the L1 data fee of reference transactions.
//
// Guarantees: one entry per reference transaction, keyed by its name. On error the map is nil, never partial.
// Ordering: none.
// Errors: the source's only.
// Concurrency: safe for concurrent use.
type L1FeeSource interface {
	L1Fees(ctx context.Context, txs []ReferenceTx) (map[string]*big.Int, error)
}

// Settlement is one prediction leaving pending, as the metrics see it.
type Settlement struct {
	Network        string
	Predictor      string
	HorizonSeconds int
	Outcome        string
	OverpayRatio   *float64 // nil unless included with a non-zero required
	EstimateError  float64  // when included
	Costs          []TxCost // when included and the price was fresh
}

// MetricsRecorder exports what the use cases observe.
//
// Guarantees: it records only, and never fails.
// Ordering: none.
// Errors: none.
// Concurrency: safe for concurrent use.
type MetricsRecorder interface {
	Settled(s Settlement)
	// Snapshot records one poll's fees; price is nil when missing or stale.
	Snapshot(network string, snapshot FeeSnapshot, latest BlockFee, price *PriceQuote)
	ModelMismatch(network string)
	BlocksBackfilled(network string, n int)
	// BlocksLost counts blocks the source no longer serves, so they will never be stored.
	BlocksLost(network string, n uint64)
	PollCompleted(network string, took time.Duration, at time.Time)
}

// SnapshotStore persists fee snapshots.
//
// Guarantees: Insert returns a new ID, unique in the store. Get returns the snapshots it finds; an ID never stored is
// absent from the map, not an error, and no IDs give an empty map.
// Ordering: none.
// Errors: the store's only.
// Concurrency: safe for concurrent use.
type SnapshotStore interface {
	// Insert stores the snapshot and returns its ID.
	Insert(ctx context.Context, snapshot FeeSnapshot) (int64, error)
	Get(ctx context.Context, ids []int64) (map[int64]FeeSnapshot, error)
}

// PredictionStore persists predictions and their scores.
//
// Guarantees: Insert stores every prediction given. Pending returns one network's predictions still pending. Settle
// writes each prediction's status and score, so a settled prediction leaves Pending.
// Ordering: Pending returns predictions in the order they were inserted.
// Errors: the store's only.
// Concurrency: safe for concurrent use.
type PredictionStore interface {
	Insert(ctx context.Context, predictions []Prediction) error
	Pending(ctx context.Context, network string) ([]Prediction, error)
	// Settle writes the status and score of predictions that are no longer pending.
	Settle(ctx context.Context, predictions []Prediction) error
}

// PriceSource reads a network's coin price in USD.
//
// Guarantees: the quote's Answer is positive and its UpdatedAt set. Whether it is fresh enough is the caller's to judge.
// Ordering: none.
// Errors: the source's only, including an answer that is not positive.
// Concurrency: safe for concurrent use.
type PriceSource interface {
	Price(ctx context.Context) (PriceQuote, error)
}

// PredictionInput is what a predictor sees for one horizon.
type PredictionInput struct {
	Snapshot FeeSnapshot
	History  []BlockFee // ascending, the growth window, ending at Snapshot.AtBlock
	Horizon  Horizon
	Ceiling  BaseFeeCeiling // nil when no model applies
}

// Predictor proposes a bid for a horizon.
type Predictor interface {
	Name() string
	Predict(in PredictionInput) (Bid, error)
}
