package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ava-labs/libevm/common"
	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/BenWolfaardt/gasometer"
)

// Config follows avalanchego's config package pattern: keys, flags with defaults, viper layering, the config file's
// shape, then one validated get<Section>Config per section.

const (
	configFileKey        = "config-file"
	configContentKey     = "config-file-content"
	configContentTypeKey = "config-file-content-type"

	logLevelKey  = "log-level"
	logFormatKey = "log-format"

	httpListenAddressKey   = "http-listen-address"
	httpShutdownTimeoutKey = "http-shutdown-timeout"

	databaseURLKey = "database-url"

	pollIntervalKey   = "poll-interval"
	blockRetentionKey = "block-retention"

	horizonsKey     = "horizons"
	expiryFactorKey = "expiry-factor"

	predictorsKey = "predictors"

	gasometerTipQuantileKey    = "gasometer-tip-quantile"
	gasometerTipWindowKey      = "gasometer-tip-window"
	gasometerGrowthQuantileKey = "gasometer-growth-quantile"
	gasometerGrowthWindowKey   = "gasometer-growth-window"

	// Lists of objects, set only from the config file.
	referenceTxsKey = "reference-txs"
	networksKey     = "networks"
)

// appName names the flag set.
const appName = "gasometer"

// deprecatedKeys maps a retired key to the message shown when it is still used.
var deprecatedKeys = map[string]string{}

// buildFlagSet defines every flag with its default.
func buildFlagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet(appName, pflag.ContinueOnError)

	fs.String(configFileKey, "", fmt.Sprintf("Config file path. Ignored if %s is set", configContentKey))
	fs.String(configContentKey, "", "Base64-encoded config content")
	fs.String(configContentTypeKey, "yaml", "Format of the base64 config content: json, yaml or toml")

	fs.String(logLevelKey, "info", "Log level: debug, info, warn or error")
	fs.String(logFormatKey, "json", "Log format: json or console")

	fs.String(httpListenAddressKey, ":9090", "Address for /metrics, /healthz and /readyz")
	fs.Duration(httpShutdownTimeoutKey, 10*time.Second, "Time allowed for the HTTP server to drain on shutdown")

	fs.String(databaseURLKey, "", "Postgres connection URL")

	fs.Duration(pollIntervalKey, 60*time.Second, "Time between polls of each network")
	fs.Duration(blockRetentionKey, 48*time.Hour, "How long block rows are kept")

	fs.IntSlice(horizonsKey, []int{2, 30, 120}, "Prediction horizons in seconds")
	fs.Int(expiryFactorKey, 10, "Horizons a prediction waits for the network before it expires")

	fs.StringSlice(predictorsKey, []string{"node", "wallet", "gasometer"}, "Predictors to run")

	fs.Float64(gasometerTipQuantileKey, 0.9, "Quantile of recent required tips the gasometer predictor bids")
	fs.Int(gasometerTipWindowKey, 30, "Blocks of required tips the gasometer predictor considers")
	fs.Float64(gasometerGrowthQuantileKey, 0.99, "Quantile of base fee growth behind the gasometer predictor's max fee")
	fs.Duration(gasometerGrowthWindowKey, time.Hour, "History of base fee growth the gasometer predictor considers")
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

// envPrefix prefixes every environment variable, e.g. GASOMETER_DATABASE_URL.
const envPrefix = "gasometer"

var dashesToUnderscores = strings.NewReplacer("-", "_")

// envVarName is the environment variable for key.
func envVarName(key string) string {
	return strings.ToUpper(envPrefix + "_" + dashesToUnderscores.Replace(key))
}

