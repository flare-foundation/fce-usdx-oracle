package store

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Golden vectors from test/ConfigPayloadVector.t.sol, which builds the same two
// payloads from the structs as they are declared upstream and prints Solidity's
// own encoding. Regenerate with:
//
//	forge test --match-path test/ConfigPayloadVector.t.sol -vv
//
// These pin the decoder against Solidity rather than against the encoder: a
// round trip would pass even if both sides were wrong in the same way, which is
// exactly the failure a wire-format change invites.
const (
	vectorSalt       = "0xa05e334153147e75f3f416139b5109d1179cb56fef6a4ecb4c4cbc92a7c37b70"
	vectorCommitment = "0x78ccb727d3a566d9f8dc3f5bb3cf1dbbe283b46b5c3533cbcf3ccb75d62692f8"

	vectorEndpointsHash = "0xf3a4b77228fd9d882bc1ae24eeb91e5d91f1d75e7303217b67345ad3d4bf33d1"
	vectorAdminsHash    = "0x551849ebed418d7f8b59c1a79491483c2ca13036c8da355beaef774dffd00ecc"

	vectorCashURL = "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount"
)

// The two payload blobs live in deploy/testdata/wire, shared with the codec's
// own tests in pkg/wire/configpayload: what is pinned here is that this
// package's conversion and validation accept what Solidity produced.
const (
	vectorEndpointsFile = "../../../deploy/testdata/wire/endpoints-v7.abi.hex"
	vectorAdminsFile    = "../../../deploy/testdata/wire/admins-v3.abi.hex"
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

	got, err := configpayload.CommitPrivateURL(PrivateURL{Salt: salt, Url: vectorCashURL})
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	if got.Hex() != vectorCommitment {
		t.Fatalf("commitment = %s, want %s", got.Hex(), vectorCommitment)
	}

	// The salt is what makes publishing the commitment safe: without it the
	// hash of a low-entropy URL is brute-forceable from chain data, so the
	// codec refuses to produce an unsalted one at all.
	if _, err := configpayload.CommitPrivateURL(PrivateURL{Url: vectorCashURL}); err == nil {
		t.Fatal("an unsalted commitment must be refused")
	}
}

func TestDecodePrivateURLRoundTrip(t *testing.T) {
	salt := common.HexToHash(vectorSalt)

	preimage, err := configpayload.EncodePrivateURL(PrivateURL{Salt: salt, Url: vectorCashURL})
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

func TestDecodeAdminsMatchesSolidity(t *testing.T) {
	payload := mustFixture(t, vectorAdminsFile)

	if got := crypto.Keccak256Hash(payload).Hex(); got != vectorAdminsHash {
		t.Fatalf("fixture hash = %s, want %s", got, vectorAdminsHash)
	}

	wire, err := decodeAdmins(payload)
	if err != nil {
		t.Fatalf("decodeAdmins: %v", err)
	}
	version, sets, err := convertAdmins(wire, config.AdminPolicy())
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

// TestPayloadsAreBoundToThisFeed covers what multi-feed support introduced: one
// sender publishes for every feed of the extension, so a machine serving one
// feed can be handed a sibling's configuration. Applying it would install a
// provider set this feed's price is then computed from, and every update would
// afterwards carry a hash this feed's store rejects.
func TestPayloadsAreBoundToThisFeed(t *testing.T) {
	other := config.FeedID
	other[20] ^= 0x01

	flare, _ := configpayload.Tag("flare")
	endpoints, err := configpayload.EncodeEndpoints(1, other, []configpayload.Group{
		{Group: flare, Threshold: 1, Endpoints: []configpayload.Endpoint{
			{Kind: KindPublic, Url: "https://flare.example/rpc"},
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

	backing, _ := configpayload.Tag("backing")
	providers, _ := configpayload.Tag("providers")
	admins, err := configpayload.EncodeAdmins(1, other, []configpayload.Role{
		{Role: backing, Threshold: 1, Admins: []common.Address{{0x11}}},
		{Role: providers, Threshold: 2, Admins: []common.Address{{0x22}, {0x33}}},
	})
	if err != nil {
		t.Fatalf("EncodeAdmins: %v", err)
	}
	adminsWire, err := decodeAdmins(admins)
	if err != nil {
		t.Fatalf("decodeAdmins: %v", err)
	}
	if _, _, err := convertAdmins(adminsWire, config.AdminPolicy()); err == nil {
		t.Error("admins for another feed must be rejected")
	}
}
