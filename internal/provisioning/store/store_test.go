package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/provisioning/envelope"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeNode stands in for tee-node: it echoes the ciphertext back as plaintext,
// which exercises everything except the crypto itself. Envelope verification is
// covered exhaustively in internal/provisioning/envelope.
type fakeNode struct{ teeID common.Address }

func (f *fakeNode) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}

// testTeeID must be non-zero: a zero teeId never satisfies Verify's machine
// binding, so every envelope would be rejected before reaching the logic under
// test. The verifier and the fake node are given the same one so real envelopes
// verify.
var testTeeID = common.HexToAddress("0x00000000000000000000000000000000000000aa")

var testChainID = big.NewInt(114)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(
		envelope.NewVerifier(testChainID, testTeeID, config.AdminPolicy()),
		&fakeNode{teeID: testTeeID},
	)
}

// The restricted cash endpoint travels as a PRIVATE commitment, so the tests
// need a salt and a URL whose commitment the published group carries.
const testCashURL = "https://api.htdigitalassets.com/alm/restricted/cash"

func testCashSalt() common.Hash { return crypto.Keccak256Hash([]byte("test-salt")) }

func testCashCommitment(t *testing.T) common.Hash {
	t.Helper()
	c, err := CommitPrivateURL(PrivateURL{Salt: testCashSalt(), Url: testCashURL})
	if err != nil {
		t.Fatalf("CommitPrivateURL: %v", err)
	}
	return c
}

func testCashBlob(t *testing.T) []byte {
	t.Helper()
	blob, err := EncodePrivateURL(PrivateURL{Salt: testCashSalt(), Url: testCashURL})
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	return blob
}

// testGroups covers every pinned chain, with a mix of thresholds and a
// third-party provider added the way an operator would add Alchemy, plus the
// required hex_cash group.
func testGroups(t *testing.T) []GroupSpec {
	t.Helper()
	return []GroupSpec{
		{Group: "flare", Threshold: 2, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://flare-api.flare.network/ext/C/rpc"},
			{Kind: KindPublic, URL: "https://rpc.ankr.com/flare/{secret}", SecretRef: "ankr_flare_key"},
			{Kind: KindPublic, URL: "https://flare-mainnet.g.alchemy.com/v2/{secret}", SecretRef: "alchemy_flare_key"},
		}},
		{Group: "ethereum", Threshold: 2, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://ethereum-rpc.publicnode.com"},
			{Kind: KindPublic, URL: "https://rpc.ankr.com/eth/{secret}", SecretRef: "ankr_eth_key"},
		}},
		{Group: "songbird", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://songbird-api.flare.network/ext/C/rpc"},
		}},
		{Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPrivate, URLHash: testCashCommitment(t), SecretRef: config.HexCashURLRef},
		}},
	}
}

// withGroup returns testGroups with one group replaced, for the rejection tests.
func withGroup(t *testing.T, tag string, g GroupSpec) []GroupSpec {
	t.Helper()
	groups := testGroups(t)
	for i := range groups {
		if groups[i].Group == tag {
			groups[i] = g
			return groups
		}
	}
	return append(groups, g)
}

// withoutGroup returns testGroups with one group removed.
func withoutGroup(t *testing.T, tag string) []GroupSpec {
	t.Helper()
	groups := testGroups(t)
	out := groups[:0]
	for _, g := range groups {
		if g.Group != tag {
			out = append(out, g)
		}
	}
	return out
}

// devAdmins are the addresses of private keys 1, 2 and 3, which the envelope helpers
// sign with. Publicly known, so nothing bakes them in: a machine honours them only
// if someone publishes them to it.
var devAdmins = []common.Address{
	common.HexToAddress("0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"), // privkey 0x01
	common.HexToAddress("0x2B5AD5c4795c026514f8317c7a215E218DcCD6cF"), // privkey 0x02
	common.HexToAddress("0x6813Eb9362372EEF6200f3b1dbC3f819671cBA69"), // privkey 0x03
}

