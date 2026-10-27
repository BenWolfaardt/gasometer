//go:build live

package rpc_test

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ava-labs/libevm/common/hexutil"
	librpc "github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer/rpc"
	"github.com/BenWolfaardt/gasometer/sae"
)

const feeHistorySamples = 200

// feeHistoryCounts classifies eth_feeHistory(1, E)'s last baseFeePerGas, E being the latest block, by which branch
// the probes say ran and which value it matched.
type feeHistoryCounts struct {
	tip      int // E was the last accepted block before the call: the bound branch
	lag      int // E+1 was already accepted where the batch ran: the successor branch
	coincide int // of those, samples where the bound and E+1's header fee are the same number
	wrong    int // the value is not the one the branch serves
	skipped  int // a request reached a backend that did not have E, or a call failed

	// One batch of eth_blockNumber, eth_feeHistory(1, "latest") and eth_baseFee, so one node answers all three.
	oneNode          int
	oneNodeSuccessor int // eth_feeHistory's next differed from eth_baseFee: that node's latest trailed its accepted tip
}

// TestLiveFeeHistorySAE samples which of its two values eth_feeHistory's next base fee serves on C-Chain, the way the
// collector calls it: E from eth_getBlockByNumber("latest"), then eth_feeHistory(1, E) in a second request, which a
// load balancer may send to another node. A block number above the last accepted one fails with "not accepted yet",
// so probing E+1 in the same batch, which a node runs in order, says which branch that node took. A third request
// asks one node for all three of its latest height, eth_feeHistory(1, "latest") and eth_baseFee.
func TestLiveFeeHistorySAE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client, err := rpc.NewClient(ctx, "cchain", rpc.Config{
		URLs: []string{"https://api.avax.network/ext/bc/C/rpc"}, RateLimit: 5, Burst: 1, Timeout: 20 * time.Second,
		MaxRetries: 3, InitialBackoff: time.Second,
	}, &recordingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	fees := rpc.NewFeeSource(client, rpc.FeeSourceLimits{HistoryDepth: 1, Batch: 1})
	blocks := rpc.NewSAESource(client)
	var counts feeHistoryCounts
	for range feeHistorySamples {
		head, err := fees.HeadFee(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s, err := sampleFeeHistory(ctx, client, head.Number)
		if err != nil {
			counts.skipped++
			continue
		}
		window, err := blocks.SAEBlocks(ctx, head.Number)
		if err != nil {
			counts.skipped++
			continue
		}
		bound, err := sae.NextBaseFee(window)
		if err != nil {
			t.Fatal(err)
		}
		classify(ctx, t, &counts, s, bound, blocks, head.Number)
		if successor, err := oneNodeFeeHistory(ctx, client); err == nil {
			counts.oneNode++
			if successor {
				counts.oneNodeSuccessor++
			}
		}
		time.Sleep(time.Second)
	}
	t.Logf("eth_feeHistory next base fee over %d samples: E was the accepted tip %d, E+1 already accepted %d, "+
		"bound equal to E+1's header fee %d, value not the branch's %d, skipped %d",
		feeHistorySamples, counts.tip, counts.lag, counts.coincide, counts.wrong, counts.skipped)
	t.Logf("one node, one batch: eth_feeHistory(1, latest)'s next differs from eth_baseFee in %d of %d",
		counts.oneNodeSuccessor, counts.oneNode)
	if counts.wrong > 0 {
		t.Errorf("%d samples served a value their branch does not", counts.wrong)
	}
}

// oneNodeFeeHistory is whether eth_feeHistory(1, "latest")'s next base fee differs from eth_baseFee, both answered by
// one node in one batch.
func oneNodeFeeHistory(ctx context.Context, client *rpc.Client) (bool, error) {
	var history struct {
		BaseFee []*hexutil.Big `json:"baseFeePerGas"`
	}
	var height hexutil.Uint64
	var baseFee hexutil.Big
	elems := []librpc.BatchElem{
		{Method: "eth_blockNumber", Result: &height},
		{Method: "eth_feeHistory", Args: []any{hexutil.EncodeUint64(1), "latest", []float64{}}, Result: &history},
		{Method: "eth_baseFee", Result: &baseFee},
	}
	err := client.Do(ctx, "batch", func(ctx context.Context, c *librpc.Client) error {
		return c.BatchCallContext(ctx, elems)
	})
	if err != nil {
		return false, err
	}
	for _, e := range elems {
		if e.Error != nil {
			return false, e.Error
		}
	}
	if len(history.BaseFee) != 2 {
		return false, fmt.Errorf("eth_feeHistory(1) returned %d base fees", len(history.BaseFee))
	}
	return history.BaseFee[1].ToInt().Cmp(baseFee.ToInt()) != 0, nil
}

type feeHistorySample struct {
	next           *big.Int
	acceptedBefore bool     // E+1 resolved before eth_feeHistory
	successorFee   *big.Int // E+1's header fee when either probe found it
}

type probedHeader struct {
	BaseFee *hexutil.Big `json:"baseFeePerGas"`
}

// sampleFeeHistory runs eth_getBlockByNumber(E+1), eth_feeHistory(1, E) and eth_getBlockByNumber(E+1) in one batch.
func sampleFeeHistory(ctx context.Context, client *rpc.Client, e uint64) (feeHistorySample, error) {
	var before, after *probedHeader
	var history struct {
		BaseFee []*hexutil.Big `json:"baseFeePerGas"`
	}
	next := hexutil.EncodeUint64(e + 1)
	elems := []librpc.BatchElem{
		{Method: "eth_getBlockByNumber", Args: []any{next, false}, Result: &before},
		{Method: "eth_feeHistory", Args: []any{hexutil.EncodeUint64(1), hexutil.EncodeUint64(e), []float64{}}, Result: &history},
		{Method: "eth_getBlockByNumber", Args: []any{next, false}, Result: &after},
	}
	err := client.Do(ctx, "batch", func(ctx context.Context, c *librpc.Client) error {
		return c.BatchCallContext(ctx, elems)
	})
	if err != nil {
		return feeHistorySample{}, err
	}
	for _, i := range []int{0, 2} {
		if err := elems[i].Error; err != nil && !strings.Contains(err.Error(), "not accepted yet") {
			return feeHistorySample{}, err
		}
	}
	if elems[1].Error != nil {
		return feeHistorySample{}, elems[1].Error
	}
	if len(history.BaseFee) != 2 {
		return feeHistorySample{}, fmt.Errorf("eth_feeHistory(1) returned %d base fees", len(history.BaseFee))
	}
	s := feeHistorySample{
		next:           history.BaseFee[1].ToInt(),
		acceptedBefore: elems[0].Error == nil && before != nil,
	}
	for _, h := range []*probedHeader{before, after} {
		if h != nil && h.BaseFee != nil {
			s.successorFee = h.BaseFee.ToInt()
		}
	}
	return s, nil
}

// classify counts s by branch. A block accepted mid-call puts the bound branch's sample in wrong, unless the two
// values coincide.
func classify(ctx context.Context, t *testing.T, counts *feeHistoryCounts, s feeHistorySample, bound *big.Int,
	blocks *rpc.SAESource, e uint64) {
	t.Helper()
	if s.successorFee == nil {
		successor, err := blocks.Range(ctx, e+1, e+1)
		if err != nil {
			t.Fatal(err)
		}
		s.successorFee = successor[0].BaseFee
	}
	want := bound
	if s.acceptedBefore {
		counts.lag++
		want = s.successorFee
	} else {
		counts.tip++
	}
	if bound.Cmp(s.successorFee) == 0 {
		counts.coincide++
	}
	if s.next.Cmp(want) != 0 {
		counts.wrong++
		t.Logf("block %d: next %s, bound %s, block %d's header %s, accepted before %t",
			e, s.next, bound, e+1, s.successorFee, s.acceptedBefore)
	}
}
