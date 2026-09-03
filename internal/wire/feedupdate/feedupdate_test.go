package feedupdate

import (
	"bytes"
	"encoding/hex"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The golden vector below is produced by test/FeedUpdateVector.t.sol, which
// builds the same update from the struct as it is declared upstream in
// ITeeOracleFeedStore and runs it through the real SignedPayload formula.
// Regenerate with:
//
//	forge test --match-path test/FeedUpdateVector.t.sol -vv
//
// If one of these constants has to change, the wire format changed: check that
// the store was redeployed before assuming the Go side is what is wrong.
const (
	vectorChainID     = uint64(114)
	vectorPrefix      = "0x5445455f4f5241434c455f464545440000000000000000000000000000000000"
	vectorDataHash    = "0x94af4017f9a19345b7c295f07502dca8746bc840ed1fc0d614effe7bbab9fc41"
	vectorMessageHash = "0x45a9a51f2897e6b7d5504fd233158a2a883f744921248b386758b944d3e87f85"
	vectorDigest      = "0x66612a9ecc7d37e6331f8728af9b34881d55efdb3d76f8b73e56d4ae0cf195f2"
)

func vectorFeedID() [21]byte {
	raw, err := hex.DecodeString("20555344582f555344000000000000000000000000")
	if err != nil {
		panic(err)
	}
	var id [21]byte
	copy(id[:], raw)
	return id
}

func vectorUpdate() FeedUpdate {
	return FeedUpdate{
		ExtensionID:   big.NewInt(66281),
		FeedID:        vectorFeedID(),
		Value:         99950000,
		Decimals:      8,
		ObservedAt:    1787230941,
		EndpointsHash: crypto.Keccak256Hash([]byte("endpoints")),
		AdminsHash:    crypto.Keccak256Hash([]byte("admins")),
	}
}

// TestPrefixMatchesSolidity guards the mistake the old domain invites: the
// prefix is bytes32("TEE_ORACLE_FEED"), not keccak256 of it.
func TestPrefixMatchesSolidity(t *testing.T) {
	if got := Prefix().Hex(); got != vectorPrefix {
		t.Fatalf("prefix = %s, want %s", got, vectorPrefix)
	}
	if Prefix() == crypto.Keccak256Hash([]byte(PrefixName)) {
		t.Fatal("prefix must not be a keccak of the name")
	}
}

// TestDigestMatchesSolidity is the cross-check the whole wire format rests on.
func TestDigestMatchesSolidity(t *testing.T) {
	u := vectorUpdate()

	encoded, err := EncodeUpdate(u)
	if err != nil {
		t.Fatalf("EncodeUpdate: %v", err)
	}
	if len(encoded) != UpdateLength {
		t.Fatalf("encoded length = %d, want %d", len(encoded), UpdateLength)
	}

	dataHash, err := DataHash(u)
	if err != nil {
		t.Fatalf("DataHash: %v", err)
	}
	if dataHash.Hex() != vectorDataHash {
		t.Errorf("dataHash = %s, want %s", dataHash.Hex(), vectorDataHash)
	}

	message, err := SigningMessage(u, vectorChainID)
	if err != nil {
		t.Fatalf("SigningMessage: %v", err)
	}
	if len(message) != PayloadLength {
		t.Errorf("signing message length = %d, want %d", len(message), PayloadLength)
	}

	messageHash, err := MessageHash(u, vectorChainID)
	if err != nil {
		t.Fatalf("MessageHash: %v", err)
	}
	if messageHash.Hex() != vectorMessageHash {
		t.Errorf("messageHash = %s, want %s", messageHash.Hex(), vectorMessageHash)
	}

	digest, err := Digest(u, vectorChainID)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if digest.Hex() != vectorDigest {
		t.Errorf("digest = %s, want %s", digest.Hex(), vectorDigest)
	}
}

// TestSigningMessageIsThePreimage pins the tee-node contract: the signer is
// handed the preimage and hashes it itself, so keccak of what we send must be
// the message hash the store recomputes. Sending the hash instead would sign
// keccak256(messageHash) and fail only as a bad signature.
func TestSigningMessageIsThePreimage(t *testing.T) {
	u := vectorUpdate()

	message, err := SigningMessage(u, vectorChainID)
	if err != nil {
		t.Fatalf("SigningMessage: %v", err)
	}
	messageHash, err := MessageHash(u, vectorChainID)
	if err != nil {
		t.Fatalf("MessageHash: %v", err)
	}

	if crypto.Keccak256Hash(message) != messageHash {
		t.Fatal("keccak256(SigningMessage) != MessageHash")
	}
	if bytes.Equal(message, messageHash.Bytes()) {
		t.Fatal("SigningMessage returned the hash rather than its preimage")
	}
}

// TestChainIDIsBound is what replaced the version suffix on the old domain.
func TestChainIDIsBound(t *testing.T) {
	u := vectorUpdate()

	a, err := MessageHash(u, vectorChainID)
	if err != nil {
		t.Fatalf("MessageHash: %v", err)
	}
	b, err := MessageHash(u, vectorChainID+1)
	if err != nil {
		t.Fatalf("MessageHash: %v", err)
	}
	if a == b {
		t.Fatal("message hash does not depend on the chain id")
	}

	if _, err := SigningMessage(u, 0); err == nil {
		t.Fatal("chain id 0 must be rejected")
	}
}

// TestFeedBindingIsInTheDigest covers the cross-feed replay the store relies on
// the payload to prevent.
func TestFeedBindingIsInTheDigest(t *testing.T) {
	base, err := DataHash(vectorUpdate())
	if err != nil {
		t.Fatalf("DataHash: %v", err)
	}

	other := vectorUpdate()
	other.ExtensionID = big.NewInt(66282)
	extChanged, err := DataHash(other)
	if err != nil {
		t.Fatalf("DataHash: %v", err)
	}
	if extChanged == base {
		t.Error("data hash does not depend on the extension id")
	}

	other = vectorUpdate()
	other.FeedID[20] = 0x01
	feedChanged, err := DataHash(other)
	if err != nil {
		t.Fatalf("DataHash: %v", err)
	}
	if feedChanged == base {
		t.Error("data hash does not depend on the feed id")
	}
}

func TestDataRoundTrip(t *testing.T) {
	u := vectorUpdate()

	signature := make([]byte, SignatureLength)
	copy(signature[:32], crypto.Keccak256([]byte("r")))
	copy(signature[32:64], crypto.Keccak256([]byte("s")))
	signature[64] = 28

	data, err := EncodeData(u, signature)
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}
	if len(data) != DataLength {
		t.Fatalf("data length = %d, want %d", len(data), DataLength)
	}

	got, sig, err := DecodeData(data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}

	if got.ExtensionID.Cmp(u.ExtensionID) != 0 || got.FeedID != u.FeedID ||
		got.Value != u.Value || got.Decimals != u.Decimals ||
		got.ObservedAt != u.ObservedAt || got.EndpointsHash != u.EndpointsHash ||
		got.AdminsHash != u.AdminsHash {
		t.Errorf("round trip changed the update: %+v", got)
	}
	if sig.V != 28 || sig.R != common.BytesToHash(signature[:32]) ||
		sig.S != common.BytesToHash(signature[32:64]) {
		t.Errorf("round trip changed the signature: %+v", sig)
	}
}

