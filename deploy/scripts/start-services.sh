#!/usr/bin/env bash
#
# Start extension TEE node and proxy.
#
# By default, starts services via Docker Compose, picking the compose overlay
# from --chain (or env CHAIN, or legacy LOCAL_MODE):
#   --chain local    → docker-compose.yaml only (local devnet)
#   --chain coston   → + docker-compose.coston.yaml
#   --chain coston2  → + docker-compose.coston2.yaml
#
# Pass --local to run the extension TEE and proxy as host processes
# (tools/cmd/local-stack) instead of containers.
#
# Usage:
#   ./deploy/scripts/start-services.sh                       # local devnet, docker compose
#   ./deploy/scripts/start-services.sh --chain coston        # Coston, docker compose
#   ./deploy/scripts/start-services.sh --local               # local devnet, host processes
#
# By default the node and proxy are built from PINNED module/image versions
# (tee-node + tee-proxy), fully self-contained — no sibling repos required.
# To build them from on-disk sibling checkouts instead (while developing
# tee-node / tee-proxy), set USE_LOCAL_SIBLINGS=1:
#   USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh --chain coston2
#
# Prerequisites:
#   - Infrastructure running (Hardhat, indexer, Redis, normal TEE + proxy)
#   - EXTENSION_ID is set (defaults to reserved id 1; config/extension.env
#     written by full-setup.sh)
#   - The extension Redis is published on a free host port chosen on every run
#     (REDIS_BIND=host:port overrides); the choice is recorded in
#     deploy/config/extension.env and printed.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[start-services]${NC} $*"; }
die()  { echo -e "${RED}[start-services] ERROR:${NC} $*" >&2; exit 1; }
warn() { echo -e "${YELLOW:-}[start-services] WARN:${NC} $*" >&2; }

# --- Parse flags ---
USE_LOCAL=false
CHAIN="${CHAIN:-}"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --local) USE_LOCAL=true; shift ;;
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value (local|coston|coston2)"
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        *) die "Unknown argument: $1" ;;
    esac
done

# --- Load .env from project root (if present) ---
if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a
    source "$PROJECT_DIR/.env"
    set +a
fi

