// Package provision verifies signed configuration envelopes.
//
//	body = abi.encode(domain, chainId, teeId, version, issuedAt, ref, payloadHash)
//
// signed by at least the configured threshold of DISTINCT admins under EIP-191.
// version blocks rollback while the process lives; issuedAt survives restarts,
// where the version counter resets to zero. Every rejection returns
// failure.ProvisionRejected and nothing else, so the endpoint cannot be used to
// probe the admin sets or the freshness window.
//
// The envelope's ref selects one of two signer sets: backing governs the pinned
// backing credential, providers governs every other one. Neither is baked in — a
// Verifier starts with none and rejects everything until SET_ADMINS installs
// both, and they live only in memory, so SET_ADMINS must precede SET_SECRET
// after every restart.
//
// See docs/usdx-oracle-rationale.md §9 for the reasoning behind all of this.
package provision

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Distinct from the price report's domain so the two signatures cannot cross.
const DomainName = "USDX_ORACLE_PROVISION_V1"

// Freshness is long enough to collect M signatures across timezones, short
// enough that a captured envelope expires quickly.
const Freshness = 15 * time.Minute

const ClockSkew = 2 * time.Minute

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// maxSignatures bounds recovery work before any cryptography runs; this
// endpoint is reachable by anyone who can deliver a direct action.
const maxSignatures = 16

// Floors per signer set. Backing is 1 by decision: it authenticates a pinned
// endpoint, so a wrong value degrades the feed rather than moving the price.
const (
	MinBackingThreshold  = 1
	MinProviderThreshold = 2
)

// MaxAdmins keeps isAdmin's linear scan from being sized by the payload.
const MaxAdmins = 32

// MinThresholdFor returns the floor for whichever set governs ref. Exported so
// the provisioning CLI applies the same rule the enclave does.
func MinThresholdFor(ref, backingRef string) int {
	if ref == backingRef {
		return MinBackingThreshold
	}
	return MinProviderThreshold
}

// AdminSet is one signer set and the number of DISTINCT members that must sign.
type AdminSet struct {
	Admins    []common.Address
	Threshold int
}

func (s AdminSet) installed() bool { return len(s.Admins) > 0 && s.Threshold > 0 }

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
// NOT SAFE FOR CONCURRENT USE: Verify, SetTeeID and SetAdmins all write
// unsynchronised state, and the extension's /action handler serves actions
// concurrently. runtimecfg.Store holds applyMu across every call.
type Verifier struct {
	chainID *big.Int
	teeID   common.Address

	// Per-ref and in-memory; resets on restart, which is why issuedAt freshness
	// is also required.
	lastVersion map[string]uint64

	// Installed by SET_ADMINS, empty until then.
	backing   AdminSet
	providers AdminSet

	// Pinned by the caller at construction, NEVER read from the payload: a
	// payload that named its own refs would let the weaker set claim the
	// stronger set's credentials.
	backingRef string

	now func() time.Time
}

func (v *Verifier) HasTeeID() bool { return v.teeID != (common.Address{}) }

// SetTeeID is called once, lazily: tee-node may not be reachable at startup.
func (v *Verifier) SetTeeID(id common.Address) { v.teeID = id }

// HasAdmins reports whether both signer sets have been installed. All or
// nothing, so a machine cannot provision one class of credential and not the other.
func (v *Verifier) HasAdmins() bool { return v.backing.installed() && v.providers.installed() }

// setFor selects the signer set that governs a ref. Anything but the pinned
// backing ref falls under the STRONGER set, including refs this build has never
// heard of.
func (v *Verifier) setFor(ref string) AdminSet {
	if ref == v.backingRef {
		return v.backing
	}
	return v.providers
}

// SetAdmins installs both signer sets together or not at all, replacing any
// previous pair. The caller validates the payload; this rechecks the invariants
// the verifier itself depends on.
//
// lastVersion is deliberately NOT reset: clearing it on rotation would let an
// envelope the outgoing admins signed be replayed against the incoming set.
func (v *Verifier) SetAdmins(backing, providers AdminSet) error {
	if err := checkSet(backing, MinBackingThreshold); err != nil {
		return fmt.Errorf("provision: backing set: %w", err)
	}
	if err := checkSet(providers, MinProviderThreshold); err != nil {
		return fmt.Errorf("provision: providers set: %w", err)
	}

	v.backing = backing
	v.providers = providers
	return nil
}

func checkSet(s AdminSet, floor int) error {
	if len(s.Admins) == 0 || len(s.Admins) > MaxAdmins {
		return errors.New("admin set size out of range")
	}
	if s.Threshold < floor || s.Threshold > len(s.Admins) {
		return errors.New("admin threshold out of range")
	}

	seen := make(map[common.Address]bool, len(s.Admins))
	for _, a := range s.Admins {
		if a == (common.Address{}) {
			return errors.New("admin address is zero")
		}
		// A 2-of-3 naming the same key twice is really 2-of-2, since the
		// distinct-signer check counts it once.
		if seen[a] {
			return errors.New("duplicate admin address")
		}
		seen[a] = true
	}
	return nil
}

// NewVerifier builds a verifier with no admin sets installed. backingRef names
// the one credential the backing set governs; it is pinned policy, not payload.
func NewVerifier(chainID *big.Int, teeID common.Address, backingRef string) *Verifier {
	return &Verifier{
		chainID:     chainID,
		teeID:       teeID,
		backingRef:  backingRef,
		lastVersion: map[string]uint64{},
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Verify decodes and validates a wire message. Every failure returns
// failure.ProvisionRejected and nothing else.
func (v *Verifier) Verify(message []byte) (Envelope, error) {
	// Reported as the same rejection as everything else, so the endpoint does
	// not reveal whether a machine has been told who its admins are.
	if !v.HasAdmins() {
		return Envelope{}, failure.ProvisionRejected
	}

	e, sigs, err := decode(message)
	if err != nil {
		return Envelope{}, failure.ProvisionRejected
	}

	if len(sigs) > maxSignatures {
		return Envelope{}, failure.ProvisionRejected
	}

	set := v.setFor(e.Ref)
	if len(sigs) < set.Threshold {
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
	// The signatures cover the hash, not the payload.
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

	// Strictly increasing per ref, for the life of the process.
	if e.Version <= v.lastVersion[e.Ref] {
		return Envelope{}, failure.ProvisionRejected
	}

	body, err := EncodeBody(e)
	if err != nil {
		return Envelope{}, failure.ProvisionRejected
	}
	if !hasThreshold(body, sigs, set) {
		return Envelope{}, failure.ProvisionRejected
	}

	v.lastVersion[e.Ref] = e.Version
	return e, nil
}

// hasThreshold reports whether at least set.Threshold DISTINCT members of set
// signed. Without deduplication one compromised key could supply the same
// signature M times. Signers from the other set do not count.
func hasThreshold(body []byte, sigs [][]byte, set AdminSet) bool {
	digest := accounts.TextHash(crypto.Keccak256(body))

	seen := make(map[common.Address]bool, len(sigs))
	for _, sig := range sigs {
		signer, ok := recover(digest, sig)
		if !ok {
			continue
		}
		if !isAdmin(set.Admins, signer) {
			continue
		}
		seen[signer] = true
	}
	return len(seen) >= set.Threshold
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

	// Reject the high-s form, so one signature means one thing.
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
