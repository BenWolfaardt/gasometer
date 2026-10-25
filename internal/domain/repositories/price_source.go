package repositories

import (
	"context"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// PriceSource reads a network's coin price in USD.
type PriceSource interface {
	Price(ctx context.Context) (entities.PriceQuote, error)
}
