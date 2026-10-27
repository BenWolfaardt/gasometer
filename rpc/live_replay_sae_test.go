//go:build live

package rpc_test

import (
	"context"
	"math/big"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ava-labs/avalanchego/vms/saevm/cchain/dynamic"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/rpc"
	"github.com/BenWolfaardt/gasometer/sae"
)

// saeReplayBlocks is how many blocks the replay checks by default; GASOMETER_SAE_REPLAY_BLOCKS overrides it.
const saeReplayBlocks = 10_000

// capHorizons are the horizons the C-Chain ceiling is checked at against the realised base fee.
var capHorizons = []int{1, 2, 30, 120}

// TestLiveReplaySAE recomputes the base fee of each of the last saeReplayBlocks C-Chain blocks with the SAE gas clock,
// checks the ceiling against every realised fee it bounds, and logs the figures docs/sae-gas-pricing.md quotes.
// GASOMETER_SAE_REPLAY_END replays the window ending at that block instead of the head, such as a busy one in the past.
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
	last := envUint(t, "GASOMETER_SAE_REPLAY_END", 0)
	if last == 0 {
		head, err := fees.HeadFee(ctx)
		if err != nil {
			t.Fatal(err)
		}
		last = head.Number
	}
	// The earliest replayed block's settled block lags it by a few blocks; fetch a margin before it.
	const margin = 64
	first := last - envUint(t, "GASOMETER_SAE_REPLAY_BLOCKS", saeReplayBlocks) - margin + 1
	blocks, err := rpc.NewSAESource(client).Range(ctx, first, last)
	if err != nil {
		t.Fatal(err)
	}
	var atomic, noTxGasMismatch, noAtomicGasMismatch, aboveFloor int
	peak := new(big.Int)
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
		if b.BaseFee.Uint64() > uint64(dynamic.PriceExponent(b.MinPriceExponent).Price()) {
			aboveFloor++
		}
		if b.BaseFee.Cmp(peak) > 0 {
			peak = b.BaseFee
		}
	}
	t.Logf("%d blocks match, %d to %d, %d with atomic ops in the window",
		len(blocks)-margin, blocks[margin].Number, last, atomic)
	t.Logf("%d blocks priced above the minimum price, peak base fee %s wei", aboveFloor, peak)
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

// envUint reads a block number or count from the environment, or returns def when the variable is unset.
func envUint(t *testing.T, key string, def uint64) uint64 {
	t.Helper()
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	return n
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
