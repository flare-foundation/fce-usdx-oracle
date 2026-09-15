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
# them with an endpoint configuration and the dev-only admin set (observation
# needs the endpoints; signing a feed update needs the admin set too), asks for a
# price, and tears both down.
#
# Usage:
#   ./deploy/scripts/oracle-observe.sh
#   ./deploy/scripts/oracle-observe.sh deploy/config/endpoints/coston2.json
#   ADMINS=deploy/config/admins/<set>.json ./deploy/scripts/oracle-observe.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
section() { echo -e "\n${CYAN}=== $* ===${NC}"; }

CONFIG="${1:-$PROJECT_DIR/deploy/config/endpoints/public-only.json}"
[[ "$CONFIG" != /* ]] && CONFIG="$PROJECT_DIR/$CONFIG"
[[ -f "$CONFIG" ]] || { echo "no such config: $CONFIG" >&2; exit 1; }
ADMINS="${ADMINS:-$PROJECT_DIR/deploy/config/admins/dev-wellknown-keys.json}"
[[ "$ADMINS" != /* ]] && ADMINS="$PROJECT_DIR/$ADMINS"
[[ -f "$ADMINS" ]] || { echo "no such admin set: $ADMINS" >&2; exit 1; }

# High ports so this never collides with the Docker stack or a --local run.
# Exported: mocknode folds the dev server's state into its /info from
# EXTENSION_PORT, and the set-* helpers read that /info from SIGN_PORT.
EXT_PORT="${EXT_PORT:-8099}"
SIGN_PORT="${SIGN_PORT:-9099}"
export SIGN_PORT
source "$SCRIPT_DIR/lib/env.sh"
NODE_URL="http://127.0.0.1:$SIGN_PORT"

# A stale pair from an aborted run would hold the ports, and a fresh devserver
# could not bind while the readiness probe below happily answered from the old
# one. Refuse to start rather than test the wrong process.
for port in "$EXT_PORT" "$SIGN_PORT"; do
    if holder="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null)" && [[ -n "$holder" ]]; then
        echo -e "${RED}port $port is already in use by pid $holder ($(ps -o comm= -p "$holder"))${NC}" >&2
        echo "  kill it, or run with EXT_PORT/SIGN_PORT set to free ports" >&2
        exit 1
    fi
done

LOG_DIR="$(mktemp -d)"
MOCK_PID=""; DEV_PID=""
cleanup() {
    for pid in $DEV_PID $MOCK_PID; do
        kill "$pid" 2>/dev/null || continue
        for _ in 1 2 3 4 5 6; do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
        kill -9 "$pid" 2>/dev/null || true
        # Reap it here, so the shell does not report the termination as a job notice.
        wait "$pid" 2>/dev/null || true
    done
    rm -rf "$LOG_DIR"
}
trap cleanup EXIT

section "Building mocknode and devserver"
cd "$PROJECT_DIR"
# Built rather than `go run`: go run execs the compiled binary as a CHILD, so
# killing the go run process on exit would leave that child holding the port and
# the next invocation would fail to bind.
go build -o "$LOG_DIR/mocknode" ./cmd/mocknode
go build -o "$LOG_DIR/devserver" ./cmd/devserver

section "Starting mocknode (:$SIGN_PORT) and devserver (:$EXT_PORT)"
SIGN_PORT="$SIGN_PORT" EXTENSION_PORT="$EXT_PORT" "$LOG_DIR/mocknode" >"$LOG_DIR/mocknode.log" 2>&1 &
MOCK_PID=$!
# The extension refuses to start without CHAIN_ID and EXTENSION_ID, the same
# way the production entrypoint does. The harness has no chain, so the
# simulation's values stand in unless the environment says otherwise.
CHAIN_ID="${CHAIN_ID:-31337}" EXTENSION_ID="${EXTENSION_ID:-1}" \
EXTENSION_PORT="$EXT_PORT" SIGN_PORT="$SIGN_PORT" "$LOG_DIR/devserver" >"$LOG_DIR/devserver.log" 2>&1 &
DEV_PID=$!
# Disowned so the shell does not announce their termination when cleanup kills
# them; the pids are tracked by hand above.
disown "$MOCK_PID" "$DEV_PID" 2>/dev/null || true

# Up means the dev server's state is readable through the node's /info, the
# only way the state is read anywhere.
for _ in $(seq 1 60); do
    read_machine_state "$NODE_URL" >/dev/null 2>&1 && break
    sleep 1
done
read_machine_state "$NODE_URL" >/dev/null 2>&1 || {
    echo -e "${RED}devserver never came up (its state is not readable through mocknode at $NODE_URL)${NC}" >&2
    cat "$LOG_DIR/devserver.log" >&2
    exit 1
}

section "Provisioning endpoints from $(basename "$CONFIG")"
"$SCRIPT_DIR/set-config.sh" endpoints "$CONFIG" "$EXT_PORT"

# Without an admin set the machine observes but will not sign a feed update: it
# would commit to no admin set, and the store would reject it on-chain anyway.
section "Publishing the dev admin set from $(basename "$ADMINS")"
"$SCRIPT_DIR/set-config.sh" admins "$ADMINS" "$EXT_PORT"

section "Requesting a feed update"
# originalMessage is abi.encode(FeedUpdateRequest{feedId}): the 21-byte feed id
# left-aligned in a single 32-byte word (its only field is static, so the tuple
# encodes inline). Derived from the id rather than written out, because the
# decoder checks the length first and names a short word only "unexpected
# payload". 0x20555344582f555344 = category 0x20 + "USDX/USD".
FEED_ID="20555344582f555344000000000000000000000000"
# The timestamp stands in for the block that carried the instruction: it is what
# the extension signs as observedAt, and the reading's age is checked against it.
REQ="$(FEED_ID="$FEED_ID" python3 -c "
import json, os, time
b32 = lambda s: '0x' + s.encode().ljust(32, b'\x00').hex()
word = '0x' + bytes.fromhex(os.environ['FEED_ID']).ljust(32, b'\x00').hex()
df = {
    'instructionId': '0x' + 'c1' * 32, 'teeId': '0x' + '22' * 20,
    'timestamp': int(time.time()), 'rewardEpochId': 0,
    'opType': b32('TEE_ORACLE'), 'opCommand': b32('GET_FEED'),
    'cosigners': [], 'cosignersThreshold': 0,
    'originalMessage': word, 'additionalFixedMessage': '0x',
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
# Only the not-yet-observed answers are worth waiting out. Anything else is a
# hard failure, and waiting three minutes on it would only bury the reason.
DEADLINE=$(( $(date +%s) + 180 ))
while :; do
    RESP="$(curl -sS -X POST -H 'Content-Type: application/json' \
        --data-binary "$REQ" "http://127.0.0.1:$EXT_PORT/action")"
    STATUS="$(echo "$RESP" | jq -r '.status // 0')"
    LOG="$(echo "$RESP" | jq -r '.log // ""')"
    [[ "$STATUS" == "1" ]] && break
    # A failed result's log is "error: <reason>", with the reason drawn from the
    # extension's closed vocabulary (internal/failure).
    case "${LOG#error: }" in
        "no recent observation"|"no observation available yet"|"extension is not provisioned") ;;
        *)
            echo -e "  ${RED}GET_FEED failed: ${LOG:-status $STATUS}${NC}" >&2
            echo "  --- devserver log:" >&2; tail -n 20 "$LOG_DIR/devserver.log" >&2
            exit 1 ;;
    esac
    (( $(date +%s) >= DEADLINE )) && { echo -e "  ${RED}no observation within 180s: $LOG${NC}" >&2; tail -n 20 "$LOG_DIR/devserver.log" >&2; exit 1; }
    echo "  waiting for the observation loop ($LOG)"
    sleep 10
done
echo "$RESP" | jq '{status, log}'

section "Observation"
STATE="$(read_machine_state "$NODE_URL")"
echo "$STATE" | jq '{lastPrice, lastDecimals, lastObservedAt,
                     updateCount, failureCount, lastErrorClass,
                     sourceHealth, readiness}'

PRICE="$(echo "$STATE" | jq -r '.lastPrice // ""')"
DEC="$(echo "$STATE" | jq -r '.lastDecimals // 8')"
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
# Install them with ./deploy/scripts/upload-rpc-keys.sh, and hex_api_key with
# ./deploy/scripts/upload-hex-key.sh — two uploads, two admin sets.
echo ""
