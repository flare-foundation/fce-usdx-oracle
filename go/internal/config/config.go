// Package config contains configuration values and defaults used by the extension.
package config

import (
	"os"
	"strconv"
	"time"
)

const (
	Version = "0.1.0"

	// Op type and commands. These strings must match the bytes32 constants in
	// contracts/InstructionSender.sol exactly — a mismatch means actions fall
	// through to "unsupported op type" (HTTP 501).
	OPTypeUsdxOracle  = "USDX_ORACLE"
	OPCommandGetPrice = "GET_PRICE"

	// Provisioning. Each command is valid over exactly one action type, and the
	// handler enforces it — see extension.processUsdxConfig.
	//
	// SET_ENDPOINTS is instruction-only: its authorisation is the owner-only
	// check in the on-chain sender, which a direct action would bypass entirely.
	// SET_SECRET is direct-only: a credential has no business on-chain, where the
	// ciphertext would be permanent and public.
	OPTypeUsdxConfig      = "USDX_CONFIG"
	OPCommandSetEndpoints = "SET_ENDPOINTS"
	OPCommandSetSecret    = "SET_SECRET"

	TimeoutShutdown = 5 * time.Second

	// GetPriceRequestV1 is the request envelope a GET_PRICE instruction carries,
	// mirroring GET_PRICE_REQUEST_V1 in contracts/InstructionSender.sol.
	//
	// The observation needs nothing from the caller, so the natural payload is
	// none at all — but the registry rejects an empty `message` with
	// MessageEmpty(), found the first time this path ran on Coston2 (5 Aug 2026).
	// One version byte is the smallest payload that satisfies it and still says
	// something: the handler requires exactly this value, so an unrecognised
	// envelope is refused rather than ignored, and a future version can change
	// the request shape without ambiguity about which one arrived.
	GetPriceRequestV1 = byte(0x01)

	// PriceDecimals is the fixed-point scale of the published price: the value
	// carried on-chain is truncate(price * 10^PriceDecimals). 8 gives 1e-8
	// resolution around $1.00. IICustomFeed returns decimals as int8 and imposes
	// no constraint, so this is purely our choice — see spec §3.5.
	PriceDecimals int8 = 8
)

// The error taxonomy lives in internal/failure, which owns the whole public
// error vocabulary in one reviewable file. It is deliberately not duplicated
// here: two lists of "what may we say publicly" is one list too many.

// Source health values reported per upstream in GET /state.
//
// Unknown, not OK, is the zero value. A source that has never been polled must
// not read as healthy — that is the failure mode where an upstream silently
// stops being queried and nothing looks wrong.
const (
	HealthUnknown  = "unknown"
	HealthOK       = "ok"
	HealthDegraded = "degraded"
	HealthDown     = "down"

	// HealthNotConfigured is a source that was never provisioned, as opposed to
	// one that is broken. Only the cash endpoint can be in this state, and the
	// distinction is operational: "down" pages someone to fix an outage,
	// "not_configured" tells them to send a SET_SECRET.
	HealthNotConfigured = "not_configured"
)

// Defaults.
var (
	ExtensionPort = 8080
	SignPort      = 9090
)

// Environment variables override defaults.
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

// --- Pinned deployment facts ---
//
// Everything below is compiled into the binary and therefore covered by the
// registered code hash. These are settled facts and safety properties, not
// knobs: the contract addresses are verified on-chain (spec §3.1), the finality
// margins come from measured block times, and the observation bounds are what
// make the conservative double-read mean anything.
//
// Only two things are configurable at runtime — the RPC provider URLs and the
// credentials. Everything an operator could change to move the published price
// lives here instead.

const (
	// ObservationDeadline bounds one complete observation: both supply passes
	// and both reserve reads.
	ObservationDeadline = 20 * time.Second

	// ObservationInterval is how often the background loop observes.
	//
	// Observations run on a timer rather than inside the GET_PRICE handler
	// because tee-node gives an extension **2 seconds** to answer an action —
	// `settings.ProxyTimeout`, a const in its internal package, so not
	// configurable from here — and a complete observation measured 2.0–2.9s
	// against live sources on 5 Aug 2026. Every request timed out. Handling a
	// request now costs one signing round trip; the slow part happens here.
	//
	// Two minutes against Hex's ~10 minute refresh is deliberately faster than
	// the source: it keeps the supply side current and, more importantly, leaves
	// room for several consecutive failures inside MaxObservationAge.
	ObservationInterval = 2 * time.Minute

	// MaxObservationAge is the oldest observation that may still be signed.
	//
	// This is the fail-closed bound for the cache. Past it, GET_PRICE returns
	// failure.ObservationStale and publishes nothing, so the on-chain price ages
	// visibly rather than being refreshed with a stale figure under a fresh
	// signature. At ObservationInterval it allows four consecutive failed
	// observations before the feed stops updating.
	//
	// It has to stay well inside UsdxPriceStore.maxAge (3600s at deployment),
	// which bounds the same staleness from the other end: this caps how old the
	// inputs may be, that caps how long the report may take to land.
	MaxObservationAge = 10 * time.Minute

	// ObservationWindow is the maximum spread between the earliest and latest
	// input read — the TOCTOU bound. Not configurable: widening it silently
	// weakens the guarantee the double-read provides.
	ObservationWindow = 30 * time.Second

	// BackingMaxAge rejects a reserve figure older than this. Hex refreshes on a
	// ~10 minute tick, so anything below that is noise.
	BackingMaxAge = 30 * time.Minute

	// HTTPTimeout bounds a single upstream request attempt.
	HTTPTimeout = 5 * time.Second

	// HexAPIKeyRef names the credential for Hex's restricted endpoint.
	HexAPIKeyRef = "hex_api_key"
)

