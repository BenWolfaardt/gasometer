package probe_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/mock"
	"github.com/BenWolfaardt/gasometer/probe"
)

var params = probe.Params{Network: "cchain", Timeout: 40 * time.Millisecond, PollInterval: 2 * time.Millisecond}

func newCampaign(chain *mock.ProbeChain, store *mock.ProbeStore) *probe.Campaign {
	return probe.NewCampaign(chain, store, slog.New(slog.NewTextHandler(io.Discard, nil)), params)
}

func wantTips() []int64 { return []int64{0, 1, 10, 100, 150, 10_000, 1_000_000, 1_000_000_000} }

func byRung(probes []gasometer.Probe) map[int]gasometer.Probe {
	out := map[int]gasometer.Probe{}
	for _, p := range probes {
		out[p.Rung] = p
	}
	return out
}

func rejected(msg string) error { return fmt.Errorf("%w: %s", gasometer.ErrTxRejected, msg) }

// pendingBelow leaves transactions with a tip under min pending and includes the rest.
func pendingBelow(min int64) func(gasometer.ProbeTx) (*gasometer.Inclusion, error) {
	return func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		if tx.Tip.Cmp(big.NewInt(min)) < 0 {
			return nil, nil
		}
		return &gasometer.Inclusion{Block: 7, TimeMilliseconds: 7_000}, nil
	}
}

func TestRoundSendsEveryRungAtItsTip(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	chain.Latest[3] = 4
	if err := newCampaign(chain, store).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	probes := byRung(store.Probes())
	if len(probes) != len(probe.Ladder) || len(chain.Sent()) != len(probe.Ladder) {
		t.Fatalf("%d probes, %d sent", len(probes), len(chain.Sent()))
	}
	for i, tip := range wantTips() {
		p := probes[i+1]
		wantMax := big.NewInt(2*1_000 + tip)
		if p.Outcome != gasometer.ProbeIncluded || p.Tip.Int64() != tip || p.MaxFee.Cmp(wantMax) != 0 ||
			p.Account != chain.Address(i+1) || p.Network != "cchain" || p.HeadAtSend != 1 || p.TxHash == "" ||
			p.IncludedBlock == nil || *p.IncludedBlock != 1 || *p.IncludedAtMillis != 1_000 || p.SentAt.IsZero() {
			t.Fatalf("rung %d: %+v", i+1, p)
		}
	}
	if probes[3].Nonce != 4 {
		t.Fatalf("rung 3 nonce %d, want the latest 4", probes[3].Nonce)
	}
	for _, tx := range chain.Sent() {
		if tx.From != tx.To || tx.Value.Sign() != 0 {
			t.Fatalf("not a zero-value self-transfer: %+v", tx)
		}
	}
}

