// Package prometheus exports gasometer's measurements through the Prometheus client.
package prometheus

import (
	"math/big"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/BenWolfaardt/gasometer"
)

const namespace = "gasometer"

// Bucket sets sized for Grafana Cloud's 10,000-series free tier: one bucket per decade.
var (
	usdBuckets      = prometheus.ExponentialBuckets(1e-9, 10, 11)
	ratioBuckets    = prometheus.ExponentialBuckets(0.01, 10, 6)
	errorBuckets    = prometheus.ExponentialBuckets(0.001, 10, 5)
	durationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
)

const (
	componentBaseFee = "base_fee"
	componentTip     = "tip"
	componentL1Fee   = "l1_fee"
)

// Recorder implements gasometer.MetricsRecorder and rpc.Observer on one registry.
type Recorder struct {
	registry      *prometheus.Registry
	resolved      *prometheus.CounterVec
	overpayUSD    *prometheus.HistogramVec
	overpayRatio  *prometheus.HistogramVec
	estimateError *prometheus.HistogramVec
	paidUSD       *prometheus.HistogramVec
	requiredUSD   *prometheus.HistogramVec
	baseFee       *prometheus.GaugeVec
	nextBaseFee   *prometheus.GaugeVec
	nodeTip       *prometheus.GaugeVec
	requiredTip   *prometheus.GaugeVec
	l1Fee         *prometheus.GaugeVec
	nativeUSD     *prometheus.GaugeVec
	mismatches    *prometheus.CounterVec
	rpcRequests   *prometheus.CounterVec
	rpcDuration   *prometheus.HistogramVec
	pollDuration  *prometheus.HistogramVec
	lastPoll      *prometheus.GaugeVec
}

var _ gasometer.MetricsRecorder = (*Recorder)(nil)

// NewRecorder registers every metric, plus the Go and process collectors, on a registry of its own.
func NewRecorder() *Recorder {
	reg := prometheus.NewRegistry()
	f := factory{reg}
	r := &Recorder{
		registry: reg,
		resolved: f.counter("predictions_resolved_total", "Predictions no longer pending, by outcome.",
			"network", "predictor", "horizon", "outcome"),
		overpayUSD: f.histogram("overpay_usd", "Paid minus required for an included bid, in USD.", usdBuckets,
			"network", "predictor", "horizon", "tx"),
		overpayRatio: f.histogram("overpay_ratio", "Overpay divided by required, per gas, for an included bid.", ratioBuckets,
			"network", "predictor", "horizon"),
		estimateError: f.histogram("estimate_error_ratio", "|expected - paid| / paid per gas for an included bid.", errorBuckets,
			"network", "predictor", "horizon"),
		paidUSD: f.histogram("paid_usd", "What an included bid paid, in USD, by component.", usdBuckets,
			"network", "predictor", "horizon", "tx", "component"),
		requiredUSD: f.histogram("required_usd", "The cheapest price that would have landed in the window, in USD, by component.", usdBuckets,
			"network", "predictor", "horizon", "tx", "component"),
		baseFee:      f.gauge("base_fee_wei", "Base fee of the latest block.", "network"),
		nextBaseFee:  f.gauge("next_base_fee_wei", "Base fee of the next block.", "network"),
		nodeTip:      f.gauge("node_tip_wei", "eth_maxPriorityFeePerGas.", "network"),
		requiredTip:  f.gauge("required_tip_wei", "Required tip of the latest block.", "network"),
		l1Fee:        f.gauge("l1_fee_wei", "OP Stack L1 data fee of a reference transaction.", "network", "tx"),
		nativeUSD:    f.gauge("native_usd", "Coin price in USD.", "network"),
		mismatches:   f.counter("model_mismatch_total", "Polls where the fee model disagreed with the node's next base fee.", "network"),
		rpcRequests:  f.counter("rpc_requests_total", "RPC attempts by outcome.", "network", "method", "endpoint", "outcome"),
		rpcDuration:  f.histogram("rpc_duration_seconds", "RPC attempt latency.", durationBuckets, "network", "method"),
		pollDuration: f.histogram("poll_duration_seconds", "Time to collect, predict and score one network.", durationBuckets, "network"),
		lastPoll:     f.gauge("last_poll_timestamp_seconds", "Unix time of the last completed poll.", "network"),
	}
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}

