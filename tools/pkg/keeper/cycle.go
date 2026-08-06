// Package keeper implements the USDX oracle keeper cycle and the timer around
// it, as specified in docs/usdx-oracle-keeper.md.
//
// The keeper holds no authority. UsdxPriceStore verifies the TEE signature
// against TeeMachineRegistry, so everything here is liveness: the worst a wrong
// invocation can do is waste gas. Nothing in this package may compute, adjust or
// sanity-"fix" a price, and the signed body is relayed byte for byte — the store
// checks the signature over exactly the bytes it receives, so a re-encode would
// surface an encoder divergence as a bad signature instead of as the encoding
// bug it is.
//
// # Why there is no persistence
//
// Every partial cycle is harmless. Crash after the trigger and the instruction
// is already dispatched, the TEE still computes, and the result sits unread —
// one wasted fee. Crash before the submit and a co-keeper relays it, or the next
// cycle supersedes it. Nothing is two-phase, nothing needs to be exactly-once,
// and observedAt monotonicity makes duplicate submissions safe by construction.
//
// So the keeper keeps no state across cycles and needs no recovery logic. The
// records it emits are write-only: nothing reads one back to make a decision.
package keeper

import (
	"context"
	"encoding/json"
	"net/http"
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

// Terminal action statuses. Anything else means the instruction is still in
// flight — pending is not an error, and treating it as one discards results
// that were merely slow.
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

	// PollTimeout bounds step 2, SubmitTimeout step 3. Both are spent out of the
	// store's maxAge budget: observedAt is stamped inside the enclave before
	// signing, so every second here is one submitPrice will count against the
	// report's age. See "The deadline budget must fit inside maxAge".
	PollTimeout   time.Duration
	SubmitTimeout time.Duration

	// DryRun stops after preflight, sending no transaction.
	DryRun bool
}

// RunCycle performs one complete keeper cycle and returns its record.
//
// It never exits the process and never panics on an expected failure: a keeper
// runs this on a timer, and a single failed cycle is noise, not a reason to
// stop. The returned error is the same information as Record.Detail, for callers
// that want to fail a one-shot invocation.
func RunCycle(ctx context.Context, s *support.Support, cfg Config) (Record, error) {
	rec := Record{
		KeeperID:       cfg.KeeperID,
		CycleStartedAt: time.Now().UTC(),
		Outcome:        OutcomeInternal,
		SubmitOutcome:  "not_attempted",
	}

	// A stuck transaction consumes a nonce and blocks every later one behind it,
	// which a one-shot never sees but a looping keeper eventually will. Recorded
	// rather than acted on: the fix is operational, not something to retry into.
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
	instructionID, _, err := instrutils.RequestPriceUpdate(s, cfg.Sender)
	if err != nil {
		rec.RequestTxStatus = "failed"
		return finish(OutcomeRequestFailed, err)
	}
	rec.RequestTxStatus = "mined"
	rec.InstructionID = instructionID.Hex()
	logger.Infof("[%s] instruction %s", cfg.KeeperID, instructionID.Hex())

	// --- 2. collect -----------------------------------------------------------
	result, err := PollResult(ctx, cfg.Proxy, instructionID, cfg.PollTimeout)
	if err != nil {
		rec.ResultStatus = "timeout"
		rec.SourceHealth = sourceHealth(cfg.Proxy)
		return finish(OutcomeNoResult, err)
	}

	// Health is recorded whether or not the observation succeeded: on failure it
	// is what says WHICH source broke.
	rec.SourceHealth = sourceHealth(cfg.Proxy)

	if result.Status != statusSucceeded {
		// Fail closed. A status-0 result is the extension declining to publish,
		// and Log is a class from its closed vocabulary — the alert key.
		// Relaying anything here would be inventing a price.
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
//
// NotNewer is the expected outcome for every keeper that loses a race, and with
// ≥2 keepers exactly one submission wins per cycle. But the same guard also
// fires when our own report was stale — an old result relayed after a long
// delay — and that is a latency problem worth seeing. Comparing against the
// store's stamp is what separates "someone beat me by a second" from "I am
// submitting minutes-old reports".
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

// PollResult waits for a terminal action result.
//
// A bounded wall-clock deadline is what makes the cycle's duration knowable, and
// it has to stay inside the store's maxAge. Pending is not an error: treating it
// as one discards results that were merely slow.
func PollResult(
	ctx context.Context, proxyURL string, id common.Hash, timeout time.Duration,
) (teetypes.ActionResult, error) {
	deadline := time.Now().Add(timeout)
	backoff := time.Second

	for attempt := 1; ; attempt++ {
		res, ok, err := fetchResult(proxyURL, id)
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
func fetchResult(proxyURL string, id common.Hash) (teetypes.ActionResult, bool, error) {
	resp, err := http.Get(proxyURL + "/action/result/" + id.Hex())
	if err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("querying the proxy: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 404 until the TEE has answered: the result simply is not in storage
		// yet, which is the normal early state rather than a fault.
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
//
// Best-effort by design: a nil map means the proxy could not be read, and the
// caller must never read that as "the sources are fine". The map is decoded
// generically rather than through the extension's Go types, so this stays
// independent of any one implementation.
//
// Note also that the map is a snapshot written only when an observation runs. If
// requests stop, the last values persist and keep reading "ok" — which is why
// the alerting in the monitoring doc pages on on-chain feed age and not on this.
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
