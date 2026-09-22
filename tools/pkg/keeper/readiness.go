package keeper

import (
	"context"
	"fmt"
	"sync"
	"time"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/tools/pkg/direct"
	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// readinessRequestV1 is the pinned one-byte envelope, matching
// protocol.ReadinessRequestV1. The registry rejects an empty message and the
// extension rejects anything else.
var readinessRequestV1 = []byte{0x01}

// QueryReadiness asks one machine for its signed readiness report: one direct
// action, no fee, no transaction. This is the only thing the keeper sends over
// the direct channel — a price is always an on-chain instruction. Verified, but
// still a self-report; see tools/pkg/readinessreport for what that settles.
func QueryReadiness(
	ctx context.Context, proxyURL, apiKey string, timeout time.Duration,
) (readinessreport.Report, error) {
	if proxyURL == "" {
		return readinessreport.Report{}, errors.New("readiness needs a proxy URL")
	}
	if apiKey == "" {
		// A proxy that wants a key answers 401, which reads like an outage rather
		// than a missing setting, so refuse before the request goes out.
		return readinessreport.Report{}, errors.New(
			"readiness needs an API key (-directApiKey or DIRECT_API_KEY)")
	}

	id, err := direct.Post(ctx, proxyURL, apiKey,
		protocol.OPTypeTeeOracle, protocol.OPCommandGetReadiness,
		readinessRequestV1)
	if err != nil {
		return readinessreport.Report{}, err
	}

	// Direct results are filed under "submit"; asking for the wrong tag 404s for
	// the whole poll window.
	res, err := PollResult(ctx, proxyURL, id, teetypes.Submit, timeout)
	if err != nil {
		return readinessreport.Report{}, err
	}

	if res.Status != direct.StatusSucceeded {
		// The extension answers even when unprovisioned, so a failure here is
		// the machine refusing the request rather than describing itself — a
		// wrong envelope, or a build without the command.
		return readinessreport.Report{}, errors.Errorf(
			"the machine refused the readiness request (status %d): %s", res.Status, res.Log)
	}

	report, err := readinessreport.Decode(res.Data)
	if err != nil {
		return readinessreport.Report{}, err
	}

	// Binding the answer to the request is the point of carrying the id: without
	// this check a captured report for another action would pass verification.
	if report.InstructionID != id {
		return readinessreport.Report{}, errors.Errorf(
			"readiness answers instruction %s, not the one asked (%s)",
			report.InstructionID.Hex(), id.Hex())
	}

	return report, nil
}

// wouldRefuse reports whether EVERY machine that could serve the request says it
// would refuse to sign, and why, and brings back each machine's own view of its
// sources for the cycle record. All of them, because the request goes to every
// listed machine and any one of them signing is enough. Every uncertainty
// answers false: "not known" is not "do not try".
//
// Every machine is asked, concurrently, rather than stopping at the first that
// would sign. A readiness query costs no fee and no gas, so asking them all
// costs one round trip's latency instead of the slowest sum — and it is the only
// way the record can say which machine saw which source fail. A machine that
// cannot be read is absent from the health map rather than reported as healthy.
func wouldRefuse(
	ctx context.Context, cfg Config,
) (skip bool, why string, health map[string]map[string]string) {
	if cfg.DirectAPIKey == "" {
		return false, "", nil
	}

	// Config.Proxy plus every fleet proxy, deduplicated — the same set the
	// result is later polled on, for the same reason.
	targets := cfg.pollTargets()
	if len(targets) == 0 {
		return false, "", nil
	}

	type answer struct {
		report readinessreport.Report
		err    error
	}
	answers := make([]answer, len(targets))
	var wg sync.WaitGroup
	for i, proxy := range targets {
		wg.Go(func() {
			report, err := QueryReadiness(ctx, proxy, cfg.DirectAPIKey, readinessPreflightTimeout)
			answers[i] = answer{report: report, err: err}
		})
	}
	wg.Wait()

	// Keyed by the machine that signed the report rather than by the proxy it
	// came from: a proxy is an address, the teeId is who answered, and two
	// proxies can front the same machine.
	health = map[string]map[string]string{}
	var unreadable, willing bool
	var first string
	for i, a := range answers {
		if a.err != nil {
			logger.Debugf("[%s] readiness preflight unavailable at %s, requesting anyway: %s",
				cfg.KeeperID, targets[i], a.err)
			unreadable = true
			continue
		}

		// Present with an empty map means the machine answered and has observed
		// nothing yet; absent means it did not answer at all.
		sources := a.report.Payload.SourceHealth
		if sources == nil {
			sources = map[string]string{}
		}
		health[a.report.Signer.Hex()] = sources

		if a.report.Payload.WouldSign {
			willing = true
			continue
		}
		if first == "" {
			first = fmt.Sprintf("%s reports observation %ds old, class %q, ready=%t",
				targets[i], a.report.Payload.ObservationAge, a.report.Payload.LastErrorClass,
				a.report.Payload.Readiness.Ready)
		}
	}
	if len(health) == 0 {
		health = nil
	}
	if unreadable || willing {
		return false, "", health
	}

	return true, fmt.Sprintf(
		"skipped the on-chain request: all %d reachable machine(s) report they would "+
			"not sign (%s) — no fee spent", len(targets), first), health
}

// readinessPreflightTimeout is short on purpose: this runs before the real work
// and must not eat the cycle's budget. Failing it simply means requesting.
const readinessPreflightTimeout = 15 * time.Second
