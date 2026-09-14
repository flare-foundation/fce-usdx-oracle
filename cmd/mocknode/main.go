// Command mocknode is a local stand-in for tee-node's signing service, holding
// the key in-process instead of an enclave so the provisioning flow can be
// exercised without Confidential Space:
//
//	POST /sign      sign with the identity key (EIP-191, exactly as tee-node does)
//	POST /decrypt   ECIES-decrypt to the identity key
//	GET  /info      publish the public key and the extension's state, mirroring the TEE proxy's shape
//
// NOT SECURE AND NOT FOR DEPLOYMENT. The key comes from a fixed seed so runs
// are reproducible, which is the opposite of what a real TEE guarantees.
package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// devSeed produces a fixed key so a walkthrough is reproducible.
const devSeed = "0000000000000000000000000000000000000000000000000000000000000042"

type signRequest struct {
	Message []byte `json:"message"`
}

type signResponse struct {
	Message   []byte `json:"message"`
	Signature []byte `json:"signature"`
}

type decryptRequest struct {
	EncryptedMessage []byte `json:"encryptedMessage"`
}

type decryptResponse struct {
	DecryptedMessage []byte `json:"decryptedMessage"`
}

// infoResponse mirrors the TEE proxy's GET /info, so the provisioning CLI reads
// the public key the same way against both.
//
// The coordinates are teetypes.PublicKey — the declaration tee-node serialises
// from — and not a local pair of strings. Formatting them by hand produced
// MINIMAL hex, so a coordinate with a leading zero byte came out 62 characters
// and any consumer decoding into common.Hash rejected it.
type infoResponse struct {
	TEEInfo struct {
		TeeID     string             `json:"teeId"`
		PublicKey teetypes.PublicKey `json:"publicKey"`
		State     teetypes.TeeState  `json:"state"`
	} `json:"teeInfo"`
}

// stateUnavailableVersion is tee-node's StateUnavailableVersion: the state
// version it reports when it could not read its extension's /state.
var stateUnavailableVersion = common.MaxHash

// extensionState mirrors tee-node's ExtensionState. The extension's GET /state
// is read on every info request and its State and StateVersion carried as they
// are, with the system half zeroed; an unreachable or malformed route reports
// the unavailable sentinel rather than failing the whole response. This is what
// lets the harness read the state the way every consumer does, through the
// node's info, instead of from the extension's port.
func extensionState(url string) teetypes.TeeState {
	unavailable := teetypes.TeeState{
		SystemState: hexutil.Bytes{}, State: hexutil.Bytes{}, StateVersion: stateUnavailableVersion,
	}
	client := http.Client{Timeout: 2 * time.Second}
	res, err := client.Get(url)
	if err != nil {
		return unavailable
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode != http.StatusOK {
		return unavailable
	}
	var resp types.StateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return unavailable
	}
	return teetypes.TeeState{
		SystemState: hexutil.Bytes{}, State: resp.State, StateVersion: resp.StateVersion,
	}
}

func main() {
	port := 7701 // the sign-server default in internal/config, which the extension calls
	if v := os.Getenv("SIGN_PORT"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &port)
	}

	key, err := crypto.HexToECDSA(devSeed)
	if err != nil {
		logger.Fatalf("bad dev seed: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	// The extension whose state /info carries: EXTENSION_PORT, as for tee-node.
	stateURL := fmt.Sprintf("http://127.0.0.1:%d/state", config.ExtensionPort)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sign", signHandler(key))
	mux.HandleFunc("POST /decrypt", decryptHandler(key))
	mux.HandleFunc("GET /info", infoHandler(key, addr, stateURL))

	logger.Warnf("mocknode is INSECURE — a fixed dev key, not an enclave")
	logger.Infof("mocknode on :%d", port)
	logger.Infof("  teeId:     %s", addr.Hex())
	logger.Infof("  publicKey: 0x%x", crypto.FromECDSAPub(&key.PublicKey))
	logger.Infof("  state:     %s, folded into /info", stateURL)

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
		logger.Fatalf("server: %v", err)
	}
}

// signHandler reproduces tee-node's signWithTeeHandler exactly: EIP-191 prefixing and
// the 0/1 recovery id both behave as in production.
//
//	sig := crypto.Sign(accounts.TextHash(crypto.Keccak256(message)), key)
func signHandler(key *ecdsa.PrivateKey) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Message) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		sig, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(req.Message)), key)
		if err != nil {
			http.Error(w, "cannot sign", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(signResponse{Message: req.Message, Signature: sig})
	}
}

func decryptHandler(key *ecdsa.PrivateKey) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req decryptRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.EncryptedMessage) == 0 {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		plaintext, err := ecies.ImportECDSA(key).Decrypt(req.EncryptedMessage, nil, nil)
		if err != nil {
			// Never echo the ciphertext or the error detail.
			http.Error(w, "cannot decrypt", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(decryptResponse{DecryptedMessage: plaintext})
	}
}

func infoHandler(key *ecdsa.PrivateKey, addr fmt.Stringer, stateURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var resp infoResponse
		resp.TEEInfo.TeeID = addr.String()
		resp.TEEInfo.PublicKey = teetypes.PubKeyToStruct(&key.PublicKey)
		resp.TEEInfo.State = extensionState(stateURL)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