// testRoles is the dev pair: 1-of-1 over the backing credentials, 2-of-3 over
// the provider credentials. The same three keys appear in both sets because the
// envelope helpers sign with keys 1 and 2 regardless of ref.
func testRoles() []RoleSpec {
	return []RoleSpec{
		{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
		{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
	}
}

// setAdmins installs the dev admin sets through the real ApplyAdmins path, so
// the tests exercise the same decode and validation a SET_ADMINS instruction
// takes.
func setAdmins(t *testing.T, s *Store, version uint64) {
	t.Helper()
	if err := applyAdmins(s, version, testRoles()); err != nil {
		t.Fatalf("ApplyAdmins: %v", err)
	}
}

func applyAdmins(s *Store, version uint64, roles []RoleSpec) error {
	raw, err := EncodeAdmins(version, config.FeedID, roles)
	if err != nil {
		return err
	}
	return s.ApplyAdmins(raw)
}

// setEndpoints encodes and applies, exercising the same decode path a real
// SET_ENDPOINTS instruction takes.
func setEndpoints(s *Store, version uint64, groups []GroupSpec) error {
	raw, err := EncodeEndpoints(version, config.FeedID, groups)
	if err != nil {
		return err
	}
	return s.ApplyEndpoints(raw)
}

// setSecret installs a credential directly, bypassing envelope verification —
// that is covered exhaustively in internal/provisioning/envelope.
func setSecret(t *testing.T, s *Store, ref string, value []byte) {
	t.Helper()
	s.mu.Lock()
	s.secrets[ref] = value
	s.mu.Unlock()
}

func provisioned(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)
	for _, ref := range []string{
		config.HexAPIKeyRef, "ankr_flare_key", "ankr_eth_key", "alchemy_flare_key",
	} {
		setSecret(t, s, ref, []byte("value-"+ref))
	}
	setSecret(t, s, config.HexCashURLRef, testCashBlob(t))
	return s
}

// --- what can no longer be configured ---

// The point of the simplification: the pinned policy is what reaches the supply
// reader, and nothing in a payload can change it.
func TestPinnedPolicyIsUsed(t *testing.T) {
	cfg, _, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if len(cfg.Chains) != len(config.Chains) {
		t.Fatalf("chains = %d, want %d", len(cfg.Chains), len(config.Chains))
	}
	for i, ch := range cfg.Chains {
		pinned := config.Chains[i]
		if ch.Name != pinned.Name {
			t.Errorf("chain %d name = %q, want %q", i, ch.Name, pinned.Name)
		}
		if ch.Token != common.HexToAddress(pinned.Token) {
			t.Errorf("chain %q token = %s, want %s", ch.Name, ch.Token, pinned.Token)
		}
		if ch.MaxBlockAge != pinned.MaxBlockAge {
			t.Errorf("chain %q maxBlockAge = %s, want %s", ch.Name, ch.MaxBlockAge, pinned.MaxBlockAge)
		}
	}
}

// The bonds source stays pinned in the build. hex_cash is the deliberate
// exception and travels as a commitment; nothing else may be redirected.
func TestBondsSourceStaysPinned(t *testing.T) {
	cfg, _, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if cfg.Bonds == nil || cfg.Bonds.Name() != config.HexBondsName {
		t.Fatalf("bonds source = %v", cfg.Bonds)
	}
	if cfg.Cash == nil || cfg.Cash.Name() != config.HexCashName {
		t.Fatalf("cash source = %v", cfg.Cash)
	}
}

func TestProviderNamesAreDerived(t *testing.T) {
	cfg, _, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, ch := range cfg.Chains {
		for i, p := range ch.Providers {
			want := fmt.Sprintf("rpc_%s_%d", ch.Name, i+1)
			if p.Name != want {
				t.Errorf("provider name = %q, want %q", p.Name, want)
			}
		}
	}
}

// --- url shape ---

func TestResolveAgainst_PlaceholderRejectedInHost(t *testing.T) {
	if _, err := resolveURL("https://{secret}.example.com/rpc", "k", "v"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestResolveAgainst_SubstitutesInPathAndQuery(t *testing.T) {
	got, err := resolveURL("https://example.com/rpc/{secret}", "k", "abc")
	if err != nil || got != "https://example.com/rpc/abc" {
		t.Fatalf("path: got %q, err %v", got, err)
	}
	got, err = resolveURL("https://example.com/rpc?key={secret}", "k", "abc")
	if err != nil || got != "https://example.com/rpc?key=abc" {
		t.Fatalf("query: got %q, err %v", got, err)
	}
}

func TestResolveAgainst_RejectsSecretThatWouldAlterURL(t *testing.T) {
	for _, bad := range []string{"a/b", "a?b", "a#b", "a&b", "a b"} {
		if _, err := resolveURL("https://example.com/rpc/{secret}", "k", bad); err == nil {
			t.Errorf("secret %q must be rejected", bad)
		}
	}
}

func TestResolveAgainst_RequiresHTTPS(t *testing.T) {
	for _, bad := range []string{
		"http://example.com/rpc", "ws://example.com", "example.com/rpc",
	} {
		if _, err := resolveURL(bad, "", ""); err == nil {
			t.Errorf("url %q must be rejected", bad)
		}
	}
}

func TestAnyHostIsAccepted(t *testing.T) {
	s := newStore(t)
	groups := withGroup(t, "songbird", GroupSpec{
		Group: "songbird", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://whatever.example/rpc"},
		},
	})
	if err := setEndpoints(s, 1, groups); err != nil {
		t.Fatalf("an unknown host must be accepted: %v", err)
	}
}

// --- group tags ---

func TestSetEndpoints_RejectsMissingChain(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, withoutGroup(t, "songbird")); err == nil {
		t.Fatal("a missing chain undercounts supply and must be rejected")
	}
}

// The hex_cash group is required for the same reason: without it a machine runs
// cash-less because a group was forgotten, not because anyone decided to.
func TestSetEndpoints_RejectsMissingCashGroup(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, withoutGroup(t, config.HexCashGroup)); err == nil {
		t.Fatal("a missing hex_cash group must be rejected")
	}
}

