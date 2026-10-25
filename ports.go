package gasometer

import (
	"context"
	"math/big"
	"time"
)

// FeeSource reads one network's fee data.
type FeeSource interface {
	// FeeHistory covers [from, to]. When the source cannot reach back to from, the history starts later and its
	// OldestBlock says where.
	FeeHistory(ctx context.Context, from, to uint64, percentiles []float64) (FeeHistory, error)
	HeadFee(ctx context.Context) (HeadFee, error)
	NodeTip(ctx context.Context) (*big.Int, error)
}

// BlockStore persists per-block fee summaries.
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
}

// L1FeeSource reads the L1 data fee of reference transactions, keyed by name.
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
type MetricsRecorder interface {
	Settled(s Settlement)
	// Snapshot records one poll's fees; price is nil when missing or stale.
	Snapshot(network string, snapshot FeeSnapshot, latest BlockFee, price *PriceQuote)
	ModelMismatch(network string)
	PollCompleted(network string, took time.Duration, at time.Time)
}

// SnapshotStore persists fee snapshots.
type SnapshotStore interface {
	// Insert stores the snapshot and returns its ID.
	Insert(ctx context.Context, snapshot FeeSnapshot) (int64, error)
	Get(ctx context.Context, ids []int64) (map[int64]FeeSnapshot, error)
}

// PredictionStore persists predictions and their scores.
type PredictionStore interface {
	Insert(ctx context.Context, predictions []Prediction) error
	Pending(ctx context.Context, network string) ([]Prediction, error)
	// Settle writes the status and score of predictions that are no longer pending.
	Settle(ctx context.Context, predictions []Prediction) error
}

// PriceSource reads a network's coin price in USD.
type PriceSource interface {
	Price(ctx context.Context) (PriceQuote, error)
}

// PredictionInput is what a predictor sees for one horizon.
type PredictionInput struct {
	Snapshot FeeSnapshot
	History  []BlockFee // ascending, the growth window, ending at Snapshot.AtBlock
	Horizon  Horizon
	Params   *FeeParams // nil when no model applies
}

// Predictor proposes a bid for a horizon.
type Predictor interface {
	Name() string
	Predict(in PredictionInput) (Bid, error)
}