// TestDecodeDataRejectsWrongLength: Unpack would read only the words it expects
// from a longer buffer, so a peer on a later layout must be rejected here.
func TestDecodeDataRejectsWrongLength(t *testing.T) {
	u := vectorUpdate()
	signature := make([]byte, SignatureLength)
	signature[64] = 27

	data, err := EncodeData(u, signature)
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"short", data[:len(data)-32]},
		{"long", append(append([]byte{}, data...), make([]byte, 32)...)},
	} {
		if _, _, err := DecodeData(tc.data); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

func TestSplitSignatureRequiresNormalisedV(t *testing.T) {
	sig := make([]byte, SignatureLength)

	for _, v := range []byte{0, 1, 26, 29, 255} {
		sig[64] = v
		if _, err := SplitSignature(sig); err == nil {
			t.Errorf("v = %d must be rejected", v)
		}
	}
	for _, v := range []byte{27, 28} {
		sig[64] = v
		if _, err := SplitSignature(sig); err != nil {
			t.Errorf("v = %d must be accepted: %v", v, err)
		}
	}
	if _, err := SplitSignature(sig[:64]); err == nil {
		t.Error("a 64-byte signature must be rejected")
	}
}

// TestValueFromBig fails closed rather than truncating — a wrapped value would
// be published as fact.
func TestValueFromBig(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   *big.Int
		want int32
		ok   bool
	}{
		{"par at 8 decimals", big.NewInt(100000000), 100000000, true},
		{"max", big.NewInt(math.MaxInt32), math.MaxInt32, true},
		{"min", big.NewInt(math.MinInt32), math.MinInt32, true},
		{"nil", nil, 0, false},
		{"over", big.NewInt(math.MaxInt32 + 1), 0, false},
		{"under", big.NewInt(math.MinInt32 - 1), 0, false},
		{"far over", new(big.Int).Lsh(big.NewInt(1), 200), 0, false},
	} {
		got, err := ValueFromBig(tc.in)
		if tc.ok != (err == nil) {
			t.Errorf("%s: err = %v, want ok = %v", tc.name, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestEncodeRejectsStoreSentinels: each of these is a value the store treats as
// "nothing published", so an update carrying one could never be accepted.
func TestEncodeRejectsStoreSentinels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*FeedUpdate)
	}{
		{"zero extension id", func(u *FeedUpdate) { u.ExtensionID = big.NewInt(0) }},
		{"nil extension id", func(u *FeedUpdate) { u.ExtensionID = nil }},
		{"zero feed id", func(u *FeedUpdate) { u.FeedID = [21]byte{} }},
		{"zero observedAt", func(u *FeedUpdate) { u.ObservedAt = 0 }},
		{"zero endpoints hash", func(u *FeedUpdate) { u.EndpointsHash = common.Hash{} }},
		{"zero admins hash", func(u *FeedUpdate) { u.AdminsHash = common.Hash{} }},
		{"negative decimals", func(u *FeedUpdate) { u.Decimals = -1 }},
	} {
		u := vectorUpdate()
		tc.mutate(&u)
		if _, err := EncodeUpdate(u); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}
