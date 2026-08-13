// Package provision verifies signed configuration envelopes.
//
// RPC endpoints and the admin signer set arrive by on-chain instruction (so
// both are publicly verifiable, and the owner key is the root of trust for
// each); credentials arrive by direct instruction (no gas, no permanent
// ciphertext on-chain, instant rotation).
//
//	body = abi.encode(domain, chainId, teeId, version, issuedAt, ref, payloadHash)
//
// signed by at least the configured threshold of DISTINCT admins under EIP-191.
// version is in-memory and blocks rollback while the process lives; issuedAt
// survives restarts, where the version counter resets to zero. Every rejection
// returns failure.ProvisionRejected and nothing else, so the endpoint cannot be
// used to probe the admin set or the freshness window.
//
// The admin set is NOT baked in. A Verifier starts with none and rejects
// everything until SET_ADMINS installs one, so an enclave that has not been told
// who its admins are cannot be provisioned rather than defaulting to a set an
// attacker could guess. Because the set lives only in memory, a restart returns
// the machine to that state and SET_ADMINS must precede SET_SECRET every time.
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

// Distinct from the price report's domain so an admin signature can never be
// reinterpreted as a price, or the reverse.
const DomainName = "USDX_ORACLE_PROVISION_V1"

// Freshness is long enough to collect M signatures across timezones, short
// enough that a captured envelope expires quickly.
const Freshness = 15 * time.Minute

const ClockSkew = 2 * time.Minute

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// maxSignatures bounds work before any cryptography runs: recovery is expensive
// and this endpoint is reachable by anyone who can deliver a direct action.
const maxSignatures = 16

// MinThreshold is the floor on the admin threshold, enforced wherever a set is
// installed. M-of-N with M=1 is not a threshold scheme: one key would be the
// whole root of trust, which is the property SET_SECRET exists to avoid.
const MinThreshold = 2

// MaxAdmins bounds the published set. Recovery cost is linear in the signatures
// presented, not the admins configured, but an unbounded set would still make
// isAdmin a linear scan driven by whatever the payload named.
const MaxAdmins = 32

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Envelope is a verified provisioning message.
type Envelope struct {
	ChainID     *big.Int
	TeeID       common.Address
	Version     uint64
	IssuedAt    uint64
	Ref         string
	PayloadHash common.Hash

	// Verified to match PayloadHash before this is populated.
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

// Verifier checks envelopes against the installed admin set and tracks versions.
//
// NOT SAFE FOR CONCURRENT USE: Verify writes lastVersion, SetTeeID writes teeID
// and SetAdmins writes admins, none synchronised. runtimecfg.Store holds applyMu
// across every call. The extension's /action handler serves actions
// concurrently, so it does not do this for you.
type Verifier struct {
	chainID *big.Int
	teeID   common.Address

	// Per-ref and in-memory; resets on restart, which is why issuedAt freshness
	// is also required.
	lastVersion map[string]uint64

	// Installed by SET_ADMINS, empty until then. Nothing verifies against an
	// empty set: hasThreshold cannot reach a threshold of at least MinThreshold
	// with no admins to match, and HasAdmins lets callers say so explicitly.
	admins    []common.Address
	threshold int

	now func() time.Time
}

func (v *Verifier) HasTeeID() bool { return v.teeID != (common.Address{}) }

// SetTeeID is called once, lazily: tee-node may not be reachable at startup.
func (v *Verifier) SetTeeID(id common.Address) { v.teeID = id }

// HasAdmins reports whether an admin set has been installed.
func (v *Verifier) HasAdmins() bool { return len(v.admins) > 0 && v.threshold > 0 }

// SetAdmins installs the signer set that authorises provisioning, replacing any
// previous one. The caller validates the payload; this rechecks the invariants
// the verifier itself depends on, because a set that reached here malformed
// would weaken every later envelope rather than fail loudly at install time.
//
// lastVersion is deliberately NOT reset. Envelope versions are per-ref
// anti-rollback state, and clearing them on rotation would let an envelope the
// outgoing admins signed be replayed against the incoming set.
func (v *Verifier) SetAdmins(admins []common.Address, threshold int) error {
	if len(admins) == 0 || len(admins) > MaxAdmins {
		return errors.New("provision: admin set size out of range")
	}
	if threshold < MinThreshold || threshold > len(admins) {
		return errors.New("provision: admin threshold out of range")
	}

	seen := make(map[common.Address]bool, len(admins))
	installed := make([]common.Address, 0, len(admins))
	for _, a := range admins {
		if a == (common.Address{}) {
			return errors.New("provision: admin address is zero")
		}
		// A repeated address would inflate the apparent set size while the
		// distinct-signer check still counts it once, so a 2-of-3 naming the
		// same key twice would really be 2-of-2 with one key unusable.
		if seen[a] {
			return errors.New("provision: duplicate admin address")
		}
		seen[a] = true
		installed = append(installed, a)
	}

	v.admins = installed
	v.threshold = threshold
	return nil
}

func NewVerifier(chainID *big.Int, teeID common.Address) *Verifier {
	return &Verifier{
		chainID:     chainID,
		teeID:       teeID,
		lastVersion: map[string]uint64{},
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Verify decodes and validates a wire message. Every failure returns
// failure.ProvisionRejected and nothing else.
func (v *Verifier) Verify(message []byte) (Envelope, error) {
	// No admin set installed means nothing can be authorised yet. Checked first
	// and reported as the same rejection as everything else, so the endpoint
	// does not reveal whether a machine has been told who its admins are.
	if !v.HasAdmins() {
		return Envelope{}, failure.ProvisionRejected
	}

	e, sigs, err := decode(message)
	if err != nil {
		return Envelope{}, failure.ProvisionRejected
	}

	if len(sigs) < v.threshold || len(sigs) > maxSignatures {
		return Envelope{}, failure.ProvisionRejected
	}

	// Binding checks first: cheap, and they avoid signature recovery for an
	// envelope that could never apply.
	if v.chainID == nil || e.ChainID == nil || e.ChainID.Cmp(v.chainID) != 0 {
		return Envelope{}, failure.ProvisionRejected
	}
	// Bound to one machine, so a captured envelope cannot be redirected.
	if !v.HasTeeID() || e.TeeID != v.teeID {
		return Envelope{}, failure.ProvisionRejected
	}
	if e.Ref == "" || len(e.Ref) > 64 {
		return Envelope{}, failure.ProvisionRejected
	}
	if len(e.Payload) == 0 {
		return Envelope{}, failure.ProvisionRejected
	}
	// The signatures cover the hash, so an unverified payload would be unsigned
	// data smuggled past the admin set.
	if crypto.Keccak256Hash(e.Payload) != e.PayloadHash {
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

	// Strictly increasing per ref: blocks rolling a secret back within the life
	// of the process.
	if e.Version <= v.lastVersion[e.Ref] {
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
// Without deduplication one compromised key could supply the same signature M
// times and satisfy the threshold alone.
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

	// Reject the high-s form. secp256k1 is malleable, so accepting both would
	// let one admin signature be presented as two distinct-looking ones.
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
