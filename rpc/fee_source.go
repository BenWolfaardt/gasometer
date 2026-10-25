package rpc

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/ethclient"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
)

var _ gasometer.FeeSource = (*FeeSource)(nil)

// FeeSourceLimits are what an endpoint serves through eth_feeHistory.
type FeeSourceLimits struct {
	HistoryDepth uint64 // blocks behind the requested end the node still serves
	Batch        uint64 // most blocks one call returns
}

// FeeSource reads fee history, the latest header's fee fields and the node's tip.
type FeeSource struct {
	client *Client
	limits FeeSourceLimits
}

// NewFeeSource returns a FeeSource on client within limits.
func NewFeeSource(client *Client, limits FeeSourceLimits) *FeeSource {
	limits.HistoryDepth, limits.Batch = max(1, limits.HistoryDepth), max(1, limits.Batch)
	return &FeeSource{client: client, limits: limits}
}

// FeeHistory implements gasometer.FeeSource. It starts no earlier than the history depth allows and splits the
// range into calls of at most Batch blocks.
func (s *FeeSource) FeeHistory(ctx context.Context, from, to uint64, percentiles []float64) (gasometer.FeeHistory, error) {
	if to+1 > s.limits.HistoryDepth {
		from = max(from, to+1-s.limits.HistoryDepth)
	}
	if from > to {
		return gasometer.FeeHistory{}, fmt.Errorf("%w: range %d to %d", gasometer.ErrMalformedFeeHistory, from, to)
	}
	out := gasometer.FeeHistory{OldestBlock: from}
	for start := from; start <= to; start += s.limits.Batch {
		end := min(start+s.limits.Batch-1, to)
		batch, err := s.feeHistory(ctx, end-start+1, end, percentiles)
		if err != nil {
			return gasometer.FeeHistory{}, err
		}
		count := end - start + 1
		if batch.OldestBlock != start || uint64(batch.Len()) != count || len(batch.BaseFees) != batch.Len()+1 {
			return gasometer.FeeHistory{}, fmt.Errorf("%w: asked for %d blocks from %d, got %d from %d",
				gasometer.ErrMalformedFeeHistory, count, start, batch.Len(), batch.OldestBlock)
		}
		// Each batch's last base fee is the next batch's first; keep it only from the final batch.
		out.BaseFees = append(out.BaseFees, batch.BaseFees[:batch.Len()]...)
		out.GasUsedRatios = append(out.GasUsedRatios, batch.GasUsedRatios...)
		out.Rewards = append(out.Rewards, padRewards(batch.Rewards, batch.Len())...)
		if end == to {
			out.BaseFees = append(out.BaseFees, batch.NextBaseFee())
		}
	}
	return out, nil
}

// padRewards keeps rewards aligned with blocks when a node returns fewer rows than blocks.
func padRewards(rewards [][]*big.Int, n int) [][]*big.Int {
	for len(rewards) < n {
		rewards = append(rewards, nil)
	}
	return rewards[:n]
}

func (s *FeeSource) feeHistory(ctx context.Context, blockCount, newest uint64, percentiles []float64) (gasometer.FeeHistory, error) {
	var out gasometer.FeeHistory
	err := s.client.Do(ctx, "eth_feeHistory", func(ctx context.Context, c *rpc.Client) error {
		fh, err := ethclient.NewClient(c).FeeHistory(ctx, blockCount, new(big.Int).SetUint64(newest), percentiles)
		if err != nil {
			return err
		}
		out = gasometer.FeeHistory{
			OldestBlock:   fh.OldestBlock.Uint64(),
			BaseFees:      fh.BaseFee,
			GasUsedRatios: fh.GasUsedRatio,
			Rewards:       fh.Reward,
		}
		return nil
	})
	return out, err
}

// rawHeader holds the header fields the fee model reads; libevm's types.Header has no OP Stack or SAE fields.
type rawHeader struct {
	Number      *hexutil.Big    `json:"number"`
	BaseFee     *hexutil.Big    `json:"baseFeePerGas"`
	GasUsed     hexutil.Uint64  `json:"gasUsed"`
	BlobGasUsed *hexutil.Uint64 `json:"blobGasUsed"`
	GasLimit    hexutil.Uint64  `json:"gasLimit"`
	ExtraData   hexutil.Bytes   `json:"extraData"`
}

// HeadFee implements gasometer.FeeSource.
func (s *FeeSource) HeadFee(ctx context.Context) (gasometer.HeadFee, error) {
	var h *rawHeader
	err := s.client.Do(ctx, "eth_getBlockByNumber", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &h, "eth_getBlockByNumber", "latest", false)
	})
	if err != nil {
		return gasometer.HeadFee{}, err
	}
	if h == nil || h.Number == nil || h.BaseFee == nil {
		return gasometer.HeadFee{}, fmt.Errorf("latest header lacks number or base fee")
	}
	head := gasometer.HeadFee{
		Number:    h.Number.ToInt().Uint64(),
		BaseFee:   h.BaseFee.ToInt(),
		GasUsed:   uint64(h.GasUsed),
		GasLimit:  uint64(h.GasLimit),
		ExtraData: h.ExtraData,
	}
	if h.BlobGasUsed != nil {
		head.BlobGasUsed = uint64(*h.BlobGasUsed)
	}
	return head, nil
}

// NodeTip implements gasometer.FeeSource.
func (s *FeeSource) NodeTip(ctx context.Context) (*big.Int, error) {
	var tip *big.Int
	err := s.client.Do(ctx, "eth_maxPriorityFeePerGas", func(ctx context.Context, c *rpc.Client) error {
		var err error
		tip, err = ethclient.NewClient(c).SuggestGasTipCap(ctx)
		return err
	})
	return tip, err
}
