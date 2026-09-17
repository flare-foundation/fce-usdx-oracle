// Package supply reads USDX circulating supply across chains via RPC.
//
// To resolve different "latest" heads from providers, we pin to an explicit block:
//  1. Ask each provider for its head.
//  2. Take the Quorum-th highest claimed head, then step down through the distinct
//     heads below it until Quorum providers actually serve one.
//  3. Query all providers at that block and require Quorum identical results for a
//     uniquely most-common answer (not median).
//
// Step 2 steps down because a head is a claim, not proof. One provider naming a head
// it does not hold shifts the Quorum-th highest onto a block only a single honest
// provider can serve, and the vote there is a tie — so a single provider would
// otherwise deny the whole round.
//
// Taking the Quorum-th highest first avoids stale data from lagging providers, and
// means a provider claiming a LOW head is simply ignored rather than able to drag
// the pin backwards. MaxBlockAge covers cases head selection alone cannot.
//
// Every call is bounded by ProviderTimeout, so a provider that accepts the
// connection and never answers is demoted like any other unreachable one instead
// of holding the round until the observation deadline.
//
// Supply is the sum of totalSupply() over the pinned contracts, matching Hex’s reserves basis.
//
// See rationale §6 for details and findings §24 for the relevant incident.
package supply

import (
	"context"
	"errors"
	"maps"
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
	// Never a URL: the readiness report is public and endpoint URLs may embed API keys.
	Name string

	Client Caller
}

// ChainSource is one chain, opened and ready to read: the pinned facts it was
// built from, the agreement policy, and the RPC providers to ask. Built per
// observation from config.Chain plus the published endpoints — it holds live
// clients, so it is not configuration and outlives nothing.
type ChainSource struct {
	Name  string
	Token common.Address

	Providers []Provider

	// How many providers(RPCs) must return identical results
	Quorum int

	// MaxBlockAge refuses a pinned block whose own header timestamp is older than
	// this; zero disables it. The only freshness bound that is not wall-clock, and
	// the only one that still works at Quorum 1. Rationale §6.
	MaxBlockAge time.Duration

	// ProviderTimeout bounds a single provider's answer within one round. Quorum
	// tolerates a provider that fails; this is what makes it tolerate one that
	// merely never replies, which is otherwise indistinguishable from a slow
	// chain and costs the whole shared deadline.
	ProviderTimeout time.Duration
}

// Reading is one chain's supply, normalised out of the token's own decimals.
type Reading struct {
	Chain string

	// totalSupply scaled down by 10^decimals, so chains with differing decimals
	// are directly comparable.
	Amount *big.Rat

	// Block is the height every provider was queried at.
	Block uint64

	// BlockTime is that block's own header timestamp — when the state existed, not
	// when it was fetched. Not folded into observation.checkWindow: Ethereum's head runs
	// 12s+ behind, which would eat that window. MaxBlockAge bounds it instead.
	BlockTime time.Time

	// Recorded so a silent change is visible in logs.
	Decimals uint8

	RetrievedAt time.Time

	// Providers maps each provider's name to its status for this read. Quorum hides
	// a dead or dissenting provider until enough of them fail at once; this shows it.
	Providers map[string]string
}

