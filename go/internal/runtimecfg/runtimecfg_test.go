package runtimecfg

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/provision"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeNode stands in for tee-node: it echoes the ciphertext back as plaintext,
// which exercises everything except the crypto itself. Envelope verification is
// covered exhaustively in internal/provision.
type fakeNode struct{ teeID common.Address }

func (f *fakeNode) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}

func (f *fakeNode) TeeID(ctx context.Context) (common.Address, error) { return f.teeID, nil }

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(provision.NewVerifier(big.NewInt(114), common.Address{}), &fakeNode{})
}

// testEndpoints covers every pinned chain, with a mix of thresholds and a
// third-party provider added the way an operator would add Alchemy.
func testEndpoints() Endpoints {
	return Endpoints{
		"flare": {
			Threshold: 2,
			Providers: []Provider{
				{URL: "https://flare-api.flare.network/ext/C/rpc"},
				{URL: "https://rpc.ankr.com/flare/{secret}", Secret: "ankr_flare_key"},
				{URL: "https://flare-mainnet.g.alchemy.com/v2/{secret}", Secret: "alchemy_flare_key"},
			},
		},
		"ethereum": {
			Threshold: 2,
			Providers: []Provider{
				{URL: "https://ethereum-rpc.publicnode.com"},
				{URL: "https://rpc.ankr.com/eth/{secret}", Secret: "ankr_eth_key"},
			},
		},
		"songbird": {
			Threshold: 1,
			Providers: []Provider{
				{URL: "https://songbird-api.flare.network/ext/C/rpc"},
			},
		},
	}
}

func provisioned(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	if err := setEndpoints(s, testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	for _, ref := range []string{
		config.HexAPIKeyRef, "ankr_flare_key", "ankr_eth_key", "alchemy_flare_key",
	} {
		setSecret(t, s, ref, "value-"+ref)
	}
	return s
}

// setEndpoints marshals and applies, exercising the same decode path a real
// SET_ENDPOINTS instruction takes.
func setEndpoints(s *Store, e Endpoints) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.ApplyEndpoints(raw)
}

// setSecret installs a credential directly, bypassing envelope verification —
// that is covered exhaustively in internal/provision.
func setSecret(t *testing.T, s *Store, ref, value string) {
	t.Helper()
	s.mu.Lock()
	s.secrets[ref] = value
	s.mu.Unlock()
}

// --- what can no longer be configured ---

// The point of the simplification: the pinned policy is what reaches the supply
// reader, and nothing in a payload can change it.
func TestPinnedPolicyIsUsed(t *testing.T) {
	cfg, err := provisioned(t).Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	for _, ch := range cfg.Chains {
		pinnedChain, found := config.LookupChain(ch.Name)
		if !found {
			t.Fatalf("chain %q is not pinned", ch.Name)
		}
		if ch.Token != common.HexToAddress(pinnedChain.Token) {
			t.Errorf("chain %q token = %s, want the pinned %s", ch.Name, ch.Token, pinnedChain.Token)
		}
		if ch.FinalityMargin != pinnedChain.FinalityMargin {
			t.Errorf("chain %q margin = %d, want the pinned %d",
				ch.Name, ch.FinalityMargin, pinnedChain.FinalityMargin)
		}
	}
}

// A Provider has exactly two fields. Anything else an operator might try to
// send — token, quorum, finalityMargin — has nowhere to land.
func TestProviderCarriesOnlyURLAndSecret(t *testing.T) {
	raw := []byte(`{"url":"https://rpc.ankr.com/eth","secret":"k",
	                "token":"0xdead","finalityMargin":0}`)

	var p Provider
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err == nil {
		t.Error("a provider carrying price-determining fields must be rejected")
	}
}

// Provider names are positional, so nothing an operator supplies reaches the
// public GET /state.
func TestProviderNamesAreDerived(t *testing.T) {
	cfg, _ := provisioned(t).Resolve()

	for _, ch := range cfg.Chains {
		for i, p := range ch.Providers {
			want := fmt.Sprintf("rpc_%s_%d", ch.Name, i+1)
			if p.Name != want {
				t.Errorf("provider name = %q, want %q", p.Name, want)
			}
		}
	}
}

// --- the secret split ---

