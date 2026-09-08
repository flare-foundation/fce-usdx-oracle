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

source "$SCRIPT_DIR/lib/env.sh"; load_env

RPC="${CHAIN_URL:-https://coston2-api.flare.network/ext/C/rpc}"
ADDR_FILE="${ADDRESSES_FILE:-./deploy/config/coston2/deployed-addresses.json}"
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

# Reads go through a hand-built eth_call rather than `cast call`: Hardhat rejects
# cast's call object, which carries both `input` and `data`. The signature keeps
# cast's `name(args)(returns)` shape, and the values come back one per line as
# `cast call` prints them. C_VALUE attaches value, which the store's consumer read
# needs: FTSO custom feeds charge a read fee, and eth_call spends nothing.
FROM="$(cast wallet address --private-key "${LOCAL_GOVERNANCE_KEY:-${DEPLOYMENT_PRIVATE_KEY:?set DEPLOYMENT_PRIVATE_KEY or LOCAL_GOVERNANCE_KEY}}")"
c() {  # c call <to> 'name(args)(returns)' [args...]
    [[ "$1" == "call" ]] || { echo "c: only 'call' is supported" >&2; return 1; }
    local to="$2" sig="$3"; shift 3
    local inputs="${sig%)(*})" returns="(${sig##*)(}"
    local data raw
    data="$(cast calldata "$inputs" "$@")"
    raw="$(cast rpc eth_call "{\"from\":\"$FROM\",\"to\":\"$to\",\"data\":\"$data\",\"value\":\"${C_VALUE:-0x0}\"}" latest --rpc-url "$RPC" 2>&1 | tr -d '"')"
    [[ "$raw" == 0x* ]] || { echo "$raw" >&2; return 1; }
    cast abi-decode "f()$returns" "$raw"
}
# cast prints each returned value on its own line and annotates large numbers
# like "100000000 [1e8]"; strip the annotation and flatten to one line.
clean() { sed 's/\[[^]]*\]//g' | tr -d ' ' | tr '\n' ' '; }

section "Store + feed — what a consumer reads ($STORE)"
FEED_TRIPLE="$(C_VALUE=0xde0b6b3a7640000 c call "$STORE" 'getCurrentFeed()(uint256,int8,uint64)' | clean)"
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
# The extension listens on loopback only, so its state is read from inside its
# own network namespace: directly for a host-process stack (the start records
# the URL in deploy/config/stack.env), and by joining the container's namespace
# for the compose stack. The proxy's /info is the fallback for a tee-node that
# folds the extension state into the attested machine info; the current ones
# publish that field empty.
STATE_FILTER='.state | {lastPrice, lastObservedAt, updateCount, failureCount, lastErrorClass, sourceHealth, readiness}'
EXT_CONTAINER="deploy-extension-tee-1"
if [[ -n "${EXTENSION_STATE_URL:-}" ]]; then
    curl -sf -m 10 "$EXTENSION_STATE_URL" | jq "$STATE_FILTER" \
        || warn "  the recorded host extension at $EXTENSION_STATE_URL did not answer"
elif docker inspect "$EXT_CONTAINER" >/dev/null 2>&1; then
    # alpine:3.21 is the base the proxy image is built on, so it is already
    # local, and busybox wget is enough for one GET.
    docker run --rm --network "container:$EXT_CONTAINER" alpine:3.21 \
        wget -qO- -T 10 http://127.0.0.1:7702/state 2>/dev/null | jq "$STATE_FILTER" \
        || warn "  the extension in $EXT_CONTAINER did not answer"
elif [[ -n "${EXT_PROXY_URL:-}" ]]; then
    STATE_HEX="$(curl -sf -m 10 "${EXT_PROXY_URL%/}/info" | jq -r '.teeInfo.state.state // empty' 2>/dev/null || true)"
    if [[ -n "$STATE_HEX" && "$STATE_HEX" != "0x" ]]; then
        printf '%s' "${STATE_HEX#0x}" | xxd -r -p | jq "$STATE_FILTER" \
            || warn "  the state the proxy at $EXT_PROXY_URL reports is not the extension's JSON"
    else
        warn "  the proxy at $EXT_PROXY_URL publishes no machine state (this tee-node does not fold GET /state into /info)"
    fi
else
    warn "  no extension to read: neither a recorded host stack, the compose container, nor a proxy URL"
fi
echo ""
