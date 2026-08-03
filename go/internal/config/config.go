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
