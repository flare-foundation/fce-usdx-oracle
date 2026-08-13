package pricereport

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// build produces a payload the way the extension does: pack the body, sign the
// EIP-191 digest of it, normalise v to the 27/28 form the store requires, then
// wrap both halves.
func build(t *testing.T, domain common.Hash, price *big.Int, decimals int8, observedAt uint64,
	instructionID, configHash, adminsHash common.Hash) (data []byte, signer common.Address) {
	t.Helper()

	body, err := bodyArgs.Pack(domain, price, decimals, observedAt, instructionID, configHash, adminsHash)
	if err != nil {
		t.Fatalf("packing the body: %v", err)
	}

	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318")
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	sig, err := crypto.Sign(EIP191Digest(body), key)
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	sig[64] += 27

	data, err = dataArgs.Pack(body, sig)
	if err != nil {
		t.Fatalf("packing the envelope: %v", err)
	}
	return data, crypto.PubkeyToAddress(key.PublicKey)
}

func validPayload(t *testing.T) ([]byte, common.Address) {
	t.Helper()
	return build(t, Domain(), big.NewInt(99_950_000), 8, 1_754_400_000,
		common.HexToHash("0xabc"), common.HexToHash("0xdef"), common.HexToHash("0x5e21"))
}

func TestDecode(t *testing.T) {
	data, want := validPayload(t)

	r, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if r.Price.Cmp(big.NewInt(99_950_000)) != 0 {
		t.Errorf("price = %s, want 99950000", r.Price)
	}
	if r.Decimals != 8 {
		t.Errorf("decimals = %d, want 8", r.Decimals)
	}
	if r.ObservedAt != 1_754_400_000 {
		t.Errorf("observedAt = %d, want 1754400000", r.ObservedAt)
	}
	if r.InstructionID != common.HexToHash("0xabc") {
		t.Errorf("instructionId = %s", r.InstructionID.Hex())
	}
	if r.ConfigHash != common.HexToHash("0xdef") {
		t.Errorf("configHash = %s", r.ConfigHash.Hex())
	}
	if r.AdminsHash != common.HexToHash("0x5e21") {
		t.Errorf("adminsHash = %s", r.AdminsHash.Hex())
	}
	if len(r.Body) != BodyLength {
		t.Errorf("body is %d bytes, want %d", len(r.Body), BodyLength)
	}

	got, err := r.Signer()
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	if got != want {
		t.Errorf("Signer() = %s, want %s", got.Hex(), want.Hex())
	}
}

// The body handed to submitPrice must be the bytes that were signed, not a
// re-encoding of the decoded fields. If these ever differ, the store rejects a
// perfectly good report as an invalid signature and the cause is invisible.
func TestDecode_BodyIsCarriedVerbatim(t *testing.T) {
	data, _ := validPayload(t)

	r, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	reencoded, err := bodyArgs.Pack(r.Domain, r.Price, r.Decimals, r.ObservedAt,
		r.InstructionID, r.ConfigHash, r.AdminsHash)
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	if string(reencoded) != string(r.Body) {
		t.Fatal("re-encoding does not reproduce the body; Body must be relayed as received")
	}

	// And the signature must verify over exactly those bytes.
	if _, err := r.Signer(); err != nil {
		t.Errorf("the carried body does not verify: %v", err)
	}
}

// A payload signed under another schema must be refused here rather than read
// as a price. The domain is the only thing standing between the two.
func TestDecode_RejectsWrongDomain(t *testing.T) {
	data, _ := build(t, crypto.Keccak256Hash([]byte("USDX_ORACLE_PRICE_V2")),
		big.NewInt(1), 8, 1, common.Hash{}, common.Hash{}, common.Hash{})

	_, err := Decode(data)
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("expected a domain error, got %v", err)
	}
}

func TestDecode_RejectsMalformedEnvelopes(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"not ABI", []byte{1, 2, 3}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode(c.data); err == nil {
				t.Error("expected an error")
			}
		})
	}

	// A body of the wrong length decodes as ABI but is not a report; catching
	// it here is what keeps a truncated payload from reaching the chain.
	short, err := dataArgs.Pack(make([]byte, 32), make([]byte, 65))
	if err != nil {
		t.Fatalf("packing: %v", err)
	}
	if _, err := Decode(short); err == nil {
		t.Error("expected a short body to be rejected")
	}
}

// The store requires v in {27, 28} and rejects anything else with
// BadRecoveryId. Recovering off-chain has to agree, or the tooling would
// cheerfully relay a report the contract cannot accept.
func TestSigner_RequiresNormalisedV(t *testing.T) {
	data, _ := validPayload(t)
	r, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	for _, v := range []byte{0, 1, 29} {
		bad := r
		bad.Signature = append([]byte(nil), r.Signature...)
		bad.Signature[64] = v
		if _, err := bad.Signer(); err == nil {
			t.Errorf("v=%d: expected BadRecoveryId", v)
		}
	}

	short := r
	short.Signature = r.Signature[:64]
	if _, err := short.Signer(); err == nil {
		t.Error("expected BadSignatureLength for a 64-byte signature")
	}
}

func TestHuman(t *testing.T) {
	cases := []struct {
		price    int64
		decimals int8
		want     string
	}{
		{100_000_000, 8, "1.00000000"},
		{99_999_999, 8, "0.99999999"},
		{99_000_000, 8, "0.99000000"},
		{5_000_000, 8, "0.05000000"},
		{0, 8, "0.00000000"},
	}
	for _, c := range cases {
		r := Report{Price: big.NewInt(c.price), Decimals: c.decimals}
		if got := r.Human(); got != c.want {
			t.Errorf("Human(%d) = %s, want %s", c.price, got, c.want)
		}
	}
}
