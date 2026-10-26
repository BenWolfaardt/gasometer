package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
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

func load(t *testing.T, args ...string) (fileConfig, error) {
	t.Helper()
	v, err := buildViper(buildFlagSet(), args, &bytes.Buffer{})
	if err != nil {
		return fileConfig{}, err
	}
	return getConfig(v)
}

func TestRepositoryConfigLoads(t *testing.T) {
	t.Setenv(envVarName(databaseURLKey), "postgres://localhost/gasometer")
	cfg, err := load(t, "--"+configFileKey, "../../configs/gasometer.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Networks) != 3 || cfg.Database.URL != "postgres://localhost/gasometer" {
		t.Fatalf("config %+v", cfg)
	}
	c := cfg.Networks[2]
	if c.Name != "cchain" || gasometer.NetworkKind(c.Kind) != gasometer.NetworkKindAvalancheSAE || c.HistoryDepth != 25_000 ||
		len(c.RPC.URLs) != 2 || c.PriceMaxAge != time.Hour || c.RequiredTipPercentile != 0 || cfg.Networks[0].RequiredTipPercentile != 5 {
		t.Fatalf("cchain %+v", c)
	}
	if cfg.Gasometer.GrowthWindow != time.Hour || len(cfg.ReferenceTxs) != 2 || cfg.ReferenceTxs[1].DataSize != 68 {
		t.Fatalf("gasometer %+v, txs %+v", cfg.Gasometer, cfg.ReferenceTxs)
	}
}

