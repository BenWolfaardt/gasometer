package rpc

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/ethclient"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/repositories"
)

type evmFeeSource struct {
	client *Client
}

// NewEVMFeeSource reads fee history, the latest header's fee fields and the node's tip.
func NewEVMFeeSource(client *Client) repositories.FeeSource {
	return &evmFeeSource{client: client}
}

func (s *evmFeeSource) FeeHistory(ctx context.Context, blockCount, newest uint64, percentiles []float64) (entities.FeeHistory, error) {
	var out entities.FeeHistory
	err := s.client.Do(ctx, "eth_feeHistory", func(ctx context.Context, c *rpc.Client) error {
		fh, err := ethclient.NewClient(c).FeeHistory(ctx, blockCount, new(big.Int).SetUint64(newest), percentiles)
		if err != nil {
			return err
		}
		out = entities.FeeHistory{
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

func (s *evmFeeSource) HeadFee(ctx context.Context) (entities.HeadFee, error) {
	var h *rawHeader
	err := s.client.Do(ctx, "eth_getBlockByNumber", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &h, "eth_getBlockByNumber", "latest", false)
	})
	if err != nil {
		return entities.HeadFee{}, err
	}
	if h == nil || h.Number == nil || h.BaseFee == nil {
		return entities.HeadFee{}, fmt.Errorf("latest header lacks number or base fee")
	}
	head := entities.HeadFee{
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

func (s *evmFeeSource) NodeTip(ctx context.Context) (*big.Int, error) {
	var tip *big.Int
	err := s.client.Do(ctx, "eth_maxPriorityFeePerGas", func(ctx context.Context, c *rpc.Client) error {
		var err error
		tip, err = ethclient.NewClient(c).SuggestGasTipCap(ctx)
		return err
	})
	return tip, err
}
