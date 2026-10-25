// Package repositories defines the ports gasometer's use cases depend on.
package repositories

import (
	"context"
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// FeeSource reads one network's fee data.
type FeeSource interface {
	// FeeHistory covers blockCount blocks ending at newest.
	FeeHistory(ctx context.Context, blockCount, newest uint64, percentiles []float64) (entities.FeeHistory, error)
	HeadFee(ctx context.Context) (entities.HeadFee, error)
	NodeTip(ctx context.Context) (*big.Int, error)
}
