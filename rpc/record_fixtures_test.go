//go:build record

package rpc_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
