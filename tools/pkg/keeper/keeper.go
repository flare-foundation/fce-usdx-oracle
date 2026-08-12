package keeper

import (
	"context"
	"time"

	"fce-usdx-oracle/tools/pkg/support"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// LoopConfig adds the timer to a cycle Config.
type LoopConfig struct {
	Config

	// How often a cycle starts. Match the ~10-minute Hex refresh; polling faster
	// costs a fee and gas per cycle and buys nothing.
	Interval time.Duration

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
	logger.Infof("  sender=%s store=%s proxy=%s",
		cfg.Sender.Hex(), cfg.Store.Hex(), cfg.Proxy)

	for {
		started := time.Now()

		rec, err := RunCycle(ctx, s, cfg.Config)
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
