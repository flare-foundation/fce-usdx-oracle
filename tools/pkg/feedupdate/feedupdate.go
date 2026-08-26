// Package feedupdate decodes the payload a TEE machine returns for GET_FEED and
// recovers the machine that signed it.
//
//	data        = abi.encode(FeedUpdate, Signature)
//	dataHash    = keccak256(abi.encode(FeedUpdate))
//	messageHash = keccak256(abi.encode(prefix, chainId, dataHash))
//	digest      = keccak256("\x19Ethereum Signed Message:\n32" ‖ messageHash)
//
// This restates the layout rather than importing the enclave's copy in
// go/internal/report, so the relayer is an independent check on the machine
// instead of a mirror of it. Both are pinned to the same Solidity-generated
// fixtures in testdata/wire — see that directory's README.
//
// Recovering the signer here matters for one reason: when the store rejects an
// update, the revert says WHAT was wrong (StaleEndpoints, NotNewer) but never
// WHO signed it. A relayer that can name the machine turns a fleet-wide "someone
// is misconfigured" into one machine to go and look at.
package feedupdate

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// PrefixName is the signed payload's domain tag. bytes32("TEE_ORACLE_FEED") —
// the ASCII string right-padded with zeros, NOT keccak256 of it.
const PrefixName = "TEE_ORACLE_FEED"

// DataLength is abi.encode(FeedUpdate, Signature): seven static words then three.
const DataLength = 10 * 32

// Prefix returns bytes32(PrefixName).
func Prefix() common.Hash {
	var h common.Hash
	copy(h[:], PrefixName)
	return h
}

// Update is one decoded, signed feed update.
type Update struct {
	ExtensionID   *big.Int
	FeedID        [21]byte
	Value         int32
	Decimals      int8
	ObservedAt    uint64
	EndpointsHash common.Hash
	AdminsHash    common.Hash

	V uint8
	R common.Hash
	S common.Hash
}

// Every field is static, so the two structs encode inline and a flat argument
// list is byte-identical to abi.encode(FeedUpdate, Signature).
var dataArgs = mustArgs(
	"uint256", "bytes21", "int32", "int8", "uint64", "bytes32", "bytes32",
	"uint8", "bytes32", "bytes32",
)

var updateArgs = mustArgs(
	"uint256", "bytes21", "int32", "int8", "uint64", "bytes32", "bytes32",
)

var payloadArgs = mustArgs("bytes32", "uint256", "bytes32")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("feedupdate: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// Decode reads an ActionResult payload.
func Decode(data []byte) (Update, error) {
	// Length first: Unpack accepts a longer buffer and reads only the words it
	// expects, so a payload from a machine on a later layout must be rejected
	// here rather than silently truncated into a plausible-looking update.
	if len(data) != DataLength {
		return Update{}, errors.Errorf("payload is %d bytes, want %d", len(data), DataLength)
	}

	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 10 {
		return Update{}, errors.Errorf("payload is not an encoded feed update: %v", err)
	}

	extensionID, _ := values[0].(*big.Int)
	if extensionID == nil {
		return Update{}, errors.New("payload carries no extension id")
	}
	feedID, _ := values[1].([21]byte)
	value, _ := values[2].(int32)
	decimals, _ := values[3].(int8)
	observedAt, _ := values[4].(uint64)
	endpointsHash, _ := values[5].([32]byte)
	adminsHash, _ := values[6].([32]byte)
	v, _ := values[7].(uint8)
	r, _ := values[8].([32]byte)
	sig, _ := values[9].([32]byte)

	return Update{
		ExtensionID:   extensionID,
		FeedID:        feedID,
		Value:         value,
		Decimals:      decimals,
		ObservedAt:    observedAt,
		EndpointsHash: common.Hash(endpointsHash),
		AdminsHash:    common.Hash(adminsHash),
		V:             v,
		R:             common.Hash(r),
		S:             common.Hash(sig),
	}, nil
}

// Digest recomputes what the store's ECDSA.recover is called with. chainID is
// the chain the STORE is on, and is inside the signed payload: the same update
// recovers to a different address against the wrong one, which reads as a
// stranger's signature rather than as a chain mix-up.
func (u Update) Digest(chainID *big.Int) (common.Hash, error) {
	if chainID == nil || chainID.Sign() <= 0 {
		return common.Hash{}, errors.New("chain id is required")
	}

	body, err := updateArgs.Pack(
		u.ExtensionID, u.FeedID, u.Value, u.Decimals, u.ObservedAt,
		u.EndpointsHash, u.AdminsHash,
	)
	if err != nil {
		return common.Hash{}, errors.Errorf("encoding feed update: %s", err)
	}

	message, err := payloadArgs.Pack(Prefix(), chainID, crypto.Keccak256Hash(body))
	if err != nil {
		return common.Hash{}, errors.Errorf("encoding signed payload: %s", err)
	}
	return common.BytesToHash(accounts.TextHash(crypto.Keccak256(message))), nil
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
