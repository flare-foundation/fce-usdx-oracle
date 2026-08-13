// Package keeper implements the USDX oracle keeper cycle and its timer.
//
// The keeper holds no authority — the store verifies the TEE signature against
// TeeMachineRegistry — so everything here is liveness and the worst a wrong
// invocation can do is waste gas. Nothing here may compute or adjust a price,
// and the signed body is relayed byte for byte.
//
// No state is kept across cycles: every partial cycle is harmless, and
// observedAt monotonicity makes duplicate submissions safe by construction.
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
	Proxy  string

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
	// on-chain request goes to a RANDOM machine via getRandomTeeIds, and the
	// result is retrievable only from that machine's own proxy, so a keeper
	// polling one URL misses most of them. A direct request goes to the machine
	// whose proxy it was sent to, which is what makes per-machine commissioning
	// possible at all.
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
	rec := Record{
		KeeperID:       cfg.KeeperID,
		CycleStartedAt: time.Now().UTC(),
		Outcome:        OutcomeInternal,
		SubmitOutcome:  "not_attempted",
		Channel:        cfg.Channel(),
	}

	// A stuck transaction blocks every later one behind it. Recorded rather than
	// acted on: the fix is operational, not something to retry into.
	rec.PendingNonceGap = pendingNonceGap(ctx, s)

	finish := func(out Outcome, err error) (Record, error) {
		rec.Outcome = out
		rec.CycleEndedAt = time.Now().UTC()
		if err != nil {
			rec.Detail = err.Error()
		}
		rec.StoreObservedAtAfter = storeObservedAt(ctx, s, cfg.Store)
		return rec, err
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
		return finish(OutcomeRequestFailed, err)
	}
	rec.InstructionID = actionID.Hex()
	logger.Infof("[%s] %s action %s", cfg.KeeperID, cfg.Channel(), actionID.Hex())

	// --- 2. collect -----------------------------------------------------------
	result, err := PollResult(ctx, cfg.Proxy, actionID, tag, cfg.PollTimeout)
	if err != nil {
		rec.ResultStatus = "timeout"
		rec.SourceHealth = sourceHealth(cfg.Proxy)
		return finish(OutcomeNoResult, err)
	}

	// Recorded either way: on failure it is what says WHICH source broke.
	rec.SourceHealth = sourceHealth(cfg.Proxy)

	if result.Status != statusSucceeded {
		// A status-0 result is the extension declining to publish; Log is a class
		// from its closed vocabulary. Relaying anything would invent a price.
		rec.ResultStatus = "0"
		rec.ErrorClass = result.Log
		return finish(OutcomeExtensionFailed, errors.Errorf(
			"the extension published nothing (status %d): %s", result.Status, result.Log))
	}
	rec.ResultStatus = "1"

	// --- 3. relay -------------------------------------------------------------
	report, err := pricereport.Decode(result.Data)
	if err != nil {
		return finish(OutcomeInternal, errors.Errorf("decoding the action result: %s", err))
	}
	rec.Price = report.Price.String()
	rec.Decimals = report.Decimals
	rec.ObservedAt = report.ObservedAt

	if err := Preflight(s, cfg.Store, report); err != nil {
		if rj, ok := AsRejection(err); ok {
			rec.SubmitOutcome = rj.Guard
			return finish(classifyRejection(rj, report), err)
		}
		return finish(OutcomeInternal, err)
	}

	if cfg.DryRun {
		rec.SubmitOutcome = "dry_run"
		return finish(OutcomeRejected, nil)
	}

	tx, err := submit(ctx, s, cfg, report)
	if err != nil {
		rec.SubmitOutcome = "send_failed"
		return finish(OutcomeRejected, err)
	}
	rec.SubmitTx = tx.Hash().Hex()

	if err := waitMined(ctx, s, tx, cfg.SubmitTimeout); err != nil {
		// Not chased. The next cycle supersedes this one, and a replacement
		// landing after maxAge would revert TooOld anyway.
		rec.SubmitOutcome = "not_mined"
		return finish(OutcomeNotMined, err)
	}

	rec.SubmitOutcome = "accepted"
	return finish(OutcomeAccepted, nil)
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

// PollResult waits for a terminal action result. The deadline is what makes the
// cycle's duration knowable, and has to stay inside the store's maxAge.
//
// The tag is not optional: an instruction's result is filed under "threshold"
// and a direct action's under "submit", and asking for the wrong one returns 404
// forever rather than an error naming the mismatch.
func PollResult(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, error) {
	deadline := time.Now().Add(timeout)
	backoff := time.Second

	for attempt := 1; ; attempt++ {
		res, ok, err := fetchResult(proxyURL, id, tag)
		switch {
		case err != nil:
			logger.Warnf("attempt %d: %s", attempt, err)
		case ok:
			return res, nil
		default:
			logger.Infof("attempt %d: pending (status %d)", attempt, res.Status)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return teetypes.ActionResult{}, errors.Errorf(
				"no terminal result for %s within %s — FCC is fire-and-forget, so a dropped "+
					"instruction is an expected outcome, not an exceptional one", id.Hex(), timeout)
		}
		if backoff > remaining {
			backoff = remaining
		}

		select {
		case <-ctx.Done():
			return teetypes.ActionResult{}, ctx.Err()
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
	url := proxyURL + "/action/result/" + id.Hex() + "?submissionTag=" + string(tag)

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
	resp, err := http.Get(proxyURL + "/info")
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
