#!/usr/bin/env bash
# oracle-cycle.sh — run one keeper cycle against the deployed stack.
#
#   trigger   POST /direct on the proxy, or requestPriceUpdate() with --on-chain
#   collect   poll the TEE proxy for the signed ActionResult
#   relay     decode and call UsdxPriceStore.submitPrice
#
# This is the same code path the unattended keeper runs on a timer
# (tools/pkg/keeper), so what it proves by hand is what happens in production.
# The keeper holds no authority: the store verifies the TEE signature, so the
# worst a wrong invocation does is waste gas.
#
# Everything here addresses the ONE proxy at ORACLE_PROXY_URL, which is what
# makes it a per-machine tool. The fleet-wide behaviour — rotating over the
# registered machines, and the startup sweep that commissions all of them —
# needs cmd/keeper without -p; see docs/usdx-oracle-keeper.md.
#
# Usage:
#   ./scripts/oracle-cycle.sh                        # one cycle, submit for real
#   ./scripts/oracle-cycle.sh --on-chain             # pay the fee, use the permissionless path
#   ./scripts/oracle-cycle.sh --dry-run              # decode + preflight, send nothing
#   ./scripts/oracle-cycle.sh --dry-run --instruction-id 0x…   # re-check a direct result, no fee
#   ./scripts/oracle-cycle.sh --dry-run --on-chain --instruction-id 0x…  # …an instruction's
#   ./scripts/oracle-cycle.sh --loop 10m             # stay up and cycle on a timer
#   ./scripts/oracle-cycle.sh --loop 10m --records cycles.jsonl
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
die() { echo -e "${RED}[oracle-cycle] ERROR:${NC} $*" >&2; exit 1; }

[[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }
[[ -f "$PROJECT_DIR/config/extension.env" ]] && source "$PROJECT_DIR/config/extension.env"

DRY_RUN=false; INTERVAL=""; INSTRUCTION_ID=""; RECORDS=""; ON_CHAIN=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN=true; shift ;;
        # The direct channel is the default because it is what the keeper runs:
        # no registry fee, no trigger transaction. --on-chain exercises the
        # permissionless path instead, which is worth proving still works.
        --on-chain) ON_CHAIN=true; shift ;;
        --loop) [[ $# -ge 2 ]] || die "--loop needs a duration, e.g. 10m"
                INTERVAL="$2"; shift 2 ;;
        # One JSON record per cycle. Separating them from the log stream is what
        # makes the run machine-readable; the records are write-only, so nothing
        # reads one back to make a decision.
        --records) [[ $# -ge 2 ]] || die "--records needs a file path"
                RECORDS="$2"; shift 2 ;;
        # Relaying a result you already have costs no instruction fee, which
        # makes it the cheap way to re-inspect a report or retry a failed relay.
        --instruction-id) [[ $# -ge 2 ]] || die "--instruction-id needs a value"
                INSTRUCTION_ID="$2"; shift 2 ;;
        *) die "unknown argument: $1" ;;
    esac
done

[[ -n "${INSTRUCTION_SENDER:-}" ]] || die "INSTRUCTION_SENDER not set — run scripts/pre-build.sh"

# ADDRESSES_FILE in .env is written relative to the project root, but these
# tools run from tools/. Resolving it here is what keeps a bare invocation from
# dying on "no such file or directory".
ADDR_FILE="${ADDRESSES_FILE:-./config/coston2/deployed-addresses.json}"
[[ "$ADDR_FILE" != /* ]] && ADDR_FILE="$PROJECT_DIR/$ADDR_FILE"
[[ -f "$ADDR_FILE" ]] || die "addresses file not found: $ADDR_FILE"

# Docker Compose publishes 6674 -> 6664. EXT_PROXY_URL in .env is the public
# tunnel, which works but adds a hop and fails whenever the tunnel is down;
# registration is the only step that genuinely needs the public URL.
PROXY="${ORACLE_PROXY_URL:-http://localhost:6674}"

echo -e "${CYAN}sender:${NC} $INSTRUCTION_SENDER"
echo -e "${CYAN}proxy: ${NC} $PROXY"
echo -e "${CYAN}chain: ${NC} ${CHAIN_URL:-<default>}"

cd "$PROJECT_DIR/tools"

# Built and exec'd rather than `go run`: the keeper handles SIGINT/SIGTERM so an
# in-flight cycle unwinds cleanly, and under `go run` the signal goes to the
# wrapper while the real process runs as its child. exec also means this script
# leaves no shell between the supervisor and the keeper.
BIN_DIR="${TMPDIR:-/tmp}/usdx-oracle-bin"
mkdir -p "$BIN_DIR"

if [[ "$DRY_RUN" == "true" || -n "$INSTRUCTION_ID" ]]; then
    # submit-price is the operator-facing form of the same cycle: it prints the
    # decoded report and runs every store guard, optionally without sending a
    # transaction.
    ARGS=(-a "$ADDR_FILE" -p "$PROXY")
    [[ -n "$INSTRUCTION_ID" ]] \
        && ARGS+=(-instructionId "$INSTRUCTION_ID") \
        || ARGS+=(-instructionSender "$INSTRUCTION_SENDER")
    [[ "$DRY_RUN" == "true" ]] && ARGS+=(-dry-run)
    # submit-price still defaults to the on-chain channel, so direct is opt-in
    # there — the opposite of the keeper, which runs unattended and pays a fee
    # per cycle if it gets this wrong.
    #
    # With --instruction-id this also picks the submission tag: a direct action's
    # result is filed under "submit" and an instruction's under "threshold", and
    # asking for the wrong one 404s for the whole poll window. So re-checking the
    # result of an on-chain instruction needs --on-chain.
    [[ "$ON_CHAIN" == "false" ]] && ARGS+=(-direct)
    go build -o "$BIN_DIR/submit-price" ./cmd/submit-price
    exec "$BIN_DIR/submit-price" "${ARGS[@]}"
fi

# -sweep=false: this script drives one named machine, and a sweep would submit a
# price from every registered one behind the operator's back.
ARGS=(-a "$ADDR_FILE" -p "$PROXY" -instructionSender "$INSTRUCTION_SENDER" -sweep=false)
[[ "$ON_CHAIN" == "true" ]] && ARGS+=(-direct=false)
[[ -n "$RECORDS" ]] && ARGS+=(-records "$RECORDS")

go build -o "$BIN_DIR/keeper" ./cmd/keeper

if [[ -n "$INTERVAL" ]]; then
    # No -once: keeper.Run stays up and cycles on the timer.
    exec "$BIN_DIR/keeper" "${ARGS[@]}" -interval "$INTERVAL"
fi

exec "$BIN_DIR/keeper" "${ARGS[@]}" -once
