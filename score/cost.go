package score

import (
	"math/big"

	"github.com/BenWolfaardt/gasometer"
)

var weiPerCoin = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// TxCosts prices an included bid for each reference transaction in USD, split into base fee, tip and L1 fee. It
// returns nil when the bid missed.
func TxCosts(score gasometer.Score, snapshot gasometer.FeeSnapshot, price gasometer.PriceQuote, txs []gasometer.ReferenceTx) []gasometer.TxCost {
	if !score.Included {
		return nil
	}
	usdPerCoin := new(big.Rat).SetFrac(price.Answer, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(price.Decimals)), nil))
	usd := func(perGas *big.Int, gas uint64) float64 {
		w := new(big.Rat).SetInt(new(big.Int).Mul(perGas, new(big.Int).SetUint64(gas)))
		f, _ := w.Mul(w, usdPerCoin).Quo(w, weiPerCoin).Float64()
		return f
	}
	costs := make([]gasometer.TxCost, 0, len(txs))
	for _, tx := range txs {
		var l1 float64
		if fee, ok := snapshot.L1Fees[tx.Name]; ok && fee != nil {
			l1 = usd(fee, 1)
		}
		costs = append(costs, gasometer.TxCost{
			Tx:       tx.Name,
			Paid:     gasometer.CostParts{BaseFee: usd(score.PaidBaseFee, tx.Gas), Tip: usd(score.PaidTip, tx.Gas), L1Fee: l1},
			Required: gasometer.CostParts{BaseFee: usd(score.RequiredBaseFee, tx.Gas), Tip: usd(score.RequiredTip, tx.Gas), L1Fee: l1},
		})
	}
	return costs
}

// OverpayRatio is overpay ÷ required, or nil when the bid missed or required is zero.
func OverpayRatio(score gasometer.Score) *float64 {
	overpay, required := score.Overpay(), score.Required()
	if overpay == nil || required.Sign() == 0 {
		return nil
	}
	ratio, _ := new(big.Rat).SetFrac(overpay, required).Float64()
	return &ratio
}
