// Package observation orchestrates one price observation: the ordering and the
// deadlines. Arithmetic lives in package price, parsing in package decimal,
// I/O in packages backing and supply.
//
// Supply and reserves are each read once. Neither has an internal race worth
// bracketing: the quorum procedure pins every provider to one agreed block, so
// a supply figure is "supply as of block N", and Hex republishes the reserve
// figures only within ~10 minutes of an on-chain event, so a reserve figure is
// effectively constant across a 20s observation. checkWindow bounds the read
// skew between the two.
//
// Observing and signing are separate calls: an observation takes 2-3s and
// tee-node allows 2s per action, so internal/observation/poller runs Observe on a timer
// and the handler only calls Sign. See docs/usdx-oracle-rationale.md.
package observation

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/observation/backing"
	"fce-usdx-oracle/internal/observation/price"
	"fce-usdx-oracle/internal/observation/supply"
	"fce-usdx-oracle/internal/wire/feedupdate"

	"github.com/ethereum/go-ethereum/common"
)

// Signer signs the encoded report body with the TEE identity key.
type Signer interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
}

// Sources are the upstreams one observation reads. Opened per observation and
// discarded after it, which is what lets a rotated credential take effect on the
// next tick without a restart.
type Sources struct {
	Chains []supply.ChainSource
	Bonds  backing.Source

	// Nil when no credential for the restricted endpoint has been provisioned;
	// the observation then runs through price.ComputeWithoutCash.
	Cash backing.Source
}

// Binding is what the signed update commits to. It is copied verbatim into the
// payload and checked field by field by the store, so it is fixed when the
// inputs are read and never re-derived at signing time.
//
// Inert, unlike Sources: no I/O, nothing to open, safe to copy.
type Binding struct {
	// The extension this machine serves and the feed it speaks for, both pinned
	// in the build. Zero values are rejected at signing time rather than here,
	// so a build lacking them still answers GET /state.
	ExtensionID *big.Int
	FeedID      [21]byte

	// Commits the signed update to the configuration it was computed under, so
	// the store can refuse a value from a machine running something else.
	EndpointsHash common.Hash

	// Commits the signed update to the admin set in force when it was computed,
	// on the same terms: the store refuses a price from a machine whose
	// provisioning authority is not the one governance published.
	AdminsHash common.Hash
}

// Validate checks the sources before an observation reads them.
func (s Sources) Validate() error {
	if len(s.Chains) == 0 {
		return errors.New("oracle: at least one chain is required")
	}
	for _, ch := range s.Chains {
		if err := ch.Validate(); err != nil {
			return err
		}
	}
	// Cash is optional; without bonds there is no numerator at all.
	if s.Bonds == nil {
		return errors.New("oracle: the bonds backing source is required")
	}
	return nil
}

// Observation is the result of one successful price observation. It carries no
// signature: signing is a separate call, so the poller can observe on a timer
// and the handler only sign.
type Observation struct {
	// Scaled by 10^Decimals, in the store's int32 representation. The narrowing
	// happens in Observe, where an out-of-range price fails the observation
	// rather than being truncated into a plausible-looking one.
	Value    int32
	Decimals int8

	ObservedAt uint64

	// Carried whole so a later Sign commits to what was in force when the inputs
	// were read, not to whatever is current when the request arrives.
	Binding Binding

	// Health maps each source identifier to a config.Health* value.
	Health map[string]string

	// Data is the ActionResult payload, abi.encode(FeedUpdate, Signature).
	// Empty until Sign fills it in.
	Data []byte
}

// Age returns how long ago the inputs were read, against now.
func (o Observation) Age(now time.Time) time.Duration {
	return now.Sub(time.Unix(int64(o.ObservedAt), 0))
}

