package teenode

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// teeNodeSign reproduces tee-node's POST /sign, verified against
// v0.0.21-0.20260619120252-31fc839ae6d2. Every claim about the signing path is
// asserted against it, so a tee-node change fails here.
//
//	crypto.Sign(accounts.TextHash(crypto.Keccak256(message)), privKey)
func teeNodeSign(t *testing.T, message []byte, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	msgHash := crypto.Keccak256(message)
	sig, err := crypto.Sign(accounts.TextHash(msgHash), key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// newKey returns a fresh TEE identity key and its address.
func newKey(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	k, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k, crypto.PubkeyToAddress(k.PublicKey)
}

// --- The three claims about POST /sign ---

// CLAIM 1: crypto.Sign returns v as 0 or 1, NOT 27 or 28. ecrecover and OpenZeppelin
// both require 27/28, so a raw tee-node signature fails on-chain with an "invalid
// signature" that looks nothing like an encoding bug.
func TestClaim1_TeeNodeReturnsRecoveryIDNotV(t *testing.T) {
	key, _ := newKey(t)

	// Many messages, because the recovery id depends on the nonce: a single
	// sample could be 0 or 1 by luck and prove nothing.
	sawZero, sawOne := false, false
	for i := range 200 {
		sig := teeNodeSign(t, []byte{byte(i), 0xAB}, key)

		v := sig[64]
		if v != 0 && v != 1 {
			t.Fatalf("expected a recovery id of 0 or 1, got %d", v)
		}
		if v == 0 {
			sawZero = true
		} else {
			sawOne = true
		}
	}
	if !sawZero || !sawOne {
		t.Errorf("expected both recovery ids across 200 signatures (saw 0:%v 1:%v)", sawZero, sawOne)
	}
}

// CLAIM 2: EIP-191 prefixing is applied BY tee-node, so a verifier must recover
// against toEthSignedMessageHash(keccak256(body)). The bare hash succeeds but returns
// the WRONG address, which presents on-chain as an unregistered machine.
func TestClaim2_MustRecoverAgainstPrefixedHash(t *testing.T) {
	key, want := newKey(t)
	message := []byte("usdx price report body")

	sig := teeNodeSign(t, message, key)

	inner := crypto.Keccak256(message)

	// Correct: apply the EIP-191 prefix, as the contract must.
	prefixed := accounts.TextHash(inner)
	pub, err := crypto.SigToPub(prefixed, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != want {
		t.Fatalf("prefixed recovery gave %s, want %s", got, want)
	}

	// Wrong: recover against the bare hash. This does not error — it silently
	// yields a different address.
	badPub, err := crypto.SigToPub(inner, sig)
	if err != nil {
		return // erroring is also an acceptable outcome
	}
	if got := crypto.PubkeyToAddress(*badPub); got == want {
		t.Error("recovery against the unprefixed hash returned the correct address; " +
			"the EIP-191 step would then be untested and could silently regress")
	}
}

// CLAIM 3: the sign wire is base64, not hex. Everything else in FCC is hex.
func TestClaim3_WireIsBase64NotHex(t *testing.T) {
	body, err := json.Marshal(signRequest{Message: []byte{0xDE, 0xAD, 0xBE, 0xEF}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := string(body)
	if !strings.Contains(got, `"3q2+7w=="`) {
		t.Errorf("expected base64-encoded message, got %s", got)
	}
	if strings.Contains(got, "0xdeadbeef") || strings.Contains(got, "deadbeef") {
		t.Errorf("message was hex-encoded; tee-node expects base64: %s", got)
	}
}

// Our locally declared structs must stay interchangeable with tee-node's.
func TestWireFormatMatchesTeeNode(t *testing.T) {
	ours, err := json.Marshal(signRequest{Message: []byte("hello")})
	if err != nil {
		t.Fatalf("marshal ours: %v", err)
	}
	theirs, err := json.Marshal(teetypes.SignRequest{Message: []byte("hello")})
	if err != nil {
		t.Fatalf("marshal theirs: %v", err)
	}
	if string(ours) != string(theirs) {
		t.Errorf("SignRequest diverged from tee-node:\n ours:   %s\n theirs: %s", ours, theirs)
	}

	oursResp, _ := json.Marshal(signResponse{Message: []byte("m"), Signature: []byte("s")})
	theirsResp, _ := json.Marshal(teetypes.SignResponse{Message: []byte("m"), Signature: []byte("s")})
	if string(oursResp) != string(theirsResp) {
		t.Errorf("SignResponse diverged from tee-node:\n ours:   %s\n theirs: %s", oursResp, theirsResp)
	}
}

// --- NormalizeV ---

func TestNormalizeV(t *testing.T) {
	sig := make([]byte, SignatureLength)

	for _, c := range []struct{ in, want byte }{
		{0, 27}, {1, 28},
		{27, 27}, {28, 28}, // idempotent
	} {
		sig[64] = c.in
		got, err := NormalizeV(sig)
		if err != nil {
			t.Fatalf("v=%d: unexpected error %v", c.in, err)
		}
		if got[64] != c.want {
			t.Errorf("v=%d normalised to %d, want %d", c.in, got[64], c.want)
		}
	}
}

func TestNormalizeV_Rejects(t *testing.T) {
	sig := make([]byte, SignatureLength)
	for _, v := range []byte{2, 26, 29, 255} {
		sig[64] = v
		if _, err := NormalizeV(sig); err == nil {
			t.Errorf("v=%d: expected an error", v)
		}
	}
	for _, n := range []int{0, 64, 66} {
		if _, err := NormalizeV(make([]byte, n)); err == nil {
			t.Errorf("length %d: expected an error", n)
		}
	}
}

// NormalizeV must not mutate its input: the caller may still need the original.
func TestNormalizeV_DoesNotMutateInput(t *testing.T) {
	sig := make([]byte, SignatureLength)
	sig[64] = 1

	if _, err := NormalizeV(sig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig[64] != 1 {
		t.Errorf("input was mutated: v is now %d", sig[64])
	}
}

// The end-to-end property the store depends on: a signature that came from
// tee-node, normalised by this package, recovers to the TEE address when
// verified the way stock OpenZeppelin does it.
func TestSignedReportRecoversAfterNormalisation(t *testing.T) {
	key, want := newKey(t)
	body := []byte("abi-encoded price report")

	raw := teeNodeSign(t, body, key)
	normalised, err := NormalizeV(raw)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}

	if normalised[64] != 27 && normalised[64] != 28 {
		t.Fatalf("v is %d; ecrecover requires 27 or 28", normalised[64])
	}

	// What the feed store will do: ECDSA.recover(toEthSignedMessageHash(keccak256(body)), sig).
	// crypto.SigToPub needs the 0/1 form, so undo the offset for this check —
	// the contract's ecrecover does the equivalent internally.
	forRecovery := make([]byte, SignatureLength)
	copy(forRecovery, normalised)
	forRecovery[64] -= 27

	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), forRecovery)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub); got != want {
		t.Errorf("recovered %s, want %s", got, want)
	}
}

// --- Client ---

func TestClient_Sign(t *testing.T) {
	key, _ := newKey(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req signRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(signResponse{
			Message:   req.Message,
			Signature: teeNodeSign(t, req.Message, key),
		})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, client: srv.Client()}

	sig, err := c.Sign(context.Background(), []byte("report"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sig) != SignatureLength {
		t.Fatalf("signature length %d, want %d", len(sig), SignatureLength)
	}
	// The client normalises, so callers never see a raw recovery id.
	if sig[64] != 27 && sig[64] != 28 {
		t.Errorf("client returned v=%d; it must normalise to 27/28", sig[64])
	}
}

func TestClient_SignFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    failure.Failure
	}{
		{"service error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, failure.SignRejected},
		{"garbage body", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}, failure.SignRejected},
		{"short signature", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(signResponse{Signature: []byte{1, 2, 3}})
		}, failure.SignRejected},
		{"invalid recovery id", func(w http.ResponseWriter, r *http.Request) {
			sig := make([]byte, SignatureLength)
			sig[64] = 99
			_ = json.NewEncoder(w).Encode(signResponse{Signature: sig})
		}, failure.SignRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(c.handler)
			defer srv.Close()
			cl := &Client{baseURL: srv.URL, client: srv.Client()}

			if _, err := cl.Sign(context.Background(), []byte("m")); !errors.Is(err, c.want) {
				t.Errorf("expected %v, got %v", c.want, err)
			}
		})
	}
}

func TestClient_SignUnavailable(t *testing.T) {
	// A port nothing is listening on.
	c := &Client{baseURL: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	if _, err := c.Sign(context.Background(), []byte("m")); !errors.Is(err, failure.SignUnavailable) {
		t.Errorf("expected SignUnavailable, got %v", err)
	}
}

func TestClient_Decrypt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req decryptRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(decryptResponse{DecryptedMessage: []byte("api-key-plaintext")})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, client: srv.Client()}
	got, err := c.Decrypt(context.Background(), []byte("ciphertext"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "api-key-plaintext" {
		t.Errorf("got %q", got)
	}
}

// Errors from this package reach ActionResult.Log, and a failed decrypt's
// response body could contain key material.
func TestClient_ErrorsArePublishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cannot decrypt key SECRET_MATERIAL_HERE", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, client: srv.Client()}
	_, err := c.Decrypt(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "SECRET_MATERIAL_HERE") {
		t.Errorf("error leaks the service response body: %v", err)
	}
	var f failure.Failure
	if !errors.As(err, &f) {
		t.Errorf("error %v is not a failure.Failure", err)
	}
}
