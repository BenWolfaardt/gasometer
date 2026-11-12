package mock_test

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer/internal/gasometer"
	"github.com/BenWolfaardt/gasometer/internal/gasometer/gasometertest"
	"github.com/BenWolfaardt/gasometer/internal/mock"
)

func TestMetricsRecorderConcurrentCallers(t *testing.T) {
	m := mock.NewMetricsRecorder()
	gasometertest.Concurrently(t, 8, func(i int) {
		network := fmt.Sprintf("net-%d", i%2)
		m.Settled(gasometer.Settlement{Network: network, Predictor: "node", Outcome: gasometer.OutcomeMissed})
		m.Snapshot(network, gasometer.FeeSnapshot{NextBaseFee: big.NewInt(1), NodeTip: big.NewInt(1)},
			gasometer.BlockFee{BaseFee: big.NewInt(1), RequiredTip: big.NewInt(1)}, nil)
		m.ModelMismatch(network)
		m.BlocksBackfilled(network, 2)
		m.BlocksLost(network, 3)
		m.PollCompleted(network, time.Second, time.Unix(1_800_000_000, 0))
	})
	for _, network := range []string{"net-0", "net-1"} {
		if m.Mismatches[network] != 4 || m.Backfilled[network] != 8 || m.Lost[network] != 12 || m.PollCount(network) != 4 {
			t.Errorf("%s: mismatches %d, backfilled %d, lost %d, polls %d", network,
				m.Mismatches[network], m.Backfilled[network], m.Lost[network], m.PollCount(network))
		}
	}
	if len(m.Settlements) != 8 || len(m.Snapshots) != 8 {
		t.Fatalf("%d settlements, %d snapshots", len(m.Settlements), len(m.Snapshots))
	}
}
