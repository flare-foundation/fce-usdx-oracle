package oraclepayload

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The fixtures are Solidity's own encoding, produced by
// test/ConfigPayloadVector.t.sol and shared with the enclave's decoder in
// go/internal/provisioning/store. See testdata/wire/README.md for why a shared fixture
// rather than shared code.
const (
	endpointsFile   = "../../../testdata/wire/endpoints-v7.abi.hex"
	adminsFile      = "../../../testdata/wire/admins-v3.abi.hex"
	feedRequestFile = "../../../testdata/wire/feed-update-request.abi.hex"

	// The USDX feed the fixtures are published for.
	vectorFeedID = "0x20555344582f555344000000000000000000000000"

	vectorSalt       = "0xa05e334153147e75f3f416139b5109d1179cb56fef6a4ecb4c4cbc92a7c37b70"
	vectorCommitment = "0x78ccb727d3a566d9f8dc3f5bb3cf1dbbe283b46b5c3533cbcf3ccb75d62692f8"
	vectorCashURL    = "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount"

	vectorEndpointsHash = "0xd241e7c63ba9ab995063996a7cfc5854eacf1fe94af785f18e5ef98795a2614c"
	vectorAdminsHash    = "0xab4e61f34281cf851bf9e996732f647e2f13d69dd3e49bcc99964fa2051ec9da"
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

func mustTag(t *testing.T, name string) [32]byte {
	t.Helper()
	tag, err := Tag(name)
	if err != nil {
		t.Fatalf("Tag(%q): %v", name, err)
	}
	return tag
}

func testFeedID(t *testing.T) [21]byte {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(vectorFeedID, "0x"))
	if err != nil || len(raw) != 21 {
		t.Fatalf("bad feed id fixture: %v", err)
	}
	var id [21]byte
	copy(id[:], raw)
	return id
}

func vectorGroups(t *testing.T) []Group {
	t.Helper()
	return []Group{
		{Group: mustTag(t, "flare"), Threshold: 2, Endpoints: []Endpoint{
			{Kind: KindPublic, Url: "https://flare-api.flare.network/ext/C/rpc"},
			{Kind: KindPublic, Url: "https://rpc.ankr.com/flare/{secret}", SecretRef: "ankr_flare_key"},
		}},
		{Group: mustTag(t, "ethereum"), Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, Url: "https://eth.example/rpc"},
		}},
		{Group: mustTag(t, "songbird"), Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, Url: "https://sgb.example/rpc"},
		}},
		{Group: mustTag(t, "hex_cash"), Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPrivate, UrlHash: common.HexToHash(vectorCommitment), SecretRef: "hex_cash_url"},
		}},
	}
}

func vectorRoles(t *testing.T) []Role {
	t.Helper()
	return []Role{
		{Role: mustTag(t, "backing"), Threshold: 1, Admins: []common.Address{
			common.HexToAddress("0x1111111111111111111111111111111111111111"),
		}},
		{Role: mustTag(t, "providers"), Threshold: 2, Admins: []common.Address{
			common.HexToAddress("0x2222222222222222222222222222222222222222"),
			common.HexToAddress("0x3333333333333333333333333333333333333333"),
		}},
	}
}

// TestEncodeEndpointsMatchesSolidity is the check that matters. Both payloads
// are ABI-dynamic, so there is no canonical encoding to fall back on: if this
// encoder and the contract's disagree, the recorded hash is one no machine can
// satisfy and every feed update reverts StaleEndpoints, silently and forever.
func TestEncodeEndpointsMatchesSolidity(t *testing.T) {
	got, err := EncodeEndpoints(7, testFeedID(t), vectorGroups(t))
	if err != nil {
		t.Fatalf("EncodeEndpoints: %v", err)
	}
	if want := mustFixture(t, endpointsFile); !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from Solidity\n got %x\nwant %x", got, want)
	}
}

func TestEncodeAdminsMatchesSolidity(t *testing.T) {
	got, err := EncodeAdmins(3, testFeedID(t), vectorRoles(t))
	if err != nil {
		t.Fatalf("EncodeAdmins: %v", err)
	}
	if want := mustFixture(t, adminsFile); !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from Solidity\n got %x\nwant %x", got, want)
	}
}

// The hash is the whole point of the encoder: it is what the sender records and
// what every subsequent feed update has to carry.
func TestHashesMatchSolidity(t *testing.T) {
	endpointsHash, err := EndpointsHash(7, testFeedID(t), vectorGroups(t))
	if err != nil {
		t.Fatalf("EndpointsHash: %v", err)
	}
	if endpointsHash.Hex() != vectorEndpointsHash {
		t.Errorf("endpoints hash = %s, want %s", endpointsHash.Hex(), vectorEndpointsHash)
	}

	adminsHash, err := AdminsHash(3, testFeedID(t), vectorRoles(t))
	if err != nil {
		t.Fatalf("AdminsHash: %v", err)
	}
	if adminsHash.Hex() != vectorAdminsHash {
		t.Errorf("admins hash = %s, want %s", adminsHash.Hex(), vectorAdminsHash)
	}
}

