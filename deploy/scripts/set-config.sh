#!/usr/bin/env bash
# set-config.sh — send a TEE_ORACLE/SET_ENDPOINTS or SET_ADMINS instruction to a
# running extension, without the chain.
#
# The payload is the ABI-encoded configuration that setEndpoints() or setAdmins()
# dispatches on-chain — Endpoints{version, feedId, groups} or Admins{version,
# feedId, roles} — produced by the same encoder the on-chain path uses
# (tools/cmd/send-endpoints or send-admins, -encode) and wrapped in the DataFixed
# envelope tee-node would deliver. The extension applies exactly the same
# validation either way. Note there is deliberately no host allowlist: any https
# host is accepted, and whoever can send an endpoints payload controls the price
# (findings doc §15). Observation needs only the endpoints, but the machine will
# not sign a feed update until an admin set is installed: an update that cannot
# commit to published admin sets would be rejected by the store. The default
# admin set is the dev-only one built from well-known keys; never publish it to
# a live feed (docs/usdx-oracle-production-setup.md).
#
# The extension's store accepts only a version newer than the last it applied,
# so re-provisioning a running dev server needs a higher CONFIG_VERSION.
#
# Usage:
#   go run ./cmd/dev/mocknode &  go run ./cmd/dev/devserver &
#   ./deploy/scripts/set-config.sh endpoints deploy/config/endpoints/public-only.json
#   ./deploy/scripts/set-config.sh admins    deploy/config/admins/dev-wellknown-keys.json
#   CONFIG_VERSION=2 ./deploy/scripts/set-config.sh endpoints|admins [config] [port]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"

GREEN='\033[0;32m'; RED='\033[0;31m'; NC='\033[0m'
die() { echo -e "${RED}$*${NC}" >&2; exit 1; }

KIND="${1:-}"
case "$KIND" in
    endpoints)
        TOOL=send-endpoints; COMMAND=SET_ENDPOINTS; ID_BYTE=aa
        DEFAULT_CONFIG="deploy/config/endpoints/public-only.json"
        REJECT_HINT="  The extension log names the reason. There is no host allowlist — any
  https host is accepted — so look at URL shape, threshold bounds, per-chain
  coverage (findings doc §15), and the version: a running extension takes
  only a version newer than the last it applied (CONFIG_VERSION)." ;;
    admins)
        TOOL=send-admins; COMMAND=SET_ADMINS; ID_BYTE=ad
        DEFAULT_CONFIG="deploy/config/admins/dev-wellknown-keys.json"
        REJECT_HINT="  The extension log names the reason: both roles must be present with
  distinct addresses at or above their floors, and the version must be
  newer than the last one applied (CONFIG_VERSION)." ;;
    *) die "usage: $0 endpoints|admins [config] [port]" ;;
esac
CONFIG="${2:-$PROJECT_DIR/$DEFAULT_CONFIG}"
PORT="${3:-${EXTENSION_PORT:-7702}}"
VERSION="${CONFIG_VERSION:-1}"
[[ "$CONFIG" != /* ]] && CONFIG="$PROJECT_DIR/$CONFIG"

[[ -f "$CONFIG" ]] || die "no such config: $CONFIG"
jq empty "$CONFIG" 2>/dev/null || die "$CONFIG is not valid JSON"

# The encoder validates the configuration the way the publisher would — URL
# shape and thresholds, or role floors and distinct addresses — so a malformed
# file fails here with a named reason rather than as an opaque rejection from
# the extension.
PAYLOAD="$(cd "$PROJECT_DIR/tools" && go run "./cmd/$TOOL" -encode -version "$VERSION" -config "$CONFIG")"
[[ "$PAYLOAD" =~ ^0x[0-9a-f]+$ ]] || die "encoder produced no payload"

# The action type MUST be "instruction": both commands are refused over the
# direct channel, because on-chain their authorisation is the onlyGovernance
# check in the sender.
BODY="$(PAYLOAD="$PAYLOAD" COMMAND="$COMMAND" ID_BYTE="$ID_BYTE" python3 - <<'PY'
import json, os

def b32(s):
    return "0x" + s.encode().ljust(32, b"\x00").hex()

action_id = "0x" + os.environ["ID_BYTE"] * 32
data_fixed = {
    "instructionId": action_id,
    "teeId": "0x" + "22" * 20,
    "timestamp": 1700000000,
    "rewardEpochId": 0,
    "opType": b32("TEE_ORACLE"),
    "opCommand": b32(os.environ["COMMAND"]),
    "cosigners": [],
    "cosignersThreshold": 0,
    "originalMessage": os.environ["PAYLOAD"],
    "additionalFixedMessage": "0x",
}

print(json.dumps({
    "data": {
        "id": action_id,
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

echo "Sending $COMMAND v$VERSION from $(basename "$CONFIG") to :$PORT"
RESP="$(curl -sS -X POST -H 'Content-Type: application/json' \
    --data-binary "$BODY" "http://127.0.0.1:$PORT/action")"

STATUS="$(echo "$RESP" | jq -r '.status // "?"')"
LOG="$(echo "$RESP" | jq -r '.log // "?"')"

echo ""
echo "Readiness:"
# The machine's account of itself is its signed readiness report, asked of the
# dev server directly: the harness has no proxy.
if REPORT="$(read_extension_readiness "http://127.0.0.1:$PORT")"; then
    jq '.readiness' <<<"$REPORT"
else
    echo "  (the dev server at :$PORT did not answer the readiness request)"
fi

if [[ "$STATUS" == "1" ]]; then
    echo -e "${GREEN}accepted${NC}"
    exit 0
fi
echo -e "${RED}rejected: $LOG${NC}" >&2
echo "$REJECT_HINT" >&2
exit 1
