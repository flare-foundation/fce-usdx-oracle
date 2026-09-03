// Package provision verifies signed configuration envelopes.
//
//	body = abi.encode(domain, chainId, teeId, version, issuedAt, ref, payloadHash)
//
// signed by a threshold of distinct admins under EIP-191. version blocks rollback
// while the process lives, issuedAt survives restarts, and every rejection returns
// failure.ProvisionRejected alike, so the endpoint cannot be probed.
//
// ref picks a role-tagged signer set installed by SET_ADMINS — a mapping pinned by
// the caller, never read from the payload. Sets are memory-only, so SET_ADMINS must
// precede SET_SECRET after every restart. See docs/usdx-oracle-rationale.md §9.
package envelope

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

// Long enough to collect M signatures across timezones, short enough that a
// captured envelope expires quickly. The only replay bar surviving a restart.
const Freshness = time.Hour

const ClockSkew = 2 * time.Minute

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// Bounds recovery work before any cryptography runs; anyone who can deliver a
// direct action reaches this. Exported so the CLI can refuse a doomed envelope.
const MaxSignatures = 16

// Role tags, matching the bytes32 tags SET_ADMINS carries. The contract enforces
// only non-zero and unique, so what each tag governs is pinned here: the backing
// set governs the refs a Policy names, the providers set everything else.
const (
	RoleBacking   = "backing"
	RoleProviders = "providers"
)

// Roles lists every role tag a SET_ADMINS payload must carry. A missing role is an
// error, not a partial install: the sets go in together or not at all.
func Roles() []string { return []string{RoleBacking, RoleProviders} }

// Policy is the deployment's decision about the two signer sets: which refs the
// backing set governs, and the least threshold each set may be installed with.
// The contract validates neither, so the floors are the only guard. It is pinned
// in the build (internal/config) and never read from a payload: a payload naming
// its own refs would let the weaker set claim the stronger set's credentials.
type Policy struct {
	BackingRefs  []string
	MinBacking   int
	MinProviders int
}

// Validate reports whether every set could be installed under this policy.
func (p Policy) Validate() error {
	for _, role := range Roles() {
		floor, _ := p.FloorFor(role)
		if floor < 1 || floor > MaxThreshold {
			return fmt.Errorf("provision: %s floor %d is not between 1 and %d", role, floor, MaxThreshold)
		}
	}
	return nil
}

// FloorFor returns the least threshold for a role tag, and whether the tag is
// one this build recognises.
func (p Policy) FloorFor(role string) (int, bool) {
	switch role {
	case RoleBacking:
		return p.MinBacking, true
	case RoleProviders:
		return p.MinProviders, true
	}
	return 0, false
}

// RoleFor maps a ref to the role that governs it. Anything not named as a
// backing ref falls under the STRONGER providers set, including unknown refs.
func (p Policy) RoleFor(ref string) string {
	for _, r := range p.BackingRefs {
		if r == ref {
			return RoleBacking
		}
	}
	return RoleProviders
}

// MinThresholdFor returns the floor for whichever set governs ref. Fewer
// signatures than this can never be accepted anywhere, whatever SET_ADMINS
// published, so a CLI can refuse a doomed envelope before the trip.
func (p Policy) MinThresholdFor(ref string) int {
	floor, _ := p.FloorFor(p.RoleFor(ref))
	return floor
}

// MaxAdmins keeps isAdmin's linear scan from being sized by the payload.
const MaxAdmins = 32

// MaxThreshold is MaxSignatures: a higher threshold installs cleanly and is then
// unsatisfiable forever. Bounding the threshold rather than the set leaves M-of-N
// free up to the cap — a 3-of-20 only ever submits 3.
const MaxThreshold = MaxSignatures

// AdminSet is one signer set and the number of distinct members that must sign.
type AdminSet struct {
	Admins    []common.Address
	Threshold int
}

func (s AdminSet) installed() bool { return len(s.Admins) > 0 && s.Threshold > 0 }

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Envelope is a verified provisioning message.
type Envelope struct {
	ChainID  *big.Int
	TeeID    common.Address
	Version  uint64
	IssuedAt uint64
	Ref      string

	// The ciphertext the signatures commit to. The signed body carries
	// keccak256(Payload) rather than the payload itself, so the commitment is
	// derived here on encoding and checked against the body on decoding: a
	// decoded Envelope cannot disagree with what its admins signed.
	Payload []byte
}