func TestResolveAgainst_PlaceholderRejectedInHost(t *testing.T) {
	if _, err := resolveURL("https://{secret}/eth", "k", "evil.com"); err == nil {
		t.Error("{secret} in the host would let a credential repoint the chain")
	}
}

func TestResolveAgainst_SubstitutesInPathAndQuery(t *testing.T) {
	got, err := resolveURL("https://rpc.ankr.com/eth/{secret}", "k", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://rpc.ankr.com/eth/abc123" {
		t.Errorf("got %q", got)
	}
}

func TestResolveAgainst_RejectsSecretThatWouldAlterURL(t *testing.T) {
	for _, bad := range []string{"a/b", "a?b", "a#b", "a b"} {
		if _, err := resolveURL("https://rpc.ankr.com/eth/{secret}", "k", bad); err == nil {
			t.Errorf("secret %q could restructure the url", bad)
		}
	}
}

func TestResolveAgainst_RequiresHTTPS(t *testing.T) {
	if _, err := resolveURL("http://rpc.ankr.com/x", "", ""); err == nil {
		t.Error("plaintext must be rejected")
	}
}

// --- chain completeness ---

// A missing chain silently undercounts supply, which OVERSTATES the price — the
// error direction this oracle exists to prevent.
func TestSetEndpoints_RejectsMissingChain(t *testing.T) {
	s := newStore(t)
	e := testEndpoints()
	delete(e, "songbird")

	if err := setEndpoints(s, e); err == nil {
		t.Fatal("a missing chain must be rejected")
	}
}

func TestSetEndpoints_RejectsUnknownChain(t *testing.T) {
	s := newStore(t)
	e := testEndpoints()
	e["polygon"] = ChainEndpoints{Threshold: 1, Providers: []Provider{{URL: "https://rpc.ankr.com/polygon"}}}

	if err := setEndpoints(s, e); err == nil {
		t.Fatal("a chain outside the pinned set must be rejected")
	}
}

// --- the configurable threshold ---

// The threshold reaches the supply reader as configured, so 2-of-3 really is
// two of three.
func TestThresholdIsConfigurable(t *testing.T) {
	cfg, err := provisioned(t).Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := map[string]int{"flare": 2, "ethereum": 2, "songbird": 1}
	for _, ch := range cfg.Chains {
		if ch.Quorum != want[ch.Name] {
			t.Errorf("chain %q threshold = %d, want %d", ch.Name, ch.Quorum, want[ch.Name])
		}
	}
}

// Adding a provider and raising the threshold is exactly the change this
// design exists to make possible without a rebuild.
func TestThresholdCanChangeWithoutRebuild(t *testing.T) {
	s := provisioned(t)

	e := testEndpoints()
	flare := e["flare"]
	flare.Threshold = 3 // was 2, of 3 providers
	e["flare"] = flare

	if err := setEndpoints(s, e); err != nil {
		t.Fatalf("raising the threshold must be accepted: %v", err)
	}
	cfg, _ := s.Resolve()
	for _, ch := range cfg.Chains {
		if ch.Name == "flare" && ch.Quorum != 3 {
			t.Errorf("flare threshold = %d, want 3", ch.Quorum)
		}
	}
}

// A threshold above the provider count can never be met, so it would configure
// a chain that fails on every observation.
func TestSetEndpoints_RejectsThresholdAboveProviderCount(t *testing.T) {
	s := newStore(t)
	e := testEndpoints()
	e["songbird"] = ChainEndpoints{
		Threshold: 2,
		Providers: []Provider{{URL: "https://songbird-api.flare.network/ext/C/rpc"}},
	}

	if err := setEndpoints(s, e); err == nil {
		t.Fatal("a threshold above the provider count must be rejected")
	}
}

func TestSetEndpoints_RejectsBadThreshold(t *testing.T) {
	for _, th := range []int{0, -1} {
		s := newStore(t)
		e := testEndpoints()
		e["songbird"] = ChainEndpoints{
			Threshold: th,
			Providers: []Provider{{URL: "https://songbird-api.flare.network/ext/C/rpc"}},
		}
		if err := setEndpoints(s, e); err == nil {
			t.Errorf("threshold %d must be rejected", th)
		}
	}
}

