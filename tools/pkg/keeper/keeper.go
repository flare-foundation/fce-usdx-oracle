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
	if cfg.ProxyOverride != "" {
		logger.Infof("  every machine is addressed at %s, overriding its registered url",
			cfg.ProxyOverride)
	}

	// The fleet is read for the result poll: the instruction goes to a machine
	// the registry picks and only that machine's proxy holds the answer. Two
	// eth_calls, so it costs nothing to keep current.
	var fl fleet
	fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)
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

		// Re-read each tick so machines added or suspended mid-run are picked up
		// without a restart.
		fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)

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
	fl.refresh(ctx, s, cfg.Store, cfg.ProxyOverride)
	return RunCycle(ctx, s, cfg.cycleConfig(&fl))
}

// cycleConfig points one cycle at the whole fleet. It addresses nobody — the
// registry chooses which machine serves the instruction — so the result has to
// be looked for on every machine's proxy.
func (cfg LoopConfig) cycleConfig(fl *fleet) Config {
	c := cfg.Config
	c.ResultProxies = fl.proxies()
	// Falling through with the configured proxy is deliberate: an unreadable
	// registry must not stop the feed, and cfg.Proxy is the one machine we were
	// told about by hand.
	return c
}
