// Package poll runs each network's poll: collect its fees, forecast with every predictor, settle what has resolved, and
// prune old blocks, on a loop that lets in-flight work finish at shutdown.
package poll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
)

// Observation is what one poll saw.
type Observation struct {
	Snapshot gasometer.FeeSnapshot
	Head     gasometer.HeadFee
	Params   *gasometer.FeeParams // nil when no model applies
}

// CollectorParams are a collector's decisions for one network.
type CollectorParams struct {
	RequiredTipPercentile float64 // eth_feeHistory percentile taken as the required tip
	PriceMaxAge           time.Duration
	InitialBackfill       uint64 // blocks fetched on a first run
}

// CollectorDeps are a collector's collaborators. Prices and L1Fees may be nil.
type CollectorDeps struct {
	Fees      gasometer.FeeSource
	Prices    gasometer.PriceSource
	L1Fees    gasometer.L1FeeSource
	Blocks    gasometer.BlockStore
	Snapshots gasometer.SnapshotStore
	Metrics   gasometer.MetricsRecorder
	Now       func() time.Time
	Log       *slog.Logger
}

// Collector polls one network, stores its blocks and a snapshot, and runs the fee model self-check.
type Collector struct {
	network gasometer.Network
	params  CollectorParams
	txs     []gasometer.ReferenceTx
	deps    CollectorDeps
	log     *slog.Logger
	// warnedVersions is touched only by the network's own poll goroutine.
	warnedVersions map[byte]bool
}

// NewCollector builds the collector for one network.
func NewCollector(network gasometer.Network, params CollectorParams, txs []gasometer.ReferenceTx, deps CollectorDeps) *Collector {
	return &Collector{
		network:        network,
		params:         params,
		txs:            txs,
		deps:           deps,
		log:            deps.Log.With("network", network.Name),
		warnedVersions: map[byte]bool{},
	}
}

// Collect runs one poll.
func (c *Collector) Collect(ctx context.Context) (Observation, error) {
	head, err := c.deps.Fees.HeadFee(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("read head: %w", err)
	}
	from, err := c.firstBlock(ctx, head.Number)
	if err != nil {
		return Observation{}, err
	}
	observedAt := c.deps.Now()
	latest, next, err := c.storeBlocks(ctx, from, head.Number, observedAt)
	if err != nil {
		return Observation{}, err
	}
	params := c.selfCheck(head, next)

	nodeTip, err := c.deps.Fees.NodeTip(ctx)
	if err != nil {
		return Observation{}, fmt.Errorf("read node tip: %w", err)
	}
	snapshot := gasometer.FeeSnapshot{
		Network:     c.network.Name,
		AtBlock:     head.Number,
		ObservedAt:  observedAt,
		NextBaseFee: next,
		NodeTip:     nodeTip,
		L1Fees:      map[string]*big.Int{},
	}
	freshPrice := c.readPrice(ctx, &snapshot)
	c.readL1Fees(ctx, &snapshot)

	if snapshot.ID, err = c.deps.Snapshots.Insert(ctx, snapshot); err != nil {
		return Observation{}, fmt.Errorf("store snapshot: %w", err)
	}
	c.deps.Metrics.Snapshot(c.network.Name, snapshot, latest, freshPrice)
	return Observation{Snapshot: snapshot, Head: head, Params: params}, nil
}

// firstBlock is the block after the latest stored, or the start of the initial backfill on a first run.
func (c *Collector) firstBlock(ctx context.Context, head uint64) (uint64, error) {
	latest, ok, err := c.deps.Blocks.Latest(ctx, c.network.Name)
	switch {
	case err != nil:
		return 0, fmt.Errorf("read latest stored block: %w", err)
	case !ok:
		return blocksBack(head, c.params.InitialBackfill), nil
	case latest >= head:
		return head, nil
	default:
		return latest + 1, nil
	}
}

// blocksBack is the first of the n blocks ending at head.
func blocksBack(head, n uint64) uint64 {
	if n == 0 || n > head {
		return 0
	}
	return head - n + 1
}

