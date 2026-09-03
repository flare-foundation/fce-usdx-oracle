#!/usr/bin/env bash
# oracle-status.sh — read what the oracle currently publishes, on-chain and in
# the machine, without sending anything.
#
# Three surfaces, bottom of the stack upward:
#   1. TeeOracleFeedStore  — the consumer view AND the operator view. The store
#      implements the FTSO custom-feed interface itself, so there is no separate
#      feed adapter: getCurrentFeed() is what a consumer reads, observedAt /
#      extensionId / instructionsSender is what an operator checks.
#   2. TeeMachineRegistry  — is the machine still registered at PRODUCTION
#   3. the extension's GET /state — per-source health and readiness
#
# The store is the upstream TeeOracleFeedStore from flare-smart-contracts-v2;
# its address lives under "TeeOracleFeedStore" in the deployed-addresses file.
#
# Usage:
#   ./deploy/scripts/oracle-status.sh
#   ./deploy/scripts/oracle-status.sh --store 0x…
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

GREEN='\033[0;32m'; YELLOW='\033[0;33m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
# Not named `head`: this script pipes into head(1), and a function of that name
# would shadow it.
section() { echo -e "\n${CYAN}=== $* ===${NC}"; }
warn() { echo -e "${YELLOW}$*${NC}"; }

[[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }

RPC="${CHAIN_URL:-https://coston2-api.flare.network/ext/C/rpc}"
ADDR_FILE="${ADDRESSES_FILE:-./config/coston2/deployed-addresses.json}"
[[ "$ADDR_FILE" != /* ]] && ADDR_FILE="$PROJECT_DIR/$ADDR_FILE"

lookup() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0] // empty' "$ADDR_FILE"; }

STORE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --store) STORE="$2"; shift 2 ;;
        *) echo "unknown argument: $1" >&2; exit 1 ;;
    esac
done
STORE="${STORE:-$(lookup TeeOracleFeedStore)}"
DIAMOND="$(lookup FlareTeeManager)"

if [[ -z "$STORE" || "$STORE" == "0x0000000000000000000000000000000000000000" ]]; then
    echo "no TeeOracleFeedStore address in $ADDR_FILE — pass --store, or slot the" >&2
    echo "upstream store address into the addresses file after the flare-smart-contracts-v2 deploy." >&2
    exit 1
fi

# This repo's .env sets CHAIN=coston2, which `cast` reads as its own --chain
# flag and rejects — it wants a name from its own network list. It arrives by
# two routes and both have to be cut: sourcing .env above exported it into the
# environment, and cast additionally auto-loads .env from the working
# directory. Hence env -u for the first and a neutral cwd for the second.
NEUTRAL="$(mktemp -d)"
trap 'rm -rf "$NEUTRAL"' EXIT
c() { (cd "$NEUTRAL" && env -u CHAIN cast "$@" --rpc-url "$RPC"); }

# cast prints each returned value on its own line and annotates large numbers
# like "100000000 [1e8]"; strip the annotation and flatten to one line.
clean() { sed 's/\[[^]]*\]//g' | tr -d ' ' | tr '\n' ' '; }

section "Store + feed — what a consumer reads ($STORE)"
FEED_TRIPLE="$(c call "$STORE" 'getCurrentFeed()(uint256,int8,uint64)' | clean)"
VALUE="$(echo "$FEED_TRIPLE" | cut -d' ' -f1)"
DECIMALS="$(echo "$FEED_TRIPLE" | cut -d' ' -f2)"
TS="$(echo "$FEED_TRIPLE" | cut -d' ' -f3)"
echo "  getCurrentFeed(): value=$VALUE decimals=$DECIMALS observedAt=$TS"
# Read it as value / 10^decimals and nothing else. In particular it is not a
# float: at 8 decimals a float64 round-trip is already lossy, which is why the
# whole path carries it as a uint256.
awk -v v="$VALUE" -v d="$DECIMALS" 'BEGIN { printf "  price:            $%.*f\n", d, v / (10 ^ d) }'

section "Store — the operator view ($STORE)"
OBSERVED_AT="$(c call "$STORE" 'observedAt()(uint64)' | clean | tr -d ' ')"
echo "  observedAt:       $OBSERVED_AT"
echo "  extensionId:      $(c call "$STORE" 'extensionId()(uint256)')"
echo "  instructionsSender: $(c call "$STORE" 'instructionsSender()(address)')"
echo "  feeDestination:   $(c call "$STORE" 'feeDestination()(address)')"

# The feed cannot revert, so a stale price is returned looking exactly like a
# fresh one. The timestamp is the only signal there is — always check the age.
AGE=$(( $(date -u +%s) - OBSERVED_AT ))
if [[ "$OBSERVED_AT" == "0" ]]; then
    echo -e "  age:              ${RED}no data — no feed update has ever landed${NC}"
else
    echo -e "  age:              ${AGE}s since the last accepted update"
fi

section "Machine registration"
if [[ -n "$DIAMOND" ]]; then
    EXT_ID="$(c call "$STORE" 'extensionId()(uint256)' | sed 's/\[[^]]*\]//g' | tr -d ' ')"
    c call "$DIAMOND" 'getActiveTeeMachines(uint256)(address[],string[])' "$EXT_ID" | sed 's/^/  /'
    TEE="$(c call "$DIAMOND" 'getActiveTeeMachines(uint256)(address[],string[])' "$EXT_ID" | head -1 | tr -d '[]" ' | cut -d, -f1)"
    if [[ -n "$TEE" && "$TEE" != "" ]]; then
        STATUS="$(c call "$DIAMOND" 'getTeeMachineStatus(address)(uint8)' "$TEE" | tr -d ' ')"
        # The store requires PRODUCTION (2); anything less and submitFeedUpdates
        # rejects the signer.
        [[ "$STATUS" == "2" ]] \
            && echo -e "  status:           ${GREEN}2 (PRODUCTION)${NC}" \
            || echo -e "  status:           ${RED}$STATUS — store requires 2${NC}"
    fi
else
    warn "  no FlareTeeManager in $ADDR_FILE"
fi

section "Extension health — GET /state"
# Under Docker the extension port is container-internal, so reach it from a
# throwaway container on the same network rather than from the host.
NET="$(docker inspect fce-usdx-oracle-extension-tee-1 \
        --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}' 2>/dev/null || true)"
if [[ -n "$NET" ]]; then
    docker run --rm --network "$NET" curlimages/curl:latest \
        -s -m 10 http://extension-tee:7702/state 2>/dev/null \
        | jq '.state | {lastPrice, lastObservedAt, updateCount, failureCount,
                        lastErrorClass, sourceHealth, readiness}' \
        || warn "  extension did not answer"
else
    warn "  extension-tee container is not running"
fi
echo ""
