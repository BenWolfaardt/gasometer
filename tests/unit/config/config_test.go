package config_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/config"
)

const minimalYAML = `
poll-interval: 30s
networks:
  - name: base
    chain-id: 8453
    kind: opstack
    coin: ETH
    rpc:
      urls: [https://mainnet.base.org]
    price-feed: "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70"
    price-max-age: 1h
    history-depth: 100000
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gasometer.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, args ...string) (entities.Config, error) {
	t.Helper()
	v, err := config.BuildViper(config.BuildFlagSet(), args, &bytes.Buffer{})
	if err != nil {
		return entities.Config{}, err
	}
	return config.GetConfig(v)
}

func TestRepositoryConfigLoads(t *testing.T) {
	t.Setenv(config.EnvVarName(config.DatabaseURLKey), "postgres://localhost/gasometer")
	cfg, err := load(t, "--"+config.ConfigFileKey, "../../../configs/gasometer.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Networks) != 3 || cfg.Database.URL != "postgres://localhost/gasometer" {
		t.Fatalf("config %+v", cfg)
	}
	c := cfg.Networks[2]
	if c.Network.Name != "cchain" || c.Network.Kind != entities.NetworkKindAvalancheSAE || c.HistoryDepth != 25_000 ||
		len(c.RPC.URLs) != 2 || c.PriceMaxAge != time.Hour {
		t.Fatalf("cchain %+v", c)
	}
	if cfg.Gasometer.GrowthWindow != time.Hour || len(cfg.ReferenceTxs) != 2 || cfg.ReferenceTxs[1].DataSize != 68 {
		t.Fatalf("gasometer %+v, txs %+v", cfg.Gasometer, cfg.ReferenceTxs)
	}
}

func TestDefaults(t *testing.T) {
	t.Setenv(config.EnvVarName(config.DatabaseURLKey), "postgres://x")
	cfg, err := load(t, "--"+config.ConfigFileKey, writeConfig(t, minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks[0]
	if n.RequiredTipPercentile != 5 || n.FeeHistoryBatch != 1024 || n.RPC.RateLimit != 5 || n.RPC.Burst != 10 ||
		n.RPC.Timeout != 10*time.Second || n.RPC.MaxRetries != 3 || n.RPC.InitialBackoff != 500*time.Millisecond {
		t.Fatalf("network defaults %+v", n)
	}
	if cfg.Schedule.BlockRetention != 48*time.Hour || cfg.Scoring.ExpiryFactor != 10 ||
		len(cfg.Scoring.HorizonSeconds) != 3 || len(cfg.Predictors) != 3 || cfg.Gasometer.TipWindow != 30 ||
		cfg.Log.Format != "json" || cfg.HTTP.ListenAddress != ":9090" {
		t.Fatalf("defaults %+v", cfg)
	}
	if len(cfg.ReferenceTxs) != 2 || cfg.ReferenceTxs[0].Gas != 21_000 || cfg.ReferenceTxs[1].Gas != 65_000 {
		t.Fatalf("reference txs %+v", cfg.ReferenceTxs)
	}
}

func TestPrecedence(t *testing.T) {
	path := writeConfig(t, minimalYAML)
	t.Setenv(config.EnvVarName(config.DatabaseURLKey), "postgres://x")

	cfg, err := load(t, "--"+config.ConfigFileKey, path)
	if err != nil || cfg.Schedule.PollInterval != 30*time.Second {
		t.Fatalf("file: %v %v", cfg.Schedule.PollInterval, err)
	}

	t.Setenv(config.EnvVarName(config.PollIntervalKey), "45s")
	cfg, err = load(t, "--"+config.ConfigFileKey, path)
	if err != nil || cfg.Schedule.PollInterval != 45*time.Second {
		t.Fatalf("env over file: %v %v", cfg.Schedule.PollInterval, err)
	}

	cfg, err = load(t, "--"+config.ConfigFileKey, path, "--"+config.PollIntervalKey, "15s")
	if err != nil || cfg.Schedule.PollInterval != 15*time.Second {
		t.Fatalf("flag over env: %v %v", cfg.Schedule.PollInterval, err)
	}
}

func TestEnvVarName(t *testing.T) {
	if got := config.EnvVarName(config.GasometerGrowthWindowKey); got != "GASOMETER_GASOMETER_GROWTH_WINDOW" {
		t.Fatalf("got %s", got)
	}
}

func TestConfigContent(t *testing.T) {
	t.Setenv(config.EnvVarName(config.DatabaseURLKey), "postgres://x")
	content := base64.StdEncoding.EncodeToString([]byte(minimalYAML))
	cfg, err := load(t, "--"+config.ConfigContentKey, content, "--"+config.ConfigFileKey, "/does/not/exist.yml")
	if err != nil || cfg.Schedule.PollInterval != 30*time.Second {
		t.Fatalf("content: %v %v", cfg.Schedule.PollInterval, err)
	}
	if _, err := load(t, "--"+config.ConfigContentKey, "not base64!"); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

func TestWarnDeprecated(t *testing.T) {
	v, err := config.BuildViper(config.BuildFlagSet(), []string{"--" + config.ConfigFileKey, writeConfig(t, "old-key: 1\n")}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	config.WarnDeprecated(v, map[string]string{"old-key": "use new-key", "unused-key": "gone"}, &out)
	if got := out.String(); got != "config old-key is deprecated: use new-key\n" {
		t.Fatalf("got %q", got)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		replace [2]string
		want    error
	}{
		{"log level", []string{"--log-level", "loud"}, [2]string{}, config.ErrInvalidLogLevel},
		{"log format", []string{"--log-format", "xml"}, [2]string{}, config.ErrInvalidLogFormat},
		{"listen address", []string{"--http-listen-address", ""}, [2]string{}, config.ErrMissingListenAddress},
		{"shutdown timeout", []string{"--http-shutdown-timeout", "0s"}, [2]string{}, config.ErrInvalidShutdownTimeout},
		{"database url", []string{"--database-url", ""}, [2]string{}, config.ErrMissingDatabaseURL},
		{"poll interval", []string{"--poll-interval", "0s"}, [2]string{}, config.ErrInvalidPollInterval},
		{"retention below horizons", []string{"--block-retention", "20m"}, [2]string{}, config.ErrInvalidBlockRetention},
		{"no horizons", nil, [2]string{"poll-interval: 30s", "horizons: []"}, config.ErrNoHorizons},
		{"duplicate horizon", []string{"--horizons", "2,2"}, [2]string{}, config.ErrInvalidHorizon},
		{"negative horizon", []string{"--horizons", "-2"}, [2]string{}, config.ErrInvalidHorizon},
		{"expiry factor", []string{"--expiry-factor", "0"}, [2]string{}, config.ErrInvalidExpiryFactor},
		{"no predictors", []string{"--predictors", ""}, [2]string{}, config.ErrNoPredictors},
		{"unknown predictor", []string{"--predictors", "oracle"}, [2]string{}, config.ErrUnknownPredictor},
		{"duplicate predictor", []string{"--predictors", "node,node"}, [2]string{}, config.ErrUnknownPredictor},
		{"tip quantile", []string{"--gasometer-tip-quantile", "1.5"}, [2]string{}, config.ErrInvalidQuantile},
		{"growth quantile", []string{"--gasometer-growth-quantile", "0"}, [2]string{}, config.ErrInvalidQuantile},
		{"tip window", []string{"--gasometer-tip-window", "0"}, [2]string{}, config.ErrInvalidTipWindow},
		{"growth window", []string{"--gasometer-growth-window", "0s"}, [2]string{}, config.ErrInvalidGrowthWindow},
		{"empty reference txs", nil, [2]string{"poll-interval: 30s", "reference-txs: []"}, config.ErrNoReferenceTxs},
		{"reference tx without gas", nil, [2]string{"poll-interval: 30s", "reference-txs: [{name: a}]"}, config.ErrInvalidReferenceTx},
		{"duplicate reference tx", nil, [2]string{"poll-interval: 30s", "reference-txs: [{name: a, gas: 1}, {name: a, gas: 1}]"}, config.ErrInvalidReferenceTx},
		{"no networks", nil, [2]string{"networks:", "unused:"}, config.ErrNoNetworks},
		{"network name", nil, [2]string{"name: base", "name: \"\""}, config.ErrInvalidNetworkName},
		{"network kind", nil, [2]string{"kind: opstack", "kind: rollup"}, config.ErrInvalidNetworkKind},
		{"chain id", nil, [2]string{"chain-id: 8453", "chain-id: 0"}, config.ErrInvalidNetworkIdentity},
		{"coin", nil, [2]string{"coin: ETH", "coin: \"\""}, config.ErrInvalidNetworkIdentity},
		{"rpc url scheme", nil, [2]string{"https://mainnet.base.org", "wss://mainnet.base.org"}, config.ErrInvalidRPCURL},
		{"no rpc urls", nil, [2]string{"urls: [https://mainnet.base.org]", "urls: []"}, config.ErrInvalidRPCURL},
		{"rpc rate limit", nil, [2]string{"urls: [https://mainnet.base.org]", "urls: [https://mainnet.base.org]\n      rate-limit: 0"}, config.ErrInvalidRPCLimits},
		{"price feed", nil, [2]string{"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", "0x1234"}, config.ErrInvalidPriceFeed},
		{"price max age", nil, [2]string{"price-max-age: 1h", "price-max-age: 0s"}, config.ErrInvalidPriceMaxAge},
		{"tip percentile", nil, [2]string{"history-depth: 100000", "history-depth: 100000\n    required-tip-percentile: 101"}, config.ErrInvalidTipPercentile},
		{"history depth", nil, [2]string{"history-depth: 100000", "history-depth: 0"}, config.ErrInvalidHistoryDepth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := minimalYAML
			if tt.replace[0] != "" {
				if !strings.Contains(yaml, tt.replace[0]) {
					t.Fatalf("fixture lacks %q", tt.replace[0])
				}
				yaml = strings.Replace(yaml, tt.replace[0], tt.replace[1], 1)
			}
			args := append([]string{"--" + config.ConfigFileKey, writeConfig(t, yaml), "--database-url", "postgres://x"}, tt.args...)
			_, err := load(t, args...)
			if !errors.Is(err, tt.want) || !errors.Is(err, entities.ErrInvalidConfig) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestUnknownNetworkKeyIsRejected(t *testing.T) {
	yaml := strings.Replace(minimalYAML, "coin: ETH", "coin: ETH\n    colour: blue", 1)
	if _, err := load(t, "--"+config.ConfigFileKey, writeConfig(t, yaml), "--database-url", "postgres://x"); err == nil ||
		!strings.Contains(err.Error(), "colour") {
		t.Fatalf("got %v", err)
	}
}
