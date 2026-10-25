package repositories

import (
	"context"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// SnapshotStore persists fee snapshots.
type SnapshotStore interface {
	// Insert stores the snapshot and returns its ID.
	Insert(ctx context.Context, snapshot entities.FeeSnapshot) (int64, error)
	Get(ctx context.Context, ids []int64) (map[int64]entities.FeeSnapshot, error)
}

// PredictionStore persists predictions and their scores.
type PredictionStore interface {
	Insert(ctx context.Context, predictions []entities.Prediction) error
	Pending(ctx context.Context, network string) ([]entities.Prediction, error)
	// Settle writes the status and score of predictions that are no longer pending.
	Settle(ctx context.Context, predictions []entities.Prediction) error
}
