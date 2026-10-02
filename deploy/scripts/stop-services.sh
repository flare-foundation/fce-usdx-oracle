#!/usr/bin/env bash
#
# Stop extension services.
#
# By default, stops Docker Compose services, picking the compose overlay from
# --chain (or env CHAIN, or legacy LOCAL_MODE):
#   --chain local    → docker-compose.yaml only
#   --chain <live chain>  → + docker-compose.live.yaml, on the network
#                           start-services.sh brought it up on
#
# The chain's indexer (docker-compose.<chain>-indexer.yaml) is a stack of its own
# and is left running: it is slow to catch up again.
#
# Without flags the stack recorded by start-services.sh (deploy/config/stack.env)
# is stopped; --local forces the host-process stack, --chain the compose one.
#
# Before stopping, the machine the stack fronts is retired on chain
# (tools/cmd/retire-tee: the registry's pause). Its identity is the enclave key
# in the running process, so a stopped stack cannot come back as the same
# machine; left registered it would stay in the sender's active set, paid for
# on every publication and undeliverable for the relay. --keep-registered skips
# this; a restart is a new machine either way, so keep a registration only to
# inspect it.
#
# Usage:
#   ./deploy/scripts/stop-services.sh                       # local devnet, docker compose
#   ./deploy/scripts/stop-services.sh --chain coston        # Coston, docker compose
#   ./deploy/scripts/stop-services.sh --local               # background Go processes
#   ./deploy/scripts/stop-services.sh --keep-registered     # stop, but leave the machine registered
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
log()  { echo -e "${GREEN}[stop-services]${NC} $*"; }
die()  { echo -e "${RED}[stop-services] ERROR:${NC} $*" >&2; exit 1; }

# The libraries: load_env (.env, then the stack start-services.sh recorded in
# deploy/config/stack.env) and the chain rows. Sourced before the flags are
# read, so a usage error can list the chains.
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"
# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"

# --- Parse flags ---
USE_LOCAL=false
CHAIN="${CHAIN:-}"
RETIRE=true
while [[ $# -gt 0 ]]; do
    case "$1" in
        --local) USE_LOCAL=true; shift ;;
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value ($CHAINS_USAGE)"
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        --keep-registered) RETIRE=false; shift ;;
        *) die "Unknown argument: $1" ;;
    esac
done

load_env

# Without --local, the recorded stack decides what "stop" means: a host-process
# stack is stopped as such rather than a compose stack that is not there.
if [[ "$USE_LOCAL" == "false" && "${STACK:-}" == "host" ]]; then
    log "the recorded stack is the host-process one — stopping that"
    USE_LOCAL=true
fi

LOCAL_MODE="${LOCAL_MODE:-true}"

# --- Resolve CHAIN (flag > CHAIN in .env > its CHAIN_ID > the simulation) ---
CHAIN="$(resolve_chain "$CHAIN")"
chain_known "$CHAIN" || die "Unknown --chain value: $CHAIN (valid: $CHAINS_USAGE)"

# --- Retire the machine on chain while its proxy can still name it ---
# The proxy's /info carries the attested key the machine is registered under;
# once the stack is down nothing can say which machine it was. Best effort: a
# proxy that is already gone, or a chain that cannot be reached, is reported and
# the stop goes ahead; tools/cmd/retire-tee -stale sweeps such leftovers later.
if [[ "$RETIRE" == "true" ]]; then
    if [[ -z "${EXT_PROXY_URL:-}" ]]; then
        log "no proxy URL recorded (EXT_PROXY_URL); not retiring a machine — run tools/cmd/retire-tee -stale to sweep"
    elif ! curl -sf -m 5 "${EXT_PROXY_URL%/}/info" >/dev/null 2>&1; then
        log "the proxy at $EXT_PROXY_URL does not answer; not retiring a machine — run tools/cmd/retire-tee -stale to sweep"
    else
        log "Retiring the machine behind $EXT_PROXY_URL on chain (pass --keep-registered to skip)..."
        ( cd "$PROJECT_DIR/tools" && go run ./cmd/retire-tee -p "$EXT_PROXY_URL" ) \
            || log "retiring failed; stopping the stack anyway — run tools/cmd/retire-tee -stale to sweep"
    fi
fi

if [[ "$USE_LOCAL" == "true" ]]; then
    # --- Stop background Go processes ---
    E2E="$SCRIPT_DIR/e2e.sh"
    PID_DIR="$PROJECT_DIR/out/pids"

    log "Stopping background Go processes..."
    "$E2E" stop-all "$PID_DIR"
    if [[ "$CHAIN" != "local" ]]; then
        # The redis start-services.sh ran for the host proxy from the base
        # compose file. The chain's indexer, if it runs, is left alone: it is
        # started on its own and is slow to catch up again.
        log "Removing the compose redis of the host proxy..."
        SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}" docker compose -f "$PROJECT_DIR/deploy/docker-compose.yaml" rm -sf redis >/dev/null 2>&1 || true
    fi
    [[ "$(grep -E "^STACK=" "$PROJECT_DIR/deploy/config/stack.env" 2>/dev/null)" == "STACK=host" ]] && rm -f "$PROJECT_DIR/deploy/config/stack.env"
    log "Done."
else
    # --- Stop Docker Compose services ---
    COMPOSE_FILES=("-f" "$PROJECT_DIR/deploy/docker-compose.yaml")

    # Mirror start-services.sh: include the siblings overlay when active so
    # compose resolves the same project/services that were started.
    case "${USE_LOCAL_SIBLINGS:-}" in
        1|true|yes|on) COMPOSE_FILES+=("-f" "$PROJECT_DIR/deploy/docker-compose.siblings.yaml") ;;
    esac

    # The live overlay, the same single file start-services.sh brought the stack
    # up with, with the variables it interpolates set as start-services.sh sets
    # them: the network is the chain indexer's (lib/chains.sh). The chain's
    # indexer is a stack of its own and is left running: it is slow to catch up
    # again and nothing else depends on this stop.
    if [[ "$CHAIN" != local ]]; then
        COMPOSE_FILES+=("-f" "$PROJECT_DIR/deploy/docker-compose.live.yaml")
        CHAIN_URL="${CHAIN_URL:-$(chain_rpc "$CHAIN")}"
        CHAIN_ID="${CHAIN_ID:-$(chain_id "$CHAIN")}"
        COMPOSE_NETWORK="${COMPOSE_NETWORK:-$(chain_compose_network "$CHAIN")}"
        export CHAIN_URL CHAIN_ID COMPOSE_NETWORK
    fi

    # docker-compose.yaml interpolates SOURCE_DATE_EPOCH as a build arg. It's
    # irrelevant on `down`, but compose still warns when it's unset — silence it.
    export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}"

    log "Stopping Docker Compose services (chain: $CHAIN)..."
    docker compose "${COMPOSE_FILES[@]}" down
    [[ "$(grep -E "^STACK=" "$PROJECT_DIR/deploy/config/stack.env" 2>/dev/null)" == "STACK=docker" ]] && rm -f "$PROJECT_DIR/deploy/config/stack.env"
    log "Done."
fi