// storeBlocks fetches and stores [from, head], returning the head block and the next base fee.
func (c *Collector) storeBlocks(ctx context.Context, from, head uint64, observedAt time.Time) (gasometer.BlockFee, *big.Int, error) {
	history, err := c.deps.Fees.FeeHistory(ctx, from, head, fee.RewardPercentiles(c.params.RequiredTipPercentile))
	if err != nil {
		return gasometer.BlockFee{}, nil, fmt.Errorf("read fee history %d to %d: %w", from, head, err)
	}
	n := uint64(history.Len())
	if n == 0 || history.OldestBlock < from || history.OldestBlock+n-1 != head {
		return gasometer.BlockFee{}, nil, fmt.Errorf("%w: asked for %d to %d, got %d blocks from %d",
			gasometer.ErrMalformedFeeHistory, from, head, n, history.OldestBlock)
	}
	if history.OldestBlock > from {
		c.log.Warn("blocks beyond the RPC history depth are lost; predictions spanning them become unresolvable",
			"from", from, "to", history.OldestBlock-1)
	}
	blocks, missing, err := fee.BlockFees(history, observedAt)
	if err != nil {
		return gasometer.BlockFee{}, nil, fmt.Errorf("summarise fee history: %w", err)
	}
	if err := c.deps.Blocks.Upsert(ctx, c.network.Name, blocks); err != nil {
		return gasometer.BlockFee{}, nil, fmt.Errorf("store blocks %d to %d: %w", history.OldestBlock, head, err)
	}
	if missing > 0 {
		c.log.Warn("fee history rewards missing, treated as empty blocks", "blocks", missing)
	}
	return blocks[len(blocks)-1], history.NextBaseFee(), nil
}

// selfCheck compares the model's next base fee with the RPC's and returns the params the model used.
func (c *Collector) selfCheck(head gasometer.HeadFee, next *big.Int) *gasometer.FeeParams {
	if c.network.Kind != gasometer.NetworkKindOPStack {
		return nil
	}
	params, err := fee.DecodeOPStackExtraData(head.ExtraData)
	if err != nil {
		c.logDecodeError(head, err)
		return nil
	}
	computed, err := fee.NextBaseFee(params, head.BaseFee, head.GasMetered(), head.GasLimit)
	if err != nil {
		c.log.Warn("fee model could not run", "err", err, "block", head.Number)
		return &params
	}
	if computed.Cmp(next) != 0 {
		c.deps.Metrics.ModelMismatch(c.network.Name)
		c.log.Warn("fee model mismatch", "block", head.Number, "model", computed, "rpc", next)
	}
	return &params
}

func (c *Collector) logDecodeError(head gasometer.HeadFee, err error) {
	if !errors.Is(err, gasometer.ErrUnknownExtraDataVersion) {
		c.log.Error("decode extraData", "err", err, "block", head.Number)
		return
	}
	version := head.ExtraData[0]
	if !c.warnedVersions[version] {
		c.warnedVersions[version] = true
		c.log.Error("decode extraData; projecting uncapped", "err", err, "block", head.Number)
	}
}

// readPrice sets the snapshot's price and returns it when fresh.
func (c *Collector) readPrice(ctx context.Context, snapshot *gasometer.FeeSnapshot) *gasometer.PriceQuote {
	if c.deps.Prices == nil {
		return nil
	}
	quote, err := c.deps.Prices.Price(ctx)
	if err != nil {
		c.log.Warn("read price", "err", err)
		return nil
	}
	snapshot.Price = &quote
	if quote.Stale(snapshot.ObservedAt, c.params.PriceMaxAge) {
		c.log.Warn("price is stale; skipping USD metrics", "updated_at", quote.UpdatedAt)
		return nil
	}
	return &quote
}

func (c *Collector) readL1Fees(ctx context.Context, snapshot *gasometer.FeeSnapshot) {
	if c.deps.L1Fees == nil {
		return
	}
	fees, err := c.deps.L1Fees.L1Fees(ctx, c.txs)
	if err != nil {
		c.log.Warn("read L1 fees", "err", err)
		return
	}
	snapshot.L1Fees = fees
}
