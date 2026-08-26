#!/usr/bin/env bash
# oracle-observe.sh — query the price the API sources actually yield, with no
# chain, no Docker and no TEE.
#
# This is the highest-value local check: it runs the real observation path
# against live Flare, Ethereum, Songbird and Hex, then prints the price and the
# per-source health. If this is wrong, everything downstream is wrong; if this
# is right, the rest is transport.
#
# It starts a mock signing node and the dev server on high ports, provisions
# them with an endpoint configuration, asks for a price, and tears both down.
#
# Usage:
#   ./scripts/oracle-observe.sh
#   ./scripts/oracle-observe.sh testdata/endpoints/coston2.json
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
head() { echo -e "\n${CYAN}=== $* ===${NC}"; }

CONFIG="${1:-$PROJECT_DIR/testdata/endpoints/public-only.json}"
[[ "$CONFIG" != /* ]] && CONFIG="$PROJECT_DIR/$CONFIG"
[[ -f "$CONFIG" ]] || { echo "no such config: $CONFIG" >&2; exit 1; }

# High ports so this never collides with the Docker stack or a --local run.
EXT_PORT="${EXT_PORT:-8099}"
SIGN_PORT="${SIGN_PORT:-9099}"

LOG_DIR="$(mktemp -d)"
MOCK_PID=""; DEV_PID=""
cleanup() {
    [[ -n "$DEV_PID"  ]] && kill "$DEV_PID"  2>/dev/null || true
    [[ -n "$MOCK_PID" ]] && kill "$MOCK_PID" 2>/dev/null || true
    rm -rf "$LOG_DIR"
}
trap cleanup EXIT

head "Building mocknode and devserver"
cd "$PROJECT_DIR/go"
# Built rather than `go run`: go run execs the compiled binary as a CHILD, so
# killing the go run process on exit would leave that child holding the port and
# the next invocation would fail to bind.
go build -o "$LOG_DIR/mocknode" ./cmd/mocknode
go build -o "$LOG_DIR/devserver" ./cmd/devserver

head "Starting mocknode (:$SIGN_PORT) and devserver (:$EXT_PORT)"
SIGN_PORT="$SIGN_PORT" "$LOG_DIR/mocknode" >"$LOG_DIR/mocknode.log" 2>&1 &
MOCK_PID=$!
EXTENSION_PORT="$EXT_PORT" SIGN_PORT="$SIGN_PORT" "$LOG_DIR/devserver" >"$LOG_DIR/devserver.log" 2>&1 &
DEV_PID=$!

# `go run` compiles first, so the port is not up immediately.
for _ in $(seq 1 60); do
    curl -sf -m 2 "http://127.0.0.1:$EXT_PORT/state" >/dev/null 2>&1 && break
    sleep 1
done
curl -sf -m 2 "http://127.0.0.1:$EXT_PORT/state" >/dev/null 2>&1 || {
    echo -e "${RED}devserver never came up${NC}" >&2
    cat "$LOG_DIR/devserver.log" >&2
    exit 1
}

head "Provisioning endpoints from $(basename "$CONFIG")"
"$SCRIPT_DIR/set-endpoints.sh" "$CONFIG" "$EXT_PORT"

head "Requesting a feed update"
# originalMessage is abi.encode(FeedUpdateRequest{feedId}): the 21-byte feed id
# in a single 32-byte word (its only field is static, so the tuple encodes
# inline). This replaced the old GET_FEED_REQUEST_V1 = 0x01 byte when one sender
# started serving several feeds. 0x20555344582f555344 = category 0x20 + "USDX/USD".
REQ="$(python3 -c "
import json
b32 = lambda s: '0x' + s.encode().ljust(32, b'\x00').hex()
df = {
    'instructionId': '0x' + 'c1' * 32, 'teeId': '0x' + '22' * 20,
    'timestamp': 1785945000, 'rewardEpochId': 0,
    'opType': b32('TEE_ORACLE'), 'opCommand': b32('GET_FEED'),
    'cosigners': [], 'cosignersThreshold': 0,
    'originalMessage': '0x20555344582f55534400000000000000000000000000000000000000000000', 'additionalFixedMessage': '0x',
}
print(json.dumps({
    'data': {'id': '0x' + 'c1' * 32, 'type': 'instruction', 'submissionTag': 'submit',
             'message': '0x' + json.dumps(df).encode().hex()},
    'additionalVariableMessages': [], 'timestamps': [],
    'additionalActionData': '0x', 'signatures': [],
}))")"

# Observation happens on a background timer, never on the request path — a
# GET_FEED costs one signature and nothing more, which is what keeps it inside
# tee-node's 2s action budget. So the first request after provisioning is served
# from the failed boot-time observation and returns not_configured; the price
# only appears once the poller ticks again. Wait that out rather than reporting
# the startup transient as a failure.
DEADLINE=$(( $(date +%s) + 180 ))
while :; do
    RESP="$(curl -sS -X POST -H 'Content-Type: application/json' \
        --data-binary "$REQ" "http://127.0.0.1:$EXT_PORT/action")"
    STATUS="$(echo "$RESP" | jq -r '.status // 0')"
    [[ "$STATUS" == "1" ]] && break
    (( $(date +%s) >= DEADLINE )) && break
    echo "  waiting for the observation loop ($(echo "$RESP" | jq -r '.log // ""'))"
    sleep 10
done
echo "$RESP" | jq '{status, log}'

head "Observation"
STATE="$(curl -sS "http://127.0.0.1:$EXT_PORT/state")"
echo "$STATE" | jq '.state | {lastPrice, lastDecimals, lastObservedAt,
                              updateCount, failureCount, lastErrorClass,
                              sourceHealth, readiness}'

PRICE="$(echo "$STATE" | jq -r '.state.lastPrice // ""')"
DEC="$(echo "$STATE" | jq -r '.state.lastDecimals // 8')"
if [[ -n "$PRICE" ]]; then
    python3 -c "print(f'\n  price: \${int('$PRICE')/10**int('$DEC'):.8f}')"
    echo -e "  ${GREEN}observation succeeded${NC}"
else
    echo -e "\n  ${RED}no price — read .log above and sourceHealth for which source broke${NC}"
fi

# ready:false with only hex_api_key missing is expected until Hex issues the
# key. The feed still publishes at par, because a bonds-only price that already
# reaches par is provably what the full computation would give. It will not
# publish anything below par.
#
# Any OTHER missing ref is an RPC credential, and those are not optional in the
# same way: a provider that cannot authenticate drops out of its chain's quorum.
# Install them with ./scripts/upload-rpc-keys.sh, and hex_api_key with
# ./scripts/upload-hex-key.sh — two uploads, two admin sets.
echo ""