func TestSetEndpoints_RejectsEmptyProviderList(t *testing.T) {
	s := newStore(t)
	e := testEndpoints()
	e["songbird"] = ChainEndpoints{Threshold: 1}

	if err := setEndpoints(s, e); err == nil {
		t.Fatal("a chain with no providers must be rejected")
	}
}

// Any host may be named — that is the decision recorded in the package doc.
// This test exists so the property is visible rather than assumed, and so
// reintroducing an allowlist is a deliberate change to a named test.
func TestAnyHostIsAccepted(t *testing.T) {
	s := newStore(t)
	e := testEndpoints()
	e["songbird"] = ChainEndpoints{
		Threshold: 1,
		Providers: []Provider{{URL: "https://anything-at-all.example.com/rpc"}},
	}

	if err := setEndpoints(s, e); err != nil {
		t.Fatalf("an arbitrary host must be accepted: %v", err)
	}
}

// --- readiness ---

func TestStore_StartsUnconfigured(t *testing.T) {
	s := newStore(t)

	r := s.Readiness()
	if r.Ready || r.EndpointsSet {
		t.Errorf("a fresh store must be unconfigured: %+v", r)
	}
	if _, err := s.Resolve(); err == nil {
		t.Error("Resolve must fail before provisioning")
	}
}

// The Hex key is always reported as required. Since 5 Aug 2026 it no longer
// blocks Resolve — the observation runs cash-less and publishes only where the
// figure provably cannot matter — but Ready means "fully provisioned", and a
// machine that can answer for only part of the price range must not claim it.
func TestStore_HexKeyIsAlwaysRequired(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}

	found := false
	for _, ref := range s.Readiness().MissingRefs {
		if ref == config.HexAPIKeyRef {
			found = true
		}
	}
	if !found {
		t.Errorf("%s must be reported missing", config.HexAPIKeyRef)
	}
	if s.Readiness().Ready {
		t.Error("must not be ready without the Hex key")
	}
}

// Endpoints without the Hex key resolve to a config with no cash source, rather
// than refusing outright. The oracle then decides per observation whether the
// missing figure could have changed the answer; refusing here would take that
// decision away and freeze the feed on a credential the feed does not always
// need.
func TestResolve_WithoutHexKeyYieldsNoCashSource(t *testing.T) {
	s := newStore(t)
	if err := setEndpoints(s, testEndpoints()); err != nil {
		t.Fatalf("SetEndpoints: %v", err)
	}
	// Every provider credential, but deliberately not the Hex key: the provider
	// secrets are still hard requirements, because a URL with an unsubstituted
	// {secret} cannot be dialled at all.
	for _, ref := range []string{"ankr_flare_key", "ankr_eth_key", "alchemy_flare_key"} {
		setSecret(t, s, ref, "value-"+ref)
	}

	cfg, err := s.Resolve()
	if err != nil {
		t.Fatalf("Resolve without the Hex key must succeed, got %v", err)
	}
	if cfg.Bonds == nil {
		t.Error("the bonds source is not optional and must always resolve")
	}
	if cfg.Cash != nil {
		t.Error("Cash must be nil when no credential has been provisioned")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a cash-less config must still validate: %v", err)
	}
}

func TestStore_BecomesReadyWhenEverythingArrives(t *testing.T) {
	s := provisioned(t)

	r := s.Readiness()
	if !r.Ready {
		t.Fatalf("expected ready, got %+v", r)
	}
	if len(r.MissingRefs) != 0 {
		t.Errorf("missing refs = %v", r.MissingRefs)
	}

	cfg, err := s.Resolve()
	if err != nil {
		t.Fatalf("Resolve should succeed once ready: %v", err)
	}
	if len(cfg.Chains) != len(config.Chains) {
		t.Errorf("chains = %d, want %d", len(cfg.Chains), len(config.Chains))
	}
}

// Fire-and-forget delivery gives no ordering guarantee, so a secret may arrive
// before the endpoints that reference it.
func TestStore_SecretBeforeEndpoints(t *testing.T) {
	s := newStore(t)
	setSecret(t, s, config.HexAPIKeyRef, "k")
	if s.Readiness().Ready {
		t.Error("a secret alone must not make the store ready")
	}
}

