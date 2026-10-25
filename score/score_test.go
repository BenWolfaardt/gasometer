package score_test

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/score"
)

func bid(tip, maxFee, expected int64) gasometer.Bid {
	return gasometer.Bid{Tip: wei(tip), MaxFee: wei(maxFee), ExpectedBaseFee: wei(expected)}
}

func TestScoreBid(t *testing.T) {
	tests := []struct {
		name              string
		bid               gasometer.Bid
		baseFees, tips    []int64
		included          bool
		inclusion         uint64
		paidBase, paidTip int64
		reqBase, reqTip   int64
		estimateError     float64
	}{
		{
			name: "included in the first block",
			bid:  bid(10, 200, 100), baseFees: []int64{100, 100, 100}, tips: []int64{5, 5, 5},
			included: true, inclusion: 11, paidBase: 100, paidTip: 10, reqBase: 100, reqTip: 5,
		},
		{
			name: "included late once the tip clears",
			bid:  bid(10, 200, 100), baseFees: []int64{100, 100, 90}, tips: []int64{20, 15, 8},
			included: true, inclusion: 13, paidBase: 90, paidTip: 10, reqBase: 90, reqTip: 8,
			estimateError: 10.0 / 100,
		},
		{
			name: "missed on base fee",
			bid:  bid(10, 105, 95), baseFees: []int64{110, 120, 130}, tips: []int64{0, 0, 0},
			reqBase: 110, reqTip: 0,
		},
		{
			name: "missed on tip",
			bid:  bid(4, 500, 100), baseFees: []int64{100, 100, 100}, tips: []int64{5, 6, 5},
			reqBase: 100, reqTip: 5,
		},
		{
			name: "missed on effective tip",
			bid:  bid(10, 105, 95), baseFees: []int64{100}, tips: []int64{8},
			reqBase: 100, reqTip: 8,
		},
		{
			name: "effective tip capped by max fee",
			bid:  bid(10, 108, 95), baseFees: []int64{100}, tips: []int64{8},
			included: true, inclusion: 11, paidBase: 100, paidTip: 8, reqBase: 100, reqTip: 8,
			estimateError: 3.0 / 108,
		},
		{
			name: "required is the cheapest block, not the inclusion block",
			bid:  bid(10, 200, 100), baseFees: []int64{100, 50}, tips: []int64{5, 5},
			included: true, inclusion: 11, paidBase: 100, paidTip: 10, reqBase: 50, reqTip: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := score.Bid(tt.bid, 10, len(tt.baseFees), blocks(11, tt.baseFees, tt.tips))
			if err != nil {
				t.Fatal(err)
			}
			if got.Included != tt.included || !eqWei(got.RequiredBaseFee, tt.reqBase) || !eqWei(got.RequiredTip, tt.reqTip) {
				t.Fatalf("got %+v", got)
			}
			if !tt.included {
				if got.Paid() != nil || got.Overpay() != nil || got.PaidBaseFee != nil {
					t.Fatalf("missed bid has paid fields: %+v", got)
				}
				return
			}
			if got.InclusionBlock != tt.inclusion || !eqWei(got.PaidBaseFee, tt.paidBase) || !eqWei(got.PaidTip, tt.paidTip) {
				t.Fatalf("got %+v", got)
			}
			overpay := tt.paidBase + tt.paidTip - tt.reqBase - tt.reqTip
			if !eqWei(got.Overpay(), overpay) {
				t.Fatalf("overpay %s, want %d", got.Overpay(), overpay)
			}
			if math.Abs(got.EstimateError-tt.estimateError) > 1e-12 {
				t.Fatalf("estimate error %v, want %v", got.EstimateError, tt.estimateError)
			}
		})
	}
}

func TestScoreBidZeroPaid(t *testing.T) {
	got, err := score.Bid(bid(0, 0, 3), 0, 1, blocks(1, []int64{0}, []int64{0}))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Included || got.EstimateError != 3 {
		t.Fatalf("got %+v", got)
	}
}

func TestScoreBidErrors(t *testing.T) {
	window := blocks(11, []int64{1, 1}, []int64{0, 0})
	tests := []struct {
		name   string
		bid    gasometer.Bid
		k      int
		window []gasometer.BlockFee
		want   error
	}{
		{"gap: short window", bid(1, 2, 1), 3, window, gasometer.ErrBlockGap},
		{"gap: wrong block", bid(1, 2, 1), 2, append(window[:1:1], blocks(13, []int64{1}, []int64{0})...), gasometer.ErrBlockGap},
		{"gap: window starts late", bid(1, 2, 1), 2, blocks(12, []int64{1, 1}, []int64{0, 0}), gasometer.ErrBlockGap},
		{"zero horizon", bid(1, 2, 1), 0, nil, gasometer.ErrInvalidHorizon},
		{"incomplete bid", gasometer.Bid{Tip: wei(1)}, 2, window, gasometer.ErrInvalidBid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := score.Bid(tt.bid, 10, tt.k, tt.window); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func wei(v int64) *big.Int { return big.NewInt(v) }

func eqWei(a *big.Int, b int64) bool { return a != nil && a.Cmp(wei(b)) == 0 }

// blocks builds contiguous blocks from first with the given base fees and required tips.
func blocks(first uint64, baseFees, requiredTips []int64) []gasometer.BlockFee {
	out := make([]gasometer.BlockFee, len(baseFees))
	for i := range baseFees {
		out[i] = gasometer.BlockFee{
			Number:      first + uint64(i),
			BaseFee:     wei(baseFees[i]),
			RequiredTip: wei(requiredTips[i]),
			MedianTip:   wei(requiredTips[i]),
		}
	}
	return out
}
