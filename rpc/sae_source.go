package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/ava-labs/avalanchego/vms/saevm/cchain/tx"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
)

var _ gasometer.SAESource = (*SAESource)(nil)

// saeBatch is the most blocks per batch call; the public C-Chain endpoint rejects batches over 10.
const saeBatch = 10

// errIncompleteSAEHeader is a block missing a field the SAE gas clock reads.
var errIncompleteSAEHeader = errors.New("incomplete SAE header")

// SAESource reads Avalanche SAE blocks with full transactions, so it can sum their gas limits and parse their
// atomic ops.
type SAESource struct {
	client *Client
}

// NewSAESource returns an SAESource on client.
func NewSAESource(client *Client) *SAESource {
	return &SAESource{client: client}
}

// SAEBlocks implements gasometer.SAESource. A head without settled fields is returned alone.
func (s *SAESource) SAEBlocks(ctx context.Context, head uint64) ([]gasometer.SAEBlock, error) {
	last, err := s.Range(ctx, head, head)
	if err != nil {
		return nil, err
	}
	settled := last[0].Settled
	if settled == nil {
		return last, nil
	}
	if settled.Height >= head {
		return nil, fmt.Errorf("%w: block %d settles %d", gasometer.ErrInvalidSAEBlocks, head, settled.Height)
	}
	blocks, err := s.Range(ctx, settled.Height, head-1)
	if err != nil {
		return nil, err
	}
	return append(blocks, last[0]), nil
}

// Range returns blocks [from, to], ascending, in batches of at most saeBatch.
func (s *SAESource) Range(ctx context.Context, from, to uint64) ([]gasometer.SAEBlock, error) {
	if from > to {
		return nil, fmt.Errorf("%w: range %d to %d", gasometer.ErrInvalidSAEBlocks, from, to)
	}
	out := make([]gasometer.SAEBlock, 0, to-from+1)
	for start := from; start <= to; start += saeBatch {
		end := min(start+saeBatch-1, to)
		raw, err := s.batch(ctx, start, end)
		if err != nil {
			return nil, err
		}
		for i, r := range raw {
			b, err := r.decode(start + uint64(i))
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *SAESource) batch(ctx context.Context, from, to uint64) ([]*rawSAEBlock, error) {
	raw := make([]*rawSAEBlock, to-from+1)
	err := s.client.Do(ctx, "eth_getBlockByNumber", func(ctx context.Context, c *rpc.Client) error {
		elems := make([]rpc.BatchElem, len(raw))
		for i := range raw {
			raw[i] = nil
			elems[i] = rpc.BatchElem{
				Method: "eth_getBlockByNumber",
				Args:   []any{hexutil.EncodeUint64(from + uint64(i)), true},
				Result: &raw[i],
			}
		}
		if err := c.BatchCallContext(ctx, elems); err != nil {
			return err
		}
		for i, e := range elems {
			if e.Error != nil {
				return fmt.Errorf("block %d: %w", from+uint64(i), e.Error)
			}
		}
		return nil
	})
	return raw, err
}

// rawSAEBlock holds the block fields the SAE gas clock reads; libevm's types.Header has no SAE fields.
type rawSAEBlock struct {
	Number                *hexutil.Uint64 `json:"number"`
	Timestamp             hexutil.Uint64  `json:"timestamp"`
	TimestampMilliseconds *hexutil.Uint64 `json:"timestampMilliseconds"`
	BaseFee               *hexutil.Big    `json:"baseFeePerGas"`
	SettledHeight         *hexutil.Uint64 `json:"settledHeight"`
	SettledGasUnix        *hexutil.Uint64 `json:"settledGasUnix"`
	SettledGasNumerator   *hexutil.Uint64 `json:"settledGasNumerator"`
	SettledExcess         *hexutil.Uint64 `json:"settledExcess"`
	TargetExponent        *hexutil.Uint64 `json:"targetExponent"`
	MinPriceExponent      *hexutil.Uint64 `json:"minPriceExponent"`
	MinDelayExcess        *hexutil.Uint64 `json:"minDelayExcess"`
	BlockExtraData        hexutil.Bytes   `json:"blockExtraData"`
	Transactions          []struct {
		Gas hexutil.Uint64 `json:"gas"`
	} `json:"transactions"`
}

func (r *rawSAEBlock) decode(want uint64) (gasometer.SAEBlock, error) {
	switch {
	case r == nil:
		return gasometer.SAEBlock{}, fmt.Errorf("block %d not found", want)
	case r.Number == nil || uint64(*r.Number) != want:
		return gasometer.SAEBlock{}, fmt.Errorf("asked for block %d, got %v", want, r.Number)
	case r.BaseFee == nil || r.TimestampMilliseconds == nil || r.TargetExponent == nil ||
		r.MinPriceExponent == nil || r.MinDelayExcess == nil:
		return gasometer.SAEBlock{}, fmt.Errorf("%w: block %d", errIncompleteSAEHeader, want)
	}
	b := gasometer.SAEBlock{
		Number:           want,
		Time:             uint64(r.Timestamp),
		TimeMilliseconds: uint64(*r.TimestampMilliseconds),
		BaseFee:          r.BaseFee.ToInt(),
		TargetExponent:   uint64(*r.TargetExponent),
		MinPriceExponent: uint64(*r.MinPriceExponent),
		MinDelayExcess:   uint64(*r.MinDelayExcess),
	}
	settled, err := r.settled()
	if err != nil {
		return gasometer.SAEBlock{}, fmt.Errorf("block %d: %w", want, err)
	}
	b.Settled = settled
	for _, t := range r.Transactions {
		b.TxGasLimit += uint64(t.Gas)
	}
	if b.AtomicGas, err = atomicGas(r.BlockExtraData); err != nil {
		return gasometer.SAEBlock{}, fmt.Errorf("block %d atomic txs: %w", want, err)
	}
	return b, nil
}

// settled is nil when every settled field is absent, as on a synchronous block.
func (r *rawSAEBlock) settled() (*gasometer.SAESettled, error) {
	fields := []*hexutil.Uint64{r.SettledHeight, r.SettledGasUnix, r.SettledGasNumerator, r.SettledExcess}
	present := 0
	for _, f := range fields {
		if f != nil {
			present++
		}
	}
	switch present {
	case 0:
		return nil, nil
	case len(fields):
		return &gasometer.SAESettled{
			Height:       uint64(*r.SettledHeight),
			GasUnix:      uint64(*r.SettledGasUnix),
			GasNumerator: uint64(*r.SettledGasNumerator),
			Excess:       uint64(*r.SettledExcess),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %d of %d settled fields", errIncompleteSAEHeader, present, len(fields))
	}
}

// atomicGas is the gas of the atomic ops in extData, in the slice encoding every block since Apricot Phase 5 uses.
func atomicGas(extData []byte) (uint64, error) {
	txs, err := tx.ParseSlice(extData)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, t := range txs {
		g, err := tx.GasUsed(t.Unsigned)
		if err != nil {
			return 0, err
		}
		total += uint64(g)
	}
	return total, nil
}
