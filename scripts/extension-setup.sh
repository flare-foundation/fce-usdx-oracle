#!/usr/bin/env bash
# extension-setup.sh — Extension-specific setup that runs BEFORE Docker Compose.
#
# This hook runs between pre-build (contract deployment) and docker-up (starting
# the TEE). Use it for any setup whose output the extension needs at startup —
# for example:
#   - Deploying auxiliary contracts (ERC20 tokens, oracles, vaults)
#   - Writing config files the extension reads at init (pairs, feeds, pools)
#   - Minting test tokens, setting allowances, seeding initial state
#
# The following variables are available (sourced from .env + config/extension.env):
#
#   INSTRUCTION_SENDER   — your deployed InstructionSender contract address
#   EXTENSION_ID         — your extension's ID on the TeeExtensionRegistry
#   CHAIN_URL            — chain RPC endpoint
#   ADDRESSES_FILE       — path to deployed-addresses.json
#   DEPLOYMENT_PRIVATE_KEY — funded deployer key
#
# Example: deploy a helper contract and write its address to a config file
#
#   cd "$PROJECT_DIR/tools"
#   HELPER_ADDR=$(go run ./cmd/deploy-helper -a "$ADDRESSES_FILE" -c "$CHAIN_URL")
#   echo "HELPER_CONTRACT=$HELPER_ADDR" > "$PROJECT_DIR/config/helper.env"
#
# Why this matters:
#   The extension container reads config at startup. Anything it needs must be
#   written to disk BEFORE `docker compose up`. If you deploy contracts after
#   the container starts, you'll need to restart it — this hook prevents that.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GREEN='\033[0;32m'; NC='\033[0m'
log() { echo -e "${GREEN}[extension-setup]${NC} $*"; }

# --- Load environment ---
if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a; source "$PROJECT_DIR/.env"; set +a
fi
if [[ -f "$PROJECT_DIR/config/extension.env" ]]; then
    source "$PROJECT_DIR/config/extension.env"
fi

log "EXTENSION_ID:       ${EXTENSION_ID:-<not set>}"
log "INSTRUCTION_SENDER: ${INSTRUCTION_SENDER:-<not set>}"
log "CHAIN_URL:          ${CHAIN_URL:-<not set>}"

# --- Deploy UsdxPriceStore and UsdxCustomFeed ---
#
# This must happen HERE, before the image is built, because the store address is
# pinned into the extension's embedded config and therefore into the code hash.
# It cannot be injected at runtime: the signed price report binds the store
# address, so an operator able to change it could obtain TEE signatures over
# reports bound to a store they control.
#
# The ordering works because pre-build.sh has already registered the extension
# and produced EXTENSION_ID, which the store's constructor needs. Registering the
# *extension* does not require a running TEE machine — only registering a
# *machine* does, and that happens later in post-build.sh.

CONFIG_JSON="$PROJECT_DIR/go/internal/setup/config/${NETWORK:-coston2}.json"

if [[ -z "${EXTENSION_ID:-}" ]]; then
    echo "[extension-setup] ERROR: EXTENSION_ID is not set — run pre-build.sh first." >&2
    exit 1
fi
if [[ ! -f "$CONFIG_JSON" ]]; then
    echo "[extension-setup] ERROR: no config at $CONFIG_JSON" >&2
    exit 1
fi

# Reuse an already-deployed store unless FORCE_REDEPLOY_STORE=1. Redeploying
# resets the stored price and the observedAt high-water mark, so it is never the
# default.
EXISTING_STORE=$(jq -r '.target.store' "$CONFIG_JSON")
if [[ "$EXISTING_STORE" != "0x0000000000000000000000000000000000000001" && "${FORCE_REDEPLOY_STORE:-0}" != "1" ]]; then
    log "Store already pinned at $EXISTING_STORE — skipping deployment."
    log "Set FORCE_REDEPLOY_STORE=1 to deploy a fresh one (this resets the stored price)."
else
    log "Deploying UsdxPriceStore and UsdxCustomFeed..."
    cd "$PROJECT_DIR/tools"
    STORE_ADDR=$(go run ./cmd/deploy-store \
        -a "$ADDRESSES_FILE" -c "$CHAIN_URL" \
        -extensionId "$EXTENSION_ID" \
        -feedCategory "${FEED_CATEGORY:-32}" \
        -feedName "${FEED_NAME:-USDX/USD}" \
        -requiredTeeStatus "${REQUIRED_TEE_STATUS:-1}" | tail -1)

    if [[ ! "$STORE_ADDR" =~ ^0x[0-9a-fA-F]{40}$ ]]; then
        echo "[extension-setup] ERROR: deploy-store returned '$STORE_ADDR', not an address" >&2
        exit 1
    fi

    log "UsdxPriceStore deployed at $STORE_ADDR"

    # Pin it into the embedded config. Changing this file changes the code hash,
    # which is the intended consequence, not a side effect.
    tmp="$(mktemp)"
    jq --arg store "$STORE_ADDR" '.target.store = $store' "$CONFIG_JSON" > "$tmp"
    mv "$tmp" "$CONFIG_JSON"
    log "Pinned store address into $CONFIG_JSON"
fi

# NOTE: neither endpoints nor credentials are configured here.
#
# The extension starts UNPROVISIONED and that is normal. Endpoints arrive later
# as an on-chain USDX_CONFIG/SET_ENDPOINTS instruction, and credentials as a
# USDX_CONFIG/SET_SECRET direct instruction carrying an M-of-N signed envelope.
# Both are memory-only, so every restart returns the extension to
# not_configured until the keeper re-submits.
#
# What IS baked into the image is the host allowlist that bounds which endpoints
# can ever be configured, and the admin signer set that authorises provisioning.
# Those are the parts that must not be substitutable — see the findings doc §14.
log "Extension will start unprovisioned; provision it with SET_ENDPOINTS and SET_SECRET."
