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

// The op pair for GET_READINESS is declared here, not read off the deployed
// sender the way GET_PRICE is. That asymmetry follows the command's: a price is
// requestable on-chain so its constants live in the contract and both channels
// read the same source, while readiness exists only on the direct channel and
// has no contract counterpart to drift from. These must match
// go/internal/config.
const (
	opTypeUsdxOracle      = "USDX_ORACLE"
	opCommandGetReadiness = "GET_READINESS"
)

// readinessRequestV1 is the pinned one-byte envelope, matching
// config.ReadinessRequestV1. The registry rejects an empty message and the
// extension rejects anything else.
var readinessRequestV1 = []byte{0x01}

// QueryReadiness asks one machine for its signed readiness report.
//
// Round trip over the direct channel: post the action, wait for the result,
// decode and check the signature. No fee and no transaction — the cost is one
// action, which is why it is affordable to ask before deciding whether to pay
// for anything else.
//
// The returned report is verified but still a self-report; see
// tools/pkg/readinessreport on what that does and does not settle.
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

// wouldRefuse reports whether the addressed machine says it would refuse to
// sign, and why. Used only before a paid request.
//
// It answers false for every uncertainty — no proxy, no key, a build without
// the command, an unreachable machine. The question being asked is "can this
// fee be saved", and the honest answer when readiness is unavailable is "not
// known", which must not become "do not try": that would turn every machine
// that cannot answer into one the keeper refuses to use.
func wouldRefuse(ctx context.Context, cfg Config) (bool, string) {
	if cfg.Proxy == "" || cfg.DirectAPIKey == "" {
		return false, ""
	}

	report, err := QueryReadiness(ctx, cfg.Proxy, cfg.DirectAPIKey, readinessPreflightTimeout)
	if err != nil {
		logger.Debugf("[%s] readiness preflight unavailable, requesting anyway: %s", cfg.KeeperID, err)
		return false, ""
	}
	if report.Payload.WouldSign {
		return false, ""
	}

	return true, fmt.Sprintf(
		"skipped the on-chain request: the machine reports it would not sign "+
			"(observation %ds old, class %q, ready=%t) — no fee spent",
		report.Payload.ObservationAge, report.Payload.LastErrorClass, report.Payload.Readiness.Ready)
}

// readinessPreflightTimeout is short on purpose: this runs before the real work
// and must not eat the cycle's budget. Failing it simply means requesting.
const readinessPreflightTimeout = 15 * time.Second

// QueryReadinessSigner is QueryReadiness with the signer checked against the
// machine the caller believes it is talking to. Use it wherever the answer will
// inform a decision about a specific machine: without it a proxy could serve a
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
