package observation

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/observation/backing"
	"fce-usdx-oracle/internal/observation/supply"
	oracletypes "fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/pkg/wire/feedupdate"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// --- fakes ---

// fakeChain serves totalSupply from a script: one entry per call. Any entry
// past the first is only reached if something reads supply more than once,
// which is what TestObserve_ReadsSupplyOncePerChain pins down.
type fakeChain struct {
	mu       sync.Mutex
	supplies []string // consumed one per totalSupply call
	calls    int      // totalSupply calls served
	decimals uint8
	err      error

	// blockTime overrides the pinned block's header timestamp. Zero means "now",
	// which keeps MaxBlockAge satisfied for the tests that do not care; the ones
	// that assert observedAt set it to something distinguishable from wall clock.
	blockTime time.Time
}

// supplyCalls reports how many totalSupply calls this chain has served.
func (f *fakeChain) supplyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// HeaderByNumber serves both the head (n == nil) and the pinned block. The timestamp
// is "now" rather than zero: agreedBlockTime treats a zero stamp as malformed.
func (f *fakeChain) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if f.err != nil {
		return nil, f.err
	}
	number := big.NewInt(1000)
	if n != nil {
		number = new(big.Int).Set(n)
	}
	stamp := f.blockTime
	if stamp.IsZero() {
		stamp = time.Now()
	}
	return &types.Header{Number: number, Time: uint64(stamp.Unix())}, nil
}

func (f *fakeChain) CallContract(ctx context.Context, call ethereum.CallMsg, block *big.Int) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	// decimals()
	if len(call.Data) == 4 && call.Data[0] == 0x31 {
		return common.LeftPadBytes(big.NewInt(int64(f.decimals)).Bytes(), 32), nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	s := f.supplies[0]
	if len(f.supplies) > 1 {
		f.supplies = f.supplies[1:]
	}
	v, _ := new(big.Int).SetString(s, 10)
	return common.LeftPadBytes(v.Bytes(), 32), nil
}

type fakeSource struct {
	name   string
	amount string
	err    error
	delay  time.Duration
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(ctx context.Context) (backing.Reading, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return backing.Reading{}, failure.BackingTimeout
		case <-time.After(f.delay):
		}
	}
	if f.err != nil {
		return backing.Reading{}, f.err
	}
	amt, _ := new(big.Rat).SetString(f.amount)
	return backing.Reading{
		Amount: amt, AsOf: time.Now().UTC(), RetrievedAt: time.Now().UTC(),
	}, nil
}

// fakeSigner records what it was asked to sign, so tests can assert the signed
// bytes really are the encoded report body.
type fakeSigner struct {
	err    error
	signed [][]byte
	mu     sync.Mutex
}

func (s *fakeSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	s.signed = append(s.signed, message)
	s.mu.Unlock()
	sig := make([]byte, 65)
	sig[64] = 27
	return sig, nil
}

// --- helpers ---

// testEndpointsHash stands in for keccak256 of a SET_ENDPOINTS payload.
var testEndpointsHash = common.HexToHash("0x9f3c")

// testAdminsHash stands in for keccak256 of a SET_ADMINS payload. Distinct from
// testEndpointsHash so a test cannot pass by carrying the wrong one through.
var testAdminsHash = common.HexToHash("0x5e21")

// The feed this test machine speaks for. Non-zero because the store's sentinels
// are zero: pkg/wire/feedupdate refuses to sign an update naming neither.
var testExtensionID = big.NewInt(66281)

var testFeedID = [21]byte{
	0x20, 'U', 'S', 'D', 'X', '/', 'U', 'S', 'D',
}

