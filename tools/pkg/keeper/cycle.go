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
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/direct"
	"fce-usdx-oracle/tools/pkg/feedupdate"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// Config is everything one cycle needs.
type Config struct {
	KeeperID string

	Sender common.Address
	Store  common.Address

	// FeedID is the one feed this keeper serves. Named in the on-chain request
	// and inside the signed update, so a mismatch with the store reverts.
	FeedID [21]byte

	// TeeIDs are the machines to ask. The sender dispatches one instruction
	// addressed to every target and the fee scales with the count, so this is
	// both a routing and a cost decision — and, since the store wants a
	// threshold of distinct signatures per submission, a liveness one. Every
	// target must be at the feed's latest published configuration, or the
	// request reverts. An answer signed by any other machine is dropped before
	// submission. On a LoopConfig, empty means the keeper picks the targets each
	// cycle (LoopConfig.picksTargets); a cycle that still has none fails before
	// any request.
	TeeIDs []common.Address

	// Proxy is the first place results are looked for. The readiness preflight
	// asks it along with every ResultProxies entry before paying the registry
	// fee.
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

	// RequestTimeout bounds the trigger: sending requestFeedUpdate and waiting
	// for its receipt. A transaction that is broadcast and never mined produces
	// no receipt at all, and cycles are single-flight, so an unbounded wait here
	// stops the keeper entirely rather than costing it one cycle. Zero means
	// DefaultRequestTimeout.
	RequestTimeout time.Duration

	// MinBalance warns when the paying account falls below it. Zero disables the
	// check. A keeper that runs dry keeps cycling and failing at the request,
	// which reads like a chain problem rather than an empty wallet.
	MinBalance *big.Int

	// DryRun stops before submitFeedUpdates: the requestFeedUpdate transaction is
	// still sent and its fee paid, since that is what makes the machines answer.
	DryRun bool
}

// DefaultRequestTimeout bounds the trigger when a caller sets none. Generous
// against a congested chain, short enough that a stuck transaction costs one
// cycle rather than every cycle after it.
const DefaultRequestTimeout = 2 * time.Minute

