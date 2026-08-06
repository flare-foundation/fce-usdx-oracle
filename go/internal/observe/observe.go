// Package observe runs price observations on a timer and serves the latest one.
//
// # Why this exists
//
// tee-node gives an extension **2 seconds** to answer an action:
//
//	// tee-node internal/extension/extension.go
//	client := http.Client{Timeout: settings.ProxyTimeout}  // const = 2 * time.Second
//
// It is a const in tee-node's internal package — not an env var, not a config
// field, not reachable from this module. A complete observation reads five
// upstream sources, two of them across multiple RPC providers, and measured
// 2.0–2.9s against live sources on 5 Aug 2026. Observing inside the handler
// therefore timed out on every request: tee-node recorded status 3 with a
// client-timeout log, the extension's answer was discarded, and no price ever
// reached the chain.
//
// So the slow half runs here, on a timer, and the handler does only the fast
// half — one signing round trip against the enclave's own key.
//
// # What this does not change
//
// The published price still comes from a real observation of real sources, and
// `observedAt` still means what it always meant: when the inputs were read. It
// is simply no longer the same instant as when someone asked. Every freshness
// guard survives — package backing still rejects a stale reserve figure,
// checkWindow still bounds the spread across inputs, and UsdxPriceStore still
// enforces maxAge on arrival.
//
// What is added is one more bound, because a cache introduces a staleness mode
// the request-time design did not have: past config.MaxObservationAge, Latest
// fails closed rather than serving an old observation. A stale price under a
// fresh signature is the one outcome worse than no price at all.
//
// # Fail closed, never serve the previous good one
//
// A failed observation replaces the error, never the observation — the last
// good figures stay available until they age out. That is deliberate: a single
// failed round is normal (one RPC provider flapping), and dropping a
// still-recent price for it would stop the feed for no reason. What is not
// allowed is serving those figures indefinitely, which is what MaxObservationAge
// bounds.
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

// Resolver produces the configuration for the next observation.
//
// Called per observation rather than captured once, so a re-provisioned
// endpoint set or a rotated credential takes effect on the next round with no
// restart — the same reason runtimecfg.Resolve is cheap.
type Resolver interface {
	Resolve() (oracle.Config, error)
}

// Poller observes on a timer and keeps the most recent result.
type Poller struct {
	resolver Resolver
	signer   oracle.Signer

	// interval and maxAge are fields rather than direct constant reads so tests
	// can drive the loop without sleeping for minutes. Production always gets
	// the config values, via New.
	interval time.Duration
	maxAge   time.Duration

	// now is the clock, injectable for the same reason.
	now func() time.Time

	mu sync.RWMutex

	// last is the newest successful observation. Zero-valued until one lands.
	last     oracle.Observation
	haveLast bool

	// lastErr is why the most recent attempt failed, always a failure.Failure so
	// it is safe to publish. Cleared on success.
	lastErr error

	// counters are cumulative across the process lifetime, for GET /state.
	observations int
	failures     int
}

// New returns a Poller that has not observed yet.
func New(resolver Resolver, signer oracle.Signer) *Poller {
	return &Poller{
		resolver: resolver,
		signer:   signer,
		interval: config.ObservationInterval,
		maxAge:   config.MaxObservationAge,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Run observes immediately and then every interval, until ctx is cancelled.
//
// It never returns an error: a failed observation is recorded and the loop
// continues. The process must not exit because an upstream is down — that is
// the state the feed is designed to survive, visibly, by letting the on-chain
// timestamp age.
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

// observeOnce runs one observation and records the outcome.
func (p *Poller) observeOnce(ctx context.Context) {
	cfg, err := p.resolver.Resolve()
	if err != nil {
		// Unprovisioned is the normal state at boot and after every restart,
		// since credentials are memory-only. Not worth a log line each tick.
		p.record(oracle.Observation{}, err)
		return
	}

	obs, err := oracle.Observe(ctx, cfg)
	if err != nil {
		// The class is safe to log: failure vocabulary is compile-time constant
		// text, never an upstream's own error string.
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
		// The previous observation is deliberately left in place; it ages out
		// via maxAge rather than being dropped on the first bad round.
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

// Latest returns the newest observation, or a publishable failure.
//
// The staleness check is here rather than in the handler so that every consumer
// of a cached observation gets it — there is no path to the figures that skips
// the bound.
func (p *Poller) Latest() (oracle.Observation, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.haveLast {
		// Never observed. Report why the attempts failed if we know, so a
		// missing credential does not present as a mysterious absence.
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

// Report returns the latest observation signed for instructionID.
//
// This is the whole of what a GET_PRICE request costs: one signing round trip
// against the enclave, which is what fits inside tee-node's 2s budget.
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

// asFailure keeps anything that is not already vocabulary out of the public
// surfaces. Resolve and Observe return failure values today, but this package
// must not depend on that staying true — lastErr reaches ActionResult.Log.
func asFailure(err error) error {
	var f failure.Failure
	if errors.As(err, &f) {
		return f
	}
	return failure.Internal
}
