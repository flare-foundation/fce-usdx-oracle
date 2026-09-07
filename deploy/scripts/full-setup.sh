#!/usr/bin/env bash
# full-setup.sh — Run the complete extension lifecycle: start → post-build.
#
# The extension contracts (TeeOracleInstructionsSender, TeeOracleFeedStore) are
# upstream from flare-smart-contracts-v2 and already on-chain — nothing is
# deployed locally, so there is no pre-build phase. The extension's EXTENSION_ID
# is the reserved id 1 (pinned in internal/config). This script just pins
# deploy/config/extension.env, starts the services, and registers the TEE machine.
#
# Usage:
#   ./deploy/scripts/full-setup.sh                          # setup only, docker compose, local devnet
#   ./deploy/scripts/full-setup.sh --local                  # extension TEE and proxy as host processes (tools/cmd/local-stack)
#   ./deploy/scripts/full-setup.sh --chain coston           # target Coston testnet (chain_id=16)
#   ./deploy/scripts/full-setup.sh --chain coston2          # target Coston2 testnet
#
# Flags:
#   --chain <name>   local | coston | coston2 (default: local)
#   --local          run the extension TEE and proxy as host processes instead of containers
#
# End-to-end validation of the running feed is a separate step — see
# scripts/oracle-cycle.sh and docs/testing.md ("Running the USDX oracle end to
# end yourself"). It needs a provisioned machine, so it is not part of setup.
#
# Prerequisites:
#   - Local devnet: Hardhat, indexer, Redis, normal TEE + proxy running.
#   - Coston/Coston2: a reachable chain indexer DB. For Coston, bring up
#     e2e/docker/coston (coston-indexer-db + coston-indexer) before this script.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[full-setup]${NC} $*"; }
warn() { echo -e "${YELLOW}[full-setup]${NC} $*"; }
die()  { echo -e "${RED}[full-setup] ERROR:${NC} $*" >&2; exit 1; }

USE_LOCAL=false
CHAIN=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --local) USE_LOCAL=true; shift ;;
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value (local|coston|coston2)"
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        *) die "Unknown argument: $1" ;;
    esac
done

# --- Auto-activate chain-specific env when --chain was passed explicitly ---
# If --chain <name> was given and a .env.<name> exists, activate it (copy → .env)
# BEFORE loading, so --chain actually switches chains. Put per-chain values in
# .env.<name>; note this overwrites the active .env.
if [[ -n "$CHAIN" && -f "$PROJECT_DIR/.env.$CHAIN" ]]; then
    cp "$PROJECT_DIR/.env.$CHAIN" "$PROJECT_DIR/.env"
    log "Activated .env.$CHAIN → .env"
elif [[ -n "$CHAIN" && "$CHAIN" != "local" && ! -f "$PROJECT_DIR/.env.$CHAIN" ]]; then
    warn "No .env.$CHAIN found — using the existing .env as-is, which may target a different chain."
    warn "Create .env.$CHAIN (e.g. from .env.example) so '--chain $CHAIN' switches automatically."
fi

# --- Resolve chain (flag > env > legacy LOCAL_MODE) ---
# Load .env first so user-set defaults are visible
if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$PROJECT_DIR/.env"
    set +a
fi

if [[ -z "$CHAIN" ]]; then
    if [[ -n "${CHAIN:-}" ]]; then
        :  # honor env CHAIN
    elif [[ "${LOCAL_MODE:-true}" == "true" ]]; then
        CHAIN="local"
    else
        # Legacy default: LOCAL_MODE=false meant Coston2
        CHAIN="coston2"
    fi
fi

# --- Apply chain defaults (don't clobber explicit env settings) ---
case "$CHAIN" in
    local)
        export LOCAL_MODE="true"
        ;;
    coston)
        export LOCAL_MODE="false"
        export ADDRESSES_FILE="${ADDRESSES_FILE:-$PROJECT_DIR/deploy/config/coston/deployed-addresses.json}"
        export CHAIN_URL="${CHAIN_URL:-https://coston-api.flare.network/ext/C/rpc}"
        export NORMAL_PROXY_URL="${NORMAL_PROXY_URL:-https://tee-proxy-coston-1.flare.rocks}"
        ;;
    coston2)
        export LOCAL_MODE="false"
        export ADDRESSES_FILE="${ADDRESSES_FILE:-$PROJECT_DIR/deploy/config/coston2/deployed-addresses.json}"
        export CHAIN_URL="${CHAIN_URL:-https://coston2-api.flare.network/ext/C/rpc}"
        export NORMAL_PROXY_URL="${NORMAL_PROXY_URL:-https://tee-proxy-coston2-1.flare.rocks}"
        ;;
    *) die "Unknown --chain value: $CHAIN (valid: local, coston, coston2)" ;;
esac
export CHAIN

log "Chain: $CHAIN"

# --- Indexer reachability hint for testnets ---
if [[ "$CHAIN" == "coston" ]]; then
    if ! (echo > /dev/tcp/127.0.0.1/3306) >/dev/null 2>&1; then
        warn "No service on 127.0.0.1:3306 — the ext-proxy needs a Coston indexer DB."
        warn "Start it with: (cd ../../e2e/docker/coston && docker compose up -d coston-indexer-db coston-indexer)"
    fi
fi

# --- Phase 1: Pin extension identity ---
# The extension id is the reserved 1 (pinned in internal/config); the feed
# store address is resolved from the addresses file when the keeper runs.
echo -e "\n${CYAN}╔══════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  Phase 1: Pin extension identity      ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════╝${NC}"
EXTENSION_ID="${EXTENSION_ID:-1}"
cat > "$PROJECT_DIR/deploy/config/extension.env" <<EOF
# Auto-generated by full-setup.sh. tee-node and the extension both read this
# EXTENSION_ID; the reserved id 1 is the build default. There is no separately-
# deployed local contract, so this file carries only the identity the container needs.
EXTENSION_ID=$EXTENSION_ID
EOF

log "EXTENSION_ID=$EXTENSION_ID (reserved id 1)"

# --- Phase 2: Start services (TEE node + proxy) ---
echo -e "\n${CYAN}╔══════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  Phase 2: Start services             ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════╝${NC}"
START_ARGS=(--chain "$CHAIN")
[[ "$USE_LOCAL" == "true" ]] && START_ARGS+=(--local)
"$SCRIPT_DIR/start-services.sh" "${START_ARGS[@]}" || die "Failed to start services"

# --- Phase 3: Post-build (register TEE version + machine) ---
echo -e "\n${CYAN}╔══════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  Phase 3: Post-build                 ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════╝${NC}"
"$SCRIPT_DIR/post-build.sh" || die "Post-build failed"

echo ""
echo -e "${GREEN}========================================${NC}"
echo -e "${GREEN} Full setup complete${NC}"
echo -e "${GREEN}========================================${NC}"
echo ""
echo -e "${CYAN}Publish the configuration:${NC}"
echo "  ./deploy/scripts/publish-local.sh endpoints"
echo "  ./deploy/scripts/publish-local.sh admins"
echo -e "${CYAN}Validate the feed end to end:${NC} ./deploy/scripts/oracle-cycle.sh -tee <machine> --dry-run   (two minutes after the publications)"
echo ""
STOP_HINT="./deploy/scripts/stop-services.sh --chain $CHAIN"
[[ "$USE_LOCAL" == "true" ]] && STOP_HINT="$STOP_HINT --local"
echo -e "${CYAN}Stop services:${NC}  $STOP_HINT"
