// Package config contains configuration values and defaults used by the extension.
package config

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"

	"fce-usdx-oracle/internal/provisioning/envelope"
)

const (
	Version = "0.1.0"

	// GET_FEED carries an ABI-encoded FeedUpdateRequest naming the feed (see
	// feedupdate.DecodeFeedRequest); this extension refuses any feed but its own.

	// Direct-only, so nothing upstream defines its shape. Anything else is
	// rejected rather than ignored, so no caller depends on a dropped field.
	ReadinessRequestV1 = byte(0x01)

	// Pinned though the store accepts any scale: every consumer assumes 8. int32
	// on the wire caps at ~21.47, and internal/wire/feedupdate fails closed out of range.
	PriceDecimals int8 = 8
)

// The op type and commands as they travel: bytes32, the name's ASCII
// right-padded with zeros. Defined as the hash because that is the only form
// the handlers ever compare against, and building it here means no call site
// can pad it differently.
//
// Must match the upstream sender's bytes32 constants exactly. One op type
// covers every command, so the per-command action-type guards in extension.go
// are the only fence between permissionless and governance-only commands.
var (
	OPTypeTeeOracle = teeutils.ToHash("TEE_ORACLE")

	// Instruction-only: the on-chain path is the permissionless one, and a
	// fallback path is one nobody notices has broken. Enforced in extension.go.
	OPCommandGetFeed = teeutils.ToHash("GET_FEED")

	// Direct-only, no counterpart in the sender: a status answer is stale within
	// minutes and not worth a fee. Enforced in extension.go.
	OPCommandGetReadiness = teeutils.ToHash("GET_READINESS")

	// SET_ENDPOINTS/SET_ADMINS are instruction-only: the direct channel would skip
	// the sender's onlyGovernance check. SET_SECRET is direct-only: a ciphertext
	// on-chain would be permanent and public. Enforced when a configuration is applied (internal/provisioning/store).
	OPCommandSetEndpoints = teeutils.ToHash("SET_ENDPOINTS")
	OPCommandSetAdmins    = teeutils.ToHash("SET_ADMINS")
	OPCommandSetSecret    = teeutils.ToHash("SET_SECRET")
)

// OpName recovers the ASCII an op hash was built from, so a rejection can name
// the command a machine understood rather than making an operator decode a
// bytes32 by hand. The padding is zeros, so trimming them is exact.
func OpName(h common.Hash) string {
	return strings.TrimRight(string(h[:]), "\x00")
}

// Source health per upstream in GET /state. Unknown is the zero value: a source
// that has never been polled must not read as healthy.
const (
	HealthUnknown = "unknown"
	HealthOK      = "ok"
	HealthDown    = "down"

	// Answered but lost the agreement vote: a WRONG value is a different problem
	// from no value.
	HealthDisagree = "disagree"

	// Head below the pinned block. Tolerated, but recorded: it is also how a
	// rewind looks.
	HealthLagging = "lagging"

	// Never provisioned, as opposed to broken: "down" pages someone,
	// "not_configured" tells them to send a SET_SECRET.
	HealthNotConfigured = "not_configured"
)

// The Docker image's port pair, and so the default: the sign server tee-node
// runs for the extension, and the extension's own /action + /state server, both
// loopback. EXTENSION_PORT and SIGN_PORT in the environment override them (they
// are in the image's launch-policy label for port collisions); cmd/start-tee,
// the host-process stack, passes its own pair explicitly instead, clear of the
// e2e simulation's ports.
var (
	ExtensionPort = 7702
	SignPort      = 7701
)

func init() {
	ep := os.Getenv("EXTENSION_PORT")
	sp := os.Getenv("SIGN_PORT")

	if ep != "" {
		if v, err := strconv.Atoi(ep); err == nil {
			ExtensionPort = v
		}
	}
	if sp != "" {
		if v, err := strconv.Atoi(sp); err == nil {
			SignPort = v
		}
	}
}

// Pinned deployment facts, covered by the registered code hash. Only RPC provider
// URLs, the cash endpoint's URL, and credentials are configurable at runtime.

const (
	ObservationDeadline = 20 * time.Second

	// Faster than Hex's ~10 minute post-event republish, so several consecutive
	// failures still fit inside MaxObservationAge.
	ObservationInterval = 2 * time.Minute

	// Oldest observation that may still be signed; past it GET_FEED publishes
	// nothing. The only staleness bound anywhere — replay is separately covered
	// by the strictly increasing observedAt ratchet.
	MaxObservationAge = 10 * time.Minute

	// Read skew between earliest and latest input, not data age. Sits above
	// ObservationDeadline, so the deadline binds first and this never fires.
	ObservationWindow = 30 * time.Second

	HTTPTimeout = 5 * time.Second

	// Names, never values: the credential substituted into X-API-Key, and the
	// provisioned entry carrying the cash endpoint's URL (see HexCashGroup).
	HexAPIKeyRef  = "hex_api_key"
	HexCashURLRef = "hex_cash_url"
)

