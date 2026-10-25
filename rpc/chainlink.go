package rpc

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	ethereum "github.com/ava-labs/libevm"
	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/ethclient"
	"github.com/ava-labs/libevm/rpc"

	"github.com/BenWolfaardt/gasometer"
)

var (
	selectorLatestRoundData = common.FromHex("0xfeaf968c")
	selectorDecimals        = common.FromHex("0x313ce567")
)

var _ gasometer.PriceSource = (*ChainlinkPriceSource)(nil)

// ChainlinkPriceSource reads an aggregator's latestRoundData.
type ChainlinkPriceSource struct {
	client *Client
	feed   common.Address

	mu       sync.Mutex
	decimals *uint8 // read once; a feed's decimals never change
}

// NewChainlinkPriceSource returns a price source on the aggregator at feed.
func NewChainlinkPriceSource(client *Client, feed string) *ChainlinkPriceSource {
	return &ChainlinkPriceSource{client: client, feed: common.HexToAddress(feed)}
}

// Price implements gasometer.PriceSource.
func (s *ChainlinkPriceSource) Price(ctx context.Context) (gasometer.PriceQuote, error) {
	decimals, err := s.readDecimals(ctx)
	if err != nil {
		return gasometer.PriceQuote{}, err
	}
	out, err := s.call(ctx, selectorLatestRoundData)
	if err != nil {
		return gasometer.PriceQuote{}, fmt.Errorf("latestRoundData: %w", err)
	}
	// (roundId, answer int256, startedAt, updatedAt, answeredInRound)
	if len(out) != 5*32 {
		return gasometer.PriceQuote{}, fmt.Errorf("latestRoundData returned %d bytes", len(out))
	}
	answer := new(big.Int).SetBytes(out[32:64])
	if out[32]&0x80 != 0 || answer.Sign() == 0 {
		return gasometer.PriceQuote{}, fmt.Errorf("latestRoundData answer is not positive")
	}
	updatedAt := new(big.Int).SetBytes(out[96:128])
	if !updatedAt.IsInt64() {
		return gasometer.PriceQuote{}, fmt.Errorf("latestRoundData updatedAt out of range")
	}
	return gasometer.PriceQuote{Answer: answer, Decimals: decimals, UpdatedAt: time.Unix(updatedAt.Int64(), 0).UTC()}, nil
}

func (s *ChainlinkPriceSource) readDecimals(ctx context.Context) (uint8, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.decimals != nil {
		return *s.decimals, nil
	}
	out, err := s.call(ctx, selectorDecimals)
	if err != nil {
		return 0, fmt.Errorf("decimals: %w", err)
	}
	if len(out) != 32 || new(big.Int).SetBytes(out).Cmp(big.NewInt(255)) > 0 {
		return 0, fmt.Errorf("decimals returned %x", out)
	}
	d := out[31]
	s.decimals = &d
	return d, nil
}

func (s *ChainlinkPriceSource) call(ctx context.Context, data []byte) ([]byte, error) {
	var out []byte
	err := s.client.Do(ctx, "eth_call", func(ctx context.Context, c *rpc.Client) error {
		var err error
		out, err = ethclient.NewClient(c).CallContract(ctx, ethereum.CallMsg{To: &s.feed, Data: data}, nil)
		return err
	})
	return out, err
}
