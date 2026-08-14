// Package config contains configuration values and defaults used by the extension.
package config

import (
	"os"
	"strconv"
	"time"
)

const (
	Version = "0.1.0"

	// Must match the bytes32 constants in contracts/InstructionSender.sol exactly.
	OPTypeUsdxOracle  = "USDX_ORACLE"
	OPCommandGetPrice = "GET_PRICE"

	// GET_READINESS is direct-only, and needs no counterpart in the sender: a
	// price must be requestable permissionlessly because feed liveness is a
	// public good, whereas readiness is operational detail whose answer is
	// stale within minutes. On-chain it would spend a fee per poll to write
	// ephemeral state into permanent public calldata. Enforced in extension.go.
	OPCommandGetReadiness = "GET_READINESS"

	// SET_ENDPOINTS is instruction-only (its authorisation is the owner-only
	// check in the on-chain sender); SET_SECRET is direct-only (a ciphertext
	// on-chain would be permanent and public). Enforced in extension.go.
	OPTypeUsdxConfig      = "USDX_CONFIG"
	OPCommandSetEndpoints = "SET_ENDPOINTS"
	OPCommandSetAdmins    = "SET_ADMINS"
	OPCommandSetSecret    = "SET_SECRET"

	TimeoutShutdown = 5 * time.Second

	// The observation needs no input, but the registry rejects an empty message
	// with MessageEmpty(), so GET_PRICE carries one version byte.
	GetPriceRequestV1 = byte(0x01)

	// Same shape for GET_READINESS, and checked just as strictly: a request
	// that carries anything else is rejected rather than ignored, so no caller
	// can come to depend on a field the extension silently drops.
	ReadinessRequestV1 = byte(0x01)

	PriceDecimals int8 = 8
)

