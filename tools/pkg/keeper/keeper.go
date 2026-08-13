package keeper

import (
	"context"
	"math/rand/v2"
	"time"

	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// LoopConfig adds the timer, the channel schedule and the fleet policy to a
// cycle Config.
type LoopConfig struct {
	Config

	// How often a cycle starts. Match the ~10-minute Hex refresh; polling faster
	// costs gas per cycle and buys nothing.
	Interval time.Duration

	// OnChainEvery is how often a cycle raises GET_PRICE on-chain instead of
	// directly. Zero means never.
	//
	// The two channels produce the same signed report — GET_PRICE grants no
	// authority, and the store checks the signature the same way however it was
	// requested — so the only thing the on-chain path buys is that it is the
	// permissionless one: anybody can pay the registry fee and get a price
	// published, without the proxy's API key or network reach. Exercising it on
	// a slow cadence keeps that path proven to work while paying the fee a
	// handful of times a day instead of on every cycle.
	OnChainEvery time.Duration

	// Sweep lands a price from every registered machine before the first timed
	// cycle. Off makes sense for a cron-driven keeper, where every invocation
	// would otherwise sweep the whole fleet.
	Sweep bool

	// ProxyOverride replaces the URL the registry publishes for every machine.
	// Only ever right with a single machine — it sends every request to the same
	// proxy — and it exists for the case where the keeper reaches a machine on a
	// different address than the one it registered.
	ProxyOverride string

	// How many times the sweep re-tries a machine that did not land a price, and
	// how long it waits first. The wait wants to be at least one of the
	// extension's observation intervals: a machine rejected as NotNewer needs a
	// newer observation before a retry can go any differently.
	SweepAttempts   int
	SweepRetryDelay time.Duration

	// Nil discards records, which is only ever right in a test.
	Emitter *Emitter
}

// Run drives cycles until ctx is cancelled.
//
// Single-flight: cycles run sequentially, so two in-flight instructions can
// never consume two nonces and submit out of order. A cycle that overruns
// Interval delays the next tick rather than running beside it.
//
// A failed cycle is never fatal — deciding when a run of failures becomes
// signal is the alerting layer's job.
func Run(ctx context.Context, s *support.Support, cfg LoopConfig) error {
	logger.Infof("keeper %s starting: interval=%s poll=%s submit=%s",
		cfg.KeeperID, cfg.Interval, cfg.PollTimeout, cfg.SubmitTimeout)
	logger.Infof("  sender=%s store=%s", cfg.Sender.Hex(), cfg.Store.Hex())
	switch {
	case !cfg.Direct:
		logger.Infof("  channel=instruction on every cycle — the direct channel is off")
	case cfg.OnChainEvery > 0:
		logger.Infof("  channel=direct, rotating over the fleet, with an on-chain instruction every %s",
			cfg.OnChainEvery)
	default:
		logger.Infof("  channel=direct, rotating over the fleet — no cycle ever goes on-chain")
	}
	if cfg.ProxyOverride != "" {
		logger.Infof("  every machine is addressed at %s, overriding its registered url",
			cfg.ProxyOverride)
	}

	// The fleet is read whether or not the direct channel is on: an on-chain
	// cycle needs every proxy to look for its result on, and that read costs
	// nothing but two eth_calls.
	var fl fleet
	fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)
	for _, m := range fl.machines {
		logger.Infof("  machine %s at %s", m.TeeID.Hex(), m.Proxy)
	}
	if len(fl.machines) == 0 {
		logger.Warnf("  no machine listed for this extension — falling back to %s for every cycle",
			cfg.Proxy)
	}

	if cfg.Direct && cfg.Sweep {
		Sweep(ctx, s, cfg, fl.machines)
	}

	// The clock starts now, so the first cycle after the sweep is a direct one
	// and the fee is not paid at every restart. A keeper that restarts more
	// often than OnChainEvery therefore never exercises the on-chain path —
	// which is the right trade: a crash loop must not also burn fees.
	lastOnChain := time.Now()

	for {
		started := time.Now()

		if ctx.Err() != nil {
			logger.Infof("keeper %s stopped", cfg.KeeperID)
			return nil
		}

		onChain := !cfg.Direct || (cfg.OnChainEvery > 0 && started.Sub(lastOnChain) >= cfg.OnChainEvery)

		// Re-read each tick so machines added or suspended mid-run are picked up
		// without a restart.
		fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)

		cycleCfg := cfg.cycleConfig(&fl, onChain)
		if onChain {
			// Stamped before the cycle, not after it succeeds: a failing
			// on-chain request must not be retried at every tick, paying the
			// registry fee each time for the same failure.
			lastOnChain = started
		}

		rec, err := RunCycle(ctx, s, cycleCfg)
		cfg.Emitter.Emit(rec)

		switch {
		case rec.Outcome.Advanced():
			logger.Infof("[%s] %s — store now at observedAt=%d",
				cfg.KeeperID, rec.Outcome, rec.StoreObservedAtAfter)
		case err != nil:
			logger.Warnf("[%s] %s: %s", cfg.KeeperID, rec.Outcome, err)
		default:
			logger.Warnf("[%s] %s", cfg.KeeperID, rec.Outcome)
		}

		if rec.PendingNonceGap > 0 {
			// A stuck transaction blocks every later one behind its nonce, so
			// this degrades into a total outage rather than a bad cycle.
			logger.Warnf("[%s] %d transaction(s) broadcast but not mined — later cycles queue behind them",
				cfg.KeeperID, rec.PendingNonceGap)
		}

		wait := cfg.Interval - time.Since(started)
		if wait <= 0 {
			logger.Warnf("[%s] cycle took %s, longer than the %s interval — skipping the missed tick",
				cfg.KeeperID, time.Since(started).Round(time.Second), cfg.Interval)
			wait = 0
		}

		select {
		case <-ctx.Done():
			logger.Infof("keeper %s stopped", cfg.KeeperID)
			return nil
		case <-time.After(wait):
		}
	}
}

