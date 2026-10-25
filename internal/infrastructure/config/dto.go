package config

import (
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// DTO is the loaded configuration before conversion to the domain.
type DTO struct {
	Log          LogDTO
	HTTP         HTTPDTO
	Database     DatabaseDTO
	Schedule     ScheduleDTO
	Scoring      ScoringDTO
	Predictors   []string
	Gasometer    GasometerDTO
	ReferenceTxs []ReferenceTxDTO
	Networks     []NetworkDTO
}

// LogDTO is the log section.
type LogDTO struct {
	Level  string
	Format string
}

// HTTPDTO is the HTTP section.
type HTTPDTO struct {
	ListenAddress   string
	ShutdownTimeout time.Duration
}

// DatabaseDTO is the database section.
type DatabaseDTO struct {
	URL string
}

// ScheduleDTO is the schedule section.
type ScheduleDTO struct {
	PollInterval   time.Duration
	BlockRetention time.Duration
}

// ScoringDTO is the scoring section.
type ScoringDTO struct {
	Horizons     []int
	ExpiryFactor int
}

// GasometerDTO tunes the gasometer predictor.
type GasometerDTO struct {
	TipQuantile    float64
	TipWindow      int
	GrowthQuantile float64
	GrowthWindow   time.Duration
}

// ReferenceTxDTO is one entry of reference-txs.
type ReferenceTxDTO struct {
	Name     string `mapstructure:"name"`
	Gas      uint64 `mapstructure:"gas"`
	DataSize int    `mapstructure:"data-size"`
}

// NetworkDTO is one entry of networks.
type NetworkDTO struct {
	Name                  string        `mapstructure:"name"`
	ChainID               uint64        `mapstructure:"chain-id"`
	Kind                  string        `mapstructure:"kind"`
	Coin                  string        `mapstructure:"coin"`
	RPC                   RPCDTO        `mapstructure:"rpc"`
	PriceFeed             string        `mapstructure:"price-feed"`
	PriceMaxAge           time.Duration `mapstructure:"price-max-age"`
	RequiredTipPercentile float64       `mapstructure:"required-tip-percentile"`
	HistoryDepth          uint64        `mapstructure:"history-depth"`
	FeeHistoryBatch       uint64        `mapstructure:"fee-history-batch"`
}

// RPCDTO is a network's rpc block.
type RPCDTO struct {
	URLs           []string      `mapstructure:"urls"`
	RateLimit      float64       `mapstructure:"rate-limit"`
	Burst          int           `mapstructure:"burst"`
	Timeout        time.Duration `mapstructure:"timeout"`
	MaxRetries     int           `mapstructure:"max-retries"`
	InitialBackoff time.Duration `mapstructure:"initial-backoff"`
}

// Defaults applied to omitted fields of a network entry.
var (
	defaultReferenceTxs = []ReferenceTxDTO{
		{Name: "native", Gas: 21_000, DataSize: 0},
		{Name: "erc20", Gas: 65_000, DataSize: 68},
	}
	defaultNetwork = NetworkDTO{
		RequiredTipPercentile: 5,
		FeeHistoryBatch:       1024,
		RPC: RPCDTO{
			RateLimit:      5,
			Burst:          10,
			Timeout:        10 * time.Second,
			MaxRetries:     3,
			InitialBackoff: 500 * time.Millisecond,
		},
	}
)

// ToDomain converts a validated DTO to the domain config.
func (c DTO) ToDomain() entities.Config {
	cfg := entities.Config{
		Log:        entities.LogConfig{Level: c.Log.Level, Format: c.Log.Format},
		HTTP:       entities.HTTPConfig{ListenAddress: c.HTTP.ListenAddress, ShutdownTimeout: c.HTTP.ShutdownTimeout},
		Database:   entities.DatabaseConfig{URL: c.Database.URL},
		Schedule:   entities.ScheduleConfig{PollInterval: c.Schedule.PollInterval, BlockRetention: c.Schedule.BlockRetention},
		Scoring:    entities.ScoringConfig{HorizonSeconds: append([]int(nil), c.Scoring.Horizons...), ExpiryFactor: c.Scoring.ExpiryFactor},
		Predictors: append([]string(nil), c.Predictors...),
		Gasometer: entities.GasometerParams{
			TipQuantile:    c.Gasometer.TipQuantile,
			TipWindow:      c.Gasometer.TipWindow,
			GrowthQuantile: c.Gasometer.GrowthQuantile,
			GrowthWindow:   c.Gasometer.GrowthWindow,
		},
	}
	for _, tx := range c.ReferenceTxs {
		cfg.ReferenceTxs = append(cfg.ReferenceTxs, entities.ReferenceTx{Name: tx.Name, Gas: tx.Gas, DataSize: tx.DataSize})
	}
	for _, n := range c.Networks {
		cfg.Networks = append(cfg.Networks, entities.NetworkConfig{
			Network: entities.Network{Name: n.Name, ChainID: n.ChainID, Kind: entities.NetworkKind(n.Kind), Coin: n.Coin},
			RPC: entities.RPCConfig{
				URLs:           append([]string(nil), n.RPC.URLs...),
				RateLimit:      n.RPC.RateLimit,
				Burst:          n.RPC.Burst,
				Timeout:        n.RPC.Timeout,
				MaxRetries:     n.RPC.MaxRetries,
				InitialBackoff: n.RPC.InitialBackoff,
			},
			PriceFeed:             n.PriceFeed,
			PriceMaxAge:           n.PriceMaxAge,
			RequiredTipPercentile: n.RequiredTipPercentile,
			HistoryDepth:          n.HistoryDepth,
			FeeHistoryBatch:       n.FeeHistoryBatch,
		})
	}
	return cfg
}
