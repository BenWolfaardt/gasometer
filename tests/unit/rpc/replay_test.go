package rpc_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/rpc"
)

type exchange struct {
	Request  json.RawMessage `json:"request"`
	Response json.RawMessage `json:"response"`
}

type jsonRPCRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params any             `json:"params"`
}

// replayServer answers JSON-RPC requests from recorded exchanges, matching method and params exactly, so a test
// also proves the request our sources send is the one that was recorded.
type replayServer struct {
	t         *testing.T
	exchanges []exchange
	mu        sync.Mutex
	userAgent string
	calls     map[string]int
	fail      func(n int) int // status to return on the nth request, 0 to replay
	requests  int
}

func newReplayServer(t *testing.T, network string) (*replayServer, *httptest.Server) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + network + ".json")
	if err != nil {
		t.Fatal(err)
	}
	r := &replayServer{t: t, calls: map[string]int{}}
	if err := json.Unmarshal(raw, &r.exchanges); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return r, srv
}

func (r *replayServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.userAgent = req.Header.Get("User-Agent")
	r.requests++
	n := r.requests
	fail := r.fail
	r.mu.Unlock()
	if fail != nil {
		if status := fail(n); status != 0 {
			http.Error(w, http.StatusText(status), status)
			return
		}
	}
	var in jsonRPCRequest
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, e := range r.exchanges {
		var rec jsonRPCRequest
		_ = json.Unmarshal(e.Request, &rec)
		if rec.Method != in.Method || !reflect.DeepEqual(rec.Params, in.Params) {
			continue
		}
		var resp map[string]any
		_ = json.Unmarshal(e.Response, &resp)
		resp["id"] = in.ID
		r.mu.Lock()
		r.calls[in.Method]++
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	r.t.Errorf("no recorded exchange for %s %v", in.Method, in.Params)
	http.Error(w, "unrecorded request", http.StatusNotFound)
}

func (r *replayServer) callCount(method string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[method]
}

type observation struct {
	method, endpoint, outcome string
}

type recordingObserver struct {
	mu  sync.Mutex
	obs []observation
}

func (o *recordingObserver) ObserveRPC(_, method, endpoint, outcome string, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.obs = append(o.obs, observation{method, endpoint, outcome})
}

func (o *recordingObserver) outcomes() []observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]observation(nil), o.obs...)
}

func testRPCConfig(urls ...string) entities.RPCConfig {
	return entities.RPCConfig{
		URLs: urls, RateLimit: 1_000, Burst: 100, Timeout: 2 * time.Second,
		MaxRetries: 2, InitialBackoff: time.Millisecond,
	}
}

func newClient(t *testing.T, observer rpc.Observer, urls ...string) *rpc.Client {
	t.Helper()
	c, err := rpc.NewClient(t.Context(), "test", testRPCConfig(urls...), observer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
