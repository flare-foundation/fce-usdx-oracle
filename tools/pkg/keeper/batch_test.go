package keeper

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"fce-usdx-oracle/pkg/protocol"
	wire "fce-usdx-oracle/pkg/wire/feedupdate"
	"fce-usdx-oracle/tools/pkg/feedupdate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	testChain     = big.NewInt(114)
	testEndpoints = common.HexToHash("0x9f3c")
	testAdmins    = common.HexToHash("0x5e21")
	// What the store and sender would report: the extension and the feed's
	// latest published generation, which every answer must carry.
	testCommitments = commitments{extensionID: big.NewInt(1), endpoints: testEndpoints, admins: testAdmins}
)

// signed produces an update the given machine key has signed, carrying the
// current commitments, so screening can recover it the way the store would.
func signed(t *testing.T, key *ecdsa.PrivateKey, observedAt uint64, value int32) feedupdate.Update {
	t.Helper()
	u := feedupdate.Update{FeedUpdate: wire.FeedUpdate{
		ExtensionID: big.NewInt(1), FeedID: protocol.FeedID, Value: value, Decimals: 8, ObservedAt: observedAt,
		EndpointsHash: testEndpoints, AdminsHash: testAdmins,
	}}
	return resign(t, key, u)
}

// resign signs u as it is, so a test can alter a field and still have a valid
// signature over the altered update.
func resign(t *testing.T, key *ecdsa.PrivateKey, u feedupdate.Update) feedupdate.Update {
	t.Helper()
	digest, err := u.Digest(testChain)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := crypto.Sign(digest.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	u.V = sig[64] + 27
	u.R = common.BytesToHash(sig[:32])
	u.S = common.BytesToHash(sig[32:64])
	return u
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func addr(key *ecdsa.PrivateKey) common.Address { return crypto.PubkeyToAddress(key.PublicKey) }

// screened runs the chain-free screen with every signer addressed, the way a
// cycle that asked exactly these machines would.
func screened(t *testing.T, keys []*ecdsa.PrivateKey, updates ...feedupdate.Update) ([]signedUpdate, []Dropped) {
	t.Helper()
	asked := make([]common.Address, 0, len(keys))
	for _, k := range keys {
		asked = append(asked, addr(k))
	}
	return screenUpdates(updates, testChain, asked, testCommitments)
}

func guards(dropped []Dropped) []string {
	out := make([]string, 0, len(dropped))
	for _, d := range dropped {
		out = append(out, d.Guard)
	}
	return out
}

// --- screening: one bad answer costs its machine the round, not the fleet ---

// An answer whose signature recovers to nobody is dropped by name; the rest go on.
func TestScreenDropsAnUnsignedAnswerAndKeepsTheRest(t *testing.T) {
	k := newKey(t)
	unsigned := feedupdate.Update{FeedUpdate: wire.FeedUpdate{ExtensionID: big.NewInt(1), FeedID: protocol.FeedID, ObservedAt: 100,
		EndpointsHash: testEndpoints, AdminsHash: testAdmins}}

	kept, dropped := screened(t, []*ecdsa.PrivateKey{k}, unsigned, signed(t, k, 100, 1))
	if len(kept) != 1 || kept[0].signer != addr(k) {
		t.Fatalf("kept = %+v, want the signed answer only", kept)
	}
	if len(dropped) != 1 || dropped[0].Guard != "BadSignature" || dropped[0].TeeID != "" {
		t.Fatalf("dropped = %+v, want one BadSignature with no machine", dropped)
	}
}

// A proxy can only put a signature the store would refuse anyway into the round;
// here it is dropped before it can sink the honest answers with it.
func TestScreenDropsAnswersFromMachinesNotAsked(t *testing.T) {
	asked, stranger := newKey(t), newKey(t)
	kept, dropped := screened(t, []*ecdsa.PrivateKey{asked},
		signed(t, stranger, 100, 1), signed(t, asked, 100, 2))

	if len(kept) != 1 || kept[0].signer != addr(asked) {
		t.Fatalf("kept = %+v, want the addressed machine's answer only", kept)
	}
	if len(dropped) != 1 || dropped[0].Guard != "NotAddressed" || dropped[0].TeeID != addr(stranger).Hex() {
		t.Fatalf("dropped = %+v, want the stranger named under NotAddressed", dropped)
	}
}

// No addressed list means no such restriction: a caller relaying answers it did
// not itself request still gets every recovered signer.
func TestScreenWithoutAddressedMachinesKeepsEverySigner(t *testing.T) {
	kept, dropped := screenUpdates(
		[]feedupdate.Update{signed(t, newKey(t), 100, 1), signed(t, newKey(t), 100, 2)},
		testChain, nil, testCommitments)
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("kept %d, dropped %d; want both kept", len(kept), len(dropped))
	}
}

// The case that used to stop the feed: one machine still running the previous
// configuration answers under the old hash. It alone is left out.
func TestScreenDropsOneStaleAnswerAndKeepsTheRest(t *testing.T) {
	k1, k2, lagging := newKey(t), newKey(t), newKey(t)
	stale := signed(t, lagging, 100, 3)
	stale.EndpointsHash = common.HexToHash("0x0001")
	stale = resign(t, lagging, stale)

	kept, dropped := screened(t, []*ecdsa.PrivateKey{k1, k2, lagging},
		signed(t, k1, 100, 1), stale, signed(t, k2, 100, 2))
	if len(kept) != 2 || kept[0].signer != addr(k1) || kept[1].signer != addr(k2) {
		t.Fatalf("kept = %+v, want k1 and k2 in arrival order", kept)
	}
	if len(dropped) != 1 || dropped[0].Guard != "StaleEndpoints" || dropped[0].TeeID != addr(lagging).Hex() {
		t.Fatalf("dropped = %+v, want the lagging machine under StaleEndpoints", dropped)
	}
}

// Every per-answer guard names itself, so the record says what to fix.
func TestScreenNamesEachGuard(t *testing.T) {
	k := newKey(t)
	wrongExt := signed(t, k, 100, 1)
	wrongExt.ExtensionID = big.NewInt(2)
	wrongFeed := signed(t, k, 100, 1)
	wrongFeed.FeedID[20] = 0xff
	staleAdmins := signed(t, k, 100, 1)
	staleAdmins.AdminsHash = common.HexToHash("0x0002")

	_, dropped := screened(t, []*ecdsa.PrivateKey{k},
		resign(t, k, wrongExt), resign(t, k, wrongFeed), resign(t, k, staleAdmins))
	want := []string{"WrongExtensionId", "WrongFeedId", "StaleAdmins"}
	got := guards(dropped)
	if len(got) != len(want) {
		t.Fatalf("guards = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("guard %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// --- grouping: one batch per observedAt, one answer per machine ---

// The store rejects a batch whose updates disagree on observedAt, so the machine
// that stamped a different instant is left out — and named — rather than
// sinking the round.
func TestChooseBatchKeepsTheLargestAgreeingGroup(t *testing.T) {
	k1, k2, k3 := newKey(t), newKey(t), newKey(t)
	members, _ := screened(t, []*ecdsa.PrivateKey{k1, k2, k3},
		signed(t, k1, 100, 99_000_000),
		signed(t, k3, 101, 99_500_000),
		signed(t, k2, 100, 98_000_000))

	batch, signers, dropped := chooseBatch(members)
	if len(batch) != 2 || batch[0].ObservedAt != 100 || batch[1].ObservedAt != 100 {
		t.Fatalf("batch = %+v, want the two updates stamped 100", batch)
	}
	want := []common.Address{addr(k1), addr(k2)}
	for i := range want {
		if signers[i] != want[i] {
			t.Errorf("signer %d = %s, want %s", i, signers[i].Hex(), want[i].Hex())
		}
	}
	if len(dropped) != 1 || dropped[0].Guard != "ObservedAtMismatch" || dropped[0].TeeID != addr(k3).Hex() {
		t.Fatalf("dropped = %+v, want k3 under ObservedAtMismatch", dropped)
	}
}

// Equal-sized groups: the newer instant wins, since the ratchet only moves forward.
func TestChooseBatchPrefersTheNewerInstantOnATie(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	members, _ := screened(t, []*ecdsa.PrivateKey{k1, k2}, signed(t, k1, 100, 1), signed(t, k2, 101, 1))
	batch, _, dropped := chooseBatch(members)
	if len(batch) != 1 || batch[0].ObservedAt != 101 {
		t.Fatalf("batch = %+v, want the update stamped 101", batch)
	}
	if len(dropped) != 1 || dropped[0].TeeID != addr(k1).Hex() {
		t.Fatalf("dropped = %+v, want k1", dropped)
	}
}

// The store rejects a repeated signer, so one answer per machine goes up and the
// repeat is named.
func TestChooseBatchTakesOneUpdatePerMachine(t *testing.T) {
	k := newKey(t)
	members, _ := screened(t, []*ecdsa.PrivateKey{k}, signed(t, k, 100, 1), signed(t, k, 100, 2))
	batch, signers, dropped := chooseBatch(members)
	if len(batch) != 1 || len(signers) != 1 || batch[0].Value != 1 {
		t.Fatalf("batch = %+v, want the machine's first answer only", batch)
	}
	if len(dropped) != 1 || dropped[0].Guard != "DuplicateTeeId" {
		t.Fatalf("dropped = %+v, want one DuplicateTeeId", dropped)
	}
}

// Nothing screened means nothing to choose, and no error: the caller names the
// round from the dropped list.
func TestChooseBatchWithNoMembersIsEmpty(t *testing.T) {
	batch, signers, dropped := chooseBatch(nil)
	if len(batch) != 0 || len(signers) != 0 || len(dropped) != 0 {
		t.Fatalf("batch=%v signers=%v dropped=%v, want all empty", batch, signers, dropped)
	}
}

// A round with nothing left is named by the one guard everything failed, so
// "every answer was unsigned" and "every answer was stale" stay distinguishable.
func TestDroppedGuardNamesTheOneGuardOrNoUsableUpdate(t *testing.T) {
	if g := droppedGuard([]Dropped{{Guard: "BadSignature"}, {Guard: "BadSignature"}}); g != "BadSignature" {
		t.Errorf("uniform drops named %q, want BadSignature", g)
	}
	if g := droppedGuard([]Dropped{{Guard: "BadSignature"}, {Guard: "StaleEndpoints"}}); g != "NoUsableUpdate" {
		t.Errorf("mixed drops named %q, want NoUsableUpdate", g)
	}
	if g := droppedGuard(nil); g != "NoUsableUpdate" {
		t.Errorf("no drops named %q, want NoUsableUpdate", g)
	}
}
