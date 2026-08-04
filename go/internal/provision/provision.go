// Package provision verifies signed configuration envelopes.
//
// # Why configuration is not baked in
//
// Two things the extension needs are deliberately not compiled into the image:
//
//   - RPC endpoints, which arrive by on-chain instruction so that *which
//     providers we read* is publicly verifiable — that visibility is the point.
//   - Credentials, which arrive by direct instruction: no gas, no permanent
//     on-chain record of a ciphertext, instant rotation, and no secret-handling
//     path in Solidity.
//
// What stays baked is the part that must not be substitutable: the admin signer
// set that authorises provisioning. Note there is NO host allowlist: endpoint
// configuration is unconstrained, so whoever can send SET_ENDPOINTS can name
// any https host and any threshold, and the attested code will faithfully sign
// whatever those sources return. The published price is therefore only as
// trustworthy as the contract owner key as well as the TEE — a deliberate
// trade for rotation without rebuilds; see the findings doc §15.
//
// # The envelope
//
//	body = abi.encode(
//	    bytes32 domain,       keccak256("USDX_ORACLE_PROVISION_V1")
//	    uint256 chainId,      binds to one network
//	    address teeId,        binds to ONE machine
//	    uint64  version,      strictly increasing, blocks in-session rollback
//	    uint64  issuedAt,     freshness, blocks cross-session replay
//	    string  ref,          which secret or config slot this sets
//	    bytes32 payloadHash   keccak256 of the ciphertext or config blob
//	)
//
// signed by at least Threshold DISTINCT admins from the baked set, using the
// standard EIP-191 personal-sign convention so a hardware wallet can produce
// them.
//
// version and issuedAt are complementary, not redundant. version is held in
// memory and blocks replaying an older envelope while the process lives.
// issuedAt survives restarts, where the version counter resets to zero and
// would otherwise let a captured envelope be replayed indefinitely.
//
// # One failure reason
//
// Every rejection returns failure.ProvisionRejected. A caller learns whether an
// envelope was accepted, never which check failed — otherwise the endpoint
// becomes an oracle for probing the admin set, the freshness window, and which
// machine is which.
package provision

