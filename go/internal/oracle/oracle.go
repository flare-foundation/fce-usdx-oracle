// Package oracle orchestrates one price observation.
//
// It is the only place that touches every other package, and the only place
// where timing matters. The arithmetic lives in package price, the parsing in
// package decimal, the I/O in packages backing and supply; what is here is the
// ordering and the deadlines.
//
// # The conservative double-read
//
// Supply reads and reserve reads are not atomic, and a redemption landing
// between them changes both sides of the ratio. Read reserves first (high) and
// supply after the burn (low) and the price is over-stated — the direction that
// matters, because an over-stated price hides a shortfall.
//
// This is not hypothetical at the observed scale. Spec §6.1 records a single
// 1,000,000 USDX burn on Flare at 07:10:58Z on 31 Jul 2026: 4.8% of supply in
// one event, far larger than any plausible deviation threshold. And a stress
// event is exactly when redemptions cluster and the price matters most.
//
// So the observation brackets the reserve read between two supply reads and
// takes the LARGER supply:
//
//	S₁ ← supply        B ← reserves        S₂ ← supply
//	S  = max(S₁, S₂)
//	price = min(1, B / S)
//
// A larger denominator is a lower price. Any timing skew therefore biases the
// result downward, never upward: the error is always in the safe direction.
// The cost is one extra round of RPC reads per observation.
//
// # Fail closed, always
//
// Any source down, any value implausible, any window exceeded: publish nothing
// and return a failure. Never guess, never interpolate, never fall back to a
// cached figure. A frozen feed with an ageing timestamp is the designed failure
// mode (spec §7.15) and consumers are required to check the timestamp; a
// confidently wrong price is not recoverable downstream.
//
// # Observing on a timer, signing on demand
//
// Observe is slow — five upstream sources, two of them multi-provider — and
// tee-node gives an extension **2 seconds** to answer an action
// (`settings.ProxyTimeout`, a const in its internal package). Measured against
// live sources on 5 Aug 2026 an observation takes 2.0–2.9s, so running one
// inside the request handler timed out every time, and the extension's answer
// never reached the chain at all.
//
// So the two halves are separate calls. internal/observe runs Observe on a
// timer and keeps the latest result; the handler calls Sign, which binds that
// result to the requesting instruction id and returns in one signing round
// trip. The instruction id is why signing cannot happen in advance, and the
// upstream latency is why observing cannot happen on demand.
//
// What is signed is always the observation's own figures — its price, its
// observedAt, its EndpointsHash. Nothing is resampled at signing time, because
// a fresh timestamp over stale inputs is exactly the lie the freshness guards
// exist to prevent. Staleness is bounded by the caller instead:
// config.MaxObservationAge, enforced in internal/observe.
//
// # Observing without cash
//
// The cash figure is the one input with a single unredundant source — Hex's
// restricted endpoint, one URL, gated by one credential. Cfg.Cash is therefore
// allowed to be nil (no credential provisioned), and a fetch failure is not
// automatically fatal.
//
// What is NOT permitted is publishing a different number when it is missing.
// docs/usdx-oracle-availability.md settles that: dropping cash from the
// numerator would suppress exactly the buffer that decides whether a depeg
// prints, and nothing in the signed payload could say the value was computed
// that way. So the observation takes the one case where the missing figure
// cannot matter — cash is non-negative and the price is capped at par, so a
// bonds-only price that already reaches par is the answer — and fails closed
// everywhere else. price.ComputeWithoutCash is that rule; the equality it
// relies on is pinned by TestComputeWithoutCash_MatchesTheFullComputation.
//
// Health still reports the source as down or unconfigured, so an operator can
// tell a par-from-bonds observation from a complete one even though the
// published price is identical by construction.
package oracle

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"fce-usdx-oracle/internal/backing"
	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/price"
	"fce-usdx-oracle/internal/report"
	"fce-usdx-oracle/internal/supply"

	"github.com/ethereum/go-ethereum/common"
)

// Signer signs the encoded report body with the TEE identity key.
type Signer interface {
	Sign(ctx context.Context, message []byte) ([]byte, error)
}

// Config is a complete observation configuration.
//
// It carries no target binding: where a signed report lands is settled by the
// store's registered-machine check, not by the payload (see internal/report).
type Config struct {
	Chains []supply.ChainConfig
	Bonds  backing.Source

	// Cash is nil when no credential for the restricted endpoint has been
	// provisioned. The observation then runs through
	// price.ComputeWithoutCash, which publishes only when the missing figure
	// provably cannot change the answer — see "Observing without cash" above.
	Cash backing.Source

	// EndpointsHash commits the signed report to the configuration it was
	// computed under, so UsdxPriceStore can refuse a price from a machine
	// running something other than what setEndpoints published.
	EndpointsHash common.Hash
}

// The observation's timing bounds and output scale are constants in
// internal/config, not fields here. They are safety properties rather than
// knobs: widening the window silently weakens the TOCTOU guarantee the
// conservative double-read provides, and the decimals are what the store and
// every consumer agree on.

