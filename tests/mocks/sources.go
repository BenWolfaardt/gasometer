// Package mocks holds hand-written fakes for every repository port.
package mocks

import (
	"context"
	"math/big"
	"sync"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// FeeHistoryCall records one FeeHistory request.
type FeeHistoryCall struct {
	BlockCount, Newest uint64
	Percentiles        []float64
}

// FeeSource is a repositories.FeeSource driven by function fields.
type FeeSource struct {
	mu              sync.Mutex
	FeeHistoryFn    func(blockCount, newest uint64, percentiles []float64) (entities.FeeHistory, error)
	HeadFeeFn       func() (entities.HeadFee, error)
	NodeTipFn       func() (*big.Int, error)
	FeeHistoryCalls []FeeHistoryCall
}

// FeeHistory implements repositories.FeeSource.
func (m *FeeSource) FeeHistory(_ context.Context, blockCount, newest uint64, percentiles []float64) (entities.FeeHistory, error) {
	m.mu.Lock()
	m.FeeHistoryCalls = append(m.FeeHistoryCalls, FeeHistoryCall{blockCount, newest, percentiles})
	m.mu.Unlock()
	return m.FeeHistoryFn(blockCount, newest, percentiles)
}

// HeadFee implements repositories.FeeSource.
func (m *FeeSource) HeadFee(context.Context) (entities.HeadFee, error) { return m.HeadFeeFn() }

// NodeTip implements repositories.FeeSource.
func (m *FeeSource) NodeTip(context.Context) (*big.Int, error) { return m.NodeTipFn() }

// Calls returns a copy of the recorded FeeHistory calls.
func (m *FeeSource) Calls() []FeeHistoryCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]FeeHistoryCall(nil), m.FeeHistoryCalls...)
}

// PriceSource is a repositories.PriceSource driven by a function field.
type PriceSource struct {
	PriceFn func() (entities.PriceQuote, error)
}

// Price implements repositories.PriceSource.
func (m *PriceSource) Price(context.Context) (entities.PriceQuote, error) { return m.PriceFn() }

// L1FeeSource is a repositories.L1FeeSource driven by a function field.
type L1FeeSource struct {
	L1FeesFn func(txs []entities.ReferenceTx) (map[string]*big.Int, error)
}

// L1Fees implements repositories.L1FeeSource.
func (m *L1FeeSource) L1Fees(_ context.Context, txs []entities.ReferenceTx) (map[string]*big.Int, error) {
	return m.L1FeesFn(txs)
}
