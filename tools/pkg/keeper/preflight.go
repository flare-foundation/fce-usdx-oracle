// Preflight checks a batch of decoded feed updates against every store guard
// before spending gas. Screen judges each answer on its own — its signer, its
// feed binding, its configuration commitments — so one bad answer is dropped
// rather than sinking the round; Preflight then judges the batch: the submission
// policy's count, the observedAt ratchet, and the contract's own dry run, an
// eth_call of submitFeedUpdates, which enforces everything else. The cheap checks
// duplicate one contract rule each and turn a revert into a named, actionable
// reason.
//
// The store verifies each TEE signature through Fdc2Verification, so the keeper
// holds no authority — the worst a wrong call can do is waste gas. Nothing here
// may compute or adjust a price, and the updates are relayed byte for byte.
package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/feedupdate"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// feedID is the one feed this keeper serves. Pinned here because the store has
// no feedId getter — it is checked at init and never read again — so a preflight
// that wanted it would have to trust the caller. The keeper's configured feed
// matches the store's by construction (same deployment), and the signed update
// names the same id, so this is a sanity check rather than a load-bearing one.
//
// It mirrors internal/config.FeedID; the two cannot share a definition because
// tools/ is a separate module and internal/ is not importable from here.
var feedID = mustFeedID("0x21555344582f555344000000000000000000000000")

// maxBatch is the store's ceiling on signatures per submission.
const maxBatch = 32

func mustFeedID(s string) [21]byte {
	raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(raw) != 21 {
		panic("keeper: invalid feed id " + s)
	}
	var id [21]byte
	copy(id[:], raw)
	return id
}

// Rejection is a batch the store will not accept, named by the guard that
// rejects it. The name matters more than the message: StaleEndpoints needs a
// setEndpoints call, NotNewer usually needs nothing, and "execution reverted"
// distinguishes neither.
type Rejection struct {
	Guard string
	Msg   string

	// Set for NotNewer, so the caller can tell a lost race from a stale
	// submission by comparing against the batch's observedAt.
	StoreObservedAt uint64
}

func (r *Rejection) Error() string { return r.Msg }

func reject(guard, format string, args ...any) *Rejection {
	return &Rejection{Guard: guard, Msg: guard + ": " + fmt.Sprintf(format, args...)}
}

// AsRejection reports whether err is a named store rejection.
func AsRejection(err error) (*Rejection, bool) {
	var r *Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// commitments is what every answer must carry to be accepted: the store's
// extension id and the feed's latest published configuration generation, read
// once per cycle.
type commitments struct {
	extensionID *big.Int
	endpoints   common.Hash
	admins      common.Hash
}

// readCommitments reads them from the store and its sender. A feed with nothing
// published is a whole-round rejection: no machine could have answered under
// the right generation.
func readCommitments(ctx context.Context, s *support.Support, storeAddr common.Address) (commitments, error) {
	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return commitments{}, errors.Errorf("binding the store: %s", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return commitments{}, errors.Errorf("reading extensionId from %s: %s", storeAddr.Hex(), err)
	}
	senderAddr, err := store.InstructionsSender(opts)
	if err != nil {
		return commitments{}, errors.Errorf("reading the instructions sender: %s", err)
	}
	sender, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return commitments{}, errors.Errorf("binding the sender: %s", err)
	}

	// The configuration hashes are feed-level: one latest published generation,
	// which every machine's update must carry.
	latestEndpoints, err := sender.LatestEndpointsHash(opts, feedID)
	if err != nil {
		return commitments{}, reject("SenderUnavailable", "reading latestEndpointsHash: %s", err)
	}
	if common.Hash(latestEndpoints) == (common.Hash{}) {
		return commitments{}, reject("NoEndpointsPublished",
			"no endpoint configuration has been published for feed %#x — run send-endpoints first", feedID)
	}
	latestAdmins, err := sender.LatestAdminsHash(opts, feedID)
	if err != nil {
		return commitments{}, reject("SenderUnavailable", "reading latestAdminsHash: %s", err)
	}
	if common.Hash(latestAdmins) == (common.Hash{}) {
		return commitments{}, reject("NoAdminsPublished",
			"no admin set has been published for feed %#x — run send-admins first", feedID)
	}
	return commitments{
		extensionID: extensionID,
		endpoints:   common.Hash(latestEndpoints),
		admins:      common.Hash(latestAdmins),
	}, nil
}

// Screen judges every answer on its own and returns the ones the store could
// accept, each with the machine its signature recovers to, and the rest named
// by the guard that would have rejected them. addressed are the machines this
// cycle asked: an answer signed by any other key is dropped, so a proxy cannot
// sink the round with a signature the store would refuse anyway. An empty list
// lifts that restriction. The store's own verification decides in the end;
// this only keeps one bad answer from costing the good ones their round.
func Screen(
	ctx context.Context, s *support.Support, storeAddr common.Address,
	addressed []common.Address, updates []feedupdate.Update,
) ([]signedUpdate, []Dropped, error) {
	want, err := readCommitments(ctx, s, storeAddr)
	if err != nil {
		return nil, nil, err
	}
	kept, dropped := screenUpdates(updates, s.ChainID, addressed, want)
	return kept, dropped, nil
}

