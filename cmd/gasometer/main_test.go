package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/infrastructure/rpc"
)

type observedCall struct {
	network, method, endpoint, outcome string
	took                               time.Duration
	err                                error
}

type countingObserver struct {
	mu    sync.Mutex
	calls []observedCall
}

func (o *countingObserver) ObserveRPC(network, method, endpoint, outcome string, took time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, observedCall{network, method, endpoint, outcome, took, err})
}

func newTestObserver() (rpcObserver, *countingObserver, *bytes.Buffer) {
	var logged bytes.Buffer
	next := &countingObserver{}
	return rpcObserver{next: next, log: slog.New(slog.NewJSONHandler(&logged, nil))}, next, &logged
}

func TestRPCObserverLogsARetryableAttemptAtInfo(t *testing.T) {
	o, next, logged := newTestObserver()
	cause := errors.New("cannot query unfinalized data: not accepted yet")
	o.ObserveRPC("cchain", "eth_feeHistory", "api.avax.network", rpc.OutcomeRetryable, 87*time.Millisecond, cause)

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("logged %d lines: %q", len(lines), logged.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"level": "INFO", "msg": "rpc attempt failed; retryable", "network": "cchain", "method": "eth_feeHistory",
		"endpoint": "api.avax.network", "err": cause.Error(), "took_ms": float64(87),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	wantCall := observedCall{"cchain", "eth_feeHistory", "api.avax.network", rpc.OutcomeRetryable, 87 * time.Millisecond, cause}
	if len(next.calls) != 1 || next.calls[0] != wantCall {
		t.Fatalf("next saw %+v, want %+v", next.calls, wantCall)
	}
}

func TestRPCObserverLogsNothingForOKOrError(t *testing.T) {
	o, next, logged := newTestObserver()
	o.ObserveRPC("base", "eth_blockNumber", "mainnet.base.org", rpc.OutcomeOK, time.Millisecond, nil)
	o.ObserveRPC("base", "eth_call", "mainnet.base.org", rpc.OutcomeError, time.Millisecond, errors.New("execution reverted"))
	if logged.Len() != 0 {
		t.Fatalf("logged %q; the caller reports a final error, and ok is the counter's", logged.String())
	}
	if len(next.calls) != 2 || next.calls[0].outcome != rpc.OutcomeOK || next.calls[1].outcome != rpc.OutcomeError {
		t.Fatalf("next saw %+v", next.calls)
	}
}

func TestRPCObserverIsSafeForConcurrentCallers(t *testing.T) {
	o, next, logged := newTestObserver()
	const callers, each = 8, 50
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			for range each {
				o.ObserveRPC("op", "eth_feeHistory", "mainnet.optimism.io", rpc.OutcomeRetryable, time.Millisecond, errors.New("429"))
			}
		})
	}
	wg.Wait()
	if len(next.calls) != callers*each {
		t.Fatalf("next saw %d calls, want %d", len(next.calls), callers*each)
	}
	if n := strings.Count(logged.String(), "\n"); n != callers*each {
		t.Fatalf("logged %d lines, want %d", n, callers*each)
	}
}
