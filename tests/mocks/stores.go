package mocks

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// BlockStore is an in-memory repositories.BlockStore with injectable errors.
type BlockStore struct {
	mu        sync.Mutex
	Blocks    map[string]map[uint64]entities.BlockFee
	AvgTime   map[string]time.Duration
	Err       error
	PruneCuts []time.Time
}

// NewBlockStore returns an empty BlockStore.
func NewBlockStore() *BlockStore {
	return &BlockStore{Blocks: map[string]map[uint64]entities.BlockFee{}, AvgTime: map[string]time.Duration{}}
}

// Upsert implements repositories.BlockStore.
func (m *BlockStore) Upsert(_ context.Context, network string, blocks []entities.BlockFee) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	if m.Blocks[network] == nil {
		m.Blocks[network] = map[uint64]entities.BlockFee{}
	}
	for _, b := range blocks {
		m.Blocks[network][b.Number] = b
	}
	return nil
}

// Range implements repositories.BlockStore.
func (m *BlockStore) Range(_ context.Context, network string, from, to uint64) ([]entities.BlockFee, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	var out []entities.BlockFee
	for n, b := range m.Blocks[network] {
		if n >= from && n <= to {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b entities.BlockFee) int { return cmp.Compare(a.Number, b.Number) })
	return out, nil
}

// Latest implements repositories.BlockStore.
func (m *BlockStore) Latest(_ context.Context, network string) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, false, m.Err
	}
	var latest uint64
	found := false
	for n := range m.Blocks[network] {
		if !found || n > latest {
			latest, found = n, true
		}
	}
	return latest, found, nil
}

// AverageBlockTime implements repositories.BlockStore from AvgTime.
func (m *BlockStore) AverageBlockTime(_ context.Context, network string) (time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	d, ok := m.AvgTime[network]
	if !ok {
		return 0, entities.ErrInsufficientHistory
	}
	return d, nil
}

// Prune implements repositories.BlockStore.
func (m *BlockStore) Prune(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	m.PruneCuts = append(m.PruneCuts, cutoff)
	var n int64
	for _, blocks := range m.Blocks {
		for k, b := range blocks {
			if b.ObservedAt.Before(cutoff) {
				delete(blocks, k)
				n++
			}
		}
	}
	return n, nil
}

// SnapshotStore is an in-memory repositories.SnapshotStore.
type SnapshotStore struct {
	mu        sync.Mutex
	Snapshots map[int64]entities.FeeSnapshot
	Err       error
	nextID    int64
}

// NewSnapshotStore returns an empty SnapshotStore.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{Snapshots: map[int64]entities.FeeSnapshot{}}
}

// Insert implements repositories.SnapshotStore.
func (m *SnapshotStore) Insert(_ context.Context, s entities.FeeSnapshot) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	m.nextID++
	s.ID = m.nextID
	m.Snapshots[s.ID] = s
	return s.ID, nil
}

// Get implements repositories.SnapshotStore.
func (m *SnapshotStore) Get(_ context.Context, ids []int64) (map[int64]entities.FeeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	out := map[int64]entities.FeeSnapshot{}
	for _, id := range ids {
		if s, ok := m.Snapshots[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

// PredictionStore is an in-memory repositories.PredictionStore.
type PredictionStore struct {
	mu          sync.Mutex
	Predictions []entities.Prediction
	Settled     []entities.Prediction
	Err         error
}

// Insert implements repositories.PredictionStore.
func (m *PredictionStore) Insert(_ context.Context, ps []entities.Prediction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	for _, p := range ps {
		p.ID = int64(len(m.Predictions) + 1)
		m.Predictions = append(m.Predictions, p)
	}
	return nil
}

// Pending implements repositories.PredictionStore.
func (m *PredictionStore) Pending(_ context.Context, network string) ([]entities.Prediction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	var out []entities.Prediction
	for _, p := range m.Predictions {
		if p.Network == network && p.Status == entities.PredictionPending {
			out = append(out, p)
		}
	}
	return out, nil
}

// Settle implements repositories.PredictionStore.
func (m *PredictionStore) Settle(_ context.Context, ps []entities.Prediction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	for _, p := range ps {
		m.Predictions[p.ID-1] = p
		m.Settled = append(m.Settled, p)
	}
	return nil
}
