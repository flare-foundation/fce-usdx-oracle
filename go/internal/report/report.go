// Package report defines the main feed wire structs: the GET_FEED request envelope
// and the signed feed update.
//
//	Hashes:
//	  dataHash    = keccak256(abi.encode(FeedUpdate))
//	  messageHash = keccak256(abi.encode(prefix, chainId, dataHash))
//	  digest      = keccak256("\x19Ethereum Signed Message:\n32" ‖ messageHash)
//	  data        = abi.encode(FeedUpdate, Signature)
//
// This wire format matches SignedPayload.messageHash(TEE_ORACLE_FEED, ...) in
// flare-smart-contracts-v2. Key points:
//
//  1. prefix is bytes32("TEE_ORACLE_FEED"): plain ASCII, right-padded, not keccak256.
//  2. The signer receives the messageHash preimage (abi.encode(...)), not its hash;
//     tee-node hashes and signs using EIP-191. Passing the hash would sign the wrong data.
//  3. chainId is now signed, replacing the old version field—it prevents cross-chain replay.
//
// FCC is fire-and-forget, so replay protection is internal: extensionId/feedId identify the feed,
// observedAt is monotonic, endpointsHash and adminsHash commit to config and signer set.
// instructionId is removed; correlation uses tee-node action id.
//
// Requests now use an ABI-encoded FeedUpdateRequest (was a fixed byte) to support multiple feeds.
// This extension only serves one: DecodeFeedRequest only accepts that id, mirroring the store’s
// own update check.
//
// Every FeedUpdate field is static, so ABI encoding is always canonical and can be re-encoded freely.
// Only dynamic SET_ENDPOINTS and SET_ADMINS payloads must avoid re-serializing before hashing.
package report

