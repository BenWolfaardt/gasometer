package usecases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
)

// ScorePredictions resolves, expires or marks unresolvable the pending predictions of one network.
type ScorePredictions interface {
	Execute(ctx context.Context, head uint64) (int, error)
}

// ScoreDeps are ScorePredictions' collaborators.
type ScoreDeps struct {
	Blocks      repositories.BlockStore
	Snapshots   repositories.SnapshotStore
	Predictions repositories.PredictionStore
	Metrics     repositories.MetricsRecorder
	Now         func() time.Time
	Log         zerolog.Logger
}

type scorePredictions struct {
	network      entities.NetworkConfig
	expiryFactor int
	txs          []entities.ReferenceTx
	deps         ScoreDeps
}

// NewScorePredictions builds the score step for one network.
func NewScorePredictions(network entities.NetworkConfig, expiryFactor int, txs []entities.ReferenceTx, deps ScoreDeps) ScorePredictions {
	deps.Log = deps.Log.With().Str("network", network.Network.Name).Logger()
	return &scorePredictions{network: network, expiryFactor: expiryFactor, txs: txs, deps: deps}
}

func (s *scorePredictions) Execute(ctx context.Context, head uint64) (int, error) {
	name := s.network.Network.Name
	pending, err := s.deps.Predictions.Pending(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("load pending predictions: %w", err)
	}
	now := s.deps.Now()
	var settled, ready []entities.Prediction
	for _, p := range pending {
		switch {
		case lastBlock(p) <= head:
			ready = append(ready, p)
		case now.Sub(p.MadeAt) > time.Duration(s.expiryFactor*p.Horizon.Seconds)*time.Second:
			p.Status, p.ResolvedAt = entities.PredictionExpired, now
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

func lastBlock(p entities.Prediction) uint64 {
	return p.AtBlock + uint64(p.Horizon.Blocks)
}

// resolve scores every ready prediction against one load of the blocks they span.
func (s *scorePredictions) resolve(ctx context.Context, ready []entities.Prediction, now time.Time) ([]entities.Prediction, error) {
	if len(ready) == 0 {
		return nil, nil
	}
	from, to := ready[0].AtBlock+1, lastBlock(ready[0])
	for _, p := range ready[1:] {
		from, to = min(from, p.AtBlock+1), max(to, lastBlock(p))
	}
	stored, err := s.deps.Blocks.Range(ctx, s.network.Network.Name, from, to)
	if err != nil {
		return nil, fmt.Errorf("load blocks %d to %d: %w", from, to, err)
	}
	byNumber := make(map[uint64]entities.BlockFee, len(stored))
	for _, b := range stored {
		byNumber[b.Number] = b
	}

	out := make([]entities.Prediction, 0, len(ready))
	for _, p := range ready {
		window := make([]entities.BlockFee, 0, p.Horizon.Blocks)
		for n := p.AtBlock + 1; n <= lastBlock(p); n++ {
			if b, ok := byNumber[n]; ok {
				window = append(window, b)
			}
		}
		p.ResolvedAt = now
		score, err := services.ScoreBid(p.Bid, p.AtBlock, p.Horizon.Blocks, window)
		if err != nil {
			if !errors.Is(err, entities.ErrBlockGap) {
				s.deps.Log.Error().Err(err).Int64("prediction", p.ID).Msg("score prediction")
			}
			p.Status = entities.PredictionUnresolvable
			out = append(out, p)
			continue
		}
		p.Status, p.Score = entities.PredictionResolved, &score
		out = append(out, p)
	}
	return out, nil
}

// record exports every settled prediction, pricing included ones with their snapshot's fresh price.
func (s *scorePredictions) record(ctx context.Context, settled []entities.Prediction) error {
	var ids []int64
	for _, p := range settled {
		if p.Score != nil && p.Score.Included {
			ids = append(ids, p.SnapshotID)
		}
	}
	snapshots := map[int64]entities.FeeSnapshot{}
	if len(ids) > 0 {
		var err error
		if snapshots, err = s.deps.Snapshots.Get(ctx, ids); err != nil {
			return fmt.Errorf("load snapshots: %w", err)
		}
	}
	for _, p := range settled {
		settlement := repositories.Settlement{
			Network:        p.Network,
			Predictor:      p.Predictor,
			HorizonSeconds: p.Horizon.Seconds,
			Outcome:        p.Outcome(),
		}
		if p.Score != nil && p.Score.Included {
			settlement.OverpayRatio = services.OverpayRatio(*p.Score)
			settlement.EstimateError = p.Score.EstimateError
			snapshot, ok := snapshots[p.SnapshotID]
			if ok && snapshot.Price != nil && !snapshot.Price.Stale(snapshot.ObservedAt, s.network.PriceMaxAge) {
				settlement.Costs = services.TxCosts(*p.Score, snapshot, *snapshot.Price, s.txs)
			}
		}
		s.deps.Metrics.Settled(settlement)
	}
	return nil
}