# --- Load extension config ---
# REDIS_BIND from the environment or .env is an explicit choice and wins. The
# copy recorded in extension.env by an earlier run is deliberately NOT reused:
# a fresh port is picked on every start, so a stale binding can never collide
# with whatever else has since claimed the old one.
REDIS_BIND_OVERRIDE="${REDIS_BIND:-}"
CONFIG_FILE="$PROJECT_DIR/deploy/config/extension.env"
if [[ -f "$CONFIG_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$CONFIG_FILE"
fi

# --- Redis host port ---
# The e2e stack publishes its own four redis instances on 6379-6382, and other
# sibling stacks claim ports too, so a fixed default is a recurring collision.
# Ask the OS for a free port instead; compose recreates the redis container on
# a changed binding, which loses nothing (it runs with persistence off).
if [[ -n "$REDIS_BIND_OVERRIDE" ]]; then
    REDIS_BIND="$REDIS_BIND_OVERRIDE"
else
    REDIS_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')" \
        || die "could not pick a free port for redis (python3 required)"
    REDIS_BIND="127.0.0.1:$REDIS_PORT"
fi
export REDIS_BIND

# --- Proxy config rendering ---
# The proxy TOMLs embed three contract addresses that the simulation redeploys
# on every run, and the host-mode TOML embeds the redis port. Rather than edit
# the committed files, a copy is rendered into out/ from the e2e sim dump.
SIM_DUMP_FILE="${SIM_DUMP:-$PROJECT_DIR/../../e2e/docker/sim_dump/deployed-addresses.json}"
[[ "$SIM_DUMP_FILE" != /* ]] && SIM_DUMP_FILE="$PROJECT_DIR/$SIM_DUMP_FILE"
render_proxy_config() {  # render_proxy_config <src.toml> <dst.toml> [redis-port]
    local src="$1" dst="$2" port="${3:-}" args=() pair key name addr
    mkdir -p "$(dirname "$dst")"
    if [[ "$CHAIN" == "local" && -f "$SIM_DUMP_FILE" ]]; then
        for pair in flare_systems_manager:FlareSystemsManager relay:Relay voter_registry:VoterRegistry; do
            key="${pair%%:*}"; name="${pair##*:}"
            addr="$(jq -r --arg n "$name" '.[$n] // empty' "$SIM_DUMP_FILE")"
            [[ -n "$addr" ]] && args+=(-e "s#^$key = .*#$key = \"$addr\"#")
        done
        log "proxy addresses from the sim dump ($(basename "$(dirname "$SIM_DUMP_FILE")")/$(basename "$SIM_DUMP_FILE"))"
    elif [[ "$CHAIN" == "local" ]]; then
        warn "no sim dump at $SIM_DUMP_FILE — the proxy keeps the committed addresses, which are stale after every new simulation"
    fi
    [[ -n "$port" ]] && args+=(-e "s#^redis_port = .*#redis_port = \":$port\"#")
    if (( ${#args[@]} )); then sed "${args[@]}" "$src" > "$dst"; else cp "$src" "$dst"; fi
}
mkdir -p "$(dirname "$CONFIG_FILE")"
{ grep -vE '^REDIS_BIND=' "$CONFIG_FILE" 2>/dev/null || true; echo "REDIS_BIND=$REDIS_BIND"; } > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"

# Reserved id 1 (pinned in internal/config); the upstream sender/store are
# not deployed locally. May be overridden for a test extension on another id.
EXTENSION_ID="${EXTENSION_ID:-1}"
PROXY_PRIVATE_KEY="${PROXY_PRIVATE_KEY:-983760a4ebf75b2ac3a93531168a0f225d01e5dc6e3568adbd46233ba1fb4fa4}"
LOCAL_MODE="${LOCAL_MODE:-true}"

# --- Resolve CHAIN (flag > env > legacy LOCAL_MODE) ---
if [[ -z "$CHAIN" ]]; then
    if [[ "$LOCAL_MODE" == "true" ]]; then
        CHAIN="local"
    else
        CHAIN="coston2"  # legacy
    fi
fi
case "$CHAIN" in
    local|coston|coston2) ;;
    *) die "Unknown --chain value: $CHAIN (valid: local, coston, coston2)" ;;
esac

[[ -n "$EXTENSION_ID" ]] || die "EXTENSION_ID is empty"

# The repo root is the Go module and the build context; compose builds Dockerfile.


# --- Resolve local-siblings toggle ---
case "${USE_LOCAL_SIBLINGS:-}" in
    1|true|yes|on) USE_LOCAL_SIBLINGS=true ;;
    *)             USE_LOCAL_SIBLINGS=false ;;
esac

log "Chain:          $CHAIN"
log "Extension ID:   $EXTENSION_ID"
log "Local mode:     $LOCAL_MODE"
log "Local siblings: $USE_LOCAL_SIBLINGS"
log "Redis bind:     $REDIS_BIND (host side; a new free port each run unless REDIS_BIND is set)"

# --- Manage go.work for host builds (git-ignored, fully managed here) ---
# Toggle on: host `go` builds (e.g. --local mode) resolve the on-disk sibling
# tee-node / tee-proxy. Toggle off: remove any stale go.work so host builds fall
# back to the pinned module versions in go.mod / tools/go.mod.
GOWORK_FILE="$PROJECT_DIR/go.work"
if [[ "$USE_LOCAL_SIBLINGS" == "true" ]]; then
    SIBLINGS_ROOT="$(cd "$PROJECT_DIR/../.." && pwd)"
    for sib in tee-node tee-proxy; do
        [[ -d "$SIBLINGS_ROOT/$sib" ]] || die "USE_LOCAL_SIBLINGS set but $SIBLINGS_ROOT/$sib is missing.\n  Clone it into $SIBLINGS_ROOT/, or unset USE_LOCAL_SIBLINGS to use pinned modules."
    done
    log "Writing $GOWORK_FILE (host builds use sibling tee-node / tee-proxy)"
    GO_VERSION=$(grep '^go ' "$PROJECT_DIR/go.mod" | awk '{print $2}')
    cat > "$GOWORK_FILE" <<EOF
// Generated by deploy/scripts/start-services.sh for USE_LOCAL_SIBLINGS (git-ignored).
// Points host \`go\` builds at the on-disk sibling tee-node / tee-proxy checkouts.
// Re-run start-services.sh without USE_LOCAL_SIBLINGS (or delete this file) to
// return to the pinned module versions in go.mod / tools/go.mod.
go $GO_VERSION

use (
	.
	./tools
	../../tee-node
	../../tee-proxy
)
EOF
    # The siblings may require a newer Go than this module's directive; `go work
    # use` raises the workspace's go line to whatever its members need.
    (cd "$PROJECT_DIR" && go work use . ./tools ../../tee-node ../../tee-proxy) || die "go work use failed"
elif [[ -f "$GOWORK_FILE" ]]; then
    log "Removing stale $GOWORK_FILE — host builds use pinned modules"
    rm -f "$GOWORK_FILE" "$PROJECT_DIR/go.work.sum"
fi

# ============================================================
# Docker Compose mode (default)
# ============================================================
if [[ "$USE_LOCAL" == "false" ]]; then
    log "Starting services with Docker Compose..."

    # Dockerfile expects SOURCE_DATE_EPOCH for reproducible builds — see REPRODUCIBILITY.md.
    # Without it, `touch -h -d @${SOURCE_DATE_EPOCH}` in the builder stage fails with "invalid date format '@'".
    if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
        if SOURCE_DATE_EPOCH=$(git -C "$PROJECT_DIR" log -1 --format=%ct 2>/dev/null) && [[ -n "$SOURCE_DATE_EPOCH" ]]; then
            export SOURCE_DATE_EPOCH
        else
            export SOURCE_DATE_EPOCH=0
        fi
    fi
    log "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"

    # --- Build tee-proxy image locally if no remote registry is configured ---
    if [[ -z "${REGISTRY:-}" ]]; then
        # In siblings mode an existing image is the pinned build, not the sibling,
        # so it is always rebuilt (Docker's layer cache keeps that cheap).
        if [[ "$USE_LOCAL_SIBLINGS" == "true" ]] || ! docker image inspect local/tee-proxy >/dev/null 2>&1; then
            if [[ "$USE_LOCAL_SIBLINGS" == "true" ]]; then
                # Dev build: use the on-disk sibling tee-proxy checkout. Its Dockerfile
                # declares no module proxy argument, so the build reads a copy with
                # GOPROXY declared after the first FROM; the copy is otherwise the
                # sibling's Dockerfile verbatim, and the checkout is the build context.
                TEE_PROXY_DIR="$SIBLINGS_ROOT/tee-proxy"
                [[ -d "$TEE_PROXY_DIR" ]] || die "USE_LOCAL_SIBLINGS set but tee-proxy repo not present at $TEE_PROXY_DIR.\n  Clone it into $SIBLINGS_ROOT/, or unset USE_LOCAL_SIBLINGS to build the pinned proxy."
                modcache_proxy_start
                log "Building local/tee-proxy from sibling checkout $TEE_PROXY_DIR (USE_LOCAL_SIBLINGS)..."
                awk '{ print } /^FROM / && !done { print "ARG GOPROXY=https://proxy.golang.org,direct"; print "ENV GOPROXY=$GOPROXY"; done = 1 }' "$TEE_PROXY_DIR/Dockerfile" \
                    | docker build --load --build-arg GOPROXY="$GOPROXY_BUILD" -f - -t local/tee-proxy "$TEE_PROXY_DIR" || die "Failed to build tee-proxy image"
            else
                # Default: pinned, self-contained build from the in-repo Dockerfile
                # (clones tee-proxy at the version in proxy/Dockerfile, matching tools/go.mod).
                PROXY_DOCKERFILE="$PROJECT_DIR/deploy/proxy/Dockerfile"
                [[ -f "$PROXY_DOCKERFILE" ]] || die "Image local/tee-proxy not found and proxy Dockerfile missing at $PROXY_DOCKERFILE.\n  Set REGISTRY in .env to pull from a remote registry, restore proxy/Dockerfile, or use USE_LOCAL_SIBLINGS=1 to build from a sibling checkout."
                log "Building local/tee-proxy from $PROXY_DOCKERFILE (pinned, self-cloning)..."
                docker build --load -f "$PROXY_DOCKERFILE" -t local/tee-proxy "$PROJECT_DIR/deploy/proxy" || die "Failed to build tee-proxy image"
            fi
            log "local/tee-proxy image built successfully"
        else
            log "local/tee-proxy image already exists (use 'docker rmi local/tee-proxy' to force rebuild)"
        fi
    fi

    COMPOSE_FILES=("-f" "$PROJECT_DIR/deploy/docker-compose.yaml")

    # Toggle: swap the pinned self-contained node build for the sibling-based one.
    if [[ "$USE_LOCAL_SIBLINGS" == "true" ]]; then
        log "USE_LOCAL_SIBLINGS — node built from on-disk tee-node via Dockerfile.siblings"
        COMPOSE_FILES+=("-f" "$PROJECT_DIR/deploy/docker-compose.siblings.yaml")
        modcache_proxy_start
    fi

    case "$CHAIN" in
        local) ;;
        coston)
            log "Coston mode — attaching docker-compose.coston.yaml"
            COMPOSE_FILES+=("-f" "$PROJECT_DIR/deploy/docker-compose.coston.yaml")
            ;;
        coston2)
            log "Coston2 mode — attaching docker-compose.coston2.yaml"
            COMPOSE_FILES+=("-f" "$PROJECT_DIR/deploy/docker-compose.coston2.yaml")
            ;;
    esac

    # A container created but never started by an aborted run keeps that run's
    # port binding and network id; starting it fails on either. Nothing lives in
    # such a container, so it is removed rather than diagnosed.
    for c in $(docker compose "${COMPOSE_FILES[@]}" ps -a --status created -q 2>/dev/null); do
        log "removing never-started container $(docker inspect --format '{{.Name}}' "$c" | tr -d /) left by an aborted run"
        docker rm -f "$c" >/dev/null
    done
    if [[ "$CHAIN" == "local" ]]; then
        PROXY_CONFIG_FILE="$PROJECT_DIR/out/extension_proxy.docker.local.toml"
        render_proxy_config "$PROJECT_DIR/deploy/config/proxy/extension_proxy.docker.toml" "$PROXY_CONFIG_FILE"
        export PROXY_CONFIG_FILE
    fi
    # Local mode recreates every container on each start. Redis is published on a
    # fresh port per run and so is always new, and a proxy that outlives its redis,
    # or the simulation it synced against, keeps signing-policy state for a chain
    # that no longer exists and never initialises the node for the current one.
    # A recreated node can come up under a new identity, so the machine has to be
    # registered again: full-setup.sh does that in its next phase. Coston stacks
    # keep their containers.
    UP_FLAGS=()
    [[ "$CHAIN" == "local" ]] && UP_FLAGS=(--force-recreate)
    docker compose "${COMPOSE_FILES[@]}" up -d --build "${UP_FLAGS[@]}" || die "docker compose up failed"

    # Wait for proxy to be ready
    E2E="$SCRIPT_DIR/e2e.sh"
    EXT_PROXY_URL="${EXT_PROXY_URL:-http://localhost:6674}"
    log "Waiting for extension proxy at $EXT_PROXY_URL/info ..."
    "$E2E" wait-for-url "$EXT_PROXY_URL/info" 120

    # Validate EXTENSION_ID is recognized by proxy
    log "Validating EXTENSION_ID against proxy..."
    PROXY_INFO=$(curl -sf "$EXT_PROXY_URL/info" 2>/dev/null || true)
    if [[ -n "$PROXY_INFO" ]]; then
        if ! echo "$PROXY_INFO" | grep -q "$EXTENSION_ID" 2>/dev/null; then
            echo -e "${RED}WARNING: EXTENSION_ID $EXTENSION_ID not found in proxy /info response${NC}" >&2
            echo -e "${RED}The proxy may be filtering for a different extension. Check config.${NC}" >&2
        fi
    fi

    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN} Services started (Docker Compose)${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo -e "${CYAN}Mode${NC}"
    case "$CHAIN" in
        local)   echo "  Local devnet" ;;
        coston)  echo "  Coston testnet (chain_id=16)" ;;
        coston2) echo "  Coston2 testnet (chain_id=114)" ;;
    esac
    echo ""
    echo -e "${CYAN}Services${NC}"
    echo "  redis, ext-proxy, extension-tee"
    echo "  Proxy URL: $EXT_PROXY_URL"
    echo "  Redis:     $REDIS_BIND on the host (redis-cli -h ${REDIS_BIND%:*} -p ${REDIS_BIND##*:})"
    echo ""
    echo -e "${CYAN}Commands${NC}"
    echo "  Logs:    docker compose ${COMPOSE_FILES[*]} logs -f"
    echo "  Stop:    ./deploy/scripts/stop-services.sh --chain $CHAIN"
    exit 0
fi

# ============================================================
# Host-process mode (--local)
# ============================================================
# tools/cmd/local-stack runs the proxy in-process and the extension TEE as its
# child, built from the module root (against the sibling tee-node when a go.work
# is present). Redis is the simulation's fourth instance unless
# LOCAL_STACK_REDIS names another. The proxy binds 6663 and 6664 on the host.
log "Starting the extension TEE and proxy as host processes (--local)..."
[[ "$USE_LOCAL_SIBLINGS" == "true" ]] || log "pinned tee-node (set USE_LOCAL_SIBLINGS=1 to build against the sibling checkouts, as a develop simulation needs)"

E2E="$SCRIPT_DIR/e2e.sh"
PID_DIR="$PROJECT_DIR/out/pids"; LOG_DIR="$PROJECT_DIR/out/logs"; BIN_DIR="$PROJECT_DIR/out/bin"
PIDFILE="$PID_DIR/local-stack.pid"; LOGFILE="$LOG_DIR/local-stack.log"
HOST_PROXY_URL="http://localhost:6664"
mkdir -p "$PID_DIR" "$LOG_DIR" "$BIN_DIR"

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
[[ -n "${LOCAL_STACK_REDIS:-}" ]] && ARGS+=(-redis "$LOCAL_STACK_REDIS")
"$E2E" start local-stack "$PIDFILE" "$LOGFILE" "$BIN_DIR/local-stack" "${ARGS[@]}"

# Ready means the proxy answers; the runner builds start-tee first (a minute
# against a cold cache), then brings the proxy up. Its summary, printed once the
# node reports its signing policy, is shown when it has appeared.
DEADLINE=$(( $(date +%s) + 240 ))
until curl -sf -m 3 "$HOST_PROXY_URL/info" >/dev/null 2>&1; do
    if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
        tail -n 25 "$LOGFILE" >&2
        die "local-stack exited; its log is above (full log: ${LOGFILE#$PROJECT_DIR/})"
    fi
    (( $(date +%s) >= DEADLINE )) && { tail -n 25 "$LOGFILE" >&2; die "the host proxy did not answer on $HOST_PROXY_URL within 240s"; }
    sleep 3
done
for _ in $(seq 20); do grep -q 'Extension stack running' "$LOGFILE" 2>/dev/null && break; sleep 3; done

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN} Services started (host processes)${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
sed -n '/Extension stack running/,/^Ctrl-C/p' "$LOGFILE" | grep -vE '^Continue with|^  EXT_PROXY_URL|^  \./deploy|^Ctrl-C|^$' \
    || echo "  (the runner has not printed its summary yet; see ${LOGFILE#$PROJECT_DIR/})"
echo ""
echo -e "${CYAN}Processes${NC}   local-stack PID $(cat "$PIDFILE"), log ${LOGFILE#$PROJECT_DIR/} (the TEE's lines tagged)"
echo -e "${CYAN}Proxy${NC}       $HOST_PROXY_URL — pass EXT_PROXY_URL=$HOST_PROXY_URL to the follow-up scripts"
echo -e "${CYAN}Stop${NC}        $SCRIPT_DIR/stop-services.sh --local"
