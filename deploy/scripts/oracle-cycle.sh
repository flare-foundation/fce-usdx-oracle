#!/usr/bin/env bash
# oracle-cycle.sh — run one keeper cycle against the deployed stack.
#
#   trigger   requestFeedUpdate(feedId, teeIds) on TeeOracleInstructionsSender
#   collect   poll the TEE proxy for the signed ActionResult
#   relay     decode every machine's signed update and call
#             TeeOracleFeedStore.submitFeedUpdates with the ones that agree
#
# This is the same code path the unattended keeper runs on a timer
# (tools/pkg/keeper), so what it proves by hand is what happens in production.
# The keeper holds no authority: the store verifies the TEE signature through
# Fdc2Verification, so the worst a wrong invocation does is waste gas.
#
# The keeper names the target machines explicitly in every request. Without
# -tee it picks them each cycle: the extension's active machines that are at the
# feed's latest configuration, read from the chain. With -tee the set is fixed,
# and a named machine that is paused or replaced stops every request. Each
# machine files its result on its own proxy, and the keeper polls
# every one at the url the machine registered; EXT_PROXY_URL is where it looks
# first and its fallback when the registry lists none. PROXY_OVERRIDE addresses
# every machine at one proxy instead — right only with a single machine that is
# reachable at a different address than it registered, since with two the other
# machine's result is never collected. See docs/usdx-oracle-keeper.md.
#
# Usage:
#   ./deploy/scripts/oracle-cycle.sh                                 # one cycle, targets read from the chain
#   ./deploy/scripts/oracle-cycle.sh -tee a,b,c                      # one cycle, a fixed set of targets
#   ./deploy/scripts/oracle-cycle.sh --dry-run                       # request and collect (the fee is paid), but submit nothing
#   ./deploy/scripts/oracle-cycle.sh --loop 10m                      # stay up and cycle on a timer
#   ./deploy/scripts/oracle-cycle.sh --loop 10m --records cycles.jsonl
#   PROXY_OVERRIDE=http://localhost:6674 ./deploy/scripts/oracle-cycle.sh -tee a   # one machine, other address
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
die() { echo -e "${RED}[oracle-cycle] ERROR:${NC} $*" >&2; exit 1; }

# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"; load_env

DRY_RUN=false; INTERVAL=""; RECORDS=""; TEE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true; shift ;;
        --loop) [[ $# -ge 2 ]] || die "--loop needs a duration, e.g. 10m"
                INTERVAL="$2"; shift 2 ;;
        --records) [[ $# -ge 2 ]] || die "--records needs a file path"
                RECORDS="$2"; shift 2 ;;
        -tee) [[ $# -ge 2 ]] || die "-tee needs a comma-separated list of addresses"
                TEE="$2"; shift 2 ;;
        *) die "unknown argument: $1" ;;
    esac
done

# The keeper runs from tools/, so a relative records path is resolved here,
# against the directory the command was typed in, before that change.
[[ -n "$RECORDS" && "$RECORDS" != /* ]] && RECORDS="$PWD/$RECORDS"

CHAIN="$(resolve_chain "${CHAIN:-}")"
ADDR_FILE="${ADDRESSES_FILE:-./deploy/config/$CHAIN/deployed-addresses.json}"
[[ "$ADDR_FILE" != /* ]] && ADDR_FILE="$PROJECT_DIR/$ADDR_FILE"
[[ -f "$ADDR_FILE" ]] || die "addresses file not found: $ADDR_FILE"

# The feed store is the upstream TeeOracleFeedStore entry in the addresses file;
# the keeper derives the instructions sender from the store's own pointer.
STORE=$(jq -r '[.[] | select(.name == "TeeOracleFeedStore") | .address][0] // empty' "$ADDR_FILE")
if [[ -z "$STORE" || "$STORE" == "0x0000000000000000000000000000000000000000" ]]; then
    die "no TeeOracleFeedStore address in $ADDR_FILE — slot it in after the upstream deploy"
fi

# Where results are looked for first; the keeper reads each machine's own url
# from the registry. Exported for the keeper's -p default.
export EXT_PROXY_URL="${EXT_PROXY_URL:-http://localhost:6674}"

echo -e "${CYAN}store: ${NC} $STORE"
echo -e "${CYAN}proxy: ${NC} $EXT_PROXY_URL (first looked at; each machine at its registered url)"
[[ -n "${PROXY_OVERRIDE:-}" ]] && echo -e "${CYAN}override:${NC} $PROXY_OVERRIDE (every machine addressed there)"
echo -e "${CYAN}tee:   ${NC} ${TEE:-every active machine at the latest feed configuration, read each cycle}"

cd "$PROJECT_DIR/tools"

BIN_DIR="${TMPDIR:-/tmp}/usdx-oracle-bin"
mkdir -p "$BIN_DIR"

ARGS=(-a "$ADDR_FILE" -store "$STORE")
[[ -n "$TEE" ]] && ARGS+=(-tee "$TEE")
# -p given explicitly overrides every machine's registered url, so it is passed
# only when the operator asked for that.
[[ -n "${PROXY_OVERRIDE:-}" ]] && ARGS+=(-p "$PROXY_OVERRIDE")
# The readiness preflight needs the proxy's direct-channel key, which the keeper
# reads from DIRECT_API_KEY (never passed on its command line, where ps shows
# it). The simulation's proxy accepts any (api_key_optional), so a recorded local
# stack gets a placeholder and the keeper stops warning about paying fees blind.
if [[ -n "${STACK:-}" ]]; then
    DIRECT_API_KEY="${DIRECT_API_KEY:-local}"
fi
[[ -n "${DIRECT_API_KEY:-}" ]] && export DIRECT_API_KEY
[[ "$DRY_RUN" == "true" ]] && ARGS+=(-dry-run)
[[ -n "$RECORDS" ]] && ARGS+=(-records "$RECORDS")

go build -o "$BIN_DIR/keeper" ./cmd/keeper

if [[ -n "$INTERVAL" ]]; then
    exec "$BIN_DIR/keeper" "${ARGS[@]}" -interval "$INTERVAL"
fi

exec "$BIN_DIR/keeper" "${ARGS[@]}" -once
