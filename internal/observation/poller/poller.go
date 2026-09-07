// Package poller runs price observations on a timer and serves the latest one.
//
// A full observation takes 2.0–2.9s and tee-node allows 2s per action, so the
// slow half runs here and the handler does only the signing round trip.
// Caching adds a staleness mode, so Latest fails closed past
// config.MaxObservationAge.
//
// That bound is measured twice, against two different clocks. Latest uses this
// machine's, which is what catches upstreams that have stopped — but it is also
// the clock that stamped the observation, so it cannot catch itself being wrong.
// Report measures the same age again against the requesting instruction's block
// timestamp, which the data-provider quorum cosigned and the host cannot move.
// See checkClock.
package poller

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/observation"
	"fce-usdx-oracle/internal/wire/readiness"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// Opener opens the sources for the next observation and reports the binding
// they were opened under. Called per observation, so a rotated credential takes
// effect without a restart.
type Opener interface {
	Open() (observation.Sources, observation.Binding, error)
}

// Poller observes on a timer and keeps the most recent result.
type Poller struct {
	opener Opener
	signer observation.Signer

	// Fields rather than reads of the config constants, so a test can drive the
	// loop without sleeping. New sets the production values.
	interval time.Duration
	maxAge   time.Duration
	now      func() time.Time

	mu sync.RWMutex

	last     observation.Observation
	haveLast bool

	// Always a failure.Failure, so it is safe to publish.
	lastErr error

	observations int
	failures     int
}

