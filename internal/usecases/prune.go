package usecases

import (
	"context"
	"fmt"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
)

// Prune drops block rows past retention.
type Prune interface {
	Execute(ctx context.Context) (int64, error)
}

type prune struct {
	blocks    repositories.BlockStore
	retention time.Duration
	now       func() time.Time
}

// NewPrune builds the prune step.
func NewPrune(blocks repositories.BlockStore, retention time.Duration, now func() time.Time) Prune {
	return &prune{blocks: blocks, retention: retention, now: now}
}

func (p *prune) Execute(ctx context.Context) (int64, error) {
	n, err := p.blocks.Prune(ctx, p.now().Add(-p.retention))
	if err != nil {
		return 0, fmt.Errorf("prune blocks: %w", err)
	}
	return n, nil
}
