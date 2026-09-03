#!/usr/bin/env bash
# regen-wire-fixtures.sh — Refresh deploy/testdata/wire from the Forge vector tests.
#
# The fixtures are Solidity's own encoding of the payloads that cross the
# extension boundary. Two independent Go codecs assert against them: the
# enclave's in internal/provisioning/store and the publisher's in
# tools/pkg/oraclepayload. Keeping them independent is deliberate — tools/ is a
# cross-check on the extension, not a mirror — and these files are what stops
# that independence turning into divergence.
#
# Usage: ./deploy/scripts/regen-wire-fixtures.sh   (runs forge from contracts/)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
DEST="$PROJECT_DIR/deploy/testdata/wire"
mkdir -p "$DEST"
cd "$PROJECT_DIR/contracts"

# CHAIN from .env collides with contracts's own chain selection, so it is dropped.
OUT="$(env -u CHAIN forge test --match-path 'test/*Vector.t.sol' -vv)"

# "CONSOLE_LABEL:filename" — the label is printed immediately before its value.
LABELS=(
    "ENDPOINTS:endpoints-v7.abi.hex"
    "ADMINS:admins-v3.abi.hex"
    "FEED_UPDATE_REQUEST:feed-update-request.abi.hex"
    "ENCODED:feed-update.abi.hex"
    "PAYLOAD_PREIMAGE:feed-update-payload-preimage.abi.hex"
)

for entry in "${LABELS[@]}"; do
    IFS=":" read -r LABEL FILE <<< "$entry"
    # grep -A1 on an exact-match line, then take the 0x value and strip it.
    VALUE="$(printf '%s\n' "$OUT" | grep -A1 -x "  $LABEL" | grep -o '0x[0-9a-f]*' | head -1 | sed 's/^0x//')"
    if [[ -z "$VALUE" ]]; then
        echo "ERROR: no value printed for $LABEL" >&2
        exit 1
    fi
    printf '%s\n' "$VALUE" > "$DEST/$FILE"
    echo "  $FILE ($(( ${#VALUE} / 2 )) bytes)"
done

echo ""
echo "Wrote ${#LABELS[@]} fixtures to deploy/testdata/wire"
