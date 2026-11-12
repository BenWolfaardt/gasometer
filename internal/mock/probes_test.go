package mock_test

import (
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/gasometertest"
	"github.com/BenWolfaardt/gasometer/internal/mock"
)

func TestProbeStoreContract(t *testing.T) {
	gasometertest.ProbeStore(t, func(*testing.T) (gasometer.ProbeStore, func() int) {
		store := &mock.ProbeStore{}
		return store, func() int { return len(store.Probes()) }
	})
}

func TestProbeChainContract(t *testing.T) {
	gasometertest.ProbeChain(t, func(*testing.T) gasometer.ProbeChain { return mock.NewProbeChain() })
}
