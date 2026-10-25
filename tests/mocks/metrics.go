package mocks

import (
	"sync"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
)

// SnapshotRecord is one recorded Snapshot call.
type SnapshotRecord struct {
	Network  string
	Snapshot entities.FeeSnapshot
	Latest   entities.BlockFee
	Price    *entities.PriceQuote
}

// MetricsRecorder records every call.
type MetricsRecorder struct {
	mu          sync.Mutex
	Settlements []repositories.Settlement
	Snapshots   []SnapshotRecord
	Mismatches  map[string]int
	Polls       map[string]int
}

// NewMetricsRecorder returns an empty recorder.
func NewMetricsRecorder() *MetricsRecorder {
	return &MetricsRecorder{Mismatches: map[string]int{}, Polls: map[string]int{}}
}

// Settled implements repositories.MetricsRecorder.
func (m *MetricsRecorder) Settled(s repositories.Settlement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Settlements = append(m.Settlements, s)
}

// Snapshot implements repositories.MetricsRecorder.
func (m *MetricsRecorder) Snapshot(network string, s entities.FeeSnapshot, latest entities.BlockFee, price *entities.PriceQuote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Snapshots = append(m.Snapshots, SnapshotRecord{network, s, latest, price})
}

// ModelMismatch implements repositories.MetricsRecorder.
func (m *MetricsRecorder) ModelMismatch(network string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Mismatches[network]++
}

// PollCompleted implements repositories.MetricsRecorder.
func (m *MetricsRecorder) PollCompleted(network string, _ time.Duration, _ time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Polls[network]++
}

// PollCount returns how many polls completed on network.
func (m *MetricsRecorder) PollCount(network string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Polls[network]
}