func testConfig(t *testing.T, chain *fakeChain, bonds, cash *fakeSource) (Sources, Binding) {
	t.Helper()
	sources := Sources{
		Chains: []supply.ChainSource{{
			Name:  "flare",
			Token: common.HexToAddress("0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B"),
			Providers: []supply.Provider{
				{Name: "rpc_flare_1", Client: chain},
			},
			Quorum:          1,
			ProviderTimeout: time.Second,
		}},
		Bonds: bonds,
		Cash:  cashSource(cash),
	}
	return sources, Binding{
		ExtensionID:   testExtensionID,
		FeedID:        testFeedID,
		EndpointsHash: testEndpointsHash,
		AdminsHash:    testAdminsHash,
	}
}

// cashSource converts a nil *fakeSource into a nil interface. A typed nil would leave
// Sources.Cash non-nil, and the cash-less path is selected by exactly that comparison
// — so the shortcut would test the wrong branch and then panic in Name().
func cashSource(f *fakeSource) backing.Source {
	if f == nil {
		return nil
	}
	return f
}

// observe runs both halves back to back — observe, then sign — which is what
// most of these tests assert on. Production splits them across a timer and a
// request; the arithmetic and the fail-closed rules are identical either way.
// observeConfig builds the standard single-chain configuration and observes it,
// for the tests that never touch the binding.
func observeConfig(t *testing.T, chain *fakeChain, bonds, cash *fakeSource) (Observation, error) {
	t.Helper()
	src, binding := testConfig(t, chain, bonds, cash)
	return observe(t, src, binding)
}

func observe(t *testing.T, src Sources, binding Binding) (Observation, error) {
	t.Helper()
	return observeWith(t, src, binding, &fakeSigner{})
}

func observeWith(t *testing.T, src Sources, binding Binding, signer Signer) (Observation, error) {
	t.Helper()
	obs, err := Observe(context.Background(), src, binding)
	if err != nil {
		return obs, err
	}
	return Sign(context.Background(), signer, obs, testRequestedAt)
}

// testRequestedAt is the block timestamp of the instruction that asks for the
// update: what the update is signed with as observedAt.
const testRequestedAt uint64 = 1_800_000_000

// --- the single supply pass ---

// Supply is read once per chain per observation: the double-read that used to bracket
// the reserve read doubled the fail-closed surface for nothing. The call count is
// asserted rather than inferred, so a reintroduced bracket fails here.
func TestObserve_ReadsSupplyOncePerChain(t *testing.T) {
	// The real event from spec §6.1: 1,000,000 USDX burned, 4.8% of supply. A
	// second pass would consume the post-burn figure; one pass never sees it.
	chain := &fakeChain{
		supplies: []string{"21620459685024", "20620459685024"},
		decimals: 6,
	}
	bonds := &fakeSource{name: "hex_bonds", amount: "20620459.685024"}
	cash := &fakeSource{name: "hex_cash", amount: "0"}

	got, err := observeConfig(t, chain, bonds, cash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := chain.supplyCalls(); n != 1 {
		t.Errorf("totalSupply called %d times, want 1", n)
	}

	// 20620459.685024 / 21620459.685024 = 0.9537475144114222...
	// truncated at 8 decimals -> 95374751. Verified independently with exact
	// rational arithmetic, not derived from this implementation.
	if want := int32(95_374_751); got.Value != want {
		t.Errorf("value = %d, want %d", got.Value, want)
	}
}

func TestObserve_StableSupplyGivesPar(t *testing.T) {
	chain := &fakeChain{supplies: []string{"25452544524018"}, decimals: 6}
	bonds := &fakeSource{name: "hex_bonds", amount: "25452544.52"}
	cash := &fakeSource{name: "hex_cash", amount: "205304.47"}

	got, err := observeConfig(t, chain, bonds, cash)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The production figures from spec §3.0: backing ≥ supply, so par.
	if want := int32(100_000_000); got.Value != want {
		t.Errorf("value = %d, want %d", got.Value, want)
	}
}

// --- fail closed ---

func TestObserve_FailsClosed(t *testing.T) {
	ok := func() *fakeChain { return &fakeChain{supplies: []string{"1000000"}, decimals: 6} }

	cases := []struct {
		name  string
		chain *fakeChain
		bonds *fakeSource
		cash  *fakeSource
		want  failure.Failure
	}{
		{
			"bonds source down", ok(),
			&fakeSource{name: "hex_bonds", err: failure.BackingUnavailable},
			&fakeSource{name: "hex_cash", amount: "1"},
			failure.BackingUnavailable,
		},
		{
			// A cash outage is fatal only when the figure could have changed
			// the answer. Bonds at half of supply is squarely that case: the
			// price is genuinely below par and cash decides how far.
			"cash source down below par", ok(),
			&fakeSource{name: "hex_bonds", amount: "0.5"},
			&fakeSource{name: "hex_cash", err: failure.BackingTimeout},
			failure.BackingUnavailable,
		},
		{
			"chain down",
			&fakeChain{supplies: []string{"1"}, decimals: 6, err: errors.New("refused")},
			&fakeSource{name: "hex_bonds", amount: "1"},
			&fakeSource{name: "hex_cash", amount: "1"},
			failure.SupplyUnavailable,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := observeConfig(t, c.chain, c.bonds, c.cash)
			if !errors.Is(err, c.want) {
				t.Errorf("expected %v, got %v", c.want, err)
			}
		})
	}
}

