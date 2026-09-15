package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
	"github.com/pkg/errors"
)

// Declared here rather than read off the instruction sender: GET_READINESS is
// the one command with no on-chain counterpart, so there is nothing to drift
// from. These must match internal/config.
const (
	opTypeTeeOracle       = "TEE_ORACLE"
	opCommandGetReadiness = "GET_READINESS"
)

// readinessRequestV1 is the pinned one-byte envelope, matching
// config.ReadinessRequestV1. The registry rejects an empty message and the
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

	id, err := postDirectAction(ctx, proxyURL, apiKey,
		teeutils.ToHash(opTypeTeeOracle), teeutils.ToHash(opCommandGetReadiness),
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
// would refuse to sign, and why. All of them, because the request goes to every
// listed machine and any one of them signing is enough. Every uncertainty answers false: "not known" is not "do not try".
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

// postDirectAction enqueues one direct action on a proxy and returns the id it
// assigned. Takes the op pair loose rather than reading it off the instruction
// sender: GET_READINESS has no on-chain counterpart to read it from.
func postDirectAction(
	ctx context.Context, proxyURL, apiKey string,
	opType, opCommand common.Hash, message []byte,
) (common.Hash, error) {
	body, err := json.Marshal(map[string]string{
		"opType":    opType.Hex(),
		"opCommand": opCommand.Hex(),
		"message":   hexutil.Encode(message),
	})
	if err != nil {
		return common.Hash{}, errors.Errorf("encoding the direct instruction: %s", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(proxyURL, "/")+"/direct", bytes.NewReader(body))
	if err != nil {
		return common.Hash{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-API-Key", apiKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return common.Hash{}, errors.Errorf("posting to the proxy: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is the proxy's own error text; 401 here means the API key,
		// not the extension.
		return common.Hash{}, errors.Errorf("proxy returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(out)))
	}

	// The proxy echoes the action it enqueued, which is where the id comes from.
	var action teetypes.Action
	if err := json.Unmarshal(out, &action); err != nil {
		return common.Hash{}, errors.Errorf("decoding the proxy response: %s", err)
	}
	if action.Data.ID == (common.Hash{}) {
		return common.Hash{}, errors.New("the proxy returned no action id")
	}
	return action.Data.ID, nil
}
