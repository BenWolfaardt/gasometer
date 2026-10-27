package rpc

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
)

var _ gasometer.ProbeChain = (*TxSender)(nil)

// SeedLength is the probe seed's length in bytes.
const SeedLength = 32

// transferGas is a plain transfer's gas.
const transferGas = 21_000

// A retried send meets one of these when an earlier attempt reached the node: it already holds the transaction, or
// the transaction is included and its nonce used. A probe account sends nothing else at that nonce, so either means
// this transaction was sent, and the receipt lookup says what became of it.
const (
	alreadyKnown = "already known"
	nonceTooLow  = "nonce too low"
)

// TxSender signs and sends transfers between accounts derived from one seed, and reads their state.
type TxSender struct {
	client *Client
	signer types.Signer
	keys   []*ecdsa.PrivateKey
}

// NewTxSender derives accounts 0 to accounts-1 from seed: account i's key is keccak256(seed ‖ i), i as 8 bytes big
// endian.
func NewTxSender(client *Client, chainID *big.Int, seed []byte, accounts int) (*TxSender, error) {
	if len(seed) != SeedLength {
		return nil, fmt.Errorf("seed is %d bytes, want %d", len(seed), SeedLength)
	}
	s := &TxSender{client: client, signer: types.LatestSignerForChainID(chainID)}
	for i := range accounts {
		var index [8]byte
		binary.BigEndian.PutUint64(index[:], uint64(i))
		key, err := crypto.ToECDSA(crypto.Keccak256(seed, index[:]))
		if err != nil {
			return nil, fmt.Errorf("account %d key: %w", i, err)
		}
		s.keys = append(s.keys, key)
	}
	return s, nil
}

// Address implements gasometer.ProbeChain.
func (s *TxSender) Address(account int) string {
	return s.address(account).Hex()
}

func (s *TxSender) address(account int) common.Address {
	return crypto.PubkeyToAddress(s.keys[account].PublicKey)
}

// Nonces implements gasometer.ProbeChain. Both counts come from one batch, so one backend answers them.
func (s *TxSender) Nonces(ctx context.Context, account int) (uint64, uint64, error) {
	var latest, pending hexutil.Uint64
	addr := s.address(account)
	err := s.client.Do(ctx, "eth_getTransactionCount", func(ctx context.Context, c *rpc.Client) error {
		elems := []rpc.BatchElem{
			{Method: "eth_getTransactionCount", Args: []any{addr, "latest"}, Result: &latest},
			{Method: "eth_getTransactionCount", Args: []any{addr, "pending"}, Result: &pending},
		}
		if err := c.BatchCallContext(ctx, elems); err != nil {
			return err
		}
		return errors.Join(elems[0].Error, elems[1].Error)
	})
	return uint64(latest), uint64(pending), err
}

// Balance implements gasometer.ProbeChain.
func (s *TxSender) Balance(ctx context.Context, account int) (*big.Int, error) {
	var balance hexutil.Big
	err := s.client.Do(ctx, "eth_getBalance", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &balance, "eth_getBalance", s.address(account), "latest")
	})
	return balance.ToInt(), err
}

// BaseFee implements gasometer.ProbeChain with eth_baseFee, the last accepted block's bound on the next base fee.
func (s *TxSender) BaseFee(ctx context.Context) (*big.Int, error) {
	return s.callBig(ctx, "eth_baseFee")
}

// NodeTip implements gasometer.ProbeChain.
func (s *TxSender) NodeTip(ctx context.Context) (*big.Int, error) {
	return s.callBig(ctx, "eth_maxPriorityFeePerGas")
}

func (s *TxSender) callBig(ctx context.Context, method string) (*big.Int, error) {
	var v hexutil.Big
	err := s.client.Do(ctx, method, func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &v, method)
	})
	return v.ToInt(), err
}

// Head implements gasometer.ProbeChain.
func (s *TxSender) Head(ctx context.Context) (uint64, error) {
	var n hexutil.Uint64
	err := s.client.Do(ctx, "eth_blockNumber", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &n, "eth_blockNumber")
	})
	return uint64(n), err
}

// Send implements gasometer.ProbeChain. A node already holding the transaction or its nonce counts as sent; any other
// JSON-RPC error is a refusal.
func (s *TxSender) Send(ctx context.Context, ptx gasometer.ProbeTx) (string, error) {
	to := s.address(ptx.To)
	tx, err := types.SignNewTx(s.keys[ptx.From], s.signer, &types.DynamicFeeTx{
		ChainID:   s.signer.ChainID(),
		Nonce:     ptx.Nonce,
		GasTipCap: ptx.Tip,
		GasFeeCap: ptx.MaxFee,
		Gas:       transferGas,
		To:        &to,
		Value:     ptx.Value,
	})
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	err = s.client.Do(ctx, "eth_sendRawTransaction", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, nil, "eth_sendRawTransaction", hexutil.Bytes(raw))
	})
	var rpcErr rpc.Error
	switch {
	case err == nil:
	case errors.As(err, &rpcErr) && (strings.Contains(rpcErr.Error(), alreadyKnown) ||
		strings.Contains(rpcErr.Error(), nonceTooLow)):
	case errors.As(err, &rpcErr):
		return "", fmt.Errorf("%w: %s", gasometer.ErrTxRejected, rpcErr.Error())
	default:
		return "", err
	}
	return tx.Hash().Hex(), nil
}

// Inclusion implements gasometer.ProbeChain.
func (s *TxSender) Inclusion(ctx context.Context, hash string) (*gasometer.Inclusion, error) {
	var receipt *struct {
		BlockNumber hexutil.Uint64 `json:"blockNumber"`
	}
	err := s.client.Do(ctx, "eth_getTransactionReceipt", func(ctx context.Context, c *rpc.Client) error {
		return c.CallContext(ctx, &receipt, "eth_getTransactionReceipt", common.HexToHash(hash))
	})
	if err != nil || receipt == nil {
		return nil, err
	}
	var block *struct {
		TimestampMilliseconds *hexutil.Uint64 `json:"timestampMilliseconds"`
	}
	number := hexutil.EncodeUint64(uint64(receipt.BlockNumber))
	err = s.client.Do(ctx, "eth_getBlockByNumber", func(ctx context.Context, c *rpc.Client) error {
		if err := c.CallContext(ctx, &block, "eth_getBlockByNumber", number, false); err != nil {
			return err
		}
		if block == nil {
			return fmt.Errorf("block %s: %w", number, errBehindHead)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if block.TimestampMilliseconds == nil {
		return nil, fmt.Errorf("block %s has no timestampMilliseconds", number)
	}
	return &gasometer.Inclusion{Block: uint64(receipt.BlockNumber), TimeMilliseconds: uint64(*block.TimestampMilliseconds)}, nil
}
