#!/usr/bin/env bash
# set-admins.sh — send a TEE_ORACLE/SET_ADMINS instruction to a running
# extension, without the chain.
#
# The payload is the ABI-encoded Admins{version, feedId, roles} that setAdmins()
# dispatches on-chain, produced by the same encoder the on-chain path uses
# (tools/cmd/send-admins -encode), and wrapped in the DataFixed envelope
# tee-node would deliver. The extension applies exactly the same validation
# either way. Observation needs only the endpoints, but the machine will not
# sign a feed update until an admin set is installed: an update that cannot
# commit to published admin sets would be rejected by the store.
#
# The default set is the dev-only one built from well-known keys. Never publish
# it to a live feed (docs/usdx-oracle-production-setup.md).
#
# The extension's store accepts only a version newer than the last it applied,
# so re-provisioning a running dev server needs a higher ADMINS_VERSION.
#
# Usage:
#   go run ./cmd/mocknode &  go run ./cmd/devserver &
#   ./deploy/scripts/set-admins.sh deploy/config/admins/dev-wellknown-keys.json
#   ADMINS_VERSION=2 ./deploy/scripts/set-admins.sh <config> [port]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
source "$SCRIPT_DIR/lib/env.sh"

CONFIG="${1:-$PROJECT_DIR/deploy/config/admins/dev-wellknown-keys.json}"
PORT="${2:-${EXTENSION_PORT:-7702}}"
VERSION="${ADMINS_VERSION:-1}"
[[ "$CONFIG" != /* ]] && CONFIG="$PROJECT_DIR/$CONFIG"

GREEN='\033[0;32m'; RED='\033[0;31m'; NC='\033[0m'

[[ -f "$CONFIG" ]] || { echo -e "${RED}no such config: $CONFIG${NC}" >&2; exit 1; }
jq empty "$CONFIG" 2>/dev/null || { echo -e "${RED}$CONFIG is not valid JSON${NC}" >&2; exit 1; }

# The encoder validates the admin sets the way the publisher would — role
# floors, distinct addresses — so a bad file fails here with a named reason
# rather than as an opaque rejection from the extension.
PAYLOAD="$(cd "$PROJECT_DIR/tools" && go run ./cmd/send-admins -encode -version "$VERSION" -config "$CONFIG")"
[[ "$PAYLOAD" =~ ^0x[0-9a-f]+$ ]] || { echo -e "${RED}encoder produced no payload${NC}" >&2; exit 1; }

# The action type MUST be "instruction": SET_ADMINS is refused over the direct
# channel, because on-chain its authorisation is the onlyGovernance check in the
# sender.
BODY="$(PAYLOAD="$PAYLOAD" python3 - <<'PY'
import json, os

def b32(s):
    return "0x" + s.encode().ljust(32, b"\x00").hex()

data_fixed = {
    "instructionId": "0x" + "ad" * 32,
    "teeId": "0x" + "22" * 20,
    "timestamp": 1700000000,
    "rewardEpochId": 0,
    "opType": b32("TEE_ORACLE"),
    "opCommand": b32("SET_ADMINS"),
    "cosigners": [],
    "cosignersThreshold": 0,
    "originalMessage": os.environ["PAYLOAD"],
    "additionalFixedMessage": "0x",
}

print(json.dumps({
    "data": {
        "id": "0x" + "ad" * 32,
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

echo "Sending SET_ADMINS v$VERSION from $(basename "$CONFIG") to :$PORT"
RESP="$(curl -sS -X POST -H 'Content-Type: application/json' \
    --data-binary "$BODY" "http://127.0.0.1:$PORT/action")"

STATUS="$(echo "$RESP" | jq -r '.status // "?"')"
LOG="$(echo "$RESP" | jq -r '.log // "?"')"

echo ""
echo "Readiness:"
# Read through the node, as every consumer does: mocknode folds the dev
# server's GET /state into its /info the way tee-node does.
INFO_URL="${INFO_URL:-http://127.0.0.1:${SIGN_PORT:-7701}}"
if STATE="$(read_machine_state "$INFO_URL")"; then
    jq '.readiness' <<<"$STATE"
else
    echo "  (no state readable through the node at $INFO_URL — is mocknode running with EXTENSION_PORT=$PORT?)"
fi

if [[ "$STATUS" == "1" ]]; then
    echo -e "${GREEN}accepted${NC}"
    exit 0
fi
echo -e "${RED}rejected: $LOG${NC}" >&2
echo "  The extension log names the reason: both roles must be present with" >&2
echo "  distinct addresses at or above their floors, and the version must be" >&2
echo "  newer than the last one applied (ADMINS_VERSION)." >&2
exit 1