// Validate checks a configuration at startup.
func (c Config) Validate() error {
	if len(c.Chains) == 0 {
		return errors.New("oracle: at least one chain is required")
	}
	for _, ch := range c.Chains {
		if err := ch.Validate(); err != nil {
			return err
		}
	}
	// Cash is optional; Bonds is not. Without the bond figure there is no
	// numerator at all, and no rule that can recover one.
	if c.Bonds == nil {
		return errors.New("oracle: the bonds backing source is required")
	}
	return nil
}

// Observation is the result of one successful price observation.
//
// It carries no signature. Signing is a separate step because the signed body
// binds the instruction id, which is not known when the observation runs — see
// Sign and the "Observing on a timer" note in the package doc.
type Observation struct {
	Price      *big.Int
	Decimals   int8
	ObservedAt uint64

	// EndpointsHash is the configuration this price was computed under, carried
	// so a later Sign commits to the config actually in force at observation
	// time rather than whatever is current when the request arrives.
	EndpointsHash common.Hash

	// Health maps each source identifier to a config.Health* value.
	Health map[string]string

	// Data is the ActionResult payload, abi.encode(body, signature). Empty until
	// Sign fills it in for a specific instruction.
	Data []byte
}

// Age returns how long ago the inputs were read, against now.
func (o Observation) Age(now time.Time) time.Duration {
	return now.Sub(time.Unix(int64(o.ObservedAt), 0))
}

// Observe performs one complete observation.
//
// The returned error is always a failure.Failure, safe to publish.
func Observe(ctx context.Context, cfg Config) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, config.ObservationDeadline)
	defer cancel()

	health := newHealth(cfg)

	// S₁ — supply before the reserve read.
	first, err := readAllChains(ctx, cfg, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// B — reserves, bracketed. cashOK is false when the source is unconfigured
	// or failed; that is not fatal here, it narrows what may be published.
	bonds, cash, cashOK, err := readBacking(ctx, cfg, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// S₂ — supply after. Any redemption that landed mid-observation shows up
	// here as a lower supply; taking the max keeps the earlier, higher figure.
	second, err := readAllChains(ctx, cfg, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// The consistency window spans every input, including both supply passes.
	// A cash reading that was never taken carries a zero timestamp, which
	// checkWindow ignores; it is excluded explicitly so that stays deliberate.
	readings := []backing.Reading{bonds}
	if cashOK {
		readings = append(readings, cash)
	}
	if err := checkWindow(config.ObservationWindow, first, second, readings...); err != nil {
		return Observation{Health: health}, err
	}

	s1, s2 := supply.Total(first), supply.Total(second)
	total := s1
	if s2.Cmp(s1) > 0 {
		total = s2
	}

	// Without cash, only a price that cannot depend on it may be published.
	// ComputeWithoutCash returns an error rather than a degraded number in
	// every other case.
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

	// observedAt is wall-clock completion time, not the oldest input timestamp.
	//
	// The store requires strict monotonicity, and an input timestamp can stall:
	// the public reserves endpoint ticks on a ~10 minute cadence, so two
	// observations minutes apart could share one, and the second would be
	// rejected as a replay. Freshness of the underlying data is guarded
	// separately and earlier — a stale reserve figure fails the observation
	// outright in package backing, so a published price is never built on data
	// older than MaxAge.
	observedAt := uint64(time.Now().UTC().Unix())

	return Observation{
		Price:         value,
		Decimals:      config.PriceDecimals,
		ObservedAt:    observedAt,
		EndpointsHash: cfg.EndpointsHash,
		Health:        health,
	}, nil
}

// Sign binds an observation to one instruction and returns the ActionResult
// payload, abi.encode(body, signature).
//
// Separate from Observe because the two happen at different times: the
// observation runs on a timer, and the instruction id only exists when a
// request arrives. What is signed is the observation's own figures and the
// configuration hash it was computed under — never anything resampled at
// signing time, which would put a fresh timestamp on stale inputs.
//
// The returned Observation is the input with Data filled in; the caller hands
// that straight to buildResult.
func Sign(
	ctx context.Context, signer Signer, obs Observation, instructionID common.Hash,
) (Observation, error) {
	data, err := signPayload(ctx, signer, obs, instructionID)
	if err != nil {
		return Observation{Health: obs.Health}, err
	}
	obs.Data = data
	return obs, nil
}

// signPayload builds the report, signs the body, and wraps it into the payload.
func signPayload(
	ctx context.Context, signer Signer, obs Observation, instructionID common.Hash,
) ([]byte, error) {
	body, err := report.EncodeBody(report.PriceReport{
		Price:         obs.Price,
		Decimals:      obs.Decimals,
		ObservedAt:    obs.ObservedAt,
		InstructionID: instructionID,
		EndpointsHash: obs.EndpointsHash,
	})
	if err != nil {
		return nil, err
	}

	signature, err := signer.Sign(ctx, body)
	if err != nil {
		return nil, err
	}

	// body is carried verbatim; see package report on why it is never
	// re-encoded on the way in.
	return report.EncodeData(body, signature)
}

// readAllChains reads every chain concurrently. Any chain failing fails the
// whole observation: a partial sum is a wrong supply, not a smaller one.
func readAllChains(ctx context.Context, cfg Config, health map[string]string) ([]supply.Reading, error) {
	readings := make([]supply.Reading, len(cfg.Chains))
	errs := make([]error, len(cfg.Chains))

	var wg sync.WaitGroup
	for i, ch := range cfg.Chains {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readings[i], errs[i] = supply.Read(ctx, ch)
		}()
	}
	wg.Wait()

	var firstErr error
	for i, err := range errs {
		name := chainHealthKey(cfg.Chains[i].Name)
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
		return nil, asFailure(firstErr, failure.SupplyUnavailable)
	}
	return readings, nil
}

// readBacking reads the reserve figures concurrently.
//
// Only the bonds error is fatal. A cash source that is absent or fails returns
// cashOK false, and the caller decides whether the observation can still
// produce a price — see "Observing without cash" in the package doc. That
// asymmetry is the whole difference between the two sources: bonds has no
// substitute, and cash has one narrow case where it is not needed.
func readBacking(
	ctx context.Context, cfg Config, health map[string]string,
) (bonds backing.Reading, cash backing.Reading, cashOK bool, err error) {
	var (
		bondsErr, cashErr error
		wg                sync.WaitGroup
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		bonds, bondsErr = cfg.Bonds.Fetch(ctx)
	}()
	if cfg.Cash != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cash, cashErr = cfg.Cash.Fetch(ctx)
		}()
	}
	wg.Wait()

	health[cfg.Bonds.Name()] = healthOf(bondsErr)
	if cfg.Cash != nil {
		health[cfg.Cash.Name()] = healthOf(cashErr)
	} else {
		// Distinct from "down": nothing is broken, the credential was never
		// provisioned. An operator reading GET /state needs to tell those
		// apart — one is an outage, the other is a missing SET_SECRET.
		health[config.HexCashName] = config.HealthNotConfigured
	}

	if bondsErr != nil {
		return backing.Reading{}, backing.Reading{}, false, asFailure(bondsErr, failure.BackingUnavailable)
	}
	return bonds, cash, cfg.Cash != nil && cashErr == nil, nil
}