// An unrecognised tag means publisher and build disagree about the schema.
// Stopping beats applying the subset we understand.
func TestSetEndpoints_RejectsUnknownGroup(t *testing.T) {
	s := newStore(t)
	groups := append(testGroups(t), GroupSpec{
		Group: "bitcoin", Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://btc.example/rpc"},
		},
	})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("an unknown group tag must be rejected")
	}
}

// --- the cash group's shape ---

// PUBLIC would put the restricted URL on-chain in the clear AND let whoever
// publishes configuration point the price numerator wherever they liked.
func TestSetEndpoints_RejectsPublicCashEndpoint(t *testing.T) {
	s := newStore(t)
	groups := withGroup(t, config.HexCashGroup, GroupSpec{
		Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://evil.example/cash"},
		},
	})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("a PUBLIC hex_cash endpoint must be rejected")
	}
}

// The ref is pinned so the role governing the cash URL is not the publisher's
// choice — naming a providers-governed ref would demote it.
func TestSetEndpoints_RejectsCashEndpointWithWrongRef(t *testing.T) {
	s := newStore(t)
	groups := withGroup(t, config.HexCashGroup, GroupSpec{
		Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPrivate, URLHash: testCashCommitment(t), SecretRef: "something_else"},
		}})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("hex_cash naming another ref must be rejected")
	}
}

func TestSetEndpoints_RejectsMultipleCashEndpoints(t *testing.T) {
	s := newStore(t)
	ep := Endpoint{Kind: KindPrivate, URLHash: testCashCommitment(t), SecretRef: config.HexCashURLRef}
	groups := withGroup(t, config.HexCashGroup, GroupSpec{
		Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{ep, ep},
	})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("hex_cash must hold exactly one endpoint")
	}
}

