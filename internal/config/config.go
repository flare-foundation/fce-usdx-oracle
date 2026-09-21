// Package config contains configuration values and defaults used by the extension.
package config

import (
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"fce-usdx-oracle/pkg/protocol"
)

const (
	Version = "0.1.0"

	// GET_FEED carries an ABI-encoded FeedUpdateRequest naming the feed (see
	// feedupdate.DecodeFeedRequest); this extension refuses any feed but its own.

	// Direct-only, so nothing upstream defines its shape. Anything else is
	// rejected rather than ignored, so no caller depends on a dropped field.
	ReadinessRequestV1 = protocol.ReadinessRequestV1

	// Pinned though the store accepts any scale: every consumer assumes 8. int32
	// on the wire caps at ~21.47, and pkg/wire/feedupdate fails closed out of range.
	PriceDecimals int8 = 8
)

// The op type and commands, the extension's ids and the admin policy are the
// protocol's, pinned in pkg/protocol and named here so the enclave's own code
// keeps one vocabulary. Which channel each command is accepted over is enforced
// in extension.go and internal/provisioning/store.
var (
	OPTypeTeeOracle       = protocol.OPTypeTeeOracle
	OPCommandGetFeed      = protocol.OPCommandGetFeed
	OPCommandGetReadiness = protocol.OPCommandGetReadiness
	OPCommandSetEndpoints = protocol.OPCommandSetEndpoints
	OPCommandSetAdmins    = protocol.OPCommandSetAdmins
	OPCommandSetSecret    = protocol.OPCommandSetSecret
)

// OpName recovers the ASCII an op hash was built from.
func OpName(h common.Hash) string { return protocol.OpName(h) }

// Source health per upstream in the readiness report. Unknown is the zero value: a source
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
// are in the image's launch-policy label for port collisions); cmd/dev/start-tee,
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

	// Bound on one RPC provider's answer within a single round of the supply read.
	// Four rounds run in sequence per chain, so an endpoint that accepts the
	// connection and never answers costs at most four times this, leaving the rest
	// of ObservationDeadline to the reserve reads that follow. Without it a single
	// silent endpoint consumes the whole deadline and fails an observation the
	// remaining providers could satisfy.
	ProviderTimeout = 3 * time.Second

	// Cap on a single RPC response body. go-ethereum buffers a response whole —
	// the success path into a JSON decoder, the non-2xx path into a byte buffer —
	// so without this a provider answering with a multi-gigabyte result is held in
	// memory before the single-word check that would reject it ever runs. The
	// enclave keeps its provisioned secrets in memory, so exhausting it costs a
	// full re-provisioning round rather than a restart. A one-word call result and
	// its headers are a few hundred bytes.
	MaxRPCResponseBytes = 1 << 20

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

	// The credential refs are the protocol's (pkg/protocol); named here for the
	// enclave's own code. Names, never values.
	HexAPIKeyRef  = protocol.HexAPIKeyRef
	HexCashURLRef = protocol.HexCashURLRef
)

// The bonds endpoint is pinned: redirecting it would hand whoever publishes
// configuration the numerator of the price. The cash URL is itself sensitive, so
// it travels as a PRIVATE endpoint whose salted commitment is all the chain sees.
const (
	HexBondsURL = "https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount"

	// The metric each endpoint must name in its own response, compared on every
	// read. Both endpoints share one response shape, so this label is the only
	// field distinguishing a reserve amount from another metric on the same host.
	// It bounds the cash endpoint in particular, whose URL is provisioned rather
	// than pinned here.
	HexBondsMetric = "current_reserves_amount"
	HexCashMetric  = "current_cash_reserves_amount"

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

// The admin policy — which refs the backing set governs and the least threshold
// each set may be installed with — is the protocol's. The enclave's verifier is
// built with it, and usdxprov applies the same rule before an envelope makes the
// trip. See pkg/protocol for the decisions behind the floors.
func BackingRefs() []string        { return protocol.BackingRefs() }
func IsBackingRef(ref string) bool { return protocol.IsBackingRef(ref) }
func AdminPolicy() protocol.Policy { return protocol.AdminPolicy() }

const (
	MinBackingThreshold  = protocol.MinBackingThreshold
	MinProviderThreshold = protocol.MinProviderThreshold
)

// ChainHealthKey and ProviderHealthKey name a chain and one of its RPC
// providers in the sourceHealth map the readiness report carries.
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

// The chain set, the token on each and the home chain are the protocol's
// (pkg/protocol). Provider lists and agreement thresholds are configured at
// runtime; see internal/provisioning/store.
type Chain = protocol.Chain

var Chains = protocol.Chains

const HomeChain = protocol.HomeChain

func LookupChain(name string) (Chain, bool) { return protocol.LookupChain(name) }

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

	// The feed this build serves, as the protocol pins it.
	FeedID = protocol.FeedID
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

// Neither reserve source carries a freshness bound: Hex republishes on a mint or a
// redemption, not on a schedule, so last_update is a change timestamp and a bound
// flapped good data to `down`. Feed age is bounded by MaxObservationAge instead.

// BondsSource is Hex's public reserves endpoint. RequireCurrency must stay false:
// the public endpoint omits the field entirely.
func BondsSource() BackingSpec {
	return BackingSpec{
		Name: HexBondsName, URL: HexBondsURL,
		MetricName:      HexBondsMetric,
		RequireCurrency: false,
		Timeout:         HTTPTimeout,
	}
}

// CashSource is Hex's restricted endpoint plus the injected credential. The URL
// arrives as a PRIVATE endpoint whose commitment the hex_cash group pins.
func CashSource(url, apiKey string) BackingSpec {
	return BackingSpec{
		Name: HexCashName, URL: url,
		MetricName:      HexCashMetric,
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
	MetricName      string
	RequireCurrency bool
	Timeout         time.Duration
	AuthHeader      string
	AuthValue       string
}
