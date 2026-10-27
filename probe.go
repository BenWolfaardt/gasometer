package gasometer

import (
	"context"
	"math/big"
	"time"
)

// ProbeOutcome is what became of a probe transaction.
type ProbeOutcome string

const (
	ProbeIncluded ProbeOutcome = "included"
	// ProbeTimeout is a transaction neither included nor replaced in time; it may still be pending.
	ProbeTimeout ProbeOutcome = "timeout"
	// ProbeRejected is a transaction the node refused at submission.
	ProbeRejected ProbeOutcome = "rejected"
	// ProbeReplaced is a transaction that timed out and was replaced at the same nonce.
	ProbeReplaced ProbeOutcome = "replaced"
)

// Probe is one transaction sent at a known tip, and whether and when it was included.
type Probe struct {
	Network          string
	Rung             int
	Account          string
	Nonce            uint64
	Tip              *big.Int
	MaxFee           *big.Int
	TxHash           string
	SentAt           time.Time
	HeadAtSend       uint64
	IncludedBlock    *uint64
	IncludedAtMillis *uint64 // the including block's timestamp, in Unix milliseconds
	Outcome          ProbeOutcome
	Error            string // the node's message when rejected
	ReplacedBy       string // the replacement's hash when replaced
}

// ProbeStore persists probes.
type ProbeStore interface {
	Insert(ctx context.Context, p Probe) error
}

// ProbeTx is a transfer between two of the probe's accounts.
type ProbeTx struct {
	From, To int
	Value    *big.Int
	Nonce    uint64
	Tip      *big.Int
	MaxFee   *big.Int
}

// Inclusion is the block a transaction landed in.
type Inclusion struct {
	Block            uint64
	TimeMilliseconds uint64
}

// ProbeChain sends transfers from accounts derived from one seed, numbered from 0, and reads their state.
type ProbeChain interface {
	Address(account int) string
	// Nonces are the account's nonce after the latest block and including the node's pending transactions.
	Nonces(ctx context.Context, account int) (latest, pending uint64, err error)
	Balance(ctx context.Context, account int) (*big.Int, error)
	BaseFee(ctx context.Context) (*big.Int, error)
	NodeTip(ctx context.Context) (*big.Int, error)
	Head(ctx context.Context) (uint64, error)
	// Send returns the transaction's hash; a node refusing it wraps ErrTxRejected.
	Send(ctx context.Context, tx ProbeTx) (string, error)
	// Inclusion is nil until the transaction has a receipt.
	Inclusion(ctx context.Context, hash string) (*Inclusion, error)
}
