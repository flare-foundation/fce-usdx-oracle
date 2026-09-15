#!/usr/bin/env bash
#
# Start extension TEE node and proxy.
#
# By default, starts services via Docker Compose, picking the compose overlay
# from --chain (or env CHAIN, or legacy LOCAL_MODE):
#   --chain local            → docker-compose.yaml only (local devnet)
#   --chain coston|coston2|songbird|flare
#                            → + docker-compose.live.yaml, with the proxy
#                              configuration rendered for that chain
#
# A live chain also needs its C-chain indexer, which docker-compose.<chain>-indexer.yaml
# runs; start-services.sh refuses to start against one that is behind the chain.
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
#   - local: the e2e simulation running (Hardhat, indexer, Redis, normal TEE + proxy)
#   - a live chain: the chain's indexer stack up and caught up
#     (docker compose -f deploy/docker-compose.<chain>-indexer.yaml up -d)
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
# Sibling image builds fetch their Go modules from the host module cache.
# shellcheck source=lib/modcache-proxy.sh
source "$SCRIPT_DIR/lib/modcache-proxy.sh"
# load_env / record_stack: the proxy URL the follow-up scripts pick up.
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"
# The chains the scripts know, one row each.
# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"

# check_indexer_sync: the proxy refuses to serve until the chain indexer's
# database is within a minute of the chain, sleeping between checks (up to
# db_sync_max_sleep_time), so a lagging indexer looks like a proxy that never
# comes up. When the chain's indexer compose file runs its database here, the
# lag is read from it and a start against a lagging one is refused with the
# reason; an external indexer is not checked.
check_indexer_sync() {
    local file="$PROJECT_DIR/deploy/docker-compose.$CHAIN-indexer.yaml" svc="$CHAIN-indexer-db" behind
    [[ -f "$file" ]] || return 0
    behind="$(docker compose -f "$file" exec -T "$svc" mysql -uroot -proot db -N \
        -e "select unix_timestamp() - block_timestamp from states where name='last_database_block'" 2>/dev/null | tr -d '[:space:]')"
    if [[ -z "$behind" ]]; then
        warn "the $svc container is not running or has no indexed block yet — the proxy will wait for the indexer (docker compose -f ${file#"$PROJECT_DIR"/} up -d)"
        return 0
    fi
    (( behind <= 60 )) || die "the chain indexer is $behind s behind the chain; the proxy would not serve until it has caught up — follow it with: docker compose -f ${file#"$PROJECT_DIR"/} logs -f $CHAIN-indexer"
    log "Chain indexer: ${behind}s behind the chain"
}

# --- Parse flags ---
USE_LOCAL=false
CHAIN="${CHAIN:-}"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --local) USE_LOCAL=true; shift ;;
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value ($CHAINS_USAGE)"
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        *) die "Unknown argument: $1" ;;
    esac
done

# --- Load .env from project root (if present) ---
if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a
    # shellcheck source=/dev/null
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

# render_live_proxy_config <docker|host> → the path of the rendered file.
# A live chain's proxy configuration is generated per run from
# config/proxy/extension_proxy.live.toml, never edited by hand: the chain id,
# the three contract addresses (from the chain's deployed-addresses.json, which
# import-upstream-addresses.sh refreshes from the network's contract registry),
# the redis of this run, and the indexer database. A container reaches both over
# the compose network; host processes reach them on the host's ports, and take
# the port block tools/cmd/local-stack binds.
render_live_proxy_config() {
    local mode="$1" src="$PROJECT_DIR/deploy/config/proxy/extension_proxy.live.toml"
    local dst="$PROJECT_DIR/out/proxy/extension_proxy.$CHAIN.$mode.toml"
    local args=() pair key name addr db_host db_port redis
    [[ -f "$ADDRESSES_FILE_ABS" ]] || die "no addresses file at ${ADDRESSES_FILE_ABS#"$PROJECT_DIR"/} — run ./deploy/scripts/import-upstream-addresses.sh $CHAIN"
    for pair in flare_systems_manager:FlareSystemsManager relay:Relay voter_registry:VoterRegistry; do
        key="${pair%%:*}"; name="${pair##*:}"
        addr="$(jq -r --arg n "$name" '[.[] | select(.name == $n) | .address][0] // empty' "$ADDRESSES_FILE_ABS")"
        [[ -n "$addr" ]] || die "$name is not in ${ADDRESSES_FILE_ABS#"$PROJECT_DIR"/} — run ./deploy/scripts/import-upstream-addresses.sh $CHAIN"
        args+=(-e "s#^$key = .*#$key = \"$addr\"#")
    done
    args+=(-e "s#^chain_id = .*#chain_id = $CHAIN_ID#")
    # INDEXER_DB_* in .env names an indexer of your own; without them, the one
    # the chain's indexer compose file runs.
    if [[ -n "${INDEXER_DB_HOST:-}" ]]; then
        db_host="$INDEXER_DB_HOST"; db_port="${INDEXER_DB_PORT:-3306}"
    elif [[ "$mode" == docker ]]; then
        db_host="$CHAIN-indexer-db"; db_port=3306
    else
        db_host=127.0.0.1; db_port="$(chain_indexer_port "$CHAIN")"
        [[ -n "$db_port" ]] || die "no indexer database for $CHAIN — set INDEXER_DB_HOST/INDEXER_DB_PORT in .env to name one"
    fi
    args+=(-e "s#^host = .*#host = \"$db_host\"#" -e "s#^port = .*#port = $db_port#")
    [[ -n "${INDEXER_DB_NAME:-}" ]]     && args+=(-e "s#^database = .*#database = \"$INDEXER_DB_NAME\"#")
    [[ -n "${INDEXER_DB_USER:-}" ]]     && args+=(-e "s#^username = .*#username = \"$INDEXER_DB_USER\"#")
    [[ -n "${INDEXER_DB_PASSWORD:-}" ]] && args+=(-e "s#^password = .*#password = \"$INDEXER_DB_PASSWORD\"#")
    if [[ "$mode" == docker ]]; then
        redis="redis:6379"
    else
        redis="$LOCAL_STACK_REDIS"
        args+=(-e "s#^internal = .*#internal = \"6683\"#" -e "s#^external = .*#external = \"6684\"#")
    fi
    args+=(-e "s#^redis_port = .*#redis_port = \"$redis\"#")
    mkdir -p "$(dirname "$dst")"
    sed "${args[@]}" "$src" > "$dst"
    echo "$dst"
}

