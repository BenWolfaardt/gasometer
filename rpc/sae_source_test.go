package rpc_test

import (
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/rpc"
	"github.com/BenWolfaardt/gasometer/sae"
)

// saeServer answers batched eth_getBlockByNumber calls from testdata/sae.json, a recorded C-Chain window holding
// atomic txs. edit may change a block before it is served.
type saeServer struct {
	t       *testing.T
	blocks  map[string]map[string]any
	mu      sync.Mutex
	batches []int
	edit    func(number string, block map[string]any) (errMsg string)
}

func newSAEServer(t *testing.T) (*saeServer, *httptest.Server) {
	t.Helper()
	raw, err := os.ReadFile("testdata/sae.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &saeServer{t: t}
	if err := json.Unmarshal(raw, &s.blocks); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *saeServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var batch []jsonRPCRequest
	if err := json.Unmarshal(body, &batch); err != nil {
		http.Error(w, "SAESource must batch", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.batches = append(s.batches, len(batch))
	edit := s.edit
	s.mu.Unlock()
	out := make([]map[string]any, len(batch))
	for i, in := range batch {
		params, _ := in.Params.([]any)
		number, _ := params[0].(string)
		resp := map[string]any{"jsonrpc": "2.0", "id": in.ID}
		block := cloneJSON(s.blocks[number])
		msg := ""
		if edit != nil {
			msg = edit(number, block)
		}
		switch {
		case in.Method != "eth_getBlockByNumber" || params[1] != true:
			s.t.Errorf("unexpected call %s %v", in.Method, in.Params)
		case msg != "":
			resp["error"] = map[string]any{"code": -32000, "message": msg}
		case block == nil:
			resp["result"] = nil
		default:
			resp["result"] = block
		}
		out[i] = resp
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *saeServer) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.batches)
}

func cloneJSON(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// recordedWindow is the recorded window's head and settled block numbers.
func recordedWindow(t *testing.T, s *saeServer) (head, settled uint64) {
	t.Helper()
	for number, b := range s.blocks {
		n := hexUint(t, number)
		if n > head {
			head = n
			settled = hexUint(t, b["settledHeight"].(string))
		}
	}
	return head, settled
}

func hexUint(t *testing.T, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s[2:], 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSAESourceDecodesRecordedWindow(t *testing.T) {
	server, srv := newSAEServer(t)
	blocks, err := rpc.NewSAESource(newClient(t, &recordingObserver{}, srv.URL)).SAEBlocks(t.Context(), mustHead(t, server))
	if err != nil {
		t.Fatal(err)
	}
	head, settled := recordedWindow(t, server)
	if len(blocks) != int(head-settled+1) || blocks[0].Number != settled || blocks[len(blocks)-1].Number != head {
		t.Fatalf("got %d blocks from %d, want %d to %d", len(blocks), blocks[0].Number, settled, head)
	}
	atomic := false
	for _, b := range blocks {
		raw := server.blocks["0x"+strconv.FormatUint(b.Number, 16)]
		var gas uint64
		for _, tx := range raw["transactions"].([]any) {
			gas += hexUint(t, tx.(map[string]any)["gas"].(string))
		}
		if b.TxGasLimit != gas {
			t.Fatalf("block %d: tx gas %d, recorded %d", b.Number, b.TxGasLimit, gas)
		}
		if want := hexUint(t, raw["timestampMilliseconds"].(string)); b.TimeMilliseconds != want ||
			b.Time != hexUint(t, raw["timestamp"].(string)) {
			t.Fatalf("block %d: time %d / %d ms", b.Number, b.Time, b.TimeMilliseconds)
		}
		if (raw["blockExtraData"] != "0x") != (b.AtomicGas > 0) {
			t.Fatalf("block %d: atomic gas %d for extra data %v", b.Number, b.AtomicGas, raw["blockExtraData"])
		}
		atomic = atomic || b.AtomicGas > 0
	}
	if !atomic {
		t.Fatal("recorded window has no atomic gas")
	}
	got, err := sae.BaseFee(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if want := blocks[len(blocks)-1].BaseFee; got.Cmp(want) != 0 || want.Cmp(big.NewInt(0)) <= 0 {
		t.Fatalf("base fee %s, header %s", got, want)
	}
}

func mustHead(t *testing.T, s *saeServer) uint64 {
	t.Helper()
	head, _ := recordedWindow(t, s)
	return head
}

func TestSAESourceBatchesAtTen(t *testing.T) {
	server, srv := newSAEServer(t)
	head, _ := recordedWindow(t, server)
	template := server.blocks["0x"+strconv.FormatUint(head, 16)]
	server.blocks = map[string]map[string]any{}
	for n := uint64(100); n <= 122; n++ {
		b := cloneJSON(template)
		b["number"] = "0x" + strconv.FormatUint(n, 16)
		server.blocks[b["number"].(string)] = b
	}
	blocks, err := rpc.NewSAESource(newClient(t, &recordingObserver{}, srv.URL)).Range(t.Context(), 100, 122)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 23 || blocks[0].Number != 100 || blocks[22].Number != 122 {
		t.Fatalf("got %d blocks", len(blocks))
	}
	if got := server.batchSizes(); !slices.Equal(got, []int{10, 10, 3}) {
		t.Fatalf("batch sizes %v", got)
	}
}

func TestSAESourceRejectsIncompleteHeader(t *testing.T) {
	for _, field := range []string{"timestampMilliseconds", "targetExponent", "minPriceExponent", "minDelayExcess",
		"baseFeePerGas", "settledExcess"} {
		t.Run(field, func(t *testing.T) {
			server, srv := newSAEServer(t)
			server.edit = func(_ string, block map[string]any) string {
				delete(block, field)
				return ""
			}
			_, err := rpc.NewSAESource(newClient(t, &recordingObserver{}, srv.URL)).SAEBlocks(t.Context(), mustHead(t, server))
			if err == nil {
				t.Fatal("no error")
			}
		})
	}
}

func TestSAESourceHeadWithoutSettledFields(t *testing.T) {
	server, srv := newSAEServer(t)
	server.edit = func(_ string, block map[string]any) string {
		for _, f := range []string{"settledHeight", "settledGasUnix", "settledGasNumerator", "settledExcess"} {
			delete(block, f)
		}
		return ""
	}
	blocks, err := rpc.NewSAESource(newClient(t, &recordingObserver{}, srv.URL)).SAEBlocks(t.Context(), mustHead(t, server))
	if err != nil || len(blocks) != 1 || blocks[0].Settled != nil {
		t.Fatalf("blocks %+v, err %v", blocks, err)
	}
	if _, err := sae.BaseFee(blocks); !errors.Is(err, gasometer.ErrInvalidSAEBlocks) {
		t.Fatalf("sae.BaseFee: %v", err)
	}
}

func TestSAESourceRetriesBlockNotAcceptedYet(t *testing.T) {
	server, srv := newSAEServer(t)
	failed := false
	server.edit = func(_ string, _ map[string]any) string {
		if failed {
			return ""
		}
		failed = true
		return "block not found: not accepted yet"
	}
	observer := &recordingObserver{}
	if _, err := rpc.NewSAESource(newClient(t, observer, srv.URL)).SAEBlocks(t.Context(), mustHead(t, server)); err != nil {
		t.Fatal(err)
	}
	if got := observer.outcomes(); len(got) < 2 || got[0].outcome != rpc.OutcomeRetryable {
		t.Fatalf("outcomes %v", got)
	}
}

func TestSAESourceBlockNotFound(t *testing.T) {
	_, srv := newSAEServer(t)
	if _, err := rpc.NewSAESource(newClient(t, &recordingObserver{}, srv.URL)).SAEBlocks(t.Context(), 1); err == nil {
		t.Fatal("no error for a missing block")
	}
}
