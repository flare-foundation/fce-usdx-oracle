#!/usr/bin/env bash
# generate-bindings.sh — Generate the Go bindings for the upstream TEE oracle interfaces.
#
# The oracle contracts live in flare-smart-contracts-v2 and are deployed from
# there; this repository only calls them. contracts/ compiles the interfaces
# straight out of a sibling checkout of that repository (the path is the
# remapping in contracts/foundry.toml) and this script turns the resulting ABIs
# into abigen bindings:
#
#   tools/pkg/contracts/teeoraclesender   IITeeOracleInstructionsSender  keeper, provisioning CLIs
#   tools/pkg/contracts/teeoraclestore    IITeeOracleFeedStore           keeper
#   pkg/contracts/teeoraclestructs        TeeOracleStructs               payload codecs, both modules
#
# Every entry is an interface, so abigen runs without --bin and emits no Deploy
# helper: there is nothing here to deploy. TeeOracleStructs is upstream's
# ABI-only re-export of the payload wrappers (Endpoints, Admins,
# FeedUpdateRequest). The contracts assemble those internally, so without it the
# wrappers would appear in no ABI and the off-chain codecs would have to guess a
# layout they cannot afford to get wrong. It is generated once, into the
# extension's module, which the tooling imports.
#
# Prerequisites: forge (Foundry), jq, and the flare-smart-contracts-v2 checkout
# contracts/foundry.toml points at.
#
# Usage: ./deploy/scripts/generate-bindings.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
CONTRACTS_DIR="$PROJECT_DIR/contracts"

# "SourceFile.sol:TypeName:go_package:module" — module is the Go module the
# binding is written into, as a path relative to the repository root: `tools`,
# `.`, or `both`. The source files are the ones contracts/src/Bindings.sol imports.
CONTRACTS=(
    "IITeeOracleInstructionsSender.sol:IITeeOracleInstructionsSender:teeoraclesender:tools"
    "IITeeOracleFeedStore.sol:IITeeOracleFeedStore:teeoraclestore:tools"
    "TeeOracleStructs.sol:TeeOracleStructs:teeoraclestructs:."
)

echo "=== Step 1: Compile the upstream interfaces ==="
( cd "$CONTRACTS_DIR" && forge build )

# emit_binding MODULE SRC CONTRACT_NAME GO_PKG
# Writes the .abi, the go:generate directive and autogen.go into
# <MODULE>/pkg/contracts/<GO_PKG>/.
emit_binding() {
    local MODULE="$1" SRC="$2" CONTRACT_NAME="$3" GO_PKG="$4"
    local BINDINGS_DIR="$PROJECT_DIR/$MODULE/pkg/contracts/$GO_PKG"
    [[ "$MODULE" == "." ]] && BINDINGS_DIR="$PROJECT_DIR/pkg/contracts/$GO_PKG"

    mkdir -p "$BINDINGS_DIR"
    jq '.abi' "$FORGE_OUT" > "$BINDINGS_DIR/${CONTRACT_NAME}.abi"

    # An interface compiles to no bytecode. abigen only needs --bin to emit a
    # Deploy helper, so omitting it yields call-and-pack bindings and no way to
    # deploy something this repository does not own.
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

    ( cd "$PROJECT_DIR/$MODULE" && go generate "./pkg/contracts/$GO_PKG/" )
    echo "  → ${BINDINGS_DIR#"$PROJECT_DIR"/}/autogen.go"
}

for entry in "${CONTRACTS[@]}"; do
    IFS=":" read -r SRC CONTRACT_NAME GO_PKG MODULES <<< "$entry"

    echo ""
    echo "=== $CONTRACT_NAME ($SRC) → $GO_PKG ==="

    # forge flattens its output tree to the source basename, wherever the
    # source lives in the upstream checkout.
    FORGE_OUT="$CONTRACTS_DIR/out/${SRC}/${CONTRACT_NAME}.json"
    if [[ ! -f "$FORGE_OUT" ]]; then
        echo "ERROR: forge output not found at $FORGE_OUT" >&2
        echo "       Is ${SRC} imported by contracts/src/Bindings.sol?" >&2
        exit 1
    fi

    if [[ -z "$(jq -r '.bytecode.object' "$FORGE_OUT" | sed 's/^0x//')" ]]; then
        echo "  (interface: no bytecode, bindings are call-only)"
    fi

    if [[ "$MODULES" == "both" ]]; then
        emit_binding tools "$SRC" "$CONTRACT_NAME" "$GO_PKG"
        emit_binding .     "$SRC" "$CONTRACT_NAME" "$GO_PKG"
    else
        emit_binding "$MODULES" "$SRC" "$CONTRACT_NAME" "$GO_PKG"
    fi
done

echo ""
echo "=== Done ==="