var bodyArgs = mustArgs(
	argSpec{"Domain", "bytes32"},
	argSpec{"ChainID", "uint256"},
	argSpec{"TeeID", "address"},
	argSpec{"Version", "uint64"},
	argSpec{"IssuedAt", "uint64"},
	argSpec{"Ref", "string"},
	argSpec{"PayloadHash", "bytes32"},
)

var outerArgs = mustArgs(
	argSpec{"Body", "bytes"},
	argSpec{"Signatures", "bytes[]"},
	argSpec{"Payload", "bytes"},
)

// wireBody and wireOuter are the decoded forms of the two argument lists. Their
// field names are what Arguments.Copy matches the argument names against, so
// each layout is declared once rather than once per direction.
type wireBody struct {
	Domain      common.Hash
	ChainID     *big.Int
	TeeID       common.Address
	Version     uint64
	IssuedAt    uint64
	Ref         string
	PayloadHash common.Hash
}

type wireOuter struct {
	Body       []byte
	Signatures [][]byte
	Payload    []byte
}

// argSpec is one ABI argument's name and type. Names do not affect the encoding
// — they are what Arguments.Copy maps onto struct fields, and an unnamed
// argument cannot be copied into a struct at all.
type argSpec struct{ name, typ string }

func mustArgs(specs ...argSpec) abi.Arguments {
	args := make(abi.Arguments, len(specs))
	for i, spec := range specs {
		ty, err := abi.NewType(spec.typ, "", nil)
		if err != nil {
			panic("provision: invalid ABI type " + spec.typ + ": " + err.Error())
		}
		args[i] = abi.Argument{Name: spec.name, Type: ty}
	}
	return args
}