// buildViper parses args into fs and layers flags over environment over the config file or content. Deprecation
// warnings go to warn.
func buildViper(fs *pflag.FlagSet, args []string, warn io.Writer) (*viper.Viper, error) {
	if err := deprecateFlags(fs); err != nil {
		return nil, err
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	v := viper.New()
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(dashesToUnderscores)
	v.SetEnvPrefix(envPrefix)
	if err := v.BindPFlags(fs); err != nil {
		return nil, err
	}

	switch {
	case v.IsSet(configContentKey) && v.GetString(configContentKey) != "":
		content, err := base64.StdEncoding.DecodeString(v.GetString(configContentKey))
		if err != nil {
			return nil, fmt.Errorf("decode base64 config content: %w", err)
		}
		v.SetConfigType(v.GetString(configContentTypeKey))
		if err := v.ReadConfig(bytes.NewBuffer(content)); err != nil {
			return nil, fmt.Errorf("read config content: %w", err)
		}
	case v.GetString(configFileKey) != "":
		v.SetConfigFile(os.ExpandEnv(v.GetString(configFileKey)))
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config file: %w", err)
		}
	}

	warnDeprecated(v, deprecatedKeys, warn)
	return v, nil
}

// warnDeprecated writes a warning for each deprecated key the config file still sets.
func warnDeprecated(v *viper.Viper, deprecated map[string]string, warn io.Writer) {
	for key, message := range deprecated {
		if v.InConfig(key) {
			_, _ = fmt.Fprintf(warn, "config %s is deprecated: %s\n", key, message)
		}
	}
}

// fileConfig is the validated configuration as loaded; main maps it onto each component.
type fileConfig struct {
	Log          logConfig
	HTTP         httpConfig
	Database     databaseConfig
	Schedule     scheduleConfig
	Scoring      scoringConfig
	Predictors   []string
	Gasometer    gasometerConfig
	ReferenceTxs []referenceTxConfig
	Networks     []networkConfig
}

// logConfig is the log section.
type logConfig struct {
	Level  string
	Format string
}

// httpConfig is the HTTP section.
type httpConfig struct {
	ListenAddress   string
	ShutdownTimeout time.Duration
}

// databaseConfig is the database section.
type databaseConfig struct {
	URL string
}

// scheduleConfig is the schedule section.
type scheduleConfig struct {
	PollInterval   time.Duration
	BlockRetention time.Duration
}

// scoringConfig is the scoring section.
type scoringConfig struct {
	Horizons     []int
	ExpiryFactor int
}

// gasometerConfig tunes the gasometer predictor.
type gasometerConfig struct {
	TipQuantile    float64
	TipWindow      int
	GrowthQuantile float64
	GrowthWindow   time.Duration
}

// referenceTxConfig is one entry of reference-txs.
type referenceTxConfig struct {
	Name     string `mapstructure:"name"`
	Gas      uint64 `mapstructure:"gas"`
	DataSize int    `mapstructure:"data-size"`
}

// networkConfig is one entry of networks.
type networkConfig struct {
	Name                  string        `mapstructure:"name"`
	ChainID               uint64        `mapstructure:"chain-id"`
	Kind                  string        `mapstructure:"kind"`
	Coin                  string        `mapstructure:"coin"`
	RPC                   rpcConfig     `mapstructure:"rpc"`
	PriceFeed             string        `mapstructure:"price-feed"`
	PriceMaxAge           time.Duration `mapstructure:"price-max-age"`
	RequiredTipPercentile float64       `mapstructure:"required-tip-percentile"`
	HistoryDepth          uint64        `mapstructure:"history-depth"`
	FeeHistoryBatch       uint64        `mapstructure:"fee-history-batch"`
	InitialBackfill       uint64        `mapstructure:"initial-backfill"`
}

// rpcConfig is a network's rpc block.
type rpcConfig struct {
	URLs           []string      `mapstructure:"urls"`
	RateLimit      float64       `mapstructure:"rate-limit"`
	Burst          int           `mapstructure:"burst"`
	Timeout        time.Duration `mapstructure:"timeout"`
	MaxRetries     int           `mapstructure:"max-retries"`
	InitialBackoff time.Duration `mapstructure:"initial-backoff"`
}

