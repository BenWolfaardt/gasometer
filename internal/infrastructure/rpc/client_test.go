package rpc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/BenWolfaardt/gasometer/internal/infrastructure/rpc"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// libevm's package var core.SenderCacher starts NumCPU workers at import; they live for the process.
		goleak.IgnoreTopFunction("github.com/ava-labs/libevm/core.(*txSenderCacher).cache"),
		// libevm's metrics arbiter starts on the first NewMeter and is never stopped.
		goleak.IgnoreTopFunction("github.com/ava-labs/libevm/metrics.(*meterArbiter).tick"),
	)
}

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
		if o.outcome != want[i] || o.failed != (want[i] != rpc.OutcomeOK) || o.method != "eth_maxPriorityFeePerGas" || o.endpoint != host(t, srv.URL) {
			t.Fatalf("observation %d: %+v", i, o)
		}
	}
}

// The server stays up throughout, so only the client's own Close can end the connection it opened.
func TestClientCloseReleasesConnections(t *testing.T) {
	_, srv := newReplayServer(t, "base")
	idle := goleak.IgnoreCurrent()
	c, err := rpc.NewClient(t.Context(), "test", testRPCConfig(srv.URL), &recordingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newFeeSource(c).NodeTip(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := goleak.Find(idle); err != nil {
		t.Fatalf("connection goroutines outlived Close: %v", err)
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
	if got := observer.outcomes(); len(got) != 1 || got[0].outcome != rpc.OutcomeError || !got[0].failed {
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

func TestClientRetriesBackendBehindHead(t *testing.T) {
	lagging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"block not found: not accepted yet: block 98556508"}}`))
	}))
	defer lagging.Close()
	_, up := newReplayServer(t, "base")
	observer := &recordingObserver{}
	if _, err := newFeeSource(newClient(t, observer, lagging.URL, up.URL)).NodeTip(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := observer.outcomes()
	if len(got) != 4 || got[0].outcome != rpc.OutcomeRetryable || got[2].endpoint != host(t, lagging.URL) ||
		got[3].endpoint != host(t, up.URL) || got[3].outcome != rpc.OutcomeOK {
		t.Fatalf("observations %+v", got)
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

func TestClientReportsAnAttemptTheCallerEndedAsAnError(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	observer := &recordingObserver{}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := newFeeSource(newClient(t, observer, slow.URL)).NodeTip(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// The per-attempt timeout is 2 s, so the 50 ms deadline is the caller's, not the endpoint's.
	if got := observer.outcomes(); len(got) != 1 || got[0].outcome != rpc.OutcomeError || !got[0].failed {
		t.Fatalf("observations %+v; want one error, not a retryable attempt", got)
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
