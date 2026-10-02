#!/usr/bin/env bash
# oracle-watch.sh — follow the machine's own account of itself, and record it.
#
# Every interval, tools/cmd/oracle-watch asks the machine for its signed
# readiness report over the proxy's direct channel, appends one JSON sample per
# poll, and prints each change: a source's health, the class of the last failed
# observation, the ready and would-sign flags, a failed observation. It reads
# and never sends — no fee, no transaction — so it can run all day next to the
# keeper. The machine observes every two minutes on its own timer; that is the
# interval worth polling at.
#
# Usage:
#   ./deploy/scripts/oracle-watch.sh                                 # every 2m, samples to stdout
#   ./deploy/scripts/oracle-watch.sh --records out/watch.jsonl       # samples appended to a file, changes on the terminal
#   ./deploy/scripts/oracle-watch.sh --interval 5m --records out/watch.jsonl
#   ./deploy/scripts/oracle-watch.sh --once                          # one sample and the full picture
#
# The proxy is EXT_PROXY_URL from .env (the recorded stack on the simulation),
# the direct key DIRECT_API_KEY — required, since the report is the one thing the
# machine says about itself; the simulation's proxy accepts any key.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
RED='\033[0;31m'; NC='\033[0m'
die() { echo -e "${RED}[oracle-watch] ERROR:${NC} $*" >&2; exit 1; }
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"; load_env

INTERVAL=""; RECORDS=""; ONCE=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --interval) [[ $# -ge 2 ]] || die "--interval needs a duration, e.g. 2m"; INTERVAL="$2"; shift 2 ;;
        --records)  [[ $# -ge 2 ]] || die "--records needs a file path"; RECORDS="$2"; shift 2 ;;
        --once)     ONCE=true; shift ;;
        *) die "unknown argument: $1 (usage: $0 [--interval 2m] [--records file] [--once])" ;;
    esac
done
# The tool runs from tools/, so a relative records path is resolved here, against
# the directory the command was typed in.
[[ -n "$RECORDS" && "$RECORDS" != /* ]] && RECORDS="$PWD/$RECORDS"

PROXY="${EXT_PROXY_URL:-http://localhost:6674}"
ARGS=(-p "$PROXY")
# The tool reads the key from DIRECT_API_KEY, never from its command line, where
# ps shows it, and refuses to start without one. The simulation's proxy accepts
# any /direct key (api_key_optional), so a recorded local stack gets a placeholder.
DIRECT_API_KEY="${DIRECT_API_KEY:-${STACK:+local}}"
[[ -n "$DIRECT_API_KEY" ]] && export DIRECT_API_KEY
[[ -n "$INTERVAL" ]] && ARGS+=(-interval "$INTERVAL")
[[ -n "$RECORDS" ]] && ARGS+=(-records "$RECORDS")
[[ "$ONCE" == true ]] && ARGS+=(-once)

cd "$PROJECT_DIR/tools"
BIN_DIR="${TMPDIR:-/tmp}/usdx-oracle-bin"
mkdir -p "$BIN_DIR"
go build -o "$BIN_DIR/oracle-watch" ./cmd/oracle-watch
exec "$BIN_DIR/oracle-watch" "${ARGS[@]}"
