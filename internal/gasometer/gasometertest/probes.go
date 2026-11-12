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
