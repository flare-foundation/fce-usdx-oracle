package runtimecfg

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"fce-usdx-oracle/internal/config"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Golden vectors from test/ConfigPayloadVector.t.sol, which builds the same two
// payloads from the structs as they are declared upstream and prints Solidity's
// own encoding. Regenerate with:
//
//	forge test --match-path test/ConfigPayloadVector.t.sol -vv
//
// These pin the decoder against Solidity rather than against this package's own
// encoder — a round trip through EncodeEndpoints would pass even if both sides
// were wrong in the same way, which is exactly the failure a wire-format change
// invites.
const (
	vectorSalt       = "0xa05e334153147e75f3f416139b5109d1179cb56fef6a4ecb4c4cbc92a7c37b70"
	vectorCommitment = "0x78ccb727d3a566d9f8dc3f5bb3cf1dbbe283b46b5c3533cbcf3ccb75d62692f8"

	vectorEndpointsHash = "0xd241e7c63ba9ab995063996a7cfc5854eacf1fe94af785f18e5ef98795a2614c"
	vectorAdminsHash    = "0xab4e61f34281cf851bf9e996732f647e2f13d69dd3e49bcc99964fa2051ec9da"

	vectorCashURL = "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount"
)

// The two payload blobs live in testdata/wire so this decoder and the
// publisher's encoder in tools/pkg/oraclepayload assert against ONE copy of
// Solidity's output. The packages are deliberately independent — tools/ is a
// cross-check on the extension, not a mirror of it — and a shared fixture is
// what keeps that independence from becoming divergence.
const (
	vectorEndpointsFile = "../../../testdata/wire/endpoints-v7.abi.hex"
	vectorAdminsFile    = "../../../testdata/wire/admins-v3.abi.hex"
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

// TestSaltMatchesSolidity ties Go's keccak to Solidity's before anything else
// leans on it.
func TestSaltMatchesSolidity(t *testing.T) {
	if got := crypto.Keccak256Hash([]byte("salt")).Hex(); got != vectorSalt {
		t.Fatalf("keccak256(\"salt\") = %s, want %s", got, vectorSalt)
	}
}

// TestCommitPrivateURLMatchesSolidity pins the PRIVATE endpoint commitment
// format. It is a build-pinned constant on both sides: a machine that computed
// it differently would refuse every published hex_cash group.
func TestCommitPrivateURLMatchesSolidity(t *testing.T) {
	salt := common.HexToHash(vectorSalt)

	got, err := CommitPrivateURL(PrivateURL{Salt: salt, Url: vectorCashURL})
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	if got.Hex() != vectorCommitment {
		t.Fatalf("commitment = %s, want %s", got.Hex(), vectorCommitment)
	}

	// The salt is what makes publishing the commitment safe: without it the
	// hash of a low-entropy URL is brute-forceable from chain data.
	unsalted, err := CommitPrivateURL(PrivateURL{Salt: common.Hash{}, Url: vectorCashURL})
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	if unsalted == got {
		t.Fatal("commitment does not depend on the salt")
	}
}

func TestDecodePrivateURLRoundTrip(t *testing.T) {
	salt := common.HexToHash(vectorSalt)

	preimage, err := EncodePrivateURL(PrivateURL{Salt: salt, Url: vectorCashURL})
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	got, ok := decodePrivateURL(preimage)
	if !ok {
		t.Fatal("decodePrivateURL rejected its own encoding")
	}
	if got.Salt != salt || got.Url != vectorCashURL {
		t.Fatalf("round trip changed (salt, url): %s %q", got.Salt.Hex(), got.Url)
	}

	if _, ok := decodePrivateURL([]byte("not abi")); ok {
		t.Error("garbage must not decode")
	}
}

// TestDecodeEndpointsMatchesSolidity decodes Solidity's own bytes.
func TestDecodeEndpointsMatchesSolidity(t *testing.T) {
	payload := mustFixture(t, vectorEndpointsFile)

	if got := crypto.Keccak256Hash(payload).Hex(); got != vectorEndpointsHash {
		t.Fatalf("fixture hash = %s, want %s", got, vectorEndpointsHash)
	}

	wire, err := decodeEndpoints(payload)
	if err != nil {
		t.Fatalf("decodeEndpoints: %v", err)
	}
	e, err := convertEndpoints(wire)
	if err != nil {
		t.Fatalf("convertEndpoints: %v", err)
	}

	if e.Version != 7 {
		t.Errorf("version = %d, want 7", e.Version)
	}
	if e.FeedID != config.FeedID {
		t.Errorf("feed id = %#x, want %#x", e.FeedID, config.FeedID)
	}
	if len(e.Groups) != 4 {
		t.Fatalf("groups = %d, want 4", len(e.Groups))
	}

	flare, ok := e.Groups["flare"]
	if !ok {
		t.Fatal("flare group is missing")
	}
	if flare.Threshold != 2 || len(flare.Endpoints) != 2 {
		t.Errorf("flare = threshold %d, %d endpoints; want 2, 2", flare.Threshold, len(flare.Endpoints))
	}
	if flare.Endpoints[0].URL != "https://flare-api.flare.network/ext/C/rpc" ||
		flare.Endpoints[0].Kind != KindPublic || flare.Endpoints[0].SecretRef != "" {
		t.Errorf("flare[0] = %+v", flare.Endpoints[0])
	}
	if flare.Endpoints[1].URL != "https://rpc.ankr.com/flare/{secret}" ||
		flare.Endpoints[1].SecretRef != "ankr_flare_key" {
		t.Errorf("flare[1] = %+v", flare.Endpoints[1])
	}

	cash, ok := e.Groups["hex_cash"]
	if !ok {
		t.Fatal("hex_cash group is missing")
	}
	if len(cash.Endpoints) != 1 {
		t.Fatalf("hex_cash endpoints = %d, want 1", len(cash.Endpoints))
	}
	ep := cash.Endpoints[0]
	if ep.Kind != KindPrivate || ep.URL != "" {
		t.Errorf("hex_cash endpoint is not PRIVATE: %+v", ep)
	}
	if ep.URLHash.Hex() != vectorCommitment {
		t.Errorf("hex_cash commitment = %s, want %s", ep.URLHash.Hex(), vectorCommitment)
	}
	if ep.SecretRef != "hex_cash_url" {
		t.Errorf("hex_cash secretRef = %q", ep.SecretRef)
	}

	// validate() must accept a payload Solidity produced and the contract would
	// have accepted; if this fails the enclave's own rules have drifted.
	if err := validate(e); err != nil {
		t.Errorf("validate rejected the Solidity fixture: %v", err)
	}
}

// TestEncodeEndpointsMatchesSolidity pins the encoder the publishing tools will
// use against the same bytes.
func TestEncodeEndpointsMatchesSolidity(t *testing.T) {
	commitment := common.HexToHash(vectorCommitment)

	got, err := EncodeEndpoints(7, config.FeedID, []GroupSpec{
		{Group: "flare", Threshold: 2, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://flare-api.flare.network/ext/C/rpc"},
			{Kind: KindPublic, URL: "https://rpc.ankr.com/flare/{secret}", SecretRef: "ankr_flare_key"},
		}},
		{Group: "ethereum", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://eth.example/rpc"},
		}},
		{Group: "songbird", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://sgb.example/rpc"},
		}},
		{Group: "hex_cash", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPrivate, URLHash: commitment, SecretRef: "hex_cash_url"},
		}},
	})
	if err != nil {
		t.Fatalf("EncodeEndpoints: %v", err)
	}

	if want := mustFixture(t, vectorEndpointsFile); !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from Solidity\n got %x\nwant %x", got, want)
	}
}

