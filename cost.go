package gasometer

// ReferenceTx is a representative transaction that costs are quoted for.
type ReferenceTx struct {
	Name     string
	Gas      uint64
	DataSize int // calldata bytes, drives the L1 data fee
}

// CostParts splits a transaction's cost in USD.
type CostParts struct {
	BaseFee float64
	Tip     float64
	L1Fee   float64
}

// Total is the sum of the parts.
func (c CostParts) Total() float64 {
	return c.BaseFee + c.Tip + c.L1Fee
}

// TxCost is what a resolved bid cost for one reference transaction, in USD.
type TxCost struct {
	Tx       string
	Paid     CostParts
	Required CostParts
}

// Overpay is paid minus required, part by part so a shared L1 fee cancels exactly.
func (c TxCost) Overpay() float64 {
	return (c.Paid.BaseFee - c.Required.BaseFee) + (c.Paid.Tip - c.Required.Tip) + (c.Paid.L1Fee - c.Required.L1Fee)
}
