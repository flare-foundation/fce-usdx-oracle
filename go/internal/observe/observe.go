// Package observe runs price observations on a timer and serves the latest one.
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
package observe

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"
	"fce-usdx-oracle/internal/readiness"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// Opener opens the sources for the next observation and reports the binding
// they were opened under. Called per observation, so a rotated credential takes
// effect without a restart.
type Opener interface {
	Open() (oracle.Sources, oracle.Binding, error)
}

// Poller observes on a timer and keeps the most recent result.
type Poller struct {
	opener Opener
	signer oracle.Signer

	// Fields rather than direct constant reads so tests can drive the loop
	// without sleeping. New always sets the real ones.
	interval time.Duration
	maxAge   time.Duration
	now      func() time.Time

	mu sync.RWMutex

	last     oracle.Observation
	haveLast bool

	// Always a failure.Failure, so it is safe to publish.
	lastErr error

	observations int
	failures     int
}

func New(opener Opener, signer oracle.Signer) *Poller {
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
		p.record(oracle.Observation{}, err)
		return
	}

	obs, err := oracle.Observe(ctx, src, binding)
	if err != nil {
		// Safe to log: failure vocabulary is constant text.
		logger.Warnf("observation failed: %s", err)
		p.record(obs, err)
		return
	}
	p.record(obs, nil)
}

func (p *Poller) record(obs oracle.Observation, err error) {
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
func (p *Poller) Latest() (oracle.Observation, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.haveLast {
		if p.lastErr != nil {
			return oracle.Observation{Health: p.last.Health}, p.lastErr
		}
		return oracle.Observation{Health: p.last.Health}, failure.NoObservation
	}

	if age := p.last.Age(p.now()); age > p.maxAge {
		return oracle.Observation{Health: p.last.Health}, failure.ObservationStale
	}
	return p.last, nil
}

// Report returns the latest observation, signed — the whole of what a GET_FEED
// request costs. The update carries no instruction id: the store dropped the
// field, so nothing binds a signature to the request that asked for it.
//
// requestedAt is the block timestamp of the instruction event that asked for
// this update, taken from the cosigned DataFixed. It is not published — what
// gets signed is still the moment the inputs were READ — but it is the clock
// checkClock measures against. Nothing about the request may enter the payload;
// see the note on Sign.
func (p *Poller) Report(ctx context.Context, requestedAt uint64) (oracle.Observation, error) {
	obs, err := p.Latest()
	if err != nil {
		return obs, err
	}
	if err := checkClock(obs.ObservedAt, requestedAt, p.maxAge); err != nil {
		// Health still travels: a clock fault says nothing about the sources, and
		// an operator reading a refusal needs to see they are fine.
		return oracle.Observation{Health: obs.Health}, err
	}
	return oracle.Sign(ctx, p.signer, obs)
}

// checkClock enforces observation staleness against the block's timestamp,
// ensuring only fresh data is reported for a given feed update request.
// Only older (stale) observations can be flagged; newer observations are allowed.
// Returns an error if the bound is not met.
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

// ReadinessReport assembles what the poller knows about itself, binds it to one
// instruction and signs it. Assembled here because everything but the provisioning
// half lives behind this mutex, and so does the signer. No I/O beyond the signing call.
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
		ChainID:        config.ChainID,
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

	// asFailure, not the raw error: a signer's own words can name the endpoint it
	// failed to reach, and this must not depend on the signer keeping to vocabulary.
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
