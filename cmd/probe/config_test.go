package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/BenWolfaardt/gasometer"
)

var testSeedHex = strings.Repeat("ab", 32)

func env(seed string) func(string) string {
	return func(key string) string {
		if key == seedEnv {
			return seed
		}
		return ""
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig([]string{"run", "--database-url", "postgres://x"}, env(testSeedHex), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.command != commandRun || cfg.rpcURL != "https://api.avax.network/ext/bc/C/rpc" || cfg.chainID.Int64() != 43114 ||
		cfg.network != "cchain" || cfg.interval != 5*time.Minute || cfg.duration != 48*time.Hour ||
		cfg.timeout != time.Minute || cfg.pollInterval != time.Second || cfg.rateLimit != 10 ||
		cfg.fundWei.String() != "60000000000000000" || len(cfg.seed) != 32 || cfg.seed[0] != 0xab {
		t.Fatalf("config %+v", cfg)
	}
}

func TestParseConfigAcceptsPrefixedSeedAndFlags(t *testing.T) {
	cfg, err := parseConfig([]string{"fund", "--fund-wei", "1000", "--log-level", "debug"}, env("0x"+testSeedHex), io.Discard)
	if err != nil || cfg.command != commandFund || cfg.fundWei.Int64() != 1_000 || cfg.logLevel.String() != "DEBUG" {
		t.Fatalf("config %+v, err %v", cfg, err)
	}
	if _, err := parseConfig([]string{"sweep"}, env(testSeedHex), io.Discard); err != nil {
		t.Fatalf("sweep without a database: %v", err)
	}
}

func TestParseConfigRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		seed string
	}{
		{"no command", nil, testSeedHex},
		{"two commands", []string{"fund", "run"}, testSeedHex},
		{"unknown command", []string{"send"}, testSeedHex},
		{"run without database", []string{"run"}, testSeedHex},
		{"missing seed", []string{"fund"}, ""},
		{"short seed", []string{"fund"}, testSeedHex[2:]},
		{"seed not hex", []string{"fund"}, strings.Repeat("zz", 32)},
		{"zero fund", []string{"fund", "--fund-wei", "0"}, testSeedHex},
		{"fund not a number", []string{"fund", "--fund-wei", "0.5"}, testSeedHex},
		{"round longer than interval", []string{"fund", "--interval", "2m"}, testSeedHex},
		{"zero poll interval", []string{"fund", "--poll-interval", "0s"}, testSeedHex},
		{"zero rate limit", []string{"fund", "--rate-limit", "0"}, testSeedHex},
		{"bad log level", []string{"fund", "--log-level", "loud"}, testSeedHex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseConfig(tc.args, env(tc.seed), io.Discard); !errors.Is(err, gasometer.ErrInvalidConfig) {
				t.Fatalf("err %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestParseConfigHelp(t *testing.T) {
	var out strings.Builder
	if _, err := parseConfig([]string{"--help"}, env(""), &out); !errors.Is(err, pflag.ErrHelp) ||
		!strings.Contains(out.String(), seedEnv) {
		t.Fatalf("err %v, output %q", err, out.String())
	}
}
