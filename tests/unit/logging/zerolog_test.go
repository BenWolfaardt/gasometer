package logging_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/infrastructure/logging"
)

func TestJSONLogger(t *testing.T) {
	var buf bytes.Buffer
	log, err := logging.New("info", "json", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug().Msg("hidden")
	log.Info().Str("network", "base").Msg("polled")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("not one JSON line: %q", buf.String())
	}
	if line["message"] != "polled" || line["network"] != "base" || line["level"] != "info" || line["time"] == nil {
		t.Fatalf("line %v", line)
	}
}

func TestConsoleLogger(t *testing.T) {
	var buf bytes.Buffer
	log, err := logging.New("debug", "console", &buf)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug().Msg("polled")
	if out := buf.String(); !strings.Contains(out, "polled") || strings.HasPrefix(out, "{") {
		t.Fatalf("console output %q", out)
	}
}

func TestLoggerRejectsBadLevel(t *testing.T) {
	if _, err := logging.New("loud", "json", &bytes.Buffer{}); err == nil {
		t.Fatal("want error")
	}
}
