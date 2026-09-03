// Preflight checks a batch of decoded feed updates against every store guard
// before spending gas. The cheap checks duplicate one contract rule each and turn
// a revert into a named, actionable reason; the last one is the contract's own
// dry run, an eth_call of submitFeedUpdates, which enforces everything else.
//
// The store verifies each TEE signature through Fdc2Verification, so the keeper
// holds no authority — the worst a wrong call can do is waste gas. Nothing here
// may compute or adjust a price, and the updates are relayed byte for byte.
package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
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
var feedID = mustFeedID("0x20555344582f555344000000000000000000000000")

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

// Preflight checks the batch against the store at storeAddr. signers are the
// machines recovered from each update's signature, in batch order; every update
// in the batch carries the same observedAt (see chooseBatch).
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

	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return errors.Errorf("reading extensionId from %s: %s", storeAddr.Hex(), err)
	}
	for i, u := range batch {
		if u.ExtensionID == nil || u.ExtensionID.Cmp(extensionID) != 0 {
			return reject("WrongExtensionId",
				"%s names extension %s, the store accepts %s",
				signers[i].Hex(), u.ExtensionID, extensionID)
		}
		// feedId has no getter (it is checked at init and never read again), so
		// a mismatch reverts WrongFeedId on chain rather than here. The keeper's
		// pinned feedID matches the store's by construction, and a divergence
		// means a deployment error, not a stale configuration.
		if u.FeedID != feedID {
			return reject("WrongFeedId",
				"%s names feed %#x, this keeper serves %#x", signers[i].Hex(), u.FeedID, feedID)
		}
	}

	senderAddr, err := store.InstructionsSender(opts)
	if err != nil {
		return errors.Errorf("reading the instructions sender: %s", err)
	}
	sender, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the sender: %s", err)
	}

	// The configuration hashes are feed-level: one latest published generation,
	// which every machine's update must carry. A machine still running the
	// previous one names itself here.
	latestEndpoints, err := sender.LatestEndpointsHash(opts, feedID)
	if err != nil {
		return reject("SenderUnavailable", "reading latestEndpointsHash: %s", err)
	}
	if common.Hash(latestEndpoints) == (common.Hash{}) {
		return reject("NoEndpointsPublished",
			"no endpoint configuration has been published for feed %#x — run send-endpoints first", feedID)
	}
	for i, u := range batch {
		if u.EndpointsHash != common.Hash(latestEndpoints) {
			return reject("StaleEndpoints",
				"%s computed its update under endpoints %s but the feed's latest is %s; "+
					"the machine never applied the published configuration, or a newer one was sent",
				signers[i].Hex(), u.EndpointsHash.Hex(), common.Hash(latestEndpoints).Hex())
		}
	}

	latestAdmins, err := sender.LatestAdminsHash(opts, feedID)
	if err != nil {
		return reject("SenderUnavailable", "reading latestAdminsHash: %s", err)
	}
	if common.Hash(latestAdmins) == (common.Hash{}) {
		return reject("NoAdminsPublished",
			"no admin set has been published for feed %#x — run send-admins first", feedID)
	}
	for i, u := range batch {
		if u.AdminsHash != common.Hash(latestAdmins) {
			return reject("StaleAdmins",
				"%s computed its update under admin set %s but the feed's latest is %s; "+
					"the machine never applied the published admin set, or a newer one was sent",
				signers[i].Hex(), u.AdminsHash.Hex(), common.Hash(latestAdmins).Hex())
		}
	}

	// The submission policy: how many distinct machines must sign one batch.
	// Fewer means waiting for the rest is the fix, or asking more machines.
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
			if i := strings.IndexByte(named, '['); i > 0 {
				guard = named[:i]
			}
			return reject(guard, "the store rejects this batch in rehearsal: %s", named)
		}
	}
	if reason := fccutils.DecodeRevertReason(err); reason != "" {
		return reject("Reverted", "the store rejects this batch in rehearsal: %s", reason)
	}
	return errors.Errorf("rehearsing submitFeedUpdates: %s", err)
}
