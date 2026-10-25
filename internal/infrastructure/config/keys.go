// Package config loads gasometer's configuration from flags, environment and a config file with viper, on
// avalanchego's config package pattern, and hands the domain a validated entities.Config.
package config

const (
	ConfigFileKey        = "config-file"
	ConfigContentKey     = "config-file-content"
	ConfigContentTypeKey = "config-file-content-type"

	LogLevelKey  = "log-level"
	LogFormatKey = "log-format"

	HTTPListenAddressKey   = "http-listen-address"
	HTTPShutdownTimeoutKey = "http-shutdown-timeout"

	DatabaseURLKey = "database-url"

	PollIntervalKey   = "poll-interval"
	BlockRetentionKey = "block-retention"

	HorizonsKey     = "horizons"
	ExpiryFactorKey = "expiry-factor"

	PredictorsKey = "predictors"

	GasometerTipQuantileKey    = "gasometer-tip-quantile"
	GasometerTipWindowKey      = "gasometer-tip-window"
	GasometerGrowthQuantileKey = "gasometer-growth-quantile"
	GasometerGrowthWindowKey   = "gasometer-growth-window"

	// Lists of objects, set only from the config file.
	ReferenceTxsKey = "reference-txs"
	NetworksKey     = "networks"
)
