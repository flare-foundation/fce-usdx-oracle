package supply

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var testToken = common.HexToAddress("0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B")

// fakeClient is a scriptable Caller. It records the block every call was made
// at, which is how the tests assert the never-a-tag rule.
type fakeClient struct {
	head     uint64
	headErr  error
	supply   *big.Int
	decimals uint8
	callErr  error

	// badLen returns a result that is not a single ABI word.
	badLen bool

	// blockTime is the header timestamp served for the PINNED block. Zero means
	// "now", which is what a healthy chain looks like; set it to backdate a
	// block and exercise MaxBlockAge.
	blockTime time.Time

	// blockTimeErr fails only the pinned-block header read, leaving the head
	// read healthy — how a provider that cannot serve the pinned block behaves.
	blockTimeErr error

	mu           sync.Mutex
	calledBlocks []*big.Int
}

// HeaderByNumber serves the head when number is nil and the pinned block
// otherwise. Both carry a timestamp: a real chain has no blocks without one, and
// agreedBlockTime rejects a zero stamp as malformed.
func (c *fakeClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	if number == nil {
		if c.headErr != nil {
			return nil, c.headErr
		}
		return &types.Header{
			Number: new(big.Int).SetUint64(c.head),
			Time:   uint64(c.stampFor(c.head).Unix()),
		}, nil
	}

	if c.blockTimeErr != nil {
		return nil, c.blockTimeErr
	}
	// A provider whose head is below the requested block does not hold it.
	if number.IsUint64() && number.Uint64() > c.head {
		return nil, errors.New("unknown block")
	}
	return &types.Header{
		Number: new(big.Int).Set(number),
		Time:   uint64(c.stampFor(number.Uint64()).Unix()),
	}, nil
}

func (c *fakeClient) stampFor(uint64) time.Time {
	if c.blockTime.IsZero() {
		return time.Now()
	}
	return c.blockTime
}

func (c *fakeClient) CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error) {
	c.mu.Lock()
	c.calledBlocks = append(c.calledBlocks, blockNumber)
	c.mu.Unlock()

	if c.callErr != nil {
		return nil, c.callErr
	}
	if c.badLen {
		return []byte{0x01}, nil
	}
	switch {
	case len(call.Data) == 4 && call.Data[0] == selectorDecimals[0]:
		return common.LeftPadBytes(big.NewInt(int64(c.decimals)).Bytes(), 32), nil
	default:
		return common.LeftPadBytes(c.supply.Bytes(), 32), nil
	}
}

// client builds a healthy provider reporting the given head and supply.
func client(head uint64, supply string, decimals uint8) *fakeClient {
	s, _ := new(big.Int).SetString(supply, 10)
	return &fakeClient{head: head, supply: s, decimals: decimals}
}

func cfg(t *testing.T, quorum int, clients ...*fakeClient) ChainConfig {
	t.Helper()
	providers := make([]Provider, len(clients))
	for i, c := range clients {
		providers[i] = Provider{Name: "rpc_test_" + string(rune('a'+i)), Client: c}
	}
	return ChainConfig{
		Name: "testchain", Token: testToken, Providers: providers,
		Quorum: quorum,
	}
}

