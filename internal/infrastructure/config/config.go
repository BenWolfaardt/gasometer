package config

import (
	"fmt"
	"net/url"
	"slices"
	"time"

	"github.com/ava-labs/libevm/common"
	"github.com/go-viper/mapstructure/v2"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// Predictor names the config accepts.
var knownPredictors = []string{"node", "wallet", "gasometer"}

var (
	ErrInvalidLogLevel        = invalid("log level must be trace, debug, info, warn or error")
	ErrInvalidLogFormat       = invalid("log format must be json or console")
	ErrMissingListenAddress   = invalid("http listen address is required")
	ErrInvalidShutdownTimeout = invalid("http shutdown timeout must be positive")
	ErrMissingDatabaseURL     = invalid("database url is required")
	ErrInvalidPollInterval    = invalid("poll interval must be positive")
	ErrInvalidBlockRetention  = invalid("block retention must outlast the longest horizon times the expiry factor")
	ErrNoHorizons             = invalid("at least one horizon is required")
	ErrInvalidHorizon         = invalid("horizons must be positive and unique")
	ErrInvalidExpiryFactor    = invalid("expiry factor must be at least 1")
	ErrNoPredictors           = invalid("at least one predictor is required")
	ErrUnknownPredictor       = invalid("predictor must be node, wallet or gasometer, once each")
	ErrInvalidQuantile        = invalid("quantiles must be in (0, 1]")
	ErrInvalidTipWindow       = invalid("tip window must be at least 1 block")
	ErrInvalidGrowthWindow    = invalid("growth window must be positive")
	ErrNoReferenceTxs         = invalid("at least one reference transaction is required")
	ErrInvalidReferenceTx     = invalid("reference transactions need a unique name, positive gas and non-negative data size")
	ErrNoNetworks             = invalid("at least one network is required")
	ErrInvalidNetworkName     = invalid("networks need a unique, non-empty name")
	ErrInvalidNetworkKind     = invalid("network kind must be opstack or avalanche-sae")
	ErrInvalidNetworkIdentity = invalid("networks need a chain id and a coin")
	ErrInvalidRPCURL          = invalid("rpc urls must be one or more http or https urls")
	ErrInvalidRPCLimits       = invalid("rpc rate limit, burst, timeout and backoff must be positive, retries non-negative")
	ErrInvalidPriceFeed       = invalid("price feed must be a hex address")
	ErrInvalidPriceMaxAge     = invalid("price max age must be positive")
	ErrInvalidTipPercentile   = invalid("required tip percentile must be in [0, 100]")
	ErrInvalidHistoryDepth    = invalid("history depth and fee history batch must be at least 1")
	errDecodeList             = invalid("decode config list")
)

func invalid(msg string) error {
	return fmt.Errorf("%w: %s", entities.ErrInvalidConfig, msg)
}

// GetConfig reads and validates every section of v.
func GetConfig(v *viper.Viper) (entities.Config, error) {
	var (
		dto DTO
		err error
	)
	if dto.Log, err = getLogConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.HTTP, err = getHTTPConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.Database, err = getDatabaseConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.Scoring, err = getScoringConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.Schedule, err = getScheduleConfig(v, dto.Scoring); err != nil {
		return entities.Config{}, err
	}
	if dto.Predictors, err = getPredictorsConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.Gasometer, err = getGasometerConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.ReferenceTxs, err = getReferenceTxsConfig(v); err != nil {
		return entities.Config{}, err
	}
	if dto.Networks, err = getNetworksConfig(v); err != nil {
		return entities.Config{}, err
	}
	return dto.ToDomain(), nil
}

func getLogConfig(v *viper.Viper) (LogDTO, error) {
	c := LogDTO{Level: v.GetString(LogLevelKey), Format: v.GetString(LogFormatKey)}
	if _, err := zerolog.ParseLevel(c.Level); err != nil || c.Level == "" {
		return LogDTO{}, fmt.Errorf("%w: %q", ErrInvalidLogLevel, c.Level)
	}
	if c.Format != "json" && c.Format != "console" {
		return LogDTO{}, fmt.Errorf("%w: %q", ErrInvalidLogFormat, c.Format)
	}
	return c, nil
}

func getHTTPConfig(v *viper.Viper) (HTTPDTO, error) {
	c := HTTPDTO{ListenAddress: v.GetString(HTTPListenAddressKey), ShutdownTimeout: v.GetDuration(HTTPShutdownTimeoutKey)}
	if c.ListenAddress == "" {
		return HTTPDTO{}, ErrMissingListenAddress
	}
	if c.ShutdownTimeout <= 0 {
		return HTTPDTO{}, fmt.Errorf("%w: %s", ErrInvalidShutdownTimeout, c.ShutdownTimeout)
	}
	return c, nil
}

func getDatabaseConfig(v *viper.Viper) (DatabaseDTO, error) {
	c := DatabaseDTO{URL: v.GetString(DatabaseURLKey)}
	if c.URL == "" {
		return DatabaseDTO{}, ErrMissingDatabaseURL
	}
	return c, nil
}

func getScoringConfig(v *viper.Viper) (ScoringDTO, error) {
	c := ScoringDTO{Horizons: v.GetIntSlice(HorizonsKey), ExpiryFactor: v.GetInt(ExpiryFactorKey)}
	if len(c.Horizons) == 0 {
		return ScoringDTO{}, ErrNoHorizons
	}
	seen := map[int]bool{}
	for _, h := range c.Horizons {
		if h <= 0 || seen[h] {
			return ScoringDTO{}, fmt.Errorf("%w: %v", ErrInvalidHorizon, c.Horizons)
		}
		seen[h] = true
	}
	if c.ExpiryFactor < 1 {
		return ScoringDTO{}, fmt.Errorf("%w: %d", ErrInvalidExpiryFactor, c.ExpiryFactor)
	}
	return c, nil
}

func getScheduleConfig(v *viper.Viper, scoring ScoringDTO) (ScheduleDTO, error) {
	c := ScheduleDTO{PollInterval: v.GetDuration(PollIntervalKey), BlockRetention: v.GetDuration(BlockRetentionKey)}
	if c.PollInterval <= 0 {
		return ScheduleDTO{}, fmt.Errorf("%w: %s", ErrInvalidPollInterval, c.PollInterval)
	}
	longest := time.Duration(slices.Max(scoring.Horizons)*scoring.ExpiryFactor) * time.Second
	if c.BlockRetention <= longest {
		return ScheduleDTO{}, fmt.Errorf("%w: %s against %s", ErrInvalidBlockRetention, c.BlockRetention, longest)
	}
	return c, nil
}

func getPredictorsConfig(v *viper.Viper) ([]string, error) {
	names := v.GetStringSlice(PredictorsKey)
	if len(names) == 0 {
		return nil, ErrNoPredictors
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !slices.Contains(knownPredictors, n) || seen[n] {
			return nil, fmt.Errorf("%w: %v", ErrUnknownPredictor, names)
		}
		seen[n] = true
	}
	return names, nil
}

func getGasometerConfig(v *viper.Viper) (GasometerDTO, error) {
	c := GasometerDTO{
		TipQuantile:    v.GetFloat64(GasometerTipQuantileKey),
		TipWindow:      v.GetInt(GasometerTipWindowKey),
		GrowthQuantile: v.GetFloat64(GasometerGrowthQuantileKey),
		GrowthWindow:   v.GetDuration(GasometerGrowthWindowKey),
	}
	for _, q := range []float64{c.TipQuantile, c.GrowthQuantile} {
		if q <= 0 || q > 1 {
			return GasometerDTO{}, fmt.Errorf("%w: %v", ErrInvalidQuantile, q)
		}
	}
	if c.TipWindow < 1 {
		return GasometerDTO{}, fmt.Errorf("%w: %d", ErrInvalidTipWindow, c.TipWindow)
	}
	if c.GrowthWindow <= 0 {
		return GasometerDTO{}, fmt.Errorf("%w: %s", ErrInvalidGrowthWindow, c.GrowthWindow)
	}
	return c, nil
}

func getReferenceTxsConfig(v *viper.Viper) ([]ReferenceTxDTO, error) {
	if !v.IsSet(ReferenceTxsKey) {
		return slices.Clone(defaultReferenceTxs), nil
	}
	var txs []ReferenceTxDTO
	if err := decodeList(v, ReferenceTxsKey, func() ReferenceTxDTO { return ReferenceTxDTO{} }, &txs); err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return nil, ErrNoReferenceTxs
	}
	seen := map[string]bool{}
	for _, tx := range txs {
		if tx.Name == "" || seen[tx.Name] || tx.Gas == 0 || tx.DataSize < 0 {
			return nil, fmt.Errorf("%w: %+v", ErrInvalidReferenceTx, tx)
		}
		seen[tx.Name] = true
	}
	return txs, nil
}

