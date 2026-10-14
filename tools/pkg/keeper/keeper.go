package keeper

import (
	"context"
	"time"

	"fce-usdx-oracle/tools/pkg/support"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// LoopConfig adds the timer and the fleet policy to a cycle Config.
type LoopConfig struct {
	Config

	// How often a cycle starts. Match the ~10-minute Hex refresh; polling faster
	// costs gas per cycle and buys nothing.
	Interval time.Duration

	// ProxyOverride replaces the URL the registry publishes for every machine, for
	// a machine reachable at a different address than it registered. Only ever
	// right with a single machine: it sends every request to the same proxy.
	ProxyOverride string

	// Nil discards records, which is only ever right in a test.
	Emitter *Emitter

	// Bonds, when set, fetches the public bonds endpoint at the start of every
	// cycle and logs the answer. Nil records nothing.
	Bonds *BondsLog
}

// Run drives cycles until ctx is cancelled. Single-flight: cycles run
// sequentially, so two in-flight instructions can never take two nonces and
// submit out of order. A failed cycle is never fatal.
func Run(ctx context.Context, s *support.Support, cfg LoopConfig) error {
	logger.Infof("keeper %s starting: interval=%s poll=%s submit=%s",
		cfg.KeeperID, cfg.Interval, cfg.PollTimeout, cfg.SubmitTimeout)
	logger.Infof("  sender=%s store=%s", cfg.Sender.Hex(), cfg.Store.Hex())
	logger.Infof("  every cycle requests on-chain through requestFeedUpdate(feedId, teeIds), " +
		"naming the target machines explicitly")
	if cfg.picksTargets() {
		logger.Infof("  targets: every active machine at the feed's latest configuration, re-read each cycle")
	} else {
		logger.Infof("  targets: %s", hexList(cfg.TeeIDs))
	}
	if cfg.ProxyOverride != "" {
		logger.Infof("  every machine is addressed at %s, overriding its registered url",
			cfg.ProxyOverride)
	}

	// The fleet is read for the result poll, every addressed machine answering on
	// its own proxy, and for the targets when the keeper picks them. A few
	// eth_calls, so it costs nothing to keep current.
	var fl fleet
	fl.refresh(ctx, s, cfg)
	for _, m := range fl.machines {
		logger.Infof("  machine %s at %s", m.TeeID.Hex(), m.Proxy)
	}
	if len(fl.machines) == 0 {
		logger.Warnf("  no machine listed for this extension — falling back to %s for every cycle",
			cfg.Proxy)
	}

	for {
		started := time.Now()

		if ctx.Err() != nil {
			logger.Infof("keeper %s stopped", cfg.KeeperID)
			return nil
		}

		// Re-read each tick so machines added, restarted or paused mid-run are
		// picked up without a restart.
		fl.refresh(ctx, s, cfg)
		cfg.Bonds.Record(ctx)

		rec, err := RunCycle(ctx, s, cfg.cycleConfig(&fl))
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

// RunOnce performs a single cycle the way the loop would: the cron deployment,
// where the process is started fresh on every tick.
func RunOnce(ctx context.Context, s *support.Support, cfg LoopConfig) (Record, error) {
	var fl fleet
	fl.refresh(ctx, s, cfg)
	cfg.Bonds.Record(ctx)
	return RunCycle(ctx, s, cfg.cycleConfig(&fl))
}

// picksTargets reports whether the keeper chooses its own target machines,
// which it does whenever none were given: every cycle then asks the machines
// the registry lists as active that are at the feed's latest configuration. A
// fixed list goes stale the moment a machine restarts under a new id or is
// paused, and a request naming a paused machine reverts as a whole.
func (cfg LoopConfig) picksTargets() bool {
	return len(cfg.TeeIDs) == 0
}

// cycleConfig points one cycle at the whole fleet: each addressed machine files
// its result on its own proxy, so every proxy is a place to look. A keeper that
// picks its own targets requests from the fleet's requestable machines.
func (cfg LoopConfig) cycleConfig(fl *fleet) Config {
	c := cfg.Config
	c.ResultProxies = fl.proxies()
	// Falling through with the configured proxy is deliberate: an unreadable
	// registry must not stop the feed, and cfg.Proxy is the one machine we were
	// told about by hand.
	if cfg.picksTargets() {
		c.TeeIDs = fl.requestable
	}
	return c
}