func TestObserve_ZeroSupplyFailsClosed(t *testing.T) {
	chain := &fakeChain{supplies: []string{"0"}, decimals: 6}
	bonds := &fakeSource{name: "hex_bonds", amount: "100"}
	cash := &fakeSource{name: "hex_cash", amount: "0"}

	if _, err := observeConfig(t, chain, bonds, cash); !errors.Is(err, failure.SupplyZero) {
		t.Errorf("expected SupplyZero, got %v", err)
	}
}

func TestObserve_SigningFailurePublishesNothing(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observeWith(t, src, binding, &fakeSigner{err: failure.SignUnavailable})
	if !errors.Is(err, failure.SignUnavailable) {
		t.Fatalf("expected SignUnavailable, got %v", err)
	}
	if len(got.Data) != 0 {
		t.Error("a failed observation must carry no payload")
	}
}

// The deadline is a constant in internal/config, so it is exercised by
// cancelling the caller's context instead — the property under test is that the
// observation honours a deadline at all, not which one.
func TestObserve_RespectsContextCancellation(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1", delay: 3 * time.Second},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := Observe(ctx, src, binding); err == nil {
		t.Fatal("expected the observation to fail once its context expired")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("observation ran %v past its cancelled context", elapsed)
	}
}

// --- payload ---

