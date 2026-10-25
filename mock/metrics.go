package mock

import (
	"sync"
	"time"

	"github.com/BenWolfaardt/gasometer"
)

// SnapshotRecord is one recorded Snapshot call.
type SnapshotRecord struct {
	Network  string
	Snapshot gasometer.FeeSnapshot
	Latest   gasometer.BlockFee
	Price    *gasometer.PriceQuote
}

var _ gasometer.MetricsRecorder = (*MetricsRecorder)(nil)

// MetricsRecorder records every call.
type MetricsRecorder struct {
	mu          sync.Mutex
	Settlements []gasometer.Settlement
	Snapshots   []SnapshotRecord
	Mismatches  map[string]int
	Polls       map[string]int
}

// NewMetricsRecorder returns an empty recorder.
func NewMetricsRecorder() *MetricsRecorder {
	return &MetricsRecorder{Mismatches: map[string]int{}, Polls: map[string]int{}}
}

// Settled implements gasometer.MetricsRecorder.
func (m *MetricsRecorder) Settled(s gasometer.Settlement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Settlements = append(m.Settlements, s)
}

// Snapshot implements gasometer.MetricsRecorder.
func (m *MetricsRecorder) Snapshot(network string, s gasometer.FeeSnapshot, latest gasometer.BlockFee, price *gasometer.PriceQuote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Snapshots = append(m.Snapshots, SnapshotRecord{network, s, latest, price})
}

// ModelMismatch implements gasometer.MetricsRecorder.
func (m *MetricsRecorder) ModelMismatch(network string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Mismatches[network]++
}

// PollCompleted implements gasometer.MetricsRecorder.
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