// A bad update must not take a working feed down.
func TestStore_FailedUpdateKeepsPreviousSnapshot(t *testing.T) {
	s := provisioned(t)
	if !s.Readiness().Ready {
		t.Fatal("setup: expected ready")
	}

	bad := testEndpoints()
	// Threshold above the provider count: invalid, so it must be refused.
	bad["flare"] = ChainEndpoints{Threshold: 9, Providers: []Provider{{URL: "https://x.example.com/rpc"}}}
	if err := setEndpoints(s, bad); err == nil {
		t.Fatal("expected the bad update to be rejected")
	}
	if !s.Readiness().Ready {
		t.Error("a rejected update must leave the working configuration in place")
	}
}

// Rotation must take effect without a restart.
func TestStore_SecretRotationTakesEffect(t *testing.T) {
	s := provisioned(t)
	setSecret(t, s, "ankr_eth_key", "rotated")
	if !s.Readiness().Ready {
		t.Error("the store must stay ready across a rotation")
	}
}

// A credential containing a URL separator could restructure an endpoint after
// substitution, so ApplySecret refuses it after decryption.
func TestApplySecret_RejectsCredentialThatWouldAlterAURL(t *testing.T) {
	// '&' is here because a credential substituted into a query VALUE can
	// otherwise append parameters of its own: "?key=abc&admin=true".
	for _, bad := range []string{"has/slash", "has space", "has?query", "has&amp", ""} {
		if _, err := resolveURL("https://x.example.com/p/{secret}", "k", bad); err == nil {
			t.Errorf("credential %q must be refused", bad)
		}
	}
}

