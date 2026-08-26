// Package keeper implements the USDX oracle keeper cycle and its timer.
//
// The keeper holds no authority — the store verifies the TEE signature against
// TeeMachineRegistry — so everything here is liveness and the worst a wrong
// invocation can do is waste gas. Nothing here may compute or adjust a price,
// and the signed body is relayed byte for byte.
//
// A cycle keeps no state: every partial cycle is harmless, and observedAt
// monotonicity makes duplicate submissions safe by construction. Every cycle
// raises its request on-chain through requestFeedUpdate(feedId, teeIds), naming
// the target machines explicitly, and a restart costs nothing.
package keeper

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/feedupdate"
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

	// FeedID is the one feed this keeper serves. Named in the on-chain request
	// and inside the signed update, so a mismatch with the store reverts.
	FeedID [21]byte

	// TeeIDs are the machines to ask. The sender dispatches one instruction per
	// target and the fee scales with the count, so this is both a routing and a
	// cost decision. Empty is a configuration error caught before any request.
	TeeIDs []common.Address

	// Proxy is the first place the result is looked for, and the machine the
	// readiness preflight asks before paying the registry fee.
	Proxy string

	// ResultProxies are further proxies to look for the result on: only the
	// machine the sender picked holds the result, but every target is a
	// candidate until one answers.
	ResultProxies []string

	// DirectAPIKey unlocks the proxy's /direct endpoint, which the readiness
	// preflight uses. GET_FEED never goes that way — it is an on-chain
	// instruction and nothing else — so an empty key only costs the preflight.
	DirectAPIKey string

	// PollTimeout bounds the wait for a terminal action result. There is no
	// maxAge on the store any more — the acceptance window went away with
	// multi-feed support — so this is the only deadline on the observation's
	// age from the keeper's side.
	PollTimeout   time.Duration
	SubmitTimeout time.Duration

	// DryRun stops after preflight, sending no transaction.
	DryRun bool
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

// Collect is the first half of a cycle: raise the instruction, wait for a terminal
// result, decode the report. Split from Relay so a caller can inspect the report
// before submitting it. Its Record is half-filled on success; pass it to Relay.
func Collect(ctx context.Context, s *support.Support, cfg Config) (feedupdate.Update, Record, error) {
	rec := Record{
		KeeperID:       cfg.KeeperID,
		CycleStartedAt: time.Now().UTC(),
		Outcome:        OutcomeInternal,
		SubmitOutcome:  "not_attempted",
		Proxy:          cfg.Proxy,
	}

	// A stuck transaction blocks every later one behind it. Recorded rather than
	// acted on: the fix is operational, not something to retry into.
	rec.PendingNonceGap = pendingNonceGap(ctx, s)

	fail := func(out Outcome, err error) (feedupdate.Update, Record, error) {
		rec, err = finish(ctx, s, cfg, rec, out, err)
		return feedupdate.Update{}, rec, err
	}

	// --- 1. trigger ---
	// Ask before paying: a machine whose observation has aged out will refuse
	// whatever it is asked, so the fee would buy a predictable failure. Best
	// effort throughout — an unreadable machine means requesting anyway.
	if len(cfg.TeeIDs) == 0 {
		return fail(OutcomeInternal, errors.New("no target machines configured"))
	}

	if skip, why := wouldRefuse(ctx, cfg); skip {
		return fail(OutcomeRequestSkipped, errors.New(why))
	}

	actionID, _, err := instrutils.RequestFeedUpdate(s, cfg.Sender, cfg.FeedID, cfg.TeeIDs)
	if err != nil {
		rec.RequestTxStatus = "failed"
		return fail(OutcomeRequestFailed, err)
	}
	rec.RequestTxStatus = "mined"
	rec.InstructionID = actionID.Hex()
	logger.Infof("[%s] instruction %s", cfg.KeeperID, actionID.Hex())

	// --- 2. collect ---
	// An instruction's result is filed under "threshold"; the wrong tag 404s
	// until the deadline rather than erroring.
	targets := cfg.pollTargets()
	result, from, err := PollResultAny(ctx, targets, actionID, teetypes.Threshold, cfg.PollTimeout)
	if err != nil {
		rec.ResultStatus = "timeout"
		rec.SourceHealth = sourceHealth(cfg.Proxy)
		return fail(OutcomeNoResult, err)
	}

	// The answering proxy: the one the registry happened to route to, rather
	// than the one configured.
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

	report, err := feedupdate.Decode(result.Data)
	if err != nil {
		return fail(OutcomeInternal, errors.Errorf("decoding the action result: %s", err))
	}
	rec.Price = report.Price()
	rec.Decimals = report.Decimals
	rec.ObservedAt = report.ObservedAt

	// The signer is the machine that actually produced this update — the only
	// thing that says which of the fleet the sender picked, and so the only
	// source rec.TeeID ever has.
	if teeID, err := report.Signer(s.ChainID); err == nil {
		rec.TeeID = teeID.Hex()
	}

	return report, rec, nil
}

