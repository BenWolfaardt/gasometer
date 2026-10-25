package rpc_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/ava-labs/libevm/common/hexutil"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/fee"
	"github.com/BenWolfaardt/gasometer/rpc"
)

type feeHistoryCall struct{ count, newest uint64 }

// syntheticNode answers eth_feeHistory with base fee = block number and the next block's base fee after the last.
// short trims that many blocks from every response; shift moves the whole response that many blocks later.
func syntheticNode(t *testing.T, short, shift int) (*httptest.Server, func() []feeHistoryCall) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []feeHistoryCall
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var countHex, newestHex string
		_ = json.Unmarshal(req.Params[0], &countHex)
		_ = json.Unmarshal(req.Params[1], &newestHex)
		count, _ := strconv.ParseUint(countHex[2:], 16, 64)
		newest, _ := strconv.ParseUint(newestHex[2:], 16, 64)
		mu.Lock()
		calls = append(calls, feeHistoryCall{count, newest})
		mu.Unlock()

		count -= uint64(short)
		newest += uint64(shift)
		oldest := newest - count + 1
		result := map[string]any{"oldestBlock": hexutil.EncodeUint64(oldest)}
		var baseFees []string
		var ratios []float64
		var rewards [][]string
		for n := oldest; n <= newest+1; n++ {
			baseFees = append(baseFees, hexutil.EncodeUint64(n))
			if n <= newest {
				ratios = append(ratios, 0.5)
				rewards = append(rewards, []string{"0x1", "0x2"})
			}
		}
		result["baseFeePerGas"], result["gasUsedRatio"], result["reward"] = baseFees, ratios, rewards
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []feeHistoryCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]feeHistoryCall(nil), calls...)
	}
}

func TestFeeSourceBatches(t *testing.T) {
	srv, calls := syntheticNode(t, 0, 0)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 100_000, Batch: 4})
	h, err := fees.FeeHistory(t.Context(), 4_991, 5_000, fee.RewardPercentiles(5))
	if err != nil {
		t.Fatal(err)
	}
	want := []feeHistoryCall{{4, 4_994}, {4, 4_998}, {2, 5_000}}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if h.OldestBlock != 4_991 || h.Len() != 10 || len(h.BaseFees) != 11 || len(h.Rewards) != 10 {
		t.Fatalf("history oldest %d, %d blocks, %d base fees", h.OldestBlock, h.Len(), len(h.BaseFees))
	}
	for i, fee := range h.BaseFees {
		if fee.Uint64() != 4_991+uint64(i) {
			t.Fatalf("base fee %d is %s, want %d", i, fee, 4_991+i)
		}
	}
}

func TestFeeSourceClampsToHistoryDepth(t *testing.T) {
	srv, calls := syntheticNode(t, 0, 0)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 50, Batch: 1024})
	h, err := fees.FeeHistory(t.Context(), 100, 5_000, fee.RewardPercentiles(5))
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || got[0] != (feeHistoryCall{50, 5_000}) {
		t.Fatalf("calls %+v", got)
	}
	if h.OldestBlock != 4_951 || h.Len() != 50 {
		t.Fatalf("history from %d, %d blocks", h.OldestBlock, h.Len())
	}
}

func TestFeeSourceSingleBlock(t *testing.T) {
	srv, _ := syntheticNode(t, 0, 0)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 100, Batch: 1024})
	h, err := fees.FeeHistory(t.Context(), 7, 7, fee.RewardPercentiles(5))
	if err != nil || h.OldestBlock != 7 || h.Len() != 1 || h.NextBaseFee().Uint64() != 8 {
		t.Fatalf("history %+v, err %v", h, err)
	}
}

func TestFeeSourceRejectsShortResponses(t *testing.T) {
	srv, _ := syntheticNode(t, 1, 0)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 100, Batch: 1024})
	if _, err := fees.FeeHistory(t.Context(), 1, 10, fee.RewardPercentiles(5)); !errors.Is(err, gasometer.ErrMalformedFeeHistory) {
		t.Fatalf("got %v", err)
	}
}

func TestFeeSourceRejectsInvertedRange(t *testing.T) {
	srv, calls := syntheticNode(t, 0, 0)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 100, Batch: 1024})
	if _, err := fees.FeeHistory(t.Context(), 11, 10, fee.RewardPercentiles(5)); !errors.Is(err, gasometer.ErrMalformedFeeHistory) || len(calls()) != 0 {
		t.Fatalf("got %v after %d calls", err, len(calls()))
	}
}

func TestFeeSourceRejectsShiftedResponses(t *testing.T) {
	srv, _ := syntheticNode(t, 0, 1)
	fees := rpc.NewFeeSource(newClient(t, &recordingObserver{}, srv.URL), rpc.FeeSourceLimits{HistoryDepth: 100, Batch: 1024})
	if _, err := fees.FeeHistory(t.Context(), 1, 10, fee.RewardPercentiles(5)); !errors.Is(err, gasometer.ErrMalformedFeeHistory) {
		t.Fatalf("got %v", err)
	}
}
