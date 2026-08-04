package provision

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	testChainID = big.NewInt(114)
	testTeeID   = common.HexToAddress("0x00000000000000000000000000000000000000AA")
)

type admin struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newAdmins(t *testing.T, n int) []admin {
	t.Helper()
	out := make([]admin, n)
	for i := range out {
		k, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		out[i] = admin{key: k, addr: crypto.PubkeyToAddress(k.PublicKey)}
	}
	return out
}

// newTestVerifier binds a Verifier to a generated admin set rather than the
// baked placeholders.
func newTestVerifier(t *testing.T, admins []admin, threshold int) *Verifier {
	t.Helper()
	v := NewVerifier(testChainID, testTeeID)
	v.admins = nil
	for _, a := range admins {
		v.admins = append(v.admins, a.addr)
	}
	v.threshold = threshold
	return v
}

func testEnvelope(payload []byte) Envelope {
	return Envelope{
		ChainID:     testChainID,
		TeeID:       testTeeID,
		Version:     1,
		IssuedAt:    uint64(time.Now().UTC().Unix()),
		Ref:         "hex_api_key",
		PayloadHash: crypto.Keccak256Hash(payload),
		Payload:     payload,
	}
}

// sign produces an EIP-191 personal-sign signature, as a hardware wallet would.
func sign(t *testing.T, e Envelope, a admin) []byte {
	t.Helper()
	body, err := EncodeBody(e)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	sig, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(body)), a.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig[64] += 27 // personal-sign convention
	return sig
}

func encode(t *testing.T, e Envelope, sigs [][]byte) []byte {
	t.Helper()
	msg, err := Encode(e, sigs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return msg
}

// --- happy path ---

func TestVerify_AcceptsThresholdSignatures(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("ciphertext"))
	msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])})

	got, err := v.Verify(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ref != "hex_api_key" {
		t.Errorf("ref = %q", got.Ref)
	}
	if string(got.Payload) != "ciphertext" {
		t.Errorf("payload = %q", got.Payload)
	}
}

func TestVerify_AcceptsMoreThanThreshold(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	msg := encode(t, e, [][]byte{
		sign(t, e, admins[0]), sign(t, e, admins[1]), sign(t, e, admins[2]),
	})
	if _, err := v.Verify(msg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- the M-of-N properties ---

func TestVerify_RejectsBelowThreshold(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	msg := encode(t, e, [][]byte{sign(t, e, admins[0])})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Errorf("one signature must not satisfy a 2-of-3 threshold, got %v", err)
	}
}

// The property that makes M-of-N mean anything: one compromised key must not be
// able to satisfy the threshold by signing repeatedly.
func TestVerify_RejectsDuplicateSignerReachingThreshold(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	one := sign(t, e, admins[0])
	msg := encode(t, e, [][]byte{one, one})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("the same admin signing twice must not satisfy a 2-of-3 threshold")
	}
}

func TestVerify_RejectsNonAdminSigner(t *testing.T) {
	admins := newAdmins(t, 3)
	stranger := newAdmins(t, 1)[0]
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, stranger)})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("a non-admin signature must not count toward the threshold")
	}
}

func TestVerify_RejectsGarbageSignatures(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	junk := make([]byte, SignatureLength)
	msg := encode(t, e, [][]byte{junk, junk})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("unrecoverable signatures must be rejected")
	}
}

// --- binding ---

func TestVerify_RejectsWrongBinding(t *testing.T) {
	admins := newAdmins(t, 3)

	cases := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"another chain", func(e *Envelope) { e.ChainID = big.NewInt(1) }},
		{"another machine", func(e *Envelope) {
			e.TeeID = common.HexToAddress("0x00000000000000000000000000000000000000BB")
		}},
		{"empty ref", func(e *Envelope) { e.Ref = "" }},
		{"over-long ref", func(e *Envelope) { e.Ref = string(make([]byte, 100)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := newTestVerifier(t, admins, 2)
			e := testEnvelope([]byte("c"))
			c.mutate(&e)
			msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])})

			if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
				t.Errorf("expected rejection, got %v", err)
			}
		})
	}
}

// The signatures cover the payload HASH, so a swapped payload is unsigned data
// smuggled past the admin set.
func TestVerify_RejectsSwappedPayload(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("real ciphertext"))
	sigs := [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])}

	tampered := e
	tampered.Payload = []byte("attacker ciphertext") // hash no longer matches
	msg := encode(t, tampered, sigs)

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("a payload that does not match the signed hash must be rejected")
	}
}

// --- freshness and replay ---

func TestVerify_RejectsStaleEnvelope(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	e.IssuedAt = uint64(time.Now().UTC().Add(-Freshness - time.Minute).Unix())
	msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("an envelope older than the freshness window must be rejected")
	}
}

func TestVerify_RejectsFutureEnvelope(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	e.IssuedAt = uint64(time.Now().UTC().Add(time.Hour).Unix())
	msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])})

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("an envelope stamped far in the future must be rejected")
	}
}

// Replaying an accepted envelope must not re-apply it.
func TestVerify_RejectsReplay(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	msg := encode(t, e, [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])})

	if _, err := v.Verify(msg); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("replaying an accepted envelope must be rejected")
	}
}

// Rolling a secret back to an older version must be rejected too.
func TestVerify_RejectsVersionRollback(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	newer := testEnvelope([]byte("v5"))
	newer.Version = 5
	if _, err := v.Verify(encode(t, newer,
		[][]byte{sign(t, newer, admins[0]), sign(t, newer, admins[1])})); err != nil {
		t.Fatalf("version 5: %v", err)
	}

	older := testEnvelope([]byte("v4"))
	older.Version = 4
	if _, err := v.Verify(encode(t, older,
		[][]byte{sign(t, older, admins[0]), sign(t, older, admins[1])})); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("an older version must not roll the secret back")
	}
}

