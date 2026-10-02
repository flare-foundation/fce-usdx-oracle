package keeper

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/feedupdate"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	relayStore  = common.HexToAddress("0x5703e")
	relaySender = common.HexToAddress("0x5e4de7")
)

// fakeNode is a JSON-RPC node holding one feed store and its instructions
// sender, enough for Relay to screen, rehearse, submit and read back. Each knob
// is what a test varies: the store's observedAt on each read, how the rehearsal
// and gas estimation answer, and the status of the mined receipt.
type fakeNode struct {
	t *testing.T

	mu       sync.Mutex
	reads    int
	calls    int
	observed func(read int) uint64 // the store's observedAt on its read-th read, from 1
	// submitted is the revert data for the call-th eth_call of submitFeedUpdates,
	// from 1 (the rehearsal, then a mined submission's replay); nil passes.
	submitted func(call int) []byte
	estimate  []byte // revert data for eth_estimateGas; nil passes
	status    uint64 // the receipt's status
	pending   bool   // no receipt is ever served
	sent      bool
	replayed  bool // an eth_call of submitFeedUpdates at a named block was made

	store, sender *abi.ABI
}

func newFakeNode(t *testing.T) *fakeNode {
	t.Helper()
	store, err := teeoraclestore.IITeeOracleFeedStoreMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := teeoraclesender.IITeeOracleInstructionsSenderMetaData.GetAbi()
	if err != nil {
		t.Fatal(err)
	}
	return &fakeNode{t: t, store: store, sender: sender, status: types.ReceiptStatusSuccessful}
}

// revertWith is the revert data of a store custom error.
func (n *fakeNode) revertWith(name string) []byte {
	n.t.Helper()
	e, ok := n.store.Errors[name]
	if !ok {
		n.t.Fatalf("the store ABI has no error %s", name)
	}
	return e.ID.Bytes()[:4]
}

// support is a Support whose chain client talks to the fake node, signing as key.
func (n *fakeNode) support(key *ecdsa.PrivateKey) *support.Support {
	n.t.Helper()
	srv := httptest.NewServer(n)
	n.t.Cleanup(srv.Close)
	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(client.Close)
	return &support.Support{Prv: key, ChainClient: client, ChainID: testChain}
}

type rpcRequest struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (n *fakeNode) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		n.t.Errorf("decoding an rpc request: %v", err)
		return
	}
	result, rerr := n.answer(req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		n.t.Errorf("encoding an rpc response: %v", err)
	}
}

func reverted(data []byte) *rpcError {
	return &rpcError{Code: 3, Message: "execution reverted", Data: hexutil.Encode(data)}
}

func (n *fakeNode) answer(req rpcRequest) (any, *rpcError) {
	n.mu.Lock()
	defer n.mu.Unlock()

	switch req.Method {
	case "eth_call":
		return n.call(req)
	case "eth_getBlockByNumber":
		// No baseFee, so the transactor builds a legacy transaction.
		return map[string]any{
			"parentHash": common.Hash{}, "sha3Uncles": types.EmptyUncleHash, "miner": common.Address{},
			"stateRoot": common.Hash{}, "transactionsRoot": types.EmptyTxsHash, "receiptsRoot": types.EmptyReceiptsHash,
			"logsBloom": types.Bloom{}, "difficulty": "0x0", "number": "0x10", "gasLimit": "0x1c9c380",
			"gasUsed": "0x0", "timestamp": "0x64", "extraData": "0x",
		}, nil
	case "eth_gasPrice":
		return "0x1", nil
	case "eth_getCode":
		return "0x6080", nil
	case "eth_estimateGas":
		if n.estimate != nil {
			return nil, reverted(n.estimate)
		}
		return "0x30000", nil
	case "eth_getTransactionCount":
		return "0x0", nil
	case "eth_sendRawTransaction":
		var raw hexutil.Bytes
		if err := json.Unmarshal(req.Params[0], &raw); err != nil {
			n.t.Errorf("decoding the raw transaction: %v", err)
		}
		var tx types.Transaction
		if err := tx.UnmarshalBinary(raw); err != nil {
			n.t.Errorf("decoding the transaction: %v", err)
		}
		n.sent = true
		return tx.Hash(), nil
	case "eth_getTransactionReceipt":
		if n.pending {
			return nil, nil
		}
		var hash common.Hash
		if err := json.Unmarshal(req.Params[0], &hash); err != nil {
			n.t.Errorf("decoding the receipt request: %v", err)
		}
		return map[string]any{
			"status": hexutil.Uint64(n.status), "cumulativeGasUsed": "0x5208", "logsBloom": types.Bloom{},
			"logs": []any{}, "transactionHash": hash, "gasUsed": "0x5208", "type": "0x0",
			"blockNumber": "0x11", "blockHash": common.HexToHash("0xb10c"), "transactionIndex": "0x0",
			"effectiveGasPrice": "0x1",
		}, nil
	}
	n.t.Errorf("unexpected rpc %s", req.Method)
	return nil, &rpcError{Code: -32601, Message: "method not found"}
}

