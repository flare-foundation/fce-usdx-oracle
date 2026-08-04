#!/usr/bin/env bash
# generate-bindings.sh — Compile Solidity contracts and generate Go bindings.
#
# Prerequisites: forge (Foundry), jq
#
# Usage: ./scripts/generate-bindings.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Contracts to bind ---
# Each entry is "SourceFile.sol:ContractName:go_package". The deployment tooling
# is Go regardless of the extension language, so every contract it must deploy
# or call needs bindings here.
CONTRACTS=(
    "InstructionSender.sol:UsdxOracleInstructionSender:usdxoracle"
    "UsdxPriceStore.sol:UsdxPriceStore:usdxstore"
    "UsdxCustomFeed.sol:UsdxCustomFeed:usdxfeed"
)

cd "$PROJECT_DIR"

echo "=== Step 1: Compile Solidity contracts ==="
forge build

for entry in "${CONTRACTS[@]}"; do
    IFS=":" read -r SRC CONTRACT_NAME GO_PKG <<< "$entry"
    BINDINGS_DIR="$PROJECT_DIR/tools/pkg/contracts/$GO_PKG"

    echo ""
    echo "=== $CONTRACT_NAME ($SRC) → $GO_PKG ==="

    if ! grep -q "contract ${CONTRACT_NAME}" "$PROJECT_DIR/contracts/${SRC}" 2>/dev/null; then
        echo "ERROR: contract '${CONTRACT_NAME}' not found in contracts/${SRC}."
        exit 1
    fi

    FORGE_OUT="$PROJECT_DIR/out/${SRC}/${CONTRACT_NAME}.json"
    if [[ ! -f "$FORGE_OUT" ]]; then
        echo "ERROR: forge output not found at $FORGE_OUT"
        exit 1
    fi

    mkdir -p "$BINDINGS_DIR"
    jq '.abi' "$FORGE_OUT" > "$BINDINGS_DIR/${CONTRACT_NAME}.abi"
    jq -r '.bytecode.object' "$FORGE_OUT" | sed 's/^0x//' > "$BINDINGS_DIR/${CONTRACT_NAME}.bin"

    # The go:generate directive lives beside the bindings so `go generate ./...`
    # keeps working; write it if the package is new.
    GEN_FILE="$BINDINGS_DIR/${GO_PKG}.go"
    if [[ ! -f "$GEN_FILE" ]]; then
        cat > "$GEN_FILE" <<GENEOF
//go:generate go run github.com/ethereum/go-ethereum/cmd/abigen --abi=${CONTRACT_NAME}.abi --bin=${CONTRACT_NAME}.bin --pkg=${GO_PKG} --type=${CONTRACT_NAME} --out=autogen.go

package ${GO_PKG}
GENEOF
        echo "  created $GEN_FILE"
    fi

    ( cd "$PROJECT_DIR/tools" && go generate ./pkg/contracts/$GO_PKG/ )
    echo "  → $BINDINGS_DIR/autogen.go"
done

echo ""
echo "=== Done ==="
