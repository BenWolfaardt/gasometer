package services

import (
	"math/big"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
)

var weiPerCoin = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// TxCosts prices an included bid for each reference transaction in USD, split into base fee, tip and L1 fee. It
// returns nil when the bid missed.
func TxCosts(score entities.Score, snapshot entities.FeeSnapshot, price entities.PriceQuote, txs []entities.ReferenceTx) []entities.TxCost {
	if !score.Included {
		return nil
	}
	usdPerCoin := new(big.Rat).SetFrac(price.Answer, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(price.Decimals)), nil))
	usd := func(perGas *big.Int, gas uint64) float64 {
		w := new(big.Rat).SetInt(new(big.Int).Mul(perGas, new(big.Int).SetUint64(gas)))
		f, _ := w.Mul(w, usdPerCoin).Quo(w, weiPerCoin).Float64()
		return f
	}
	costs := make([]entities.TxCost, 0, len(txs))
	for _, tx := range txs {
		var l1 float64
		if fee, ok := snapshot.L1Fees[tx.Name]; ok && fee != nil {
			l1 = usd(fee, 1)
		}
		costs = append(costs, entities.TxCost{
			Tx:       tx.Name,
			Paid:     entities.CostParts{BaseFee: usd(score.PaidBaseFee, tx.Gas), Tip: usd(score.PaidTip, tx.Gas), L1Fee: l1},
			Required: entities.CostParts{BaseFee: usd(score.RequiredBaseFee, tx.Gas), Tip: usd(score.RequiredTip, tx.Gas), L1Fee: l1},
		})
	}
	return costs
}

// OverpayRatio is overpay ÷ required, or nil when the bid missed or required is zero.
func OverpayRatio(score entities.Score) *float64 {
	overpay, required := score.Overpay(), score.Required()
	if overpay == nil || required.Sign() == 0 {
		return nil
	}
	ratio, _ := new(big.Rat).SetFrac(overpay, required).Float64()
	return &ratio
}
