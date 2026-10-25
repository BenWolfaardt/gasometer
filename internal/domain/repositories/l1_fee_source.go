package repositories

import (
	"context"
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// L1FeeSource reads the L1 data fee of reference transactions, keyed by name.
type L1FeeSource interface {
	L1Fees(ctx context.Context, txs []entities.ReferenceTx) (map[string]*big.Int, error)
}