func TestDecodeAdminsMatchesSolidity(t *testing.T) {
	payload := mustFixture(t, vectorAdminsFile)

	if got := crypto.Keccak256Hash(payload).Hex(); got != vectorAdminsHash {
		t.Fatalf("fixture hash = %s, want %s", got, vectorAdminsHash)
	}

	wire, err := decodeAdmins(payload)
	if err != nil {
		t.Fatalf("decodeAdmins: %v", err)
	}
	version, sets, err := convertAdmins(wire)
	if err != nil {
		t.Fatalf("convertAdmins: %v", err)
	}

	if version != 3 {
		t.Errorf("version = %d, want 3", version)
	}
	backing, ok := sets["backing"]
	if !ok {
		t.Fatal("backing role is missing")
	}
	if backing.Threshold != 1 || len(backing.Admins) != 1 ||
		backing.Admins[0] != common.HexToAddress("0x1111111111111111111111111111111111111111") {
		t.Errorf("backing = %+v", backing)
	}
	providers, ok := sets["providers"]
	if !ok {
		t.Fatal("providers role is missing")
	}
	if providers.Threshold != 2 || len(providers.Admins) != 2 {
		t.Errorf("providers = %+v", providers)
	}
}

func TestEncodeAdminsMatchesSolidity(t *testing.T) {
	got, err := EncodeAdmins(3, config.FeedID, []RoleSpec{
		{Role: "backing", Threshold: 1, Admins: []common.Address{
			common.HexToAddress("0x1111111111111111111111111111111111111111"),
		}},
		{Role: "providers", Threshold: 2, Admins: []common.Address{
			common.HexToAddress("0x2222222222222222222222222222222222222222"),
			common.HexToAddress("0x3333333333333333333333333333333333333333"),
		}},
	})
	if err != nil {
		t.Fatalf("EncodeAdmins: %v", err)
	}

	if want := mustFixture(t, vectorAdminsFile); !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from Solidity\n got %x\nwant %x", got, want)
	}
}