// Gatherer serves the registry to a metrics endpoint.
func (r *Recorder) Gatherer() prometheus.Gatherer {
	return r.registry
}

// InitNetwork exports the network's mismatch counter at zero, so a dashboard reads 0 rather than no data.
func (r *Recorder) InitNetwork(network string) {
	r.mismatches.WithLabelValues(network)
}

// Settled implements gasometer.MetricsRecorder.
func (r *Recorder) Settled(s gasometer.Settlement) {
	horizon := horizonLabel(s.HorizonSeconds)
	r.resolved.WithLabelValues(s.Network, s.Predictor, horizon, s.Outcome).Inc()
	if s.Outcome != gasometer.OutcomeIncluded {
		return
	}
	r.estimateError.WithLabelValues(s.Network, s.Predictor, horizon).Observe(s.EstimateError)
	if s.OverpayRatio != nil {
		r.overpayRatio.WithLabelValues(s.Network, s.Predictor, horizon).Observe(*s.OverpayRatio)
	}
	for _, c := range s.Costs {
		r.overpayUSD.WithLabelValues(s.Network, s.Predictor, horizon, c.Tx).Observe(c.Overpay())
		observeParts(r.paidUSD, c.Paid, s.Network, s.Predictor, horizon, c.Tx)
		observeParts(r.requiredUSD, c.Required, s.Network, s.Predictor, horizon, c.Tx)
	}
}

func observeParts(h *prometheus.HistogramVec, parts gasometer.CostParts, network, predictor, horizon, tx string) {
	h.WithLabelValues(network, predictor, horizon, tx, componentBaseFee).Observe(parts.BaseFee)
	h.WithLabelValues(network, predictor, horizon, tx, componentTip).Observe(parts.Tip)
	h.WithLabelValues(network, predictor, horizon, tx, componentL1Fee).Observe(parts.L1Fee)
}

// Snapshot implements gasometer.MetricsRecorder.
func (r *Recorder) Snapshot(network string, s gasometer.FeeSnapshot, latest gasometer.BlockFee, price *gasometer.PriceQuote) {
	setWei(r.baseFee.WithLabelValues(network), latest.BaseFee)
	setWei(r.requiredTip.WithLabelValues(network), latest.RequiredTip)
	setWei(r.nextBaseFee.WithLabelValues(network), s.NextBaseFee)
	setWei(r.nodeTip.WithLabelValues(network), s.NodeTip)
	for tx, fee := range s.L1Fees {
		setWei(r.l1Fee.WithLabelValues(network, tx), fee)
	}
	if price != nil {
		r.nativeUSD.WithLabelValues(network).Set(price.USD())
	}
}

// ModelMismatch implements gasometer.MetricsRecorder.
func (r *Recorder) ModelMismatch(network string) {
	r.mismatches.WithLabelValues(network).Inc()
}

// PollCompleted implements gasometer.MetricsRecorder.
func (r *Recorder) PollCompleted(network string, took time.Duration, at time.Time) {
	r.pollDuration.WithLabelValues(network).Observe(took.Seconds())
	r.lastPoll.WithLabelValues(network).Set(float64(at.Unix()))
}

// ObserveRPC implements rpc.Observer.
func (r *Recorder) ObserveRPC(network, method, endpoint, outcome string, took time.Duration) {
	r.rpcRequests.WithLabelValues(network, method, endpoint, outcome).Inc()
	r.rpcDuration.WithLabelValues(network, method).Observe(took.Seconds())
}

func horizonLabel(seconds int) string {
	return strconv.Itoa(seconds) + "s"
}

func setWei(g prometheus.Gauge, wei *big.Int) {
	if wei == nil {
		return
	}
	f, _ := new(big.Float).SetInt(wei).Float64()
	g.Set(f)
}

type factory struct {
	reg prometheus.Registerer
}

func (f factory) counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: namespace, Name: name, Help: help}, labels)
	f.reg.MustRegister(c)
	return c
}

func (f factory) gauge(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: name, Help: help}, labels)
	f.reg.MustRegister(g)
	return g
}

func (f factory) histogram(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: namespace, Name: name, Help: help, Buckets: buckets}, labels)
	f.reg.MustRegister(h)
	return h
}