// EncodeBody returns the exact bytes admins sign. Exported so the provisioning
// CLI produces byte-identical input to what verification recomputes.
func EncodeBody(e Envelope) ([]byte, error) {
	if e.ChainID == nil {
		return nil, errors.New("provision: chain id is required")
	}
	if len(e.Payload) == 0 {
		return nil, errors.New("provision: payload is required")
	}
	return bodyArgs.Pack(
		Domain(), e.ChainID, e.TeeID, e.Version, e.IssuedAt, e.Ref, crypto.Keccak256Hash(e.Payload),
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
// NOT SAFE FOR CONCURRENT USE: Verify and SetAdmins both write
// unsynchronised state. store.Store holds applyMu across every call.
type Verifier struct {
	chainID *big.Int
	teeID   common.Address

	// Per-ref and in-memory; resets on restart, which is why issuedAt freshness
	// is also required.
	lastVersion map[string]uint64

	// Installed by SET_ADMINS, keyed by role tag, empty until then.
	sets map[string]AdminSet

	// Pinned at construction, never read from a payload.
	policy Policy

	now func() time.Time
}

// HasAdmins reports whether every role's signer set has been installed. All or
// nothing, so a machine cannot provision one class of credential and not the other.
func (v *Verifier) HasAdmins() bool {
	for _, role := range Roles() {
		if !v.sets[role].installed() {
			return false
		}
	}
	return true
}

// setFor selects the signer set governing a ref. See Policy.RoleFor: anything
// not named as a backing ref falls under the STRONGER set.
func (v *Verifier) setFor(ref string) AdminSet {
	return v.sets[v.policy.RoleFor(ref)]
}

// Policy returns the pinned policy the verifier was built with.
func (v *Verifier) Policy() Policy {
	p := v.policy
	p.BackingRefs = append([]string(nil), p.BackingRefs...)
	return p
}

// SetAdmins installs every role's signer set together or not at all.
// lastVersion is not reset: clearing it on rotation would let an
// envelope the outgoing admins signed be replayed against the incoming set.
func (v *Verifier) SetAdmins(sets map[string]AdminSet) error {
	// Validated into a fresh map before anything is installed, so a payload that
	// is good for one role and bad for another leaves the live sets untouched.
	installed := make(map[string]AdminSet, len(sets))
	for _, role := range Roles() {
		set, ok := sets[role]
		if !ok {
			return fmt.Errorf("provision: %s set: missing", role)
		}
		floor, _ := v.policy.FloorFor(role)
		if err := checkSet(set, floor); err != nil {
			return fmt.Errorf("provision: %s set: %w", role, err)
		}
		installed[role] = set
	}
	// A role this build does not know cannot be installed anywhere, so accepting
	// the payload would silently drop a set the publisher believes is in force.
	for role := range sets {
		if _, ok := v.policy.FloorFor(role); !ok {
			return fmt.Errorf("provision: unknown admin role %q", role)
		}
	}

	v.sets = installed
	return nil
}

func checkSet(s AdminSet, floor int) error {
	if len(s.Admins) == 0 || len(s.Admins) > MaxAdmins {
		return errors.New("admin set size out of range")
	}
	if s.Threshold < floor || s.Threshold > len(s.Admins) || s.Threshold > MaxThreshold {
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

// NewVerifier builds a verifier with no admin sets installed. The policy is
// pinned in the build, never read from a payload; an unusable one is a build
// error and is refused here rather than at the first SET_ADMINS.
func NewVerifier(chainID *big.Int, teeID common.Address, policy Policy) *Verifier {
	// A zero teeID would match the zero address an envelope carries when its
	// machine binding was never filled in, so every such envelope would pass the
	// one check that ties a credential to one enclave.
	if teeID == (common.Address{}) {
		panic("provision: teeID is zero — resolve the machine's address before building a verifier")
	}
	if err := policy.Validate(); err != nil {
		panic(err.Error())
	}
	policy.BackingRefs = append([]string(nil), policy.BackingRefs...)
	return &Verifier{
		chainID:     chainID,
		teeID:       teeID,
		policy:      policy,
		lastVersion: map[string]uint64{},
		sets:        map[string]AdminSet{},
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

	if len(sigs) > MaxSignatures {
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
	if e.TeeID != v.teeID {
		return Envelope{}, failure.ProvisionRejected
	}
	if e.Ref == "" || len(e.Ref) > 64 {
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

// hasThreshold reports whether set.Threshold distinct members signed; without
// deduplication one compromised key could supply the same signature M times.
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

	// go-ethereum's own validation, with homestead set so the high-s form is
	// rejected and one signature means one thing. Wider than the hand-rolled
	// low-s test it replaces, which admitted r or s of zero and either at or
	// above the curve order.
	r := new(big.Int).SetBytes(normalised[:32])
	s := new(big.Int).SetBytes(normalised[32:64])
	if !crypto.ValidateSignatureValues(normalised[64], r, s, true) {
		return common.Address{}, false
	}

	pub, err := crypto.SigToPub(digest, normalised)
	if err != nil || pub == nil {
		return common.Address{}, false
	}
	return crypto.PubkeyToAddress(*pub), true
}

func decode(message []byte) (Envelope, [][]byte, error) {
	values, err := outerArgs.Unpack(message)
	if err != nil || len(values) != 3 {
		return Envelope{}, nil, errors.New("bad outer encoding")
	}

	// Copy matches on the argument names, so a field added or reordered above is
	// an error here rather than a silently zeroed value.
	var outer wireOuter
	if err := outerArgs.Copy(&outer, values); err != nil {
		return Envelope{}, nil, errors.New("bad outer encoding")
	}

	values, err = bodyArgs.Unpack(outer.Body)
	if err != nil || len(values) != 7 {
		return Envelope{}, nil, errors.New("bad body encoding")
	}

	var body wireBody
	if err := bodyArgs.Copy(&body, values); err != nil {
		return Envelope{}, nil, errors.New("bad body encoding")
	}

	if body.Domain != Domain() {
		return Envelope{}, nil, errors.New("wrong domain")
	}

	// The signatures cover the hash, not the payload, so the payload delivered
	// alongside must be the one the body commits to.
	if len(outer.Payload) == 0 || crypto.Keccak256Hash(outer.Payload) != body.PayloadHash {
		return Envelope{}, nil, errors.New("payload does not match the signed hash")
	}

	return Envelope{
		ChainID:  body.ChainID,
		TeeID:    body.TeeID,
		Version:  body.Version,
		IssuedAt: body.IssuedAt,
		Ref:      strings.TrimSpace(body.Ref),
		Payload:  outer.Payload,
	}, outer.Signatures, nil
}