// Mirrors the contract's kind-consistency rule. Re-checked here because a
// malformed pair getting through would mean dialling a URL nobody committed to.
func TestSetEndpoints_RejectsInconsistentKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		ep   Endpoint
	}{
		{"private with a url", Endpoint{
			Kind: KindPrivate, URL: "https://x.example", URLHash: common.HexToHash("0x01"),
			SecretRef: config.HexCashURLRef}},
		{"private without a commitment", Endpoint{
			Kind: KindPrivate, SecretRef: config.HexCashURLRef}},
		{"unknown kind", Endpoint{Kind: 7, URL: "https://x.example"}},
	} {
		s := newStore(t)
		groups := withGroup(t, config.HexCashGroup, GroupSpec{
			Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{tc.ep},
		})
		if err := setEndpoints(s, 1, groups); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		}
	}
}

// --- thresholds ---

func TestThresholdIsConfigurable(t *testing.T) {
	cfg, _, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := map[string]int{"flare": 2, "ethereum": 2, "songbird": 1}
	for _, ch := range cfg.Chains {
		if ch.Quorum != want[ch.Name] {
			t.Errorf("chain %q quorum = %d, want %d", ch.Name, ch.Quorum, want[ch.Name])
		}
	}
}

func TestSetEndpoints_RejectsThresholdAboveEndpointCount(t *testing.T) {
	s := newStore(t)
	groups := withGroup(t, "songbird", GroupSpec{
		Group: "songbird", Threshold: 2, Endpoints: []Endpoint{
			{Kind: KindPublic, URL: "https://songbird-api.flare.network/ext/C/rpc"},
		},
	})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("threshold above the endpoint count must be rejected")
	}
}

func TestSetEndpoints_RejectsBadThreshold(t *testing.T) {
	for _, threshold := range []uint64{0, 1 << 40} {
		s := newStore(t)
		groups := withGroup(t, "songbird", GroupSpec{
			Group: "songbird", Threshold: threshold, Endpoints: []Endpoint{
				{Kind: KindPublic, URL: "https://songbird-api.flare.network/ext/C/rpc"},
			},
		})
		if err := setEndpoints(s, 1, groups); err == nil {
			t.Errorf("threshold %d must be rejected", threshold)
		}
	}
}

func TestSetEndpoints_RejectsEmptyEndpointList(t *testing.T) {
	s := newStore(t)
	groups := withGroup(t, "songbird", GroupSpec{Group: "songbird", Threshold: 1})
	if err := setEndpoints(s, 1, groups); err == nil {
		t.Fatal("an empty endpoint list must be rejected")
	}
}

// --- endpoints versioning (new: the payload never carried one before) ---

func TestSetEndpoints_RejectsNonIncreasingVersion(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 5, testGroups(t)); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	for _, v := range []uint64{0, 1, 5} {
		if err := setEndpoints(s, v, testGroups(t)); err == nil {
			t.Errorf("version %d must not be accepted after 5", v)
		}
	}
	if err := setEndpoints(s, 6, testGroups(t)); err != nil {
		t.Errorf("version 6 must be accepted after 5: %v", err)
	}
}

// A rejected publication must leave the working configuration alone.
func TestSetEndpoints_RejectedPublicationKeepsPreviousSnapshot(t *testing.T) {
	s := provisioned(t)
	before, binding, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := setEndpoints(s, 2, withoutGroup(t, "songbird")); err == nil {
		t.Fatal("expected rejection")
	}

	after, binding, err := s.Open()
	if err != nil {
		t.Fatalf("Resolve after rejection: %v", err)
	}
	if len(after.Chains) != len(before.Chains) {
		t.Fatalf("chains changed after a rejected publication")
	}
	if s.Readiness().EndpointsHash != binding.EndpointsHash.Hex() {
		t.Error("endpoints hash changed after a rejected publication")
	}
}

// --- the backing credentials ---

// Without this a payload author could name a backing credential as a chain
// provider's secret and have it substituted into a host of their choosing.
func TestSetEndpoints_RejectsBackingCredentialAsProviderSecret(t *testing.T) {
	for _, ref := range config.BackingRefs() {
		s := newStore(t)
		groups := withGroup(t, "songbird", GroupSpec{
			Group: "songbird", Threshold: 1, Endpoints: []Endpoint{
				{Kind: KindPublic, URL: "https://evil.example/rpc/{secret}", SecretRef: ref},
			},
		})
		if err := setEndpoints(s, 1, groups); err == nil {
			t.Errorf("%q must not be usable as a chain provider secret", ref)
		}
	}
}

