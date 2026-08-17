package keeper

import (
	"context"
	"fmt"
	"time"

	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/pkg/errors"
)

// Declared here, not read off the sender the way GET_PRICE is: readiness exists
// only on the direct channel and has no contract counterpart to drift from.
// These must match go/internal/config.
const (
	opTypeUsdxOracle      = "USDX_ORACLE"
	opCommandGetReadiness = "GET_READINESS"
)

// readinessRequestV1 is the pinned one-byte envelope, matching
// config.ReadinessRequestV1. The registry rejects an empty message and the
// extension rejects anything else.
var readinessRequestV1 = []byte{0x01}

// QueryReadiness asks one machine for its signed readiness report: one direct
// action, no fee, no transaction. Verified, but still a self-report — see
// tools/pkg/readinessreport for what that does and does not settle.
func QueryReadiness(
	ctx context.Context, proxyURL, apiKey string, timeout time.Duration,
) (readinessreport.Report, error) {
	if proxyURL == "" {
		return readinessreport.Report{}, errors.New("readiness needs a proxy URL")
	}
	if apiKey == "" {
		// Same reasoning as RequestDirect: a proxy that wants a key answers 401,
		// which reads like an outage rather than a missing setting.
		return readinessreport.Report{}, errors.New(
			"readiness needs an API key (-directApiKey or DIRECT_API_KEY)")
	}

	id, err := postDirectAction(ctx, proxyURL, apiKey,
		teeutils.ToHash(opTypeUsdxOracle), teeutils.ToHash(opCommandGetReadiness),
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

	if res.Status != statusSucceeded {
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
// would refuse to sign, and why. All of them, because getRandomTeeIds picks the
// machine. Every uncertainty answers false: "not known" is not "do not try".
func wouldRefuse(ctx context.Context, cfg Config) (bool, string) {
	if cfg.DirectAPIKey == "" {
		return false, ""
	}

	// Config.Proxy plus every fleet proxy, deduplicated — the same set the
	// result is later polled on, for the same reason.
	targets := cfg.pollTargets()
	if len(targets) == 0 {
		return false, ""
	}

	// Returns on the first machine that would sign or cannot be read, so the healthy
	// path costs one round trip. Only a unanimously refusing fleet pays for them all.
	var first string
	for _, proxy := range targets {
		report, err := QueryReadiness(ctx, proxy, cfg.DirectAPIKey, readinessPreflightTimeout)
		if err != nil {
			logger.Debugf("[%s] readiness preflight unavailable at %s, requesting anyway: %s",
				cfg.KeeperID, proxy, err)
			return false, ""
		}
		if report.Payload.WouldSign {
			return false, ""
		}
		if first == "" {
			first = fmt.Sprintf("%s reports observation %ds old, class %q, ready=%t",
				proxy, report.Payload.ObservationAge, report.Payload.LastErrorClass,
				report.Payload.Readiness.Ready)
		}
	}

	return true, fmt.Sprintf(
		"skipped the on-chain request: all %d reachable machine(s) report they would "+
			"not sign (%s) — no fee spent", len(targets), first)
}

// readinessPreflightTimeout is short on purpose: this runs before the real work
// and must not eat the cycle's budget. Failing it simply means requesting.
const readinessPreflightTimeout = 15 * time.Second

// QueryReadinessSigner is QueryReadiness with the signer checked against the
// machine the caller believes it is talking to. Without it a proxy could serve a
// valid report signed by a different enclave.
func QueryReadinessSigner(
	ctx context.Context, proxyURL, apiKey string, want common.Address, timeout time.Duration,
) (readinessreport.Report, error) {
	report, err := QueryReadiness(ctx, proxyURL, apiKey, timeout)
	if err != nil {
		return readinessreport.Report{}, err
	}
	if want != (common.Address{}) && report.Signer != want {
		return readinessreport.Report{}, errors.Errorf(
			"readiness is signed by %s, not the expected %s",
			report.Signer.Hex(), want.Hex())
	}
	return report, nil
}
