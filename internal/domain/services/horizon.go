package services

import (
	"fmt"
	"math"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

// BlocksIn converts a duration to a whole number of blocks at the given average block time, at least one.
func BlocksIn(d, avgBlockTime time.Duration) (int, error) {
	if avgBlockTime <= 0 || d <= 0 {
		return 0, fmt.Errorf("%w: %s at %s per block", entities.ErrInvalidHorizon, d, avgBlockTime)
	}
	return max(1, int(math.Round(float64(d)/float64(avgBlockTime)))), nil
}

// Horizons converts horizon seconds to block counts at the given average block time.
func Horizons(seconds []int, avgBlockTime time.Duration) ([]entities.Horizon, error) {
	out := make([]entities.Horizon, 0, len(seconds))
	for _, s := range seconds {
		n, err := BlocksIn(time.Duration(s)*time.Second, avgBlockTime)
		if err != nil {
			return nil, err
		}
		out = append(out, entities.Horizon{Seconds: s, Blocks: n})
	}
	return out, nil
}