// TestApplySecret_RejectsQuerySeparatorOnArrival checks the same rule at the
// other checkpoint. resolveURL refuses at substitution; this refuses on arrival,
// so a credential that could restructure a URL is never stored at all.
func TestApplySecret_RejectsQuerySeparatorOnArrival(t *testing.T) {
	s := concurrentStore(t)

	msg := envelopeFor(t, config.HexAPIKeyRef, "abc&admin=true", 1)
	if err := s.ApplySecret(context.Background(), msg); err == nil {
		t.Fatal("a credential containing '&' must be refused: it can append query parameters")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.secrets[config.HexAPIKeyRef] != "" {
		t.Error("a rejected credential was stored anyway")
	}
}

// TestSetEndpoints_RejectsBackingCredentialAsProviderSecret is the containment
// for the exfiltration path: the Hex credential authenticates a PINNED endpoint
// by header, so naming it as a provider secret would substitute it into a URL on
// a host the payload author chose, handing it over on the next observation.
//
// The URL author (owner key) and the credential holder (admins) are different
// parties. resolveURL already stops a credential changing its destination; this
// stops the URL author choosing a destination for someone else's credential.
func TestSetEndpoints_RejectsBackingCredentialAsProviderSecret(t *testing.T) {
	e := testEndpoints()

	ce := e["songbird"]
	ce.Providers = append(ce.Providers, Provider{
		URL:    "https://attacker.example/collect/{secret}",
		Secret: config.HexAPIKeyRef,
	})
	ce.Threshold = 1
	e["songbird"] = ce

	s := newStore(t)
	err := setEndpoints(s, e)
	if err == nil {
		t.Fatal("naming the backing credential as a provider secret must be refused")
	}
	if !strings.Contains(err.Error(), config.HexAPIKeyRef) {
		t.Errorf("error should name the offending ref, got: %v", err)
	}

	// Nothing was applied, so a rejected payload cannot leave partial state.
	if s.Readiness().EndpointsSet {
		t.Error("a rejected payload was applied anyway")
	}
}

func TestReadiness_ExposesNoSecrets(t *testing.T) {
	s := provisioned(t)
	for _, ref := range s.Readiness().MissingRefs {
		if strings.Contains(ref, "value-") {
			t.Error("readiness leaks a secret value")
		}
	}
}

// --- concurrency ---
//
// /action is an ordinary net/http handler, so tee-node delivering two actions
// at once runs two handler goroutines. These tests go through the real
// ApplySecret path — envelope and all — rather than the setSecret bypass,
// because what needs protecting lives in the verifier.

// concurrentTeeID must be non-zero: a zero teeId never satisfies Verify's
// machine binding, so the envelope path would reject before reaching anything
// worth testing.
var concurrentTeeID = common.HexToAddress("0x00000000000000000000000000000000000000aa")

var concurrentChainID = big.NewInt(114)

// concurrentStore wires a store whose fakeNode reports a teeId matching the
// verifier's, so real envelopes verify.
func concurrentStore(t *testing.T) *Store {
	t.Helper()
	return New(
		provision.NewVerifier(concurrentChainID, concurrentTeeID),
		&fakeNode{teeID: concurrentTeeID},
	)
}

// envelopeFor builds a message signed by two of the baked dev admins. fakeNode
// echoes ciphertext back as plaintext, so the payload is the credential itself.
func envelopeFor(t *testing.T, ref, value string, version uint64) []byte {
	t.Helper()

	payload := []byte(value)
	env := provision.Envelope{
		ChainID:     concurrentChainID,
		TeeID:       concurrentTeeID,
		Version:     version,
		IssuedAt:    uint64(time.Now().UTC().Unix()),
		Ref:         ref,
		PayloadHash: crypto.Keccak256Hash(payload),
		Payload:     payload,
	}

	body, err := provision.EncodeBody(env)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	digest := accounts.TextHash(crypto.Keccak256(body))

	// Private keys 1 and 2 — the baked development admin set. Threshold is 2, so
	// two distinct signers is exactly enough.
	var sigs [][]byte
	for _, raw := range []string{
		"0000000000000000000000000000000000000000000000000000000000000001",
		"0000000000000000000000000000000000000000000000000000000000000002",
	} {
		key, err := crypto.HexToECDSA(raw)
		if err != nil {
			t.Fatalf("admin key: %v", err)
		}
		sig, err := crypto.Sign(digest, key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		sig[64] += 27 // personal-sign convention
		sigs = append(sigs, sig)
	}

	msg, err := provision.Encode(env, sigs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return msg
}

// TestApplySecret_IsSafeUnderConcurrency drives every entry point at once.
// Without applyMu the verifier's lastVersion map is written unsynchronised,
// which -race reports and which the runtime turns into a fatal concurrent map
// write — a process kill, not a recoverable panic.
func TestApplySecret_IsSafeUnderConcurrency(t *testing.T) {
	s := concurrentStore(t)

	refs := []string{
		config.HexAPIKeyRef, "ankr_flare_key", "ankr_eth_key", "alchemy_flare_key",
	}
	messages := make([][]byte, len(refs))
	for i, ref := range refs {
		messages[i] = envelopeFor(t, ref, "value-"+ref, 1)
	}

	raw, err := json.Marshal(testEndpoints())
	if err != nil {
		t.Fatalf("marshal endpoints: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, len(refs))

	for i := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.ApplySecret(context.Background(), messages[i])
		}()
	}
	// Writers and readers together: a reader blocking behind a provisioning
	// call would deadlock here rather than merely race.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.ApplyEndpoints(raw)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Readiness()
			_, _ = s.Resolve()
		}()
	}
	wg.Wait()

	for i, ref := range refs {
		if errs[i] != nil {
			t.Errorf("ApplySecret(%s): %v", ref, errs[i])
		}
	}

	// Every credential landed, and in its own slot.
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ref := range refs {
		if got := s.secrets[ref]; got != "value-"+ref {
			t.Errorf("secrets[%s] = %q, want %q", ref, got, "value-"+ref)
		}
	}
}

// TestApplySecret_ConcurrentReplayAdmitsExactlyOne is the security half. The
// per-ref version check is read-then-write; run unserialised, several goroutines
// read the same old version and all conclude they are newer, so one captured
// envelope replays as many times as it is submitted in parallel.
func TestApplySecret_ConcurrentReplayAdmitsExactlyOne(t *testing.T) {
	s := concurrentStore(t)

	// One envelope, submitted many times at once — exactly what an attacker
	// replaying a captured message would do.
	message := envelopeFor(t, config.HexAPIKeyRef, "the-credential", 1)

	const attempts = 8
	var wg sync.WaitGroup
	results := make([]error, attempts)

	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.ApplySecret(context.Background(), message)
		}()
	}
	wg.Wait()

	accepted := 0
	for _, err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Errorf("%d of %d replays accepted, want exactly 1", accepted, attempts)
	}
}