// Observe performs one complete observation. The returned error is always a
// failure.Failure, safe to publish.
func Observe(ctx context.Context, src Sources, binding Binding) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, config.ObservationDeadline)
	defer cancel()

	health := newHealth(src)

	// S — supply, one pass, each chain pinned to its own agreed block.
	supplies, err := readAllChains(ctx, src, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// B — reserves.
	bonds, cash, cashOK, err := readBacking(ctx, src, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// Cash that was never read carries a zero timestamp, which checkWindow
	// ignores; excluded explicitly so that stays deliberate.
	readings := []backing.Reading{bonds}
	if cashOK {
		readings = append(readings, cash)
	}
	if err := checkWindow(config.ObservationWindow, supplies, readings...); err != nil {
		return Observation{Health: health}, err
	}

	total := supply.Total(supplies)

	var value *big.Int
	if cashOK {
		value, err = price.Compute(price.Inputs{
			Bonds:  bonds.Amount,
			Cash:   cash.Amount,
			Supply: total,
		}, config.PriceDecimals)
	} else {
		value, err = price.ComputeWithoutCash(bonds.Amount, total, config.PriceDecimals)
	}
	if err != nil {
		return Observation{Health: health}, err
	}

	// Narrowed here rather than at signing time, so an out-of-range price fails
	// the observation it came from. Fails closed: a wrapped int32 would be
	// published as fact.
	scaled, err := feedupdate.ValueFromBig(value)
	if err != nil {
		return Observation{Health: health}, err
	}

	// observedAt is the home chain's block header timestamp, not wall clock and
	// not a reserve figure's own stamp. An already-mined block guarantees the
	// store's requirement that observedAt be strictly below block.timestamp;
	// MaxObservationAge bounds how old it may be.
	observedAt, err := homeBlockTime(supplies)
	if err != nil {
		return Observation{Health: health}, err
	}

	return Observation{
		Value:      scaled,
		Decimals:   config.PriceDecimals,
		ObservedAt: observedAt,
		Binding:    binding,
		Health:     health,
	}, nil
}

// Sign returns the ActionResult payload, abi.encode(FeedUpdate, Signature).
// Nothing is resampled here: doing so would put a fresh timestamp on inputs
// that were read earlier.
//
// The payload binds no request identifier, so a signature is not tied to the
// request that asked for it. Replay protection is the strictly increasing
// observedAt and the feed binding, both enforced by the store; correlation is
// the tee-node action id, outside the signature.
func Sign(ctx context.Context, signer Signer, obs Observation) (Observation, error) {
	data, err := signPayload(ctx, signer, obs)
	if err != nil {
		return Observation{Health: obs.Health}, err
	}
	obs.Data = data
	return obs, nil
}

func signPayload(ctx context.Context, signer Signer, obs Observation) ([]byte, error) {
	update := feedupdate.FeedUpdate{
		ExtensionID:   obs.Binding.ExtensionID,
		FeedID:        obs.Binding.FeedID,
		Value:         obs.Value,
		Decimals:      obs.Decimals,
		ObservedAt:    obs.ObservedAt,
		EndpointsHash: obs.Binding.EndpointsHash,
		AdminsHash:    obs.Binding.AdminsHash,
	}

	// The signer receives the SignedPayload preimage, not its hash: tee-node
	// hashes what it is given and applies EIP-191 itself. See package feedupdate.
	message, err := feedupdate.SigningMessage(update, config.ChainID)
	if err != nil {
		return nil, err
	}

	signature, err := signer.Sign(ctx, message)
	if err != nil {
		return nil, err
	}

	return feedupdate.EncodeData(update, signature)
}

// homeBlockTime picks the stamp out of the home chain's reading. Fails closed:
// a missing or zero block time would otherwise stamp the epoch, which the store
// reads as older than everything and rejects as NotNewer — after the fee.
func homeBlockTime(supplies []supply.Reading) (uint64, error) {
	for _, r := range supplies {
		if r.Chain != config.HomeChain {
			continue
		}
		if r.BlockTime.IsZero() {
			return 0, failure.Internal
		}
		unix := r.BlockTime.Unix()
		if unix <= 0 {
			return 0, failure.Internal
		}
		return uint64(unix), nil
	}
	return 0, failure.Internal
}

// readAllChains reads every chain concurrently. Any chain failing fails the
// whole observation: a partial sum is a wrong supply, not a smaller one.
func readAllChains(ctx context.Context, src Sources, health map[string]string) ([]supply.Reading, error) {
	readings := make([]supply.Reading, len(src.Chains))
	errs := make([]error, len(src.Chains))

	var wg sync.WaitGroup
	for i, ch := range src.Chains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readings[i], errs[i] = supply.Read(ctx, ch)
		}()
	}
	wg.Wait()

	var firstErr error
	for i, err := range errs {
		name := config.ChainHealthKey(src.Chains[i].Name)

		// Reported whether or not the chain read succeeded. Quorum lets a chain
		// stay "ok" while one of its providers is dead, dissenting or lagging,
		// and that state is invisible without this.
		for provider, status := range readings[i].Providers {
			health[provider] = status
		}

		if err != nil {
			health[name] = config.HealthDown
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		health[name] = config.HealthOK
	}
	if firstErr != nil {
		return nil, failure.Coerce(firstErr, failure.SupplyUnavailable)
	}
	return readings, nil
}

