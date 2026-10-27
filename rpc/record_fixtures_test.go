//go:build record

package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ava-labs/libevm/common/hexutil"
	ethrpc "github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
	"github.com/BenWolfaardt/gasometer/rpc"
)

const fixtureDir = "testdata"

type nopObserver struct{}

func (nopObserver) ObserveRPC(string, string, string, string, time.Duration) {}

// TestRecordFixtures drives every source against the public endpoints through a recording proxy and writes the
// exchanges that tests/unit/rpc replays.
func TestRecordFixtures(t *testing.T) {
	refTxs := []gasometer.ReferenceTx{{Name: "native", Gas: 21_000}, {Name: "erc20", Gas: 65_000, DataSize: 68}}
	for _, n := range []struct {
		name, url, feed string
		chainID         uint64
		opStack         bool
	}{
		{"base", "https://mainnet.base.org", "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", 8453, true},
		{"op", "https://mainnet.optimism.io", "0x13e3Ee699D1909E989722E753853AE30b17e08c5", 10, true},
		{"cchain", "https://api.avax.network/ext/bc/C/rpc", "0x0A77230d17318075983913bC2145DB16C7366156", 43114, false},
	} {
		t.Run(n.name, func(t *testing.T) {
			var (
				mu        sync.Mutex
				exchanges []exchange
			)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, n.url, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", rpc.UserAgent)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
				defer resp.Body.Close()
				out, _ := io.ReadAll(resp.Body)
				if resp.StatusCode == http.StatusOK {
					mu.Lock()
					exchanges = append(exchanges, exchange{Request: body, Response: out})
					mu.Unlock()
				}
				w.WriteHeader(resp.StatusCode)
				_, _ = w.Write(out)
			}))
			defer proxy.Close()

			client, err := rpc.NewClient(t.Context(), n.name, rpc.Config{
				URLs: []string{proxy.URL}, RateLimit: 2, Burst: 1, Timeout: 20 * time.Second,
				MaxRetries: 3, InitialBackoff: time.Second,
			}, nopObserver{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()

			fees := rpc.NewFeeSource(client, rpc.FeeSourceLimits{HistoryDepth: 25_000, Batch: 1024})
			head, err := fees.HeadFee(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fees.FeeHistory(t.Context(), head.Number-3, head.Number, fee.RewardPercentiles(5)); err != nil {
				t.Fatal(err)
			}
			if _, err := fees.NodeTip(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := rpc.NewChainlinkPriceSource(client, n.feed).Price(t.Context()); err != nil {
				t.Fatal(err)
			}
			if n.opStack {
				l1, err := rpc.NewL1FeeSource(client, n.chainID).L1Fees(t.Context(), refTxs)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("L1 fees %v", l1)
			}

			out, err := json.MarshalIndent(exchanges, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixtureDir, n.name+".json")
			if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("head %d, %d exchanges to %s", head.Number, len(exchanges), path)
		})
	}
}

// saeFixture is sae/testdata/blocks.json: consecutive C-Chain blocks and the RPC's next base fee after the last.
type saeFixture struct {
	NextBaseFee *big.Int             `json:"next_base_fee"`
	Blocks      []gasometer.SAEBlock `json:"blocks"`
}

const saeFixtureBlocks = 1_000

// TestRecordSAEFixture records saeFixtureBlocks C-Chain blocks ending at a head whose next base fee eth_baseFee and
// eth_feeHistory agree on, so the RPC served the worst-case bound rather than the following block's header.
func TestRecordSAEFixture(t *testing.T) {
	const url = "https://api.avax.network/ext/bc/C/rpc"
	ctx := t.Context()
	raw, err := ethrpc.DialContext(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	var head uint64
	var next *big.Int
	for attempt := 0; next == nil; attempt++ {
		if attempt == 20 {
			t.Fatal("head kept moving during the next base fee read")
		}
		head, next = stableNextBaseFee(ctx, t, raw)
		time.Sleep(time.Second)
	}

	client, err := rpc.NewClient(ctx, "cchain", rpc.Config{
		URLs: []string{url}, RateLimit: 5, Burst: 1, Timeout: 20 * time.Second, MaxRetries: 3, InitialBackoff: time.Second,
	}, nopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	blocks, err := rpc.NewSAESource(client).Range(ctx, head-saeFixtureBlocks+1, head)
	if err != nil {
		t.Fatal(err)
	}
	atomic := 0
	for _, b := range blocks {
		if b.AtomicGas > 0 {
			atomic++
		}
	}
	if atomic == 0 {
		t.Fatal("no block with atomic txs; record again")
	}
	out, err := json.Marshal(saeFixture{NextBaseFee: next, Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "sae", "testdata", "blocks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("blocks %d to %d, %d with atomic txs, next base fee %s, to %s", blocks[0].Number, head, atomic, next, path)
	recordAtomicWindow(ctx, t, raw, blocks)
}

// recordAtomicWindow writes testdata/sae.json: the raw blocks of the first window in blocks, settled block to head,
// that holds atomic txs after its settled block. Transactions keep only their gas, the one field SAESource reads.
func recordAtomicWindow(ctx context.Context, t *testing.T, c *ethrpc.Client, blocks []gasometer.SAEBlock) {
	t.Helper()
	first := blocks[0].Number
	for _, head := range blocks {
		if head.Settled == nil || head.Settled.Height < first {
			continue
		}
		window := blocks[head.Settled.Height-first : head.Number-first+1]
		hasAtomic := false
		for _, b := range window[1:] {
			hasAtomic = hasAtomic || b.AtomicGas > 0
		}
		if !hasAtomic {
			continue
		}
		recorded := map[string]map[string]any{}
		for _, b := range window {
			var block map[string]any
			if err := c.CallContext(ctx, &block, "eth_getBlockByNumber", hexutil.EncodeUint64(b.Number), true); err != nil {
				t.Fatal(err)
			}
			txs, _ := block["transactions"].([]any)
			for i, tx := range txs {
				txs[i] = map[string]any{"gas": tx.(map[string]any)["gas"]}
			}
			recorded[hexutil.EncodeUint64(b.Number)] = block
		}
		out, err := json.MarshalIndent(recorded, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(fixtureDir, "sae.json")
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("window %d to %d to %s", window[0].Number, head.Number, path)
		return
	}
	t.Fatal("no window holds atomic txs; record again")
}

// stableNextBaseFee returns the head and its next base fee when one batch agrees on both, or a nil fee.
func stableNextBaseFee(ctx context.Context, t *testing.T, c *ethrpc.Client) (uint64, *big.Int) {
	t.Helper()
	var (
		before, after hexutil.Uint64
		baseFee       hexutil.Big
		history       struct {
			OldestBlock hexutil.Uint64 `json:"oldestBlock"`
			BaseFee     []hexutil.Big  `json:"baseFeePerGas"`
		}
	)
	batch := []ethrpc.BatchElem{
		{Method: "eth_blockNumber", Result: &before},
		{Method: "eth_feeHistory", Args: []any{"0x1", "latest", []any{}}, Result: &history},
		{Method: "eth_baseFee", Result: &baseFee},
		{Method: "eth_blockNumber", Result: &after},
	}
	if err := c.BatchCallContext(ctx, batch); err != nil {
		t.Fatal(err)
	}
	for _, e := range batch {
		if e.Error != nil {
			return 0, nil
		}
	}
	if before != after || history.OldestBlock != before || len(history.BaseFee) != 2 ||
		history.BaseFee[1].ToInt().Cmp(baseFee.ToInt()) != 0 {
		return 0, nil
	}
	return uint64(before), baseFee.ToInt()
}