// --- the private cash URL ---

func TestOpen_CashURLComesFromTheCommitment(t *testing.T) {
	cfg, _, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if cfg.Cash == nil {
		t.Fatal("cash source is missing")
	}
}

// A URL whose commitment does not match must fail the provisioning step that
// introduced it, not the next observation — in both orders of arrival.
func TestPrivateURL_MismatchIsRejectedOnSecret(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)

	wrong, err := EncodePrivateURL(PrivateURL{Salt: testCashSalt(), Url: "https://evil.example/cash"})
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	if err := s.ApplySecret(context.Background(), envelopeFor(t, config.HexCashURLRef, wrong, 1)); err == nil {
		t.Fatal("a URL that does not match its commitment must be rejected")
	}
}

func TestPrivateURL_MismatchIsRejectedOnEndpoints(t *testing.T) {
	s := newStore(t)
	setAdmins(t, s, 1)

	// The URL arrives first, before any configuration names it.
	wrong, err := EncodePrivateURL(PrivateURL{Salt: testCashSalt(), Url: "https://evil.example/cash"})
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	if err := s.ApplySecret(context.Background(), envelopeFor(t, config.HexCashURLRef, wrong, 1)); err != nil {
		t.Fatalf("with no configuration to check against, it must be accepted: %v", err)
	}

	// The publication that names it is what must now fail.
	if err := setEndpoints(s, 1, testGroups(t)); err == nil {
		t.Fatal("a configuration whose commitment no provisioned URL matches must be rejected")
	}
}

func TestPrivateURL_UnsaltedCommitmentIsRejected(t *testing.T) {
	s := newStore(t)
	setAdmins(t, s, 1)

	blob, err := EncodePrivateURL(PrivateURL{Salt: common.Hash{}, Url: testCashURL})
	if err != nil {
		t.Fatalf("EncodePrivateURL: %v", err)
	}
	commitment := crypto.Keccak256Hash(blob)

	groups := withGroup(t, config.HexCashGroup, GroupSpec{
		Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{
			{Kind: KindPrivate, URLHash: commitment, SecretRef: config.HexCashURLRef},
		}})
	if err := setEndpoints(s, 1, groups); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	// The commitment matches, but a zero salt makes it brute-forceable from
	// chain data, so it is refused anyway.
	if err := s.ApplySecret(context.Background(), envelopeFor(t, config.HexCashURLRef, blob, 1)); err == nil {
		t.Fatal("an unsalted commitment must be rejected")
	}
}

// A private URL must still be a well-formed https URL with no placeholder —
// there is no second ref to fill one.
func TestPrivateURL_ShapeIsChecked(t *testing.T) {
	for _, bad := range []string{
		"http://api.example/cash",
		"https://api.example/{secret}",
	} {
		s := newStore(t)
		setAdmins(t, s, 1)

		blob, err := EncodePrivateURL(PrivateURL{Salt: testCashSalt(), Url: bad})
		if err != nil {
			t.Fatalf("EncodePrivateURL: %v", err)
		}
		commitment := crypto.Keccak256Hash(blob)
		groups := withGroup(t, config.HexCashGroup, GroupSpec{
			Group: config.HexCashGroup, Threshold: 1, Endpoints: []Endpoint{
				{Kind: KindPrivate, URLHash: commitment, SecretRef: config.HexCashURLRef},
			}})
		if err := setEndpoints(s, 1, groups); err != nil {
			t.Fatalf("SetEndpoints: %v", err)
		}
		if err := s.ApplySecret(context.Background(), envelopeFor(t, config.HexCashURLRef, blob, 1)); err == nil {
			t.Errorf("private url %q must be rejected", bad)
		}
	}
}

// --- readiness and resolution ---