// The signed body must be the report, and the payload must carry it verbatim
// alongside the signature.
func TestObserve_SignsTheEncodedBody(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	signer := &fakeSigner{}
	got, err := observeWith(t, src, binding, signer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(signer.signed) != 1 {
		t.Fatalf("expected exactly one signing call, got %d", len(signer.signed))
	}
	// The signer is handed the SignedPayload PREIMAGE, not the feed update and
	// not the hash: tee-node keccaks what it is given before applying EIP-191.
	if len(signer.signed[0]) != feedupdate.PayloadLength {
		t.Errorf("signed %d bytes, want the %d-byte SignedPayload preimage",
			len(signer.signed[0]), feedupdate.PayloadLength)
	}
	if want, err := feedupdate.SigningMessage(feedupdate.FeedUpdate{
		ExtensionID:   testExtensionID,
		FeedID:        testFeedID,
		Value:         got.Value,
		Decimals:      got.Decimals,
		ObservedAt:    testRequestedAt,
		EndpointsHash: testEndpointsHash,
		AdminsHash:    testAdminsHash,
	}, config.ChainID()); err != nil {
		t.Fatalf("SigningMessage: %v", err)
	} else if !bytes.Equal(signer.signed[0], want) {
		t.Error("the signed message is not the SignedPayload preimage for this observation")
	}

	r, sig, err := feedupdate.DecodeData(got.Data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if sig.V != 27 && sig.V != 28 {
		t.Errorf("signature v = %d, want 27 or 28", sig.V)
	}

	// The update names the exact feed, so it cannot be replayed onto another
	// feed the same extension serves.
	if r.ExtensionID.Cmp(testExtensionID) != 0 || r.FeedID != testFeedID {
		t.Errorf("feed binding = %v/%x", r.ExtensionID, r.FeedID)
	}
	// The signed update commits to the configuration this price was computed
	// under, so the store can refuse a machine running something else.
	if r.EndpointsHash != testEndpointsHash {
		t.Errorf("endpointsHash = %s, want %s", r.EndpointsHash.Hex(), testEndpointsHash.Hex())
	}
	// observedAt is the requesting instruction's block timestamp, as the store
	// defines it — not the reading's own time, which stays on the observation.
	if r.ObservedAt != testRequestedAt {
		t.Errorf("observedAt in body = %d, want the request's block timestamp %d",
			r.ObservedAt, testRequestedAt)
	}
}

// The signed observedAt is the requesting instruction's block timestamp: the
// store's definition of the field, and the one value every machine answering
// the same request shares, which is what lets their updates form one batch.
// The reading behind it keeps its own time on the observation.
func TestSign_StampsTheRequestingBlockTimestamp(t *testing.T) {
	readAt := time.Unix(int64(testRequestedAt)-53, 0)
	chain := &fakeChain{supplies: []string{"1000000"}, decimals: 6, blockTime: readAt}
	bonds := &fakeSource{name: "hex_bonds", amount: "1.0"}
	src, binding := testConfig(t, chain, bonds, nil)

	obs, err := Observe(context.Background(), src, binding)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.ObservedAt != uint64(readAt.Unix()) {
		t.Fatalf("reading time = %d, want the block time %d", obs.ObservedAt, readAt.Unix())
	}

	signed, err := Sign(context.Background(), &fakeSigner{}, obs, testRequestedAt)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	r, _, err := feedupdate.DecodeData(signed.Data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if r.ObservedAt != testRequestedAt {
		t.Errorf("signed observedAt = %d, want the request's block timestamp %d", r.ObservedAt, testRequestedAt)
	}
	if signed.ObservedAt != obs.ObservedAt {
		t.Errorf("signing moved the reading's own time from %d to %d", obs.ObservedAt, signed.ObservedAt)
	}
	if r.Value != obs.Value || r.Decimals != obs.Decimals {
		t.Errorf("signed %d/%d, want the observation's own %d/%d", r.Value, r.Decimals, obs.Value, obs.Decimals)
	}
}

// Zero is the store's "never seen" sentinel and marks an instruction without a
// block timestamp; it is refused by name, before anything reaches the signer.
func TestSign_RefusesAZeroTimestamp(t *testing.T) {
	chain := &fakeChain{supplies: []string{"1000000"}, decimals: 6}
	bonds := &fakeSource{name: "hex_bonds", amount: "1.0"}
	src, binding := testConfig(t, chain, bonds, nil)
	obs, err := Observe(context.Background(), src, binding)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	signer := &fakeSigner{}
	if _, err := Sign(context.Background(), signer, obs, 0); !errors.Is(err, failure.MissingRequestTime) {
		t.Fatalf("expected failure.MissingRequestTime, got %v", err)
	}
	if len(signer.signed) != 0 {
		t.Errorf("a zero timestamp reached the signer %d times", len(signer.signed))
	}
}

// --- health ---

// Every configured source must appear, so an absent key never reads as healthy.
func TestObserve_HealthSeededForEverySource(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observe(t, src, binding)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"rpc_flare", "hex_bonds", "hex_cash"} {
		status, ok := got.Health[name]
		if !ok {
			t.Errorf("source %q missing from health; absence reads as healthy", name)
			continue
		}
		if status != oracletypes.HealthOK {
			t.Errorf("source %q = %q, want %q", name, status, oracletypes.HealthOK)
		}
	}
}

// A failed observation still reports health, so an operator can see WHICH
// source broke rather than only that something did.
func TestObserve_HealthReportedOnFailure(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", err: failure.BackingUnavailable},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observe(t, src, binding)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if got.Health["hex_bonds"] != oracletypes.HealthDown {
		t.Errorf("hex_bonds = %q, want %q", got.Health["hex_bonds"], oracletypes.HealthDown)
	}
	if got.Health["hex_cash"] != oracletypes.HealthOK {
		t.Errorf("hex_cash = %q, want %q", got.Health["hex_cash"], oracletypes.HealthOK)
	}
}

// --- errors are publishable ---

func TestObserve_AllErrorsArePublishable(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1"}, decimals: 6, err: errors.New("dial tcp 10.0.0.1:8545: refused")},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	_, err := observe(t, src, binding)
	if err == nil {
		t.Fatal("expected an error")
	}
	var f failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v is not a failure.Failure and could leak an endpoint", err)
	}
	if got := err.Error(); got == "dial tcp 10.0.0.1:8545: refused" {
		t.Error("the raw transport error reached a publishable surface")
	}
}

