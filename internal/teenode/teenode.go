// Package teenode talks to tee-node's local signing service on
// http://localhost:$SIGN_PORT: POST /sign and POST /decrypt.
//
// tee-node signs keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(msg)),
// so three things follow, each of which breaks verification silently if missed
// and each pinned by a test here:
//
//  1. EIP-191 prefixing is applied by tee-node, so the contract must recover
//     against toEthSignedMessageHash(keccak256(body)), not the bare hash.
//  2. crypto.Sign returns v as 0/1 but ecrecover needs 27/28. We normalise on
//     the way out so the contract can use stock OpenZeppelin.
//  3. The wire is base64, not hex — encoding/json renders []byte that way.
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

const maxBodyBytes = 1 << 20

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

const recoveryIDOffset = 27

// Client calls tee-node's signing service on localhost.
type Client struct {
	baseURL string
	client  *http.Client

	mu    sync.Mutex
	teeID common.Address
}

func New(signPort int, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Client{
		// Loopback only: the signing service is unauthenticated because it is
		// not reachable off-host.
		baseURL: fmt.Sprintf("http://localhost:%d", signPort),
		client:  &http.Client{Timeout: timeout},
	}
}

// signRequest mirrors tee-node's types.SignRequest. Declared rather than
// imported so the wire format is visible in one place;
// TestWireFormatMatchesTeeNode asserts the two stay interchangeable.
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

// Sign returns a 65-byte signature whose v is already normalised to 27/28.
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

// Decrypt unseals a secret encrypted to the TEE public key. The plaintext is a
// credential: never log it, echo it into an error, or let it reach
// ActionResult.Log or the readiness report.
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
	defer func() { _ = resp.Body.Close() }()

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

// NormalizeV converts a go-ethereum recovery id (0 or 1) into the v ecrecover
// requires (27 or 28). Done here rather than in the contract so the feed store
// uses stock OpenZeppelin. Idempotent.
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

// teeIDProbe is domain-separated and constant, so its signature can never be a
// valid price report or provisioning envelope. tee-node exposes no "who am I"
// endpoint, so signing a known message is the only way to learn the address.
const teeIDProbe = "USDX_ORACLE_TEE_ID_PROBE_V1"

// TeeID returns the address of this machine's TEE identity key, cached: the key
// does not change for the life of the process.
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