func TestDefaults(t *testing.T) {
	t.Setenv(envVarName(databaseURLKey), "postgres://x")
	cfg, err := load(t, "--"+configFileKey, writeConfig(t, minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Networks[0]
	if n.RequiredTipPercentile != 5 || n.FeeHistoryBatch != 1024 || n.InitialBackfill != 1024 || n.RPC.RateLimit != 5 || n.RPC.Burst != 10 ||
		n.RPC.Timeout != 10*time.Second || n.RPC.MaxRetries != 3 || n.RPC.InitialBackoff != 500*time.Millisecond {
		t.Fatalf("network defaults %+v", n)
	}
	if cfg.Schedule.BlockRetention != 30*24*time.Hour || cfg.Scoring.ExpiryFactor != 10 ||
		len(cfg.Scoring.Horizons) != 3 || len(cfg.Predictors) != 4 || cfg.Gasometer.TipWindow != 1_800 ||
		cfg.Log.Format != "json" || cfg.HTTP.ListenAddress != ":9090" {
		t.Fatalf("defaults %+v", cfg)
	}
	if len(cfg.ReferenceTxs) != 2 || cfg.ReferenceTxs[0].Gas != 21_000 || cfg.ReferenceTxs[1].Gas != 65_000 {
		t.Fatalf("reference txs %+v", cfg.ReferenceTxs)
	}
}

func TestPrecedence(t *testing.T) {
	path := writeConfig(t, minimalYAML)
	t.Setenv(envVarName(databaseURLKey), "postgres://x")

	cfg, err := load(t, "--"+configFileKey, path)
	if err != nil || cfg.Schedule.PollInterval != 30*time.Second {
		t.Fatalf("file: %v %v", cfg.Schedule.PollInterval, err)
	}

	t.Setenv(envVarName(pollIntervalKey), "45s")
	cfg, err = load(t, "--"+configFileKey, path)
	if err != nil || cfg.Schedule.PollInterval != 45*time.Second {
		t.Fatalf("env over file: %v %v", cfg.Schedule.PollInterval, err)
	}

	cfg, err = load(t, "--"+configFileKey, path, "--"+pollIntervalKey, "15s")
	if err != nil || cfg.Schedule.PollInterval != 15*time.Second {
		t.Fatalf("flag over env: %v %v", cfg.Schedule.PollInterval, err)
	}
}

func TestEnvVarName(t *testing.T) {
	if got := envVarName(gasometerGrowthWindowKey); got != "GASOMETER_GASOMETER_GROWTH_WINDOW" {
		t.Fatalf("got %s", got)
	}
}

func TestConfigContent(t *testing.T) {
	t.Setenv(envVarName(databaseURLKey), "postgres://x")
	content := base64.StdEncoding.EncodeToString([]byte(minimalYAML))
	cfg, err := load(t, "--"+configContentKey, content, "--"+configFileKey, "/does/not/exist.yml")
	if err != nil || cfg.Schedule.PollInterval != 30*time.Second {
		t.Fatalf("content: %v %v", cfg.Schedule.PollInterval, err)
	}
	if _, err := load(t, "--"+configContentKey, "not base64!"); err == nil {
		t.Fatal("bad base64 accepted")
	}
}

func TestWarnDeprecated(t *testing.T) {
	v, err := buildViper(buildFlagSet(), []string{"--" + configFileKey, writeConfig(t, "old-key: 1\n")}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	warnDeprecated(v, map[string]string{"old-key": "use new-key", "unused-key": "gone"}, &out)
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
		{"log level", []string{"--log-level", "loud"}, [2]string{}, errInvalidLogLevel},
		{"log format", []string{"--log-format", "xml"}, [2]string{}, errInvalidLogFormat},
		{"listen address", []string{"--http-listen-address", ""}, [2]string{}, errMissingListenAddress},
		{"shutdown timeout", []string{"--http-shutdown-timeout", "0s"}, [2]string{}, errInvalidShutdownTimeout},
		{"database url", []string{"--database-url", ""}, [2]string{}, errMissingDatabaseURL},
		{"poll interval", []string{"--poll-interval", "0s"}, [2]string{}, errInvalidPollInterval},
		{"retention below horizons", []string{"--block-retention", "20m"}, [2]string{}, errInvalidBlockRetention},
		{"no horizons", nil, [2]string{"poll-interval: 30s", "horizons: []"}, errNoHorizons},
		{"duplicate horizon", []string{"--horizons", "2,2"}, [2]string{}, errInvalidHorizon},
		{"negative horizon", []string{"--horizons", "-2"}, [2]string{}, errInvalidHorizon},
		{"expiry factor", []string{"--expiry-factor", "0"}, [2]string{}, errInvalidExpiryFactor},
		{"no predictors", []string{"--predictors", ""}, [2]string{}, errNoPredictors},
		{"unknown predictor", []string{"--predictors", "oracle"}, [2]string{}, errUnknownPredictor},
		{"duplicate predictor", []string{"--predictors", "node,node"}, [2]string{}, errUnknownPredictor},
		{"tip quantile", []string{"--gasometer-tip-quantile", "1.5"}, [2]string{}, errInvalidQuantile},
		{"growth quantile", []string{"--gasometer-growth-quantile", "0"}, [2]string{}, errInvalidQuantile},
		{"tip window", []string{"--gasometer-tip-window", "0"}, [2]string{}, errInvalidTipWindow},
		{"growth window", []string{"--gasometer-growth-window", "0s"}, [2]string{}, errInvalidGrowthWindow},
		{"empty reference txs", nil, [2]string{"poll-interval: 30s", "reference-txs: []"}, errNoReferenceTxs},
		{"reference tx without gas", nil, [2]string{"poll-interval: 30s", "reference-txs: [{name: a}]"}, errInvalidReferenceTx},
		{"duplicate reference tx", nil, [2]string{"poll-interval: 30s", "reference-txs: [{name: a, gas: 1}, {name: a, gas: 1}]"}, errInvalidReferenceTx},
		{"no networks", nil, [2]string{"networks:", "unused:"}, errNoNetworks},
		{"network name", nil, [2]string{"name: base", "name: \"\""}, errInvalidNetworkName},
		{"network kind", nil, [2]string{"kind: opstack", "kind: rollup"}, errInvalidNetworkKind},
		{"chain id", nil, [2]string{"chain-id: 8453", "chain-id: 0"}, errInvalidNetworkIdentity},
		{"coin", nil, [2]string{"coin: ETH", "coin: \"\""}, errInvalidNetworkIdentity},
		{"rpc url scheme", nil, [2]string{"https://mainnet.base.org", "wss://mainnet.base.org"}, errInvalidRPCURL},
		{"no rpc urls", nil, [2]string{"urls: [https://mainnet.base.org]", "urls: []"}, errInvalidRPCURL},
		{"rpc rate limit", nil, [2]string{"urls: [https://mainnet.base.org]", "urls: [https://mainnet.base.org]\n      rate-limit: 0"}, errInvalidRPCLimits},
		{"price feed", nil, [2]string{"0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70", "0x1234"}, errInvalidPriceFeed},
		{"price max age", nil, [2]string{"price-max-age: 1h", "price-max-age: 0s"}, errInvalidPriceMaxAge},
		{"tip percentile", nil, [2]string{"history-depth: 100000", "history-depth: 100000\n    required-tip-percentile: 101"}, errInvalidTipPercentile},
		{"history depth", nil, [2]string{"history-depth: 100000", "history-depth: 0"}, errInvalidHistoryDepth},
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
			args := append([]string{"--" + configFileKey, writeConfig(t, yaml), "--database-url", "postgres://x"}, tt.args...)
			_, err := load(t, args...)
			if !errors.Is(err, tt.want) || !errors.Is(err, gasometer.ErrInvalidConfig) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestUnknownNetworkKeyIsRejected(t *testing.T) {
	yaml := strings.Replace(minimalYAML, "coin: ETH", "coin: ETH\n    colour: blue", 1)
	if _, err := load(t, "--"+configFileKey, writeConfig(t, yaml), "--database-url", "postgres://x"); err == nil ||
		!strings.Contains(err.Error(), "colour") {
		t.Fatalf("got %v", err)
	}
}

func TestJSONLogger(t *testing.T) {
	var buf bytes.Buffer
	log, err := newLogger("info", "json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hidden")
	log.Info("polled", "network", "base")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	if line["msg"] != "polled" || line["network"] != "base" || line["level"] != "INFO" || line["time"] == nil {
		t.Fatalf("line %v", line)
	}
}

func TestConsoleLogger(t *testing.T) {
	var buf bytes.Buffer
	log, err := newLogger("debug", "console", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("polled")
	if out := buf.String(); !strings.Contains(out, "msg=polled") || strings.HasPrefix(out, "{") {
		t.Fatalf("console output %q", out)
	}
}

func TestLoggerRejectsBadLevel(t *testing.T) {
	for _, level := range []string{"loud", "", "trace"} {
		if _, err := newLogger(level, "json", &bytes.Buffer{}); err == nil {
			t.Fatalf("level %q accepted", level)
		}
	}
}
