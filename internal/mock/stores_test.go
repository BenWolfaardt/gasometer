package mock_test

import (
	"testing"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/gasometertest"
	"github.com/BenWolfaardt/gasometer/internal/mock"
)

func TestBlockStoreContract(t *testing.T) {
	gasometertest.BlockStore(t, func(*testing.T) gasometer.BlockStore { return mock.NewBlockStore() })
}

func TestSnapshotStoreContract(t *testing.T) {
	gasometertest.SnapshotStore(t, func(*testing.T) gasometer.SnapshotStore { return mock.NewSnapshotStore() })
}

func TestPredictionStoreContract(t *testing.T) {
	gasometertest.PredictionStore(t, func(*testing.T) (gasometer.SnapshotStore, gasometer.PredictionStore) {
		return mock.NewSnapshotStore(), &mock.PredictionStore{}
	})
}
