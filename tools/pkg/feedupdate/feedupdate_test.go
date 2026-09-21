package feedupdate

import (
	"math/big"
	"testing"

	wire "fce-usdx-oracle/pkg/wire/feedupdate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// From test/FeedUpdateVector.t.sol. The encoding itself is the extension's and
// is pinned there; this pins the digest this package hands to recovery.
const (
	vectorChainID = 114
	vectorDigest  = "0x69cab05978fc8e14b84a1ef06c4fbffc91604cb27aee63cea454ad3c7974ed7c"
	vectorPrefix  = "0x5445455f4f5241434c455f464545440000000000000000000000000000000000"
)

func vectorUpdate() Update {
	var feedID [21]byte
	copy(feedID[:], common.FromHex("0x21555344582f555344000000000000000000000000"))
	return Update{
		FeedUpdate: FeedUpdate{
			ExtensionID:   big.NewInt(66281),
			FeedID:        feedID,
			Value:         99950000,
			Decimals:      8,
			ObservedAt:    1787230941,
			EndpointsHash: crypto.Keccak256Hash([]byte("endpoints")),
			AdminsHash:    crypto.Keccak256Hash([]byte("admins")),
		},
		Signature: Signature{V: 28},
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

// Decode reverses the extension's own encoding, field for field.
func TestDecodeRoundTripsTheExtensionsEncoding(t *testing.T) {
	u := vectorUpdate()
	sig := make([]byte, 65)
	copy(sig[:32], u.R.Bytes())
	copy(sig[32:64], u.S.Bytes())
	sig[64] = u.V

	data, err := wire.EncodeData(u.FeedUpdate, sig)
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ExtensionID.Cmp(u.ExtensionID) != 0 || got.FeedID != u.FeedID || got.Value != u.Value ||
		got.Decimals != u.Decimals || got.ObservedAt != u.ObservedAt ||
		got.EndpointsHash != u.EndpointsHash || got.AdminsHash != u.AdminsHash || got.V != u.V {
		t.Fatalf("round trip changed the update:\n got %+v\nwant %+v", got, u)
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
	if got := u.FeedName(); got != "33:USDX/USD" {
		t.Errorf("feed name = %q, want 33:USDX/USD", got)
	}
}
