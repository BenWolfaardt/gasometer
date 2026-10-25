// Package entities holds gasometer's domain types.
package entities

// NetworkKind selects the fee model a network follows.
type NetworkKind string

const (
	NetworkKindOPStack      NetworkKind = "opstack"
	NetworkKindAvalancheSAE NetworkKind = "avalanche-sae"
)

// Valid reports whether k is a known network kind.
func (k NetworkKind) Valid() bool {
	return k == NetworkKindOPStack || k == NetworkKindAvalancheSAE
}

// Network is one tracked network.
type Network struct {
	Name    string
	ChainID uint64
	Kind    NetworkKind
	Coin    string // native coin symbol, e.g. ETH
}

// HasL1Fee reports whether transactions pay a separate L1 data fee.
func (n Network) HasL1Fee() bool {
	return n.Kind == NetworkKindOPStack
}
