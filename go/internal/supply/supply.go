// Package supply reads USDX circulating supply across chains via RPC.
//
// # The quorum procedure
//
// Reading totalSupply() from a single provider makes that provider able to
// dictate the published price. Reading it from three at "latest" does not fix
// it: providers disagree about what "latest" is, so three honest providers
// routinely return three different numbers and the disagreement is
// indistinguishable from an attack.
//
// Measured 31 Jul 2026, the same is true of "finalized": every live Ethereum
// endpoint supports it and sits ~83 blocks behind head, but 1rpc.io resolved it
// 32 blocks earlier than the others. A tag is not a shared truth.
//
// So the read is pinned to an explicit block, uniformly across chains:
//
//  1. ask every provider to resolve its finality reference to a block NUMBER
//  2. take the MINIMUM across providers — the block all of them have
//  3. subtract the per-chain margin
//  4. query every provider at that exact number
//  5. require Quorum identical results, else fail the whole observation
//
// Identical, not median. A read at a fixed block is deterministic: two honest
// providers cannot disagree, so disagreement means one is wrong or lying and
// there is no safe way to average that.
//
// # Constraints worth knowing
//
// The margin must stay inside the shallowest provider's state window or that
// provider drops out of quorum on every round. Measured retention ranges from
// latest-only to full archive (publicnode/1rpc ~64 blocks, blockrazor ~128,
// drpc/mevblocker ≥100k). eth.merkle.io cannot serve explicit-block queries at
// all and is unusable here despite being reachable.
//
// Only eth_call and block lookups are used — never eth_getLogs. Worth recording
// because Flare's public RPC caps log ranges at 30 blocks, which looks like a
// blocker until you notice this package does not need it.
//
// # No exclusions
//
// Supply is the plain sum of totalSupply() over the configured contracts. There
// is no exclusion list and no balanceOf call: the contract list IS the policy,
// and it is pinned in configuration covered by the code hash. The set must
// match the basis on which Hex reports reserves, or the ratio compares two
// different populations — see the note on the pre-migration Ethereum contract
// in docs/usdx-oracle-spec.md §3.1.
package supply

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ERC-20 selectors. Hand-built rather than generated: two four-byte constants
// are easier for a reviewer to confirm than a code-generated binding.
var (
	selectorTotalSupply = []byte{0x18, 0x16, 0x0d, 0xdd} // totalSupply()
	selectorDecimals    = []byte{0x31, 0x3c, 0xe5, 0x67} // decimals()
)

// maxDecimals bounds a decimals() response. USDX is 6 on Flare/Songbird and 18
// on Ethereum; anything beyond 36 is a broken or hostile contract.
const maxDecimals = 36

// Caller is the subset of an Ethereum RPC client this package needs.
//
// Narrow on purpose: it is the seam the tests substitute, and it documents that
// this package can only read — there is no path here that signs or sends.
type Caller interface {
	// HeaderByNumber resolves a block. A nil number means the client's latest;
	// negative rpc.BlockNumber sentinels select tags such as finalized.
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)

	// CallContract performs eth_call at an explicit block.
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

// Provider is one RPC endpoint for a chain.
type Provider struct {
	// Name identifies the provider in GET /state health, e.g. "rpc_flare_1".
	// Never a URL: /state is public and endpoint URLs may embed API keys.
	Name string

	Client Caller
}

// ChainConfig describes one chain's supply read.
type ChainConfig struct {
	// Name identifies the chain in health reporting, e.g. "flare".
	Name string

	// Token is the USDX contract on this chain.
	Token common.Address

	// Providers are the independent RPC endpoints to query.
	Providers []Provider

	// Quorum is how many providers must return identical results. Must be at
	// least 1 and at most len(Providers).
	//
	// Songbird is the awkward case: exactly one working public RPC exists, so a
	// 3-provider rule cannot be met there for free. Setting Quorum to 1 is a
	// deliberate acceptance of single-provider risk for that chain's share of
	// supply, not an oversight — see spec §2.8.
	Quorum int

	// FinalityTag is the reference each provider resolves before the minimum is
	// taken: "finalized" on Ethereum, "latest" on Flare and Songbird.
	FinalityTag BlockTag

	// FinalityMargin is subtracted from the agreed block. Per-chain, not
	// global: measured block times are Flare 1.11s, Songbird 1.07s, Ethereum
	// 12.01s, so 60s of margin is ~54 blocks on Flare but ~5 on Ethereum.
	FinalityMargin uint64
}

// BlockTag is a finality reference.
type BlockTag string

const (
	TagLatest    BlockTag = "latest"
	TagFinalized BlockTag = "finalized"
)

// blockNumArg converts a tag to the sentinel ethclient expects.
func (t BlockTag) blockNumArg() *big.Int {
	switch t {
	case TagFinalized:
		return big.NewInt(-3) // rpc.FinalizedBlockNumber
	default:
		return nil // latest
	}
}

// Reading is one chain's supply, normalised out of the token's own decimals.
type Reading struct {
	Chain string

	// Amount is the supply as a USD-denominated quantity: totalSupply scaled
	// down by 10^decimals. 1 USDX = 1 unit, so this is directly comparable
	// across chains despite their differing decimals.
	Amount *big.Rat

	// Block is the height every provider was queried at.
	Block uint64

	// Decimals is what decimals() returned, recorded so a silent change is
	// visible in logs.
	Decimals uint8

	RetrievedAt time.Time
}

// Validate checks a chain configuration. Returns an ordinary error: a bad
// config must stop startup, not surface through the feed.
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

