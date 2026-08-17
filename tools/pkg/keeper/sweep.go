package keeper

import (
	"context"
	"sort"
	"sync"
	"time"

	"fce-usdx-oracle/tools/pkg/pricereport"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// Sweep lands one price from every machine over the direct channel before the timer
// starts. This is commissioning — landing a price is what makes isProvenConfigured
// true — and it runs at every start, because a re-pushed config decommissions silently.
func Sweep(ctx context.Context, s *support.Support, cfg LoopConfig, machines []Machine) []Record {
	if len(machines) == 0 {
		logger.Warnf("[%s] nothing to sweep: no reachable machine is registered to this extension",
			cfg.KeeperID)
		return nil
	}

	attempts := cfg.SweepAttempts
	if attempts < 1 {
		attempts = 1
	}
	if cfg.DryRun {
		// Nothing can be commissioned without a transaction, so retrying would
		// only repeat the same preflight.
		attempts = 1
	}

	logger.Infof("[%s] sweeping %d machine(s) so each lands a price and is commissioned on-chain "+
		"— up to %d attempt(s), %s apart, before the first timed cycle",
		cfg.KeeperID, len(machines), attempts, cfg.SweepRetryDelay)

	var (
		out     []Record
		pending = machines
	)
	for attempt := 1; attempt <= attempts && len(pending) > 0; attempt++ {
		if attempt > 1 {
			// Machines rejected as NotNewer need a newer observation before a
			// retry can differ, and each stamps one every ObservationInterval.
			logger.Infof("[%s] %d machine(s) did not land a price; retrying in %s (attempt %d/%d)",
				cfg.KeeperID, len(pending), cfg.SweepRetryDelay, attempt, attempts)
			select {
			case <-ctx.Done():
				return out
			case <-time.After(cfg.SweepRetryDelay):
			}
		}

		records := sweepPass(ctx, s, cfg, pending)

		var failed []Machine
		for i, rec := range records {
			cfg.Emitter.Emit(rec)
			out = append(out, rec)

			// Only this machine's own submission being accepted commissions it.
			// race_lost means someone else's report landed, which advances the
			// feed but proves nothing about this machine.
			if rec.Outcome == OutcomeAccepted {
				logger.Infof("[%s] commissioned %s", cfg.KeeperID, pending[i].TeeID.Hex())
				continue
			}
			logger.Warnf("[%s] %s did not land a price: %s (%s)",
				cfg.KeeperID, pending[i].TeeID.Hex(), rec.Outcome, rec.SubmitOutcome)
			failed = append(failed, pending[i])
		}
		pending = failed

		if ctx.Err() != nil {
			return out
		}
	}

	if len(pending) > 0 && !cfg.DryRun {
		// Not fatal: the machines that did land prices keep the feed alive, and
		// the rotation will try the rest again on their turn.
		for _, m := range pending {
			logger.Warnf("[%s] %s is still not commissioned — check it in fleet-status",
				cfg.KeeperID, m.TeeID.Hex())
		}
	}
	return out
}

// swept is one machine's half-finished cycle, held between the two phases.
type swept struct {
	machine Machine
	cfg     Config
	rec     Record
	report  pricereport.Report
	ok      bool
}

// sweepPass collects from every machine, then submits in ascending observedAt order
// — see submissionOrder. Collection is concurrent, submission strictly sequential:
// two in-flight submissions would take two nonces and could land out of order.
func sweepPass(ctx context.Context, s *support.Support, cfg LoopConfig, machines []Machine) []Record {
	held := make([]swept, len(machines))

	var wg sync.WaitGroup
	for i, m := range machines {
		machineCfg := cfg.Config
		machineCfg.Proxy = m.Proxy
		machineCfg.TeeID = m.TeeID
		machineCfg.Direct = true
		// A direct action's result can only come from the machine it was sent
		// to, so a fan-out here would poll every other machine for nothing.
		machineCfg.ResultProxies = nil

		held[i] = swept{machine: m, cfg: machineCfg}

		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			report, rec, err := Collect(ctx, s, held[i].cfg)
			rec.Phase = "bootstrap"
			held[i].rec = rec
			held[i].report = report
			held[i].ok = err == nil
		}(i)
	}
	wg.Wait()

	order := submissionOrder(held)
	for n, i := range order {
		if err := ctx.Err(); err != nil {
			// Cancelled mid-sweep. The reports left unsubmitted still need a
			// terminal record, or they emit as half-filled cycles that read like
			// an internal error rather than an interrupted run.
			for _, j := range order[n:] {
				held[j].rec, _ = finish(ctx, s, held[j].cfg, held[j].rec, OutcomeInternal, err)
				held[j].rec.Phase = "bootstrap"
			}
			break
		}

		rec, _ := Relay(ctx, s, held[i].cfg, held[i].rec, held[i].report)
		rec.Phase = "bootstrap"
		held[i].rec = rec
	}

	out := make([]Record, len(held))
	for i := range held {
		out[i] = held[i].rec
	}
	return out
}

// submissionOrder is oldest observation first, skipping machines that produced
// none. Ascending because observedAt must strictly increase: newest-first would
// land one report and reject the rest as NotNewer, one machine per pass forever.
func submissionOrder(held []swept) []int {
	order := make([]int, 0, len(held))
	for i := range held {
		if held[i].ok {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		return held[order[a]].report.ObservedAt < held[order[b]].report.ObservedAt
	})
	return order
}