// The bonds endpoint is pinned: redirecting it would hand whoever publishes
// configuration the numerator of the price. The cash URL is itself sensitive, so
// it travels as a PRIVATE endpoint whose salted commitment is all the chain sees.
const (
	HexBondsURL = "https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount"

	// OPEN: the endpoint returns 404 rather than 401 without credentials, so
	// bearer vs X-API-Key cannot be distinguished until a key is issued.
	HexCashAuthHeader = "X-API-Key"

	HexBondsName = "hex_bonds"
	HexCashName  = "hex_cash"

	// SET_ENDPOINTS group tag for the cash endpoint: exactly one entry, and it
	// must be PRIVATE or the restricted URL lands on-chain in the clear.
	// Enforced in internal/provisioning/store.
	HexCashGroup = HexCashName
)

// BackingRefs names the credentials the backing admin role governs. Pinned, never
// read from a payload: a payload naming its own refs would let the weaker signer
// set claim the stronger one's. Every other ref falls to the providers set.
func BackingRefs() []string {
	return []string{HexAPIKeyRef, HexCashURLRef}
}

// IsBackingRef reports whether ref is governed by the backing role.
func IsBackingRef(ref string) bool {
	for _, r := range BackingRefs() {
		if r == ref {
			return true
		}
	}
	return false
}

// The admin signer-set policy. SET_ADMINS publishes two role-tagged sets, and a
// SET_SECRET envelope must carry a threshold of signatures from the one that
// governs its ref: the backing set for BackingRefs, the providers set for every
// other ref. The floors are the least threshold a set may be installed with; the
// contract validates neither, so they are the only guard.
//
// Backing is 1-of-N by decision. It authenticates the pinned reserve sources
// (HexBondsURL above), so a wrong credential degrades the feed rather than
// moving the price. Unpin the bonds URL and this floor must rise. A provider
// credential is substituted into a URL the publisher chooses, hence 2.
const (
	MinBackingThreshold  = 1
	MinProviderThreshold = 2
)

// AdminPolicy is the pinned policy the envelope verifier is built with, and the
// rule usdxprov applies before an envelope makes the trip.
func AdminPolicy() envelope.Policy {
	return envelope.Policy{
		BackingRefs:  BackingRefs(),
		MinBacking:   MinBackingThreshold,
		MinProviders: MinProviderThreshold,
	}
}

// ChainHealthKey and ProviderHealthKey name a chain and one of its RPC
// providers in the sourceHealth map GET /state publishes.
//
// Both live here because both are public surface and they have to agree: the
// keys were formerly built in two packages that did not know of each other, so
// changing the prefix in one would have left the map with two naming schemes.
//
// Positional, never operator-supplied: a provider is "rpc_flare_1", so nothing
// written into a SET_ENDPOINTS payload can reach a world-readable surface.
func ChainHealthKey(chain string) string { return "rpc_" + chain }

func ProviderHealthKey(chain string, index int) string {
	return fmt.Sprintf("rpc_%s_%d", chain, index)
}

// Chain is a pinned per-chain fact. Provider lists and agreement thresholds are
// configured at runtime — see internal/provisioning/store.
type Chain struct {
	Name string

	// Pinned: another ERC-20 here would let whoever set it publish any price.
	Token string

	// Refuses a supply read whose pinned block header is older than this. Pinned:
	// the key that chooses the providers must not also choose this. Rationale §6.
	MaxBlockAge time.Duration
}

