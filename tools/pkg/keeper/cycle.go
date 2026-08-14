// Package keeper implements the USDX oracle keeper cycle and its timer.
//
// The keeper holds no authority — the store verifies the TEE signature against
// TeeMachineRegistry — so everything here is liveness and the worst a wrong
// invocation can do is waste gas. Nothing here may compute or adjust a price,
// and the signed body is relayed byte for byte.
//
// A cycle keeps no state: every partial cycle is harmless, and observedAt
// monotonicity makes duplicate submissions safe by construction. The timer
// keeps two pieces — whose turn it is in the rotation, and when the last
// on-chain instruction went out — and neither is correctness-bearing: any
// registered machine may serve any cycle, so a restart that resets them costs
// nothing beyond one extra on-chain fee.
package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
	"fce-usdx-oracle/tools/pkg/pricereport"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// Terminal action statuses. Anything else means still in flight.
const (
	statusFailed    uint8 = 0
	statusSucceeded uint8 = 1
)

// Config is everything one cycle needs.
type Config struct {
	KeeperID string

	Sender common.Address
	Store  common.Address

	// Proxy is the machine this cycle talks to: the direct request goes there,
	// and it is the first place the result is looked for.
	Proxy string

	// TeeID names the machine behind Proxy, when the caller knows it. Recorded
	// and logged only — the report's own signature is what decides which machine
	// actually answered.
	TeeID common.Address

	// ResultProxies are further proxies to look for the result on, for the
	// on-chain channel: getRandomTeeIds picks the machine, and a result is
	// retrievable only from the proxy of the machine that produced it, so
	// polling one URL of several finds nothing most of the time. Empty on the
	// direct channel, where the answer can only come from the machine the
	// request was sent to.
	ResultProxies []string

	// Direct raises the GET_PRICE action on the proxy instead of on-chain,
	// which saves the registry fee and one transaction per cycle.
	//
	// It costs nothing in trust. GET_PRICE grants no authority: it signs the
	// observation the enclave already made on its own timer, and submitPrice
	// verifies that signature against the machine registry however it was
	// requested. What it does cost is permissionlessness — the direct channel
	// needs the proxy's API key and network reach, so the on-chain path stays
	// as the one anybody can use by paying the fee.
	//
	// It also removes an ambiguity that only appears with several machines: an
	// on-chain request goes to a RANDOM machine via getRandomTeeIds, so the
	// keeper cannot know in advance which proxy will hold the result and has to
	// look on all of them — see ResultProxies. A direct request goes to the
	// machine whose proxy it was sent to, which is what makes addressing one
	// named machine, and so per-machine commissioning, possible at all.
	Direct       bool
	DirectAPIKey string

	// Both are spent out of the store's maxAge budget: observedAt is stamped in
	// the enclave before signing, so every second here counts against the
	// report's age.
	PollTimeout   time.Duration
	SubmitTimeout time.Duration

	// DryRun stops after preflight, sending no transaction.
	DryRun bool
}

// Channel names how the action was raised, for the cycle record.
func (c Config) Channel() string {
	if c.Direct {
		return "direct"
	}
	return "instruction"
}

// RunCycle performs one complete keeper cycle. It never exits the process on an
// expected failure: a single failed cycle is noise, not a reason to stop.
func RunCycle(ctx context.Context, s *support.Support, cfg Config) (Record, error) {
	report, rec, err := Collect(ctx, s, cfg)
	if err != nil {
		return rec, err
	}
	return Relay(ctx, s, cfg, rec, report)
}

