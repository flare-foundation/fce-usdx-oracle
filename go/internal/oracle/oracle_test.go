package oracle

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/backing"
	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/report"
	"fce-usdx-oracle/internal/supply"

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
}

// supplyCalls reports how many totalSupply calls this chain has served.
func (f *fakeChain) supplyCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// HeaderByNumber serves both the head (n == nil) and the pinned block. The
// timestamp is "now" rather than zero because no real chain produces a block
// without one, and supply.agreedBlockTime treats a zero stamp as a malformed
// response.
func (f *fakeChain) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if f.err != nil {
		return nil, f.err
	}
	number := big.NewInt(1000)
	if n != nil {
		number = new(big.Int).Set(n)
	}
	return &types.Header{Number: number, Time: uint64(time.Now().Unix())}, nil
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

func testConfig(t *testing.T, chain *fakeChain, bonds, cash *fakeSource) Config {
	t.Helper()
	return Config{
		Chains: []supply.ChainConfig{{
			Name:  "flare",
			Token: common.HexToAddress("0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B"),
			Providers: []supply.Provider{
				{Name: "rpc_flare_1", Client: chain},
			},
			Quorum: 1,
		}},
		Bonds:         bonds,
		Cash:          cashSource(cash),
		EndpointsHash: testEndpointsHash,
		AdminsHash:    testAdminsHash,
	}
}

// cashSource converts a nil *fakeSource into a nil interface. Assigning the
// typed nil directly would leave Config.Cash non-nil holding a nil pointer, and
// the cash-less path is selected by exactly that comparison — so the shortcut
// would silently test the wrong branch and then panic in Name().
func cashSource(f *fakeSource) backing.Source {
	if f == nil {
		return nil
	}
	return f
}

// observe runs both halves back to back — observe, then sign — which is what
// most of these tests assert on. Production splits them across a timer and a
// request; the arithmetic and the fail-closed rules are identical either way.
func observe(t *testing.T, cfg Config) (Observation, error) {
	t.Helper()
	return observeWith(t, cfg, &fakeSigner{}, common.HexToHash("0xabc"))
}

func observeWith(t *testing.T, cfg Config, signer Signer, id common.Hash) (Observation, error) {
	t.Helper()
	obs, err := Observe(context.Background(), cfg)
	if err != nil {
		return obs, err
	}
	return Sign(context.Background(), signer, obs, id)
}

// --- the single supply pass ---

// Supply is read once per chain per observation. The conservative double-read
// that used to bracket the reserve read was dropped: each pass is already
// pinned to one agreed block, and the reserve figure moves on Hex's ~10 minute
// tick, so a second pass changed the answer only when a burn or mint landed in
// the few seconds between the two pinned blocks — while doubling the supply-side
// fail-closed surface.
//
// The call count is asserted rather than inferred from the price, so a
// reintroduced bracket fails here instead of showing up as a latency
// regression.
func TestObserve_ReadsSupplyOncePerChain(t *testing.T) {
	// The real event from spec §6.1: 1,000,000 USDX burned, 4.8% of supply. A
	// second pass would consume the post-burn figure; one pass never sees it.
	chain := &fakeChain{
		supplies: []string{"21620459685024", "20620459685024"},
		decimals: 6,
	}
	bonds := &fakeSource{name: "hex_bonds", amount: "20620459.685024"}
	cash := &fakeSource{name: "hex_cash", amount: "0"}

	got, err := observe(t, testConfig(t, chain, bonds, cash))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if n := chain.supplyCalls(); n != 1 {
		t.Errorf("totalSupply called %d times, want 1", n)
	}

	// 20620459.685024 / 21620459.685024 = 0.9537475144114222...
	// truncated at 8 decimals -> 95374751. Verified independently with exact
	// rational arithmetic, not derived from this implementation.
	if want := big.NewInt(95_374_751); got.Price.Cmp(want) != 0 {
		t.Errorf("price = %s, want %s", got.Price, want)
	}
}