func TestRejectedProbeKeepsItsNonce(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	include := chain.Accept
	chain.Accept = func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		if tx.Tip.Sign() == 0 {
			return nil, rejected("transaction underpriced")
		}
		return include(tx)
	}
	c := newCampaign(chain, store)
	for range 2 {
		if err := c.Round(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var zero []gasometer.Probe
	for _, p := range store.Probes() {
		if p.Rung == 1 {
			zero = append(zero, p)
		}
	}
	if len(zero) != 2 {
		t.Fatalf("rung 1 probes %d, want one per round", len(zero))
	}
	for _, p := range zero {
		if p.Outcome != gasometer.ProbeRejected || p.Nonce != 0 || p.TxHash != "" ||
			p.Error != "transaction rejected: transaction underpriced" {
			t.Fatalf("rung 1: %+v", p)
		}
	}
}

func TestStuckProbeIsReplaced(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	chain.Accept = pendingBelow(150)
	if err := newCampaign(chain, store).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	probes := byRung(store.Probes())
	// The node tip plus 10%, and the original's max fee plus 10%, which beats twice the base fee plus that tip.
	wantMaxFee := map[int]int64{1: 2_200, 2: 2_202, 3: 2_211, 4: 2_310}
	for rung := 1; rung <= 4; rung++ {
		p := probes[rung]
		replacement := gasometer.ProbeTx{From: rung, To: rung, Value: new(big.Int), Nonce: 0, Tip: big.NewInt(165),
			MaxFee: big.NewInt(wantMaxFee[rung])}
		if p.Outcome != gasometer.ProbeReplaced || p.ReplacedBy != mock.ProbeTxHash(replacement) || p.IncludedBlock != nil {
			t.Fatalf("rung %d: %+v, want replaced by %s", rung, p, mock.ProbeTxHash(replacement))
		}
	}
	for rung := 5; rung <= 8; rung++ {
		if probes[rung].Outcome != gasometer.ProbeIncluded {
			t.Fatalf("rung %d: %+v", rung, probes[rung])
		}
	}
	if sent := len(chain.Sent()); sent != len(probe.Ladder)+4 {
		t.Fatalf("%d sent, want one per rung and four replacements", sent)
	}
}

func TestReplacementOutbidsAHighTip(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	chain.Accept = func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		if tx.Tip.Cmp(big.NewInt(1_000_000_000)) == 0 {
			return nil, nil
		}
		return &gasometer.Inclusion{Block: 2}, nil
	}
	if err := newCampaign(chain, store).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	var replacement *gasometer.ProbeTx
	for _, tx := range chain.Sent() {
		if tx.From == 8 && tx.Tip.Cmp(big.NewInt(1_000_000_000)) != 0 {
			replacement = &tx
		}
	}
	original := byRung(store.Probes())[8]
	if replacement == nil || replacement.Tip.Cmp(big.NewInt(1_100_000_000)) != 0 || replacement.Nonce != original.Nonce ||
		replacement.MaxFee.Cmp(big.NewInt(1_100_002_200)) != 0 {
		t.Fatalf("replacement %+v", replacement)
	}
	if original.Outcome != gasometer.ProbeReplaced {
		t.Fatalf("original %+v", original)
	}
}

func TestOriginalLandingAfterReplacementCountsAsIncluded(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	var original gasometer.ProbeTx
	chain.Accept = func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		switch {
		case tx.From != 1:
			return &gasometer.Inclusion{Block: 3}, nil
		case tx.Tip.Sign() == 0:
			original = tx
		default:
			chain.Include(original, &gasometer.Inclusion{Block: 9, TimeMilliseconds: 9_500})
		}
		return nil, nil
	}
	if err := newCampaign(chain, store).Round(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := byRung(store.Probes())[1]
	if p.Outcome != gasometer.ProbeIncluded || *p.IncludedBlock != 9 || *p.IncludedAtMillis != 9_500 || p.ReplacedBy != "" {
		t.Fatalf("rung 1: %+v", p)
	}
}