mkdir -p "$(dirname "$CONFIG_FILE")"
{ grep -vE '^REDIS_BIND=' "$CONFIG_FILE" 2>/dev/null || true; echo "REDIS_BIND=$REDIS_BIND"; } > "$CONFIG_FILE.tmp" && mv "$CONFIG_FILE.tmp" "$CONFIG_FILE"

# Reserved id 1 (pinned in internal/config); the upstream sender/store are
# not deployed locally. May be overridden for a test extension on another id.
EXTENSION_ID="${EXTENSION_ID:-1}"
EXT_PROXY_EXTERNAL_BIND="${EXT_PROXY_EXTERNAL_BIND:-0.0.0.0:6674}"
PROXY_PRIVATE_KEY="${PROXY_PRIVATE_KEY:-983760a4ebf75b2ac3a93531168a0f225d01e5dc6e3568adbd46233ba1fb4fa4}"
LOCAL_MODE="${LOCAL_MODE:-true}"

# --- Resolve CHAIN (flag > CHAIN in .env > its CHAIN_ID > the simulation) ---
CHAIN="$(resolve_chain "$CHAIN")"
chain_known "$CHAIN" || die "Unknown --chain value: $CHAIN (valid: $CHAINS_USAGE)"
if [[ "$CHAIN" != local ]]; then
    # The chain's own defaults, so a .env that names only the chain is enough.
    CHAIN_URL="${CHAIN_URL:-$(chain_rpc "$CHAIN")}"
    CHAIN_ID="${CHAIN_ID:-$(chain_id "$CHAIN")}"
    # FLARE_CHAIN, not CHAIN, for compose and the tools: cast reads CHAIN as --chain.
    FLARE_CHAIN="$CHAIN"; export FLARE_CHAIN CHAIN_URL CHAIN_ID
    [[ "$CHAIN_ID" == "$(chain_id "$CHAIN")" ]] \
        || die "CHAIN_ID=$CHAIN_ID in .env is not $CHAIN's ($(chain_id "$CHAIN")) — is .env the right one?"
fi

[[ -n "$EXTENSION_ID" ]] || die "EXTENSION_ID is empty"

