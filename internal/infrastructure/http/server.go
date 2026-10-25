// Package http serves /metrics, /healthz and /readyz.
package http

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ReadyFunc reports whether the service can do its work.
type ReadyFunc func(ctx context.Context) error

// Server serves metrics and health until its context ends.
type Server struct {
	srv             *http.Server
	shutdownTimeout time.Duration
}

// NewServer builds the handlers; ready backs /readyz.
func NewServer(gatherer prometheus.Gatherer, ready ReadyFunc, shutdownTimeout time.Duration) *Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return &Server{
		srv:             &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		shutdownTimeout: shutdownTimeout,
	}
}

// Serve blocks on ln until ctx ends, then drains open requests within the shutdown timeout. The serving goroutine
// has exited when Serve returns.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	served := make(chan error, 1)
	go func() { served <- s.srv.Serve(ln) }()

	select {
	case err := <-served:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	shutdownErr := s.srv.Shutdown(shutdownCtx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(fmt.Errorf("http server: %w", err), shutdownErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("http shutdown: %w", shutdownErr)
	}
	return nil
}