func TestStore_StartsUnconfigured(t *testing.T) {
	s := newStore(t)

	r := s.Readiness()
	if r.Ready || r.EndpointsSet || r.AdminsSet {
		t.Errorf("a fresh store must not be ready: %+v", r)
	}
	if _, _, err := s.Open(); !errors.Is(err, failure.NotConfigured) {
		t.Errorf("Resolve = %v, want NotConfigured", err)
	}
}

func TestStore_HexKeyIsAlwaysRequired(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}

	var found bool
	for _, ref := range s.Readiness().MissingRefs {
		if ref == config.HexAPIKeyRef {
			found = true
		}
	}
	if !found {
		t.Errorf("%q must always be listed as required", config.HexAPIKeyRef)
	}
}

// The private URL is required too: it is named by the configuration, so an
// operator needs to see that it has not arrived.
func TestStore_CashURLIsRequired(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}

	var found bool
	for _, ref := range s.Readiness().MissingRefs {
		if ref == config.HexCashURLRef {
			found = true
		}
	}
	if !found {
		t.Errorf("%q must be listed as required", config.HexCashURLRef)
	}
}

// Running cash-less is a state, not a failure: the oracle then publishes only
// where the figure provably cannot change the answer.
func TestOpen_WithoutHexKeyYieldsNoCashSource(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)
	for _, ref := range []string{"ankr_flare_key", "ankr_eth_key", "alchemy_flare_key"} {
		setSecret(t, s, ref, []byte("value"))
	}
	setSecret(t, s, config.HexCashURLRef, testCashBlob(t))

	cfg, _, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if cfg.Cash != nil {
		t.Error("without the API key there must be no cash source")
	}
}

// The same state, reached the other way: the key is there but the URL is not.
func TestOpen_WithoutCashURLYieldsNoCashSource(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)
	for _, ref := range []string{"ankr_flare_key", "ankr_eth_key", "alchemy_flare_key"} {
		setSecret(t, s, ref, []byte("value"))
	}
	setSecret(t, s, config.HexAPIKeyRef, []byte("key"))

	cfg, _, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if cfg.Cash != nil {
		t.Error("without the committed URL there must be no cash source")
	}
}

func TestStore_BecomesReadyWhenEverythingArrives(t *testing.T) {
	s := provisioned(t)
	r := s.Readiness()
	if !r.Ready {
		t.Fatalf("store is not ready: %+v", r)
	}
	if len(r.MissingRefs) != 0 {
		t.Errorf("missing refs = %v, want none", r.MissingRefs)
	}
}

func TestOpen_CarriesTheFeedBinding(t *testing.T) {
	_, binding, err := provisioned(t).Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if binding.ExtensionID == nil || binding.ExtensionID.Cmp(config.ExtensionID) != 0 {
		t.Errorf("extension id = %v, want %v", binding.ExtensionID, config.ExtensionID)
	}
	if binding.FeedID != config.FeedID {
		t.Errorf("feed id = %x, want %x", binding.FeedID, config.FeedID)
	}
}

func TestOpen_CarriesTheConfigHashes(t *testing.T) {
	s := provisioned(t)
	_, binding, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := s.Readiness()
	if binding.EndpointsHash.Hex() != r.EndpointsHash {
		t.Errorf("endpoints hash = %s, want %s", binding.EndpointsHash.Hex(), r.EndpointsHash)
	}
	if binding.AdminsHash.Hex() != r.AdminsHash {
		t.Errorf("admins hash = %s, want %s", binding.AdminsHash.Hex(), r.AdminsHash)
	}
}