func (n *fakeNode) call(req rpcRequest) (any, *rpcError) {
	var msg struct {
		To    common.Address `json:"to"`
		Input hexutil.Bytes  `json:"input"`
		Data  hexutil.Bytes  `json:"data"`
	}
	if err := json.Unmarshal(req.Params[0], &msg); err != nil {
		n.t.Errorf("decoding eth_call: %v", err)
	}
	input := msg.Input
	if len(input) == 0 {
		input = msg.Data
	}
	if len(input) < 4 {
		n.t.Errorf("eth_call without a selector")
		return nil, &rpcError{Code: -32000, Message: "no selector"}
	}

	contract := n.store
	if msg.To == relaySender {
		contract = n.sender
	}
	method, err := contract.MethodById(input[:4])
	if err != nil {
		n.t.Errorf("eth_call of an unknown method on %s: %v", msg.To.Hex(), err)
		return nil, &rpcError{Code: -32000, Message: "unknown method"}
	}

	var out []byte
	switch method.Name {
	case "extensionId":
		out, err = method.Outputs.Pack(big.NewInt(1))
	case "instructionsSender":
		out, err = method.Outputs.Pack(relaySender)
	case "latestEndpointsHash":
		out, err = method.Outputs.Pack([32]byte(testEndpoints))
	case "latestAdminsHash":
		out, err = method.Outputs.Pack([32]byte(testAdmins))
	case "requiredSignatures":
		out, err = method.Outputs.Pack(uint8(1))
	case "observedAt":
		n.reads++
		out, err = method.Outputs.Pack(n.observed(n.reads))
	case "submitFeedUpdates":
		n.calls++
		if len(req.Params) > 1 && string(req.Params[1]) != `"latest"` {
			n.replayed = true
		}
		if n.submitted != nil {
			if data := n.submitted(n.calls); data != nil {
				return nil, reverted(data)
			}
		}
		return "0x", nil
	default:
		n.t.Errorf("eth_call of %s is not part of a relay", method.Name)
		return nil, &rpcError{Code: -32000, Message: "unexpected method"}
	}
	if err != nil {
		n.t.Errorf("packing %s: %v", method.Name, err)
	}
	return hexutil.Bytes(out), nil
}

// batchStamp is the observedAt every relayed batch carries; each test sets the
// store's stamp on either side of it.
const batchStamp = 100

// relay runs Relay against the node with one machine's update stamped
// batchStamp.
func relay(t *testing.T, n *fakeNode) (Record, error) {
	t.Helper()
	return relayWithin(t, n, 5*time.Second)
}

// relayWithin is relay with the given submit timeout.
func relayWithin(t *testing.T, n *fakeNode, submitTimeout time.Duration) (Record, error) {
	t.Helper()
	machine, keeperKey := newKey(t), newKey(t)
	s := n.support(keeperKey)
	cfg := Config{
		KeeperID: "test", Sender: relaySender, Store: relayStore,
		TeeIDs:        []common.Address{addr(machine)},
		SubmitTimeout: submitTimeout,
	}
	return Relay(context.Background(), s, cfg, Record{KeeperID: "test"},
		[]feedupdate.Update{signed(t, machine, batchStamp, 99_000_000)})
}

// The control: a batch the store takes is accepted.
func TestRelayAcceptsALandedSubmission(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(int) uint64 { return 90 }

	rec, err := relay(t, n)
	if err != nil {
		t.Fatalf("relay: %v", err)
	}
	if rec.Outcome != OutcomeAccepted || rec.SubmitOutcome != "accepted" {
		t.Fatalf("outcome %s / %s, want accepted", rec.Outcome, rec.SubmitOutcome)
	}
}

// The store's own check in the preflight: it already holds a newer stamp, so
// another submission landed first.
func TestRelayPreflightNotNewerIsARaceLost(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(int) uint64 { return 120 }

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRaceLost || rec.SubmitOutcome != guardNotNewer {
		t.Fatalf("outcome %s / %s, want race_lost / NotNewer", rec.Outcome, rec.SubmitOutcome)
	}
	if n.sent {
		t.Error("a batch the preflight refused was sent")
	}
}

// Another batch lands between the preflight's read and the rehearsal: the
// preflight passes, the rehearsal reverts NotNewer, and the store read after
// it is ahead. That is a lost race, not a stale submission.
func TestRelayRehearsalNotNewerIsARaceLost(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(read int) uint64 {
		if read == 1 {
			return 90
		}
		return 110
	}
	n.submitted = always(n.revertWith("NotNewer"))

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRaceLost || rec.SubmitOutcome != guardNotNewer {
		t.Fatalf("outcome %s / %s, want race_lost / NotNewer", rec.Outcome, rec.SubmitOutcome)
	}
	if rec.StoreObservedAtAfter != 110 {
		t.Errorf("storeObservedAtAfter = %d, want 110", rec.StoreObservedAtAfter)
	}
}

