package config

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
)

// AppName names the flag set.
const AppName = "gasometer"

// deprecatedKeys maps a retired key to the message shown when it is still used.
var deprecatedKeys = map[string]string{}

// BuildFlagSet defines every flag with its default.
func BuildFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet(AppName, pflag.ContinueOnError)

	fs.String(ConfigFileKey, "", fmt.Sprintf("Config file path. Ignored if %s is set", ConfigContentKey))
	fs.String(ConfigContentKey, "", "Base64-encoded config content")
	fs.String(ConfigContentTypeKey, "yaml", "Format of the base64 config content: json, yaml or toml")

	fs.String(LogLevelKey, "info", "Log level: trace, debug, info, warn or error")
	fs.String(LogFormatKey, "json", "Log format: json or console")

	fs.String(HTTPListenAddressKey, ":9090", "Address for /metrics, /healthz and /readyz")
	fs.Duration(HTTPShutdownTimeoutKey, 10*time.Second, "Time allowed for the HTTP server to drain on shutdown")

	fs.String(DatabaseURLKey, "", "Postgres connection URL")

	fs.Duration(PollIntervalKey, 60*time.Second, "Time between polls of each network")
	fs.Duration(BlockRetentionKey, 48*time.Hour, "How long block rows are kept")

	fs.IntSlice(HorizonsKey, []int{2, 30, 120}, "Prediction horizons in seconds")
	fs.Int(ExpiryFactorKey, 10, "Horizons a prediction waits for the network before it expires")

	fs.StringSlice(PredictorsKey, []string{"node", "wallet", "gasometer"}, "Predictors to run")

	fs.Float64(GasometerTipQuantileKey, 0.9, "Quantile of recent required tips the gasometer predictor bids")
	fs.Int(GasometerTipWindowKey, 30, "Blocks of required tips the gasometer predictor considers")
	fs.Float64(GasometerGrowthQuantileKey, 0.99, "Quantile of base fee growth behind the gasometer predictor's max fee")
	fs.Duration(GasometerGrowthWindowKey, time.Hour, "History of base fee growth the gasometer predictor considers")
	return fs
}

func deprecateFlags(fs *pflag.FlagSet) error {
	for key, message := range deprecatedKeys {
		if fs.Lookup(key) == nil {
			continue
		}
		if err := fs.MarkDeprecated(key, message); err != nil {
			return err
		}
	}
	return nil
}