// The hash must be of the payload AS RECEIVED: these payloads are dynamic, so
// re-encoding the decoded struct is not guaranteed to reproduce the input, and
// the store compares against keccak256 of what the sender dispatched.
func TestApply_HashesThePayloadAsReceived(t *testing.T) {
	s := newStore(t)

	endpoints, err := EncodeEndpoints(1, config.FeedID, testGroups(t))
	if err != nil {
		t.Fatalf("EncodeEndpoints: %v", err)
	}
	if err := s.ApplyEndpoints(endpoints); err != nil {
		t.Fatalf("ApplyEndpoints: %v", err)
	}

	admins, err := EncodeAdmins(1, config.FeedID, testRoles())
	if err != nil {
		t.Fatalf("EncodeAdmins: %v", err)
	}
	if err := s.ApplyAdmins(admins); err != nil {
		t.Fatalf("ApplyAdmins: %v", err)
	}

	r := s.Readiness()
	if r.EndpointsHash != crypto.Keccak256Hash(endpoints).Hex() {
		t.Errorf("endpoints hash = %s, want %s", r.EndpointsHash, crypto.Keccak256Hash(endpoints).Hex())
	}
	if r.AdminsHash != crypto.Keccak256Hash(admins).Hex() {
		t.Errorf("admins hash = %s, want %s", r.AdminsHash, crypto.Keccak256Hash(admins).Hex())
	}
}

func TestReadiness_ExposesNoSecrets(t *testing.T) {
	s := provisioned(t)
	r := s.Readiness()

	blob := fmt.Sprintf("%+v", r)
	for _, secret := range []string{
		"value-" + config.HexAPIKeyRef, "value-ankr_flare_key", testCashURL,
	} {
		if strings.Contains(blob, secret) {
			t.Errorf("readiness leaked a value: %q", secret)
		}
	}
}

// --- admin sets ---

func TestApplyAdmins_RequiredBeforeAnythingElse(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setSecret(t, s, config.HexAPIKeyRef, []byte("k"))
	setSecret(t, s, config.HexCashURLRef, testCashBlob(t))

	if _, _, err := s.Open(); !errors.Is(err, failure.NotConfigured) {
		t.Errorf("Resolve without admins = %v, want NotConfigured", err)
	}
}

func TestApplyAdmins_RejectsBadPayloads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles []RoleSpec
	}{
		{"missing backing", []RoleSpec{
			{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
		}},
		{"missing providers", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
		}},
		{"unknown role", append(testRoles(), RoleSpec{
			Role: "auditors", Threshold: 1, Admins: devAdmins[:1],
		})},
		{"duplicate role", append(testRoles(), RoleSpec{
			Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1],
		})},
		{"providers below the floor", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
			{Role: envelope.RoleProviders, Threshold: 1, Admins: devAdmins},
		}},
		{"threshold above the set", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 2, Admins: devAdmins[:1]},
			{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
		}},
		{"zero threshold", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 0, Admins: devAdmins[:1]},
			{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
		}},
		{"empty admin list", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 1},
			{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
		}},
		{"zero admin address", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 1, Admins: []common.Address{{}}},
			{Role: envelope.RoleProviders, Threshold: 2, Admins: devAdmins},
		}},
		{"duplicate admin", []RoleSpec{
			{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
			{Role: envelope.RoleProviders, Threshold: 2, Admins: []common.Address{
				devAdmins[0], devAdmins[0], devAdmins[1],
			}},
		}},
	} {
		s := newStore(t)
		if err := applyAdmins(s, 1, tc.roles); err == nil {
			t.Errorf("%s must be rejected", tc.name)
		}
	}
}

