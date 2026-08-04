#!/usr/bin/env bash
# set-endpoints.sh — send a USDX_CONFIG/SET_ENDPOINTS instruction to a running
# extension, without the chain.
#
# This is the same payload setEndpoints() will carry on-chain, delivered
# straight to the extension's /action so the configuration can be exercised
# before that contract function exists. The extension applies exactly the same
# validation either way. Note there is deliberately no host allowlist: any
# https host is accepted, and whoever can send this payload controls the price
# (findings doc §15).
#
# Usage:
#   ( cd go && go run ./cmd/devserver ) &
#   ./scripts/set-endpoints.sh testdata/endpoints/coston2.json
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

CONFIG="${1:-$PROJECT_DIR/testdata/endpoints/coston2.json}"
PORT="${2:-${EXTENSION_PORT:-8080}}"

GREEN='\033[0;32m'; RED='\033[0;31m'; NC='\033[0m'

[[ -f "$CONFIG" ]] || { echo -e "${RED}no such config: $CONFIG${NC}" >&2; exit 1; }
jq empty "$CONFIG" 2>/dev/null || { echo -e "${RED}$CONFIG is not valid JSON${NC}" >&2; exit 1; }

# Wrap the config in the DataFixed envelope tee-node would deliver. The action
# type MUST be "instruction": SET_ENDPOINTS is refused over the direct channel,
# because on-chain its authorisation is the owner-only check in the sender.
BODY="$(CONFIG="$CONFIG" python3 - <<'PY'
import json, os

def b32(s):
    return "0x" + s.encode().ljust(32, b"\x00").hex()

payload = open(os.environ["CONFIG"], "rb").read()

data_fixed = {
    "instructionId": "0x" + "aa" * 32,
    "teeId": "0x" + "22" * 20,
    "timestamp": 1700000000,
    "rewardEpochId": 0,
    "opType": b32("USDX_CONFIG"),
    "opCommand": b32("SET_ENDPOINTS"),
    "cosigners": [],
    "cosignersThreshold": 0,
    "originalMessage": "0x" + payload.hex(),
    "additionalFixedMessage": "0x",
}

print(json.dumps({
    "data": {
        "id": "0x" + "aa" * 32,
        "type": "instruction",
        "submissionTag": "submit",
        "message": "0x" + json.dumps(data_fixed).encode().hex(),
    },
    "additionalVariableMessages": [],
    "timestamps": [],
    "additionalActionData": "0x",
    "signatures": [],
}))
PY
)"

echo "Sending SET_ENDPOINTS from $(basename "$CONFIG") to :$PORT"
RESP="$(curl -sS -X POST -H 'Content-Type: application/json' \
    --data-binary "$BODY" "http://127.0.0.1:$PORT/action")"

STATUS="$(echo "$RESP" | jq -r '.status // "?"')"
LOG="$(echo "$RESP" | jq -r '.log // "?"')"

if [[ "$STATUS" == "1" ]]; then
    echo -e "${GREEN}accepted${NC}"
else
    echo -e "${RED}rejected: $LOG${NC}"
    echo "  The extension log names the reason. There is no host allowlist — any"
    echo "  https host is accepted — so look at URL shape, threshold bounds, and"
    echo "  per-chain coverage instead (findings doc §15)."
fi

echo ""
echo "Readiness:"
curl -sS "http://127.0.0.1:$PORT/state" | jq '.state.readiness'
