// Package keeper implements the USDX oracle keeper cycle and its timer.
//
// The keeper holds no authority — the store verifies every TEE signature against
// TeeMachineRegistry — so everything here is liveness and the worst a wrong
// invocation can do is waste gas. Nothing here may compute or adjust a price:
// the signed updates are relayed byte for byte and the store takes the median.
//
// A cycle keeps no state: every partial cycle is harmless, and observedAt
// monotonicity makes duplicate submissions safe by construction. Every cycle
// raises its request on-chain through requestFeedUpdate(feedId, teeIds), naming
// the target machines explicitly. Each of them answers on its own proxy, and the
// answers that agree on observedAt go up together in one submitFeedUpdates call.
package keeper

import (
	"context"
	"encoding/json"
	"math/big"
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
	// cost decision — and, since the store wants a threshold of distinct
	// signatures per submission, a liveness one. Empty is a configuration error
	// caught before any request.
	TeeIDs []common.Address

	// Proxy is the first place results are looked for, and the machine the
	// readiness preflight asks before paying the registry fee.
	Proxy string

	// ResultProxies are further proxies to look for results on. Each addressed
	// machine files its own result on its own proxy, so every machine's proxy is
	// a place to look.
	ResultProxies []string

	// DirectAPIKey unlocks the proxy's /direct endpoint, which the readiness
	// preflight uses. GET_FEED never goes that way — it is an on-chain
	// instruction and nothing else — so an empty key only costs the preflight.
	DirectAPIKey string

	// PollTimeout bounds the wait for the machines' terminal results. The wait
	// ends early once every addressed machine has answered; a machine that never
	// answers costs the whole timeout, and the cycle then goes on with the
	// answers it has. There is no maxAge on the store, so this is also the only
	// deadline on the observation's age from the keeper's side.
	PollTimeout   time.Duration
	SubmitTimeout time.Duration

	// DryRun stops after preflight, sending no transaction.
	DryRun bool
}

// RunCycle performs one complete keeper cycle. It never exits the process on an
// expected failure: a single failed cycle is noise, not a reason to stop.
func RunCycle(ctx context.Context, s *support.Support, cfg Config) (Record, error) {
	updates, rec, err := Collect(ctx, s, cfg)
	if err != nil {
		return rec, err
	}
	return Relay(ctx, s, cfg, rec, updates)
}

