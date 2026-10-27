// Package mock holds hand-written, function-field mocks of every gasometer port.
package mock

import (
	"context"
	"math/big"
	"sync"

	"github.com/BenWolfaardt/gasometer"
)

// FeeHistoryCall records one FeeHistory request.
type FeeHistoryCall struct {
	From, To    uint64
	Percentiles []float64
}

var _ gasometer.FeeSource = (*FeeSource)(nil)

// FeeSource is a gasometer.FeeSource driven by function fields.
type FeeSource struct {
	mu              sync.Mutex
	FeeHistoryFn    func(from, to uint64, percentiles []float64) (gasometer.FeeHistory, error)
	HeadFeeFn       func() (gasometer.HeadFee, error)
	NodeTipFn       func() (*big.Int, error)
	FeeHistoryCalls []FeeHistoryCall
}

// FeeHistory implements gasometer.FeeSource.
func (m *FeeSource) FeeHistory(_ context.Context, from, to uint64, percentiles []float64) (gasometer.FeeHistory, error) {
	m.mu.Lock()
	m.FeeHistoryCalls = append(m.FeeHistoryCalls, FeeHistoryCall{from, to, percentiles})
	m.mu.Unlock()
	return m.FeeHistoryFn(from, to, percentiles)
}

// HeadFee implements gasometer.FeeSource.
func (m *FeeSource) HeadFee(context.Context) (gasometer.HeadFee, error) { return m.HeadFeeFn() }

// NodeTip implements gasometer.FeeSource.
func (m *FeeSource) NodeTip(context.Context) (*big.Int, error) { return m.NodeTipFn() }

// Calls returns a copy of the recorded FeeHistory calls.
func (m *FeeSource) Calls() []FeeHistoryCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]FeeHistoryCall(nil), m.FeeHistoryCalls...)
}

var _ gasometer.PriceSource = (*PriceSource)(nil)

// PriceSource is a gasometer.PriceSource driven by a function field.
type PriceSource struct {
	PriceFn func() (gasometer.PriceQuote, error)
}

// Price implements gasometer.PriceSource.
func (m *PriceSource) Price(context.Context) (gasometer.PriceQuote, error) { return m.PriceFn() }

var _ gasometer.L1FeeSource = (*L1FeeSource)(nil)

// L1FeeSource is a gasometer.L1FeeSource driven by a function field.
type L1FeeSource struct {
	L1FeesFn func(txs []gasometer.ReferenceTx) (map[string]*big.Int, error)
}

// L1Fees implements gasometer.L1FeeSource.
func (m *L1FeeSource) L1Fees(_ context.Context, txs []gasometer.ReferenceTx) (map[string]*big.Int, error) {
	return m.L1FeesFn(txs)
}

var _ gasometer.SAESource = (*SAESource)(nil)

// SAESource is a gasometer.SAESource driven by a function field.
type SAESource struct {
	SAEBlocksFn func(head uint64) ([]gasometer.SAEBlock, error)
}

// SAEBlocks implements gasometer.SAESource.
func (m *SAESource) SAEBlocks(_ context.Context, head uint64) ([]gasometer.SAEBlock, error) {
	return m.SAEBlocksFn(head)
}

var _ gasometer.SAEModel = (*SAEModel)(nil)

// SAEModel is a gasometer.SAEModel driven by function fields.
type SAEModel struct {
	BaseFeeFn     func(blocks []gasometer.SAEBlock) (*big.Int, error)
	NextBaseFeeFn func(blocks []gasometer.SAEBlock) (*big.Int, error)
	CeilingFn     func(blocks []gasometer.SAEBlock) gasometer.BaseFeeCeiling
}

// BaseFee implements gasometer.SAEModel.
func (m *SAEModel) BaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) { return m.BaseFeeFn(blocks) }

// NextBaseFee implements gasometer.SAEModel.
func (m *SAEModel) NextBaseFee(blocks []gasometer.SAEBlock) (*big.Int, error) {
	return m.NextBaseFeeFn(blocks)
}

// Ceiling implements gasometer.SAEModel.
func (m *SAEModel) Ceiling(blocks []gasometer.SAEBlock) gasometer.BaseFeeCeiling {
	return m.CeilingFn(blocks)
}
