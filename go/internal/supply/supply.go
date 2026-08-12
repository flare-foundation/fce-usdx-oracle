// Package supply reads USDX circulating supply across chains via RPC.
//
// Providers disagree about what "latest" resolves to, so the read is pinned to
// an explicit block: ask every provider for its head, take the Quorum-th
// HIGHEST, then query every provider at that exact number and require Quorum
// IDENTICAL results. Identical, not median: a read at a fixed block is
// deterministic, so disagreement means one provider is wrong or lying.
//
// # Why the Quorum-th highest head and not the minimum
//
// The minimum is the block every responding provider can serve, which is the
// right instinct and the wrong rule, because nothing was checking the heads
// themselves. One provider answering "what block are you at?" with a number
// from two weeks ago dragged the minimum back with it, and then every HONEST
// provider answered the supply query at that old block identically and
// correctly. Quorum passed. The enclave signed a stale supply figure.
//
// The sharp part is that the malicious provider never lies about the supply. It
// lies about which question to ask, and the honest majority corroborates the
// answer. Rewinding to when supply was lower understates supply, which
// OVERSTATES the price.
//
// The Quorum-th highest keeps the property the minimum was reaching for — at
// least Quorum providers can serve the pinned block — while making the choice
// obey the same rule as the values: Quorum-1 liars cannot move it. Providers
// below it simply fail their calls and do not contribute, which is what Quorum
// < len(Providers) exists to absorb.
//
// It cannot help a chain configured with a single provider, or with Quorum
// equal to the provider count. MaxBlockAge covers that case and every case the
// head-selection rule cannot: it bounds how old the pinned block may be in
// wall-clock terms, so an ancient block is refused however it was chosen. It is
// also the only check here that notices a chain that has simply stopped.
//
// Supply is the plain sum of totalSupply() over the pinned contracts — no
// exclusion list, no balanceOf. The set must match the basis on which Hex
// reports reserves. See docs/usdx-oracle-rationale.md.
package supply

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"sync"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ERC-20 selectors, hand-built: two four-byte constants are easier to confirm
// than a code-generated binding.
var (
	selectorTotalSupply = []byte{0x18, 0x16, 0x0d, 0xdd} // totalSupply()
	selectorDecimals    = []byte{0x31, 0x3c, 0xe5, 0x67} // decimals()
)

const maxDecimals = 36

