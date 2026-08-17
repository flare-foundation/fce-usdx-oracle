// Package oracle orchestrates one price observation: the ordering and the
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
// tee-node allows 2s per action, so internal/observe runs Observe on a timer
// and the handler only calls Sign. See docs/usdx-oracle-rationale.md.
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

// Config carries no target binding: where a signed report lands is settled by
// the store's registered-machine check, not by the payload.
type Config struct {
	Chains []supply.ChainConfig
	Bonds  backing.Source

	// Nil when no credential for the restricted endpoint has been provisioned;
	// the observation then runs through price.ComputeWithoutCash.
	Cash backing.Source

	// Commits the signed report to the configuration it was computed under, so
	// the store can refuse a price from a machine running something else.
	EndpointsHash common.Hash

	// Commits the signed report to the admin set in force when it was computed,
	// on the same terms: the store refuses a price from a machine whose
	// provisioning authority is not the one governance published.
	AdminsHash common.Hash
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
	// Cash is optional; without bonds there is no numerator at all.
	if c.Bonds == nil {
		return errors.New("oracle: the bonds backing source is required")
	}
	return nil
}

// Observation is the result of one successful price observation. It carries no
// signature: the signed body binds an instruction id that is not known yet.
type Observation struct {
	Price      *big.Int
	Decimals   int8
	ObservedAt uint64

	// The configuration this price was computed under, so a later Sign commits
	// to the config in force at observation time, not at request time.
	EndpointsHash common.Hash

	// The admin set in force at observation time, on the same terms.
	AdminsHash common.Hash

	// Health maps each source identifier to a config.Health* value.
	Health map[string]string

	// Data is the ActionResult payload, abi.encode(body, signature). Empty
	// until Sign fills it in for a specific instruction.
	Data []byte
}

// Age returns how long ago the inputs were read, against now.
func (o Observation) Age(now time.Time) time.Duration {
	return now.Sub(time.Unix(int64(o.ObservedAt), 0))
}

// Observe performs one complete observation. The returned error is always a
// failure.Failure, safe to publish.
func Observe(ctx context.Context, cfg Config) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, config.ObservationDeadline)
	defer cancel()

	health := newHealth(cfg)

	// S — supply, one pass, each chain pinned to its own agreed block.
	supplies, err := readAllChains(ctx, cfg, health)
	if err != nil {
		return Observation{Health: health}, err
	}

	// B — reserves.
	bonds, cash, cashOK, err := readBacking(ctx, cfg, health)
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

	// Wall-clock completion, not the oldest input: the store requires strict
	// monotonicity, and the reserve figures change only on an on-chain event, so
	// keying off them would leave consecutive observations sharing a stamp for
	// hours at a time. This stamps when the inputs were READ, which is what
	// MaxObservationAge and the store's maxAge both bound.
	observedAt := uint64(time.Now().UTC().Unix())

	return Observation{
		Price:         value,
		Decimals:      config.PriceDecimals,
		ObservedAt:    observedAt,
		EndpointsHash: cfg.EndpointsHash,
		AdminsHash:    cfg.AdminsHash,
		Health:        health,
	}, nil
}

// Sign binds an observation to one instruction and returns the ActionResult
// payload, abi.encode(body, signature). Nothing is resampled at signing time,
// which would put a fresh timestamp on stale inputs.
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

func signPayload(
	ctx context.Context, signer Signer, obs Observation, instructionID common.Hash,
) ([]byte, error) {
	body, err := report.EncodeBody(report.PriceReport{
		Price:         obs.Price,
		Decimals:      obs.Decimals,
		ObservedAt:    obs.ObservedAt,
		InstructionID: instructionID,
		EndpointsHash: obs.EndpointsHash,
		AdminsHash:    obs.AdminsHash,
	})
	if err != nil {
		return nil, err
	}

	signature, err := signer.Sign(ctx, body)
	if err != nil {
		return nil, err
	}

	// body is carried verbatim; see package report on why it is never re-encoded.
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
		return nil, asFailure(firstErr, failure.SupplyUnavailable)
	}
	return readings, nil
}

// readBacking reads the reserve figures concurrently. Only the bonds error is
// fatal: bonds has no substitute, cash has one narrow case where it is not
// needed.
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
		// Distinct from "down": an outage and a missing SET_SECRET need
		// different responses from whoever reads GET /state.
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
// time. Read skew only — the spread between RetrievedAt stamps, not data age.
//
// How far the reserve figure lags chain state is NOT bounded here, and no
// longer bounded anywhere: Hex republishes on an on-chain event rather than on
// a schedule, so an old figure is normally the correct one (config.BondsSource
// has the reasoning). The lag that remains fails safe — a reserve figure that
// trails a mint understates R/S, and the price is capped at par.
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
func newHealth(cfg Config) map[string]string {
	h := make(map[string]string, len(cfg.Chains)*3+2)
	for _, ch := range cfg.Chains {
		h[chainHealthKey(ch.Name)] = config.HealthUnknown
		// Seeded so a provider that was never reached still appears. An absent
		// key reads as "fine" to anything consuming /state, which is the one
		// thing this map must never do.
		for _, p := range ch.Providers {
			h[p.Name] = config.HealthUnknown
		}
	}
	if cfg.Bonds != nil {
		h[cfg.Bonds.Name()] = config.HealthUnknown
	}
	if cfg.Cash != nil {
		h[cfg.Cash.Name()] = config.HealthUnknown
	} else {
		h[config.HexCashName] = config.HealthNotConfigured
	}
	return h
}

func chainHealthKey(chain string) string { return "rpc_" + chain }

// asFailure passes a Failure through and replaces anything else with a safe
// default, so nothing outside the vocabulary reaches a public surface.
func asFailure(err error, fallback failure.Failure) error {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return fallback
}

// Runner binds a Config and a Signer together. A convenience for tests and
// one-shot use; production goes through internal/observe.
type Runner struct {
	cfg    Config
	signer Signer
}

// NewRunner validates cfg so a misconfiguration surfaces at startup rather
// than later as a mysteriously unavailable feed.
func NewRunner(cfg Config, signer Signer) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("oracle: signer is required")
	}
	return &Runner{cfg: cfg, signer: signer}, nil
}

// Observe performs one observation and signs it, back to back. Does not fit
// inside tee-node's 2s action timeout; kept for tests and tooling.
func (r *Runner) Observe(ctx context.Context, instructionID common.Hash) (Observation, error) {
	obs, err := Observe(ctx, r.cfg)
	if err != nil {
		return obs, err
	}
	return Sign(ctx, r.signer, obs, instructionID)
}