// readBacking reads the reserve figures concurrently. Only the bonds error is
// fatal: bonds has no substitute, cash has one narrow case where it is not
// needed.
func readBacking(
	ctx context.Context, src Sources, health map[string]string,
) (bonds backing.Reading, cash backing.Reading, cashOK bool, err error) {
	var (
		bondsErr, cashErr error
		wg                sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		bonds, bondsErr = src.Bonds.Fetch(ctx)
	}()
	if src.Cash != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cash, cashErr = src.Cash.Fetch(ctx)
		}()
	}
	wg.Wait()

	health[src.Bonds.Name()] = healthOf(bondsErr)
	if src.Cash != nil {
		health[src.Cash.Name()] = healthOf(cashErr)
	} else {
		// Distinct from "down": an outage and a missing SET_SECRET need
		// different responses from whoever reads GET /state.
		health[config.HexCashName] = config.HealthNotConfigured
	}

	if bondsErr != nil {
		return backing.Reading{}, backing.Reading{}, false, failure.Coerce(bondsErr, failure.BackingUnavailable)
	}
	return bonds, cash, src.Cash != nil && cashErr == nil, nil
}

func healthOf(err error) string {
	if err != nil {
		return config.HealthDown
	}
	return config.HealthOK
}

// checkWindow rejects observations whose inputs are too far apart in time.
// Only checks read skew—spread between RetrievedAt stamps, not data age.
// Reserve lag is unbounded: Hex republishes by event, so old figures are expected.
// A reserve lag understates R/S, and price is capped at par for safety.
func checkWindow(window time.Duration, supplies []supply.Reading, readings ...backing.Reading) error {
	var earliest, latest time.Time

	note := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
		if latest.IsZero() || t.After(latest) {
			latest = t
		}
	}

	for _, r := range supplies {
		note(r.RetrievedAt)
	}
	for _, r := range readings {
		note(r.RetrievedAt)
	}

	if earliest.IsZero() || latest.IsZero() {
		return failure.Internal
	}
	if latest.Sub(earliest) > window {
		return failure.WindowExceeded
	}
	return nil
}

// newHealth seeds every known source as unknown. Absence must never read as
// health: an empty map looks like "everything fine" on GET /state.
func newHealth(src Sources) map[string]string {
	h := make(map[string]string, len(src.Chains)*3+2)
	for _, ch := range src.Chains {
		h[config.ChainHealthKey(ch.Name)] = config.HealthUnknown
		// Seeded so a provider that was never reached still appears. An absent
		// key reads as "fine" to anything consuming /state, which is the one
		// thing this map must never do.
		for _, p := range ch.Providers {
			h[p.Name] = config.HealthUnknown
		}
	}
	if src.Bonds != nil {
		h[src.Bonds.Name()] = config.HealthUnknown
	}
	if src.Cash != nil {
		h[src.Cash.Name()] = config.HealthUnknown
	} else {
		h[config.HexCashName] = config.HealthNotConfigured
	}
	return h
}
