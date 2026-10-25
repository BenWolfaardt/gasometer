// Package usecases orchestrates gasometer's per-network loop: collect, predict, score and prune.
package usecases

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/rs/zerolog"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

// Observation is what one poll saw.
type Observation struct {
	Snapshot entities.FeeSnapshot
	Head     entities.HeadFee
	Params   *entities.FeeParams // nil when no model applies
}

// CollectFees polls one network, stores its blocks and a snapshot, and runs the fee model self-check.
type CollectFees interface {
	Execute(ctx context.Context) (Observation, error)
}

// CollectDeps are CollectFees' collaborators. Prices and L1Fees may be nil.
type CollectDeps struct {
	Fees      repositories.FeeSource
	Prices    repositories.PriceSource
	L1Fees    repositories.L1FeeSource
	Blocks    repositories.BlockStore
	Snapshots repositories.SnapshotStore
	Metrics   repositories.MetricsRecorder
	Now       func() time.Time
	Log       zerolog.Logger
}

type collectFees struct {
	network entities.NetworkConfig
	txs     []entities.ReferenceTx
	deps    CollectDeps
	// warnedVersions is touched only by the network's own loop goroutine.
	warnedVersions map[byte]bool
}

// NewCollectFees builds the collector for one network.
func NewCollectFees(network entities.NetworkConfig, txs []entities.ReferenceTx, deps CollectDeps) CollectFees {
	deps.Log = deps.Log.With().Str("network", network.Network.Name).Logger()
	return &collectFees{network: network, txs: txs, deps: deps, warnedVersions: map[byte]bool{}}
}

func (c *collectFees) Execute(ctx context.Context) (Observation, error) {
	name := c.network.Network.Name
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
	snapshot := entities.FeeSnapshot{
		Network:     name,
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
	c.deps.Metrics.Snapshot(name, snapshot, latest, freshPrice)
	return Observation{Snapshot: snapshot, Head: head, Params: params}, nil
}

// firstBlock is the first block to fetch: the one after the latest stored, bounded by the RPC's history depth.
// A first run fetches one batch.
func (c *collectFees) firstBlock(ctx context.Context, head uint64) (uint64, error) {
	latest, ok, err := c.deps.Blocks.Latest(ctx, c.network.Network.Name)
	if err != nil {
		return 0, fmt.Errorf("read latest stored block: %w", err)
	}
	if !ok {
		return blocksBack(head, c.network.FeeHistoryBatch), nil
	}
	if latest >= head {
		return head, nil
	}
	floor := blocksBack(head, c.network.HistoryDepth)
	if latest+1 < floor {
		c.deps.Log.Warn().Uint64("from", latest+1).Uint64("to", floor-1).
			Msg("blocks beyond the RPC history depth are lost; predictions spanning them become unresolvable")
		return floor, nil
	}
	return latest + 1, nil
}

// blocksBack is the first of the n blocks ending at head.
func blocksBack(head, n uint64) uint64 {
	if n == 0 || n > head {
		return 0
	}
	return head - n + 1
}

// storeBlocks fetches and stores [from, head] in batches, returning the head block and the next base fee.
func (c *collectFees) storeBlocks(ctx context.Context, from, head uint64, observedAt time.Time) (entities.BlockFee, *big.Int, error) {
	name := c.network.Network.Name
	percentiles := services.RewardPercentiles(c.network.RequiredTipPercentile)
	batch := max(1, c.network.FeeHistoryBatch)
	var (
		latest  entities.BlockFee
		next    *big.Int
		missing int
	)
	for start := from; start <= head; start += batch {
		end := min(start+batch-1, head)
		count := end - start + 1
		history, err := c.deps.Fees.FeeHistory(ctx, count, end, percentiles)
		if err != nil {
			return entities.BlockFee{}, nil, fmt.Errorf("read fee history to %d: %w", end, err)
		}
		if history.OldestBlock != start || uint64(history.Len()) != count {
			return entities.BlockFee{}, nil, fmt.Errorf("%w: asked for %d blocks from %d, got %d from %d",
				entities.ErrMalformedFeeHistory, count, start, history.Len(), history.OldestBlock)
		}
		blocks, missingRewards, err := services.BlockFees(history, observedAt)
		if err != nil {
			return entities.BlockFee{}, nil, fmt.Errorf("summarise fee history to %d: %w", end, err)
		}
		if err := c.deps.Blocks.Upsert(ctx, name, blocks); err != nil {
			return entities.BlockFee{}, nil, fmt.Errorf("store blocks %d to %d: %w", start, end, err)
		}
		missing += missingRewards
		latest, next = blocks[len(blocks)-1], history.NextBaseFee()
	}
	if missing > 0 {
		c.deps.Log.Warn().Int("blocks", missing).Msg("fee history rewards missing, treated as empty blocks")
	}
	return latest, next, nil
}

// selfCheck compares the model's next base fee with the RPC's and returns the params the model used.
func (c *collectFees) selfCheck(head entities.HeadFee, next *big.Int) *entities.FeeParams {
	if c.network.Network.Kind != entities.NetworkKindOPStack {
		return nil
	}
	params, err := services.DecodeOPStackExtraData(head.ExtraData)
	if err != nil {
		c.logDecodeError(head, err)
		return nil
	}
	computed, err := services.NextBaseFee(params, head.BaseFee, head.GasMetered(), head.GasLimit)
	if err != nil {
		c.deps.Log.Warn().Err(err).Uint64("block", head.Number).Msg("fee model could not run")
		return &params
	}
	if computed.Cmp(next) != 0 {
		c.deps.Metrics.ModelMismatch(c.network.Network.Name)
		c.deps.Log.Warn().Uint64("block", head.Number).Stringer("model", computed).Stringer("rpc", next).
			Msg("fee model mismatch")
	}
	return &params
}

func (c *collectFees) logDecodeError(head entities.HeadFee, err error) {
	if !errors.Is(err, entities.ErrUnknownExtraDataVersion) {
		c.deps.Log.Error().Err(err).Uint64("block", head.Number).Msg("decode extraData")
		return
	}
	version := head.ExtraData[0]
	if !c.warnedVersions[version] {
		c.warnedVersions[version] = true
		c.deps.Log.Error().Err(err).Uint64("block", head.Number).Msg("decode extraData; projecting uncapped")
	}
}

// readPrice sets the snapshot's price and returns it when fresh.
func (c *collectFees) readPrice(ctx context.Context, snapshot *entities.FeeSnapshot) *entities.PriceQuote {
	if c.deps.Prices == nil {
		return nil
	}
	quote, err := c.deps.Prices.Price(ctx)
	if err != nil {
		c.deps.Log.Warn().Err(err).Msg("read price")
		return nil
	}
	snapshot.Price = &quote
	if quote.Stale(snapshot.ObservedAt, c.network.PriceMaxAge) {
		c.deps.Log.Warn().Time("updated_at", quote.UpdatedAt).Msg("price is stale; skipping USD metrics")
		return nil
	}
	return &quote
}

func (c *collectFees) readL1Fees(ctx context.Context, snapshot *entities.FeeSnapshot) {
	if c.deps.L1Fees == nil {
		return
	}
	fees, err := c.deps.L1Fees.L1Fees(ctx, c.txs)
	if err != nil {
		c.deps.Log.Warn().Err(err).Msg("read L1 fees")
		return
	}
	snapshot.L1Fees = fees
}
