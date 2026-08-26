// Package config contains configuration values and defaults used by the extension.
package config

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

const (
	Version = "0.1.0"

	TimeoutShutdown = 5 * time.Second

	// GET_FEED carries an ABI-encoded FeedUpdateRequest naming the feed (see
	// report.DecodeFeedRequest); this extension refuses any feed but its own.

	// Direct-only, so nothing upstream defines its shape. Anything else is
	// rejected rather than ignored, so no caller depends on a dropped field.
	ReadinessRequestV1 = byte(0x01)

	// Pinned though the store accepts any scale: every consumer assumes 8. int32
	// on the wire caps at ~21.47, and internal/report fails closed out of range.
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
	// on-chain would be permanent and public. Enforced in config_handler.go.
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
	HealthUnknown  = "unknown"
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthDown     = "down"

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

var (
	ExtensionPort = 8080
	SignPort      = 9090
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
	// Enforced in internal/runtimecfg.
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
// configured at runtime — see internal/runtimecfg.
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

// ChainID binds provisioning envelopes to one network and is signed into every
// feed update. Set with -ldflags "-X fce-usdx-oracle/internal/config.chainIDStr=14".
var ChainID = mustChainID()

var chainIDStr = "114"

func mustChainID() uint64 {
	v, err := strconv.ParseUint(chainIDStr, 10, 64)
	if err != nil || v == 0 {
		panic("config: chainIDStr must be a positive integer, got " + chainIDStr)
	}
	return v
}

// Named inside every signed update and checked by the store, GET_FEED,
// SET_ENDPOINTS and SET_ADMINS. Pinned like the token addresses, and
// ldflags-overridable via extensionIDStr/feedIDHex for a test deployment.
var (
	ExtensionID = mustExtensionID()
	FeedID      = mustFeedID()
)

var (
	// Reserved id 1, per upstream's chain-config.
	extensionIDStr = "1"

	// Custom-feed category 0x20, then "USDX/USD" zero-padded to twenty bytes.
	feedIDHex = "0x20555344582f555344000000000000000000000000"
)

func mustExtensionID() *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimPrefix(extensionIDStr, "0x"), baseFor(extensionIDStr))
	if !ok || v.Sign() < 0 {
		panic("config: extensionIDStr must be a non-negative integer, got " + extensionIDStr)
	}
	return v
}

func baseFor(s string) int {
	if strings.HasPrefix(s, "0x") {
		return 16
	}
	return 10
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

// BackingSpec mirrors backing.Config; declared here because internal/backing
// cannot be imported from this package without a cycle.
type BackingSpec struct {
	Name            string
	URL             string
	RequireCurrency bool
	Timeout         time.Duration
	AuthHeader      string
	AuthValue       string
}