// --- observing without cash ---

// The cash figure has one unredundant source, so its absence must not take the
// feed down on its own. What it must never do is change the published number:
// this publishes only where cash provably cannot matter.
func TestObserve_WithoutCash(t *testing.T) {
	// Supply is 1.0 at 6 decimals throughout.
	chain := func() *fakeChain { return &fakeChain{supplies: []string{"1000000"}, decimals: 6} }

	cases := []struct {
		name  string
		bonds string
		want  *big.Int // nil means the observation must fail closed
	}{
		{"bonds above supply publishes par", "1.5", big.NewInt(100_000_000)},
		{"bonds equal to supply publishes par", "1", big.NewInt(100_000_000)},

		// The case this rule exists for, scaled down: Hex quotes to the cent, so
		// bonds lands a hair under supply and rounding carries it to par.
		{"bonds a hair under supply publishes par", "0.999999999", big.NewInt(100_000_000)},

		// Beyond half a ULP the cash figure decides, and there is nothing
		// honest to publish.
		{"bonds beyond half a ULP fails closed", "0.999999994", nil},
		{"one percent short fails closed", "0.99", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, binding := testConfig(t, chain(), &fakeSource{name: "hex_bonds", amount: c.bonds}, nil)

			got, err := observe(t, src, binding)
			if c.want == nil {
				if !errors.Is(err, failure.BackingUnavailable) {
					t.Fatalf("expected failure.BackingUnavailable, got value=%d err=%v", got.Value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if want := int32(c.want.Int64()); got.Value != want {
				t.Errorf("value = %d, want %d", got.Value, want)
			}
		})
	}
}

// Whatever the cash-less path publishes must equal what the same observation would
// have published with the source present. Divergence means the extension is
// publishing a degraded number under a signature that says otherwise.
func TestObserve_WithoutCash_MatchesTheFullObservation(t *testing.T) {
	chain := func() *fakeChain { return &fakeChain{supplies: []string{"1000000"}, decimals: 6} }
	bonds := func() *fakeSource { return &fakeSource{name: "hex_bonds", amount: "0.999999999"} }

	cashless, err := observeConfig(t, chain(), bonds(), nil)
	if err != nil {
		t.Fatalf("cash-less observation failed: %v", err)
	}

	for _, cash := range []string{"0", "0.000000001", "0.5", "1000"} {
		full, err := observeConfig(t, chain(), bonds(),
			&fakeSource{name: "hex_cash", amount: cash})
		if err != nil {
			t.Fatalf("cash=%s: %v", cash, err)
		}
		if full.Value != cashless.Value {
			t.Errorf("cash=%s: full observation gives %d, cash-less path published %d",
				cash, full.Value, cashless.Value)
		}
	}
}

// An unprovisioned source must not simply disappear from health. A missing key
// reads as "no such source", which is indistinguishable from healthy to anyone
// reading the readiness report — and it is a different remedy from an outage.
func TestObserve_WithoutCash_ReportsNotConfigured(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"}, nil)

	got, err := observe(t, src, binding)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h := got.Health[config.HexCashName]; h != oracletypes.HealthNotConfigured {
		t.Errorf("health[%s] = %q, want %q", config.HexCashName, h, oracletypes.HealthNotConfigured)
	}
	if h := got.Health["hex_bonds"]; h != oracletypes.HealthOK {
		t.Errorf("health[hex_bonds] = %q, want %q", h, oracletypes.HealthOK)
	}
}

// A configured source that fails is an outage, not an absence, even on the
// observations that still publish.
func TestObserve_CashDownReportsDownNotAbsent(t *testing.T) {
	src, binding := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", err: failure.BackingTimeout})

	got, err := observe(t, src, binding)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h := got.Health["hex_cash"]; h != oracletypes.HealthDown {
		t.Errorf("health[hex_cash] = %q, want %q", h, oracletypes.HealthDown)
	}
}

// --- config ---

func TestSourcesValidate(t *testing.T) {
	base, _ := testConfig(t,
		&fakeChain{supplies: []string{"1"}, decimals: 6},
		&fakeSource{name: "hex_bonds"}, &fakeSource{name: "hex_cash"},
	)
	if err := base.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Sources)
	}{
		{"no chains", func(s *Sources) { s.Chains = nil }},
		{"no bonds source", func(s *Sources) { s.Bonds = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, _ := testConfig(t,
				&fakeChain{supplies: []string{"1"}, decimals: 6},
				&fakeSource{name: "hex_bonds"}, &fakeSource{name: "hex_cash"},
			)
			tc.mutate(&src)
			if err := src.Validate(); err == nil {
				t.Error("expected a sources error")
			}
		})
	}
}