// Hex reserve endpoints. Pinned rather than configurable: these are the one
// source that cannot be swapped for another, so allowing an operator to
// redirect the reserve read would hand them the numerator of the price.
const (
	HexBondsURL = "https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount"
	HexCashURL  = "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount"

	// HexCashAuthHeader carries the credential for the restricted endpoint.
	//
	// OPEN (spec WP1): the scheme is unconfirmed — the endpoint returns 404
	// rather than 401 without credentials, so bearer vs X-API-Key cannot be
	// distinguished until a key is issued. Revisit when it is.
	HexCashAuthHeader = "X-API-Key"

	// HexBondsName and HexCashName identify the sources in GET /state health.
	HexBondsName = "hex_bonds"
	HexCashName  = "hex_cash"
)

// Chain is a pinned per-chain fact.
//
// Only what cannot change: the token contract and the chain's finality
// behaviour. The provider list and the agreement threshold are configured at
// runtime — see internal/runtimecfg.
type Chain struct {
	// Name identifies the chain in health reporting.
	Name string

	// Token is the USDX contract. Verified on-chain 31 Jul 2026 (spec §3.1).
	// Pinned because pointing this at another ERC-20 would let whoever set it
	// publish an arbitrary price.
	Token string

	// FinalityTag is the reference each provider resolves before the minimum is
	// taken: "finalized" on Ethereum, "latest" elsewhere.
	FinalityTag string

	// FinalityMargin is in BLOCKS, not seconds. Measured block times: Flare
	// 1.11s, Songbird 1.07s, Ethereum 12.01s — so the same number is a very
	// different amount of time on each. Must stay inside the shallowest
	// provider's state-retention window.
	FinalityMargin uint64
}

// Chains is the pinned chain set. USDX is a native burn-and-mint LayerZero OFT
// on each, so supply is a plain sum with no exclusions — see internal/supply.
//
// The pre-migration Ethereum contract (0x7A48…D56c, 150,000 USDX) is
// deliberately absent: Hex excludes it from their reserves figure, and counting
// it here would compare two different populations and publish a spurious ~0.6%
// depeg.
var Chains = []Chain{
	{
		Name: "flare", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B",
		FinalityTag: "latest", FinalityMargin: 54,
	},
	{
		Name: "ethereum", Token: "0xf8750b54d86BE7aE9e32b4A0C826811198D63313",
		FinalityTag: "finalized", FinalityMargin: 5,
	},
	{
		Name: "songbird", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B",
		FinalityTag: "latest", FinalityMargin: 56,
	},
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

// ChainID is the network this build serves. Set at build time alongside the
// other network-dependent values:
//
//	go build -ldflags "-X fce-usdx-oracle/internal/config.chainIDStr=14"
//
// It binds provisioning envelopes to one network, so an envelope signed for
// Coston2 cannot be replayed against mainnet. A constant rather than a config
// file because it is a pure function of which network the build targets — the
// same reason the network itself is a build-time choice.
var ChainID = mustChainID()

// chainIDStr is the ldflags injection point. Default is Coston2.
var chainIDStr = "114"

func mustChainID() uint64 {
	v, err := strconv.ParseUint(chainIDStr, 10, 64)
	if err != nil || v == 0 {
		panic("config: chainIDStr must be a positive integer, got " + chainIDStr)
	}
	return v
}

// BondsSource is the pinned configuration for Hex's public reserves endpoint.
//
// RequireCurrency is false and must stay so: the public endpoint omits the
// field entirely, so requiring it would fail closed on every call forever.
func BondsSource() BackingSpec {
	return BackingSpec{
		Name: HexBondsName, URL: HexBondsURL,
		RequireCurrency: false,
		MaxAge:          BackingMaxAge, Timeout: HTTPTimeout,
	}
}

// CashSource is the pinned configuration for Hex's restricted endpoint, plus
// the injected credential.
func CashSource(apiKey string) BackingSpec {
	return BackingSpec{
		Name: HexCashName, URL: HexCashURL,
		RequireCurrency: true,
		MaxAge:          BackingMaxAge, Timeout: HTTPTimeout,
		AuthHeader: HexCashAuthHeader, AuthValue: apiKey,
	}
}

// BackingSpec mirrors backing.Config. Declared here so the pinned facts live
// with the other pinned facts; internal/backing cannot be imported from this
// package without a cycle.
type BackingSpec struct {
	Name            string
	URL             string
	RequireCurrency bool
	MaxAge          time.Duration
	Timeout         time.Duration
	AuthHeader      string
	AuthValue       string
}