// Defaults applied to omitted fields of a network entry.
var (
	defaultReferenceTxs = []referenceTxConfig{
		{Name: "native", Gas: 21_000, DataSize: 0},
		{Name: "erc20", Gas: 65_000, DataSize: 68},
	}
	defaultNetwork = networkConfig{
		RequiredTipPercentile: 5,
		FeeHistoryBatch:       1024,
		InitialBackfill:       1024,
		RPC: rpcConfig{
			RateLimit:      5,
			Burst:          10,
			Timeout:        10 * time.Second,
			MaxRetries:     3,
			InitialBackoff: 500 * time.Millisecond,
		},
	}
)

// Predictor names the config accepts.
var knownPredictors = []string{"node", "wallet", "gasometer"}

var (
	errInvalidLogLevel        = invalid("log level must be trace, debug, info, warn or error")
	errInvalidLogFormat       = invalid("log format must be json or console")
	errMissingListenAddress   = invalid("http listen address is required")
	errInvalidShutdownTimeout = invalid("http shutdown timeout must be positive")
	errMissingDatabaseURL     = invalid("database url is required")
	errInvalidPollInterval    = invalid("poll interval must be positive")
	errInvalidBlockRetention  = invalid("block retention must outlast the longest horizon times the expiry factor")
	errNoHorizons             = invalid("at least one horizon is required")
	errInvalidHorizon         = invalid("horizons must be positive and unique")
	errInvalidExpiryFactor    = invalid("expiry factor must be at least 1")
	errNoPredictors           = invalid("at least one predictor is required")
	errUnknownPredictor       = invalid("predictor must be node, wallet or gasometer, once each")
	errInvalidQuantile        = invalid("quantiles must be in (0, 1]")
	errInvalidTipWindow       = invalid("tip window must be at least 1 block")
	errInvalidGrowthWindow    = invalid("growth window must be positive")
	errNoReferenceTxs         = invalid("at least one reference transaction is required")
	errInvalidReferenceTx     = invalid("reference transactions need a unique name, positive gas and non-negative data size")
	errNoNetworks             = invalid("at least one network is required")
	errInvalidNetworkName     = invalid("networks need a unique, non-empty name")
	errInvalidNetworkKind     = invalid("network kind must be opstack or avalanche-sae")
	errInvalidNetworkIdentity = invalid("networks need a chain id and a coin")
	errInvalidRPCURL          = invalid("rpc urls must be one or more http or https urls")
	errInvalidRPCLimits       = invalid("rpc rate limit, burst, timeout and backoff must be positive, retries non-negative")
	errInvalidPriceFeed       = invalid("price feed must be a hex address")
	errInvalidPriceMaxAge     = invalid("price max age must be positive")
	errInvalidTipPercentile   = invalid("required tip percentile must be in [0, 100]")
	errInvalidHistoryDepth    = invalid("history depth and fee history batch must be at least 1")
	errDecodeList             = invalid("decode config list")
)

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", gasometer.ErrInvalidConfig, msg)
}

// getConfig reads and validates every section of v.
func getConfig(v *viper.Viper) (fileConfig, error) {
	var (
		cfg fileConfig
		err error
	)
	if cfg.Log, err = getLogConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.HTTP, err = getHTTPConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.Database, err = getDatabaseConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.Scoring, err = getScoringConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.Schedule, err = getScheduleConfig(v, cfg.Scoring); err != nil {
		return fileConfig{}, err
	}
	if cfg.Predictors, err = getPredictorsConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.Gasometer, err = getGasometerConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.ReferenceTxs, err = getReferenceTxsConfig(v); err != nil {
		return fileConfig{}, err
	}
	if cfg.Networks, err = getNetworksConfig(v); err != nil {
		return fileConfig{}, err
	}
	return cfg, nil
}

func getLogConfig(v *viper.Viper) (logConfig, error) {
	c := logConfig{Level: v.GetString(logLevelKey), Format: v.GetString(logFormatKey)}
	if _, err := parseLevel(c.Level); err != nil {
		return logConfig{}, fmt.Errorf("%w: %q", errInvalidLogLevel, c.Level)
	}
	if c.Format != "json" && c.Format != "console" {
		return logConfig{}, fmt.Errorf("%w: %q", errInvalidLogFormat, c.Format)
	}
	return c, nil
}