// A NotNewer whose follow-up read shows the store behind the batch has not been
// shown to advance the feed — a lagging node answered the read — so it stays a
// rejection rather than being waved through as a race.
func TestRelayNotNewerWithTheStoreBehindIsARejection(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(int) uint64 { return 90 }
	n.submitted = always(n.revertWith("NotNewer"))

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRejected || rec.SubmitOutcome != guardNotNewer {
		t.Fatalf("outcome %s / %s, want rejected / NotNewer", rec.Outcome, rec.SubmitOutcome)
	}
}

// Any other guard is a rejection under its own name, however far the store
// has moved.
func TestRelayRehearsalGuardIsARejection(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(read int) uint64 {
		if read == 1 {
			return 90
		}
		return 500
	}
	n.submitted = always(n.revertWith("SpreadTooBig"))

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRejected || rec.SubmitOutcome != "SpreadTooBig" {
		t.Fatalf("outcome %s / %s, want rejected / SpreadTooBig", rec.Outcome, rec.SubmitOutcome)
	}
}

// Gas estimation is a rehearsal against pending state, so a batch overtaken
// after its rehearsal is refused there, and is classified the same way.
func TestRelayEstimateNotNewerIsARaceLost(t *testing.T) {
	n := newFakeNode(t)
	n.observed = func(read int) uint64 {
		if read == 1 {
			return 90
		}
		return 110
	}
	n.estimate = n.revertWith("NotNewer")

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRaceLost || rec.SubmitOutcome != guardNotNewer {
		t.Fatalf("outcome %s / %s, want race_lost / NotNewer", rec.Outcome, rec.SubmitOutcome)
	}
	if n.sent {
		t.Error("a batch refused at gas estimation was sent")
	}
}

// A submission that is mined and reverts has a receipt, so it is never
// not_mined. Overtaken in the block it landed in, it is a lost race, and the
// replay at that block names the guard.
func TestRelayMinedRevertWithTheStoreAheadIsARaceLost(t *testing.T) {
	n := newFakeNode(t)
	n.status = types.ReceiptStatusFailed
	n.observed = func(read int) uint64 {
		if read == 1 {
			return 90
		}
		return 100 // the competing batch carried the same stamp
	}
	n.submitted = afterTheRehearsal(n.revertWith("NotNewer"))

	rec, err := relay(t, n)
	if rec.Outcome != OutcomeRaceLost || rec.SubmitOutcome != guardNotNewer {
		t.Fatalf("outcome %s / %s, want race_lost / NotNewer", rec.Outcome, rec.SubmitOutcome)
	}
	if !n.replayed {
		t.Error("the mined revert was not replayed at its block")
	}
	if rec.SubmitTx == "" || err == nil || !strings.Contains(err.Error(), "reverted") {
		t.Errorf("submitTx %q, err %v: want the transaction named and the revert described", rec.SubmitTx, err)
	}
}

// Mined, reverted, and the store did not move: a rejection under the guard
// the replay names.
func TestRelayMinedRevertWithTheStoreBehindIsARejection(t *testing.T) {
	n := newFakeNode(t)
	n.status = types.ReceiptStatusFailed
	n.observed = func(int) uint64 { return 90 }
	n.submitted = afterTheRehearsal(n.revertWith("StaleEndpoints"))

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRejected || rec.SubmitOutcome != "StaleEndpoints" {
		t.Fatalf("outcome %s / %s, want rejected / StaleEndpoints", rec.Outcome, rec.SubmitOutcome)
	}
}

// Mined, reverted, and the replay names nothing: a rejection under Reverted.
func TestRelayMinedRevertWithNoGuardIsReverted(t *testing.T) {
	n := newFakeNode(t)
	n.status = types.ReceiptStatusFailed
	n.observed = func(int) uint64 { return 90 }

	rec, _ := relay(t, n)
	if rec.Outcome != OutcomeRejected || rec.SubmitOutcome != guardReverted {
		t.Fatalf("outcome %s / %s, want rejected / Reverted", rec.Outcome, rec.SubmitOutcome)
	}
}

// not_mined means one thing: no receipt inside the submit deadline.
func TestRelayWithNoReceiptIsNotMined(t *testing.T) {
	n := newFakeNode(t)
	n.pending = true
	n.observed = func(int) uint64 { return 90 }

	rec, _ := relayWithin(t, n, 1500*time.Millisecond)
	if rec.Outcome != OutcomeNotMined || rec.SubmitOutcome != "not_mined" {
		t.Fatalf("outcome %s / %s, want not_mined", rec.Outcome, rec.SubmitOutcome)
	}
	if n.replayed {
		t.Error("a submission with no receipt was replayed as if it had reverted")
	}
}

// always reverts every call of submitFeedUpdates with data.
func always(data []byte) func(int) []byte { return func(int) []byte { return data } }

// afterTheRehearsal lets the first call of submitFeedUpdates, the rehearsal,
// pass and reverts every later one, the replay of a mined submission, with data.
func afterTheRehearsal(data []byte) func(int) []byte {
	return func(call int) []byte {
		if call == 1 {
			return nil
		}
		return data
	}
}
