package repositories

import (
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// Settlement is one prediction leaving pending, as the metrics see it.
type Settlement struct {
	Network        string
	Predictor      string
	HorizonSeconds int
	Outcome        string
	OverpayRatio   *float64          // nil unless included with a non-zero required
	EstimateError  float64           // when included
	Costs          []entities.TxCost // when included and the price was fresh
}

// MetricsRecorder exports what the use cases observe.
type MetricsRecorder interface {
	Settled(s Settlement)
	// Snapshot records one poll's fees; price is nil when missing or stale.
	Snapshot(network string, snapshot entities.FeeSnapshot, latest entities.BlockFee, price *entities.PriceQuote)
	ModelMismatch(network string)
	PollCompleted(network string, took time.Duration, at time.Time)
}