// Read returns the chain's supply, or a publishable failure.
func Read(ctx context.Context, cfg ChainConfig) (Reading, error) {
	block, err := agreedBlock(ctx, cfg)
	if err != nil {
		return Reading{}, err
	}

	at := new(big.Int).SetUint64(block)

	decimals, err := readDecimals(ctx, cfg, at)
	if err != nil {
		return Reading{}, err
	}

	raw, err := readTotalSupply(ctx, cfg, at)
	if err != nil {
		return Reading{}, err
	}

	// Scale out of the token's own decimals so chains with different decimals
	// are summable. Exact: a big.Rat division by a power of ten loses nothing.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amount := new(big.Rat).SetFrac(raw, scale)

	return Reading{
		Chain:       cfg.Name,
		Amount:      amount,
		Block:       block,
		Decimals:    decimals,
		RetrievedAt: time.Now().UTC(),
	}, nil
}

// agreedBlock resolves every provider's finality reference and returns the
// minimum, less the margin.
//
// The minimum, not the maximum or the median: it is the only choice guaranteed
// to name a block that every provider can actually serve. A higher block would
// make the laggard fail its query every round, silently reducing the effective
// provider count to those that happen to be ahead.
func agreedBlock(ctx context.Context, cfg ChainConfig) (uint64, error) {
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
			h, err := p.Client.HeaderByNumber(ctx, cfg.FinalityTag.blockNumArg())
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

	var min uint64
	var ok int
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if ok == 0 || r.n < min {
			min = r.n
		}
		ok++
	}

	// Every provider that will be asked for the supply must also have
	// contributed a head, or the minimum is not a block they all have.
	if ok < cfg.Quorum {
		return 0, failure.SupplyUnavailable
	}

	if min < cfg.FinalityMargin {
		// A chain shallower than the margin means a misconfiguration or a
		// freshly reset devnet. Either way there is no safe block to read.
		return 0, failure.SupplyUnavailable
	}
	return min - cfg.FinalityMargin, nil
}

// readTotalSupply queries every provider at an explicit block and requires
// Quorum identical answers.
func readTotalSupply(ctx context.Context, cfg ChainConfig, at *big.Int) (*big.Int, error) {
	results, failures := callAll(ctx, cfg, selectorTotalSupply, at)

	if len(results) < cfg.Quorum {
		if failures > 0 && len(results) == 0 {
			return nil, failure.SupplyUnavailable
		}
		return nil, failure.SupplyNoQuorum
	}

	// Tally identical values. At a fixed block these are deterministic, so any
	// spread is a fault rather than noise to be averaged away.
	best, count := agreement(results)
	if count < cfg.Quorum {
		return nil, failure.SupplyNoQuorum
	}

	value := new(big.Int).SetBytes(best)
	if value.Sign() < 0 {
		return nil, failure.SupplyImplausible
	}
	return value, nil
}

// readDecimals reads decimals() at the same block, requiring the same quorum.
//
// Read every time rather than hard-coded: USDX is 6 on Flare and Songbird but
// 18 on Ethereum, which is normal for a LayerZero OFT and a live footgun.
// Assuming a value would silently misprice a chain by 10^12.
func readDecimals(ctx context.Context, cfg ChainConfig, at *big.Int) (uint8, error) {
	results, _ := callAll(ctx, cfg, selectorDecimals, at)
	if len(results) < cfg.Quorum {
		return 0, failure.SupplyUnavailable
	}

	best, count := agreement(results)
	if count < cfg.Quorum {
		return 0, failure.SupplyNoQuorum
	}

	d := new(big.Int).SetBytes(best)
	if !d.IsUint64() || d.Uint64() > maxDecimals {
		return 0, failure.SupplyMalformed
	}
	return uint8(d.Uint64()), nil
}

// callAll performs the same eth_call against every provider concurrently and
// returns the successful 32-byte results plus a count of failures.
func callAll(ctx context.Context, cfg ChainConfig, selector []byte, at *big.Int) ([][]byte, int) {
	type result struct {
		data []byte
		err  error
	}
	out := make([]result, len(cfg.Providers))

	call := ethereum.CallMsg{To: &cfg.Token, Data: selector}

	var wg sync.WaitGroup
	for i, p := range cfg.Providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The block number is passed explicitly on every call. This is the
			// whole point of the procedure: never a tag, never nil.
			data, err := p.Client.CallContract(ctx, call, at)
			out[i] = result{data: data, err: err}
		}()
	}
	wg.Wait()

	var results [][]byte
	failures := 0
	for _, r := range out {
		// A single ABI word. Anything else is a wrong address, a proxy without
		// an implementation, or a provider that silently returned empty for a
		// block it does not have.
		if r.err != nil || len(r.data) != 32 {
			failures++
			continue
		}
		results = append(results, r.data)
	}
	return results, failures
}

// agreement returns the most common value and how many providers returned it.
func agreement(results [][]byte) ([]byte, int) {
	best, bestCount := []byte(nil), 0
	for i, a := range results {
		count := 0
		for _, b := range results {
			if string(a) == string(b) {
				count++
			}
		}
		if count > bestCount {
			best, bestCount = results[i], count
		}
	}
	return best, bestCount
}

// Total sums per-chain readings.
//
// A plain sum is correct only because USDX is a native burn-and-mint LayerZero
// OFT on every chain read: token() returns the contract's own address on both
// Flare and current Ethereum, which is the native pattern rather than a
// lock-and-mint adapter. Bridging therefore burns on the source and mints on
// the destination, and no balance is counted twice. If a lock-and-mint adapter
// exists on any chain, this sum double-counts and the price is overstated.
func Total(readings []Reading) *big.Rat {
	sum := new(big.Rat)
	for _, r := range readings {
		if r.Amount != nil {
			sum.Add(sum, r.Amount)
		}
	}
	return sum
}
