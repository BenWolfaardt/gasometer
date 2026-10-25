package mocks

import "github.com/BenWolfaardt/gasometer/internal/domain/repositories"

var (
	_ repositories.FeeSource       = (*FeeSource)(nil)
	_ repositories.PriceSource     = (*PriceSource)(nil)
	_ repositories.L1FeeSource     = (*L1FeeSource)(nil)
	_ repositories.BlockStore      = (*BlockStore)(nil)
	_ repositories.SnapshotStore   = (*SnapshotStore)(nil)
	_ repositories.PredictionStore = (*PredictionStore)(nil)
	_ repositories.MetricsRecorder = (*MetricsRecorder)(nil)
)