// requestTimeout is RequestTimeout, or the default when unset.
func (c Config) requestTimeout() time.Duration {
	if c.RequestTimeout > 0 {
		return c.RequestTimeout
	}
	return DefaultRequestTimeout
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

	// Both the balance and the gap are diagnostics on the record: the keeper
	// spends but never tops itself up, and an empty account fails at the request
	// with an error that says nothing about funds.
	if balance := keeperBalance(ctx, s); balance != nil {
		rec.KeeperBalance = balance.String()
		if cfg.MinBalance != nil && cfg.MinBalance.Sign() > 0 && balance.Cmp(cfg.MinBalance) < 0 {
			logger.Warnf("[%s] balance %s wei is below the %s wei floor — top up %s or the feed stops",
				cfg.KeeperID, balance, cfg.MinBalance, crypto.PubkeyToAddress(s.Prv.PublicKey).Hex())
		}
	}

	fail := func(out Outcome, err error) ([]feedupdate.Update, Record, error) {
		rec, err = finish(ctx, s, cfg, rec, out, err)
		return nil, rec, err
	}

	// --- 1. trigger ---
	// Ask before paying: a machine whose observation has aged out will refuse
	// whatever it is asked, so the fee would buy a predictable failure. Best
	// effort throughout — an unreadable machine means requesting anyway.
	if len(cfg.TeeIDs) == 0 {
		return fail(OutcomeInternal, errors.New("no target machines: none were named, and the registry "+
			"lists no active machine at the feed's latest configuration"))
	}

	skip, why, health := wouldRefuse(ctx, cfg)
	// The preflight's view of the sources, from the first machine that answered:
	// on a failed cycle it is what says WHICH source broke.
	rec.SourceHealth = health
	if skip {
		return fail(OutcomeRequestSkipped, errors.New(why))
	}

	reqCtx, cancelRequest := context.WithTimeout(ctx, cfg.requestTimeout())
	actionID, _, err := instrutils.RequestFeedUpdate(reqCtx, s, cfg.Sender, cfg.FeedID, cfg.TeeIDs)
	cancelRequest()
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
		return fail(OutcomeNoResult, err)
	}

	// The first proxy in poll order with a result stands for the round.
	rec.Proxy = results[0].proxy

	updates := make([]feedupdate.Update, 0, len(results))
	for _, r := range results {
		if r.result.Status != direct.StatusSucceeded {
			// A status-0 result is the extension declining to publish; Log is
			// its public refusal text, "error: <reason>". Relaying anything
			// would invent a price.
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

// Relay is the second half of a cycle: judge every answer on its own, pick the
// ones that can go up together, check the batch against the store's rules and
// submit it. Split from Collect so a caller holding several rounds' updates can
// order the submissions — observedAt must strictly increase.
//
// One bad answer costs its machine the round, not the fleet: an update whose
// signature recovers to nobody or to a machine this cycle did not ask, or that
// names the wrong extension or feed or a stale configuration generation, is
// dropped and named in the record, and the rest are submitted if enough remain.
func Relay(
	ctx context.Context, s *support.Support, cfg Config, rec Record, updates []feedupdate.Update,
) (Record, error) {
	if len(updates) == 0 {
		return finish(ctx, s, cfg, rec, OutcomeInternal, errors.New("no update to relay"))
	}
	members, dropped, err := Screen(ctx, s, cfg.Store, cfg.TeeIDs, updates)
	if err != nil {
		if rj, ok := AsRejection(err); ok {
			rec.SubmitOutcome = rj.Guard
			return finish(ctx, s, cfg, rec, OutcomeRejected, err)
		}
		return finish(ctx, s, cfg, rec, OutcomeInternal, err)
	}
	batch, signers, left := chooseBatch(members)
	rec.Dropped = append(dropped, left...)
	for _, d := range rec.Dropped {
		logger.Warnf("[%s] answer dropped (%s): %s", cfg.KeeperID, d.Guard, d.Detail)
	}
	if len(batch) == 0 {
		rj := reject(droppedGuard(rec.Dropped),
			"nothing to submit: every one of the %d answer(s) was dropped", len(rec.Dropped))
		rec.SubmitOutcome = rj.Guard
		return finish(ctx, s, cfg, rec, OutcomeRejected, rj)
	}
	rec.ObservedAt = batch[0].ObservedAt
	for i, u := range batch {
		rec.TeeIDs = append(rec.TeeIDs, signers[i].Hex())
		rec.Prices = append(rec.Prices, u.Price())
	}

	ours := batch[0].ObservedAt
	if err := Preflight(ctx, s, cfg.Store, batch, signers); err != nil {
		if rj, ok := AsRejection(err); ok {
			return settleRejection(ctx, s, cfg, rec, rj, ours)
		}
		return finish(ctx, s, cfg, rec, OutcomeInternal, err)
	}

	if cfg.DryRun {
		rec.SubmitOutcome = "dry_run"
		return finish(ctx, s, cfg, rec, OutcomeDryRun, nil)
	}

	tx, err := submit(ctx, s, cfg, batch)
	if err != nil {
		// Gas estimation runs the submission against pending state, so a batch
		// overtaken since its rehearsal is refused here, by name.
		if rj, ok := AsRejection(err); ok {
			return settleRejection(ctx, s, cfg, rec, rj, ours)
		}
		rec.SubmitOutcome = "send_failed"
		return finish(ctx, s, cfg, rec, OutcomeRejected, err)
	}
	rec.SubmitTx = tx.Hash().Hex()

	receipt, err := awaitReceipt(ctx, s, tx, cfg.SubmitTimeout)
	if err != nil {
		// Not chased. The next cycle supersedes this one, and a replacement
		// carrying the same observedAt would revert NotNewer anyway.
		rec.SubmitOutcome = "not_mined"
		return finish(ctx, s, cfg, rec, OutcomeNotMined, err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return settleRevert(ctx, s, cfg, rec, batch, receipt)
	}

	rec.SubmitOutcome = "accepted"
	return finish(ctx, s, cfg, rec, OutcomeAccepted, nil)
}

// settleRejection ends a cycle whose batch the store refused before it was
// mined: in the preflight's own checks, in the rehearsal, or at gas estimation.
// A NotNewer is a lost race when the store, read again now, holds an
// observedAt at least as new as ours; every other refusal is a rejection.
func settleRejection(
	ctx context.Context, s *support.Support, cfg Config, rec Record, rj *Rejection, ours uint64,
) (Record, error) {
	rec.SubmitOutcome = rj.Guard
	out := OutcomeRejected
	if rj.Guard == guardNotNewer {
		stored, err := readStoreObservedAt(ctx, s, cfg.Store)
		if raceLost(ours, stored, err == nil) {
			out = OutcomeRaceLost
		}
	}
	return finish(ctx, s, cfg, rec, out, rj)
}

// settleRevert ends a cycle whose submission was mined and reverted. The
// replay names the guard for the record; the outcome rests on the store alone.
// A batch that passed its rehearsal and then reverted has almost always been
// overtaken, and a store that now holds an observedAt at least as new as ours
// says the feed advanced regardless of which guard the replay names. Otherwise
// it is a rejection under that guard.
func settleRevert(
	ctx context.Context, s *support.Support, cfg Config, rec Record,
	batch []feedupdate.Update, receipt *types.Receipt,
) (Record, error) {
	rj := replayRevert(ctx, s, cfg.Store, batch, receipt.BlockNumber)
	rec.SubmitOutcome = rj.Guard
	stored, err := readStoreObservedAt(ctx, s, cfg.Store)
	out := OutcomeRejected
	if raceLost(batch[0].ObservedAt, stored, err == nil) {
		out = OutcomeRaceLost
	}
	return finish(ctx, s, cfg, rec, out, errors.Wrapf(rj,
		"tx %s was mined in block %s and reverted", receipt.TxHash.Hex(), receipt.BlockNumber))
}

// raceLost reports whether a refused batch was overtaken: the store, read after
// the refusal, holds an observedAt at least as new as the batch's. The ratchet
// only moves forward, so a NotNewer reads this way whenever the read reaches a
// node that has seen the refusing state; a lagging node, or a store that could
// not be read at all, answers false and the cycle stays a rejection.
func raceLost(ours, stored uint64, readable bool) bool {
	return readable && stored >= ours
}

// signedUpdate is a decoded update with the machine its signature recovers to.
type signedUpdate struct {
	update feedupdate.Update
	signer common.Address
}

// chooseBatch picks the screened answers that can go up together. The store
// judges one batch per observedAt and rejects a repeated signer (DuplicateTeeId)
// or a mixed batch (ObservedAtMismatch), so one answer per machine is kept and
// the largest group sharing an observedAt is submitted — the newest on a tie,
// since the ratchet only moves forward. Every other answer is returned as
// dropped, named by the guard that would have rejected it, in arrival order.
// Signers come back alongside the batch, in batch order.
func chooseBatch(members []signedUpdate) (batch []feedupdate.Update, signers []common.Address, dropped []Dropped) {
	kept := make([]signedUpdate, 0, len(members))
	count := map[uint64]int{}
	seen := map[common.Address]bool{}
	for _, m := range members {
		if seen[m.signer] {
			dropped = append(dropped, Dropped{TeeID: m.signer.Hex(), Guard: "DuplicateTeeId",
				Detail: m.signer.Hex() + " answered more than once; its first answer stands"})
			continue
		}
		seen[m.signer] = true
		kept = append(kept, m)
		count[m.update.ObservedAt]++
	}
	if len(kept) == 0 {
		return nil, nil, dropped
	}

	best, found := uint64(0), false
	for at, n := range count {
		if !found || n > count[best] || (n == count[best] && at > best) {
			best, found = at, true
		}
	}

	for _, m := range kept {
		if m.update.ObservedAt != best {
			dropped = append(dropped, Dropped{TeeID: m.signer.Hex(), Guard: "ObservedAtMismatch",
				Detail: fmt.Sprintf("%s is stamped observedAt=%d, the batch %d",
					m.signer.Hex(), m.update.ObservedAt, best)})
			continue
		}
		batch = append(batch, m.update)
		signers = append(signers, m.signer)
	}
	return batch, signers, dropped
}

// droppedGuard names a round with nothing left to submit: the one guard every
// answer failed, or NoUsableUpdate when they failed different ones.
func droppedGuard(dropped []Dropped) string {
	guard := ""
	for _, d := range dropped {
		if guard == "" {
			guard = d.Guard
		} else if d.Guard != guard {
			return "NoUsableUpdate"
		}
	}
	if guard == "" {
		return "NoUsableUpdate"
	}
	return guard
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

// submit sends the batch to the store. A refusal at gas estimation that names a
// store guard comes back as a *Rejection; any other failure is a plain error.
func submit(ctx context.Context, s *support.Support, cfg Config, batch []feedupdate.Update) (*types.Transaction, error) {
	parsed, err := teeoraclestore.IITeeOracleFeedStoreMetaData.GetAbi()
	if err != nil {
		return nil, errors.Errorf("parsing the store ABI: %s", err)
	}
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
		if rj := decodeRejection(parsed, err, "the store rejects this batch at gas estimation"); rj != nil {
			return nil, rj
		}
		return nil, errors.Errorf("submitFeedUpdates rejected before broadcast: %s", err)
	}
	return tx, nil
}

// awaitReceipt waits up to timeout for tx's receipt, whatever its status. An
// error means no receipt arrived in time.
func awaitReceipt(ctx context.Context, s *support.Support, tx *types.Transaction, timeout time.Duration) (*types.Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		return nil, errors.Errorf("tx %s was not mined within %s: %s", tx.Hash().Hex(), timeout, err)
	}
	return receipt, nil
}

// PollResult waits for a terminal action result on one proxy.
func PollResult(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, error) {
	return direct.Await(ctx, proxyURL, id, tag, timeout)
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
	// What each unanswered proxy last said. A result that has not landed yet
	// reads as a proxy error (404) on every round until it does, so the rounds
	// are reported at DEBUG only and the outcome names each proxy's last answer.
	lastSeen := map[string]string{}

	type outcome struct {
		result teetypes.ActionResult
		ok     bool
		err    error
	}

	for attempt := 1; ; attempt++ {
		// Concurrent across proxies, each bounded by direct.Timeout: a round costs
		// the slowest proxy rather than the sum, so one machine owner cannot hold
		// the fleet's poll by pointing the registry at an endpoint that never
		// answers. Results are index-aligned, so the reporting order below stays
		// the caller's order.
		outcomes := make([]outcome, len(pending))
		var wg sync.WaitGroup
		for i, proxy := range pending {
			wg.Go(func() {
				res, ok, err := direct.Result(ctx, proxy, id, tag)
				outcomes[i] = outcome{result: res, ok: ok, err: err}
			})
		}
		wg.Wait()

		still := make([]string, 0, len(pending))
		for i, proxy := range pending {
			o := outcomes[i]
			switch {
			case o.err != nil:
				lastSeen[proxy] = o.err.Error()
				still = append(still, proxy)
			case o.ok:
				answered[proxy] = o.result
				continue
			default:
				lastSeen[proxy] = fmt.Sprintf("pending (status %d)", o.result.Status)
				still = append(still, proxy)
			}
			logger.Debugf("attempt %d (%s): %s", attempt, proxy, lastSeen[proxy])
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

	unanswered := make([]string, 0, len(pending))
	for _, proxy := range pending {
		unanswered = append(unanswered, proxy+": "+lastSeen[proxy])
	}
	if len(answered) == 0 {
		return nil, errors.Errorf(
			"no terminal result for %s within %s on any of %d proxy/proxies (%s) — FCC is "+
				"fire-and-forget, so a dropped instruction is an expected outcome, not an "+
				"exceptional one", id.Hex(), timeout, len(proxies), strings.Join(unanswered, "; "))
	}
	if len(unanswered) > 0 {
		// A short round is not an error — the store's threshold judges it — but
		// it is worth one line saying who was missing and why.
		logger.Infof("%d of %d proxies answered within %s; unanswered: %s",
			len(answered), len(proxies), timeout, strings.Join(unanswered, "; "))
	}
	out := make([]polled, 0, len(answered))
	for _, proxy := range proxies {
		if res, ok := answered[proxy]; ok {
			out = append(out, polled{proxy: proxy, result: res})
		}
	}
	return out, nil
}

// readStoreObservedAt reads the store's current stamp.
func readStoreObservedAt(ctx context.Context, s *support.Support, storeAddr common.Address) (uint64, error) {
	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return 0, errors.Errorf("binding the store: %s", err)
	}
	return store.ObservedAt(&bind.CallOpts{Context: ctx})
}

// storeObservedAt is readStoreObservedAt for the record, zero when unreadable.
// Best-effort: it is a diagnostic, not a control input.
func storeObservedAt(ctx context.Context, s *support.Support, storeAddr common.Address) uint64 {
	observedAt, err := readStoreObservedAt(ctx, s, storeAddr)
	if err != nil {
		return 0
	}
	return observedAt
}

// keeperBalance is what the paying account holds now, or nil if it cannot be
// read. Best-effort: a diagnostic on the record, never a control input.
func keeperBalance(ctx context.Context, s *support.Support) *big.Int {
	balance, err := s.ChainClient.BalanceAt(ctx, crypto.PubkeyToAddress(s.Prv.PublicKey), nil)
	if err != nil {
		return nil
	}
	return balance
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