# The chain's addresses, for the proxy configuration and the follow-up scripts.
ADDRESSES_FILE_ABS="${ADDRESSES_FILE:-deploy/config/$CHAIN/deployed-addresses.json}"
[[ "$ADDRESSES_FILE_ABS" != /* ]] && ADDRESSES_FILE_ABS="$PROJECT_DIR/$ADDRESSES_FILE_ABS"

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

    # The Dockerfile needs SOURCE_DATE_EPOCH, the commit timestamp: it selects the
    # Debian snapshot the builder installs from and clamps every mtime, so a build
    # without one fails and a zero would ask for a 1970 snapshot (REPRODUCIBILITY.md).
    if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
        SOURCE_DATE_EPOCH=$(git -C "$PROJECT_DIR" log -1 --format=%ct 2>/dev/null) && [[ -n "$SOURCE_DATE_EPOCH" ]] \
            || die "SOURCE_DATE_EPOCH is not set and this is not a git checkout: export the commit timestamp of the sources being built"
        export SOURCE_DATE_EPOCH
    fi
    log "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"

    # --- The proxy image ---
    # The compose file names tee-proxy's published image at the pinned tag, so
    # nothing is built here. In siblings mode the on-disk checkout is built with
    # its own Dockerfile into local/tee-proxy and named to compose instead; an
    # existing local/tee-proxy may be an older build, so it is always rebuilt
    # (Docker's layer cache keeps that cheap). TEE_PROXY_IMAGE in .env overrides
    # either.
    if [[ "$USE_LOCAL_SIBLINGS" == "true" ]]; then
        # The sibling's Dockerfile declares no module proxy argument, so the build
        # reads a copy with GOPROXY declared after the first FROM; the copy is
        # otherwise the sibling's Dockerfile verbatim, the checkout the context.
        TEE_PROXY_DIR="$SIBLINGS_ROOT/tee-proxy"
        [[ -d "$TEE_PROXY_DIR" ]] || die "USE_LOCAL_SIBLINGS set but tee-proxy repo not present at $TEE_PROXY_DIR.\n  Clone it into $SIBLINGS_ROOT/, or unset USE_LOCAL_SIBLINGS to run the published image."
        modcache_proxy_start
        log "Building local/tee-proxy from sibling checkout $TEE_PROXY_DIR (USE_LOCAL_SIBLINGS)..."
        awk '{ print } /^FROM / && !done { print "ARG GOPROXY=https://proxy.golang.org,direct"; print "ENV GOPROXY=$GOPROXY"; done = 1 }' "$TEE_PROXY_DIR/Dockerfile" \
            | docker build --load --build-arg GOPROXY="$GOPROXY_BUILD" -f - -t local/tee-proxy "$TEE_PROXY_DIR" || die "Failed to build tee-proxy image"
        TEE_PROXY_IMAGE="local/tee-proxy"; export TEE_PROXY_IMAGE
    elif [[ -n "${TEE_PROXY_IMAGE:-}" ]]; then
        log "proxy image: $TEE_PROXY_IMAGE (TEE_PROXY_IMAGE)"
    else
        log "proxy image: tee-proxy's published one at the pinned tag (docker-compose.yaml)"
    fi

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
    # The testnet overlays mount a proxy configuration the operator copies from
    # its example; it is git-ignored because it can carry database credentials.
    if [[ "$CHAIN" != "local" ]]; then
        check_indexer_sync
        PROXY_CONFIG_FILE="$(render_live_proxy_config docker)" || exit 1
        export PROXY_CONFIG_FILE
        log "proxy config rendered to ${PROXY_CONFIG_FILE#"$PROJECT_DIR"/} (addresses from ${ADDRESSES_FILE_ABS#"$PROJECT_DIR"/})"
    fi
    UP_FLAGS=()
    [[ "$CHAIN" == "local" ]] && UP_FLAGS=(--force-recreate)
    docker compose "${COMPOSE_FILES[@]}" up -d --build "${UP_FLAGS[@]}" || die "docker compose up failed"
    # The simulation's follow-up scripts find this stack's proxy through
    # deploy/config/stack.env; a testnet operator's EXT_PROXY_URL in .env is
    # the public URL and stays untouched.
    [[ "$CHAIN" == "local" ]] && record_stack docker "http://localhost:${EXT_PROXY_EXTERNAL_BIND##*:}"

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
    if [[ "$CHAIN" == local ]]; then
        echo "  Local devnet"
    else
        echo "  $CHAIN ($(chain_kind "$CHAIN"), chain_id=$CHAIN_ID)"
    fi
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
# LOCAL_STACK_REDIS names another; on a testnet, where no simulation runs, the
# base compose file's redis is started on REDIS_BIND for it. The proxy binds
# 6683 and 6684 on the host, clear of the e2e simulation's own processes; on a
# testnet the tunnel behind EXT_PROXY_URL forwards to 6684.
log "Starting the extension TEE and proxy as host processes (--local)..."
[[ "$USE_LOCAL_SIBLINGS" == "true" ]] || log "pinned tee-node (set USE_LOCAL_SIBLINGS=1 to build against the sibling checkouts, as a develop simulation needs)"

E2E="$SCRIPT_DIR/e2e.sh"
PID_DIR="$PROJECT_DIR/out/pids"; LOG_DIR="$PROJECT_DIR/out/logs"; BIN_DIR="$PROJECT_DIR/out/bin"
PIDFILE="$PID_DIR/local-stack.pid"; LOGFILE="$LOG_DIR/local-stack.log"
HOST_PROXY_URL="http://localhost:6684"
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
if [[ "$CHAIN" != "local" ]]; then
    # The proxy configuration of the chain, host variant: its [db] names the
    # indexer database on the host port the chain's indexer compose file
    # publishes. Rendered below, once this run's redis is known.
    HOST_TOML=""   # rendered below, once the redis of this run is known
    # The chain's proxy configuration enables /direct behind DIRECT_API_KEY, and
    # the proxy refuses a configuration without the key; said here rather than
    # from inside the runner.
    [[ -n "${DIRECT_API_KEY:-}" ]] || die "DIRECT_API_KEY is empty in .env — the proxy's /direct needs it (openssl rand -hex 32)"
    check_indexer_sync
    [[ -n "${EXT_PROXY_URL:-}" ]] || warn "EXT_PROXY_URL is not set; post-build.sh registers the machine under it — the tunnel in front of port 6684"
    if [[ -z "${LOCAL_STACK_REDIS:-}" ]]; then
        # No simulation, so no redis on 6382: the base compose file's redis,
        # published on the free port picked above, serves the host proxy. The
        # chain's indexer file joins the set so compose recognises the indexer
        # containers of the same project instead of reporting them as orphans.
        log "Starting the compose redis on $REDIS_BIND for the host proxy..."
        REDIS_FILES=(-f "$PROJECT_DIR/deploy/docker-compose.yaml")
        [[ -f "$PROJECT_DIR/deploy/docker-compose.$CHAIN-indexer.yaml" ]] && REDIS_FILES+=(-f "$PROJECT_DIR/deploy/docker-compose.$CHAIN-indexer.yaml")
        REDIS_OUT="$(SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-0}" docker compose "${REDIS_FILES[@]}" up -d redis 2>&1)" \
            || die "starting redis failed:\n$REDIS_OUT"
        for _ in $(seq 30); do (echo > "/dev/tcp/${REDIS_BIND%:*}/${REDIS_BIND##*:}") >/dev/null 2>&1 && break; sleep 1; done
        LOCAL_STACK_REDIS="$REDIS_BIND"
    fi
    HOST_TOML="$(render_live_proxy_config host)" || exit 1
    ARGS+=(-template "${HOST_TOML#"$PROJECT_DIR"/}")
    log "proxy config rendered to ${HOST_TOML#"$PROJECT_DIR"/} (addresses from ${ADDRESSES_FILE_ABS#"$PROJECT_DIR"/})"
fi
[[ -n "${LOCAL_STACK_REDIS:-}" ]] && ARGS+=(-redis "$LOCAL_STACK_REDIS")
"$E2E" start local-stack "$PIDFILE" "$LOGFILE" "$BIN_DIR/local-stack" "${ARGS[@]}"

# Ready means the proxy answers; the runner builds start-tee first (a minute
# against a cold cache), then brings the proxy up. Its summary, printed once the
# node reports its signing policy, is shown when it has appeared.
DEADLINE=$(( $(date +%s) + 420 ))
until curl -sf -m 3 "$HOST_PROXY_URL/info" >/dev/null 2>&1; do
    if ! kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
        tail -n 25 "$LOGFILE" >&2
        die "local-stack exited; its log is above (full log: ${LOGFILE#"$PROJECT_DIR"/})"
    fi
    (( $(date +%s) >= DEADLINE )) && { tail -n 25 "$LOGFILE" >&2; die "the host proxy did not answer on $HOST_PROXY_URL within 420s"; }
    sleep 3
done
for _ in $(seq 20); do grep -q 'Extension stack running' "$LOGFILE" 2>/dev/null && break; sleep 3; done
[[ "$CHAIN" == "local" ]] && record_stack host "$HOST_PROXY_URL"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN} Services started (host processes)${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
sed -n '/Extension stack running/,/^Ctrl-C/p' "$LOGFILE" | grep -vE '^Continue with|^  EXT_PROXY_URL|^  \./deploy|^Ctrl-C|^$' \
    || echo "  (the runner has not printed its summary yet; see ${LOGFILE#"$PROJECT_DIR"/})"
echo ""
echo -e "${CYAN}Processes${NC}   local-stack PID $(cat "$PIDFILE"), log ${LOGFILE#"$PROJECT_DIR"/} (the TEE's lines tagged)"
if [[ "$CHAIN" == "local" ]]; then
    echo -e "${CYAN}Proxy${NC}       $HOST_PROXY_URL (recorded in deploy/config/stack.env for the follow-up scripts)"
else
    echo -e "${CYAN}Proxy${NC}       $HOST_PROXY_URL on the host; public URL ${EXT_PROXY_URL:-<EXT_PROXY_URL unset>} — the tunnel must forward to port 6684 (ngrok http 6684 --url …)"
    echo -e "${CYAN}Redis${NC}       compose service on $LOCAL_STACK_REDIS"
fi
echo -e "${CYAN}Stop${NC}        $SCRIPT_DIR/stop-services.sh --local"
