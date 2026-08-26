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
# Each entry is "SourceFile.sol:TypeName:go_package:module", where module is the
# Go module root the binding is generated into — `tools`, `go`, or `both`.
#
# Every entry is an INTERFACE, and that is the whole design: this repo deploys
# no contracts. The TEE oracle sender and store are deployed from
# flare-smart-contracts-v2 by its own script (see docs/deployment-steps.md), and
# this repo only ever CALLS them. Binding the vendored interface gives the Go
# struct types (EndpointGroup, AdminRole, FeedUpdate, ...) and the call packing,
# which is all the tooling needs and all it should have — a local copy of the
# implementation would be a second source of truth for the wire format, and the
# payload encodings are dynamic enough that a second source of truth is a bug
# waiting to be deployed.
#
# An interface compiles to no bytecode, so abigen is run without --bin and emits
# no Deploy helper. That is deliberate: there is nothing here to deploy.
#
# TeeOracleStructs is upstream's ABI-only re-export. It is not deployed and not
# called; it exists so the payload WRAPPERS (Endpoints, Admins,
# FeedUpdateRequest) appear in an ABI at all. The contract assembles those
# internally, so they are never call arguments and abigen would otherwise never
# emit them — leaving the off-chain encoders to guess a layout they cannot
# afford to get wrong. It is generated into BOTH modules: tools/ uses the types
# to build payloads (oraclepayload), and go/ uses them to decode (runtimecfg).
# They are separate Go modules that cannot import one another, so each gets its
# own generated copy from the same .abi.
CONTRACTS=(
    "interfaces/IITeeOracleInstructionsSender.sol:IITeeOracleInstructionsSender:teeoraclesender:tools"
    "interfaces/IITeeOracleFeedStore.sol:IITeeOracleFeedStore:teeoraclestore:tools"
    "interfaces/TeeOracleStructs.sol:TeeOracleStructs:teeoraclestructs:both"
)

cd "$PROJECT_DIR"

echo "=== Step 1: Compile Solidity contracts ==="
forge build

# emit_binding MODULE SRC CONTRACT_NAME GO_PKG
# Generates the .abi + go:generate directive + autogen.go into
# <MODULE>/pkg/contracts/<GO_PKG>/.
emit_binding() {
    local MODULE="$1" SRC="$2" CONTRACT_NAME="$3" GO_PKG="$4"
    local BINDINGS_DIR="$PROJECT_DIR/$MODULE/pkg/contracts/$GO_PKG"

    mkdir -p "$BINDINGS_DIR"
    jq '.abi' "$FORGE_OUT" > "$BINDINGS_DIR/${CONTRACT_NAME}.abi"

    # An interface compiles to no bytecode. abigen only needs --bin to emit a
    # Deploy helper, so omitting it yields call-and-pack bindings and no way to
    # deploy something this repo does not own.
    local BYTECODE ABIGEN_BIN
    BYTECODE="$(jq -r '.bytecode.object' "$FORGE_OUT" | sed 's/^0x//')"
    ABIGEN_BIN=""
    if [[ -n "$BYTECODE" ]]; then
        printf '%s' "$BYTECODE" > "$BINDINGS_DIR/${CONTRACT_NAME}.bin"
        ABIGEN_BIN="--bin=${CONTRACT_NAME}.bin "
    else
        rm -f "$BINDINGS_DIR/${CONTRACT_NAME}.bin"
    fi

    # The go:generate directive lives beside the bindings so `go generate ./...`
    # keeps working; write it if the package is new.
    local GEN_FILE="$BINDINGS_DIR/${GO_PKG}.go"
    if [[ ! -f "$GEN_FILE" ]]; then
        cat > "$GEN_FILE" <<GENEOF
//go:generate go run github.com/ethereum/go-ethereum/cmd/abigen --abi=${CONTRACT_NAME}.abi ${ABIGEN_BIN}--pkg=${GO_PKG} --type=${CONTRACT_NAME} --out=autogen.go

package ${GO_PKG}
GENEOF
        echo "  created $GEN_FILE"
    fi

    ( cd "$PROJECT_DIR/$MODULE" && go generate ./pkg/contracts/$GO_PKG/ )
    echo "  → $BINDINGS_DIR/autogen.go"
}

for entry in "${CONTRACTS[@]}"; do
    IFS=":" read -r SRC CONTRACT_NAME GO_PKG MODULES <<< "$entry"

    echo ""
    echo "=== $CONTRACT_NAME ($SRC) → $GO_PKG ==="

    if ! grep -qE "(contract|interface) ${CONTRACT_NAME}\\b" "$PROJECT_DIR/contracts/${SRC}" 2>/dev/null; then
        echo "ERROR: '${CONTRACT_NAME}' not found in contracts/${SRC}."
        exit 1
    fi

    # forge flattens its output tree to the source BASENAME, so a nested source
    # such as interfaces/Foo.sol still lands in out/Foo.sol/.
    FORGE_OUT="$PROJECT_DIR/out/$(basename "$SRC")/${CONTRACT_NAME}.json"
    if [[ ! -f "$FORGE_OUT" ]]; then
        echo "ERROR: forge output not found at $FORGE_OUT"
        exit 1
    fi

    if [[ -z "$(jq -r '.bytecode.object' "$FORGE_OUT" | sed 's/^0x//')" ]]; then
        echo "  (interface: no bytecode, bindings are call-only)"
    fi

    if [[ "$MODULES" == "both" ]]; then
        emit_binding tools "$SRC" "$CONTRACT_NAME" "$GO_PKG"
        emit_binding go    "$SRC" "$CONTRACT_NAME" "$GO_PKG"
    else
        emit_binding "$MODULES" "$SRC" "$CONTRACT_NAME" "$GO_PKG"
    fi
done

echo ""
echo "=== Done ==="