func getNetworksConfig(v *viper.Viper) ([]NetworkDTO, error) {
	var networks []NetworkDTO
	if err := decodeList(v, NetworksKey, func() NetworkDTO { return defaultNetwork }, &networks); err != nil {
		return nil, err
	}
	if len(networks) == 0 {
		return nil, ErrNoNetworks
	}
	seen := map[string]bool{}
	for _, n := range networks {
		if n.Name == "" || seen[n.Name] {
			return nil, fmt.Errorf("%w: %q", ErrInvalidNetworkName, n.Name)
		}
		seen[n.Name] = true
		if err := validateNetwork(n); err != nil {
			return nil, fmt.Errorf("network %s: %w", n.Name, err)
		}
	}
	return networks, nil
}

func validateNetwork(n NetworkDTO) error {
	if !entities.NetworkKind(n.Kind).Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidNetworkKind, n.Kind)
	}
	if n.ChainID == 0 || n.Coin == "" {
		return ErrInvalidNetworkIdentity
	}
	if len(n.RPC.URLs) == 0 {
		return ErrInvalidRPCURL
	}
	for _, raw := range n.RPC.URLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: %q", ErrInvalidRPCURL, raw)
		}
	}
	r := n.RPC
	if r.RateLimit <= 0 || r.Burst < 1 || r.Timeout <= 0 || r.MaxRetries < 0 || r.InitialBackoff <= 0 {
		return fmt.Errorf("%w: %+v", ErrInvalidRPCLimits, r)
	}
	if !common.IsHexAddress(n.PriceFeed) {
		return fmt.Errorf("%w: %q", ErrInvalidPriceFeed, n.PriceFeed)
	}
	if n.PriceMaxAge <= 0 {
		return fmt.Errorf("%w: %s", ErrInvalidPriceMaxAge, n.PriceMaxAge)
	}
	if n.RequiredTipPercentile < 0 || n.RequiredTipPercentile > 100 {
		return fmt.Errorf("%w: %v", ErrInvalidTipPercentile, n.RequiredTipPercentile)
	}
	if n.HistoryDepth < 1 || n.FeeHistoryBatch < 1 {
		return fmt.Errorf("%w: depth %d, batch %d", ErrInvalidHistoryDepth, n.HistoryDepth, n.FeeHistoryBatch)
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
