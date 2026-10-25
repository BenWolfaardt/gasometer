package entities

import "time"

// PredictionStatus is a prediction's lifecycle state.
type PredictionStatus string

const (
	PredictionPending      PredictionStatus = "pending"
	PredictionResolved     PredictionStatus = "resolved"
	PredictionExpired      PredictionStatus = "expired"
	PredictionUnresolvable PredictionStatus = "unresolvable"
)

const (
	OutcomeIncluded     = "included"
	OutcomeMissed       = "missed"
	OutcomeExpired      = "expired"
	OutcomeUnresolvable = "unresolvable"
)

// Horizon is how far ahead a bid must land.
type Horizon struct {
	Seconds int
	Blocks  int
}

// Prediction is one predictor's bid at one horizon against one snapshot.
type Prediction struct {
	ID         int64
	SnapshotID int64
	Network    string
	Predictor  string
	Horizon    Horizon
	AtBlock    uint64
	MadeAt     time.Time
	Bid        Bid
	Status     PredictionStatus
	ResolvedAt time.Time
	Score      *Score // set once resolved
}

// Outcome is the metric label of a settled prediction.
func (p Prediction) Outcome() string {
	switch p.Status {
	case PredictionResolved:
		if p.Score != nil && p.Score.Included {
			return OutcomeIncluded
		}
		return OutcomeMissed
	case PredictionExpired:
		return OutcomeExpired
	case PredictionUnresolvable:
		return OutcomeUnresolvable
	default:
		return ""
	}
}
