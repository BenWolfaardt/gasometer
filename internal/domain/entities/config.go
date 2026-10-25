package entities

import "time"

// Config is the validated runtime configuration.
type Config struct {
	Log          LogConfig
	HTTP         HTTPConfig
	Database     DatabaseConfig
	Schedule     ScheduleConfig
	Scoring      ScoringConfig
	Predictors   []string // run in order
	Gasometer    GasometerParams
	ReferenceTxs []ReferenceTx
	Networks     []NetworkConfig
}

// LogConfig controls logger output.
type LogConfig struct {
	Level  string
	Format string
}

// HTTPConfig controls the metrics and health server.
type HTTPConfig struct {
	ListenAddress   string
	ShutdownTimeout time.Duration
}

// DatabaseConfig locates Postgres.
type DatabaseConfig struct {
	URL string
}

// ScheduleConfig controls the per-network loop.
type ScheduleConfig struct {
	PollInterval   time.Duration
	BlockRetention time.Duration
}

// ScoringConfig controls horizons and expiry.
type ScoringConfig struct {
	HorizonSeconds []int
	ExpiryFactor   int // horizons to wait before expiring
}

// GasometerParams tune the gasometer predictor.
type GasometerParams struct {
	TipQuantile    float64
	TipWindow      int // blocks
	GrowthQuantile float64
	GrowthWindow   time.Duration
}

// NetworkConfig is everything needed to track one network.
type NetworkConfig struct {
	Network               Network
	RPC                   RPCConfig
	PriceFeed             string // Chainlink native/USD aggregator
	PriceMaxAge           time.Duration
	RequiredTipPercentile float64 // eth_feeHistory percentile taken as the required tip
	HistoryDepth          uint64  // blocks back the RPC serves eth_feeHistory
	FeeHistoryBatch       uint64  // max blocks per eth_feeHistory call
}

// RPCConfig controls one network's RPC client.
type RPCConfig struct {
	URLs           []string // primary first, then fallbacks
	RateLimit      float64  // requests per second
	Burst          int
	Timeout        time.Duration
	MaxRetries     int
	InitialBackoff time.Duration
}
