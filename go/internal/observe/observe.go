// Package observe runs price observations on a timer and serves the latest one.
//
// A full observation takes 2.0–2.9s and tee-node allows 2s per action, so the
// slow half runs here and the handler does only the signing round trip.
// Caching adds a staleness mode, so Latest fails closed past
// config.MaxObservationAge.
package observe

import (
	"context"
	"errors"
	"sync"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// Resolver produces the configuration for the next observation. Called per
// observation, so a rotated credential takes effect without a restart.
type Resolver interface {
	Resolve() (oracle.Config, error)
}

// Poller observes on a timer and keeps the most recent result.
type Poller struct {
	resolver Resolver
	signer   oracle.Signer

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

func New(resolver Resolver, signer oracle.Signer) *Poller {
	return &Poller{
		resolver: resolver,
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
	cfg, err := p.resolver.Resolve()
	if err != nil {
		// Unprovisioned is the normal state at boot; not worth a log line each tick.
		p.record(oracle.Observation{}, err)
		return
	}

	obs, err := oracle.Observe(ctx, cfg)
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
		p.lastErr = asFailure(err)
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

// Report returns the latest observation signed for instructionID — the whole
// of what a GET_PRICE request costs.
func (p *Poller) Report(ctx context.Context, instructionID common.Hash) (oracle.Observation, error) {
	obs, err := p.Latest()
	if err != nil {
		return obs, err
	}
	return oracle.Sign(ctx, p.signer, obs, instructionID)
}

// Stats reports cumulative counters for GET /state.
func (p *Poller) Stats() (observations, failures int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.observations, p.failures
}

// asFailure keeps anything outside the vocabulary off the public surfaces:
// lastErr reaches ActionResult.Log, which is world-readable.
func asFailure(err error) error {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return failure.Internal
}
