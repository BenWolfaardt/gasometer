package poll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/score"
)

// SettlerParams are a settler's decisions.
type SettlerParams struct {
	ExpiryFactor int // horizons a prediction waits for the network before it expires
	PriceMaxAge  time.Duration
}

// SettlerDeps are a settler's collaborators.
type SettlerDeps struct {
	Blocks      gasometer.BlockStore
	Snapshots   gasometer.SnapshotStore
	Predictions gasometer.PredictionStore
	Metrics     gasometer.MetricsRecorder
	Now         func() time.Time
	Log         *slog.Logger
}

// Settler resolves, expires or marks unresolvable the pending predictions of one network.
type Settler struct {
	network string
	params  SettlerParams
	txs     []gasometer.ReferenceTx
	deps    SettlerDeps
	log     *slog.Logger
}

// NewSettler builds the settler for one network.
func NewSettler(network string, params SettlerParams, txs []gasometer.ReferenceTx, deps SettlerDeps) *Settler {
	return &Settler{network: network, params: params, txs: txs, deps: deps, log: deps.Log.With("network", network)}
}

// Settle moves every pending prediction it can out of pending, given the network's head, and returns how many.
func (s *Settler) Settle(ctx context.Context, head uint64) (int, error) {
	pending, err := s.deps.Predictions.Pending(ctx, s.network)
	if err != nil {
		return 0, fmt.Errorf("load pending predictions: %w", err)
	}
	now := s.deps.Now()
	var settled, ready []gasometer.Prediction
	for _, p := range pending {
		switch {
		case lastBlock(p) <= head:
			ready = append(ready, p)
		case now.Sub(p.MadeAt) > time.Duration(s.params.ExpiryFactor*p.Horizon.Seconds)*time.Second:
			p.Status, p.ResolvedAt = gasometer.PredictionExpired, now
			settled = append(settled, p)
		}
	}
	resolved, err := s.resolve(ctx, ready, now)
	if err != nil {
		return 0, err
	}
	settled = append(settled, resolved...)
	if len(settled) == 0 {
		return 0, nil
	}
	if err := s.deps.Predictions.Settle(ctx, settled); err != nil {
		return 0, fmt.Errorf("settle predictions: %w", err)
	}
	if err := s.record(ctx, settled); err != nil {
		return len(settled), err
	}
	return len(settled), nil
}

func lastBlock(p gasometer.Prediction) uint64 {
	return p.AtBlock + uint64(p.Horizon.Blocks)
}

// resolve scores every ready prediction against one load of the blocks they span.
func (s *Settler) resolve(ctx context.Context, ready []gasometer.Prediction, now time.Time) ([]gasometer.Prediction, error) {
	if len(ready) == 0 {
		return nil, nil
	}
	from, to := ready[0].AtBlock+1, lastBlock(ready[0])
	for _, p := range ready[1:] {
		from, to = min(from, p.AtBlock+1), max(to, lastBlock(p))
	}
	stored, err := s.deps.Blocks.Range(ctx, s.network, from, to)
	if err != nil {
		return nil, fmt.Errorf("load blocks %d to %d: %w", from, to, err)
	}
	byNumber := make(map[uint64]gasometer.BlockFee, len(stored))
	for _, b := range stored {
		byNumber[b.Number] = b
	}

	out := make([]gasometer.Prediction, 0, len(ready))
	for _, p := range ready {
		window := make([]gasometer.BlockFee, 0, p.Horizon.Blocks)
		for n := p.AtBlock + 1; n <= lastBlock(p); n++ {
			if b, ok := byNumber[n]; ok {
				window = append(window, b)
			}
		}
		p.ResolvedAt = now
		result, err := score.Bid(p.Bid, p.AtBlock, p.Horizon.Blocks, window)
		if err != nil {
			if !errors.Is(err, gasometer.ErrBlockGap) {
				s.log.Error("score prediction", "err", err, "prediction", p.ID)
			}
			p.Status = gasometer.PredictionUnresolvable
			out = append(out, p)
			continue
		}
		p.Status, p.Score = gasometer.PredictionResolved, &result
		out = append(out, p)
	}
	return out, nil
}

// record exports every settled prediction, pricing included ones with their snapshot's fresh price.
func (s *Settler) record(ctx context.Context, settled []gasometer.Prediction) error {
	var ids []int64
	for _, p := range settled {
		if p.Score != nil && p.Score.Included {
			ids = append(ids, p.SnapshotID)
		}
	}
	snapshots := map[int64]gasometer.FeeSnapshot{}
	if len(ids) > 0 {
		var err error
		if snapshots, err = s.deps.Snapshots.Get(ctx, ids); err != nil {
			return fmt.Errorf("load snapshots: %w", err)
		}
	}
	for _, p := range settled {
		settlement := gasometer.Settlement{
			Network:        p.Network,
			Predictor:      p.Predictor,
			HorizonSeconds: p.Horizon.Seconds,
			Outcome:        p.Outcome(),
		}
		if p.Score != nil && p.Score.Included {
			settlement.OverpayRatio = score.OverpayRatio(*p.Score)
			settlement.EstimateError = p.Score.EstimateError
			snapshot, ok := snapshots[p.SnapshotID]
			if ok && snapshot.Price != nil && !snapshot.Price.Stale(snapshot.ObservedAt, s.params.PriceMaxAge) {
				settlement.Costs = score.TxCosts(*p.Score, snapshot, *snapshot.Price, s.txs)
			}
		}
		s.deps.Metrics.Settled(settlement)
	}
	return nil
}