// Collect is the first half of a cycle: raise the action, wait for a terminal
// result, and decode the report out of it. It sends no transaction on the direct
// channel, which is what lets a caller run it against several machines at once
// — see Sweep — without two cycles racing for the same nonce.
//
// The returned Record is finished on error and half-filled on success; pass it
// to Relay either way rather than building a second one, so a cycle emits one
// record whichever step ended it.
func Collect(ctx context.Context, s *support.Support, cfg Config) (pricereport.Report, Record, error) {
	rec := Record{
		KeeperID:       cfg.KeeperID,
		CycleStartedAt: time.Now().UTC(),
		Outcome:        OutcomeInternal,
		SubmitOutcome:  "not_attempted",
		Channel:        cfg.Channel(),
		Proxy:          cfg.Proxy,
	}
	if cfg.TeeID != (common.Address{}) {
		rec.TeeID = cfg.TeeID.Hex()
	}

	// A stuck transaction blocks every later one behind it. Recorded rather than
	// acted on: the fix is operational, not something to retry into.
	rec.PendingNonceGap = pendingNonceGap(ctx, s)

	fail := func(out Outcome, err error) (pricereport.Report, Record, error) {
		rec, err = finish(ctx, s, cfg, rec, out, err)
		return pricereport.Report{}, rec, err
	}

	// --- 1. trigger -----------------------------------------------------------
	//
	// Both channels yield an id and a submission tag to collect the result
	// under; nothing downstream of here depends on which was used.
	var (
		actionID common.Hash
		tag      teetypes.SubmissionTag
		err      error
	)
	if cfg.Direct {
		actionID, err = RequestDirect(ctx, s, cfg)
		// A direct action is enqueued under the "submit" tag. The proxy's
		// result endpoint defaults to "threshold", so asking for the wrong one
		// polls a 404 until the deadline and reports a timeout that looks like
		// a dropped instruction.
		tag = teetypes.Submit
		rec.RequestTxStatus = "not_applicable"
	} else {
		actionID, _, err = instrutils.RequestPriceUpdate(s, cfg.Sender)
		tag = teetypes.Threshold
		if err == nil {
			rec.RequestTxStatus = "mined"
		} else {
			rec.RequestTxStatus = "failed"
		}
	}
	if err != nil {
		return fail(OutcomeRequestFailed, err)
	}
	rec.InstructionID = actionID.Hex()
	logger.Infof("[%s] %s action %s%s", cfg.KeeperID, cfg.Channel(), actionID.Hex(), targetSuffix(cfg))

	// --- 2. collect -----------------------------------------------------------
	targets := cfg.pollTargets()
	result, from, err := PollResultAny(ctx, targets, actionID, tag, cfg.PollTimeout)
	if err != nil {
		rec.ResultStatus = "timeout"
		rec.SourceHealth = sourceHealth(cfg.Proxy)
		return fail(OutcomeNoResult, err)
	}

	// The answering proxy, which on the on-chain channel is the one the registry
	// happened to route to rather than the one configured.
	rec.Proxy = from

	// Recorded either way: on failure it is what says WHICH source broke.
	rec.SourceHealth = sourceHealth(from)

	if result.Status != statusSucceeded {
		// A status-0 result is the extension declining to publish; Log is a class
		// from its closed vocabulary. Relaying anything would invent a price.
		rec.ResultStatus = "0"
		rec.ErrorClass = result.Log
		return fail(OutcomeExtensionFailed, errors.Errorf(
			"the extension published nothing (status %d): %s", result.Status, result.Log))
	}
	rec.ResultStatus = "1"

	report, err := pricereport.Decode(result.Data)
	if err != nil {
		return fail(OutcomeInternal, errors.Errorf("decoding the action result: %s", err))
	}
	rec.Price = report.Price.String()
	rec.Decimals = report.Decimals
	rec.ObservedAt = report.ObservedAt

	// The signer is the machine that actually produced this report, which on the
	// on-chain channel is the only thing that says which one did.
	if teeID, err := report.Signer(); err == nil {
		rec.TeeID = teeID.Hex()
	}

	return report, rec, nil
}

// Relay is the second half of a cycle: check the report against every store
// guard and submit it. Split from Collect so a caller holding reports from
// several machines can order the submissions — observedAt must strictly
// increase, so the order is what decides how many of them land.
func Relay(
	ctx context.Context, s *support.Support, cfg Config, rec Record, report pricereport.Report,
) (Record, error) {
	if err := Preflight(s, cfg.Store, report); err != nil {
		if rj, ok := AsRejection(err); ok {
			rec.SubmitOutcome = rj.Guard
			return finish(ctx, s, cfg, rec, classifyRejection(rj, report), err)
		}
		return finish(ctx, s, cfg, rec, OutcomeInternal, err)
	}

	if cfg.DryRun {
		rec.SubmitOutcome = "dry_run"
		return finish(ctx, s, cfg, rec, OutcomeRejected, nil)
	}

	tx, err := submit(ctx, s, cfg, report)
	if err != nil {
		rec.SubmitOutcome = "send_failed"
		return finish(ctx, s, cfg, rec, OutcomeRejected, err)
	}
	rec.SubmitTx = tx.Hash().Hex()

	if err := waitMined(ctx, s, tx, cfg.SubmitTimeout); err != nil {
		// Not chased. The next cycle supersedes this one, and a replacement
		// landing after maxAge would revert TooOld anyway.
		rec.SubmitOutcome = "not_mined"
		return finish(ctx, s, cfg, rec, OutcomeNotMined, err)
	}

	rec.SubmitOutcome = "accepted"
	return finish(ctx, s, cfg, rec, OutcomeAccepted, nil)
}

