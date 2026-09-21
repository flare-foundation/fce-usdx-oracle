// Package envelope verifies signed provisioning envelopes against the admin
// sets SET_ADMINS installed.
//
// The format is pkg/wire/envelope's: a body signed by a threshold of distinct
// admins under EIP-191, bound to one enclave, one ref and one ciphertext.
// Verification adds what only the enclave knows: which signer set the ref
// selects, that version strictly increases per ref while the process lives,
// that issuedAt sits inside the freshness window, which is the one replay bar
// that survives a restart, and that every rejection returns
// failure.ProvisionRejected alike, so the endpoint cannot be probed.
//
// Sets are memory-only, so SET_ADMINS must precede SET_SECRET after every
// restart. See docs/usdx-oracle-rationale.md §9.
package envelope

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/wire/eip191"
	wire "fce-usdx-oracle/pkg/wire/envelope"

	"github.com/ethereum/go-ethereum/common"
)

// AdminSet is one signer set and the number of distinct members that must sign.
type AdminSet struct {
	Admins    []common.Address
	Threshold int
}

func (s AdminSet) installed() bool { return len(s.Admins) > 0 && s.Threshold > 0 }

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
	policy protocol.Policy

	now func() time.Time
}

// HasAdmins reports whether every role's signer set has been installed. All or
// nothing, so a machine cannot provision one class of credential and not the other.
func (v *Verifier) HasAdmins() bool {
	for _, role := range protocol.Roles() {
		if !v.sets[role].installed() {
			return false
		}
	}
	return true
}

// setFor selects the signer set governing a ref. See protocol.Policy.RoleFor: anything
// not named as a backing ref falls under the STRONGER set.
func (v *Verifier) setFor(ref string) AdminSet {
	return v.sets[v.policy.RoleFor(ref)]
}

// Policy returns the pinned policy the verifier was built with.
func (v *Verifier) Policy() protocol.Policy {
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
	for _, role := range protocol.Roles() {
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
	if len(s.Admins) == 0 || len(s.Admins) > protocol.MaxAdmins {
		return errors.New("admin set size out of range")
	}
	if s.Threshold < floor || s.Threshold > len(s.Admins) || s.Threshold > protocol.MaxThreshold {
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
func NewVerifier(chainID *big.Int, teeID common.Address, policy protocol.Policy) *Verifier {
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
func (v *Verifier) Verify(message []byte) (wire.Envelope, error) {
	// Reported as the same rejection as everything else, so the endpoint does
	// not reveal whether a machine has been told who its admins are.
	if !v.HasAdmins() {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	e, sigs, err := wire.Decode(message)
	if err != nil {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	if len(sigs) > wire.MaxSignatures {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	set := v.setFor(e.Ref)
	if len(sigs) < set.Threshold {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	// Binding checks first: cheap, and they avoid signature recovery for an
	// envelope that could never apply.
	if v.chainID == nil || e.ChainID == nil || e.ChainID.Cmp(v.chainID) != 0 {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	// Bound to one machine, so a captured envelope cannot be redirected.
	if e.TeeID != v.teeID {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	if e.Ref == "" || len(e.Ref) > 64 {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	now := v.now()
	issued := time.Unix(int64(e.IssuedAt), 0).UTC()
	if e.IssuedAt == 0 {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	if now.Sub(issued) > wire.Freshness {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	if issued.Sub(now) > wire.ClockSkew {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	// Strictly increasing per ref, for the life of the process.
	if e.Version <= v.lastVersion[e.Ref] {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	body, err := wire.EncodeBody(e)
	if err != nil {
		return wire.Envelope{}, failure.ProvisionRejected
	}
	if !hasThreshold(body, sigs, set) {
		return wire.Envelope{}, failure.ProvisionRejected
	}

	v.lastVersion[e.Ref] = e.Version
	return e, nil
}

// hasThreshold reports whether set.Threshold distinct members signed; without
// deduplication one compromised key could supply the same signature M times.
func hasThreshold(body []byte, sigs [][]byte, set AdminSet) bool {
	seen := make(map[common.Address]bool, len(sigs))
	for _, sig := range sigs {
		signer, ok := eip191.Recover(body, sig)
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
	return slices.Contains(admins, a)
}