// A threshold above the signature cap installs cleanly and is then
// unsatisfiable forever, so it is refused at publication.
func TestApplyAdmins_ThresholdStopsAtTheSignatureCap(t *testing.T) {
	admins := make([]common.Address, envelope.MaxThreshold+1)
	for i := range admins {
		admins[i] = common.BigToAddress(big.NewInt(int64(i + 1)))
	}

	s := newStore(t)
	if err := applyAdmins(s, 1, []RoleSpec{
		{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
		{Role: envelope.RoleProviders, Threshold: uint64(envelope.MaxThreshold + 1), Admins: admins},
	}); err == nil {
		t.Fatalf("a threshold above %d must be rejected", envelope.MaxThreshold)
	}
}

func TestApplyAdmins_RejectsNonIncreasingVersion(t *testing.T) {
	s := newStore(t)
	setAdmins(t, s, 5)

	for _, v := range []uint64{0, 1, 5} {
		if err := applyAdmins(s, v, testRoles()); err == nil {
			t.Errorf("version %d must not be accepted after 5", v)
		}
	}
	if err := applyAdmins(s, 6, testRoles()); err != nil {
		t.Errorf("version 6 must be accepted after 5: %v", err)
	}
}

func TestApplyAdmins_RejectedRotationKeepsTheWorkingSet(t *testing.T) {
	s := newStore(t)
	setAdmins(t, s, 1)
	before := s.Readiness().AdminsHash

	if err := applyAdmins(s, 2, []RoleSpec{
		{Role: envelope.RoleBacking, Threshold: 1, Admins: devAdmins[:1]},
		{Role: envelope.RoleProviders, Threshold: 9, Admins: devAdmins},
	}); err == nil {
		t.Fatal("expected rejection")
	}

	if s.Readiness().AdminsHash != before {
		t.Error("a rejected rotation changed the live admin set")
	}
	// The working set must still verify an envelope.
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	if err := s.ApplySecret(context.Background(),
		envelopeFor(t, config.HexAPIKeyRef, []byte("k"), 1)); err != nil {
		t.Errorf("the previous admin set must still work: %v", err)
	}
}

// --- credentials ---

func TestApplySecret_RejectsCredentialThatWouldAlterAURL(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)

	for _, bad := range []string{"a/b", "a?b", "a#b", "a&b", "a b", ""} {
		err := s.ApplySecret(context.Background(),
			envelopeFor(t, "ankr_flare_key", []byte(bad), 1))
		if err == nil {
			t.Errorf("credential %q must be rejected", bad)
		}
	}
}

func TestApplySecret_RejectsOversizedMessage(t *testing.T) {
	s := provisioned(t)
	if err := s.ApplySecret(context.Background(), make([]byte, maxPayloadBytes+1)); err == nil {
		t.Fatal("an oversized message must be rejected")
	}
}

func TestStore_SecretRotationTakesEffect(t *testing.T) {
	s := provisioned(t)
	setSecret(t, s, "ankr_flare_key", []byte("rotated"))

	cfg, _, err := s.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Resolve is called per observation, so a rotated credential takes effect
	// without a restart. The URL itself never reaches /state, so this only
	// checks that resolution succeeded with the new value in place.
	if len(cfg.Chains) == 0 {
		t.Fatal("no chains resolved")
	}
}

func TestApplySecret_IsSafeUnderConcurrency(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, 1, testGroups(t)); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	setAdmins(t, s, 1)

	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(version uint64) {
			defer wg.Done()
			_ = s.ApplySecret(context.Background(),
				envelopeFor(t, "ankr_flare_key", []byte(fmt.Sprintf("v%d", version)), version))
		}(uint64(i))
	}
	wg.Wait()

	// Whichever won, exactly one value is installed and the store is intact.
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.secrets["ankr_flare_key"]) == 0 {
		t.Error("no credential was installed")
	}
}

// envelopeFor builds a signed provisioning envelope for ref, signed by dev keys
// 1 and 2 — enough for either role's floor.
func envelopeFor(t *testing.T, ref string, payload []byte, version uint64) []byte {
	t.Helper()

	env := envelope.Envelope{
		ChainID:     testChainID,
		TeeID:       testTeeID,
		Version:     version,
		IssuedAt:    uint64(time.Now().UTC().Unix()),
		Ref:         ref,
		PayloadHash: crypto.Keccak256Hash(payload),
		Payload:     payload,
	}

	body, err := envelope.EncodeBody(env)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	digest := accounts.TextHash(crypto.Keccak256(body))

	var signatures [][]byte
	for _, keyHex := range []string{
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000002",
	} {
		key, err := crypto.HexToECDSA(keyHex)
		if err != nil {
			t.Fatalf("HexToECDSA: %v", err)
		}
		sig, err := crypto.Sign(digest, key)
		if err != nil {
			t.Fatalf("Sign: %v", err)
		}
		sig[64] += 27
		signatures = append(signatures, sig)
	}

	message, err := envelope.Encode(env, signatures)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return message
}
