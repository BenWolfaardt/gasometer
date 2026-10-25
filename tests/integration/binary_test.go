//go:build integration

package integration_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBinaryStartsAndStops runs the composition root end to end: migrate, serve /readyz and /metrics, poll a
// failing RPC, then shut down cleanly on SIGTERM.
func TestBinaryStartsAndStops(t *testing.T) {
	dbURL := newDatabaseURL(t)

	rpcDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer rpcDown.Close()

	bin := filepath.Join(t.TempDir(), "gasometer")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/gasometer").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	addr := freeAddr(t)
	config := fmt.Sprintf(`
http-listen-address: %q
poll-interval: 1s
log-level: debug
networks:
  - name: base
    chain-id: 8453
    kind: opstack
    coin: ETH
    rpc:
      urls: [%s]
      max-retries: 0
      initial-backoff: 10ms
    price-feed: "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70"
    price-max-age: 1h
    history-depth: 100000
`, addr, rpcDown.URL)

	var stderr bytes.Buffer
	cmd := exec.Command(bin, "--config-file-content", base64.StdEncoding.EncodeToString([]byte(config)))
	cmd.Env = append(os.Environ(), "GASOMETER_DATABASE_URL="+dbURL)
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	base := "http://" + addr
	waitFor(t, func() bool { code, _ := httpGet(base + "/readyz"); return code == http.StatusOK })
	waitFor(t, func() bool {
		_, body := httpGet(base + "/metrics")
		return strings.Contains(body, `gasometer_rpc_requests_total{endpoint="`+strings.TrimPrefix(rpcDown.URL, "http://")+`"`) &&
			strings.Contains(body, `gasometer_model_mismatch_total{network="base"} 0`)
	})

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit: %v\n%s", err, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("did not stop within 15s\n%s", stderr.String())
	}
	for _, want := range []string{"gasometer started", "collect failed", "gasometer stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, stderr.String())
		}
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met within 15s")
}

func httpGet(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
