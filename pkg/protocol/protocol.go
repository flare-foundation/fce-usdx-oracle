// Package protocol pins what the USDX feed is on the wire: the op tags its
// commands travel under, the feed it serves, the credential refs and the admin
// roles that govern them, and the chains its supply is read from.
//
// Every value here is a fact about the protocol rather than about one
// implementation of it, so the extension and the tooling take them from the same
// place and a change lands in both by construction. The extension's operational
// settings, timeouts, ports and health names, stay in its internal/config.
package protocol

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"fce-usdx-oracle/pkg/wire/envelope"

	"github.com/ethereum/go-ethereum/common"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

// ReadinessRequestV1 is the one-byte GET_READINESS request. The command is
// direct-only, so nothing upstream defines its shape; anything else is rejected
// rather than ignored, so no caller depends on a dropped field.
const ReadinessRequestV1 = byte(0x01)

// The op type and commands as they travel: bytes32, the name's ASCII
// right-padded with zeros. Defined as the hash because that is the only form
// the handlers ever compare against, and building it here means no call site
// can pad it differently. They must match the upstream sender's bytes32
// constants exactly: a mismatch prices the wrong fee row and the instruction is
// rejected for an underpaid fee.
//
// One op type covers every command, so which channel each command is valid over
// is a security control of its own, enforced by the extension when an action is
// routed and when a configuration is applied.
var (
	OPTypeTeeOracle = teeutils.ToHash("TEE_ORACLE")

	// Instruction-only: the on-chain path is the permissionless one, and a
	// fallback path is one nobody notices has broken.
	OPCommandGetFeed = teeutils.ToHash("GET_FEED")

	// Direct-only, with no counterpart in the sender: a status answer is stale
	// within minutes and not worth a fee.
	OPCommandGetReadiness = teeutils.ToHash("GET_READINESS")

	// SET_ENDPOINTS and SET_ADMINS are instruction-only: the direct channel would
	// skip the sender's onlyGovernance check. SET_SECRET is direct-only: a
	// ciphertext on chain would be permanent and public.
	OPCommandSetEndpoints = teeutils.ToHash("SET_ENDPOINTS")
	OPCommandSetAdmins    = teeutils.ToHash("SET_ADMINS")
	OPCommandSetSecret    = teeutils.ToHash("SET_SECRET")
)

// OpName recovers the ASCII an op hash was built from, so a rejection can name
// the command rather than making an operator decode a bytes32 by hand. The
// padding is zeros, so trimming them is exact.
func OpName(h common.Hash) string {
	return strings.TrimRight(string(h[:]), "\x00")
}

// FeedID is the FTSO feed this extension serves: custom-feed category 0x21,
// then "USDX/USD" zero-padded to twenty bytes. It is signed into every update
// and checked by the store, so it is a build fact rather than a setting.
var FeedID = mustFeedID()

const feedIDHex = "0x21555344582f555344000000000000000000000000"

// mustFeedID parses the 21-byte feed id: a category byte plus a 20-byte name.
// The category must sit in [0x20, 0x40) or the store's initializer reverts
// InvalidFeedCategory; catching it here fails at startup rather than at deploy.
func mustFeedID() [21]byte {
	var id [21]byte
	raw, err := hex.DecodeString(strings.TrimPrefix(feedIDHex, "0x"))
	if err != nil || len(raw) != 21 {
		panic("protocol: feedIDHex must be 21 hex-encoded bytes, got " + feedIDHex)
	}
	if raw[0] < 0x20 || raw[0] >= 0x40 {
		panic("protocol: feedIDHex category byte must be in [0x20, 0x40), got " + feedIDHex)
	}
	copy(id[:], raw)
	return id
}

// The credential refs: names, never values. HexAPIKeyRef is the credential
// substituted into the reserve endpoints' X-API-Key header; HexCashURLRef is the
// provisioned entry carrying the restricted cash endpoint's URL, which the chain
// holds only as a salted commitment.
const (
	HexAPIKeyRef  = "hex_api_key"
	HexCashURLRef = "hex_cash_url"
)

// BackingRefs names the credentials the backing admin role governs. Pinned,
// never read from a payload: a payload naming its own refs would let the weaker
// signer set claim the stronger one's. Every other ref falls to the providers
// set.
func BackingRefs() []string {
	return []string{HexAPIKeyRef, HexCashURLRef}
}

