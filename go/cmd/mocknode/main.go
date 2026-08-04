// Command mocknode is a local stand-in for tee-node's signing service.
//
// It implements the three endpoints the extension actually uses, with a key
// held in this process instead of an enclave:
//
//	POST /sign      sign with the identity key (EIP-191, exactly as tee-node does)
//	POST /decrypt   ECIES-decrypt to the identity key
//	GET  /info      publish the public key, mirroring the TEE proxy's shape
//
// This exists so the provisioning flow can be exercised without Confidential
// Space: SET_SECRET carries a ciphertext that must be decrypted in-enclave, so
// without a decrypt endpoint the path cannot be tested at all.
//
// NOT SECURE AND NOT FOR DEPLOYMENT. The key is derived from a fixed seed so
// runs are reproducible, which is the opposite of what a real TEE guarantees.
// Nothing here is compiled into the production image — that is ./cmd/docker,
// which talks to a real tee-node.
package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
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
type infoResponse struct {
	TEEInfo struct {
		TeeID     string `json:"teeId"`
		PublicKey struct {
			X string `json:"x"`
			Y string `json:"y"`
		} `json:"publicKey"`
	} `json:"teeInfo"`
}

func main() {
	port := 9090
	if v := os.Getenv("SIGN_PORT"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &port)
	}

	key, err := crypto.HexToECDSA(devSeed)
	if err != nil {
		logger.Fatalf("bad dev seed: %v", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sign", signHandler(key))
	mux.HandleFunc("POST /decrypt", decryptHandler(key))
	mux.HandleFunc("GET /info", infoHandler(key, addr))

	logger.Warnf("mocknode is INSECURE — a fixed dev key, not an enclave")
	logger.Infof("mocknode on :%d", port)
	logger.Infof("  teeId:     %s", addr.Hex())
	logger.Infof("  publicKey: 0x%x", crypto.FromECDSAPub(&key.PublicKey))

	if err := http.ListenAndServe(fmt.Sprintf(":%d", port), mux); err != nil {
		logger.Fatalf("server: %v", err)
	}
}

// signHandler reproduces tee-node's signWithTeeHandler exactly:
//
//	msgHash := crypto.Keccak256(message)
//	sig     := crypto.Sign(accounts.TextHash(msgHash), key)
//
// so the EIP-191 prefixing and the 0/1 recovery id both behave as production.
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

func infoHandler(key *ecdsa.PrivateKey, addr fmt.Stringer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var resp infoResponse
		resp.TEEInfo.TeeID = addr.String()
		resp.TEEInfo.PublicKey.X = fmt.Sprintf("0x%x", key.PublicKey.X)
		resp.TEEInfo.PublicKey.Y = fmt.Sprintf("0x%x", key.PublicKey.Y)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
