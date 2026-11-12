package poll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/fee"
)

// BackfillerParams are a backfiller's decisions for one network.
type BackfillerParams struct {
	RequiredTipPercentile float64 // eth_feeHistory percentile taken as the required tip
	Chunk                 uint64  // blocks stored per step, so a timeout loses at most one step
}

// BackfillerDeps are a backfiller's collaborators.
type BackfillerDeps struct {
	Fees    gasometer.FeeSource
	Blocks  gasometer.BlockStore
	Metrics gasometer.MetricsRecorder
	Now     func() time.Time
	Log     *slog.Logger
}

// Backfiller fills the holes an outage leaves between stored blocks, oldest first, with whatever time its context
// allows.
type Backfiller struct {
	network string
	params  BackfillerParams
	deps    BackfillerDeps
	log     *slog.Logger
	// cursor is touched only by the network's own poll goroutine. Every block at or below it is stored or reported
	// lost, so holes are looked for only above it. It advances as each block is handled, so a pass cut short by the
	// deadline resumes without rescanning what it has already covered.
	cursor uint64
}

// NewBackfiller builds the backfiller for one network.
func NewBackfiller(network string, params BackfillerParams, deps BackfillerDeps) *Backfiller {
	params.Chunk = max(1, params.Chunk)
	return &Backfiller{network: network, params: params, deps: deps, log: deps.Log.With("network", network)}
}

// Backfill runs one pass and returns how many blocks it stored. A context that ends mid-pass ends it without an
// error: that is how a long backfill spans ticks. A pass that ends there having stored nothing warns, since an
// endpoint too slow for one chunk would otherwise stall the backfill without a trace.
func (b *Backfiller) Backfill(ctx context.Context) (int, error) {
	stored, err := b.pass(ctx)
	if err == nil || ctx.Err() == nil {
		return stored, err
	}
	if stored == 0 {
		b.log.Warn("backfill stored nothing before the tick deadline", "err", err)
	} else {
		b.log.Debug("backfill paused at the tick deadline", "stored", stored, "err", err)
	}
	return stored, nil
}

// Covered is the block at or below which every block is stored or reported lost.
func (b *Backfiller) Covered() uint64 {
	return b.cursor
}

func (b *Backfiller) pass(ctx context.Context) (int, error) {
	holes, err := b.deps.Blocks.Holes(ctx, b.network, b.cursor)
	if err != nil {
		return 0, fmt.Errorf("find holes: %w", err)
	}
	stored := 0
	for _, hole := range holes {
		// Every earlier hole is handled and Holes reports every gap above the cursor, so all below this one is stored.
		b.cursor = max(b.cursor, hole.From-1)
		n, err := b.fill(ctx, hole)
		stored += n
		if err != nil {
			return stored, err
		}
	}
	latest, ok, err := b.deps.Blocks.Latest(ctx, b.network)
	if err != nil {
		return stored, fmt.Errorf("read latest stored block: %w", err)
	}
	if ok {
		b.cursor = max(b.cursor, latest)
	}
	return stored, nil
}

// fill stores hole a chunk at a time. Blocks past the source's history depth are always the oldest part of the hole,
// so whatever is lost is reported once, as one range from the hole's start.
func (b *Backfiller) fill(ctx context.Context, hole gasometer.BlockRange) (int, error) {
	var lost *gasometer.BlockRange
	defer func() { b.reportLost(lost) }()
	observedAt, err := b.observedAt(ctx, hole)
	if err != nil {
		return 0, err
	}
	stored := 0
	for start := hole.From; start <= hole.To; {
		end := min(start+b.params.Chunk-1, hole.To)
		history, err := b.deps.Fees.FeeHistory(ctx, start, end, fee.RewardPercentiles(b.params.RequiredTipPercentile))
		if errors.Is(err, gasometer.ErrBeyondHistory) {
			lost = &gasometer.BlockRange{From: hole.From, To: end}
			start = end + 1
			continue
		}
		if err != nil {
			return stored, fmt.Errorf("read fee history %d to %d: %w", start, end, err)
		}
		if history.OldestBlock > start {
			lost = &gasometer.BlockRange{From: hole.From, To: history.OldestBlock - 1}
		}
		blocks, missing, err := fee.BlockFees(history, observedAt)
		if err != nil {
			return stored, fmt.Errorf("summarise fee history: %w", err)
		}
		if err := b.deps.Blocks.Upsert(ctx, b.network, blocks); err != nil {
			return stored, fmt.Errorf("store blocks %d to %d: %w", history.OldestBlock, end, err)
		}
		if missing > 0 {
			b.log.Warn("fee history rewards missing, treated as empty blocks", "blocks", missing)
		}
		stored += len(blocks)
		b.deps.Metrics.BlocksBackfilled(b.network, len(blocks))
		b.cursor = max(b.cursor, end)
		start = end + 1
	}
	return stored, nil
}

// observedAt is the observation time of the block above hole. Prune deletes by observation time, so stamping a
// backfilled block with the time it was filled would let a prune remove the newer blocks above it first and open a
// hole in the middle of the table.
func (b *Backfiller) observedAt(ctx context.Context, hole gasometer.BlockRange) (time.Time, error) {
	above, err := b.deps.Blocks.Range(ctx, b.network, hole.To+1, hole.To+1)
	if err != nil {
		return time.Time{}, fmt.Errorf("read block %d: %w", hole.To+1, err)
	}
	if len(above) == 0 {
		return b.deps.Now(), nil
	}
	return above[0].ObservedAt, nil
}

func (b *Backfiller) reportLost(lost *gasometer.BlockRange) {
	if lost == nil {
		return
	}
	b.log.Warn("blocks beyond the RPC history depth are lost; predictions spanning them become unresolvable",
		"from", lost.From, "to", lost.To)
	b.deps.Metrics.BlocksLost(b.network, lost.Len())
	b.cursor = max(b.cursor, lost.To)
}