// Caller is the subset of an Ethereum RPC client this package needs. Narrow on
// purpose: it is the test seam, and it shows there is no path here that signs.
type Caller interface {
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// Provider is one RPC endpoint for a chain.
type Provider struct {
	// Never a URL: /state is public and endpoint URLs may embed API keys.
	Name string

	Client Caller
}

type ChainConfig struct {
	Name  string
	Token common.Address

	Providers []Provider

	// How many providers(RPCs) must return identical results
	Quorum int

	// MaxBlockAge refuses a pinned block whose own header timestamp is older
	// than this. Zero disables the check.
	//
	// This is the backstop the agreement rules cannot provide. Every other
	// freshness bound in the system is wall-clock: checkWindow compares when the
	// HTTP calls happened, and the store compares the report timestamp against
	// block.timestamp. Reading two-week-old chain state one second ago passes
	// all of them. Only a comparison against the BLOCK's own timestamp catches
	// it, and unlike the head-selection rule it still works at Quorum 1.
	MaxBlockAge time.Duration
}

// Reading is one chain's supply, normalised out of the token's own decimals.
type Reading struct {
	Chain string

	// totalSupply scaled down by 10^decimals, so chains with differing decimals
	// are directly comparable.
	Amount *big.Rat

	// Block is the height every provider was queried at.
	Block uint64

	// BlockTime is that block's own header timestamp — when the chain state
	// being reported actually existed, as opposed to when it was fetched.
	//
	// Deliberately NOT folded into oracle.checkWindow: that window is 30s and
	// measures read skew between RetrievedAt stamps, while Ethereum's head is
	// routinely 12s or more behind wall-clock. Mixing the two would spend most
	// of the budget on ordinary block age and reject healthy observations.
	// MaxBlockAge bounds this quantity directly instead.
	BlockTime time.Time

	// Recorded so a silent change is visible in logs.
	Decimals uint8

	RetrievedAt time.Time

	// Providers maps each provider's name to its status for this read.
	//
	// Quorum lets a chain stay healthy while one provider is dead, dissenting or
	// lagging, so without this the failing provider is invisible right up until
	// enough of them fail at once.
	Providers map[string]string
}

// Validate returns an ordinary error: a bad config must stop startup, not
// surface through the feed.
func (c ChainConfig) Validate() error {
	if c.Name == "" {
		return errors.New("supply: chain name is required")
	}
	if c.Token == (common.Address{}) {
		return errors.New("supply: token address is required")
	}
	if len(c.Providers) == 0 {
		return errors.New("supply: at least one provider is required")
	}
	if c.Quorum < 1 || c.Quorum > len(c.Providers) {
		return errors.New("supply: quorum must be between 1 and the provider count")
	}
	for _, p := range c.Providers {
		if p.Name == "" {
			return errors.New("supply: provider name is required")
		}
		if p.Client == nil {
			return errors.New("supply: provider client is required")
		}
	}
	return nil
}

// Read returns the chain's supply, or a publishable failure. Either way the
// Reading carries per-provider standing.
func Read(ctx context.Context, cfg ChainConfig) (Reading, error) {
	// Everyone starts healthy and is demoted by the calls below. Sound because
	// agreedBlock contacts every provider before anything else can fail.
	health := make(map[string]string, len(cfg.Providers))
	for _, p := range cfg.Providers {
		health[p.Name] = config.HealthOK
	}
	failed := func(err error) (Reading, error) {
		return Reading{Chain: cfg.Name, Providers: health}, err
	}

	block, err := agreedBlock(ctx, cfg, health)
	if err != nil {
		return failed(err)
	}

	at := new(big.Int).SetUint64(block)

	blockTime, err := agreedBlockTime(ctx, cfg, at, health)
	if err != nil {
		return failed(err)
	}
	if cfg.MaxBlockAge > 0 && time.Since(blockTime) > cfg.MaxBlockAge {
		return failed(failure.SupplyBlockStale)
	}

	decimals, err := readDecimals(ctx, cfg, at, health)
	if err != nil {
		return failed(err)
	}

	raw, err := readTotalSupply(ctx, cfg, at, health)
	if err != nil {
		return failed(err)
	}

	// Exact: a big.Rat division by a power of ten loses nothing.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amount := new(big.Rat).SetFrac(raw, scale)

	return Reading{
		Chain:       cfg.Name,
		Amount:      amount,
		Block:       block,
		BlockTime:   blockTime,
		Decimals:    decimals,
		RetrievedAt: time.Now().UTC(),
		Providers:   health,
	}, nil
}

// agreedBlock returns the Quorum-th highest head across the providers — the
// highest block at least Quorum of them can serve.
//
// See the package comment for why this is not the minimum.
func agreedBlock(ctx context.Context, cfg ChainConfig, health map[string]string) (uint64, error) {
	type result struct {
		n   uint64
		err error
	}
	results := make([]result, len(cfg.Providers))

	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// nil is the latest block.
			h, err := p.Client.HeaderByNumber(ctx, nil)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			if h == nil || h.Number == nil {
				results[i] = result{err: errors.New("nil header")}
				return
			}
			results[i] = result{n: h.Number.Uint64()}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return 0, failure.SupplyTimeout
	}

	heads := make([]uint64, 0, len(results))
	for i, r := range results {
		if r.err != nil {
			health[cfg.Providers[i].Name] = config.HealthDown
			continue
		}
		heads = append(heads, r.n)
	}

	// Fewer than Quorum providers answered at all, so no block can reach Quorum
	// agreement later either.
	if len(heads) < cfg.Quorum {
		return 0, failure.SupplyUnavailable
	}

	// Descending, then the Quorum-th: exactly Quorum providers reported a head
	// at or above it, so at least that many hold the state.
	sort.Slice(heads, func(i, j int) bool { return heads[i] > heads[j] })
	pinned := heads[cfg.Quorum-1]

	// Block 0 is a freshly reset devnet, not a chain with USDX on it.
	if pinned == 0 {
		return 0, failure.SupplyUnavailable
	}

	// A provider below the pinned block cannot serve it and will fail its calls.
	// That is tolerated — it is what Quorum < len(Providers) is for — but it is
	// also the shape a provider takes when it tries to rewind the observation,
	// and nothing else would record it.
	for i, r := range results {
		if r.err == nil && r.n < pinned && health[cfg.Providers[i].Name] == config.HealthOK {
			health[cfg.Providers[i].Name] = config.HealthLagging
		}
	}

	return pinned, nil
}

