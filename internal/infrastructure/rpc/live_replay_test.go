//go:build live

package rpc_test

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/fee"
)

const (
	replayHeaders = 500
	// Public endpoints reject batches over 10, and rate-limit by request: Base at 25 a second, OP lower.
	replayBatch = 10
	// Public endpoints drop and rate-limit requests now and then; the test is of the fee maths, not their uptime.
	replayAttempts = 5
)

type rawHeader struct {
	Number      hexutil.Uint64 `json:"number"`
	BaseFee     *hexutil.Big   `json:"baseFeePerGas"`
	GasUsed     hexutil.Uint64 `json:"gasUsed"`
	BlobGasUsed hexutil.Uint64 `json:"blobGasUsed"`
	GasLimit    hexutil.Uint64 `json:"gasLimit"`
	ExtraData   hexutil.Bytes  `json:"extraData"`
}

// TestLiveReplayOPStack checks our next base fee against every header in the last replayHeaders blocks.
func TestLiveReplayOPStack(t *testing.T) {
	for _, network := range []struct {
		name, url string
		pace      time.Duration
	}{
		{"base", "https://mainnet.base.org", 600 * time.Millisecond},
		{"op", "https://mainnet.optimism.io", 2 * time.Second},
	} {
		t.Run(network.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			headers := fetchHeaders(ctx, t, network.url, network.pace)
			daBound := 0
			for i := 1; i < len(headers); i++ {
				parent, child := headers[i-1], headers[i]
				params, err := fee.DecodeOPStackExtraData(parent.ExtraData)
				if err != nil {
					t.Fatalf("block %d: %v", parent.Number, err)
				}
				if parent.BlobGasUsed > parent.GasUsed {
					daBound++
				}
				head := gasometer.HeadFee{GasUsed: uint64(parent.GasUsed), BlobGasUsed: uint64(parent.BlobGasUsed)}
				got, err := fee.NextBaseFee(params, parent.BaseFee.ToInt(), head.GasMetered(), uint64(parent.GasLimit))
				if err != nil {
					t.Fatalf("block %d: %v", parent.Number, err)
				}
				if got.Cmp(child.BaseFee.ToInt()) != 0 {
					t.Fatalf("block %d: computed %s, header %s (parent %s, gasUsed %d, blobGasUsed %d, gasLimit %d, params %+v)",
						child.Number, got, child.BaseFee.ToInt(), parent.BaseFee.ToInt(),
						parent.GasUsed, parent.BlobGasUsed, parent.GasLimit, params)
				}
			}
			t.Logf("%d blocks match, %d to %d, %d metered by DA footprint",
				len(headers)-1, headers[1].Number, headers[len(headers)-1].Number, daBound)
		})
	}
}

func fetchHeaders(ctx context.Context, t *testing.T, url string, interval time.Duration) []rawHeader {
	t.Helper()
	// The test owns the transport so it can close the idle connections, which would otherwise outlive it.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client, err := rpc.DialOptions(ctx, url, rpc.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var head hexutil.Uint64
	retry(ctx, t, "eth_blockNumber", func() error { return client.CallContext(ctx, &head, "eth_blockNumber") })
	first := uint64(head) - replayHeaders
	headers := make([]rawHeader, replayHeaders+1)
	pace := time.NewTicker(interval)
	defer pace.Stop()
	for start := uint64(0); start <= replayHeaders; start += replayBatch {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-pace.C:
		}
		batch := make([]rpc.BatchElem, 0, replayBatch)
		for i := start; i < min(start+replayBatch, replayHeaders+1); i++ {
			batch = append(batch, rpc.BatchElem{
				Method: "eth_getBlockByNumber",
				Args:   []any{hexutil.EncodeUint64(first + i), false},
				Result: &headers[i],
			})
		}
		retry(ctx, t, fmt.Sprintf("headers %d+", first+start), func() error {
			if err := client.BatchCallContext(ctx, batch); err != nil {
				return err
			}
			for _, elem := range batch {
				if elem.Error != nil {
					return elem.Error
				}
			}
			return nil
		})
	}
	for i, h := range headers {
		if uint64(h.Number) != first+uint64(i) || h.BaseFee == nil {
			t.Fatalf("header %d: %s", first+uint64(i), describe(h))
		}
	}
	return headers
}

// retry runs call until it succeeds, up to replayAttempts times with doubling waits, logging every failure so a
// flaky endpoint stays visible in the output.
func retry(ctx context.Context, t *testing.T, what string, call func() error) {
	t.Helper()
	wait := time.Second
	for attempt := 1; ; attempt++ {
		err := call()
		if err == nil {
			return
		}
		if attempt == replayAttempts {
			t.Fatalf("%s: %v (attempt %d of %d)", what, err, attempt, replayAttempts)
		}
		t.Logf("%s: %v (attempt %d of %d), retrying in %s", what, err, attempt, replayAttempts, wait)
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", what, ctx.Err())
		case <-time.After(wait):
		}
		wait *= 2
	}
}

func describe(h rawHeader) string {
	var fee *big.Int
	if h.BaseFee != nil {
		fee = h.BaseFee.ToInt()
	}
	return fmt.Sprintf("number %d, base fee %v", h.Number, fee)
}