func healthOf(err error) string {
	if err != nil {
		return config.HealthDown
	}
	return config.HealthOK
}

// checkWindow rejects an observation whose inputs are spread too far apart in
// time. This is what bounds the TOCTOU exposure the double-read biases.
func checkWindow(window time.Duration, first, second []supply.Reading, readings ...backing.Reading) error {
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

	for _, r := range first {
		note(r.RetrievedAt)
	}
	for _, r := range second {
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

// newHealth seeds every known source as unknown.
//
// Absence must never read as health: an empty map is indistinguishable from
// "everything fine" to anyone reading GET /state, and that is exactly the state
// where a source has silently stopped being polled.
func newHealth(cfg Config) map[string]string {
	h := make(map[string]string, len(cfg.Chains)+2)
	for _, ch := range cfg.Chains {
		h[chainHealthKey(ch.Name)] = config.HealthUnknown
	}
	if cfg.Bonds != nil {
		h[cfg.Bonds.Name()] = config.HealthUnknown
	}
	if cfg.Cash != nil {
		h[cfg.Cash.Name()] = config.HealthUnknown
	} else {
		// Seeded by its pinned name so the source never simply vanishes from
		// /state when it is unconfigured. An absent key reads as "no such
		// source", which is the one thing this map must never say.
		h[config.HexCashName] = config.HealthNotConfigured
	}
	return h
}

func chainHealthKey(chain string) string { return "rpc_" + chain }

// asFailure passes a Failure through and replaces anything else with a safe
// default. Nothing that is not already vocabulary reaches a public surface.
func asFailure(err error, fallback failure.Failure) error {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return fallback
}

// Runner binds a Config and a Signer together so a caller can observe and sign
// without assembling either.
//
// It is now a convenience for tests and one-shot use. Production goes through
// internal/observe, which owns the timer and the staleness bound — see the
// package doc.
type Runner struct {
	cfg    Config
	signer Signer
}

// NewRunner validates cfg and returns a Runner.
//
// Validation happens here rather than per observation: a misconfiguration is a
// fault that must surface at once, not one that shows up later as a
// mysteriously unavailable feed.
func NewRunner(cfg Config, signer Signer) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("oracle: signer is required")
	}
	return &Runner{cfg: cfg, signer: signer}, nil
}

// Observe performs one observation and signs it for instructionID.
//
// Both halves back to back, which is what the handler used to do and what no
// longer fits inside tee-node's 2s action timeout. Kept for tests and tooling.
func (r *Runner) Observe(ctx context.Context, instructionID common.Hash) (Observation, error) {
	obs, err := Observe(ctx, r.cfg)
	if err != nil {
		return obs, err
	}
	return Sign(ctx, r.signer, obs, instructionID)
}
