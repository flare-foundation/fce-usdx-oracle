#!/usr/bin/env bash
# full-setup.sh — Bring a machine up for a chain: start → post-build.
#
# The oracle contracts (TeeOracleInstructionsSender, TeeOracleFeedStore) must
# already be on the chain and in its addresses file: upstream's deployment,
# brought in by import-upstream-addresses.sh, or a test deployment of our own
# made by deploy-oracle.sh (local, coston_local, coston2), followed by
# tools/cmd/wire-extension. This script records EXTENSION_ID from .env in
# deploy/config/extension.env, starts the services, and registers the TEE
# machine. The id has no default in the extension; 1, the reserved id upstream's
# configuration uses, is this script's when .env names none, and a public id is
# what deploy-oracle.sh recorded in .env when the registry minted one.
#
# Usage:
#   ./deploy/scripts/full-setup.sh                          # setup only, docker compose, the chain .env names (default local)
#   ./deploy/scripts/full-setup.sh --local                  # extension TEE and proxy as host processes (tools/cmd/local-stack)
#   ./deploy/scripts/full-setup.sh --chain coston           # target Coston testnet (chain_id=16)
#   ./deploy/scripts/full-setup.sh --chain coston_local     # our own test deployment on Coston
#   ./deploy/scripts/full-setup.sh --chain coston2          # target Coston2 testnet
#
# Flags:
#   --chain <name>   local | coston | coston_local | coston2 | songbird | flare
#                    (default: the chain .env names, else local)
#   --local          run the extension TEE and proxy as host processes instead of containers
#
# End-to-end validation of the running feed is a separate step — see
# scripts/oracle-cycle.sh and docs/testing.md ("Running the USDX oracle end to
# end yourself"). It needs a provisioned machine, so it is not part of setup.
#
# Prerequisites:
#   - Local devnet: Hardhat, indexer, Redis, normal TEE + proxy running.
#   - A live chain: its indexer stack up and caught up
#     (docker compose -f deploy/docker-compose.<chain>-indexer.yaml up -d),
#     or INDEXER_DB_* in .env naming an indexer of your own.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[full-setup]${NC} $*"; }
warn() { echo -e "${YELLOW}[full-setup]${NC} $*"; }
die()  { echo -e "${RED}[full-setup] ERROR:${NC} $*" >&2; exit 1; }
# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"

USE_LOCAL=false
CHAIN=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --local) USE_LOCAL=true; shift ;;
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value ($CHAINS_USAGE)"
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        *) die "Unknown argument: $1" ;;
    esac
done

# --- Auto-activate chain-specific env when --chain was passed explicitly ---
# If --chain <name> was given and a .env.<name> exists, activate it BEFORE
# loading, so --chain actually switches chains. Through use-chain.sh rather than
# a copy, so the Hex credentials in the active .env survive the switch the same
# way they do when the operator switches by hand. Put per-chain values in
# .env.<name>; note this replaces the active .env.
if [[ -n "$CHAIN" && -f "$PROJECT_DIR/.env.$CHAIN" ]]; then
    "$SCRIPT_DIR/use-chain.sh" "$CHAIN" || exit 1
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

CHAIN="$(resolve_chain "$CHAIN")"
chain_known "$CHAIN" || die "Unknown --chain value: $CHAIN (valid: $CHAINS_USAGE)"

# --- The chain's defaults, where .env named nothing (lib/chains.sh) ---
if [[ "$CHAIN" == local ]]; then
    export LOCAL_MODE="true"
else
    export LOCAL_MODE="false"
    export ADDRESSES_FILE="${ADDRESSES_FILE:-$PROJECT_DIR/deploy/config/$CHAIN/deployed-addresses.json}"
    export CHAIN_URL="${CHAIN_URL:-$(chain_rpc "$CHAIN")}"
    export CHAIN_ID="${CHAIN_ID:-$(chain_id "$CHAIN")}"
    export NORMAL_PROXY_URL="${NORMAL_PROXY_URL:-$(chain_normal_proxy "$CHAIN")}"
    # A mainnet registry accepts no simulated machine: a stack there is a real
    # Confidential Space VM, whose .env says so. Both settings are checked: the
    # live overlay defaults MODE to 1, simulated attestation, when .env names none.
    if chain_is_mainnet "$CHAIN" && [[ "${SIMULATED_TEE:-false}" == true || "${MODE:-1}" != 0 ]]; then
        die "SIMULATED_TEE=${SIMULATED_TEE:-<unset>} and MODE=${MODE:-<unset, so 1>} on $CHAIN: a mainnet registry accepts only real attestation (GCP_*). Set SIMULATED_TEE=false and MODE=0, and run the machine on a Confidential Space VM (docs/deployment-steps.md)."
    fi
fi
FLARE_CHAIN="$CHAIN"; export FLARE_CHAIN

log "Chain: $CHAIN"

# --- Phase 1: Pin extension identity ---
# The extension id from .env, which tee-node and the extension both read from
# deploy/config/extension.env; the feed store address is resolved from the
# addresses file when the keeper runs.
echo -e "\n${CYAN}╔══════════════════════════════════════╗${NC}"
echo -e "${CYAN}║  Phase 1: Pin extension identity      ║${NC}"
echo -e "${CYAN}╚══════════════════════════════════════╝${NC}"
EXTENSION_ID="${EXTENSION_ID:-1}"
cat > "$PROJECT_DIR/deploy/config/extension.env" <<EOF
# Auto-generated by full-setup.sh from EXTENSION_ID in .env. tee-node and the
# extension both read it: 1 is the reserved id upstream's configuration uses, a
# public id is what deploy-oracle.sh recorded when the registry minted one.
EXTENSION_ID=$EXTENSION_ID
EOF

log "EXTENSION_ID=$EXTENSION_ID"

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
