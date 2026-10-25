package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/rpc"
)

func host(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestClientRetriesThenSucceeds(t *testing.T) {
	replay, srv := newReplayServer(t, "base")
	replay.fail = func(n int) int {
		if n <= 2 {
			return http.StatusTooManyRequests
		}
		return 0
	}
	observer := &recordingObserver{}
	if _, err := newFeeSource(newClient(t, observer, srv.URL)).NodeTip(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := observer.outcomes()
	want := []string{rpc.OutcomeRetryable, rpc.OutcomeRetryable, rpc.OutcomeOK}
	if len(got) != len(want) {
		t.Fatalf("observations %+v", got)
	}
	for i, o := range got {
		if o.outcome != want[i] || o.method != "eth_maxPriorityFeePerGas" || o.endpoint != host(t, srv.URL) {
			t.Fatalf("observation %d: %+v", i, o)
		}
	}
}

func TestClientFallsBackAfterRetries(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer down.Close()
	_, up := newReplayServer(t, "base")
	observer := &recordingObserver{}
	if _, err := newFeeSource(newClient(t, observer, down.URL, up.URL)).NodeTip(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := observer.outcomes()
	// MaxRetries 2: three attempts on the primary, then the fallback.
	if len(got) != 4 || got[2].endpoint != host(t, down.URL) || got[3].endpoint != host(t, up.URL) || got[3].outcome != rpc.OutcomeOK {
		t.Fatalf("observations %+v", got)
	}
}

func TestClientFailsWhenEveryEndpointFails(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	observer := &recordingObserver{}
	if _, err := newFeeSource(newClient(t, observer, down.URL, down.URL)).NodeTip(t.Context()); err == nil {
		t.Fatal("want error")
	}
	if n := len(observer.outcomes()); n != 6 {
		t.Fatalf("%d attempts, want 6", n)
	}
}

func TestClientDoesNotRetryClientErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	}))
	defer bad.Close()
	_, up := newReplayServer(t, "base")
	observer := &recordingObserver{}
	if _, err := newFeeSource(newClient(t, observer, bad.URL, up.URL)).NodeTip(t.Context()); err == nil {
		t.Fatal("want error")
	}
	if got := observer.outcomes(); len(got) != 1 || got[0].outcome != rpc.OutcomeError {
		t.Fatalf("observations %+v", got)
	}
}

func TestClientDoesNotRetryJSONRPCErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"requested block is too far behind accepted head"}}`))
	}))
	defer srv.Close()
	_, err := newFeeSource(newClient(t, &recordingObserver{}, srv.URL)).FeeHistory(t.Context(), 1, 1, []float64{5, 50})
	if err == nil || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestClientStopsOnCancel(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	cfg := testRPCConfig(down.URL, down.URL)
	cfg.InitialBackoff = time.Hour
	c, err := rpc.NewClient(t.Context(), "test", cfg, &recordingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = newFeeSource(c).NodeTip(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestClientRateLimits(t *testing.T) {
	_, srv := newReplayServer(t, "base")
	cfg := testRPCConfig(srv.URL)
	cfg.RateLimit, cfg.Burst = 20, 1
	c, err := rpc.NewClient(t.Context(), "test", cfg, &recordingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fees := newFeeSource(c)
	start := time.Now()
	for range 5 {
		if _, err := fees.NodeTip(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// Burst 1 at 20/s: four waits of 50 ms.
	if took := time.Since(start); took < 190*time.Millisecond {
		t.Fatalf("5 calls took %s, limiter not applied", took)
	}
}

func TestNewClientRequiresURLs(t *testing.T) {
	if _, err := rpc.NewClient(t.Context(), "test", testRPCConfig(), &recordingObserver{}); !errors.Is(err, rpc.ErrNoURLs) {
		t.Fatalf("got %v", err)
	}
}