// screenUpdates is the chain-free half of Screen: every answer against the
// commitments already read, in arrival order.
func screenUpdates(
	updates []feedupdate.Update, chainID *big.Int, addressed []common.Address, want commitments,
) (kept []signedUpdate, dropped []Dropped) {
	asked := make(map[common.Address]bool, len(addressed))
	for _, a := range addressed {
		asked[a] = true
	}
	for _, u := range updates {
		signer, err := u.Signer(chainID)
		if err != nil {
			dropped = append(dropped, Dropped{Guard: "BadSignature",
				Detail: "an answer recovers to no signer: " + err.Error()})
			continue
		}
		drop := func(guard, format string, args ...any) {
			dropped = append(dropped, Dropped{TeeID: signer.Hex(), Guard: guard, Detail: fmt.Sprintf(format, args...)})
		}
		switch {
		case len(asked) > 0 && !asked[signer]:
			drop("NotAddressed", "%s answered but this cycle did not ask it", signer.Hex())
		case u.ExtensionID == nil || u.ExtensionID.Cmp(want.extensionID) != 0:
			drop("WrongExtensionId", "%s names extension %s, the store accepts %s",
				signer.Hex(), u.ExtensionID, want.extensionID)
		case u.FeedID != feedID:
			// feedId has no getter on the store (it is checked at init and never
			// read again), so this compares against the keeper's pinned id; a
			// divergence means a deployment error, not a stale configuration.
			drop("WrongFeedId", "%s names feed %#x, this keeper serves %#x", signer.Hex(), u.FeedID, feedID)
		case u.EndpointsHash != want.endpoints:
			drop("StaleEndpoints", "%s computed its update under endpoints %s but the feed's latest is %s; "+
				"the machine never applied the published configuration, or a newer one was sent",
				signer.Hex(), u.EndpointsHash.Hex(), want.endpoints.Hex())
		case u.AdminsHash != want.admins:
			drop("StaleAdmins", "%s computed its update under admin set %s but the feed's latest is %s; "+
				"the machine never applied the published admin set, or a newer one was sent",
				signer.Hex(), u.AdminsHash.Hex(), want.admins.Hex())
		default:
			kept = append(kept, signedUpdate{update: u, signer: signer})
		}
	}
	return kept, dropped
}

// Preflight checks a screened batch against the store's batch-level rules
// before spending gas: the submission policy's count, the observedAt ratchet,
// and the contract's own rehearsal. Per-answer rules are Screen's. signers are
// the machines Screen recovered, in batch order, and every update carries the
// same observedAt (see chooseBatch).
func Preflight(
	ctx context.Context, s *support.Support, storeAddr common.Address,
	batch []feedupdate.Update, signers []common.Address,
) error {
	if len(batch) == 0 || len(batch) != len(signers) {
		return errors.Errorf("preflight: %d update(s) with %d signer(s)", len(batch), len(signers))
	}
	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the store: %s", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	// The submission policy: how many distinct machines must sign one batch.
	// Fewer means waiting for the rest is the fix, or asking more machines; the
	// record's dropped list says whether some answered and were left out.
	required, err := store.RequiredSignatures(opts)
	if err != nil {
		return errors.Errorf("reading requiredSignatures: %s", err)
	}
	if len(batch) < int(required) {
		return reject("NotEnoughSignatures",
			"%d machine(s) agree on observedAt=%d but the store requires %d signatures",
			len(batch), batch[0].ObservedAt, required)
	}
	if len(batch) > maxBatch {
		return reject("TooManySignatures",
			"%d machines answered, the store takes at most %d per submission", len(batch), maxBatch)
	}

	// Strictly increasing observedAt — the only time rule left. Checked here
	// rather than left to the rehearsal because the store's stamp is what tells a
	// lost race from a stale submission.
	stored, err := store.ObservedAt(opts)
	if err != nil {
		return errors.Errorf("reading observedAt: %s", err)
	}
	if batch[0].ObservedAt <= stored {
		rj := reject("NotNewer",
			"the store already holds observedAt=%d and this batch is stamped %d",
			stored, batch[0].ObservedAt)
		rj.StoreObservedAt = stored
		return rj
	}

	return rehearse(ctx, s, storeAddr, batch)
}

// rehearse is the contract's own dry run: an eth_call of submitFeedUpdates with
// the batch, from the keeper's address. It enforces every rule a real submission
// would — signature and registry status, the extension pause, the median's
// spread bound — and a revert names the guard, which becomes the Rejection.
func rehearse(ctx context.Context, s *support.Support, storeAddr common.Address, batch []feedupdate.Update) error {
	parsed, err := teeoraclestore.IITeeOracleFeedStoreMetaData.GetAbi()
	if err != nil {
		return errors.Errorf("parsing the store ABI: %s", err)
	}
	input, err := parsed.Pack("submitFeedUpdates", signedBatch(batch))
	if err != nil {
		return errors.Errorf("encoding submitFeedUpdates: %s", err)
	}
	msg := ethereum.CallMsg{To: &storeAddr, Data: input}
	if s.Prv != nil {
		msg.From = crypto.PubkeyToAddress(s.Prv.PublicKey)
	}

	if _, err = s.ChainClient.CallContract(ctx, msg, nil); err == nil {
		return nil
	}
	if data := fccutils.ExtractRevertData(err); len(data) >= 4 {
		if named := fccutils.MatchCustomError(parsed, hex.EncodeToString(data)); named != "" {
			guard := named
			if before, _, found := strings.Cut(named, "["); found && before != "" {
				guard = before
			}
			return reject(guard, "the store rejects this batch in rehearsal: %s", named)
		}
	}
	if reason := fccutils.DecodeRevertReason(err); reason != "" {
		return reject("Reverted", "the store rejects this batch in rehearsal: %s", reason)
	}
	return errors.Errorf("rehearsing submitFeedUpdates: %s", err)
}
