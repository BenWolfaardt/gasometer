package mock

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"sync"

	"github.com/BenWolfaardt/gasometer"
)

var _ gasometer.ProbeChain = (*ProbeChain)(nil)

// ProbeChain is an in-memory gasometer.ProbeChain. Accept decides what the node does with each sent transaction: an
// error refuses it, a nil inclusion leaves it pending until Include is called.
type ProbeChain struct {
	mu         sync.Mutex
	BaseFeeWei *big.Int
	NodeTipWei *big.Int
	HeadBlock  uint64
	Balances   map[int]*big.Int
	Latest     map[int]uint64
	Pending    map[int]uint64
	Accept     func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error)
	Err        error // returned by every read
	sent       []gasometer.ProbeTx
	included   map[string]*gasometer.Inclusion
}

// NewProbeChain returns a chain with no accounts, including every transaction at block 1.
func NewProbeChain() *ProbeChain {
	return &ProbeChain{
		BaseFeeWei: big.NewInt(1_000), NodeTipWei: big.NewInt(150), HeadBlock: 1,
		Balances: map[int]*big.Int{}, Latest: map[int]uint64{}, Pending: map[int]uint64{},
		Accept: func(gasometer.ProbeTx) (*gasometer.Inclusion, error) {
			return &gasometer.Inclusion{Block: 1, TimeMilliseconds: 1_000}, nil
		},
		included: map[string]*gasometer.Inclusion{},
	}
}

// ProbeTxHash is the hash ProbeChain gives tx.
func ProbeTxHash(tx gasometer.ProbeTx) string {
	return fmt.Sprintf("%d:%d:%d/%d->%d:%s", tx.From, tx.Nonce, tx.Tip, tx.MaxFee, tx.To, tx.Value)
}

// Address implements gasometer.ProbeChain.
func (m *ProbeChain) Address(account int) string { return fmt.Sprintf("account-%d", account) }

// Nonces implements gasometer.ProbeChain.
func (m *ProbeChain) Nonces(_ context.Context, account int) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Latest[account], max(m.Latest[account], m.Pending[account]), m.Err
}

// Balance implements gasometer.ProbeChain.
func (m *ProbeChain) Balance(_ context.Context, account int) (*big.Int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.Balances[account]; b != nil {
		return new(big.Int).Set(b), m.Err
	}
	return new(big.Int), m.Err
}

// BaseFee implements gasometer.ProbeChain.
func (m *ProbeChain) BaseFee(context.Context) (*big.Int, error) { return m.BaseFeeWei, m.Err }

// NodeTip implements gasometer.ProbeChain.
func (m *ProbeChain) NodeTip(context.Context) (*big.Int, error) { return m.NodeTipWei, m.Err }

// Head implements gasometer.ProbeChain.
func (m *ProbeChain) Head(context.Context) (uint64, error) { return m.HeadBlock, m.Err }

// Send implements gasometer.ProbeChain.
func (m *ProbeChain) Send(_ context.Context, tx gasometer.ProbeTx) (string, error) {
	inc, err := m.Accept(tx)
	if err != nil {
		return "", err
	}
	hash := ProbeTxHash(tx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, tx)
	m.Pending[tx.From] = max(m.Pending[tx.From], tx.Nonce+1)
	if inc != nil {
		m.include(tx, hash, inc)
	}
	return hash, nil
}

// Include includes a sent transaction.
func (m *ProbeChain) Include(tx gasometer.ProbeTx, inc *gasometer.Inclusion) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.include(tx, ProbeTxHash(tx), inc)
}

func (m *ProbeChain) include(tx gasometer.ProbeTx, hash string, inc *gasometer.Inclusion) {
	m.included[hash] = inc
	m.Latest[tx.From] = max(m.Latest[tx.From], tx.Nonce+1)
}

// Inclusion implements gasometer.ProbeChain.
func (m *ProbeChain) Inclusion(_ context.Context, hash string) (*gasometer.Inclusion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.included[hash], m.Err
}

// Sent returns every accepted transaction, in the order sent.
func (m *ProbeChain) Sent() []gasometer.ProbeTx {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

var _ gasometer.ProbeStore = (*ProbeStore)(nil)

// ProbeStore is an in-memory gasometer.ProbeStore.
type ProbeStore struct {
	mu     sync.Mutex
	probes []gasometer.Probe
	Err    error
}

// Insert implements gasometer.ProbeStore.
func (m *ProbeStore) Insert(_ context.Context, p gasometer.Probe) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.probes = append(m.probes, p)
	return nil
}

// Probes returns the stored probes in insertion order.
func (m *ProbeStore) Probes() []gasometer.Probe {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.probes)
}