// Validate returns an ordinary error: a bad config must stop startup, not
// surface through the feed.
func (c ChainSource) Validate() error {
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
	if c.ProviderTimeout <= 0 {
		return errors.New("supply: provider timeout must be positive")
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
func Read(ctx context.Context, ch ChainSource) (Reading, error) {
	// Everyone starts healthy and is demoted by the calls below. Sound because
	// providerHeads contacts every provider before anything else can fail.
	health := make(map[string]string, len(ch.Providers))
	for _, p := range ch.Providers {
		health[p.Name] = config.HealthOK
	}
	failed := func(err error) (Reading, error) {
		return Reading{Chain: ch.Name, Providers: health}, err
	}

	claims, err := providerHeads(ctx, ch, health)
	if err != nil {
		return failed(err)
	}

	candidateBlocks := pinCandidateBlocks(claims, ch.Quorum)
	if len(candidateBlocks) == 0 {
		return failed(failure.SupplyUnavailable)
	}

	var (
		lastErr    = error(failure.SupplyUnavailable)
		lastHealth = health
	)
	for _, block := range candidateBlocks {
		// A candidate the round steps away from says nothing about a provider, and
		// health only ever demotes, so each attempt scores against its own copy and
		// only the one that settles is kept.
		attempt := maps.Clone(health)

		// Before the read, not after: vote reports a provider that cannot serve the
		// block as `down` unless it is already known to be behind, and which
		// providers are behind is a property of the candidate being probed.
		markLagging(ch, claims, block, attempt)

		reading, err := readAtBlock(ctx, ch, block, attempt)
		if err == nil {
			maps.Copy(health, attempt)
			reading.Providers = health
			return reading, nil
		}

		lastErr, lastHealth = err, attempt
		if !steppable(err) {
			break
		}
	}

	maps.Copy(health, lastHealth)
	return failed(lastErr)
}

// readAtBlock reads the chain's supply at one pinned block. Separate from Read so
// the whole read can be retried lower: whether enough providers corroborate is only
// known by asking, and the round that reveals it is not always the first.
func readAtBlock(
	ctx context.Context, ch ChainSource, block uint64, health map[string]string,
) (Reading, error) {
	at := new(big.Int).SetUint64(block)

	blockTime, err := agreedBlockTime(ctx, ch, at, health)
	if err != nil {
		return Reading{}, err
	}
	if ch.MaxBlockAge > 0 && time.Since(blockTime) > ch.MaxBlockAge {
		return Reading{}, failure.SupplyBlockStale
	}

	decimals, err := readDecimals(ctx, ch, at, health)
	if err != nil {
		return Reading{}, err
	}

	raw, err := readTotalSupply(ctx, ch, at, health)
	if err != nil {
		return Reading{}, err
	}

	// Exact: a big.Rat division by a power of ten loses nothing.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	amount := new(big.Rat).SetFrac(raw, scale)

	return Reading{
		Chain:       ch.Name,
		Amount:      amount,
		Block:       block,
		BlockTime:   blockTime,
		Decimals:    decimals,
		RetrievedAt: time.Now().UTC(),
	}, nil
}

// steppable reports whether a lower block is worth trying. Only a shortfall of
// corroboration is: it is the shape a pin lands in when a provider named a head it
// does not hold, and the step re-includes the provider that claim displaced. Every
// other fault is about the block itself — too old, malformed, out of time — and a
// lower block answers none of them while spending what is left of the deadline.
func steppable(err error) bool {
	return errors.Is(err, failure.SupplyUnavailable) || errors.Is(err, failure.SupplyNoQuorum)
}

// headClaim is one provider's answer to "what is your head?", index-aligned with
// ch.Providers. It is a claim: nothing verifies the provider holds that block.
type headClaim struct {
	n  uint64
	ok bool
}

// providerHeads asks every provider for its current head and demotes the ones that
// did not answer. The result is index-aligned with ch.Providers.
func providerHeads(
	ctx context.Context, ch ChainSource, health map[string]string,
) ([]headClaim, error) {
	claims := make([]headClaim, len(ch.Providers))

	var wg sync.WaitGroup
	for i, p := range ch.Providers {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, ch.ProviderTimeout)
			defer cancel()

			// nil is the latest block.
			h, err := p.Client.HeaderByNumber(cctx, nil)
			if err != nil || h == nil || h.Number == nil {
				return
			}
			claims[i] = headClaim{n: h.Number.Uint64(), ok: true}
		})
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, failure.SupplyTimeout
	}

	answered := 0
	for i, c := range claims {
		if !c.ok {
			health[ch.Providers[i].Name] = config.HealthDown
			continue
		}
		answered++
	}

	// Fewer than Quorum providers answered at all, so no block can reach Quorum
	// agreement later either.
	if answered < ch.Quorum {
		return nil, failure.SupplyUnavailable
	}
	return claims, nil
}

