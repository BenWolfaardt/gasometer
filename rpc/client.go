// Package rpc reads fees, prices and L1 fees from EVM JSON-RPC endpoints through libevm.
package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ava-labs/libevm/rpc"
	"golang.org/x/time/rate"
)

// UserAgent is sent on every request; Base's public endpoint refuses requests without one.
const UserAgent = "gasometer (+https://github.com/BenWolfaardt/gasometer)"

const (
	OutcomeOK        = "ok"
	OutcomeRetryable = "retryable_error"
	OutcomeError     = "error"
)

// Observer receives one call per RPC attempt.
type Observer interface {
	ObserveRPC(network, method, endpoint, outcome string, took time.Duration)
}

// Config controls one network's client.
type Config struct {
	URLs           []string // primary first, then fallbacks
	RateLimit      float64  // requests per second
	Burst          int
	Timeout        time.Duration
	MaxRetries     int
	InitialBackoff time.Duration
}

// ErrNoURLs is returned when a client is configured without endpoints.
var ErrNoURLs = errors.New("no rpc urls")

// Call runs one request against an endpoint's client.
type Call func(ctx context.Context, c *rpc.Client) error

// Client sends requests to a network's endpoints in order, rate limited, retrying 429, 5xx, transport errors and
// lagging backends with exponential backoff before falling back to the next endpoint.
type Client struct {
	network   string
	endpoints []endpoint
	limiter   *rate.Limiter
	cfg       Config
	observer  Observer
}

type endpoint struct {
	host   string
	client *rpc.Client
}

// NewClient prepares a client per URL. HTTP dialling opens no connection until the first request.
func NewClient(ctx context.Context, network string, cfg Config, observer Observer) (*Client, error) {
	c := &Client{
		network:  network,
		limiter:  rate.NewLimiter(rate.Limit(cfg.RateLimit), cfg.Burst),
		cfg:      cfg,
		observer: observer,
	}
	httpClient := &http.Client{Timeout: cfg.Timeout}
	for _, raw := range cfg.URLs {
		u, err := url.Parse(raw)
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("parse rpc url: %w", err)
		}
		rc, err := rpc.DialOptions(ctx, raw, rpc.WithHTTPClient(httpClient), rpc.WithHeader("User-Agent", UserAgent))
		if err != nil {
			c.Close()
			return nil, fmt.Errorf("dial %s: %w", u.Host, err)
		}
		c.endpoints = append(c.endpoints, endpoint{host: u.Host, client: rc})
	}
	if len(c.endpoints) == 0 {
		return nil, fmt.Errorf("%w for %s", ErrNoURLs, network)
	}
	return c, nil
}

// Close releases every endpoint's client.
func (c *Client) Close() {
	for _, e := range c.endpoints {
		e.client.Close()
	}
}

// Do runs call against each endpoint in turn until one succeeds or fails with a non-retryable error.
func (c *Client) Do(ctx context.Context, method string, call Call) error {
	var lastErr error
	for _, e := range c.endpoints {
		err := c.tryEndpoint(ctx, method, e, call)
		if err == nil || !retryable(err) || ctx.Err() != nil {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("%s failed on every endpoint: %w", method, lastErr)
}

func (c *Client) tryEndpoint(ctx context.Context, method string, e endpoint, call Call) error {
	backoff := c.cfg.InitialBackoff
	var err error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if serr := sleepContext(ctx, backoff); serr != nil {
				return serr
			}
			backoff *= 2
		}
		if werr := c.limiter.Wait(ctx); werr != nil {
			return werr
		}
		start := time.Now()
		err = c.attempt(ctx, e, call)
		c.observer.ObserveRPC(c.network, method, e.host, outcome(err), time.Since(start))
		if err == nil || !retryable(err) {
			return err
		}
	}
	return fmt.Errorf("%s on %s: %w", method, e.host, err)
}

func (c *Client) attempt(ctx context.Context, e endpoint, call Call) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	return call(ctx, e.client)
}

func outcome(err error) string {
	switch {
	case err == nil:
		return OutcomeOK
	case retryable(err):
		return OutcomeRetryable
	default:
		return OutcomeError
	}
}

// notAcceptedYet is how a C-Chain node refuses a block it has not accepted. Behind a load balancer, one backend can
// report a head that the backend serving the next request has not reached; it does within a block or two.
const notAcceptedYet = "not accepted yet"

// errBehindHead is a backend answering null for a block another backend reported as the head; like notAcceptedYet,
// it has the block within a block or two.
var errBehindHead = errors.New("block not found on this backend")

// retryable is true for HTTP 429 and 5xx, transport failures, per-attempt timeouts and a backend behind head.
func retryable(err error) bool {
	if errors.Is(err, errBehindHead) {
		return true
	}
	var httpErr rpc.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	// The code is the generic -32000, so only the message identifies it.
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) && strings.Contains(rpcErr.Error(), notAcceptedYet) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