// Collect is the first half of a cycle: raise the instruction, wait for the
// machines' terminal results, decode each report. Split from Relay so a caller
// can inspect the reports before submitting them. Its Record is half-filled on
// success; pass it to Relay.
func Collect(ctx context.Context, s *support.Support, cfg Config) ([]feedupdate.Update, Record, error) {
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

	fail := func(out Outcome, err error) ([]feedupdate.Update, Record, error) {
		rec, err = finish(ctx, s, cfg, rec, out, err)
		return nil, rec, err
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
	results, err := pollResults(ctx, cfg.pollTargets(), len(cfg.TeeIDs), actionID, teetypes.Threshold, cfg.PollTimeout)
	if err != nil {
		rec.ResultStatus = "timeout"
		rec.SourceHealth = sourceHealth(cfg.Proxy)
		return fail(OutcomeNoResult, err)
	}

	// The first proxy to answer stands for the round: on failure it is what says
	// WHICH source broke.
	rec.Proxy = results[0].proxy
	rec.SourceHealth = sourceHealth(results[0].proxy)

	var updates []feedupdate.Update
	for _, r := range results {
		if r.result.Status != statusSucceeded {
			// A status-0 result is the extension declining to publish; Log is a
			// class from its closed vocabulary. Relaying anything would invent a
			// price.
			logger.Warnf("[%s] %s published nothing (status %d): %s",
				cfg.KeeperID, r.proxy, r.result.Status, r.result.Log)
			if rec.ErrorClass == "" {
				rec.ErrorClass = r.result.Log
			}
			continue
		}
		u, err := feedupdate.Decode(r.result.Data)
		if err != nil {
			logger.Warnf("[%s] %s: decoding the action result: %s", cfg.KeeperID, r.proxy, err)
			continue
		}
		updates = append(updates, u)
	}
	if len(updates) == 0 {
		rec.ResultStatus = "0"
		why := rec.ErrorClass
		if why == "" {
			why = "every result failed to decode"
		}
		return fail(OutcomeExtensionFailed, errors.Errorf(
			"no machine published a usable update (%d answered): %s", len(results), why))
	}
	rec.ResultStatus = "1"

	return updates, rec, nil
}

// Relay is the second half of a cycle: pick the updates that can go up together,
// check them against every store guard and submit them as one batch. Split from
// Collect so a caller holding several rounds' updates can order the submissions
// — observedAt must strictly increase.
func Relay(
	ctx context.Context, s *support.Support, cfg Config, rec Record, updates []feedupdate.Update,
) (Record, error) {
	batch, signers, err := chooseBatch(updates, s.ChainID)
	if err != nil {
		if rj, ok := AsRejection(err); ok {
			rec.SubmitOutcome = rj.Guard
		}
		return finish(ctx, s, cfg, rec, OutcomeRejected, err)
	}
	rec.ObservedAt = batch[0].ObservedAt
	for i, u := range batch {
		rec.TeeIDs = append(rec.TeeIDs, signers[i].Hex())
		rec.Prices = append(rec.Prices, u.Price())
	}
	if left := len(updates) - len(batch); left > 0 {
		logger.Warnf("[%s] %d of %d update(s) left out of the batch: a different observedAt, "+
			"a repeated signer, or no signer at all", cfg.KeeperID, left, len(updates))
	}

	if err := Preflight(ctx, s, cfg.Store, batch, signers); err != nil {
		if rj, ok := AsRejection(err); ok {
			rec.SubmitOutcome = rj.Guard
			return finish(ctx, s, cfg, rec, classifyRejection(rj, batch[0].ObservedAt), err)
		}
		return finish(ctx, s, cfg, rec, OutcomeInternal, err)
	}

	if cfg.DryRun {
		rec.SubmitOutcome = "dry_run"
		return finish(ctx, s, cfg, rec, OutcomeRejected, nil)
	}

	tx, err := submit(ctx, s, cfg, batch)
	if err != nil {
		rec.SubmitOutcome = "send_failed"
		return finish(ctx, s, cfg, rec, OutcomeRejected, err)
	}
	rec.SubmitTx = tx.Hash().Hex()

	if err := waitMined(ctx, s, tx, cfg.SubmitTimeout); err != nil {
		// Not chased. The next cycle supersedes this one, and a replacement
		// carrying the same observedAt would revert NotNewer anyway.
		rec.SubmitOutcome = "not_mined"
		return finish(ctx, s, cfg, rec, OutcomeNotMined, err)
	}

	rec.SubmitOutcome = "accepted"
	return finish(ctx, s, cfg, rec, OutcomeAccepted, nil)
}

// chooseBatch picks the updates that can go up together. The store judges one
// batch per observedAt and rejects a mixed one (ObservedAtMismatch) or a repeated
// signer (DuplicateTeeId), so the updates are grouped by observedAt with one per
// machine, and the largest group is submitted — the newest on a tie, since the
// ratchet only moves forward. A machine whose observation stamped a different
// instant is left out rather than sinking the whole submission. Signers are
// returned alongside, in batch order.
func chooseBatch(updates []feedupdate.Update, chainID *big.Int) ([]feedupdate.Update, []common.Address, error) {
	type member struct {
		update feedupdate.Update
		signer common.Address
	}
	groups := map[uint64][]member{}
	seen := map[common.Address]bool{}
	var unsigned []string

	for _, u := range updates {
		signer, err := u.Signer(chainID)
		if err != nil {
			unsigned = append(unsigned, err.Error())
			continue
		}
		if seen[signer] {
			continue
		}
		seen[signer] = true
		groups[u.ObservedAt] = append(groups[u.ObservedAt], member{u, signer})
	}
	if len(groups) == 0 {
		return nil, nil, reject("BadSignature",
			"no update recovers to a signer: %s", strings.Join(unsigned, "; "))
	}

	best, found := uint64(0), false
	for at, g := range groups {
		if !found || len(g) > len(groups[best]) || (len(g) == len(groups[best]) && at > best) {
			best, found = at, true
		}
	}

	batch := make([]feedupdate.Update, 0, len(groups[best]))
	signers := make([]common.Address, 0, len(groups[best]))
	for _, m := range groups[best] {
		batch = append(batch, m.update)
		signers = append(signers, m.signer)
	}
	return batch, signers, nil
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

// pollTargets is where results might be, most likely first and deduplicated.
// Every reachable machine is a candidate: each addressed machine files its
// result on its own proxy.
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
// NotNewer fires both when a keeper loses a race and when its own batch was
// stale; comparing against the store's stamp tells those apart. The only time
// rule left on the store is strict monotonicity.
func classifyRejection(rj *Rejection, observedAt uint64) Outcome {
	if rj.Guard != "NotNewer" {
		return OutcomeRejected
	}
	if rj.StoreObservedAt >= observedAt {
		return OutcomeRaceLost
	}
	return OutcomeStaleSubmission
}

// signedBatch reshapes decoded updates into the store's calldata, byte for byte.
func signedBatch(batch []feedupdate.Update) []teeoraclestore.ITeeOracleFeedStoreSignedFeedUpdate {
	out := make([]teeoraclestore.ITeeOracleFeedStoreSignedFeedUpdate, 0, len(batch))
	for _, u := range batch {
		out = append(out, teeoraclestore.ITeeOracleFeedStoreSignedFeedUpdate{
			FeedUpdate: teeoraclestore.ITeeOracleFeedStoreFeedUpdate{
				ExtensionId:   u.ExtensionID,
				FeedId:        u.FeedID,
				Value:         u.Value,
				Decimals:      u.Decimals,
				ObservedAt:    u.ObservedAt,
				EndpointsHash: u.EndpointsHash,
				AdminsHash:    u.AdminsHash,
			},
			Signature: teeoraclestore.Signature{V: u.V, R: u.R, S: u.S},
		})
	}
	return out
}

func submit(ctx context.Context, s *support.Support, cfg Config, batch []feedupdate.Update) (*types.Transaction, error) {
	store, err := teeoraclestore.NewIITeeOracleFeedStoreTransactor(cfg.Store, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return nil, err
	}
	opts.Context = ctx

	tx, err := store.SubmitFeedUpdates(opts, signedBatch(batch))
	if err != nil {
		return nil, errors.Errorf("submitFeedUpdates rejected before broadcast: %s", err)
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
	res, err := pollResults(ctx, []string{proxyURL}, 1, id, tag, timeout)
	if err != nil {
		return teetypes.ActionResult{}, err
	}
	return res[0].result, nil
}

// polled is one proxy's terminal result.
type polled struct {
	proxy  string
	result teetypes.ActionResult
}

// pollResults waits for terminal results on the given proxies. Each addressed
// machine files its own result on its own proxy, so the wait ends once `want`
// proxies have answered or the deadline passes, and whatever has arrived by then
// is returned in proxy order. Only an empty round is an error — a short one is
// for the store's threshold to judge. The wrong tag 404s forever rather than
// erroring.
func pollResults(
	ctx context.Context, proxies []string, want int, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) ([]polled, error) {
	if len(proxies) == 0 {
		return nil, errors.New("no proxy to poll for the result")
	}
	if want <= 0 || want > len(proxies) {
		want = len(proxies)
	}

	deadline := time.Now().Add(timeout)
	backoff := time.Second
	pending := append([]string(nil), proxies...)
	answered := map[string]teetypes.ActionResult{}

	for attempt := 1; ; attempt++ {
		// Sequential across proxies: a fleet is a handful of machines, and one
		// slow proxy delaying a round costs less than the concurrency would.
		still := pending[:0]
		for _, proxy := range pending {
			res, ok, err := fetchResult(proxy, id, tag)
			switch {
			case err != nil:
				logger.Warnf("attempt %d (%s): %s", attempt, proxy, err)
				still = append(still, proxy)
			case ok:
				answered[proxy] = res
			default:
				logger.Infof("attempt %d (%s): pending (status %d)", attempt, proxy, res.Status)
				still = append(still, proxy)
			}
		}
		pending = still

		remaining := time.Until(deadline)
		if len(answered) >= want || len(pending) == 0 || remaining <= 0 {
			break
		}
		if backoff > remaining {
			backoff = remaining
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < 16*time.Second {
			backoff *= 2
		}
	}

	if len(answered) == 0 {
		return nil, errors.Errorf(
			"no terminal result for %s within %s on any of %d proxy/proxies — FCC is "+
				"fire-and-forget, so a dropped instruction is an expected outcome, not an "+
				"exceptional one", id.Hex(), timeout, len(proxies))
	}
	out := make([]polled, 0, len(answered))
	for _, proxy := range proxies {
		if res, ok := answered[proxy]; ok {
			out = append(out, polled{proxy: proxy, result: res})
		}
	}
	return out, nil
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
