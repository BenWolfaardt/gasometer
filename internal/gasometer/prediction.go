package gasometer

import (
	"math/big"
	"time"
)

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

// Outcome is how a settled prediction ended: included, missed, expired or unresolvable. It is empty while pending.
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

// PredictionInput is what a predictor sees for one horizon.
type PredictionInput struct {
	Snapshot FeeSnapshot
	History  []BlockFee // ascending, the growth window, ending at Snapshot.AtBlock
	Horizon  Horizon
	Ceiling  BaseFeeCeiling // nil when no model applies
}

// Predictor proposes a bid for a horizon. It is a domain interface, not a port: every implementation runs in process.
//
// Guarantees: a bid with its tip, max fee and expected base fee all set.
// Ordering: in.History is ascending and ends at in.Snapshot.AtBlock.
// Errors: ErrInvalidBid when the snapshot lacks the next base fee or node tip the predictor needs;
// ErrInsufficientHistory when the history is too short for it; a projector's error, wrapped.
// Concurrency: safe for concurrent use.
type Predictor interface {
	Name() string
	Predict(in PredictionInput) (Bid, error)
}

// Bid is a predictor's fee, per gas in wei.
type Bid struct {
	Tip             *big.Int
	MaxFee          *big.Int
	ExpectedBaseFee *big.Int
}

// Score is a resolved prediction, per gas in wei.
type Score struct {
	Included        bool
	InclusionBlock  uint64   // when included
	PaidBaseFee     *big.Int // when included
	PaidTip         *big.Int // when included, effective tip
	RequiredBaseFee *big.Int // at the cheapest block in the window
	RequiredTip     *big.Int // at the cheapest block in the window
	EstimateError   float64  // when included
}

// Paid is the per-gas price paid, or nil when missed.
func (s Score) Paid() *big.Int {
	if !s.Included {
		return nil
	}
	return new(big.Int).Add(s.PaidBaseFee, s.PaidTip)
}

// Required is the cheapest per-gas price that would have landed in the window.
func (s Score) Required() *big.Int {
	return new(big.Int).Add(s.RequiredBaseFee, s.RequiredTip)
}

// Overpay is paid minus required, or nil when missed.
func (s Score) Overpay() *big.Int {
	paid := s.Paid()
	if paid == nil {
		return nil
	}
	return paid.Sub(paid, s.Required())
}
