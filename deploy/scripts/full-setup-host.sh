#!/usr/bin/env bash
# full-setup-host.sh — the local lifecycle of `full-setup.sh --chain local`, with
# the extension TEE and its proxy as host processes instead of containers.
#
# Phase 2 runs tools/cmd/local-stack in the background: the proxy in-process and
# the extension TEE (tee-node with the Go extension) as its child, built from the
# module root, against the sibling tee-node checkout when a go.work is present.
# Redis is the simulation's fourth instance (:6382), the one e2e reserves for an
# extension proxy. Phase 3 is post-build.sh against that proxy. It binds 6664 on
# the host while .env.local names the compose stack's 6674, so EXT_PROXY_URL is
# overridden here and has to be for every follow-up script; the printed commands
# carry it.
#
# Usage:
#   ./deploy/scripts/full-setup-host.sh                  # .env.local → .env, start, register
#   ./deploy/scripts/full-setup-host.sh --redis :6390    # another redis for the proxy queue
#
# Stop: ./deploy/scripts/stop-services.sh --local (every process under out/pids/).
# Logs: out/logs/local-stack.log carries both processes, the TEE's lines tagged.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[full-setup-host]${NC} $*"; }
warn() { echo -e "${YELLOW}[full-setup-host]${NC} $*"; }
die()  { echo -e "${RED}[full-setup-host] ERROR:${NC} $*" >&2; exit 1; }
banner() { echo -e "\n${CYAN}== $* ==${NC}"; }

REDIS=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --redis) [[ $# -ge 2 ]] || die "--redis needs an address (host:port or :port)"; REDIS="$2"; shift 2 ;;
        -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

for t in go curl jq; do command -v "$t" >/dev/null || die "$t is required"; done

# The simulation is the only chain this runs against; its keys and URLs live in
# .env.local, activated the way full-setup.sh --chain local does it.
if [[ -f "$PROJECT_DIR/.env.local" ]]; then
    cp "$PROJECT_DIR/.env.local" "$PROJECT_DIR/.env"
    log "Activated .env.local → .env"
else
    die "no .env.local in $PROJECT_DIR"
fi
set -a
# shellcheck disable=SC1091
source "$PROJECT_DIR/.env"
set +a

HOST_PROXY_URL="http://localhost:6664"
E2E="$SCRIPT_DIR/e2e.sh"
PID_DIR="$PROJECT_DIR/out/pids"; LOG_DIR="$PROJECT_DIR/out/logs"; BIN_DIR="$PROJECT_DIR/out/bin"
PIDFILE="$PID_DIR/local-stack.pid"; LOGFILE="$LOG_DIR/local-stack.log"
mkdir -p "$PID_DIR" "$LOG_DIR" "$BIN_DIR"

banner "Phase 1: Pin extension identity"
EXTENSION_ID="${EXTENSION_ID:-1}"
log "EXTENSION_ID=$EXTENSION_ID (reserved id 1); passed to the runner, which refuses any other"

banner "Phase 2: Start the extension TEE and proxy as host processes"
log "Building out/bin/local-stack from tools/ ..."
( cd "$PROJECT_DIR/tools" && go build -o "$BIN_DIR/local-stack" ./cmd/local-stack ) || die "building local-stack failed"

# A stack from an earlier run holds signing-policy state for the chain it synced
# against; a fresh start is what the compose path does too.
if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    log "a previous local-stack is running; stopping it first"
    "$E2E" stop "$PIDFILE" || true
fi
: > "$LOGFILE"

ARGS=(-extensionID "$EXTENSION_ID")
[[ -n "$REDIS" ]] && ARGS+=(-redis "$REDIS")
"$E2E" start local-stack "$PIDFILE" "$LOGFILE" "$BIN_DIR/local-stack" "${ARGS[@]}"

# The runner builds start-tee first (a minute against a cold cache), then brings
# the proxy up; its summary line names the machine.
DEADLINE=$(( $(date +%s) + 240 ))
until grep -q 'Extension stack running' "$LOGFILE" 2>/dev/null; do
    if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
        tail -n 25 "$LOGFILE" >&2
        die "local-stack exited; its log is above (full log: ${LOGFILE#$PROJECT_DIR/})"
    fi
    (( $(date +%s) >= DEADLINE )) && { tail -n 25 "$LOGFILE" >&2; die "local-stack did not come up within 240s"; }
    sleep 3
done
"$E2E" wait-for-url "$HOST_PROXY_URL/info" 30 || die "the host proxy is not answering on $HOST_PROXY_URL"
echo; sed -n '/Extension stack running/,/^Ctrl-C/p' "$LOGFILE" | grep -vE '^Continue with|^  EXT_PROXY_URL|^  \./deploy|^Ctrl-C|^$'

banner "Phase 3: Post-build"
EXT_PROXY_URL="$HOST_PROXY_URL" "$SCRIPT_DIR/post-build.sh" || die "Post-build failed"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN} Full setup complete (host processes)${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo -e "${CYAN}Processes${NC}   local-stack PID $(cat "$PIDFILE"), log ${LOGFILE#$PROJECT_DIR/}"
echo -e "${CYAN}Proxy${NC}       $HOST_PROXY_URL"
echo ""
echo -e "${CYAN}Continue with this proxy:${NC}"
echo "  EXT_PROXY_URL=$HOST_PROXY_URL ./deploy/scripts/publish-local.sh endpoints --wait"
echo "  EXT_PROXY_URL=$HOST_PROXY_URL ./deploy/scripts/publish-local.sh admins --wait"
echo "  EXT_PROXY_URL=$HOST_PROXY_URL ./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run"
echo ""
echo -e "${CYAN}Stop:${NC}       ./deploy/scripts/stop-services.sh --local"
