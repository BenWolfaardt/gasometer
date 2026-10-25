// Package logging builds the service logger.
package logging

import (
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog"
)

// New returns a logger at level writing JSON, or human-readable lines when format is "console".
func New(level, format string, w io.Writer) (zerolog.Logger, error) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		return zerolog.Logger{}, fmt.Errorf("parse log level: %w", err)
	}
	if format == "console" {
		w = zerolog.ConsoleWriter{Out: w, TimeFormat: time.RFC3339}
	}
	return zerolog.New(w).Level(lvl).With().Timestamp().Logger(), nil
}