// pinCandidateBlocks returns the blocks worth probing, highest first: the Quorum-th
// highest claim and every distinct claim below it. Duplicates are collapsed because
// probing the same block twice cannot produce a different answer, and block 0 ends
// the list — a chain that short is a reset devnet, not one with USDX on it.
func pinCandidateBlocks(claims []headClaim, quorum int) []uint64 {
	heads := make([]uint64, 0, len(claims))
	for _, c := range claims {
		if c.ok {
			heads = append(heads, c.n)
		}
	}
	if len(heads) < quorum {
		return nil
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i] > heads[j] })

	out := make([]uint64, 0, len(heads)-quorum+1)
	for _, h := range heads[quorum-1:] {
		if h == 0 {
			break
		}
		if len(out) > 0 && out[len(out)-1] == h {
			continue
		}
		out = append(out, h)
	}
	return out
}

// markLagging records providers whose own head is below the settled pin. Tolerated
// — it is what Quorum < len(Providers) is for — but recorded, because it is also the
// shape a provider takes when it tries to rewind the observation.
func markLagging(ch ChainSource, claims []headClaim, pinned uint64, health map[string]string) {
	for i, c := range claims {
		if c.ok && c.n < pinned && health[ch.Providers[i].Name] == config.HealthOK {
			health[ch.Providers[i].Name] = config.HealthLagging
		}
	}
}

// agreedBlockTime returns the pinned block's own header timestamp, agreed by
// Quorum providers. Trusting whichever provider proposed the block would let one
// liar pair an ancient number with a fresh stamp. Rationale §6.
func agreedBlockTime(
	ctx context.Context, ch ChainSource, at *big.Int, health map[string]string,
) (time.Time, error) {
	stamps := make([]uint64, len(ch.Providers))
	okAt := make([]bool, len(ch.Providers))

	var wg sync.WaitGroup
	for i, p := range ch.Providers {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, ch.ProviderTimeout)
			defer cancel()

			h, err := p.Client.HeaderByNumber(cctx, at)
			if err != nil || h == nil {
				return
			}
			stamps[i], okAt[i] = h.Time, true
		})
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return time.Time{}, failure.SupplyTimeout
	}

	answers := make([][]byte, len(ch.Providers))
	for i, ok := range okAt {
		if !ok {
			continue
		}
		answers[i] = new(big.Int).SetUint64(stamps[i]).Bytes()
	}

	best, agreed, answered := vote(ch, answers, health)
	if answered < ch.Quorum {
		return time.Time{}, failure.SupplyUnavailable
	}
	if agreed < ch.Quorum {
		return time.Time{}, failure.SupplyNoQuorum
	}

	seconds := new(big.Int).SetBytes(best)
	if !seconds.IsInt64() || seconds.Sign() <= 0 {
		return time.Time{}, failure.SupplyMalformed
	}
	return time.Unix(seconds.Int64(), 0).UTC(), nil
}

// readTotalSupply returns the token's raw totalSupply() at block at, in the
// token's own smallest unit, as agreed by Quorum providers; Read scales it by
// the decimals read at the same height. A negative value is SupplyImplausible:
// the selector returns uint256, so a sign bit can only mean a malformed answer.
//
// Fewer than Quorum responses is SupplyUnavailable when no provider answered
// at all and SupplyNoQuorum otherwise, so an outage and a split fleet are
// reported as the different faults they are.
func readTotalSupply(
	ctx context.Context, ch ChainSource, at *big.Int, health map[string]string,
) (*big.Int, error) {
	best, agreed, answered := vote(ch, callAll(ctx, ch, selectorTotalSupply, at), health)

	if answered < ch.Quorum {
		if answered == 0 {
			return nil, failure.SupplyUnavailable
		}
		return nil, failure.SupplyNoQuorum
	}
	if agreed < ch.Quorum {
		return nil, failure.SupplyNoQuorum
	}

	value := new(big.Int).SetBytes(best)
	if value.Sign() < 0 {
		return nil, failure.SupplyImplausible
	}
	return value, nil
}

