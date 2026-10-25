package entities

// ReferenceTx is a representative transaction that costs are quoted for.
type ReferenceTx struct {
	Name     string
	Gas      uint64
	DataSize int // calldata bytes, drives the L1 data fee
}