// Pinned chain set. USDX is a native burn-and-mint OFT on each, so supply is a
// plain sum; the pre-migration Ethereum contract is absent because Hex excludes it
// from reserves. Each name doubles as the SET_ENDPOINTS group tag. Rationale §6.
var Chains = []Chain{
	{Name: "flare", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
	{Name: "ethereum", Token: "0xf8750b54d86BE7aE9e32b4A0C826811198D63313", MaxBlockAge: 3 * time.Minute},
	{Name: "songbird", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
}

// HomeChain's block header timestamp stamps observedAt — any mined block would do,
// what matters is that it is in the past. The store requires observedAt <
// block.timestamp strictly, so a fast enclave clock would revert TooFarAhead.
const HomeChain = "flare"

// LookupChain returns the pinned policy for a chain name.
func LookupChain(name string) (Chain, bool) {
	for _, c := range Chains {
		if c.Name == name {
			return c, true
		}
	}
	return Chain{}, false
}

// ChainID is the chain every signed artefact is bound to, provisioning
// envelopes and feed updates alike. It is tee-node's chain: both read the same
// CHAIN_ID, so the node and the extension cannot disagree, and a process without
// it stops rather than sign for a chain nobody chose. Decimal or 0x-hex, as
// tee-node accepts it. Read on first use so the failure is a startup message.
func ChainID() uint64 {
	chainIDOnce.Do(func() {
		v, err := chainIDFrom(os.Getenv("CHAIN_ID"))
		if err != nil {
			panic("config: " + err.Error())
		}
		chainID = v
	})
	return chainID
}

var (
	chainIDOnce sync.Once
	chainID     uint64
)

func chainIDFrom(env string) (uint64, error) {
	s := strings.TrimSpace(env)
	if s == "" {
		return 0, fmt.Errorf("CHAIN_ID is not set; the extension signs for the chain tee-node is configured for and does not guess it")
	}
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil || v == 0 {
		return 0, fmt.Errorf("CHAIN_ID must be a positive integer, got %q", s)
	}
	return v, nil
}

// ExtensionID names the extension in every signed update, where the store,
// GET_FEED, SET_ENDPOINTS and SET_ADMINS check it. Like ChainID it is
// tee-node's value, read from the same EXTENSION_ID and mandatory for the same
// reason. FeedID is a build fact of this extension (feedIDHex).
func ExtensionID() *big.Int {
	extensionIDOnce.Do(func() {
		v, err := extensionIDFrom(os.Getenv("EXTENSION_ID"))
		if err != nil {
			panic("config: " + err.Error())
		}
		extensionID = v
	})
	return new(big.Int).Set(extensionID)
}

var (
	extensionIDOnce sync.Once
	extensionID     *big.Int

	FeedID = mustFeedID()

	// Custom-feed category 0x20, then "USDX/USD" zero-padded to twenty bytes.
	feedIDHex = "0x20555344582f555344000000000000000000000000"
)

func extensionIDFrom(env string) (*big.Int, error) {
	s := strings.TrimSpace(env)
	if s == "" {
		return nil, fmt.Errorf("EXTENSION_ID is not set; the extension serves the extension tee-node is configured for and does not guess it")
	}
	v, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), baseFor(s))
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("EXTENSION_ID %q is neither a decimal nor a 0x-prefixed hex integer", s)
	}
	return v, nil
}

func baseFor(s string) int {
	if strings.HasPrefix(s, "0x") {
		return 16
	}
	return 10
}

// ExtensionIDEnv returns ExtensionID as tee-node expects it in EXTENSION_ID, a
// 32-byte hex word, from however the operator wrote it: decimal, 0x-hex, or
// nothing, which means the configured id. A value naming another id is refused:
// the extension and the node read the same variable, so it can only mean a
// caller bypassed it.
func ExtensionIDEnv(raw string) (string, error) {
	if strings.TrimSpace(raw) != "" {
		v, err := extensionIDFrom(raw)
		if err != nil {
			return "", fmt.Errorf("config: %w", err)
		}
		if v.Cmp(ExtensionID()) != 0 {
			return "", fmt.Errorf("config: EXTENSION_ID %q names extension %s, but this process is configured for %s", raw, v, ExtensionID())
		}
	}
	return common.BigToHash(ExtensionID()).Hex(), nil
}

// mustFeedID parses the 21-byte FTSO feed id: a category byte plus a 20-byte name.
// The category must sit in [0x20, 0x40) or the store's initializer reverts
// InvalidFeedCategory — catching it here fails at startup rather than at deploy.
func mustFeedID() [21]byte {
	var id [21]byte

	raw, err := hex.DecodeString(strings.TrimPrefix(feedIDHex, "0x"))
	if err != nil || len(raw) != 21 {
		panic("config: feedIDHex must be 21 hex-encoded bytes, got " + feedIDHex)
	}
	if raw[0] < 0x20 || raw[0] >= 0x40 {
		panic("config: feedIDHex category byte must be in [0x20, 0x40), got " + feedIDHex)
	}

	copy(id[:], raw)
	return id
}

// Neither reserve source carries a freshness bound: Hex republishes on a mint or a
// redemption, not on a schedule, so last_update is a change timestamp and a bound
// flapped good data to `down`. Feed age is bounded by MaxObservationAge instead.

// BondsSource is Hex's public reserves endpoint. RequireCurrency must stay false:
// the public endpoint omits the field entirely.
func BondsSource() BackingSpec {
	return BackingSpec{
		Name: HexBondsName, URL: HexBondsURL,
		RequireCurrency: false,
		Timeout:         HTTPTimeout,
	}
}

// CashSource is Hex's restricted endpoint plus the injected credential. The URL
// arrives as a PRIVATE endpoint whose commitment the hex_cash group pins.
func CashSource(url, apiKey string) BackingSpec {
	return BackingSpec{
		Name: HexCashName, URL: url,
		RequireCurrency: true,
		Timeout:         HTTPTimeout,
		AuthHeader:      HexCashAuthHeader, AuthValue: apiKey,
	}
}

// BackingSpec mirrors backing.Config; declared here because internal/observation/backing
// cannot be imported from this package without a cycle.
type BackingSpec struct {
	Name            string
	URL             string
	RequireCurrency bool
	Timeout         time.Duration
	AuthHeader      string
	AuthValue       string
}