// Source health per upstream in GET /state. Unknown is the zero value: a source
// that has never been polled must not read as healthy.
const (
	HealthUnknown  = "unknown"
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthDown     = "down"

	// An RPC provider that answered but lost the agreement vote. Distinct from
	// "down" on purpose: a provider returning a WRONG value is a different
	// problem from one returning nothing, and quorum hides both.
	HealthDisagree = "disagree"

	// An RPC provider whose head sits below the block the read was pinned to, so
	// it could not serve the query. Legitimate for a node that is merely behind
	// — which is why it is tolerated rather than fatal — but it is also exactly
	// the shape a provider takes when it tries to rewind the observation to an
	// old block, and block selection now ignores such a provider rather than
	// following it. Recorded so that being ignored still leaves a trace.
	HealthLagging = "lagging"

	// Never provisioned, as opposed to broken. "down" pages someone to fix an
	// outage; "not_configured" tells them to send a SET_SECRET.
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

// Pinned deployment facts. Everything below is compiled in and therefore
// covered by the registered code hash. Only RPC provider URLs and credentials
// are configurable at runtime.

const (
	ObservationDeadline = 20 * time.Second

	// Faster than Hex's ~10 minute refresh so several consecutive failures still
	// fit inside MaxObservationAge.
	ObservationInterval = 2 * time.Minute

	// Oldest observation that may still be signed. Past it GET_PRICE publishes
	// nothing and the on-chain price ages visibly. Must stay well inside
	// UsdxPriceStore.maxAge, which bounds the same staleness from the other end.
	MaxObservationAge = 10 * time.Minute

	// Maximum spread between earliest and latest input read. Bounds read skew
	// only — how far the reserve figure lags actual chain state is bounded by
	// BackingMaxAge, and that is the larger of the two exposures.
	ObservationWindow = 30 * time.Second

	BackingMaxAge = 30 * time.Minute
	HTTPTimeout   = 5 * time.Second

	HexAPIKeyRef = "hex_api_key"
)

// Hex reserve endpoints, pinned: redirecting the reserve read would hand an
// operator the numerator of the price.
const (
	HexBondsURL = "https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount"
	HexCashURL  = "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount"

	// OPEN: the endpoint returns 404 rather than 401 without credentials, so
	// bearer vs X-API-Key cannot be distinguished until a key is issued.
	HexCashAuthHeader = "X-API-Key"

	HexBondsName = "hex_bonds"
	HexCashName  = "hex_cash"
)

// Chain is a pinned per-chain fact. Provider lists and agreement thresholds are
// configured at runtime — see internal/runtimecfg.
type Chain struct {
	Name string

	// Pinned: pointing this at another ERC-20 would let whoever set it publish
	// an arbitrary price.
	Token string

	// MaxBlockAge refuses a supply read whose pinned block is older than this,
	// measured against the block's own header timestamp.
	//
	// Pinned rather than configurable, and deliberately so: this is the check
	// that stops a hostile RPC provider rewinding the observation to a block
	// where supply was lower, which overstates the price. Letting the same key
	// that chooses the providers also choose how stale a block may be would give
	// the check away with the attack.
	//
	// Sized as a large multiple of normal block age, not as a tight bound. The
	// thing it defends against is a rewind of hours or weeks; the cost of being
	// generous is only that a briefly-stalled chain is caught a little later,
	// and the cost of being tight is rejecting healthy observations. Ethereum
	// gets more room because its head is routinely 12s+ behind wall-clock while
	// Flare and Songbird run ~1.8s blocks.
	//
	// NOT the removed FinalityMargin, which was a depth BELOW the head and was
	// dropped in Aug 2026 after measurement showed it bought no availability.
	// This bounds the freshness of the chosen block instead — a different
	// property, and one that measurement never covered.
	MaxBlockAge time.Duration
}

// Chains is the pinned chain set. USDX is a native burn-and-mint LayerZero OFT
// on each, so supply is a plain sum.
//
// The pre-migration Ethereum contract (0x7A48…D56c, 150,000 USDX) is absent on
// purpose: Hex excludes it from reserves, so counting it would publish a
// spurious ~0.6% depeg.
var Chains = []Chain{
	{Name: "flare", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
	{Name: "ethereum", Token: "0xf8750b54d86BE7aE9e32b4A0C826811198D63313", MaxBlockAge: 3 * time.Minute},
	{Name: "songbird", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", MaxBlockAge: 2 * time.Minute},
}

// LookupChain returns the pinned policy for a chain name.
func LookupChain(name string) (Chain, bool) {
	for _, c := range Chains {
		if c.Name == name {
			return c, true
		}
	}
	return Chain{}, false
}

// ChainID binds provisioning envelopes to one network, so an envelope signed
// for Coston2 cannot be replayed against mainnet. Set at build time:
//
//	go build -ldflags "-X fce-usdx-oracle/internal/config.chainIDStr=14"
var ChainID = mustChainID()

var chainIDStr = "114"

func mustChainID() uint64 {
	v, err := strconv.ParseUint(chainIDStr, 10, 64)
	if err != nil || v == 0 {
		panic("config: chainIDStr must be a positive integer, got " + chainIDStr)
	}
	return v
}

// BondsSource is Hex's public reserves endpoint. RequireCurrency must stay
// false: the public endpoint omits the field entirely.
func BondsSource() BackingSpec {
	return BackingSpec{
		Name: HexBondsName, URL: HexBondsURL,
		RequireCurrency: false,
		MaxAge:          BackingMaxAge, Timeout: HTTPTimeout,
	}
}

// CashSource is Hex's restricted endpoint, plus the injected credential.
func CashSource(apiKey string) BackingSpec {
	return BackingSpec{
		Name: HexCashName, URL: HexCashURL,
		RequireCurrency: true,
		MaxAge:          BackingMaxAge, Timeout: HTTPTimeout,
		AuthHeader: HexCashAuthHeader, AuthValue: apiKey,
	}
}

// BackingSpec mirrors backing.Config; declared here because internal/backing
// cannot be imported from this package without a cycle.
type BackingSpec struct {
	Name            string
	URL             string
	RequireCurrency bool
	MaxAge          time.Duration
	Timeout         time.Duration
	AuthHeader      string
	AuthValue       string
}