func TestObserve_StableSupplyGivesPar(t *testing.T) {
	chain := &fakeChain{supplies: []string{"25452544524018"}, decimals: 6}
	bonds := &fakeSource{name: "hex_bonds", amount: "25452544.52"}
	cash := &fakeSource{name: "hex_cash", amount: "205304.47"}

	got, err := observe(t, testConfig(t, chain, bonds, cash))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The production figures from spec §3.0: backing ≥ supply, so par.
	if want := big.NewInt(100_000_000); got.Price.Cmp(want) != 0 {
		t.Errorf("price = %s, want %s", got.Price, want)
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
			_, err := observe(t, testConfig(t, c.chain, c.bonds, c.cash))
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

	if _, err := observe(t, testConfig(t, chain, bonds, cash)); !errors.Is(err, failure.SupplyZero) {
		t.Errorf("expected SupplyZero, got %v", err)
	}
}

func TestObserve_SigningFailurePublishesNothing(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observeWith(t, cfg, &fakeSigner{err: failure.SignUnavailable}, common.HexToHash("0xabc"))
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
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1", delay: 3 * time.Second},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := Observe(ctx, cfg); err == nil {
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
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	signer := &fakeSigner{}
	got, err := observeWith(t, cfg, signer, common.HexToHash("0xdeadbeef"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(signer.signed) != 1 {
		t.Fatalf("expected exactly one signing call, got %d", len(signer.signed))
	}
	if len(signer.signed[0]) != report.BodyLength {
		t.Errorf("signed %d bytes, want the %d-byte report body",
			len(signer.signed[0]), report.BodyLength)
	}

	body, sig, err := report.DecodeData(got.Data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if len(sig) != 65 {
		t.Errorf("signature length %d, want 65", len(sig))
	}

	r, err := report.DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if r.InstructionID != common.HexToHash("0xdeadbeef") {
		t.Errorf("instructionId = %s", r.InstructionID.Hex())
	}
	// The signed body commits to the configuration this price was computed
	// under, so UsdxPriceStore can refuse a machine running something else.
	if r.EndpointsHash != testEndpointsHash {
		t.Errorf("endpointsHash = %s, want %s", r.EndpointsHash.Hex(), testEndpointsHash.Hex())
	}
	if r.ObservedAt != got.ObservedAt {
		t.Errorf("observedAt in body (%d) differs from the observation (%d); "+
			"it is the store's monotonicity key and must be signed",
			r.ObservedAt, got.ObservedAt)
	}
}

// --- health ---

// Every configured source must appear, so an absent key never reads as healthy.
func TestObserve_HealthSeededForEverySource(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observe(t, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, name := range []string{"rpc_flare", "hex_bonds", "hex_cash"} {
		status, ok := got.Health[name]
		if !ok {
			t.Errorf("source %q missing from health; absence reads as healthy", name)
			continue
		}
		if status != config.HealthOK {
			t.Errorf("source %q = %q, want %q", name, status, config.HealthOK)
		}
	}
}

// A failed observation still reports health, so an operator can see WHICH
// source broke rather than only that something did.
func TestObserve_HealthReportedOnFailure(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", err: failure.BackingUnavailable},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	got, err := observe(t, cfg)
	if err == nil {
		t.Fatal("expected a failure")
	}
	if got.Health["hex_bonds"] != config.HealthDown {
		t.Errorf("hex_bonds = %q, want %q", got.Health["hex_bonds"], config.HealthDown)
	}
	if got.Health["hex_cash"] != config.HealthOK {
		t.Errorf("hex_cash = %q, want %q", got.Health["hex_cash"], config.HealthOK)
	}
}

// --- errors are publishable ---

func TestObserve_AllErrorsArePublishable(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1"}, decimals: 6, err: errors.New("dial tcp 10.0.0.1:8545: refused")},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", amount: "0"},
	)

	_, err := observe(t, cfg)
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
			cfg := testConfig(t, chain(), &fakeSource{name: "hex_bonds", amount: c.bonds}, nil)

			got, err := observe(t, cfg)
			if c.want == nil {
				if !errors.Is(err, failure.BackingUnavailable) {
					t.Fatalf("expected failure.BackingUnavailable, got price=%v err=%v", got.Price, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Price.Cmp(c.want) != 0 {
				t.Errorf("price = %s, want %s", got.Price, c.want)
			}
		})
	}
}

// Whatever the cash-less path publishes must equal what the same observation
// would have published with the source present. If these ever diverge, the
// extension is silently publishing a degraded number under a signature that
// says otherwise.
func TestObserve_WithoutCash_MatchesTheFullObservation(t *testing.T) {
	chain := func() *fakeChain { return &fakeChain{supplies: []string{"1000000"}, decimals: 6} }
	bonds := func() *fakeSource { return &fakeSource{name: "hex_bonds", amount: "0.999999999"} }

	cashless, err := observe(t, testConfig(t, chain(), bonds(), nil))
	if err != nil {
		t.Fatalf("cash-less observation failed: %v", err)
	}

	for _, cash := range []string{"0", "0.000000001", "0.5", "1000"} {
		full, err := observe(t, testConfig(t, chain(), bonds(),
			&fakeSource{name: "hex_cash", amount: cash}))
		if err != nil {
			t.Fatalf("cash=%s: %v", cash, err)
		}
		if full.Price.Cmp(cashless.Price) != 0 {
			t.Errorf("cash=%s: full observation gives %s, cash-less path published %s",
				cash, full.Price, cashless.Price)
		}
	}
}

// An unprovisioned source must not simply disappear from health. A missing key
// reads as "no such source", which is indistinguishable from healthy to anyone
// reading GET /state — and it is a different remedy from an outage.
func TestObserve_WithoutCash_ReportsNotConfigured(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"}, nil)

	got, err := observe(t, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h := got.Health[config.HexCashName]; h != config.HealthNotConfigured {
		t.Errorf("health[%s] = %q, want %q", config.HexCashName, h, config.HealthNotConfigured)
	}
	if h := got.Health["hex_bonds"]; h != config.HealthOK {
		t.Errorf("health[hex_bonds] = %q, want %q", h, config.HealthOK)
	}
}

// A configured source that fails is an outage, not an absence, even on the
// observations that still publish.
func TestObserve_CashDownReportsDownNotAbsent(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1"},
		&fakeSource{name: "hex_cash", err: failure.BackingTimeout})

	got, err := observe(t, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h := got.Health["hex_cash"]; h != config.HealthDown {
		t.Errorf("health[hex_cash] = %q, want %q", h, config.HealthDown)
	}
}

// --- config ---

func TestConfigValidate(t *testing.T) {
	base := testConfig(t,
		&fakeChain{supplies: []string{"1"}, decimals: 6},
		&fakeSource{name: "hex_bonds"}, &fakeSource{name: "hex_cash"},
	)
	if err := base.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"no chains", func(c *Config) { c.Chains = nil }},
		{"no bonds source", func(c *Config) { c.Bonds = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t,
				&fakeChain{supplies: []string{"1"}, decimals: 6},
				&fakeSource{name: "hex_bonds"}, &fakeSource{name: "hex_cash"},
			)
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("expected a config error")
			}
		})
	}
}
