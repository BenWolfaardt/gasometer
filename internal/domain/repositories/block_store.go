package repositories

import (
	"context"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// BlockStore persists per-block fee summaries.
type BlockStore interface {
	Upsert(ctx context.Context, network string, blocks []entities.BlockFee) error
	// Range returns stored blocks in [from, to], ascending; missing blocks are absent.
	Range(ctx context.Context, network string, from, to uint64) ([]entities.BlockFee, error)
	// Latest is the highest stored block number; ok is false when none is stored.
	Latest(ctx context.Context, network string) (number uint64, ok bool, err error)
	// AverageBlockTime is measured over every stored block; ErrInsufficientHistory before two polls.
	AverageBlockTime(ctx context.Context, network string) (time.Duration, error)
	// Prune deletes blocks observed before cutoff and returns how many.
	Prune(ctx context.Context, cutoff time.Time) (int64, error)
}