// RunOnce performs a single cycle the way the loop would, and returns its
// record. It is the cron-driven deployment: the process is what repeats, so
// there is no rotation state to carry and the machine is picked at random, which
// spreads the same coverage over successive runs.
//
// The channel is whatever -direct says. OnChainEvery cannot apply — nothing here
// knows when the last run was — so a cron deployment that wants the
// permissionless path exercised schedules a second, rarer entry with the direct
// channel off.
func RunOnce(ctx context.Context, s *support.Support, cfg LoopConfig) (Record, error) {
	var fl fleet
	fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)
	if len(fl.machines) > 0 {
		fl.next = rand.IntN(len(fl.machines))
	}

	if cfg.Direct && cfg.Sweep {
		Sweep(ctx, s, cfg, fl.machines)
	}

	return RunCycle(ctx, s, cfg.cycleConfig(&fl, !cfg.Direct))
}

// cycleConfig picks the channel and the machine for one cycle.
//
// A direct cycle addresses the next machine in the rotation, so every machine
// keeps landing prices and keeps its commissioning current, and one broken
// machine costs one cycle in N rather than all of them. An on-chain cycle
// addresses nobody — the registry routes it — so it polls the whole fleet for
// the result instead.
func (cfg LoopConfig) cycleConfig(fl *fleet, onChain bool) Config {
	c := cfg.Config

	if onChain {
		c.Direct = false
		c.ResultProxies = fl.proxies()
		c.TeeID = common.Address{}
		return c
	}

	c.Direct = true
	c.ResultProxies = nil
	if m, ok := fl.take(); ok {
		c.Proxy = m.Proxy
		c.TeeID = m.TeeID
	}
	// Falling through with the configured proxy is deliberate: an unreadable
	// registry must not stop the feed, and cfg.Proxy is the one machine we were
	// told about by hand.
	return c
}
