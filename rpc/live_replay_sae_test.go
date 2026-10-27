//go:build live

package rpc_test

import (
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/rpc"
	"github.com/BenWolfaardt/gasometer/sae"
)

const saeReplayBlocks = 10_000

// capHorizons are the horizons the C-Chain ceiling is checked at against the realised base fee.
var capHorizons = []int{1, 2, 30, 120}

// TestLiveReplaySAE recomputes the base fee of each of the last saeReplayBlocks C-Chain blocks with the SAE gas clock,
// checks the ceiling against every realised fee it bounds, and logs the figures docs/sae-gas-pricing.md quotes.
func TestLiveReplaySAE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client, err := rpc.NewClient(ctx, "cchain", rpc.Config{
		URLs: []string{"https://api.avax.network/ext/bc/C/rpc"}, RateLimit: 5, Burst: 1, Timeout: 20 * time.Second,
		MaxRetries: 3, InitialBackoff: time.Second,
	}, &recordingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	fees := rpc.NewFeeSource(client, rpc.FeeSourceLimits{HistoryDepth: 1, Batch: 1})
	head, err := fees.HeadFee(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The earliest replayed block's settled block lags it by a few blocks; fetch a margin before it.
	const margin = 64
	first := head.Number - saeReplayBlocks - margin + 1
	blocks, err := rpc.NewSAESource(client).Range(ctx, first, head.Number)
	if err != nil {
		t.Fatal(err)
	}
	var atomic, noTxGasMismatch, noAtomicGasMismatch int
	lags := map[uint64]int{}
	headroom := map[int]*big.Rat{}
	for i := margin; i < len(blocks); i++ {
		b := blocks[i]
		if b.Settled == nil || b.Settled.Height < first {
			t.Fatalf("block %d settles %v, before the fetched range", b.Number, b.Settled)
		}
		lags[b.Number-b.Settled.Height]++
		window := blocks[b.Settled.Height-first : i+1]
		got, err := sae.BaseFee(window)
		if err != nil {
			t.Fatalf("block %d: %v", b.Number, err)
		}
		if got.Cmp(b.BaseFee) != 0 {
			t.Fatalf("block %d: computed %s, header %s", b.Number, got, b.BaseFee)
		}
		if !matchesWithout(t, window, func(w *gasometer.SAEBlock) { w.TxGasLimit = 0 }) {
			noTxGasMismatch++
		}
		if hasAtomicGas(window[1:]) {
			atomic++
			if !matchesWithout(t, window, func(w *gasometer.SAEBlock) { w.AtomicGas = 0 }) {
				noAtomicGasMismatch++
			}
		}
		checkCeiling(t, blocks, i, window, headroom)
	}
	t.Logf("%d blocks match, %d to %d, %d with atomic ops in the window",
		len(blocks)-margin, blocks[margin].Number, head.Number, atomic)
	t.Logf("mutations: tx gas zeroed mismatches %d of %d; atomic gas zeroed mismatches %d of %d",
		noTxGasMismatch, len(blocks)-margin, noAtomicGasMismatch, atomic)
	t.Logf("settlement lag in blocks, count: %v", lags)
	for _, k := range capHorizons {
		if h := headroom[k]; h != nil {
			f, _ := h.Float64()
			t.Logf("ceiling k=%d: smallest cap / realised %.4f", k, f)
		}
	}
}

func hasAtomicGas(blocks []gasometer.SAEBlock) bool {
	for _, b := range blocks {
		if b.AtomicGas > 0 {
			return true
		}
	}
	return false
}

// matchesWithout is whether the head's base fee still matches its header with edit applied to every block.
func matchesWithout(t *testing.T, window []gasometer.SAEBlock, edit func(*gasometer.SAEBlock)) bool {
	t.Helper()
	changed := slices.Clone(window)
	for j := range changed {
		edit(&changed[j])
	}
	got, err := sae.BaseFee(changed)
	if err != nil {
		t.Fatal(err)
	}
	return got.Cmp(window[len(window)-1].BaseFee) == 0
}

// checkCeiling asserts the ceiling after blocks[i] bounds the base fee of block i+1+k for each horizon in range, and
// keeps the smallest cap / realised ratio per horizon.
func checkCeiling(t *testing.T, blocks []gasometer.SAEBlock, i int, window []gasometer.SAEBlock, headroom map[int]*big.Rat) {
	t.Helper()
	for _, k := range capHorizons {
		if i+1+k >= len(blocks) {
			continue
		}
		bound, err := sae.MaxBaseFeeAfter(window, k)
		if err != nil {
			t.Fatalf("block %d, k=%d: %v", blocks[i].Number, k, err)
		}
		realised := blocks[i+1+k].BaseFee
		if bound.Cmp(realised) < 0 {
			t.Fatalf("block %d, k=%d: cap %s below block %d's base fee %s",
				blocks[i].Number, k, bound, blocks[i+1+k].Number, realised)
		}
		ratio := new(big.Rat).SetFrac(bound, realised)
		if h := headroom[k]; h == nil || ratio.Cmp(h) < 0 {
			headroom[k] = ratio
		}
	}
}