func TestRead_Agreement(t *testing.T) {
	c := cfg(t, 2,
		client(1000, "20620459685024", 6),
		client(1000, "20620459685024", 6),
		client(1000, "20620459685024", 6),
	)

	got, err := Read(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 20620459685024 at 6 decimals = 20,620,459.685024 — the real post-burn
	// Flare supply from spec §3.0.
	if want := "20620459.685024"; got.Amount.FloatString(6) != want {
		t.Errorf("amount = %s, want %s", got.Amount.FloatString(6), want)
	}
	if got.Block != 1000 {
		t.Errorf("block = %d, want the agreed head 1000", got.Block)
	}
}

// Ethereum's 18 decimals against Flare's 6 must normalise to the same scale, or
// one chain is misweighted by a factor of 10^12.
func TestRead_NormalisesDifferingDecimals(t *testing.T) {
	six := cfg(t, 1, client(100, "1000000", 6))                   // 1.0 at 6dp
	eighteen := cfg(t, 1, client(100, "1000000000000000000", 18)) // 1.0 at 18dp

	a, err := Read(context.Background(), six)
	if err != nil {
		t.Fatalf("6dp: %v", err)
	}
	b, err := Read(context.Background(), eighteen)
	if err != nil {
		t.Fatalf("18dp: %v", err)
	}
	if a.Amount.Cmp(b.Amount) != 0 {
		t.Errorf("6dp gave %s but 18dp gave %s; both are 1.0 USDX",
			a.Amount.FloatString(18), b.Amount.FloatString(18))
	}
}

// The core safety property: every eth_call names an explicit block number.
// Passing nil would mean "latest", which providers disagree about.
func TestRead_NeverQueriesATag(t *testing.T) {
	a, b := client(1000, "100", 6), client(1000, "100", 6)
	if _, err := Read(context.Background(), cfg(t, 2, a, b)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, c := range []*fakeClient{a, b} {
		if len(c.calledBlocks) == 0 {
			t.Fatal("no calls recorded")
		}
		for _, blk := range c.calledBlocks {
			if blk == nil {
				t.Error("a call was made with a nil block, i.e. the latest tag")
			} else if blk.Uint64() != 1000 {
				t.Errorf("call at block %s, want the agreed 1000", blk)
			}
		}
	}
}

// The pinned block is the Quorum-th highest head: the highest block at least
// Quorum providers can serve. A provider behind it is left behind rather than
// followed — which is the whole change from the old minimum rule.
func TestRead_PinsTheQuorumThHighestHead(t *testing.T) {
	a := client(1000, "100", 6)
	b := client(1000, "100", 6)
	lagging := client(950, "100", 6)

	got, err := Read(context.Background(), cfg(t, 2, a, b, lagging))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Block != 1000 {
		t.Errorf("block = %d, want 1000 — the highest block quorum 2 can serve", got.Block)
	}
	if got.Providers["rpc_test_c"] != "lagging" {
		t.Errorf("the lagging provider is %q, want %q — being ignored must still leave a trace",
			got.Providers["rpc_test_c"], "lagging")
	}
}

// The attack the old minimum rule allowed, and the reason for this whole change.
//
// One provider claims a head from two weeks ago. Under min(heads) everyone was
// then queried at that block, the HONEST providers answered identically and
// correctly, quorum passed, and the enclave signed a stale supply figure —
// understating supply, which overstates the price. The liar never lies about
// the supply; it lies about which question to ask.
func TestRead_OneProviderCannotRewindTheObservation(t *testing.T) {
	honestA := client(1_000_000, "5000000", 6)
	honestB := client(1_000_000, "5000000", 6)

	// Two weeks back, and it reports the supply as it was then.
	rewinder := client(900_000, "1000000", 6)

	got, err := Read(context.Background(), cfg(t, 2, honestA, honestB, rewinder))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Block != 1_000_000 {
		t.Fatalf("block = %d, want 1000000 — one liar must not move the pinned block", got.Block)
	}
	if want := "5.000000"; got.Amount.FloatString(6) != want {
		t.Errorf("amount = %s, want %s — the current supply, not the rewound one",
			got.Amount.FloatString(6), want)
	}
}

// At quorum == provider count the order statistic degenerates to the minimum,
// so head selection cannot help. Stated as a test because it is the case
// MaxBlockAge exists to cover, and someone will otherwise assume the new rule
// protects every configuration.
func TestRead_UnanimityStillFollowsTheLowestHead(t *testing.T) {
	got, err := Read(context.Background(), cfg(t, 2,
		client(1000, "100", 6),
		client(950, "100", 6),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Block != 950 {
		t.Errorf("block = %d, want 950: with quorum == provider count there is no slack", got.Block)
	}
}

// MaxBlockAge is the backstop that works at any quorum, including one provider.
// Every other freshness check in the system is wall-clock, so reading ancient
// state one second ago passes all of them.
func TestRead_RejectsAStaleBlock(t *testing.T) {
	old := client(1000, "100", 6)
	old.blockTime = time.Now().Add(-2 * time.Hour)

	c := cfg(t, 1, old)
	c.MaxBlockAge = 5 * time.Minute

	_, err := Read(context.Background(), c)
	if !errors.Is(err, failure.SupplyBlockStale) {
		t.Fatalf("expected SupplyBlockStale, got %v", err)
	}
}

func TestRead_AcceptsAFreshBlock(t *testing.T) {
	fresh := client(1000, "100", 6)
	fresh.blockTime = time.Now().Add(-30 * time.Second)

	c := cfg(t, 1, fresh)
	c.MaxBlockAge = 5 * time.Minute

	got, err := Read(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BlockTime.IsZero() {
		t.Error("BlockTime was not recorded")
	}
}

// Zero disables the check, so an existing deployment that has not set a value
// behaves exactly as before rather than failing closed on every read.
func TestRead_ZeroMaxBlockAgeDisablesTheCheck(t *testing.T) {
	old := client(1000, "100", 6)
	old.blockTime = time.Now().Add(-30 * 24 * time.Hour)

	if _, err := Read(context.Background(), cfg(t, 1, old)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The block timestamp is voted on, not taken from whichever provider proposed
// the block. Otherwise a liar supplies both halves — an ancient block number
// with a fresh timestamp — and MaxBlockAge is defeated while the honest
// providers corroborate the stale supply at that block.
func TestRead_BlockTimeMustReachQuorum(t *testing.T) {
	honestA := client(1000, "100", 6)
	honestA.blockTime = time.Now().Add(-90 * time.Minute)
	honestB := client(1000, "100", 6)
	honestB.blockTime = time.Now().Add(-90 * time.Minute)

	// Same block, but claims it is fresh.
	liar := client(1000, "100", 6)
	liar.blockTime = time.Now()

	c := cfg(t, 2, honestA, honestB, liar)
	c.MaxBlockAge = 5 * time.Minute

	_, err := Read(context.Background(), c)
	if !errors.Is(err, failure.SupplyBlockStale) {
		t.Fatalf("expected SupplyBlockStale — the honest majority's timestamp must win, got %v", err)
	}
}

// A provider that answered but lost the vote is visible, so it can be replaced
// before enough of them drift to break quorum.
func TestRead_DisagreeingProviderIsRecorded(t *testing.T) {
	got, err := Read(context.Background(), cfg(t, 2,
		client(1000, "1000000", 6),
		client(1000, "1000000", 6),
		client(1000, "9999999", 6),
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Providers["rpc_test_c"] != "disagree" {
		t.Errorf("outvoted provider is %q, want %q", got.Providers["rpc_test_c"], "disagree")
	}
	if got.Providers["rpc_test_a"] != "ok" {
		t.Errorf("agreeing provider is %q, want %q", got.Providers["rpc_test_a"], "ok")
	}
}

// A failed read still carries per-provider standing: on failure that map is the
// only thing that says WHICH provider broke.
func TestRead_FailureStillReportsProviders(t *testing.T) {
	down := client(1000, "100", 6)
	down.headErr = errors.New("connection refused")

	got, _ := Read(context.Background(), cfg(t, 2, down, client(1000, "100", 6)))
	if got.Providers["rpc_test_a"] != "down" {
		t.Errorf("failed provider is %q, want %q", got.Providers["rpc_test_a"], "down")
	}
}

// Disagreement at a fixed block is deterministic evidence of a fault. It must
// abort, never be averaged or medianed into a plausible-looking number.
func TestRead_DisagreementFailsClosed(t *testing.T) {
	got, err := Read(context.Background(), cfg(t, 2,
		client(1000, "100", 6),
		client(1000, "200", 6),
		client(1000, "300", 6),
	))
	if !errors.Is(err, failure.SupplyNoQuorum) {
		t.Fatalf("expected SupplyNoQuorum, got %v (value %v)", err, got.Amount)
	}
}

// Two of three agreeing is enough at quorum 2; the outlier is outvoted.
func TestRead_OutlierOutvoted(t *testing.T) {
	got, err := Read(context.Background(), cfg(t, 2,
		client(1000, "1000000", 6),
		client(1000, "1000000", 6),
		client(1000, "9999999", 6), // lying or broken
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "1.000000"; got.Amount.FloatString(6) != want {
		t.Errorf("amount = %s, want %s", got.Amount.FloatString(6), want)
	}
}

// With quorum 3 the same outlier must take the whole read down: the operator
// asked for unanimity.
func TestRead_UnanimityRejectsOutlier(t *testing.T) {
	_, err := Read(context.Background(), cfg(t, 3,
		client(1000, "1000000", 6),
		client(1000, "1000000", 6),
		client(1000, "9999999", 6),
	))
	if !errors.Is(err, failure.SupplyNoQuorum) {
		t.Errorf("expected SupplyNoQuorum, got %v", err)
	}
}

func TestRead_ProviderFailures(t *testing.T) {
	down := client(1000, "100", 6)
	down.callErr = errors.New("connection refused")

	t.Run("one down still meets quorum 2", func(t *testing.T) {
		if _, err := Read(context.Background(), cfg(t, 2,
			client(1000, "100", 6), client(1000, "100", 6), down,
		)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("two down misses quorum 2", func(t *testing.T) {
		down2 := client(1000, "100", 6)
		down2.callErr = errors.New("connection refused")
		_, err := Read(context.Background(), cfg(t, 2, client(1000, "100", 6), down, down2))
		if err == nil {
			t.Error("expected a failure when only one provider answers")
		}
	})

	t.Run("all down", func(t *testing.T) {
		d1, d2 := client(1000, "1", 6), client(1000, "1", 6)
		d1.callErr = errors.New("refused")
		d2.callErr = errors.New("refused")
		_, err := Read(context.Background(), cfg(t, 2, d1, d2))
		if !errors.Is(err, failure.SupplyUnavailable) {
			t.Errorf("expected SupplyUnavailable, got %v", err)
		}
	})
}

func TestRead_HeadResolutionFailure(t *testing.T) {
	bad := client(1000, "100", 6)
	bad.headErr = errors.New("no such method")

	_, err := Read(context.Background(), cfg(t, 2, bad, client(1000, "100", 6)))
	if !errors.Is(err, failure.SupplyUnavailable) {
		t.Errorf("expected SupplyUnavailable, got %v", err)
	}
}

// A short result is a wrong address or a provider answering for a block it does
// not have. It must not be zero-extended into a supply of 1.
func TestRead_ShortResultRejected(t *testing.T) {
	bad := client(1000, "100", 6)
	bad.badLen = true

	_, err := Read(context.Background(), cfg(t, 2, bad, client(1000, "100", 6)))
	if err == nil {
		t.Error("expected a failure for a malformed call result")
	}
}

func TestRead_ImplausibleDecimalsRejected(t *testing.T) {
	_, err := Read(context.Background(), cfg(t, 1, client(1000, "100", 200)))
	if !errors.Is(err, failure.SupplyMalformed) {
		t.Errorf("expected SupplyMalformed, got %v", err)
	}
}

// Head 0 is a freshly reset devnet, not a chain carrying USDX.
func TestRead_ZeroHeadRejected(t *testing.T) {
	c := cfg(t, 1, client(0, "100", 6))
	if _, err := Read(context.Background(), c); !errors.Is(err, failure.SupplyUnavailable) {
		t.Errorf("expected SupplyUnavailable, got %v", err)
	}
}

func TestRead_ErrorsArePublishable(t *testing.T) {
	_, err := Read(context.Background(), cfg(t, 2,
		client(1000, "100", 6), client(1000, "200", 6)))
	if err == nil {
		t.Fatal("expected an error")
	}
	var f failure.Failure
	if !errors.As(err, &f) {
		t.Errorf("error %v is not a failure.Failure and could leak an endpoint URL", err)
	}
}

func TestTotal(t *testing.T) {
	r := func(s string) Reading {
		v, _ := new(big.Rat).SetString(s)
		return Reading{Amount: v}
	}
	// The three live chains, 31 Jul 2026.
	got := Total([]Reading{
		r("20620459.685024"), // Flare
		r("3672084.839"),     // Ethereum
		r("1160000.0"),       // Songbird
	})
	if want := "25452544.524024"; got.FloatString(6) != want {
		t.Errorf("Total = %s, want %s", got.FloatString(6), want)
	}
}

func TestTotal_IgnoresNilAmounts(t *testing.T) {
	got := Total([]Reading{{Amount: nil}, {Amount: big.NewRat(1, 1)}})
	if got.FloatString(0) != "1" {
		t.Errorf("Total = %s, want 1", got.FloatString(0))
	}
}

func TestValidate(t *testing.T) {
	good := cfg(t, 2, client(1, "1", 6), client(1, "1", 6))
	if err := good.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*ChainConfig)
	}{
		{"no name", func(c *ChainConfig) { c.Name = "" }},
		{"no token", func(c *ChainConfig) { c.Token = common.Address{} }},
		{"no providers", func(c *ChainConfig) { c.Providers = nil }},
		{"quorum zero", func(c *ChainConfig) { c.Quorum = 0 }},
		{"quorum above provider count", func(c *ChainConfig) { c.Quorum = 99 }},
		{"provider without name", func(c *ChainConfig) { c.Providers[0].Name = "" }},
		{"provider without client", func(c *ChainConfig) { c.Providers[0].Client = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg(t, 2, client(1, "1", 6), client(1, "1", 6))
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Error("expected a config error")
			}
		})
	}
}

// Songbird has exactly one working public RPC, so quorum 1 must be expressible.
// It is a documented acceptance of single-provider risk, not an oversight.
func TestValidate_SingleProviderQuorumIsAllowed(t *testing.T) {
	c := cfg(t, 1, client(1, "1", 6))
	if err := c.Validate(); err != nil {
		t.Errorf("quorum 1 must be permitted for Songbird: %v", err)
	}
}