import (
	"errors"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is the provisioning domain separator's preimage. Distinct from the
// price report's domain so an admin signature can never be reinterpreted as a
// price, or the reverse.
const DomainName = "USDX_ORACLE_PROVISION_V1"

// Freshness is how long an envelope stays valid after issuedAt.
//
// Fifteen minutes: long enough to collect M signatures across timezones, short
// enough that a captured envelope expires quickly. The consequence is that
// recovering from a restart hours later needs a freshly signed envelope — which
// is the safe direction, and why the keeper holds ciphertext envelopes to
// re-submit promptly rather than relying on old ones.
const Freshness = 15 * time.Minute

// ClockSkew tolerates an envelope stamped slightly in the future.
const ClockSkew = 2 * time.Minute

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// maxSignatures bounds work before any cryptography runs. Recovery is the
// expensive operation and this endpoint is reachable by anyone who can deliver
// a direct action.
const maxSignatures = 16

// Threshold is how many distinct admin signatures an envelope needs.
const Threshold = 2

// Admins is the baked admin signer set — the root of trust for provisioning.
//
// ############################################################################
// # DEVELOPMENT PLACEHOLDERS. These are the addresses of private keys 1, 2    #
// # and 3 — the most widely known keys in existence. Anyone can sign as any   #
// # of these admins. They MUST be replaced before any deployment that serves  #
// # a live feed.                                                              #
// #                                                                           #
// # They are deliberately these rather than unusable literals like 0x1111…,   #
// # so the provisioning flow can be walked end to end locally. That           #
// # convenience is exactly why the warning is this loud.                      #
// #                                                                           #
// # TestAdminSetIsNotPlaceholder fails the moment they are replaced with      #
// # anything real, at which point delete the skip and make it a hard check.   #
// ############################################################################
var Admins = []common.Address{
	common.HexToAddress("0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"), // privkey 0x01
	common.HexToAddress("0x2B5AD5c4795c026514f8317c7a215E218DcCD6cF"), // privkey 0x02
	common.HexToAddress("0x6813Eb9362372EEF6200f3b1dbC3f819671cBA69"), // privkey 0x03
}

// Domain returns keccak256(DomainName).
func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Envelope is a verified provisioning message.
type Envelope struct {
	ChainID     *big.Int
	TeeID       common.Address
	Version     uint64
	IssuedAt    uint64
	Ref         string
	PayloadHash common.Hash

	// Payload is the ciphertext or config blob the hash commits to. Verified to
	// match PayloadHash before this is populated.
	Payload []byte
}

var bodyArgs = mustArgs("bytes32", "uint256", "address", "uint64", "uint64", "string", "bytes32")

var outerArgs = mustArgs("bytes", "bytes[]", "bytes")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("provision: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// EncodeBody returns the exact bytes admins sign. Exported so the provisioning
// CLI produces byte-identical input to what verification recomputes.
func EncodeBody(e Envelope) ([]byte, error) {
	if e.ChainID == nil {
		return nil, errors.New("provision: chain id is required")
	}
	return bodyArgs.Pack(
		Domain(), e.ChainID, e.TeeID, e.Version, e.IssuedAt, e.Ref, e.PayloadHash,
	)
}

// Encode builds the wire message: abi.encode(body, signatures, payload).
func Encode(e Envelope, signatures [][]byte) ([]byte, error) {
	body, err := EncodeBody(e)
	if err != nil {
		return nil, err
	}
	return outerArgs.Pack(body, signatures, e.Payload)
}

// Verifier checks envelopes against the baked admin set and tracks versions.
//
// Not safe for concurrent use; the extension serialises handler invocations, so
// the caller already holds a lock.
type Verifier struct {
	chainID *big.Int
	teeID   common.Address

	// lastVersion is per-ref and in-memory. It resets on restart, which is why
	// issuedAt freshness is also required — see the package doc.
	lastVersion map[string]uint64

	admins    []common.Address
	threshold int
	now       func() time.Time
}

// HasTeeID reports whether the verifier knows which machine it is.
func (v *Verifier) HasTeeID() bool { return v.teeID != (common.Address{}) }

// SetTeeID binds the verifier to a machine. Called once, lazily, because
// tee-node may not be reachable when the extension starts.
func (v *Verifier) SetTeeID(id common.Address) { v.teeID = id }

// NewVerifier returns a Verifier bound to one chain and one machine.
func NewVerifier(chainID *big.Int, teeID common.Address) *Verifier {
	return &Verifier{
		chainID:     chainID,
		teeID:       teeID,
		lastVersion: map[string]uint64{},
		admins:      Admins,
		threshold:   Threshold,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Verify decodes and validates a wire message.
//
// Every failure returns failure.ProvisionRejected and nothing else. The error
// carries no detail by construction.
func (v *Verifier) Verify(message []byte) (Envelope, error) {
	e, sigs, err := decode(message)
	if err != nil {
		return Envelope{}, failure.ProvisionRejected
	}

	if len(sigs) < v.threshold || len(sigs) > maxSignatures {
		return Envelope{}, failure.ProvisionRejected
	}

	// Binding checks first — they are cheap, and rejecting here avoids doing
	// signature recovery for an envelope that could never apply.
	if v.chainID == nil || e.ChainID == nil || e.ChainID.Cmp(v.chainID) != 0 {
		return Envelope{}, failure.ProvisionRejected
	}
	if !v.HasTeeID() || e.TeeID != v.teeID {
		// Bound to one machine: N machines means N envelopes, so a captured one
		// cannot be redirected to a different TEE.
		return Envelope{}, failure.ProvisionRejected
	}
	if e.Ref == "" || len(e.Ref) > 64 {
		return Envelope{}, failure.ProvisionRejected
	}
	if len(e.Payload) == 0 {
		return Envelope{}, failure.ProvisionRejected
	}
	if crypto.Keccak256Hash(e.Payload) != e.PayloadHash {
		// The signatures cover the hash, so an unverified payload would be
		// unsigned data smuggled past the admin set.
		return Envelope{}, failure.ProvisionRejected
	}

	now := v.now()
	issued := time.Unix(int64(e.IssuedAt), 0).UTC()
	if e.IssuedAt == 0 {
		return Envelope{}, failure.ProvisionRejected
	}
	if now.Sub(issued) > Freshness {
		return Envelope{}, failure.ProvisionRejected
	}
	if issued.Sub(now) > ClockSkew {
		return Envelope{}, failure.ProvisionRejected
	}

	if e.Version <= v.lastVersion[e.Ref] {
		// Strictly increasing per ref: blocks rolling a secret back to an older
		// value within the life of the process.
		return Envelope{}, failure.ProvisionRejected
	}

	body, err := EncodeBody(e)
	if err != nil {
		return Envelope{}, failure.ProvisionRejected
	}
	if !v.hasThreshold(body, sigs) {
		return Envelope{}, failure.ProvisionRejected
	}

	v.lastVersion[e.Ref] = e.Version
	return e, nil
}

// hasThreshold reports whether at least threshold DISTINCT admins signed.
//
// Distinctness is the whole point of M-of-N: without deduplication, one
// compromised key could supply the same signature M times and satisfy the
// threshold alone.
func (v *Verifier) hasThreshold(body []byte, sigs [][]byte) bool {
	digest := accounts.TextHash(crypto.Keccak256(body))

	seen := make(map[common.Address]bool, len(sigs))
	for _, sig := range sigs {
		signer, ok := recover(digest, sig)
		if !ok {
			continue
		}
		if !isAdmin(v.admins, signer) {
			continue
		}
		seen[signer] = true
	}
	return len(seen) >= v.threshold
}

func isAdmin(admins []common.Address, a common.Address) bool {
	for _, admin := range admins {
		if admin == a {
			return true
		}
	}
	return false
}

// recover accepts v as 27/28, the standard produced by personal-sign tooling
// and hardware wallets.
func recover(digest []byte, sig []byte) (common.Address, bool) {
	if len(sig) != SignatureLength {
		return common.Address{}, false
	}

	normalised := make([]byte, SignatureLength)
	copy(normalised, sig)

	switch normalised[64] {
	case 27, 28:
		normalised[64] -= 27
	case 0, 1:
		// Tolerated: some signers emit the raw recovery id.
	default:
		return common.Address{}, false
	}

	// Reject the high-s form. secp256k1 is malleable, so accepting both forms
	// would let one admin signature be presented as two distinct-looking ones —
	// which does not defeat the distinct-signer check, since both recover to the
	// same address, but rejecting keeps one signature meaning one thing.
	s := new(big.Int).SetBytes(normalised[32:64])
	if s.Cmp(halfOrder) > 0 {
		return common.Address{}, false
	}

	pub, err := crypto.SigToPub(digest, normalised)
	if err != nil || pub == nil {
		return common.Address{}, false
	}
	return crypto.PubkeyToAddress(*pub), true
}

// halfOrder is secp256k1's n/2.
var halfOrder, _ = new(big.Int).SetString(
	"7FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF5D576E7357A4501DDFE92F46681B20A0", 16)

func decode(message []byte) (Envelope, [][]byte, error) {
	outer, err := outerArgs.Unpack(message)
	if err != nil || len(outer) != 3 {
		return Envelope{}, nil, errors.New("bad outer encoding")
	}

	body, ok := outer[0].([]byte)
	if !ok {
		return Envelope{}, nil, errors.New("bad body")
	}
	sigs, ok := outer[1].([][]byte)
	if !ok {
		return Envelope{}, nil, errors.New("bad signatures")
	}
	payload, ok := outer[2].([]byte)
	if !ok {
		return Envelope{}, nil, errors.New("bad payload")
	}

	values, err := bodyArgs.Unpack(body)
	if err != nil || len(values) != 7 {
		return Envelope{}, nil, errors.New("bad body encoding")
	}

	domain, _ := values[0].([32]byte)
	if common.Hash(domain) != Domain() {
		return Envelope{}, nil, errors.New("wrong domain")
	}

	chainID, _ := values[1].(*big.Int)
	teeID, _ := values[2].(common.Address)
	version, _ := values[3].(uint64)
	issuedAt, _ := values[4].(uint64)
	ref, _ := values[5].(string)
	payloadHash, _ := values[6].([32]byte)

	return Envelope{
		ChainID:     chainID,
		TeeID:       teeID,
		Version:     version,
		IssuedAt:    issuedAt,
		Ref:         strings.TrimSpace(ref),
		PayloadHash: common.Hash(payloadHash),
		Payload:     payload,
	}, sigs, nil
}
