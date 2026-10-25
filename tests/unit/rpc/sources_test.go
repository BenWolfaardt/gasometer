package rpc_test

import (
	"math/big"
	"testing"
	"time"

	"github.com/ava-labs/libevm/core/types"

	"github.com/BenWolfaardt/gasometer/internal/domain/entities"
	"github.com/BenWolfaardt/gasometer/internal/domain/services"
	"github.com/BenWolfaardt/gasometer/internal/infrastructure/rpc"
)

var refTxs = []entities.ReferenceTx{{Name: "native", Gas: 21_000}, {Name: "erc20", Gas: 65_000, DataSize: 68}}

func TestEVMFeeSourceOnRecordedNetworks(t *testing.T) {
	for _, network := range []string{"base", "op", "cchain"} {
		t.Run(network, func(t *testing.T) {
			replay, srv := newReplayServer(t, network)
			fees := rpc.NewEVMFeeSource(newClient(t, &recordingObserver{}, srv.URL))

			head, err := fees.HeadFee(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if head.Number == 0 || head.BaseFee == nil || head.BaseFee.Sign() <= 0 || head.GasLimit == 0 {
				t.Fatalf("head %+v", head)
			}
			if network != "cchain" {
				if _, err := services.DecodeOPStackExtraData(head.ExtraData); err != nil {
					t.Fatalf("extraData: %v", err)
				}
				if head.BlobGasUsed == 0 {
					t.Fatal("OP Stack head lacks blobGasUsed")
				}
			}

			history, err := fees.FeeHistory(t.Context(), 4, head.Number, services.RewardPercentiles(5))
			if err != nil {
				t.Fatal(err)
			}
			if history.OldestBlock != head.Number-3 || history.Len() != 4 || len(history.BaseFees) != 5 ||
				len(history.Rewards) != 4 || len(history.Rewards[0]) != 2 {
				t.Fatalf("history %+v", history)
			}
			if history.BaseFees[3].Cmp(head.BaseFee) != 0 {
				t.Fatalf("history base fee %s at head, header %s", history.BaseFees[3], head.BaseFee)
			}
			blocks, missing, err := services.BlockFees(history, time.Now())
			if err != nil || missing != 0 || len(blocks) != 4 {
				t.Fatalf("blocks %d, missing %d, err %v", len(blocks), missing, err)
			}

			tip, err := fees.NodeTip(t.Context())
			if err != nil || tip == nil || tip.Sign() <= 0 {
				t.Fatalf("tip %v, err %v", tip, err)
			}
			if replay.userAgent != rpc.UserAgent {
				t.Fatalf("user agent %q", replay.userAgent)
			}
		})
	}
}

func TestOPStackModelMatchesRecordedHeader(t *testing.T) {
	for _, network := range []string{"base", "op"} {
		t.Run(network, func(t *testing.T) {
			_, srv := newReplayServer(t, network)
			fees := rpc.NewEVMFeeSource(newClient(t, &recordingObserver{}, srv.URL))
			head, err := fees.HeadFee(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			history, err := fees.FeeHistory(t.Context(), 4, head.Number, services.RewardPercentiles(5))
			if err != nil {
				t.Fatal(err)
			}
			params, err := services.DecodeOPStackExtraData(head.ExtraData)
			if err != nil {
				t.Fatal(err)
			}
			next, err := services.NextBaseFee(params, head.BaseFee, head.GasMetered(), head.GasLimit)
			if err != nil {
				t.Fatal(err)
			}
			if next.Cmp(history.NextBaseFee()) != 0 {
				t.Fatalf("model %s, rpc %s", next, history.NextBaseFee())
			}
		})
	}
}

func TestChainlinkPriceSource(t *testing.T) {
	feeds := map[string]string{
		"base":   "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70",
		"op":     "0x13e3Ee699D1909E989722E753853AE30b17e08c5",
		"cchain": "0x0A77230d17318075983913bC2145DB16C7366156",
	}
	for network, feed := range feeds {
		t.Run(network, func(t *testing.T) {
			replay, srv := newReplayServer(t, network)
			prices := rpc.NewChainlinkPriceSource(newClient(t, &recordingObserver{}, srv.URL), feed)
			for range 2 {
				q, err := prices.Price(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				usd := q.USD()
				if q.Decimals != 8 || usd < 1 || usd > 100_000 || q.UpdatedAt.Year() < 2026 {
					t.Fatalf("quote %+v (%.2f USD)", q, usd)
				}
			}
			// decimals is read once, latestRoundData every time.
			if got := replay.callCount("eth_call"); got != 3 {
				t.Fatalf("%d eth_calls, want 3", got)
			}
		})
	}
}

func TestOPStackL1FeeSource(t *testing.T) {
	for network, chainID := range map[string]uint64{"base": 8453, "op": 10} {
		t.Run(network, func(t *testing.T) {
			_, srv := newReplayServer(t, network)
			fees, err := rpc.NewOPStackL1FeeSource(newClient(t, &recordingObserver{}, srv.URL), chainID).L1Fees(t.Context(), refTxs)
			if err != nil {
				t.Fatal(err)
			}
			// Both reference transactions sit under Fjord's 100-byte floor (GasPriceOracle._fjordLinearRegression),
			// so they price the same.
			if len(fees) != 2 || fees["native"].Sign() <= 0 || fees["native"].Cmp(fees["erc20"]) != 0 {
				t.Fatalf("fees %v", fees)
			}
		})
	}
}

func TestEncodeReferenceTx(t *testing.T) {
	for _, ref := range refTxs {
		raw, err := rpc.EncodeReferenceTx(big.NewInt(8453), ref)
		if err != nil {
			t.Fatal(err)
		}
		var tx types.Transaction
		if err := tx.UnmarshalBinary(raw); err != nil {
			t.Fatal(err)
		}
		v, r, s := tx.RawSignatureValues()
		if tx.Type() != types.DynamicFeeTxType || tx.ChainId().Uint64() != 8453 || tx.Gas() != ref.Gas ||
			len(tx.Data()) != ref.DataSize || v.Sign() != 0 || r.Sign() != 0 || s.Sign() != 0 {
			t.Fatalf("%s: decoded %+v", ref.Name, tx)
		}
		if ref.DataSize == 68 && tx.Data()[0] != 0xa9 {
			t.Fatalf("erc20 calldata does not start with transfer: %x", tx.Data()[:4])
		}
	}
}
