// Package teenode talks to tee-node's local signing service.
//
// tee-node exposes crypto operations on http://localhost:$SIGN_PORT. Only two
// endpoints matter here: POST /sign, to sign a price report with the TEE
// identity key, and POST /decrypt, to unseal injected secrets.
//
// # What POST /sign actually does
//
// Verified against tee-node v0.0.21-0.20260619120252-31fc839ae6d2, the version
// pinned in go.mod. Reading the chain end to end:
//
//	// internal/extension/server/server.go — signWithTeeHandler
//	msgHash := crypto.Keccak256(signRequest.Message)
//	signature, err := s.node.Sign(msgHash)
//
//	// pkg/utils/crypto.go — Sign
//	sig, err := crypto.Sign(accounts.TextHash(msgHash), privKey)
//
// So the signed digest is:
//
//	keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message))
//
// Three consequences, each of which breaks verification silently if missed.
// All three are pinned by tests in this package.
//
// **1. EIP-191 prefixing is applied by tee-node, not by us.** The verifying
// contract must recover against toEthSignedMessageHash(keccak256(body)), not
// against the bare keccak256(body). Recovering against the bare hash yields a
// well-formed but wrong address, so the failure looks like an unregistered TEE
// rather than a hashing mismatch.
//
// **2. crypto.Sign returns v as 0 or 1.** ecrecover and OpenZeppelin's
// ECDSA.recover require 27 or 28; OZ rejects anything else outright. Exactly
// one side must add 27 — if both do, or neither does, every signature fails.
// This package normalises on the way out (see NormalizeV) so the contract can
// use stock OpenZeppelin unmodified.
//
// **3. The wire is base64, not hex.** tee-node's SignRequest.Message and
// SignResponse.Signature are Go []byte, and encoding/json renders []byte as
// base64. Everything else in FCC is hex, so this is a genuine asymmetry.
package teenode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// maxBodyBytes caps a response from the local signing service.
const maxBodyBytes = 1 << 20

// SignatureLength is the length of an Ethereum ECDSA signature: r ‖ s ‖ v.
const SignatureLength = 65

// recoveryIDOffset is what must be added to a go-ethereum recovery id (0 or 1)
// to produce the v that ecrecover expects (27 or 28).
const recoveryIDOffset = 27

// Client calls tee-node's signing service on localhost.
type Client struct {
	baseURL string
	client  *http.Client

	mu    sync.Mutex
	teeID common.Address
}

// New returns a Client for the given sign port.
func New(signPort int, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		// Loopback only. The signing service is not authenticated because it is
		// not reachable off-host; pointing this at anything else would hand the
		// TEE identity key's signing oracle to a remote party.
		baseURL: fmt.Sprintf("http://localhost:%d", signPort),
		client:  &http.Client{Timeout: timeout},
	}
}

// signRequest mirrors tee-node's types.SignRequest.
//
// Declared here rather than imported so the wire format this package depends on
// is visible in one place. TestWireFormatMatchesTeeNode asserts the two stay
// interchangeable.
type signRequest struct {
	// Message is base64 in JSON: Go renders []byte that way.
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

// Sign signs message with the TEE identity key and returns a 65-byte signature
// whose v is already normalised to 27/28.
//
// The digest tee-node signs is
// keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message)), so a
// verifier must apply the EIP-191 prefix to keccak256(message).
func (c *Client) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if len(message) == 0 {
		return nil, failure.Internal
	}

	var resp signResponse
	if err := c.post(ctx, "/sign", signRequest{Message: message}, &resp); err != nil {
		return nil, err
	}

	if len(resp.Signature) != SignatureLength {
		return nil, failure.SignRejected
	}

	sig, err := NormalizeV(resp.Signature)
	if err != nil {
		return nil, failure.SignRejected
	}
	return sig, nil
}

// Decrypt unseals a secret that was encrypted to the TEE public key.
//
// The plaintext is a credential. It must never be logged, echoed into an error,
// or placed in any value that reaches ActionResult.Log or GET /state.
func (c *Client) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, failure.Internal
	}

	var resp decryptResponse
	if err := c.post(ctx, "/decrypt", decryptRequest{EncryptedMessage: ciphertext}, &resp); err != nil {
		return nil, err
	}
	if len(resp.DecryptedMessage) == 0 {
		return nil, failure.SignRejected
	}
	return resp.DecryptedMessage, nil
}

// post performs one JSON round trip. Errors are vocabulary values: the response
// body from a failed decrypt could contain key material.
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return failure.Internal
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return failure.Internal
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return failure.SignUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return failure.SignRejected
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return failure.SignUnavailable
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return failure.SignRejected
	}
	return nil
}

// NormalizeV converts a go-ethereum signature's recovery id (0 or 1) into the v
// that ecrecover and OpenZeppelin's ECDSA.recover require (27 or 28).
//
// Normalising here, on the extension side, rather than in the contract is
// deliberate: it keeps UsdxPriceStore using stock OpenZeppelin with no local
// modification, which is the version an auditor already knows how to read. The
// cost is that the published signature is not byte-identical to what POST /sign
// returned — v differs by 27 — which is noted in the payload documentation.
//
// A signature already at 27/28 is returned unchanged, so this is idempotent and
// safe to apply twice.
func NormalizeV(sig []byte) ([]byte, error) {
	if len(sig) != SignatureLength {
		return nil, errors.New("teenode: signature must be 65 bytes")
	}

	out := make([]byte, SignatureLength)
	copy(out, sig)

	switch out[64] {
	case 0, 1:
		out[64] += recoveryIDOffset
	case 27, 28:
		// Already normalised.
	default:
		return nil, errors.New("teenode: signature has an invalid recovery id")
	}
	return out, nil
}

// teeIDProbe is the message signed to discover this machine's own address.
//
// Domain-separated and constant, so the resulting signature can never be a
// valid price report or provisioning envelope. tee-node exposes no "who am I"
// endpoint, and signing a known message is the only way to learn the identity
// key's address without adding plumbing outside the enclave.
const teeIDProbe = "USDX_ORACLE_TEE_ID_PROBE_V1"

// TeeID returns the address of this machine's TEE identity key.
//
// Resolved by signing a fixed probe and recovering the signer, then cached: the
// key does not change for the life of the process.
func (c *Client) TeeID(ctx context.Context) (common.Address, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.teeID != (common.Address{}) {
		return c.teeID, nil
	}

	message := []byte(teeIDProbe)
	sig, err := c.Sign(ctx, message)
	if err != nil {
		return common.Address{}, err
	}

	// Sign normalises v to 27/28; SigToPub needs the raw recovery id.
	recovery := make([]byte, SignatureLength)
	copy(recovery, sig)
	if recovery[64] >= 27 {
		recovery[64] -= 27
	}

	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(message)), recovery)
	if err != nil || pub == nil {
		return common.Address{}, failure.SignRejected
	}

	c.teeID = crypto.PubkeyToAddress(*pub)
	return c.teeID, nil
}
