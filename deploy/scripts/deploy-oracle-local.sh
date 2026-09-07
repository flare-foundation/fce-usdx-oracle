#!/usr/bin/env bash
# deploy-oracle-local.sh — put the TEE oracle contracts on the local Hardhat
# simulation and make reserved extension 1 ours.
#
# The contracts are upstream's (flare-smart-contracts-v2), compiled from the
# sibling checkout by contracts/script/DeployTeeOracleLocal.s.sol, which repeats
# upstream's deploy for this network except for its last step: the sender and the
# feed store stay governed by the deployer instead of being switched to
# production mode, so publications execute at once instead of waiting out the
# simulation's hour-long governance timelock. The deployer is the key the tools
# sign with (DEPLOYMENT_PRIVATE_KEY). Then extension 1 is reserved for
# INITIAL_OWNER (immediate: the sim's FlareTeeManager is not in production mode
# either) and every address lands in deploy/config/local/.
#
# Reads .env (activate .env.local first): CHAIN_URL, LOCAL_GOVERNANCE_KEY,
# DEPLOYMENT_PRIVATE_KEY, INITIAL_OWNER, UPSTREAM_DIR, SIM_DUMP. Needs forge, cast
# and jq.
#
# Usage:
#   ./deploy/scripts/use-chain.sh local
#   ./deploy/scripts/deploy-oracle-local.sh              # deploy once, reuse if still on chain
#   ./deploy/scripts/deploy-oracle-local.sh --refresh    # redeploy (after a new sim run)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
[[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[oracle-local]${NC} $*"; }
warn() { echo -e "${YELLOW}[oracle-local] WARN:${NC} $*" >&2; }
die()  { echo -e "${RED}[oracle-local] ERROR:${NC} $*" >&2; exit 1; }
section() { echo -e "\n${CYAN}=== $* ===${NC}"; }

REFRESH=false
[[ "${1:-}" == "--refresh" ]] && REFRESH=true

CHAIN_URL="${CHAIN_URL:-http://127.0.0.1:8545}"
UPSTREAM="${UPSTREAM_DIR:-$PROJECT_DIR/../../../fsp/flare-smart-contracts-v2}"
[[ "$UPSTREAM" != /* ]] && UPSTREAM="$PROJECT_DIR/$UPSTREAM"
SIM_DUMP="${SIM_DUMP:-$PROJECT_DIR/../../e2e/docker/sim_dump/deployed-addresses.json}"
[[ "$SIM_DUMP" != /* ]] && SIM_DUMP="$PROJECT_DIR/$SIM_DUMP"
OWNER="${INITIAL_OWNER:-0xF4E021377420Afe90c1A7D2b8968904946633a64}"
GOV_KEY="${LOCAL_GOVERNANCE_KEY:-}"
DEPLOYER_KEY="${DEPLOYMENT_PRIVATE_KEY:-}"
# forge reads the key as a uint and insists on the 0x prefix; .env may omit it.
[[ -z "$DEPLOYER_KEY" || "$DEPLOYER_KEY" == 0x* ]] || DEPLOYER_KEY="0x$DEPLOYER_KEY"

for t in forge cast jq; do command -v "$t" >/dev/null || die "$t is required"; done
[[ -n "$GOV_KEY" ]] || die "LOCAL_GOVERNANCE_KEY is not set — activate .env.local"
[[ -n "$DEPLOYER_KEY" ]] || die "DEPLOYMENT_PRIVATE_KEY is not set — activate .env.local"
[[ -d "$UPSTREAM/contracts" ]] || die "no flare-smart-contracts-v2 checkout at $UPSTREAM (UPSTREAM_DIR)"
[[ -f "$SIM_DUMP" ]] || die "no sim dump at $SIM_DUMP — bring the e2e stack up first (SIM_DUMP)"
CHAIN_ID="$(cast chain-id --rpc-url "$CHAIN_URL" 2>/dev/null)" || die "no chain at $CHAIN_URL"
[[ "$CHAIN_ID" == "31337" ]] || die "chain at $CHAIN_URL has id $CHAIN_ID, not the Hardhat sim (31337)"
GOV_ADDR="$(cast wallet address --private-key "$GOV_KEY")"

# The sim's Hardhat rejects cast's call objects, which carry both `input` and
# `data`, so reads go through a hand-built eth_call and sends carry an explicit
# gas limit, which keeps cast from asking the node to estimate.
eth_call() {  # eth_call <to> <sig> [args...] → raw hex return data; sets CALL_ERR on revert
    local to="$1" sig="$2"; shift 2
    local data; data="$(cast calldata "$sig" "$@")"
    CALL_ERR=""
    local out
    out="$(cast rpc eth_call "{\"from\":\"$GOV_ADDR\",\"to\":\"$to\",\"data\":\"$data\"}" latest --rpc-url "$CHAIN_URL" 2>&1 || true)"
    if [[ "$out" == '"0x'* ]]; then echo "${out//\"/}"; else CALL_ERR="$out"; echo ""; fi
}
GAS_LIMIT="${GAS_LIMIT:-3000000}"

# Account 0 is the simulation's busiest sender, so a transaction from it can lose
# the nonce it was built with. cast picks the nonce up again on every attempt.
send_retry() {  # send_retry <key> <cast send args...>
    local key="$1"; shift
    local attempt out
    for attempt in 1 2 3 4 5; do
        if out="$(cast send "$@" --rpc-url "$CHAIN_URL" --private-key "$key" --gas-limit "$GAS_LIMIT" --json 2>&1)"; then
            # An explicit gas limit skips estimation, so a revert surfaces only in the receipt.
            [[ "$(jq -r .status <<<"$out" 2>/dev/null)" == "0x1" ]] || { echo "reverted: $(jq -c '{transactionHash, status}' <<<"$out" 2>/dev/null || echo "$out")"; return 1; }
            echo "$out"; return 0
        fi
        grep -qiE 'nonce|replacement|underpriced|already known' <<<"$out" || { echo "$out"; return 1; }
        sleep 1
    done
    echo "$out"; return 1
}

LOCAL_ADDRS="$PROJECT_DIR/deploy/config/local/deployed-addresses.json"
GS_ADDR="${GOVERNANCE_SETTINGS_ADDRESS:-0x1000000000000000000000000000000000000007}"
mkdir -p "$PROJECT_DIR/out"
DEPLOY_OUT="$PROJECT_DIR/out/deploy-oracle-local.json"

section "Oracle contracts"
# A new simulation has a new AddressUpdater; every oracle address recorded
# against the old one is stale, so the deploy runs again.
if [[ -f "$LOCAL_ADDRS" && "$(jq -r '[.[] | select(.name=="AddressUpdater") | .address][0] // empty' "$LOCAL_ADDRS")" != "$(jq -r .AddressUpdater "$SIM_DUMP")" ]]; then
    REFRESH=true
fi
SENDER="$(jq -r '[.[] | select(.name=="TeeOracleInstructionsSender") | .address][0] // empty' "$LOCAL_ADDRS" 2>/dev/null || true)"
if [[ "$REFRESH" == "false" && -n "$SENDER" && "$(cast code "$SENDER" --rpc-url "$CHAIN_URL")" != "0x" ]]; then
    log "TeeOracleInstructionsSender already on chain at $SENDER — keeping it (pass --refresh to redeploy)"
    # The importer re-seeds the local file from the dump; the oracle entries ride along.
    jq '[.[] | select(.name | test("TeeOracle|FeedStore"))]' "$LOCAL_ADDRS" > "$DEPLOY_OUT"
else
    [[ "$(cast code "$GS_ADDR" --rpc-url "$CHAIN_URL")" != "0x" ]] || die "no GovernanceSettings code at $GS_ADDR — not a Flare simulation?"
    # The contracts are upstream's sources; their OpenZeppelin and flare-periphery
    # imports resolve through upstream's Soldeer dependencies/, which a fresh
    # checkout does not have.
    MISSING_DEPS=""
    while IFS= read -r line; do
        # `name = "version"` lines under [dependencies]; the name may itself be quoted.
        name="$(echo "$line" | cut -d= -f1 | tr -d ' "')"; ver="$(echo "$line" | cut -d= -f2 | tr -d ' "')"
        [[ -n "$name" && -n "$ver" && ! -d "$UPSTREAM/dependencies/$name-$ver" ]] && MISSING_DEPS="$MISSING_DEPS $name-$ver"
    done < <(awk '/^\[dependencies\]/{f=1; next} /^\[/{f=0} f && /=/' "$UPSTREAM/foundry.toml")
    if [[ -n "$MISSING_DEPS" ]]; then
        log "installing upstream's Foundry dependencies (missing:$MISSING_DEPS)"
        ( cd "$UPSTREAM" && forge soldeer install >/dev/null ) || die "forge soldeer install failed in $UPSTREAM"
    fi
    DEPLOYER_ADDR="$(cast wallet address --private-key "$DEPLOYER_KEY")"
    log "deploying from $DEPLOYER_ADDR, which stays the sender's governance (no production mode, no timelock)"
    FORGE_LOG="$PROJECT_DIR/out/deploy-oracle-local.forge.log"
    # --slow confirms each transaction before the next: forge precomputes the
    # CREATE addresses, and a nonce moving under it would leave proxies pointing
    # at implementations that never land.
    ( cd "$PROJECT_DIR/contracts" && \
        DEPLOYER_PRIVATE_KEY="$DEPLOYER_KEY" GOVERNANCE_SETTINGS="$GS_ADDR" \
        ADDRESS_UPDATER="$(jq -r .AddressUpdater "$SIM_DUMP")" FLARE_TEE_MANAGER="$(jq -r .FlareTeeManager "$SIM_DUMP")" \
        FDC2_VERIFICATION="$(jq -r .Fdc2Verification "$SIM_DUMP")" FEE_CALCULATOR="$(jq -r .FeeCalculator "$SIM_DUMP")" \
        forge script script/DeployTeeOracleLocal.s.sol:DeployTeeOracleLocal \
            --rpc-url "$CHAIN_URL" --private-key "$DEPLOYER_KEY" --broadcast --slow --non-interactive ) 2>&1 | tee "$FORGE_LOG" \
        || die "deploy failed (log: ${FORGE_LOG#$PROJECT_DIR/})"
    # One DEPLOYED line per contract, in upstream's format; they become a deploys
    # file in upstream's shape for the importer.
    grep -E '^[[:space:]]*DEPLOYED:' "$FORGE_LOG" \
        | sed -E 's/^[[:space:]]*DEPLOYED:[[:space:]]*([^,]+),[[:space:]]*([^:]+):[[:space:]]*(0x[0-9a-fA-F]{40}).*/\1|\2|\3/' \
        | jq -Rn '[inputs | split("|") | {name: .[0], contractName: .[1], address: .[2]}]' > "$DEPLOY_OUT"
    SENDER="$(jq -r '[.[] | select(.name=="TeeOracleInstructionsSender") | .address][0] // empty' "$DEPLOY_OUT")"
    [[ -n "$SENDER" ]] || die "deploy ran but printed no TeeOracleInstructionsSender address (log: ${FORGE_LOG#$PROJECT_DIR/})"
    log "sender at $SENDER ($(jq length "$DEPLOY_OUT") contracts)"
fi

section "Reserved extension 1 → $OWNER"
FTM="$(jq -r .FlareTeeManager "$SIM_DUMP")"
# Preflight as governance: a clean call means the registration would land; the
# one revert worth recognising is that it already has. Hardhat reports a custom
# error by name when it can and by selector when it cannot, so both are matched.
ALREADY_SEL="$(cast sig 'ReservedExtensionIdAlreadyAssigned()')"
eth_call "$FTM" 'registerReserved(uint256,address)' 1 "$OWNER" >/dev/null
if [[ -z "$CALL_ERR" ]]; then
    send_retry "$GOV_KEY" "$FTM" 'registerReserved(uint256,address)' 1 "$OWNER" >/dev/null \
        || die "registerReserved failed"
    log "extension 1 reserved for $OWNER"
elif grep -qiE "ReservedExtensionIdAlreadyAssigned|${ALREADY_SEL#0x}" <<<"$CALL_ERR"; then
    log "extension 1 is already reserved — leaving it"
else
    die "registerReserved would revert: $CALL_ERR"
fi

section "Addresses → deploy/config/local"
DEPLOYS_SRC="$DEPLOY_OUT" SIM_DUMP="$SIM_DUMP" "$SCRIPT_DIR/import-upstream-addresses.sh" local "$UPSTREAM"

echo ""
log "next: (cd tools && go run ./cmd/wire-extension) then USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local"
