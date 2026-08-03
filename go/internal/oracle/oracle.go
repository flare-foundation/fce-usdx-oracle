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
// Everything that determines the published price is here, and all of it is
// pinned in the container image and therefore covered by the registered code
// hash. None of it is caller-supplied: if the store address or the chain id
// came from the instruction, anyone could obtain a TEE signature over a report
// bound to a contract they control.
type Config struct {
	Chains []supply.ChainConfig
	Bonds  backing.Source
	Cash   backing.Source

	// HaircutBps discounts the bond component (spec §3.4). Default 0.
	HaircutBps uint16

	Decimals int8
	ChainID  *big.Int
	Store    common.Address
	FeedID   report.FeedID

	// Deadline bounds the whole observation.
	Deadline time.Duration

	// Window is the maximum spread between the earliest and latest input read.
	// Exceeding it means the inputs describe meaningfully different moments and
	// the ratio compares apples to oranges.
	Window time.Duration
}

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
	if c.Bonds == nil || c.Cash == nil {
		return errors.New("oracle: both backing sources are required")
	}
	if c.ChainID == nil || c.ChainID.Sign() <= 0 {
		return errors.New("oracle: a positive chain id is required")
	}
	if c.Store == (common.Address{}) {
		return errors.New("oracle: store address is required")
	}
	if c.Decimals < 0 {
		return errors.New("oracle: decimals must be non-negative")
	}
	if c.Deadline <= 0 || c.Window <= 0 {
		return errors.New("oracle: deadline and window must be positive")
	}
	return nil
}

// Observation is the result of one successful price observation.
type Observation struct {
	Price      *big.Int
	Decimals   int8
	ObservedAt uint64

	// Data is the ActionResult payload: abi.encode(body, signature).
	Data []byte

	// Health maps each source identifier to a config.Health* value.
	Health map[string]string
}

// Observe performs one complete observation and returns a signed payload.
//
// The returned error is always a failure.Failure, safe to publish.
func Observe(ctx context.Context, cfg Config, signer Signer, instructionID common.Hash) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Deadline)
	defer cancel()

	health := newHealth(cfg)

	// S₁ — supply before the reserve read.
	first, err := readAllChains(ctx, cfg, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// B — reserves, bracketed.
	bonds, cash, err := readBacking(ctx, cfg, health)
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
	if err := checkWindow(cfg.Window, first, second, bonds, cash); err != nil {
		return Observation{Health: health}, err
	}

	s1, s2 := supply.Total(first), supply.Total(second)
	total := s1
	if s2.Cmp(s1) > 0 {
		total = s2
	}

	value, err := price.Compute(price.Inputs{
		Bonds:      bonds.Amount,
		Cash:       cash.Amount,
		Supply:     total,
		HaircutBps: cfg.HaircutBps,
	}, cfg.Decimals)
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

	data, err := sign(ctx, cfg, signer, value, observedAt, instructionID)
	if err != nil {
		return Observation{Health: health}, err
	}

	return Observation{
		Price:      value,
		Decimals:   cfg.Decimals,
		ObservedAt: observedAt,
		Data:       data,
		Health:     health,
	}, nil
}

// sign builds the report, signs the body, and wraps it into the payload.
func sign(
	ctx context.Context, cfg Config, signer Signer,
	value *big.Int, observedAt uint64, instructionID common.Hash,
) ([]byte, error) {
	body, err := report.EncodeBody(report.PriceReport{
		ChainID:       cfg.ChainID,
		Store:         cfg.Store,
		FeedID:        cfg.FeedID,
		Price:         value,
		Decimals:      cfg.Decimals,
		ObservedAt:    observedAt,
		InstructionID: instructionID,
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

// readBacking reads both reserve figures concurrently.
func readBacking(ctx context.Context, cfg Config, health map[string]string) (backing.Reading, backing.Reading, error) {
	var (
		bonds, cash       backing.Reading
		bondsErr, cashErr error
		wg                sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		bonds, bondsErr = cfg.Bonds.Fetch(ctx)
	}()
	go func() {
		defer wg.Done()
		cash, cashErr = cfg.Cash.Fetch(ctx)
	}()
	wg.Wait()

	for _, s := range []struct {
		name string
		err  error
	}{{cfg.Bonds.Name(), bondsErr}, {cfg.Cash.Name(), cashErr}} {
		if s.err != nil {
			health[s.name] = config.HealthDown
		} else {
			health[s.name] = config.HealthOK
		}
	}

	if bondsErr != nil {
		return backing.Reading{}, backing.Reading{}, asFailure(bondsErr, failure.BackingUnavailable)
	}
	if cashErr != nil {
		return backing.Reading{}, backing.Reading{}, asFailure(cashErr, failure.BackingUnavailable)
	}
	return bonds, cash, nil
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

// Runner binds a Config and a Signer together so the extension can request an
// observation without knowing how either is assembled.
type Runner struct {
	cfg    Config
	signer Signer
}

// NewRunner validates cfg and returns a Runner.
//
// Validation happens here, at startup, rather than per observation: a
// misconfiguration is a fault that must stop the process, not one that shows up
// later as a mysteriously unavailable feed.
func NewRunner(cfg Config, signer Signer) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("oracle: signer is required")
	}
	return &Runner{cfg: cfg, signer: signer}, nil
}

// Observe performs one observation.
func (r *Runner) Observe(ctx context.Context, instructionID common.Hash) (Observation, error) {
	return Observe(ctx, r.cfg, r.signer, instructionID)
}
