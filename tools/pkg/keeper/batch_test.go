package keeper

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"fce-usdx-oracle/tools/pkg/feedupdate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var testChain = big.NewInt(114)

// signed produces an update the given machine key has signed, so chooseBatch
// can recover it the way the store would.
func signed(t *testing.T, key *ecdsa.PrivateKey, observedAt uint64, value int32) feedupdate.Update {
	t.Helper()
	u := feedupdate.Update{ExtensionID: big.NewInt(1), FeedID: feedID, Value: value, Decimals: 8, ObservedAt: observedAt}
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

// The store rejects a batch whose updates disagree on observedAt, so the machines
// that stamped a different instant are left out rather than sinking the round.
func TestChooseBatchKeepsTheLargestAgreeingGroup(t *testing.T) {
	k1, k2, k3 := newKey(t), newKey(t), newKey(t)
	updates := []feedupdate.Update{
		signed(t, k1, 100, 99_000_000),
		signed(t, k3, 101, 99_500_000),
		signed(t, k2, 100, 98_000_000),
	}

	batch, signers, err := chooseBatch(updates, testChain)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || batch[0].ObservedAt != 100 || batch[1].ObservedAt != 100 {
		t.Fatalf("batch = %+v, want the two updates stamped 100", batch)
	}
	want := []common.Address{crypto.PubkeyToAddress(k1.PublicKey), crypto.PubkeyToAddress(k2.PublicKey)}
	for i := range want {
		if signers[i] != want[i] {
			t.Errorf("signer %d = %s, want %s", i, signers[i].Hex(), want[i].Hex())
		}
	}
}

// Equal-sized groups: the newer instant wins, since the ratchet only moves forward.
func TestChooseBatchPrefersTheNewerInstantOnATie(t *testing.T) {
	updates := []feedupdate.Update{signed(t, newKey(t), 100, 1), signed(t, newKey(t), 101, 1)}
	batch, _, err := chooseBatch(updates, testChain)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || batch[0].ObservedAt != 101 {
		t.Fatalf("batch = %+v, want the update stamped 101", batch)
	}
}

// The store rejects a repeated signer, so one answer per machine goes up.
func TestChooseBatchTakesOneUpdatePerMachine(t *testing.T) {
	k := newKey(t)
	batch, signers, err := chooseBatch([]feedupdate.Update{signed(t, k, 100, 1), signed(t, k, 100, 2)}, testChain)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 1 || len(signers) != 1 || batch[0].Value != 1 {
		t.Fatalf("batch = %+v, want the machine's first answer only", batch)
	}
}

// An update that recovers to nobody cannot be submitted and must be named as such.
func TestChooseBatchRejectsWhenNothingIsSigned(t *testing.T) {
	_, _, err := chooseBatch([]feedupdate.Update{{ExtensionID: big.NewInt(1), ObservedAt: 100}}, testChain)
	rj, ok := AsRejection(err)
	if !ok || rj.Guard != "BadSignature" {
		t.Fatalf("err = %v, want a BadSignature rejection", err)
	}
}