func getHTTPConfig(v *viper.Viper) (httpConfig, error) {
	c := httpConfig{ListenAddress: v.GetString(httpListenAddressKey), ShutdownTimeout: v.GetDuration(httpShutdownTimeoutKey)}
	if c.ListenAddress == "" {
		return httpConfig{}, errMissingListenAddress
	}
	if c.ShutdownTimeout <= 0 {
		return httpConfig{}, fmt.Errorf("%w: %s", errInvalidShutdownTimeout, c.ShutdownTimeout)
	}
	return c, nil
}

func getDatabaseConfig(v *viper.Viper) (databaseConfig, error) {
	c := databaseConfig{URL: v.GetString(databaseURLKey)}
	if c.URL == "" {
		return databaseConfig{}, errMissingDatabaseURL
	}
	return c, nil
}

func getScoringConfig(v *viper.Viper) (scoringConfig, error) {
	c := scoringConfig{Horizons: v.GetIntSlice(horizonsKey), ExpiryFactor: v.GetInt(expiryFactorKey)}
	if len(c.Horizons) == 0 {
		return scoringConfig{}, errNoHorizons
	}
	seen := map[int]bool{}
	for _, h := range c.Horizons {
		if h <= 0 || seen[h] {
			return scoringConfig{}, fmt.Errorf("%w: %v", errInvalidHorizon, c.Horizons)
		}
		seen[h] = true
	}
	if c.ExpiryFactor < 1 {
		return scoringConfig{}, fmt.Errorf("%w: %d", errInvalidExpiryFactor, c.ExpiryFactor)
	}
	return c, nil
}

func getScheduleConfig(v *viper.Viper, scoring scoringConfig) (scheduleConfig, error) {
	c := scheduleConfig{PollInterval: v.GetDuration(pollIntervalKey), BlockRetention: v.GetDuration(blockRetentionKey)}
	if c.PollInterval <= 0 {
		return scheduleConfig{}, fmt.Errorf("%w: %s", errInvalidPollInterval, c.PollInterval)
	}
	longest := time.Duration(slices.Max(scoring.Horizons)*scoring.ExpiryFactor) * time.Second
	if c.BlockRetention <= longest {
		return scheduleConfig{}, fmt.Errorf("%w: %s against %s", errInvalidBlockRetention, c.BlockRetention, longest)
	}
	return c, nil
}

func getPredictorsConfig(v *viper.Viper) ([]string, error) {
	names := v.GetStringSlice(predictorsKey)
	if len(names) == 0 {
		return nil, errNoPredictors
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !slices.Contains(knownPredictors, n) || seen[n] {
			return nil, fmt.Errorf("%w: %v", errUnknownPredictor, names)
		}
		seen[n] = true
	}
	return names, nil
}

func getGasometerConfig(v *viper.Viper) (gasometerConfig, error) {
	c := gasometerConfig{
		TipQuantile:    v.GetFloat64(gasometerTipQuantileKey),
		TipWindow:      v.GetInt(gasometerTipWindowKey),
		GrowthQuantile: v.GetFloat64(gasometerGrowthQuantileKey),
		GrowthWindow:   v.GetDuration(gasometerGrowthWindowKey),
	}
	for _, q := range []float64{c.TipQuantile, c.GrowthQuantile} {
		if q <= 0 || q > 1 {
			return gasometerConfig{}, fmt.Errorf("%w: %v", errInvalidQuantile, q)
		}
	}
	if c.TipWindow < 1 {
		return gasometerConfig{}, fmt.Errorf("%w: %d", errInvalidTipWindow, c.TipWindow)
	}
	if c.GrowthWindow <= 0 {
		return gasometerConfig{}, fmt.Errorf("%w: %s", errInvalidGrowthWindow, c.GrowthWindow)
	}
	return c, nil
}

