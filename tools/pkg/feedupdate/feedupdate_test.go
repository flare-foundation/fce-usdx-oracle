package feedupdate

import (
	"encoding/hex"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// From test/FeedUpdateVector.t.sol, shared with the enclave's encoder in
// internal/wire/feedupdate. See deploy/testdata/wire/README.md for why both are pinned to
// Solidity's output rather than to each other.
const (
	vectorChainID = 114
	vectorDigest  = "0x66612a9ecc7d37e6331f8728af9b34881d55efdb3d76f8b73e56d4ae0cf195f2"
	vectorPrefix  = "0x5445455f4f5241434c455f464545440000000000000000000000000000000000"

	updateFile   = "../../../deploy/testdata/wire/feed-update.abi.hex"
	preimageFile = "../../../deploy/testdata/wire/feed-update-payload-preimage.abi.hex"
)

func mustFixture(t *testing.T, path string) []byte {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(text)))
	if err != nil {
		t.Fatalf("bad fixture hex in %s: %v", path, err)
	}
	return raw
}

func vectorUpdate() Update {
	var feedID [21]byte
	copy(feedID[:], common.FromHex("0x20555344582f555344000000000000000000000000"))
	return Update{
		ExtensionID:   big.NewInt(66281),
		FeedID:        feedID,
		Value:         99950000,
		Decimals:      8,
		ObservedAt:    1787230941,
		EndpointsHash: crypto.Keccak256Hash([]byte("endpoints")),
		AdminsHash:    crypto.Keccak256Hash([]byte("admins")),
		V:             28,
	}
}

func TestPrefixMatchesSolidity(t *testing.T) {
	if got := Prefix().Hex(); got != vectorPrefix {
		t.Fatalf("prefix = %s, want %s", got, vectorPrefix)
	}
	if Prefix() == crypto.Keccak256Hash([]byte(PrefixName)) {
		t.Fatal("prefix must not be a keccak of the name")
	}
}

// The digest is what the store recovers over. If this relayer computes it
// differently from the enclave, every update it forwards looks like it was
// signed by a stranger.
func TestDigestMatchesSolidity(t *testing.T) {
	got, err := vectorUpdate().Digest(big.NewInt(vectorChainID))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if got.Hex() != vectorDigest {
		t.Fatalf("digest = %s, want %s", got.Hex(), vectorDigest)
	}
}

// The encoded body and the SignedPayload preimage must match Solidity's, since
// the digest is built from them.
func TestEncodingMatchesSolidity(t *testing.T) {
	u := vectorUpdate()

	body, err := updateArgs.Pack(
		u.ExtensionID, u.FeedID, u.Value, u.Decimals, u.ObservedAt,
		u.EndpointsHash, u.AdminsHash,
	)
	if err != nil {
		t.Fatalf("packing update: %v", err)
	}
	if want := mustFixture(t, updateFile); string(body) != string(want) {
		t.Errorf("feed update encoding differs\n got %x\nwant %x", body, want)
	}

	preimage, err := payloadArgs.Pack(
		Prefix(), big.NewInt(vectorChainID), crypto.Keccak256Hash(body))
	if err != nil {
		t.Fatalf("packing preimage: %v", err)
	}
	if want := mustFixture(t, preimageFile); string(preimage) != string(want) {
		t.Errorf("signed payload encoding differs\n got %x\nwant %x", preimage, want)
	}
}

// The chain id is inside the signed payload, so the same update recovers to a
// different address against the wrong chain.
func TestDigestIsChainBound(t *testing.T) {
	u := vectorUpdate()

	a, err := u.Digest(big.NewInt(vectorChainID))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	b, err := u.Digest(big.NewInt(vectorChainID + 1))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if a == b {
		t.Error("digest does not depend on the chain id")
	}
	if _, err := u.Digest(nil); err == nil {
		t.Error("a nil chain id must be rejected")
	}
}

// A round trip through the real signing path, so Signer is exercised against a
// signature the store would actually accept.
func TestSignerRecoversTheSigningKey(t *testing.T) {
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}

	u := vectorUpdate()
	digest, err := u.Digest(big.NewInt(vectorChainID))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	u.R = common.BytesToHash(sig[:32])
	u.S = common.BytesToHash(sig[32:64])
	u.V = sig[64] + 27 // the store's OpenZeppelin ECDSA takes 27/28

	got, err := u.Signer(big.NewInt(vectorChainID))
	if err != nil {
		t.Fatalf("Signer: %v", err)
	}
	if want := crypto.PubkeyToAddress(key.PublicKey); got != want {
		t.Fatalf("signer = %s, want %s", got.Hex(), want.Hex())
	}

	// A raw recovery id is a bug upstream of here, not something to tolerate.
	u.V = sig[64]
	if _, err := u.Signer(big.NewInt(vectorChainID)); err == nil {
		t.Error("v of 0/1 must be rejected")
	}
}

func TestDecodeRejectsWrongLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", make([]byte, DataLength-32)},
		{"long", make([]byte, DataLength+32)},
	} {
		if _, err := Decode(tc.data); err == nil {
			t.Errorf("%s: expected rejection", tc.name)
		}
	}
}

func TestPriceRendersAtItsOwnScale(t *testing.T) {
	u := vectorUpdate()
	if got := u.Price(); got != "0.99950000" {
		t.Errorf("price = %q, want 0.99950000", got)
	}
	if got := u.FeedName(); got != "32:USDX/USD" {
		t.Errorf("feed name = %q, want 32:USDX/USD", got)
	}
}