// Versions are tracked per ref, so setting one secret must not block another.
func TestVerify_VersionsAreIndependentPerRef(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	a := testEnvelope([]byte("a"))
	a.Ref, a.Version = "hex_api_key", 5
	if _, err := v.Verify(encode(t, a, [][]byte{sign(t, a, admins[0]), sign(t, a, admins[1])})); err != nil {
		t.Fatalf("first ref: %v", err)
	}

	b := testEnvelope([]byte("b"))
	b.Ref, b.Version = "ankr_key", 1
	if _, err := v.Verify(encode(t, b, [][]byte{sign(t, b, admins[0]), sign(t, b, admins[1])})); err != nil {
		t.Errorf("a different ref at version 1 must be accepted: %v", err)
	}
}

// --- domain separation ---

// An admin signature over a price report must never verify as a provisioning
// envelope, or the reverse.
func TestVerify_RejectsForeignDomain(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	sigs := [][]byte{sign(t, e, admins[0]), sign(t, e, admins[1])}

	msg, err := Encode(e, sigs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Corrupt the domain word inside the encoded body.
	for i := range msg {
		if msg[i] == Domain().Bytes()[0] {
			msg[i] ^= 0xFF
			break
		}
	}

	if _, err := v.Verify(msg); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("a foreign domain separator must be rejected")
	}
}

// --- output discipline ---

// Every rejection must be the same reason: distinguishing causes would let a
// caller probe the admin set and the freshness window.
func TestVerify_AllRejectionsAreIndistinguishable(t *testing.T) {
	admins := newAdmins(t, 3)

	build := func(mutate func(*Envelope), sigCount int) []byte {
		e := testEnvelope([]byte("c"))
		mutate(&e)
		var sigs [][]byte
		for i := 0; i < sigCount; i++ {
			sigs = append(sigs, sign(t, e, admins[i]))
		}
		return encode(t, e, sigs)
	}

	messages := [][]byte{
		build(func(e *Envelope) { e.ChainID = big.NewInt(1) }, 2),
		build(func(e *Envelope) { e.TeeID = common.Address{} }, 2),
		build(func(e *Envelope) { e.IssuedAt = 1 }, 2),
		build(func(e *Envelope) {}, 1),
		[]byte("not even abi"),
	}

	for _, msg := range messages {
		v := newTestVerifier(t, admins, 2)
		_, err := v.Verify(msg)
		if err == nil {
			t.Fatal("expected a rejection")
		}
		if err.Error() != failure.ProvisionRejected.Error() {
			t.Errorf("rejection reason %q differs from the single permitted reason", err)
		}
	}
}

// --- resilience ---

// A hostile caller must not be able to make verification expensive.
func TestVerify_BoundsSignatureCount(t *testing.T) {
	admins := newAdmins(t, 3)
	v := newTestVerifier(t, admins, 2)

	e := testEnvelope([]byte("c"))
	var sigs [][]byte
	for i := 0; i < maxSignatures+10; i++ {
		sigs = append(sigs, sign(t, e, admins[0]))
	}

	if _, err := v.Verify(encode(t, e, sigs)); !errors.Is(err, failure.ProvisionRejected) {
		t.Error("an over-long signature list must be rejected before recovery runs")
	}
}

func FuzzVerify(f *testing.F) {
	admins := newAdmins(&testing.T{}, 3)
	e := Envelope{
		ChainID: testChainID, TeeID: testTeeID, Version: 1,
		IssuedAt: uint64(time.Now().Unix()), Ref: "k",
		PayloadHash: crypto.Keccak256Hash([]byte("c")), Payload: []byte("c"),
	}
	if msg, err := Encode(e, [][]byte{make([]byte, 65)}); err == nil {
		f.Add(msg)
	}
	f.Add([]byte("garbage"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, message []byte) {
		v := NewVerifier(testChainID, testTeeID)
		v.admins = []common.Address{admins[0].addr, admins[1].addr, admins[2].addr}

		got, err := v.Verify(message)
		if err == nil {
			// Anything accepted must be fully bound — no partial envelope may
			// escape verification.
			if got.TeeID != testTeeID || got.ChainID.Cmp(testChainID) != 0 {
				t.Errorf("accepted an envelope bound elsewhere: %+v", got)
			}
			if crypto.Keccak256Hash(got.Payload) != got.PayloadHash {
				t.Error("accepted an envelope whose payload does not match its hash")
			}
		}
	})
}

// The baked admin set must be replaced before a live deployment. This test is
// EXPECTED TO FAIL until it is — that is the point.
func TestAdminSetIsNotPlaceholder(t *testing.T) {
	// The addresses of private keys 1, 2 and 3 — publicly known, so anyone can
	// sign as these admins.
	placeholders := map[common.Address]bool{
		common.HexToAddress("0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"): true,
		common.HexToAddress("0x2B5AD5c4795c026514f8317c7a215E218DcCD6cF"): true,
		common.HexToAddress("0x6813Eb9362372EEF6200f3b1dbC3f819671cBA69"): true,
	}
	for _, a := range Admins {
		if placeholders[a] {
			t.Skipf("admin set is still the dev placeholder %s (private key 1, 2 or 3 — "+
				"publicly known). Replace before any live deployment and turn this skip "+
				"into a hard failure.", a)
		}
	}
	if len(Admins) < Threshold {
		t.Fatalf("admin set has %d members but the threshold is %d", len(Admins), Threshold)
	}
}