func New(opener Opener, signer observation.Signer) *Poller {
	return &Poller{
		opener:   opener,
		signer:   signer,
		interval: config.ObservationInterval,
		maxAge:   config.MaxObservationAge,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Run observes immediately and then every interval, until ctx is cancelled. A
// failed observation is recorded and the loop continues: the process must not
// exit because an upstream is down.
func (p *Poller) Run(ctx context.Context) {
	p.observeOnce(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.observeOnce(ctx)
		}
	}
}

func (p *Poller) observeOnce(ctx context.Context) {
	src, binding, err := p.opener.Open()
	if err != nil {
		// Unprovisioned is the normal state at boot; not worth a log line each tick.
		p.record(observation.Observation{}, err)
		return
	}

	obs, err := observation.Observe(ctx, src, binding)
	if err != nil {
		// Safe to log: failure vocabulary is constant text.
		logger.Warnf("observation failed: %s", err)
		p.record(obs, err)
		return
	}
	p.record(obs, nil)
}

func (p *Poller) record(obs observation.Observation, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err != nil {
		p.failures++
		p.lastErr = failure.Coerce(err, failure.Internal)
		// The previous observation stays and ages out via maxAge: one flapping
		// RPC provider should not stop the feed.
		if obs.Health != nil {
			p.last.Health = obs.Health
		}
		return
	}

	p.observations++
	p.last = obs
	p.haveLast = true
	p.lastErr = nil
}

// Latest returns the newest observation, or a publishable failure. The
// staleness check lives here so no path to the cached figures skips the bound.
func (p *Poller) Latest() (observation.Observation, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.haveLast {
		if p.lastErr != nil {
			return observation.Observation{Health: p.last.Health}, p.lastErr
		}
		return observation.Observation{Health: p.last.Health}, failure.NoObservation
	}

	if age := p.last.Age(p.now()); age > p.maxAge {
		return observation.Observation{Health: p.last.Health}, failure.ObservationStale
	}
	return p.last, nil
}

// Report returns the latest observation, signed. This is the whole cost of a
// GET_FEED request. The payload binds no request identifier, so a signature is
// not tied to the request that produced it.
//
// requestedAt is the block timestamp of the instruction event that asked for
// the update, taken from the cosigned DataFixed. It is never published — the
// signed observedAt remains the moment the inputs were read — and serves only
// as the clock checkClock measures staleness against.
func (p *Poller) Report(ctx context.Context, requestedAt uint64) (observation.Observation, error) {
	obs, err := p.Latest()
	if err != nil {
		return obs, err
	}
	if err := checkClock(obs.ObservedAt, requestedAt, p.maxAge); err != nil {
		// Health still travels: a clock fault says nothing about the sources, and
		// an operator reading a refusal needs to see they are fine.
		return observation.Observation{Health: obs.Health}, err
	}
	// Observing needs only the endpoints, so the price and the source health
	// are visible as soon as those are published. Signing needs the whole
	// binding: the on-chain store rejects an update that commits to no admin
	// set, so one is refused here by name rather than left to the encoder's
	// zero-hash check, which would read as an internal error.
	if obs.Binding.AdminsHash == (common.Hash{}) {
		return observation.Observation{Health: obs.Health}, failure.NotConfigured
	}
	return observation.Sign(ctx, p.signer, obs)
}

// checkClock bounds how far the observation may trail the requesting block.
// Only a stale observation is rejected; one stamped ahead of the request is
// accepted, since the store applies its own future bound.
func checkClock(observedAt, requestedAt uint64, maxAge time.Duration) error {
	// Zero requestedAt means a malformed instruction.
	if requestedAt == 0 {
		return failure.MissingRequestTime
	}
	// Flag if observed data is too old relative to the request.
	if requestedAt > observedAt && requestedAt-observedAt > uint64(maxAge.Seconds()) {
		return failure.ClockBehindChain
	}
	return nil
}

// ReadinessReport assembles the poller's account of itself, binds it to one
// instruction and signs it. It is assembled here because every field but the
// provisioning half is behind this mutex, as is the signer. The signing call is
// the only I/O.
func (p *Poller) ReadinessReport(
	ctx context.Context, instructionID common.Hash, provisioning types.Readiness,
) ([]byte, error) {
	p.mu.RLock()
	now := p.now()

	// Only the class escapes, never the error's text. lastErr is already
	// vocabulary by construction; anything that somehow is not becomes
	// ClassInternal rather than reaching a public surface in its own words.
	var lastClass failure.Class
	if p.lastErr != nil {
		var f failure.Failure
		if errors.As(p.lastErr, &f) {
			lastClass = f.Class()
		} else {
			lastClass = failure.ClassInternal
		}
	}

	rep := types.ReadinessReport{
		Readiness:      provisioning,
		Observations:   p.observations,
		Failures:       p.failures,
		LastErrorClass: string(lastClass),
		ChainID:        config.ChainID(),
	}

	if p.haveLast {
		age := p.last.Age(now)
		rep.LastObservedAt = p.last.ObservedAt
		// Clamped at zero: a clock that has gone backwards should read as fresh
		// rather than as an enormous unsigned age.
		if age > 0 {
			rep.ObservationAge = uint64(age.Seconds())
		}
		rep.WouldSign = age <= p.maxAge
	}

	// Copied under the lock: the map is replaced wholesale by record(), and
	// handing the live reference out would race with the next observation.
	if p.last.Health != nil {
		rep.SourceHealth = make(map[string]string, len(p.last.Health))
		for k, v := range p.last.Health {
			rep.SourceHealth[k] = v
		}
	}
	p.mu.RUnlock()

	payload, err := json.Marshal(rep)
	if err != nil {
		return nil, failure.Internal
	}

	body, err := readiness.EncodeBody(readiness.Report{
		InstructionID: instructionID,
		Timestamp:     uint64(now.Unix()),
		PayloadHash:   crypto.Keccak256Hash(payload),
	})
	if err != nil {
		return nil, failure.Coerce(err, failure.Internal)
	}

	// Coerced rather than passed through: a signer's own error text can name the
	// endpoint it failed to reach, and this must not rely on the signer keeping
	// to the vocabulary.
	signature, err := p.signer.Sign(ctx, body)
	if err != nil {
		return nil, failure.Coerce(err, failure.Internal)
	}

	// payload verbatim: the signature commits to these exact bytes, and
	// re-serialising would not reproduce them.
	return readiness.EncodeData(body, signature, payload)
}

// Stats reports cumulative counters for GET /state.
func (p *Poller) Stats() (observations, failures int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.observations, p.failures
}
