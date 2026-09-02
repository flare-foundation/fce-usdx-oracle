#!/usr/bin/env bash
# vendor-interfaces.sh — Refresh the upstream TEE oracle interfaces this repo binds against.
#
# The oracle contracts are deployed from flare-smart-contracts-v2; this repo only
# ever CALLS them. Vendoring the interfaces (rather than the implementations)
# gives abigen the struct layouts without creating a second source of truth for
# the wire format.
#
# Usage: ./scripts/vendor-interfaces.sh [path-to-flare-smart-contracts-v2]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
UPSTREAM="${1:-$PROJECT_DIR/../../../fsp/flare-smart-contracts-v2}"

if [[ ! -d "$UPSTREAM/contracts" ]]; then
    echo "ERROR: no contracts/ under $UPSTREAM" >&2
    echo "Usage: $0 [path-to-flare-smart-contracts-v2]" >&2
    exit 1
fi

DEST="$PROJECT_DIR/contracts/interfaces"
REV="$(cd "$UPSTREAM" && git rev-parse --short HEAD)"

# "source-path-under-contracts/"
SOURCES=(
    "userInterfaces/ISignature.sol"
    "userInterfaces/tee/ITeeOracleInstructionsSender.sol"
    "userInterfaces/tee/ITeeOracleFeedStore.sol"
    "tee/extensions/oracle/interface/IITeeOracleInstructionsSender.sol"
    "tee/extensions/oracle/interface/IITeeOracleFeedStore.sol"
    "tee/structs/TeeOracleStructs.sol"
)

for src in "${SOURCES[@]}"; do
    name="$(basename "$src")"
    cp "$UPSTREAM/contracts/$src" "$DEST/$name"
    chmod u+w "$DEST/$name"

    # Every vendored file lands in one flat directory, so all cross-imports
    # become siblings. Rewrite any relative path down to "./<basename>".
    python3 - "$DEST/$name" "$REV" <<'PYEOF'
import re, sys

path, rev = sys.argv[1], sys.argv[2]
with open(path) as f:
    text = f.read()

# Matches "./Foo.sol", "../Foo.sol" and "../../a/b/Foo.sol" alike.
text = re.sub(r'from "(?:\.{1,2}/)+(?:[^"/]+/)*([A-Za-z0-9_]+\.sol)"', r'from "./\1"', text)

banner = (
    "// Vendored from flare-smart-contracts-v2 @ %s by scripts/vendor-interfaces.sh.\n"
    "// Only the import paths are rewritten, to point at the siblings in this\n"
    "// directory. Do NOT edit the declarations: abigen derives the Go structs from\n"
    "// them, and the enclave's codec in go/internal/provisioning/store plus the publisher's\n"
    "// in tools/pkg/oraclepayload are both pinned to the same layout.\n" % rev
)
text = re.sub(r'^// Vendored from .*?\n(?://.*\n)*', '', text, count=1, flags=re.M)
text = text.replace("pragma solidity", banner + "\npragma solidity", 1)

with open(path, "w") as f:
    f.write(text)
PYEOF
    echo "  vendored $name"
done

echo ""
echo "Vendored ${#SOURCES[@]} interfaces from $UPSTREAM @ $REV"
echo "Next: ./scripts/generate-bindings.sh, then regenerate testdata/wire via"
echo "      forge test --match-path 'test/*Vector.t.sol' -vv"
