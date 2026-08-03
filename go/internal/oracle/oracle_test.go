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

// fakeChain serves totalSupply from a script: one entry per call round, so a
// test can make supply change between the two passes of the double-read.
type fakeChain struct {
	mu       sync.Mutex
	supplies []string // consumed one per totalSupply call
	decimals uint8
	err      error
}

func (f *fakeChain) HeaderByNumber(ctx context.Context, n *big.Int) (*types.Header, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &types.Header{Number: big.NewInt(1000)}, nil
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

func testConfig(t *testing.T, chain *fakeChain, bonds, cash *fakeSource) Config {
	t.Helper()
	feedID, err := report.ParseFeedID(0x20, "USDX/USD")
	if err != nil {
		t.Fatalf("ParseFeedID: %v", err)
	}
	return Config{
		Chains: []supply.ChainConfig{{
			Name:  "flare",
			Token: common.HexToAddress("0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B"),
			Providers: []supply.Provider{
				{Name: "rpc_flare_1", Client: chain},
			},
			Quorum: 1, FinalityTag: supply.TagLatest, FinalityMargin: 10,
		}},
		Bonds:    bonds,
		Cash:     cash,
		Decimals: 8,
		ChainID:  big.NewInt(114),
		Store:    common.HexToAddress("0x00000000000000000000000000000000000000AA"),
		FeedID:   feedID,
		Deadline: 5 * time.Second,
		Window:   30 * time.Second,
	}
}

func observe(t *testing.T, cfg Config) (Observation, error) {
	t.Helper()
	return Observe(context.Background(), cfg, &fakeSigner{}, common.HexToHash("0xabc"))
}

// --- the double-read ---

// The headline safety property. A redemption landing mid-observation reduces
// supply; taking max(S₁, S₂) keeps the LARGER supply, so the price is biased
// DOWN. An over-stated price would hide a shortfall.
func TestObserve_DoubleReadBiasesPriceDown(t *testing.T) {
	// 1,000,000 USDX burned between the two passes — the real event from
	// spec §6.1, 4.8% of supply.
	chain := &fakeChain{
		supplies: []string{"21620459685024", "20620459685024"},
		decimals: 6,
	}
	// Reserves matching the POST-burn supply, as Hex's figure eventually does.
	bonds := &fakeSource{name: "hex_bonds", amount: "20620459.685024"}
	cash := &fakeSource{name: "hex_cash", amount: "0"}

	got, err := observe(t, testConfig(t, chain, bonds, cash))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Naive (post-burn supply only) would give exactly par. The conservative
	// read uses the pre-burn supply, so the price must come out BELOW par.
	par := big.NewInt(100_000_000)
	if got.Price.Cmp(par) >= 0 {
		t.Errorf("price %s is at or above par; the double-read must bias downward "+
			"when supply falls mid-observation", got.Price)
	}

	// 20620459.685024 / 21620459.685024 = 0.9537475144114222...
	// truncated at 8 decimals -> 95374751. Verified independently with exact
	// rational arithmetic, not derived from this implementation.
	if want := big.NewInt(95_374_751); got.Price.Cmp(want) != 0 {
		t.Errorf("price = %s, want %s", got.Price, want)
	}
}

// Mirror case: if supply RISES between passes, max() picks the later, larger
// figure — still the conservative direction.
func TestObserve_TakesLargerSupplyWhenSupplyRises(t *testing.T) {
	chain := &fakeChain{
		supplies: []string{"1000000", "2000000"}, // supply doubles mid-read
		decimals: 6,
	}
	bonds := &fakeSource{name: "hex_bonds", amount: "1.0"}
	cash := &fakeSource{name: "hex_cash", amount: "0"}

	got, err := observe(t, testConfig(t, chain, bonds, cash))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1.0 backing / 2.0 supply = 0.5, not 1.0.
	if want := big.NewInt(50_000_000); got.Price.Cmp(want) != 0 {
		t.Errorf("price = %s, want %s (the larger supply must be used)", got.Price, want)
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
			"cash source down", ok(),
			&fakeSource{name: "hex_bonds", amount: "1"},
			&fakeSource{name: "hex_cash", err: failure.BackingTimeout},
			failure.BackingTimeout,
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

	got, err := Observe(context.Background(), cfg,
		&fakeSigner{err: failure.SignUnavailable}, common.HexToHash("0xabc"))
	if !errors.Is(err, failure.SignUnavailable) {
		t.Fatalf("expected SignUnavailable, got %v", err)
	}
	if len(got.Data) != 0 {
		t.Error("a failed observation must carry no payload")
	}
}

// The deadline must bound the whole observation, not each call individually.
func TestObserve_RespectsDeadline(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1", delay: 2 * time.Second},
		&fakeSource{name: "hex_cash", amount: "0"},
	)
	cfg.Deadline = 150 * time.Millisecond

	start := time.Now()
	_, err := observe(t, cfg)
	if err == nil {
		t.Fatal("expected the observation to fail on its deadline")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("observation ran %v, past its 150ms deadline", elapsed)
	}
}

func TestObserve_WindowExceeded(t *testing.T) {
	cfg := testConfig(t,
		&fakeChain{supplies: []string{"1000000"}, decimals: 6},
		&fakeSource{name: "hex_bonds", amount: "1", delay: 300 * time.Millisecond},
		&fakeSource{name: "hex_cash", amount: "0"},
	)
	cfg.Window = 50 * time.Millisecond // narrower than the backing fetch

	if _, err := observe(t, cfg); !errors.Is(err, failure.WindowExceeded) {
		t.Errorf("expected WindowExceeded, got %v", err)
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
	got, err := Observe(context.Background(), cfg, signer, common.HexToHash("0xdeadbeef"))
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
	if r.Store != cfg.Store {
		t.Errorf("store = %s, want %s", r.Store, cfg.Store)
	}
	if r.ChainID.Cmp(cfg.ChainID) != 0 {
		t.Errorf("chainId = %s, want %s", r.ChainID, cfg.ChainID)
	}
	if r.InstructionID != common.HexToHash("0xdeadbeef") {
		t.Errorf("instructionId = %s", r.InstructionID.Hex())
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
		{"no cash source", func(c *Config) { c.Cash = nil }},
		{"nil chain id", func(c *Config) { c.ChainID = nil }},
		{"zero chain id", func(c *Config) { c.ChainID = big.NewInt(0) }},
		{"zero store", func(c *Config) { c.Store = common.Address{} }},
		{"negative decimals", func(c *Config) { c.Decimals = -1 }},
		{"zero deadline", func(c *Config) { c.Deadline = 0 }},
		{"zero window", func(c *Config) { c.Window = 0 }},
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