func getReferenceTxsConfig(v *viper.Viper) ([]referenceTxConfig, error) {
	if !v.IsSet(referenceTxsKey) {
		return slices.Clone(defaultReferenceTxs), nil
	}
	var txs []referenceTxConfig
	if err := decodeList(v, referenceTxsKey, func() referenceTxConfig { return referenceTxConfig{} }, &txs); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, errNoReferenceTxs
	}
	seen := map[string]bool{}
	for _, tx := range txs {
		if tx.Name == "" || seen[tx.Name] || tx.Gas == 0 || tx.DataSize < 0 {
			return nil, fmt.Errorf("%w: %+v", errInvalidReferenceTx, tx)
		}
		seen[tx.Name] = true
	}
	return txs, nil
}

func getNetworksConfig(v *viper.Viper) ([]networkConfig, error) {
	var networks []networkConfig
	if err := decodeList(v, networksKey, func() networkConfig { return defaultNetwork }, &networks); err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, errNoNetworks
	}
	seen := map[string]bool{}
	for _, n := range networks {
		if n.Name == "" || seen[n.Name] {
			return nil, fmt.Errorf("%w: %q", errInvalidNetworkName, n.Name)
		}
		seen[n.Name] = true
		if err := validateNetwork(n); err != nil {
			return nil, fmt.Errorf("network %s: %w", n.Name, err)
		}
	}
	return networks, nil
}

func validateNetwork(n networkConfig) error {
	if !gasometer.NetworkKind(n.Kind).Valid() {
		return fmt.Errorf("%w: %q", errInvalidNetworkKind, n.Kind)
	}
	if n.ChainID == 0 || n.Coin == "" {
		return errInvalidNetworkIdentity
	}
	if len(n.RPC.URLs) == 0 {
		return errInvalidRPCURL
	}
	for _, raw := range n.RPC.URLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: %q", errInvalidRPCURL, raw)
		}
	}
	r := n.RPC
	if r.RateLimit <= 0 || r.Burst < 1 || r.Timeout <= 0 || r.MaxRetries < 0 || r.InitialBackoff <= 0 {
		return fmt.Errorf("%w: %+v", errInvalidRPCLimits, r)
	}
	if !common.IsHexAddress(n.PriceFeed) {
		return fmt.Errorf("%w: %q", errInvalidPriceFeed, n.PriceFeed)
	}
	if n.PriceMaxAge <= 0 {
		return fmt.Errorf("%w: %s", errInvalidPriceMaxAge, n.PriceMaxAge)
	}
	if n.RequiredTipPercentile < 0 || n.RequiredTipPercentile > 100 {
		return fmt.Errorf("%w: %v", errInvalidTipPercentile, n.RequiredTipPercentile)
	}
	if n.HistoryDepth < 1 || n.FeeHistoryBatch < 1 || n.InitialBackfill < 1 {
		return fmt.Errorf("%w: depth %d, batch %d, backfill %d", errInvalidHistoryDepth, n.HistoryDepth, n.FeeHistoryBatch, n.InitialBackfill)
	}
	return nil
}

// decodeList decodes each entry of a list of objects into a fresh value from newEntry, so omitted fields keep
// their defaults.
func decodeList[T any](v *viper.Viper, key string, newEntry func() T, out *[]T) error {
	var raw []map[string]any
	if err := v.UnmarshalKey(key, &raw); err != nil {
		return fmt.Errorf("%w %s: %w", errDecodeList, key, err)
	}
	for i, entry := range raw {
		value := newEntry()
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
			),
			ErrorUnused:      true,
			WeaklyTypedInput: true,
			Result:           &value,
		})
		if err != nil {
			return err
		}
		if err := decoder.Decode(entry); err != nil {
			return fmt.Errorf("%w %s[%d]: %w", errDecodeList, key, i, err)
		}
		*out = append(*out, value)
	}
	return nil
}

func parseLevel(level string) (slog.Level, error) {
	var l slog.Level
	if level == "" {
		return l, errInvalidLogLevel
	}
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return l, fmt.Errorf("%w: %q", errInvalidLogLevel, level)
	}
	return l, nil
}

// newLogger writes JSON at level, or human-readable lines when format is "console".
func newLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "console" {
		return slog.New(slog.NewTextHandler(w, opts)), nil
	}
	return slog.New(slog.NewJSONHandler(w, opts)), nil
}
