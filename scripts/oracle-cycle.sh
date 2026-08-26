#!/usr/bin/env bash
# oracle-cycle.sh — run one keeper cycle against the deployed stack.
#
#   trigger   requestFeedUpdate(feedId, teeIds) on TeeOracleInstructionsSender
#   collect   poll the TEE proxy for the signed ActionResult
#   relay     decode and call TeeOracleFeedStore.submitFeedUpdate
#
# This is the same code path the unattended keeper runs on a timer
# (tools/pkg/keeper), so what it proves by hand is what happens in production.
# The keeper holds no authority: the store verifies the TEE signature through
# Fdc2Verification, so the worst a wrong invocation does is waste gas.
#
# The keeper names the target machines explicitly now (no more getRandomTeeIds),
# so -tee is what routes the request. -p pins where the result is polled and
# where the readiness preflight asks; see docs/usdx-oracle-keeper.md for the
# fleet-wide form.
#
# Usage:
#   ./scripts/oracle-cycle.sh -tee @a,@b,@c                      # one cycle, submit for real
#   ./scripts/oracle-cycle.sh -tee @a,@b,@c --dry-run            # preflight only, send nothing
#   ./scripts/oracle-cycle.sh -tee @a,@b,@c --loop 10m           # stay up and cycle on a timer
#   ./scripts/oracle-cycle.sh -tee @a,@b,@c --loop 10m --records cycles.jsonl
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
die() { echo -e "${RED}[oracle-cycle] ERROR:${NC} $*" >&2; exit 1; }

[[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }

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

[[ -n "$TEE" ]] || die "-tee is required: at least one TEE machine address"

ADDR_FILE="${ADDRESSES_FILE:-./config/coston2/deployed-addresses.json}"
[[ "$ADDR_FILE" != /* ]] && ADDR_FILE="$PROJECT_DIR/$ADDR_FILE"
[[ -f "$ADDR_FILE" ]] || die "addresses file not found: $ADDR_FILE"

# The feed store is the upstream TeeOracleFeedStore entry in the addresses file;
# the keeper derives the instructions sender from the store's own pointer.
STORE=$(jq -r '[.[] | select(.name == "TeeOracleFeedStore") | .address][0] // empty' "$ADDR_FILE")
if [[ -z "$STORE" || "$STORE" == "0x0000000000000000000000000000000000000000" ]]; then
    die "no TeeOracleFeedStore address in $ADDR_FILE — slot it in after the upstream deploy"
fi

PROXY="${ORACLE_PROXY_URL:-http://localhost:6674}"

echo -e "${CYAN}store: ${NC} $STORE"
echo -e "${CYAN}proxy: ${NC} $PROXY"
echo -e "${CYAN}tee:   ${NC} $TEE"

cd "$PROJECT_DIR/tools"

BIN_DIR="${TMPDIR:-/tmp}/usdx-oracle-bin"
mkdir -p "$BIN_DIR"

ARGS=(-a "$ADDR_FILE" -p "$PROXY" -store "$STORE" -tee "$TEE")
[[ "$DRY_RUN" == "true" ]] && ARGS+=(-dry-run)
[[ -n "$RECORDS" ]] && ARGS+=(-records "$RECORDS")

go build -o "$BIN_DIR/keeper" ./cmd/keeper

if [[ -n "$INTERVAL" ]]; then
    exec "$BIN_DIR/keeper" "${ARGS[@]}" -interval "$INTERVAL"
fi

exec "$BIN_DIR/keeper" "${ARGS[@]}" -once
