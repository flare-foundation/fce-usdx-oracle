// Preflight checks a decoded feed update against every store guard before
// spending gas. Each check duplicates one the contract makes, turning a revert
// into a named, actionable reason.
//
// The store verifies the TEE signature through Fdc2Verification, so the keeper
// holds no authority — the worst a wrong call can do is waste gas. Nothing here
// may compute or adjust a price, and the update is relayed byte for byte.
package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/feedupdate"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
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

func mustFeedID(s string) [21]byte {
	raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(raw) != 21 {
		panic("keeper: invalid feed id " + s)
	}
	var id [21]byte
	copy(id[:], raw)
	return id
}

// Rejection is an update the store will not accept, named by the guard that
// rejects it. The name matters more than the message: StaleEndpoints needs a
// setEndpoints call, NotNewer usually needs nothing, and "execution reverted"
// distinguishes neither.
type Rejection struct {
	Guard string
	Msg   string

	// Set for NotNewer, so the caller can tell a lost race from a stale
	// submission by comparing against the update's observedAt.
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

// Preflight checks u against the store at storeAddr. chainID is the chain the
// store is on — the same chain the update was signed against, since the chain
// id is inside the signed payload.
func Preflight(s *support.Support, storeAddr common.Address, chainID *big.Int, u feedupdate.Update) error {
	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the store: %s", err)
	}
	opts := &bind.CallOpts{Context: context.Background()}

	// Recover the signer once. The store does the same through
	// Fdc2Verification.verifyTeeSignature, and every check below needs the
	// machine id; recovering here turns a BadSignature into a named rejection
	// rather than a revert the keeper cannot distinguish from a registry fault.
	teeID, err := u.Signer(chainID)
	if err != nil {
		return reject("BadSignature", "the update does not recover to a signer: %s", err)
	}

	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return errors.Errorf("reading extensionId from %s: %s", storeAddr.Hex(), err)
	}
	if u.ExtensionID == nil || u.ExtensionID.Cmp(extensionID) != 0 {
		return reject("WrongExtensionId",
			"the update names extension %s, the store accepts %s",
			u.ExtensionID, extensionID)
	}

	// feedId has no getter (it is checked at init and never read again), so a
	// mismatch reverts WrongFeedId on chain rather than here. The keeper's
	// pinned feedID matches the store's by construction, and the signed update
	// names the same id — a divergence means a deployment error, not a stale
	// configuration, and the revert names it precisely.
	if u.FeedID != feedID {
		return reject("WrongFeedId",
			"the update names feed %#x, this keeper serves %#x",
			u.FeedID, feedID)
	}

	senderAddr, err := store.InstructionsSender(opts)
	if err != nil {
		return errors.Errorf("reading the instructions sender: %s", err)
	}
	sender, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the sender: %s", err)
	}

	// The registry checks that the signer is on this extension and in
	// PRODUCTION, but the store's verifyTeeSignature already enforces both —
	// and rejects while the extension is emergency paused. Re-checking here
	// would duplicate the on-chain call and could drift from it; the rejection
	// that matters is the one the store actually raises.

	expectedEndpoints, err := sender.ExpectedEndpointsHash(opts, u.FeedID, teeID)
	if err != nil {
		return reject("SenderUnavailable", "reading expectedEndpointsHash: %s", err)
	}
	if common.Hash(expectedEndpoints) == (common.Hash{}) {
		return reject("NoEndpointsPublished",
			"no endpoint configuration has been sent to %s for feed %#x — run send-endpoints first",
			teeID.Hex(), u.FeedID)
	}
	if common.Hash(expectedEndpoints) != u.EndpointsHash {
		return reject("StaleEndpoints",
			"the machine computed this update under endpoints %s but the sender expects %s; "+
				"the machine never applied the published configuration, or a newer one was sent",
			u.EndpointsHash.Hex(), common.Hash(expectedEndpoints).Hex())
	}

	expectedAdmins, err := sender.ExpectedAdminsHash(opts, u.FeedID, teeID)
	if err != nil {
		return reject("SenderUnavailable", "reading expectedAdminsHash: %s", err)
	}
	if common.Hash(expectedAdmins) == (common.Hash{}) {
		return reject("NoAdminsPublished",
			"no admin set has been sent to %s for feed %#x — run send-admins first",
			teeID.Hex(), u.FeedID)
	}
	if common.Hash(expectedAdmins) != u.AdminsHash {
		return reject("StaleAdmins",
			"the machine computed this update under admin set %s but the sender expects %s; "+
				"the machine never applied the published admin set, or a newer one was sent",
			u.AdminsHash.Hex(), common.Hash(expectedAdmins).Hex())
	}

	// Strictly increasing observedAt — the only time rule left. The store
	// rejects TooFarAhead (observedAt < block.timestamp) and NotNewer; there is
	// no maxAge and no maxFutureSkew, so the keeper's own cycle timing is what
	// keeps a relayed update inside the window. An update newer than the chain
	// is now impossible by construction (observedAt is the home chain's block
	// header timestamp, which is always in the past), so only NotNewer is
	// checked here.
	stored, err := store.ObservedAt(opts)
	if err != nil {
		return errors.Errorf("reading observedAt: %s", err)
	}
	if u.ObservedAt <= stored {
		rj := reject("NotNewer",
			"the store already holds observedAt=%d and this update is stamped %d",
			stored, u.ObservedAt)
		rj.StoreObservedAt = stored
		return rj
	}

	return nil
}