// TestTagString covers what the contract lets through: any non-zero bytes32.
func TestTagString(t *testing.T) {
	if got, ok := tagString(tagBytes32("flare")); !ok || got != "flare" {
		t.Errorf("tagString(flare) = %q, %v", got, ok)
	}
	if _, ok := tagString([32]byte{}); ok {
		t.Error("the zero tag must be rejected")
	}
	// Non-printable bytes are a legal on-chain tag and must not become a name.
	var binary [32]byte
	binary[0] = 0x01
	if _, ok := tagString(binary); ok {
		t.Error("a non-printable tag must be rejected")
	}
}

func TestNarrow(t *testing.T) {
	if v, ok := narrow(3); !ok || v != 3 {
		t.Errorf("narrow(3) = %d, %v", v, ok)
	}
	// The wire type is uint64 and every consumer is an int: an absurd threshold
	// must be rejected rather than wrapped into a small one.
	if _, ok := narrow(1 << 40); ok {
		t.Error("an out-of-range threshold must be rejected")
	}
}

// TestPayloadsAreBoundToThisFeed covers what multi-feed support introduced: one
// sender publishes for every feed of the extension, so a machine serving one
// feed can be handed a sibling's configuration. Applying it would install a
// provider set this feed's price is then computed from, and every update would
// afterwards carry a hash this feed's store rejects.
func TestPayloadsAreBoundToThisFeed(t *testing.T) {
	other := config.FeedID
	other[20] ^= 0x01

	endpoints, err := EncodeEndpoints(1, other, []GroupSpec{
		{Group: "flare", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://flare.example/rpc"},
		}},
	})
	if err != nil {
		t.Fatalf("EncodeEndpoints: %v", err)
	}
	wire, err := decodeEndpoints(endpoints)
	if err != nil {
		t.Fatalf("decodeEndpoints: %v", err)
	}
	e, err := convertEndpoints(wire)
	if err != nil {
		t.Fatalf("convertEndpoints: %v", err)
	}
	if err := validate(e); err == nil {
		t.Error("endpoints for another feed must be rejected")
	}

	admins, err := EncodeAdmins(1, other, []RoleSpec{
		{Role: "backing", Threshold: 1, Admins: []common.Address{{0x11}}},
		{Role: "providers", Threshold: 2, Admins: []common.Address{{0x22}, {0x33}}},
	})
	if err != nil {
		t.Fatalf("EncodeAdmins: %v", err)
	}
	adminsWire, err := decodeAdmins(admins)
	if err != nil {
		t.Fatalf("decodeAdmins: %v", err)
	}
	if _, _, err := convertAdmins(adminsWire); err == nil {
		t.Error("admins for another feed must be rejected")
	}
}
