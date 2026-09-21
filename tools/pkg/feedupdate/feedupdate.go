// Package feedupdate decodes the payload a TEE machine returns for GET_FEED and
// recovers the machine that signed it.
//
//	data        = abi.encode(FeedUpdate, Signature)
//	dataHash    = keccak256(abi.encode(FeedUpdate))
//	messageHash = keccak256(abi.encode(prefix, chainId, dataHash))
//	digest      = keccak256("\x19Ethereum Signed Message:\n32" ‖ messageHash)
//
// The layout and the digest are the extension's, imported from
// pkg/wire/feedupdate; both are pinned to Solidity's own bytes in
// deploy/testdata/wire. What this package adds is what the enclave never does
// with its own output: recover the signer, and render the update for an
// operator.
//
// Recovering the signer matters for one reason: when the store rejects an
// update, the revert says WHAT was wrong (StaleEndpoints, NotNewer) but never
// WHO signed it. A relayer that can name the machine turns a fleet-wide "someone
// is misconfigured" into one machine to go and look at.
package feedupdate

import (
	"fmt"
	"math/big"

	wire "fce-usdx-oracle/pkg/wire/feedupdate"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// PrefixName and DataLength are the extension's, restated by name only.
const (
	PrefixName = wire.PrefixName
	DataLength = wire.DataLength
)

// Prefix returns bytes32(PrefixName).
func Prefix() common.Hash { return wire.Prefix() }

// FeedUpdate and Signature are the extension's wire types, so a caller can
// build an Update without importing the enclave's package by name.
type (
	FeedUpdate = wire.FeedUpdate
	Signature  = wire.Signature
)

// Update is one decoded, signed feed update: the body and the signature over it,
// with every field of both reachable directly.
type Update struct {
	FeedUpdate
	Signature
}

// Decode reads an ActionResult payload.
func Decode(data []byte) (Update, error) {
	// Length first, with the length in the message: the extension's decoder
	// refuses a wrong length too, but says nothing about how wrong.
	if len(data) != DataLength {
		return Update{}, errors.Errorf("payload is %d bytes, want %d", len(data), DataLength)
	}
	u, sig, err := wire.DecodeData(data)
	if err != nil {
		return Update{}, errors.New("payload is not an encoded feed update")
	}
	return Update{FeedUpdate: u, Signature: sig}, nil
}

// Digest recomputes what the store's ECDSA.recover is called with. chainID is
// the chain the STORE is on, and is inside the signed payload: the same update
// recovers to a different address against the wrong one, which reads as a
// stranger's signature rather than as a chain mix-up.
func (u Update) Digest(chainID *big.Int) (common.Hash, error) {
	if chainID == nil || chainID.Sign() <= 0 {
		return common.Hash{}, errors.New("chain id is required")
	}
	if !chainID.IsUint64() {
		return common.Hash{}, errors.Errorf("chain id %s does not fit the signed payload", chainID)
	}
	digest, err := wire.Digest(u.FeedUpdate, chainID.Uint64())
	if err != nil {
		return common.Hash{}, errors.Errorf("computing the digest: %s", err)
	}
	return digest, nil
}

// Signer recovers the TEE machine that signed this update.
func (u Update) Signer(chainID *big.Int) (common.Address, error) {
	digest, err := u.Digest(chainID)
	if err != nil {
		return common.Address{}, err
	}

	// ECDSA.recover takes v as 27/28; SigToPub wants the raw recovery id.
	if u.V != 27 && u.V != 28 {
		return common.Address{}, errors.Errorf("signature v is %d, want 27 or 28", u.V)
	}
	sig := make([]byte, 65)
	copy(sig[:32], u.R.Bytes())
	copy(sig[32:64], u.S.Bytes())
	sig[64] = u.V - 27

	pub, err := crypto.SigToPub(digest.Bytes(), sig)
	if err != nil || pub == nil {
		return common.Address{}, errors.Errorf("recovering signer: %s", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// Price renders the value at its own scale, for operator output.
func (u Update) Price() string {
	if u.Decimals <= 0 {
		return fmt.Sprintf("%d", u.Value)
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(u.Decimals)), nil)
	whole := new(big.Int).Quo(big.NewInt(int64(u.Value)), scale)
	frac := new(big.Int).Rem(big.NewInt(int64(u.Value)), scale)
	if frac.Sign() < 0 {
		frac.Neg(frac)
	}
	return fmt.Sprintf("%s.%0*s", whole, int(u.Decimals), frac.String())
}

// FeedName renders the feed id the way it is written: a category byte, then the
// ASCII name.
func (u Update) FeedName() string {
	name := u.FeedID[1:]
	end := len(name)
	for end > 0 && name[end-1] == 0 {
		end--
	}
	return fmt.Sprintf("%d:%s", u.FeedID[0], string(name[:end]))
}

// Human is the operator-facing summary.
func (u Update) Human() string {
	return fmt.Sprintf(
		"feed %s  value %s (%d @ %d decimals)  observedAt %d  extension %s",
		u.FeedName(), u.Price(), u.Value, u.Decimals, u.ObservedAt, u.ExtensionID)
}