import (
	"math"
	"math/big"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// PrefixName is the domain tag, cast to bytes32 rather than hashed. Shared by
// every deployed TEE oracle feed store — per-feed separation is the extensionId
// and feedId inside the update itself, not the prefix.
const PrefixName = "TEE_ORACLE_FEED"

// UpdateLength is the encoded FeedUpdate's length: seven static ABI words.
const UpdateLength = 7 * 32

// RequestLength is the encoded FeedUpdateRequest's length. Its one field is
// static, so the tuple encodes inline: a single word, no offset.
const RequestLength = 32

// PayloadLength is the encoded SignedPayload's length: three static ABI words.
const PayloadLength = 3 * 32

// DataLength is abi.encode(FeedUpdate, Signature): seven words then three.
const DataLength = UpdateLength + 3*32

// SignatureLength is r ‖ s ‖ v, the form tee-node returns.
const SignatureLength = 65

// Prefix returns bytes32(PrefixName): the ASCII bytes, right-padded with zeros.
func Prefix() common.Hash {
	var h common.Hash
	copy(h[:], PrefixName)
	return h
}

// FeedUpdate is the signed body. Field order is the struct's declaration order
// in ITeeOracleFeedStore, which is what abi.encode follows.
type FeedUpdate struct {
	// ExtensionID and FeedID name the exact feed this update is for. The store
	// rejects an update naming any other pair, so one machine's signature cannot
	// be replayed onto a second feed the same extension serves.
	ExtensionID *big.Int
	FeedID      [21]byte

	// Value is scaled by 10^Decimals. Signed and only 32 bits wide — see
	// ValueFromBig, which is the only way one should be produced.
	Value    int32
	Decimals int8

	ObservedAt uint64

	// keccak256 of the raw SET_ENDPOINTS payload as received, never
	// re-serialised: the contract hashes the message it dispatched, so the
	// enclave must hash the message it was handed.
	EndpointsHash common.Hash

	// keccak256 of the raw SET_ADMINS payload as received, on the same terms.
	AdminsHash common.Hash
}

// Signature is the store's {v, r, s} form. Splitting the 65-byte blob happens
// here rather than in the keeper, which stays a pure relay with no format
// knowledge to drift out of step.
type Signature struct {
	V uint8
	R common.Hash
	S common.Hash
}

// updateArgs is abi.encode(FeedUpdate). Every field is static, so the tuple
// encodes inline — no offset word — and a flat argument list produces the
// identical bytes. Same for dataArgs below, which is abi.encode(FeedUpdate,
// Signature) written as ten static fields.
var updateArgs = mustArgs(
	"uint256", // extensionId
	"bytes21", // feedId
	"int32",   // value
	"int8",    // decimals
	"uint64",  // observedAt
	"bytes32", // endpointsHash
	"bytes32", // adminsHash
)

// requestArgs is abi.encode(FeedUpdateRequest). One static field, so a flat
// argument list is byte-identical to the tuple — same reasoning as updateArgs.
var requestArgs = mustArgs("bytes21")

var payloadArgs = mustArgs(
	"bytes32", // prefix
	"uint256", // chainId
	"bytes32", // dataHash
)

var dataArgs = mustArgs(
	"uint256", "bytes21", "int32", "int8", "uint64", "bytes32", "bytes32",
	"uint8", "bytes32", "bytes32",
)

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("report: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// EncodeFeedRequest builds the GET_FEED instruction message for a feed. The
// enclave never sends one — the on-chain sender does — but the conformance
// harness and the tooling need to produce exactly what it dispatches.
func EncodeFeedRequest(feedID [21]byte) ([]byte, error) {
	if feedID == ([21]byte{}) {
		return nil, failure.Internal
	}
	message, err := requestArgs.Pack(feedID)
	if err != nil {
		return nil, failure.Internal
	}
	return message, nil
}

// DecodeFeedRequest reads a GET_FEED instruction message and returns the feed it
// asks for. Callers compare the result against their own pinned feed id: serving
// a request for another feed would produce an update the store rejects anyway,
// but only after the fee was paid and the answer written to a public log.
//
// The length is checked first: Unpack accepts a longer buffer and reads only the
// word it expects, so a request from a peer on a later layout must be rejected
// here rather than silently truncated.
func DecodeFeedRequest(message []byte) ([21]byte, error) {
	if len(message) != RequestLength {
		return [21]byte{}, failure.UnexpectedPayload
	}

	values, err := requestArgs.Unpack(message)
	if err != nil || len(values) != 1 {
		return [21]byte{}, failure.UnexpectedPayload
	}
	feedID, ok := values[0].([21]byte)
	if !ok || feedID == ([21]byte{}) {
		return [21]byte{}, failure.UnexpectedPayload
	}
	return feedID, nil
}

// ValueFromBig narrows a computed price to the store's int32 representation.
// Fails closed rather than truncating: at PriceDecimals = 8 the ceiling is
// ~21.47, so overflow means the price arithmetic went wrong, and a silently
// wrapped value would be published as fact.
func ValueFromBig(v *big.Int) (int32, error) {
	if v == nil || !v.IsInt64() {
		return 0, failure.Internal
	}
	n := v.Int64()
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, failure.Internal
	}
	return int32(n), nil
}

// validate rejects the store's sentinels and anything that could not be
// accepted, so a doomed update fails here rather than as an opaque revert.
func validate(u FeedUpdate) error {
	// Zero is the store's ZeroExtensionId sentinel, and an unset feed id means
	// the build was never given one — see config.ExtensionID.
	if u.ExtensionID == nil || u.ExtensionID.Sign() <= 0 {
		return failure.Internal
	}
	if u.FeedID == ([21]byte{}) {
		return failure.Internal
	}
	if u.Decimals < 0 {
		return failure.Internal
	}
	// Zero observedAt, zero endpointsHash and zero adminsHash are the store's
	// "never seen", NoEndpointsPublished and NoAdminsPublished sentinels; none
	// could ever be accepted.
	if u.ObservedAt == 0 {
		return failure.Internal
	}
	if u.EndpointsHash == (common.Hash{}) {
		return failure.Internal
	}
	if u.AdminsHash == (common.Hash{}) {
		return failure.Internal
	}
	return nil
}

// EncodeUpdate returns abi.encode(FeedUpdate).
func EncodeUpdate(u FeedUpdate) ([]byte, error) {
	if err := validate(u); err != nil {
		return nil, err
	}
	body, err := updateArgs.Pack(
		u.ExtensionID,
		u.FeedID,
		u.Value,
		u.Decimals,
		u.ObservedAt,
		u.EndpointsHash,
		u.AdminsHash,
	)
	if err != nil {
		return nil, failure.Internal
	}
	return body, nil
}

// DataHash is keccak256(abi.encode(FeedUpdate)).
func DataHash(u FeedUpdate) (common.Hash, error) {
	body, err := EncodeUpdate(u)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(body), nil
}

// SigningMessage returns the bytes to hand the signer: the SignedPayload
// preimage, abi.encode(prefix, chainId, dataHash). tee-node keccaks it and
// applies EIP-191, which lands exactly on the store's
// toEthSignedMessageHash(SignedPayload.messageHash(...)). See the package doc.
func SigningMessage(u FeedUpdate, chainID uint64) ([]byte, error) {
	if chainID == 0 {
		return nil, failure.Internal
	}
	dataHash, err := DataHash(u)
	if err != nil {
		return nil, err
	}
	message, err := payloadArgs.Pack(Prefix(), new(big.Int).SetUint64(chainID), dataHash)
	if err != nil {
		return nil, failure.Internal
	}
	return message, nil
}

// MessageHash is keccak256 of SigningMessage — the value the store computes.
func MessageHash(u FeedUpdate, chainID uint64) (common.Hash, error) {
	message, err := SigningMessage(u, chainID)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(message), nil
}

// Digest is what ECDSA recovers over. For verification and tooling; the enclave
// never computes it, because tee-node applies the EIP-191 wrapper itself.
func Digest(u FeedUpdate, chainID uint64) (common.Hash, error) {
	messageHash, err := MessageHash(u, chainID)
	if err != nil {
		return common.Hash{}, err
	}
	return common.BytesToHash(accounts.TextHash(messageHash.Bytes())), nil
}

// SplitSignature turns tee-node's r ‖ s ‖ v blob into the store's struct. v is
// required to be 27/28 — tee-node's Sign already normalises it, and the store's
// OpenZeppelin ECDSA rejects anything else — so a raw recovery id is a bug
// upstream of here rather than something to paper over.
func SplitSignature(signature []byte) (Signature, error) {
	if len(signature) != SignatureLength {
		return Signature{}, failure.Internal
	}
	v := signature[64]
	if v != 27 && v != 28 {
		return Signature{}, failure.Internal
	}
	return Signature{
		V: v,
		R: common.BytesToHash(signature[:32]),
		S: common.BytesToHash(signature[32:64]),
	}, nil
}

// EncodeData builds the ActionResult payload, abi.encode(FeedUpdate,
// Signature) — the two arguments submitFeedUpdate takes, in order, so the
// keeper decodes and forwards without reshaping anything.
func EncodeData(u FeedUpdate, signature []byte) ([]byte, error) {
	if err := validate(u); err != nil {
		return nil, err
	}
	sig, err := SplitSignature(signature)
	if err != nil {
		return nil, err
	}

	data, err := dataArgs.Pack(
		u.ExtensionID, u.FeedID, u.Value, u.Decimals, u.ObservedAt,
		u.EndpointsHash, u.AdminsHash,
		sig.V, sig.R, sig.S,
	)
	if err != nil {
		return nil, failure.Internal
	}
	return data, nil
}

// DecodeData reverses EncodeData.
func DecodeData(data []byte) (FeedUpdate, Signature, error) {
	// Length first: Unpack accepts a longer buffer and reads only the words it
	// expects, so a payload from a peer on a later layout must be rejected here
	// rather than silently truncated.
	if len(data) != DataLength {
		return FeedUpdate{}, Signature{}, failure.Internal
	}

	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 10 {
		return FeedUpdate{}, Signature{}, failure.Internal
	}

	extensionID, _ := values[0].(*big.Int)
	feedID, _ := values[1].([21]byte)
	value, _ := values[2].(int32)
	decimals, _ := values[3].(int8)
	observedAt, _ := values[4].(uint64)
	endpointsHash, _ := values[5].([32]byte)
	adminsHash, _ := values[6].([32]byte)
	v, _ := values[7].(uint8)
	r, _ := values[8].([32]byte)
	s, _ := values[9].([32]byte)

	if extensionID == nil {
		return FeedUpdate{}, Signature{}, failure.Internal
	}

	return FeedUpdate{
			ExtensionID:   extensionID,
			FeedID:        feedID,
			Value:         value,
			Decimals:      decimals,
			ObservedAt:    observedAt,
			EndpointsHash: common.Hash(endpointsHash),
			AdminsHash:    common.Hash(adminsHash),
		}, Signature{
			V: v,
			R: common.Hash(r),
			S: common.Hash(s),
		}, nil
}
