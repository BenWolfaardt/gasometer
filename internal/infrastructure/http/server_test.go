package http_test

import (
	"context"
	"errors"
	"io"
	"net"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/goleak"

	"github.com/BenWolfaardt/gasometer/internal/infrastructure/http"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func serve(t *testing.T, ready http.ReadyFunc) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "gasometer_test_total"})
	reg.MustRegister(c)
	c.Inc()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- http.NewServer(reg, ready, time.Second).Serve(ctx, ln) }()
	return "http://" + ln.Addr().String(), cancel, done
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &nethttp.Client{Transport: &nethttp.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestServerEndpoints(t *testing.T) {
	notReady := errors.New("database down")
	var readyErr error
	base, cancel, done := serve(t, func(context.Context) error { return readyErr })

	if code, body := get(t, base+"/metrics"); code != 200 || !strings.Contains(body, "gasometer_test_total 1") {
		t.Fatalf("metrics %d %q", code, body)
	}
	if code, _ := get(t, base+"/healthz"); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	if code, _ := get(t, base+"/readyz"); code != 200 {
		t.Fatalf("readyz %d", code)
	}
	readyErr = notReady
	if code, body := get(t, base+"/readyz"); code != 503 || !strings.Contains(body, "database down") {
		t.Fatalf("readyz not ready %d %q", code, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServerReportsListenerFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	err = http.NewServer(prometheus.NewRegistry(), func(context.Context) error { return nil }, time.Second).Serve(context.Background(), ln)
	if err == nil {
		t.Fatal("want error from a closed listener")
	}
}
