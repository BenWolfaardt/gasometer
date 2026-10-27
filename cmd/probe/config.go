package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/rpc"
)

// seedEnv names the variable holding the probe seed, hex encoded; it is read from the environment only, so it never
// reaches a shell history or a config file.
const seedEnv = "GASOMETER_PROBE_SEED"

const (
	commandFund  = "fund"
	commandRun   = "run"
	commandSweep = "sweep"
)

const usage = `Usage: probe <fund|run|sweep> [flags]

  fund   top every rung up to --fund-wei from account 0
  run    send a round of the tip ladder every --interval for --duration
  sweep  return every rung's balance to account 0

The seed is read from ` + seedEnv + `, 32 bytes in hex.

Flags:
`

// config is one invocation's settings.
type config struct {
	command      string
	rpcURL       string
	chainID      *big.Int
	network      string
	databaseURL  string
	interval     time.Duration
	duration     time.Duration
	timeout      time.Duration
	pollInterval time.Duration
	rateLimit    float64
	fundWei      *big.Int
	seed         []byte
	logLevel     slog.Level
}

// parseConfig reads the command and flags from args and the seed from getenv.
func parseConfig(args []string, getenv func(string) string, stderr io.Writer) (config, error) {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	rpcURL := fs.String("rpc-url", "https://api.avax.network/ext/bc/C/rpc", "The one endpoint transactions are sent to")
	chainID := fs.Int64("chain-id", 43114, "Chain ID transactions are signed for")
	network := fs.String("network", "cchain", "Network name recorded with each probe")
	databaseURL := fs.String("database-url", "", "Postgres connection URL; required by run")
	interval := fs.Duration("interval", 5*time.Minute, "Time between rounds")
	duration := fs.Duration("duration", 48*time.Hour, "How long run sends rounds for")
	timeout := fs.Duration("timeout", 60*time.Second, "Wait for inclusion before a probe is replaced")
	pollInterval := fs.Duration("poll-interval", time.Second, "Time between receipt lookups")
	rateLimit := fs.Float64("rate-limit", 10, "Requests per second to the endpoint")
	fundWei := fs.String("fund-wei", "60000000000000000", "Balance fund tops each rung up to, in wei")
	logLevel := fs.String("log-level", "info", "Log level: debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}

	cfg := config{
		rpcURL: *rpcURL, chainID: big.NewInt(*chainID), network: *network, databaseURL: *databaseURL,
		interval: *interval, duration: *duration, timeout: *timeout, pollInterval: *pollInterval, rateLimit: *rateLimit,
	}
	var errs []error
	switch fs.NArg() {
	case 1:
		cfg.command = fs.Arg(0)
	default:
		errs = append(errs, fmt.Errorf("want one command, got %q", fs.Args()))
	}
	switch cfg.command {
	case commandFund, commandSweep:
	case commandRun:
		if cfg.databaseURL == "" {
			errs = append(errs, errors.New("run needs --database-url"))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown command %q", cfg.command))
	}
	for name, d := range map[string]time.Duration{"interval": cfg.interval, "duration": cfg.duration,
		"timeout": cfg.timeout, "poll-interval": cfg.pollInterval} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("--%s must be positive", name))
		}
	}
	if 2*cfg.timeout >= cfg.interval {
		errs = append(errs, errors.New("--interval must exceed twice --timeout, so a round settles before the next"))
	}
	if cfg.rateLimit <= 0 {
		errs = append(errs, errors.New("--rate-limit must be positive"))
	}
	var ok bool
	if cfg.fundWei, ok = new(big.Int).SetString(*fundWei, 10); !ok || cfg.fundWei.Sign() <= 0 {
		errs = append(errs, fmt.Errorf("--fund-wei %q is not a positive integer", *fundWei))
	}
	if err := cfg.logLevel.UnmarshalText([]byte(*logLevel)); err != nil {
		errs = append(errs, fmt.Errorf("--log-level: %w", err))
	}
	seed, err := hex.DecodeString(strings.TrimPrefix(getenv(seedEnv), "0x"))
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("%s is not hex", seedEnv))
	case len(seed) != rpc.SeedLength:
		errs = append(errs, fmt.Errorf("%s is %d bytes, want %d", seedEnv, len(seed), rpc.SeedLength))
	}
	cfg.seed = seed
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("%w: %w", gasometer.ErrInvalidConfig, err)
	}
	return cfg, nil
}