// IsBackingRef reports whether ref is governed by the backing role.
func IsBackingRef(ref string) bool {
	return slices.Contains(BackingRefs(), ref)
}

// Role tags, matching the bytes32 tags SET_ADMINS carries. The contract
// enforces only non-zero and unique, so what each tag governs is pinned here:
// the backing set governs BackingRefs, the providers set everything else.
const (
	RoleBacking   = "backing"
	RoleProviders = "providers"
)

// Roles lists every role tag a SET_ADMINS payload must carry. A missing role is
// an error, not a partial install: the sets go in together or not at all.
func Roles() []string { return []string{RoleBacking, RoleProviders} }

// The floors are the least threshold each set may be installed with. The
// contract validates neither, so they are the only guard.
//
// Backing is 1-of-N by decision. It authenticates the pinned reserve sources, so
// a wrong credential degrades the feed rather than moving the price; unpin the
// bonds URL and this floor must rise. A provider credential is substituted into a
// URL the publisher chooses, hence 2.
const (
	MinBackingThreshold  = 1
	MinProviderThreshold = 2
)

// MaxAdmins bounds the size of a signer set, so a membership scan is never sized
// by the payload.
const MaxAdmins = 32

// MaxThreshold is the envelope's signature cap: a higher threshold installs
// cleanly and is then unsatisfiable forever. Bounding the threshold rather than
// the set leaves M-of-N free up to the cap; a 3-of-20 only ever submits 3.
const MaxThreshold = envelope.MaxSignatures

// Policy is the decision about the two signer sets: which refs the backing set
// governs, and the least threshold each set may be installed with. It is pinned
// in the build and never read from a payload.
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
			return fmt.Errorf("protocol: %s floor %d is not between 1 and %d", role, floor, MaxThreshold)
		}
	}
	return nil
}

// FloorFor returns the least threshold for a role tag, and whether the tag is
// one the protocol recognises.
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
	if slices.Contains(p.BackingRefs, ref) {
		return RoleBacking
	}
	return RoleProviders
}

// MinThresholdFor returns the floor for whichever set governs ref. Fewer
// signatures than this can never be accepted anywhere, whatever SET_ADMINS
// published, so a tool can refuse a doomed envelope before the trip.
func (p Policy) MinThresholdFor(ref string) int {
	floor, _ := p.FloorFor(p.RoleFor(ref))
	return floor
}

// AdminPolicy is the pinned policy: the enclave's verifier is built with it, and
// usdxprov applies the same rule before an envelope makes the trip.
func AdminPolicy() Policy {
	return Policy{
		BackingRefs:  BackingRefs(),
		MinBacking:   MinBackingThreshold,
		MinProviders: MinProviderThreshold,
	}
}

// Chain is a pinned per-chain fact. Provider lists and agreement thresholds are
// configured at runtime and published on chain.
type Chain struct {
	Name string

	// Pinned: another ERC-20 here would let whoever set it publish any price.
	Token string

	// Refuses a supply read whose pinned block header is older than this. Pinned:
	// the key that chooses the providers must not also choose this. See
	// docs/usdx-oracle-rationale.md §6.
	MaxBlockAge time.Duration
}

// Chains is the pinned chain set. USDX is a native burn-and-mint OFT on each,
// so supply is a plain sum; the pre-migration Ethereum contract is absent
// because Hex excludes it from reserves. Each name doubles as the SET_ENDPOINTS
// group tag. See docs/usdx-oracle-rationale.md §6.
var Chains = []Chain{
	{Name: "flare", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
	{Name: "ethereum", Token: "0xf8750b54d86BE7aE9e32b4A0C826811198D63313", MaxBlockAge: 3 * time.Minute},
	{Name: "songbird", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
}

// HomeChain's block header timestamp is a reading's own time (the observation's
// ObservedAt): a mined block's instant, which the host cannot move, rather than
// the enclave's wall clock. The signed update carries the requesting
// instruction's block timestamp instead, and the extension bounds how far
// behind that the inputs may have been read.
const HomeChain = "flare"

// LookupChain returns the pinned facts for a chain name.
func LookupChain(name string) (Chain, bool) {
	for _, c := range Chains {
		if c.Name == name {
			return c, true
		}
	}
	return Chain{}, false
}