// agreedBlockTime returns the pinned block's own header timestamp, agreed by
// Quorum providers.
//
// Agreement is required rather than trusting whichever provider proposed the
// block. Taking the timestamp from that one provider would hand a liar both
// halves of the lie: an ancient block number paired with a fresh timestamp,
// which passes MaxBlockAge and leaves the honest providers corroborating the
// stale supply at that block. A header at a fixed height is deterministic, so
// honest providers agree exactly and one liar is outvoted.
func agreedBlockTime(
	ctx context.Context, cfg ChainConfig, at *big.Int, health map[string]string,
) (time.Time, error) {
	stamps := make([]uint64, len(cfg.Providers))
	okAt := make([]bool, len(cfg.Providers))

	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := p.Client.HeaderByNumber(ctx, at)
			if err != nil || h == nil {
				return
			}
			stamps[i], okAt[i] = h.Time, true
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return time.Time{}, failure.SupplyTimeout
	}

	answers := make([][]byte, len(cfg.Providers))
	for i, ok := range okAt {
		if !ok {
			continue
		}
		answers[i] = new(big.Int).SetUint64(stamps[i]).Bytes()
	}

	best, agreed, answered := vote(cfg, answers, health)
	if answered < cfg.Quorum {
		return time.Time{}, failure.SupplyUnavailable
	}
	if agreed < cfg.Quorum {
		return time.Time{}, failure.SupplyNoQuorum
	}

	seconds := new(big.Int).SetBytes(best)
	if !seconds.IsInt64() || seconds.Sign() <= 0 {
		return time.Time{}, failure.SupplyMalformed
	}
	return time.Unix(seconds.Int64(), 0).UTC(), nil
}

func readTotalSupply(
	ctx context.Context, cfg ChainConfig, at *big.Int, health map[string]string,
) (*big.Int, error) {
	best, agreed, answered := vote(cfg, callAll(ctx, cfg, selectorTotalSupply, at), health)

	if answered < cfg.Quorum {
		if answered == 0 {
			return nil, failure.SupplyUnavailable
		}
		return nil, failure.SupplyNoQuorum
	}
	if agreed < cfg.Quorum {
		return nil, failure.SupplyNoQuorum
	}

	value := new(big.Int).SetBytes(best)
	if value.Sign() < 0 {
		return nil, failure.SupplyImplausible
	}
	return value, nil
}

// readDecimals reads decimals() at the same block. Read every time rather than
// hard-coded: USDX is 6 on Flare and Songbird but 18 on Ethereum, and assuming
// a value would misprice a chain by 10^12.
func readDecimals(
	ctx context.Context, cfg ChainConfig, at *big.Int, health map[string]string,
) (uint8, error) {
	best, agreed, answered := vote(cfg, callAll(ctx, cfg, selectorDecimals, at), health)
	if answered < cfg.Quorum {
		return 0, failure.SupplyUnavailable
	}
	if agreed < cfg.Quorum {
		return 0, failure.SupplyNoQuorum
	}

	d := new(big.Int).SetBytes(best)
	if !d.IsUint64() || d.Uint64() > maxDecimals {
		return 0, failure.SupplyMalformed
	}
	return uint8(d.Uint64()), nil
}

// callAll performs the same eth_call against every provider concurrently. The
// result is index-aligned with cfg.Providers; nil means that provider gave no
// usable answer. Index-aligned rather than compacted so the caller can still
// say WHICH provider failed.
func callAll(ctx context.Context, cfg ChainConfig, selector []byte, at *big.Int) [][]byte {
	answers := make([][]byte, len(cfg.Providers))

	call := ethereum.CallMsg{To: &cfg.Token, Data: selector}

	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Always an explicit block number: never a tag, never nil.
			data, err := p.Client.CallContract(ctx, call, at)
			// A single ABI word. Anything else is a wrong address, a proxy
			// without an implementation, or a provider missing that block.
			if err != nil || len(data) != 32 {
				return
			}
			answers[i] = data
		}()
	}
	wg.Wait()

	return answers
}

// vote returns the most common answer, how many providers gave it, and how many
// answered at all — and records each provider's standing in health. Demote
// only: a provider already marked down or lagging by an earlier call must not be
// promoted back to merely disagreeing by a later one.
func vote(cfg ChainConfig, answers [][]byte, health map[string]string) (best []byte, agreed, answered int) {
	for _, a := range answers {
		if a == nil {
			continue
		}
		answered++
		count := 0
		for _, b := range answers {
			if b != nil && string(a) == string(b) {
				count++
			}
		}
		if count > agreed {
			best, agreed = a, count
		}
	}

	for i, a := range answers {
		name := cfg.Providers[i].Name
		switch {
		case a == nil:
			// A lagging provider is EXPECTED to fail here — it does not hold the
			// pinned block. Keeping "lagging" names the actual cause; "down"
			// would send someone looking for an outage that is not there.
			if health[name] != config.HealthLagging {
				health[name] = config.HealthDown
			}
		case string(a) != string(best) && health[name] == config.HealthOK:
			health[name] = config.HealthDisagree
		}
	}
	return best, agreed, answered
}

// Total sums per-chain readings. A plain sum is correct only because USDX is a
// native burn-and-mint LayerZero OFT on every chain read; a lock-and-mint
// adapter anywhere would make this double-count and overstate the price.
func Total(readings []Reading) *big.Rat {
	sum := new(big.Rat)
	for _, r := range readings {
		if r.Amount != nil {
			sum.Add(sum, r.Amount)
		}
	}
	return sum
}
