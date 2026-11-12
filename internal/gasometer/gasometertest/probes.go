package gasometertest

import (
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
)

func probe(rung int) gasometer.Probe {
	return gasometer.Probe{
		Network: "cchain", Rung: rung, Account: "0xabc", Nonce: uint64(rung), Tip: wei(int64(rung)), MaxFee: wei(100),
		TxHash: "0xdef", SentAt: t0, HeadAtSend: 10, Outcome: gasometer.ProbeTimeout,
	}
}

// ProbeStore runs the gasometer.ProbeStore contract, with a new store from newStore for each subtest. The port has no
// reader, so newStore also returns how many probes the store holds.
func ProbeStore(t *testing.T, newStore func(t *testing.T) (gasometer.ProbeStore, func() int)) {
	t.Run("insert appends one record per call", func(t *testing.T) {
		store, count := newStore(t)
		for rung := range 3 {
			if err := store.Insert(t.Context(), probe(rung)); err != nil {
				t.Fatal(err)
			}
			if got := count(); got != rung+1 {
				t.Fatalf("after %d inserts the store holds %d", rung+1, got)
			}
		}
	})

	t.Run("identical probes are kept twice", func(t *testing.T) {
		store, count := newStore(t)
		for range 2 {
			if err := store.Insert(t.Context(), probe(1)); err != nil {
				t.Fatal(err)
			}
		}
		if got := count(); got != 2 {
			t.Fatalf("store holds %d, want 2", got)
		}
	})

	t.Run("concurrent inserters", func(t *testing.T) {
		store, count := newStore(t)
		Concurrently(t, callers, func(i int) {
			if err := store.Insert(t.Context(), probe(i)); err != nil {
				t.Errorf("caller %d: %v", i, err)
			}
		})
		if got := count(); got != callers {
			t.Fatalf("store holds %d, want %d", got, callers)
		}
	})
}

// unknownTx is a transaction hash no chain has seen.
const unknownTx = "0x00000000000000000000000000000000000000000000000000000000000000ff"

// ProbeChain runs the gasometer.ProbeChain contract lines that hold for every chain, with a new chain from newChain
// for each subtest. The chain must accept a transfer between any of its first callers accounts at nonce 0.
func ProbeChain(t *testing.T, newChain func(t *testing.T) gasometer.ProbeChain) {
	t.Run("address is pure", func(t *testing.T) {
		chain := newChain(t)
		for account := range 3 {
			if a, b := chain.Address(account), chain.Address(account); a != b || a == "" {
				t.Fatalf("Address(%d) gave %q then %q", account, a, b)
			}
		}
		if chain.Address(0) == chain.Address(1) {
			t.Fatal("accounts 0 and 1 share an address")
		}
	})

	t.Run("inclusion is nil for an unknown transaction", func(t *testing.T) {
		inc, err := newChain(t).Inclusion(t.Context(), unknownTx)
		if inc != nil || err != nil {
			t.Fatalf("inclusion %+v, err %v", inc, err)
		}
	})

	t.Run("accounts used concurrently", func(t *testing.T) {
		chain := newChain(t)
		Concurrently(t, callers, func(i int) {
			hash, err := chain.Send(t.Context(), gasometer.ProbeTx{
				From: i, To: (i + 1) % callers, Value: wei(1), Nonce: 0, Tip: wei(1), MaxFee: wei(2_000),
			})
			if err != nil || hash == "" {
				t.Errorf("account %d send: %q, %v", i, hash, err)
				return
			}
			if _, err := chain.Inclusion(t.Context(), hash); err != nil {
				t.Errorf("account %d inclusion: %v", i, err)
			}
			if _, _, err := chain.Nonces(t.Context(), i); err != nil {
				t.Errorf("account %d nonces: %v", i, err)
			}
			if _, err := chain.Balance(t.Context(), i); err != nil {
				t.Errorf("account %d balance: %v", i, err)
			}
			if _, err := chain.BaseFee(t.Context()); err != nil {
				t.Errorf("account %d base fee: %v", i, err)
			}
			if _, err := chain.NodeTip(t.Context()); err != nil {
				t.Errorf("account %d node tip: %v", i, err)
			}
			if _, err := chain.Head(t.Context()); err != nil {
				t.Errorf("account %d head: %v", i, err)
			}
		})
	})
}