// finish stamps the terminal fields every ending shares.
func finish(
	ctx context.Context, s *support.Support, cfg Config, rec Record, out Outcome, err error,
) (Record, error) {
	rec.Outcome = out
	rec.CycleEndedAt = time.Now().UTC()
	if err != nil {
		rec.Detail = err.Error()
	}
	rec.StoreObservedAtAfter = storeObservedAt(ctx, s, cfg.Store)
	return rec, err
}

// pollTargets is where the result might be, most likely first and deduplicated.
func (c Config) pollTargets() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 1+len(c.ResultProxies))

	for _, p := range append([]string{c.Proxy}, c.ResultProxies...) {
		p = strings.TrimRight(strings.TrimSpace(p), "/")
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// targetSuffix names the machine a cycle addressed, when it addressed one.
func targetSuffix(cfg Config) string {
	if cfg.TeeID == (common.Address{}) {
		return ""
	}
	return " to " + cfg.TeeID.Hex()
}

// classifyRejection separates the one benign store guard from the rest.
// NotNewer fires both when a keeper loses a race and when its own report was
// stale; comparing against the store's stamp tells those apart.
func classifyRejection(rj *Rejection, r pricereport.Report) Outcome {
	if rj.Guard != "NotNewer" {
		return OutcomeRejected
	}
	if rj.StoreObservedAt >= r.ObservedAt {
		return OutcomeRaceLost
	}
	return OutcomeStaleSubmission
}

func submit(ctx context.Context, s *support.Support, cfg Config, r pricereport.Report) (*types.Transaction, error) {
	store, err := usdxstore.NewUsdxPriceStore(cfg.Store, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return nil, err
	}
	opts.Context = ctx

	tx, err := store.SubmitPrice(opts, r.Body, r.Signature)
	if err != nil {
		return nil, errors.Errorf("submitPrice rejected before broadcast: %s", err)
	}
	return tx, nil
}

func waitMined(ctx context.Context, s *support.Support, tx *types.Transaction, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		return errors.Errorf("tx %s was not mined within %s: %s", tx.Hash().Hex(), timeout, err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		// Preflight passed, so this is a race that landed between the check and
		// the send, or a guard that changed underneath us.
		return errors.Errorf("tx %s reverted despite passing preflight — re-run submit-price "+
			"with -dry-run to see which guard rejects it", tx.Hash().Hex())
	}
	return nil
}

// RequestDirect raises a GET_PRICE action on the proxy and returns the action id
// the proxy assigned it.
//
// The op pair and request envelope are read off the deployed sender rather than
// restated here, so the two channels cannot drift apart — see
// instrutils.ReadGetPriceRequest.
func RequestDirect(ctx context.Context, s *support.Support, cfg Config) (common.Hash, error) {
	if cfg.Proxy == "" {
		return common.Hash{}, errors.New("direct mode needs a proxy URL")
	}
	if cfg.DirectAPIKey == "" {
		// The proxy can be configured with api_key_optional, but a keeper that
		// silently sends no key against a proxy that wants one gets a 401 that
		// reads like an outage. Require it here and let the operator opt out
		// deliberately by not using direct mode.
		return common.Hash{}, errors.New("direct mode needs an API key (-directApiKey or DIRECT_API_KEY)")
	}

	req, err := instrutils.ReadGetPriceRequest(s, cfg.Sender)
	if err != nil {
		return common.Hash{}, err
	}

	return postDirectAction(ctx, cfg.Proxy, cfg.DirectAPIKey, req)
}

// postDirectAction is the HTTP half of RequestDirect, split out so it can be
// exercised without a chain to read the op constants from.
func postDirectAction(
	ctx context.Context, proxyURL, apiKey string, req instrutils.GetPriceRequest,
) (common.Hash, error) {
	body, err := json.Marshal(map[string]string{
		"opType":    req.OPType.Hex(),
		"opCommand": req.OPCommand.Hex(),
		"message":   hexutil.Encode(req.Message),
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
	defer resp.Body.Close()

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

// PollResult waits for a terminal action result on one proxy.
func PollResult(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, error) {
	res, _, err := PollResultAny(ctx, []string{proxyURL}, id, tag, timeout)
	return res, err
}

// PollResultAny waits for a terminal action result on any of several proxies and
// reports which one answered. The deadline is what makes the cycle's duration
// knowable, and has to stay inside the store's maxAge.
//
// Several proxies because an on-chain instruction is routed to a machine the
// registry picks at random, and only that machine's proxy ever holds the result:
// asking just one of a fleet of N finds nothing (N-1)/N of the time, and the
// symptom is a poll that expires looking exactly like a dropped instruction. On
// the direct channel there is only ever one target.
//
// The tag is not optional: an instruction's result is filed under "threshold"
// and a direct action's under "submit", and asking for the wrong one returns 404
// forever rather than an error naming the mismatch.
func PollResultAny(
	ctx context.Context, proxies []string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, string, error) {
	if len(proxies) == 0 {
		return teetypes.ActionResult{}, "", errors.New("no proxy to poll for the result")
	}

	deadline := time.Now().Add(timeout)
	backoff := time.Second

	for attempt := 1; ; attempt++ {
		// Sequential across proxies: a fleet is a handful of machines, and one
		// slow proxy delaying a round costs less than the concurrency would.
		for _, proxy := range proxies {
			res, ok, err := fetchResult(proxy, id, tag)
			switch {
			case err != nil:
				logger.Warnf("attempt %d (%s): %s", attempt, proxy, err)
			case ok:
				return res, proxy, nil
			default:
				logger.Infof("attempt %d (%s): pending (status %d)", attempt, proxy, res.Status)
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return teetypes.ActionResult{}, "", errors.Errorf(
				"no terminal result for %s within %s on any of %d proxy/proxies — FCC is "+
					"fire-and-forget, so a dropped instruction is an expected outcome, not an "+
					"exceptional one", id.Hex(), timeout, len(proxies))
		}
		if backoff > remaining {
			backoff = remaining
		}

		select {
		case <-ctx.Done():
			return teetypes.ActionResult{}, "", ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 16*time.Second {
			backoff *= 2
		}
	}
}

// fetchResult returns the result and whether its status is terminal.
func fetchResult(
	proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
) (teetypes.ActionResult, bool, error) {
	url := strings.TrimRight(proxyURL, "/") +
		"/action/result/" + id.Hex() + "?submissionTag=" + string(tag)

	resp, err := http.Get(url)
	if err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("querying the proxy: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 404 until the TEE has answered — the normal early state, not a fault.
		return teetypes.ActionResult{}, false, errors.Errorf("proxy returned %d", resp.StatusCode)
	}

	var body teetypes.ActionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("decoding the proxy response: %s", err)
	}

	status := body.Result.Status
	return body.Result, status == statusSucceeded || status == statusFailed, nil
}

// sourceHealth reads the extension's per-source health map off the proxy.
// Best-effort: a nil map means the proxy could not be read, never "the sources
// are fine". The map is only written when an observation runs, so stale "ok"
// values persist if requests stop — alert on on-chain feed age instead.
func sourceHealth(proxyURL string) map[string]string {
	resp, err := http.Get(strings.TrimRight(proxyURL, "/") + "/info")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	// TeeState.State carries the extension's GET /state body as hex bytes.
	var info struct {
		TeeInfo struct {
			State struct {
				State hexutil.Bytes `json:"state"`
			} `json:"state"`
		} `json:"teeInfo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil
	}

	var state struct {
		State struct {
			SourceHealth map[string]string `json:"sourceHealth"`
		} `json:"state"`
	}
	if err := json.Unmarshal(info.TeeInfo.State.State, &state); err != nil {
		return nil
	}
	return state.State.SourceHealth
}

// storeObservedAt reads the store's current stamp. Best-effort: it is a
// diagnostic on the record, not a control input.
func storeObservedAt(ctx context.Context, s *support.Support, storeAddr common.Address) uint64 {
	store, err := usdxstore.NewUsdxPriceStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return 0
	}
	observedAt, err := store.ObservedAt(&bind.CallOpts{Context: ctx})
	if err != nil {
		return 0
	}
	return observedAt
}

// pendingNonceGap is how many of this keeper's transactions are broadcast but
// not yet mined.
func pendingNonceGap(ctx context.Context, s *support.Support) uint64 {
	from := crypto.PubkeyToAddress(s.Prv.PublicKey)

	mined, err := s.ChainClient.NonceAt(ctx, from, nil)
	if err != nil {
		return 0
	}
	pending, err := s.ChainClient.PendingNonceAt(ctx, from)
	if err != nil {
		return 0
	}
	if pending > mined {
		return pending - mined
	}
	return 0
}
