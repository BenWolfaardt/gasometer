package rpc_test

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"

	"github.com/BenWolfaardt/gasometer/rpc"
)

// roundData is a latestRoundData return with the given answer word and updatedAt 1,800,000,000.
func roundData(answer []byte) []byte {
	out := make([]byte, 5*32)
	copy(out[32:64], common.LeftPadBytes(answer, 32))
	copy(out[96:128], common.LeftPadBytes(big.NewInt(1_800_000_000).Bytes(), 32))
	return out
}

// feedServer answers decimals with 8 and latestRoundData with round.
func feedServer(t *testing.T, round []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := common.LeftPadBytes([]byte{8}, 32)
		if strings.Contains(string(req.Params[0]), "0xfeaf968c") {
			result = round
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": hexutil.Bytes(result)})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChainlinkPriceSourceRejectsBadRounds(t *testing.T) {
	negative := make([]byte, 32)
	for i := range negative {
		negative[i] = 0xff
	}
	tests := map[string][]byte{
		"zero answer":     roundData(nil),
		"negative answer": roundData(negative),
		"short return":    roundData([]byte{1})[:64],
	}
	for name, round := range tests {
		t.Run(name, func(t *testing.T) {
			srv := feedServer(t, round)
			_, err := rpc.NewChainlinkPriceSource(newClient(t, &recordingObserver{}, srv.URL), "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70").Price(t.Context())
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestChainlinkPriceSourceDecodesRound(t *testing.T) {
	srv := feedServer(t, roundData(big.NewInt(312_345_000_000).Bytes()))
	q, err := rpc.NewChainlinkPriceSource(newClient(t, &recordingObserver{}, srv.URL), "0x71041dddad3595F9CEd3DcCFBe3D1F4b0a16Bb70").Price(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if q.USD() != 3_123.45 || q.UpdatedAt.Unix() != 1_800_000_000 {
		t.Fatalf("quote %+v", q)
	}
}