// --- the observation timestamp ---

// The reading's own time is the HOME CHAIN'S BLOCK TIME, not the enclave's wall
// clock: an already-mined block is an instant the host cannot move, so the age
// bounds measured from it mean something. It is not what the update is signed
// with — see TestSign_StampsTheRequestingBlockTimestamp.
func TestObserve_StampsWithTheHomeChainBlockTime(t *testing.T) {
	// Far enough back that it cannot be confused with time.Now().
	blockTime := time.Now().Add(-37 * time.Second).Truncate(time.Second)

	chain := &fakeChain{supplies: []string{"1000000"}, decimals: 6, blockTime: blockTime}
	bonds := &fakeSource{name: "hex_bonds", amount: "1.0"}

	got, err := observeConfig(t, chain, bonds, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := uint64(blockTime.Unix()); got.ObservedAt != want {
		t.Errorf("observedAt = %d, want the block time %d", got.ObservedAt, want)
	}
	if got.ObservedAt >= uint64(time.Now().Unix()) {
		t.Error("the reading's time is not strictly in the past")
	}
}

// A home chain whose reading carries no usable block time fails the observation
// rather than stamping the epoch, which the store would reject as NotNewer —
// after the fee had been paid.
func TestObserve_FailsClosedWithoutAHomeChainStamp(t *testing.T) {
	chain := &fakeChain{supplies: []string{"1000000"}, decimals: 6}
	bonds := &fakeSource{name: "hex_bonds", amount: "1.0"}

	src, binding := testConfig(t, chain, bonds, nil)
	// testConfig builds the single chain as the home chain; renaming it leaves the
	// observation with no home reading to stamp from.
	src.Chains[0].Name = "not-the-home-chain"

	if _, err := observe(t, src, binding); err == nil {
		t.Fatal("an observation with no home-chain stamp must fail")
	}
}
