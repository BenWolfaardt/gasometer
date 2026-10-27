//go:build live

package rpc_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/rpc"
	"github.com/BenWolfaardt/gasometer/sae"
)

const saeReplayBlocks = 2_000

// TestLiveReplaySAE recomputes the base fee of each of the last saeReplayBlocks C-Chain blocks with the SAE gas clock.
func TestLiveReplaySAE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	atomic := 0
	for i := margin; i < len(blocks); i++ {
		b := blocks[i]
		if b.Settled == nil || b.Settled.Height < first {
			t.Fatalf("block %d settles %v, before the fetched range", b.Number, b.Settled)
		}
		window := blocks[b.Settled.Height-first : i+1]
		got, err := sae.BaseFee(window)
		if err != nil {
			t.Fatalf("block %d: %v", b.Number, err)
		}
		if got.Cmp(b.BaseFee) != 0 {
			t.Fatalf("block %d: computed %s, header %s", b.Number, got, b.BaseFee)
		}
		for _, w := range window[1:] {
			if w.AtomicGas > 0 {
				atomic++
				break
			}
		}
	}
	t.Logf("%d blocks match, %d to %d, %d with atomic ops in the window",
		len(blocks)-margin, blocks[margin].Number, head.Number, atomic)
}