// The version is inside the hashed payload, so two publications of an identical
// configuration commit to different hashes. Predicting one for the wrong version
// is the easy way to chase a StaleEndpoints revert that is not there.
func TestHashDependsOnTheVersion(t *testing.T) {
	a, err := EndpointsHash(7, testFeedID(t), vectorGroups(t))
	if err != nil {
		t.Fatalf("EndpointsHash: %v", err)
	}
	b, err := EndpointsHash(8, testFeedID(t), vectorGroups(t))
	if err != nil {
		t.Fatalf("EndpointsHash: %v", err)
	}
	if a == b {
		t.Fatal("the endpoints hash does not depend on the version")
	}

	// Version 0 is the contract's "never published" sentinel.
	if _, err := EncodeEndpoints(0, testFeedID(t), vectorGroups(t)); err == nil {
		t.Error("version 0 must be rejected")
	}
	if _, err := EncodeAdmins(0, testFeedID(t), vectorRoles(t)); err == nil {
		t.Error("version 0 must be rejected")
	}
}

func TestCommitPrivateURLMatchesSolidity(t *testing.T) {
	got, err := CommitPrivateURL(common.HexToHash(vectorSalt), vectorCashURL)
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	if got.Hex() != vectorCommitment {
		t.Fatalf("commitment = %s, want %s", got.Hex(), vectorCommitment)
	}

	// An unsalted commitment to a low-entropy URL is brute-forceable from chain
	// data, so the publisher refuses to produce one at all.
	if _, err := CommitPrivateURL(common.Hash{}, vectorCashURL); err == nil {
		t.Error("a zero salt must be rejected")
	}
	if _, err := EncodePrivateURL(common.Hash{}, vectorCashURL); err == nil {
		t.Error("a zero salt must be rejected")
	}
}

// The blob the machine is provisioned with must be the commitment's preimage.
func TestEncodePrivateURLIsTheCommitmentPreimage(t *testing.T) {
	salt := common.HexToHash(vectorSalt)

	blob, err := EncodePrivateURL(salt, vectorCashURL)
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	commitment, err := CommitPrivateURL(salt, vectorCashURL)
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	if crypto.Keccak256Hash(blob) != commitment {
		t.Fatal("keccak256(EncodePrivateURL) != CommitPrivateURL")
	}
}

func TestTag(t *testing.T) {
	if got := mustTag(t, "flare"); TagName(got) != "flare" {
		t.Errorf("round trip = %q", TagName(got))
	}
	if _, err := Tag(""); err == nil {
		t.Error("an empty tag must be rejected")
	}
	// Truncation would produce a tag the contract accepts and the extension
	// rejects as unknown, which reads as a schema disagreement rather than a typo.
	if _, err := Tag(strings.Repeat("x", 33)); err == nil {
		t.Error("an over-long tag must be rejected rather than truncated")
	}
}

func TestThreshold(t *testing.T) {
	if v, err := Threshold(2); err != nil || v != 2 {
		t.Errorf("Threshold(2) = %d, %v", v, err)
	}
	for _, bad := range []int{0, -1} {
		if _, err := Threshold(bad); err == nil {
			t.Errorf("threshold %d must be rejected", bad)
		}
	}
}

// The GET_FEED envelope is an ABI-encoded FeedUpdateRequest now, not the old
// single 0x01 byte. A publisher that still sent the byte would have every
// request refused by the extension as an unexpected payload.
func TestEncodeFeedRequestMatchesSolidity(t *testing.T) {
	got, err := EncodeFeedRequest(testFeedID(t))
	if err != nil {
		t.Fatalf("EncodeFeedRequest: %v", err)
	}
	if want := mustFixture(t, feedRequestFile); !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from Solidity\n got %x\nwant %x", got, want)
	}
	if len(got) != 32 {
		t.Errorf("length = %d, want one word", len(got))
	}
	if _, err := EncodeFeedRequest([21]byte{}); err == nil {
		t.Error("the zero feed id must be rejected")
	}
}

// The feed id is inside both hashed payloads, so publishing the same
// configuration for a sibling feed commits to a different hash.
func TestHashDependsOnTheFeed(t *testing.T) {
	feedID := testFeedID(t)
	other := feedID
	other[20] ^= 0x01

	a, err := EndpointsHash(7, feedID, vectorGroups(t))
	if err != nil {
		t.Fatalf("EndpointsHash: %v", err)
	}
	b, err := EndpointsHash(7, other, vectorGroups(t))
	if err != nil {
		t.Fatalf("EndpointsHash: %v", err)
	}
	if a == b {
		t.Error("the endpoints hash does not depend on the feed id")
	}

	if _, err := EncodeEndpoints(7, [21]byte{}, vectorGroups(t)); err == nil {
		t.Error("the zero feed id must be rejected")
	}
	if _, err := EncodeAdmins(3, [21]byte{}, vectorRoles(t)); err == nil {
		t.Error("the zero feed id must be rejected")
	}
}