// readDecimals returns the token's ERC-20 decimals() at block at, as agreed by
// Quorum providers. Read divides the raw totalSupply by 10^decimals to get whole
// tokens, so this is the scale that makes supply comparable across chains. A
// value above maxDecimals is SupplyMalformed: a provider returning garbage here
// would otherwise shift the supply by 10^N rather than fail.
//
// Read per observation rather than hard-coded: USDX is 6 on Flare and Songbird
// but 18 on Ethereum, and assuming a value would misprice a chain by 10^12.
func readDecimals(
	ctx context.Context, ch ChainSource, at *big.Int, health map[string]string,
) (uint8, error) {
	best, agreed, answered := vote(ch, callAll(ctx, ch, selectorDecimals, at), health)
	if answered < ch.Quorum {
		return 0, failure.SupplyUnavailable
	}
	if agreed < ch.Quorum {
		return 0, failure.SupplyNoQuorum
	}

	d := new(big.Int).SetBytes(best)
	if !d.IsUint64() || d.Uint64() > maxDecimals {
		return 0, failure.SupplyMalformed
	}
	return uint8(d.Uint64()), nil
}

// callAll performs the same eth_call against every provider concurrently. The
// result is index-aligned with ch.Providers — nil means no usable answer — so
// the caller can still name the provider that failed.
func callAll(ctx context.Context, ch ChainSource, selector []byte, at *big.Int) [][]byte {
	answers := make([][]byte, len(ch.Providers))

	call := ethereum.CallMsg{To: &ch.Token, Data: selector}

	var wg sync.WaitGroup
	for i, p := range ch.Providers {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, ch.ProviderTimeout)
			defer cancel()

			// Always an explicit block number: never a tag, never nil.
			data, err := p.Client.CallContract(cctx, call, at)
			// A single ABI word. Anything else is a wrong address, a proxy
			// without an implementation, or a provider missing that block.
			if err != nil || len(data) != 32 {
				return
			}
			answers[i] = data
		})
	}
	wg.Wait()

	return answers
}

// vote returns the most common answer, how many gave it, how many answered at all,
// and records each provider's standing. Demote only. The winner must be UNIQUE: a tie
// reports zero agreement, else the provider ORDER would pick the supply figure.
func vote(ch ChainSource, answers [][]byte, health map[string]string) (best []byte, agreed, answered int) {
	// Tallied per distinct answer, not per provider, so the runner-up is visible.
	type tally struct {
		value []byte
		count int
	}
	var tallies []tally

	for _, a := range answers {
		if a == nil {
			continue
		}
		answered++

		seen := false
		for i := range tallies {
			if string(tallies[i].value) == string(a) {
				tallies[i].count++
				seen = true
				break
			}
		}
		if !seen {
			tallies = append(tallies, tally{value: a, count: 1})
		}
	}

	tied := false
	for _, t := range tallies {
		switch {
		case t.count > agreed:
			// A strictly better answer clears any earlier tie.
			best, agreed, tied = t.value, t.count, false
		case t.count == agreed:
			tied = true
		}
	}

	for i, a := range answers {
		name := ch.Providers[i].Name
		switch {
		case a == nil:
			// A lagging provider is expected to fail here — it does not hold the
			// pinned block. Keeping "lagging" names the actual cause; "down"
			// would send someone looking for an outage that is not there.
			if health[name] != config.HealthLagging {
				health[name] = config.HealthDown
			}
		case string(a) != string(best) && health[name] == config.HealthOK:
			health[name] = config.HealthDisagree
		}
	}

	if tied {
		// best still returned: callers check agreed first, and health is already set.
		return best, 0, answered
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