func TestRefusedReplacementLeavesTheRungWaiting(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	chain.Accept = func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		switch {
		case tx.From != 1:
			return &gasometer.Inclusion{Block: 3}, nil
		case tx.Tip.Sign() == 0:
			return nil, nil
		}
		return nil, rejected("replacement transaction underpriced")
	}
	c := newCampaign(chain, store)
	for range 2 {
		if err := c.Round(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	var rung1 []gasometer.Probe
	for _, p := range store.Probes() {
		if p.Rung == 1 {
			rung1 = append(rung1, p)
		}
	}
	if len(rung1) != 1 || rung1[0].Outcome != gasometer.ProbeTimeout {
		t.Fatalf("rung 1 probes %+v, want one timeout and the second round skipped", rung1)
	}
	sentFromRung1 := 0
	for _, tx := range chain.Sent() {
		if tx.From == 1 {
			sentFromRung1++
		}
	}
	if sentFromRung1 != 1 {
		t.Fatalf("rung 1 sent %d, want only the original", sentFromRung1)
	}
}

func TestUnincludedReplacementSkipsTheNextRound(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	c := newCampaign(chain, store)
	chain.Accept = func(tx gasometer.ProbeTx) (*gasometer.Inclusion, error) {
		if tx.From == 1 {
			return nil, nil
		}
		return &gasometer.Inclusion{Block: 3}, nil
	}
	for range 2 {
		if err := c.Round(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	probes := store.Probes()
	if len(probes) != 2*len(probe.Ladder)-1 {
		t.Fatalf("%d probes, want rung 1 skipped in the second round", len(probes))
	}
	if p := byRung(probes)[1]; p.Outcome != gasometer.ProbeReplaced || p.ReplacedBy == "" {
		t.Fatalf("rung 1: %+v", p)
	}
}

func TestRoundReportsStoreAndChainFailures(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{Err: errors.New("disk full")}
	if err := newCampaign(chain, store).Round(t.Context()); err == nil {
		t.Fatal("store failure not reported")
	}
	chain.Err = errors.New("rpc down")
	if err := newCampaign(chain, &mock.ProbeStore{}).Round(t.Context()); err == nil || len(chain.Sent()) != len(probe.Ladder) {
		t.Fatalf("chain failure: %v, sent %d", err, len(chain.Sent()))
	}
}

func TestRunStopsAfterTheDuration(t *testing.T) {
	chain, store := mock.NewProbeChain(), &mock.ProbeStore{}
	start := time.Now()
	if err := newCampaign(chain, store).Run(t.Context(), 5*time.Millisecond, 22*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	rounds := len(store.Probes()) / len(probe.Ladder)
	if rounds < 3 || rounds > 7 || time.Since(start) > time.Second {
		t.Fatalf("%d rounds in %s", rounds, time.Since(start))
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := newCampaign(mock.NewProbeChain(), &mock.ProbeStore{}).Run(ctx, time.Hour, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestFundTopsUpRungsBelowTheTarget(t *testing.T) {
	chain := mock.NewProbeChain()
	chain.Latest[probe.FundingAccount], chain.Pending[probe.FundingAccount] = 3, 5
	chain.Balances[2] = big.NewInt(400)
	chain.Balances[4] = big.NewInt(1_000)
	if err := newCampaign(chain, &mock.ProbeStore{}).Fund(t.Context(), big.NewInt(1_000)); err != nil {
		t.Fatal(err)
	}
	sent := chain.Sent()
	if len(sent) != len(probe.Ladder)-1 {
		t.Fatalf("%d transfers, want every rung but 4", len(sent))
	}
	for i, tx := range sent {
		want := int64(1_000)
		if tx.To == 2 {
			want = 600
		}
		if tx.From != probe.FundingAccount || tx.To == 4 || tx.Value.Int64() != want || tx.Nonce != uint64(5+i) {
			t.Fatalf("transfer %d: %+v", i, tx)
		}
	}
}

func TestSweepReturnsBalancesLessTheFee(t *testing.T) {
	chain := mock.NewProbeChain()
	cost := int64(21_000 * (2*1_000 + 150))
	chain.Balances[1] = big.NewInt(cost + 5)
	chain.Balances[2] = big.NewInt(cost)
	chain.Balances[3] = big.NewInt(1_000_000_000)
	chain.Latest[3], chain.Pending[3] = 2, 3
	chain.Balances[4] = big.NewInt(1_000_000_000)
	chain.Latest[4] = 6
	if err := newCampaign(chain, &mock.ProbeStore{}).Sweep(t.Context()); err != nil {
		t.Fatal(err)
	}
	sent := chain.Sent()
	if len(sent) != 2 || sent[0].From != 1 || sent[0].Value.Int64() != 5 ||
		sent[1].From != 4 || sent[1].Value.Int64() != 1_000_000_000-cost || sent[1].Nonce != 6 {
		t.Fatalf("sweeps %+v, want rung 1's dust over the fee and rung 4; rung 2 too poor, rung 3 pending", sent)
	}
	for _, tx := range sent {
		if tx.To != probe.FundingAccount {
			t.Fatalf("sweep to %d", tx.To)
		}
	}
}

func TestFundFailsWhenATransferIsNotIncluded(t *testing.T) {
	chain := mock.NewProbeChain()
	chain.Accept = func(gasometer.ProbeTx) (*gasometer.Inclusion, error) { return nil, nil }
	if err := newCampaign(chain, &mock.ProbeStore{}).Fund(t.Context(), big.NewInt(1)); err == nil {
		t.Fatal("no error")
	}
}