// Relay is the second half of a cycle: check the update against every store
// guard and submit it. Split from Collect so a caller holding several machines'
// updates can order the submissions — observedAt must strictly increase.
func Relay(
	ctx context.Context, s *support.Support, cfg Config, rec Record, report feedupdate.Update,
) (Record, error) {
	if err := Preflight(s, cfg.Store, s.ChainID, report); err != nil {
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
// Every reachable machine is a candidate: the registry picks which one serves the
// instruction, and only that machine's own proxy holds the result.
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

// classifyRejection separates the one benign store guard from the rest.
// NotNewer fires both when a keeper loses a race and when its own update was
// stale; comparing against the store's stamp tells those apart. TooOld and
// maxFutureSkew are gone — the only time rule left is strict monotonicity.
func classifyRejection(rj *Rejection, r feedupdate.Update) Outcome {
	if rj.Guard != "NotNewer" {
		return OutcomeRejected
	}
	if rj.StoreObservedAt >= r.ObservedAt {
		return OutcomeRaceLost
	}
	return OutcomeStaleSubmission
}

func submit(ctx context.Context, s *support.Support, cfg Config, r feedupdate.Update) (*types.Transaction, error) {
	store, err := teeoraclestore.NewIITeeOracleFeedStoreTransactor(cfg.Store, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return nil, err
	}
	opts.Context = ctx

	tx, err := store.SubmitFeedUpdate(opts,
		teeoraclestore.ITeeOracleFeedStoreFeedUpdate{
			ExtensionId:   r.ExtensionID,
			FeedId:        r.FeedID,
			Value:         r.Value,
			Decimals:      r.Decimals,
			ObservedAt:    r.ObservedAt,
			EndpointsHash: r.EndpointsHash,
			AdminsHash:    r.AdminsHash,
		},
		teeoraclestore.Signature{V: r.V, R: r.R, S: r.S},
	)
	if err != nil {
		return nil, errors.Errorf("submitFeedUpdate rejected before broadcast: %s", err)
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
		return errors.Errorf("tx %s reverted despite passing preflight — re-run "+
			"oracle-cycle.sh --dry-run to see which guard rejects it", tx.Hash().Hex())
	}
	return nil
}

// PollResult waits for a terminal action result on one proxy.
func PollResult(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, error) {
	res, _, err := PollResultAny(ctx, []string{proxyURL}, id, tag, timeout)
	return res, err
}

// PollResultAny waits for a terminal result on any of several proxies and reports
// which answered — an instruction is routed at random and only the chosen machine's
// proxy holds it. The wrong tag 404s forever rather than erroring.
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
// Best-effort: nil means the proxy could not be read, never "the sources are
// fine". Stale "ok" values persist if requests stop — alert on feed age instead.
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
	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
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
