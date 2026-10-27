package rpc_test

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/common/hexutil"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"

	"github.com/BenWolfaardt/gasometer"
	"github.com/BenWolfaardt/gasometer/rpc"
)

const testChainID = 43114

var testSeed = []byte(strings.Repeat("s", rpc.SeedLength))

// chainServer is a JSON-RPC node holding sent transactions. sendError, when set, answers eth_sendRawTransaction with
// that message; nullBlocks answers that many eth_getBlockByNumber calls with null first.
type chainServer struct {
	mu         sync.Mutex
	requests   int
	sent       []*types.Transaction
	included   map[common.Hash]bool
	sendError  string
	nullBlocks int
}

func newChainServer(t *testing.T) (*chainServer, *rpc.TxSender) {
	t.Helper()
	s := &chainServer{included: map[common.Hash]bool{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	sender, err := rpc.NewTxSender(newClient(t, &recordingObserver{}, srv.URL), big.NewInt(testChainID), testSeed, 9)
	if err != nil {
		t.Fatal(err)
	}
	return s, sender
}

func (s *chainServer) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	w.Header().Set("Content-Type", "application/json")
	if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
		var batch []jsonRPCRequest
		_ = json.Unmarshal(body, &batch)
		out := make([]map[string]any, len(batch))
		for i, r := range batch {
			out[i] = s.answer(r)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	var r jsonRPCRequest
	_ = json.Unmarshal(body, &r)
	_ = json.NewEncoder(w).Encode(s.answer(r))
}

func (s *chainServer) answer(r jsonRPCRequest) map[string]any {
	resp := map[string]any{"jsonrpc": "2.0", "id": r.ID}
	params, _ := r.Params.([]any)
	switch r.Method {
	case "eth_getTransactionCount":
		resp["result"] = map[string]string{"latest": "0x3", "pending": "0x5"}[params[1].(string)]
	case "eth_getBalance":
		resp["result"] = "0x10"
	case "eth_baseFee":
		resp["result"] = "0x64"
	case "eth_maxPriorityFeePerGas":
		resp["result"] = "0x96"
	case "eth_blockNumber":
		resp["result"] = "0x2a"
	case "eth_sendRawTransaction":
		if s.sendError != "" {
			resp["error"] = map[string]any{"code": -32000, "message": s.sendError}
			return resp
		}
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(hexutil.MustDecode(params[0].(string))); err != nil {
			resp["error"] = map[string]any{"code": -32602, "message": err.Error()}
			return resp
		}
		s.sent = append(s.sent, tx)
		resp["result"] = tx.Hash()
	case "eth_getTransactionReceipt":
		if s.included[common.HexToHash(params[0].(string))] {
			resp["result"] = map[string]string{"blockNumber": "0x7"}
		} else {
			resp["result"] = nil
		}
	case "eth_getBlockByNumber":
		if s.nullBlocks > 0 {
			s.nullBlocks--
			resp["result"] = nil
		} else {
			resp["result"] = map[string]string{"number": params[0].(string), "timestampMilliseconds": "0x1a179aafff1"}
		}
	default:
		resp["error"] = map[string]any{"code": -32601, "message": "method not found"}
	}
	return resp
}

func derivedAddress(t *testing.T, i uint64) common.Address {
	t.Helper()
	var index [8]byte
	binary.BigEndian.PutUint64(index[:], i)
	key, err := crypto.ToECDSA(crypto.Keccak256(append(append([]byte{}, testSeed...), index[:]...)))
	if err != nil {
		t.Fatal(err)
	}
	return crypto.PubkeyToAddress(key.PublicKey)
}

func TestTxSenderDerivesAccountsFromTheSeed(t *testing.T) {
	_, sender := newChainServer(t)
	seen := map[string]bool{}
	for i := range 9 {
		addr := sender.Address(i)
		if addr != derivedAddress(t, uint64(i)).Hex() || seen[addr] {
			t.Fatalf("account %d: %s", i, addr)
		}
		seen[addr] = true
	}
	if _, err := rpc.NewTxSender(nil, big.NewInt(testChainID), testSeed[1:], 1); err == nil {
		t.Fatal("31-byte seed accepted")
	}
}

func TestTxSenderSignsADynamicFeeTransfer(t *testing.T) {
	server, sender := newChainServer(t)
	hash, err := sender.Send(t.Context(), gasometer.ProbeTx{
		From: 3, To: 0, Value: big.NewInt(12_345), Nonce: 7, Tip: big.NewInt(150), MaxFee: big.NewInt(5_000_000_150),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(server.sent) != 1 {
		t.Fatalf("%d sent", len(server.sent))
	}
	tx := server.sent[0]
	from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(testChainID)), tx)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Type() != types.DynamicFeeTxType || tx.ChainId().Int64() != testChainID || tx.Nonce() != 7 ||
		tx.GasTipCap().Int64() != 150 || tx.GasFeeCap().Int64() != 5_000_000_150 || tx.Gas() != 21_000 ||
		*tx.To() != derivedAddress(t, 0) || tx.Value().Int64() != 12_345 || from != derivedAddress(t, 3) {
		t.Fatalf("tx %+v from %s", tx, from)
	}
	if hash != tx.Hash().Hex() {
		t.Fatalf("hash %s, sent %s", hash, tx.Hash().Hex())
	}
}

func TestTxSenderSendOutcomes(t *testing.T) {
	probeTx := gasometer.ProbeTx{From: 1, To: 1, Value: new(big.Int), Tip: new(big.Int), MaxFee: big.NewInt(100)}

	server, sender := newChainServer(t)
	server.sendError = "transaction underpriced"
	_, err := sender.Send(t.Context(), probeTx)
	if !errors.Is(err, gasometer.ErrTxRejected) || !strings.Contains(err.Error(), "transaction underpriced") {
		t.Fatalf("refusal: %v", err)
	}

	for _, msg := range []string{"already known", "nonce too low: next nonce 4, tx nonce 3"} {
		server.sendError = msg
		hash, err := sender.Send(t.Context(), probeTx)
		if err != nil || !strings.HasPrefix(hash, "0x") || len(hash) != 66 {
			t.Fatalf("%s: %q, %v", msg, hash, err)
		}
	}
}

func TestTxSenderReadsNoncesInOneRequest(t *testing.T) {
	server, sender := newChainServer(t)
	latest, pending, err := sender.Nonces(t.Context(), 2)
	if err != nil || latest != 3 || pending != 5 {
		t.Fatalf("nonces %d %d, err %v", latest, pending, err)
	}
	if server.requests != 1 {
		t.Fatalf("%d requests, want one batch", server.requests)
	}
}

func TestTxSenderReads(t *testing.T) {
	_, sender := newChainServer(t)
	ctx := t.Context()
	balance, err := sender.Balance(ctx, 1)
	if err != nil || balance.Int64() != 16 {
		t.Fatalf("balance %v, %v", balance, err)
	}
	baseFee, err := sender.BaseFee(ctx)
	if err != nil || baseFee.Int64() != 100 {
		t.Fatalf("base fee %v, %v", baseFee, err)
	}
	tip, err := sender.NodeTip(ctx)
	if err != nil || tip.Int64() != 150 {
		t.Fatalf("node tip %v, %v", tip, err)
	}
	head, err := sender.Head(ctx)
	if err != nil || head != 42 {
		t.Fatalf("head %d, %v", head, err)
	}
}

func TestTxSenderInclusion(t *testing.T) {
	server, sender := newChainServer(t)
	hash, err := sender.Send(t.Context(), gasometer.ProbeTx{From: 1, To: 1, Value: new(big.Int), Tip: big.NewInt(1), MaxFee: big.NewInt(2)})
	if err != nil {
		t.Fatal(err)
	}
	if inc, err := sender.Inclusion(t.Context(), hash); inc != nil || err != nil {
		t.Fatalf("before inclusion: %+v, %v", inc, err)
	}
	server.mu.Lock()
	server.included[common.HexToHash(hash)] = true
	server.nullBlocks = 1
	server.mu.Unlock()
	inc, err := sender.Inclusion(t.Context(), hash)
	if err != nil || inc == nil || inc.Block != 7 || inc.TimeMilliseconds != 1_793_042_612_209 {
		t.Fatalf("inclusion %+v, %v", inc, err)
	}
}
