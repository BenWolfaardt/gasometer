package score_test

import (
	"testing"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/score"
)

func TestTxCostsMissedIsNil(t *testing.T) {
	s := gasometer.Score{RequiredBaseFee: wei(1), RequiredTip: wei(1)}
	if got := score.TxCosts(s, gasometer.FeeSnapshot{}, gasometer.PriceQuote{Answer: wei(1)}, nil); got != nil {
		t.Fatalf("got %+v", got)
	}
	if score.OverpayRatio(s) != nil {
		t.Fatal("missed bid has an overpay ratio")
	}
}

func TestOverpayRatioZeroRequired(t *testing.T) {
	s := gasometer.Score{Included: true, PaidBaseFee: wei(0), PaidTip: wei(3), RequiredBaseFee: wei(0), RequiredTip: wei(0)}
	if score.OverpayRatio(s) != nil {
		t.Fatal("zero required must have no ratio")
	}
}

func TestTxCostsWithoutL1Fee(t *testing.T) {
	s := gasometer.Score{Included: true, PaidBaseFee: wei(1e9), PaidTip: wei(1e9), RequiredBaseFee: wei(1e9), RequiredTip: wei(0)}
	price := gasometer.PriceQuote{Answer: wei(25_00000000), Decimals: 8}
	got := score.TxCosts(s, gasometer.FeeSnapshot{}, price, []gasometer.ReferenceTx{{Name: "native", Gas: 21_000}})
	// 21,000 gas × 1 gwei = 2.1e-5 AVAX, at 25 USD.
	if len(got) != 1 || got[0].Paid.BaseFee != 25*2.1e-5 || got[0].Paid.L1Fee != 0 || got[0].Required.Tip != 0 {
		t.Fatalf("got %+v", got)
	}
}
