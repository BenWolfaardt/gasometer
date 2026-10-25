package rpc

import (
	"context"
	"fmt"
	"math/big"

	ethereum "github.com/ava-labs/libevm"
	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/ethclient"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
)

// GasPriceOracle is the OP Stack predeploy that prices L1 data.
var GasPriceOracle = common.HexToAddress("0x420000000000000000000000000000000000000F")

var (
	selectorGetL1Fee   = crypto.Keccak256([]byte("getL1Fee(bytes)"))[:4]
	selectorTransfer   = crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]
	referenceTo        = common.HexToAddress("0x5a0b54d5dc17e0aadc383d2db43b0a0d3e029c4c")
	referenceRecipient = common.HexToAddress("0x8f2a1e6b9c7d4e3f5a0b1c2d3e4f5a6b7c8d9e0f")
)

var _ gasometer.L1FeeSource = (*L1FeeSource)(nil)

// L1FeeSource reads GasPriceOracle.getL1Fee for representative transactions.
type L1FeeSource struct {
	client  *Client
	chainID *big.Int
}

// NewL1FeeSource returns an L1 fee source on an OP Stack network's client.
func NewL1FeeSource(client *Client, chainID uint64) *L1FeeSource {
	return &L1FeeSource{client: client, chainID: new(big.Int).SetUint64(chainID)}
}

// L1Fees implements gasometer.L1FeeSource.
func (s *L1FeeSource) L1Fees(ctx context.Context, txs []gasometer.ReferenceTx) (map[string]*big.Int, error) {
	fees := make(map[string]*big.Int, len(txs))
	for _, tx := range txs {
		encoded, err := EncodeReferenceTx(s.chainID, tx)
		if err != nil {
			return nil, err
		}
		var out []byte
		err = s.client.Do(ctx, "eth_call", func(ctx context.Context, c *rpc.Client) error {
			out, err = ethclient.NewClient(c).CallContract(ctx, ethereum.CallMsg{
				To: &GasPriceOracle, Data: encodeBytesCall(selectorGetL1Fee, encoded),
			}, nil)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("getL1Fee for %s: %w", tx.Name, err)
		}
		if len(out) != 32 {
			return nil, fmt.Errorf("getL1Fee for %s returned %d bytes", tx.Name, len(out))
		}
		fees[tx.Name] = new(big.Int).SetBytes(out)
	}
	return fees, nil
}

// EncodeReferenceTx is an unsigned EIP-1559 transaction of tx's gas, with calldata shaped like an ERC-20 transfer
// and sized to tx.DataSize. The oracle adds the signature's size itself.
func EncodeReferenceTx(chainID *big.Int, tx gasometer.ReferenceTx) ([]byte, error) {
	value := big.NewInt(0)
	if tx.DataSize == 0 {
		value = big.NewInt(10_000_000_000_000_000)
	}
	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1_234,
		GasTipCap: big.NewInt(1_000_000),
		GasFeeCap: big.NewInt(1_000_000_000),
		Gas:       tx.Gas,
		To:        &referenceTo,
		Value:     value,
		Data:      referenceCalldata(tx.DataSize),
	}).MarshalBinary()
}

func referenceCalldata(size int) []byte {
	if size == 0 {
		return nil
	}
	data := append([]byte{}, selectorTransfer...)
	data = append(data, common.LeftPadBytes(referenceRecipient.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(big.NewInt(1_500_000_000).Bytes(), 32)...)
	if len(data) >= size {
		return data[:size]
	}
	return append(data, make([]byte, size-len(data))...)
}

// encodeBytesCall ABI-encodes a call to a function taking one bytes argument.
func encodeBytesCall(selector, arg []byte) []byte {
	out := append([]byte{}, selector...)
	out = append(out, common.LeftPadBytes(big.NewInt(32).Bytes(), 32)...)
	out = append(out, common.LeftPadBytes(big.NewInt(int64(len(arg))).Bytes(), 32)...)
	out = append(out, arg...)
	if pad := len(arg) % 32; pad != 0 {
		out = append(out, make([]byte, 32-pad)...)
	}
	return out
}
