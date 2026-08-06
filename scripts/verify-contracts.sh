#!/usr/bin/env bash
# verify-contracts.sh — publish contract source to the block explorer.
#
# Verification is what lets anyone read the code behind an address rather than
# taking our word for it. For this project that is not cosmetic: the whole claim
# is that a consumer can check *what* computes the price, and half of that chain
# is on-chain logic. An unverified UsdxPriceStore means nobody can confirm the
# acceptance rules they are relying on.
#
#   ./scripts/verify-contracts.sh                    # coston2, skip already-verified
#   ./scripts/verify-contracts.sh --chain coston
#   ./scripts/verify-contracts.sh --force            # re-submit even if verified
#   ./scripts/verify-contracts.sh --only UsdxPriceStore
#
# Constructor arguments are recovered from the deployment itself, never
# hand-written. They have to be: `UsdxPriceStore.setExtension` rewrites two of
# the values its constructor took, so reading them out of storage today would
# produce arguments the contract was never deployed with, and verification would
# fail with no obvious reason. The explorer serves the creation bytecode, which
# is `initcode || abi.encode(args)` — strip the locally compiled initcode and
# what remains is exactly the arguments.
#
# That subtraction doubles as a reproducibility check: if the local build does
# not reproduce the deployed initcode byte for byte, the prefix will not match
# and this stops rather than submitting something that cannot verify.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[verify]${NC} $*"; }
warn() { echo -e "${YELLOW}[verify]${NC} $*"; }
die()  { echo -e "${RED}[verify] ERROR:${NC} $*" >&2; exit 1; }

CHAIN="coston2"
FORCE=false
ONLY=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --chain) [[ $# -ge 2 ]] || die "--chain requires a value"; CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        --only)  [[ $# -ge 2 ]] || die "--only requires a contract name"; ONLY="$2"; shift 2 ;;
        --only=*) ONLY="${1#--only=}"; shift ;;
        --force) FORCE=true; shift ;;
        -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
        *) die "Unknown argument: $1" ;;
    esac
done

case "$CHAIN" in
    coston2) EXPLORER="https://coston2-explorer.flare.network"; CHAIN_ID=114 ;;
    coston)  EXPLORER="https://coston-explorer.flare.network";  CHAIN_ID=16  ;;
    *) die "Unknown --chain: $CHAIN (valid: coston, coston2)" ;;
esac

command -v forge >/dev/null || die "forge not found — install Foundry"
command -v jq    >/dev/null || die "jq not found"

ADDRESSES_FILE="$PROJECT_DIR/config/$CHAIN/deployed-addresses.json"
[[ -f "$ADDRESSES_FILE" ]] || die "no addresses file at $ADDRESSES_FILE"

cd "$PROJECT_DIR"

# Build first. Verification compares against the artifacts in out/, so a stale
# build is the difference between "verified" and a mismatch nobody can explain.
log "Building contracts..."
BUILD_LOG="$(mktemp)"
# forge build writes lint notes to stderr on every run; they bury the output
# that matters here. Kept and shown only if the build actually fails.
forge build >"$BUILD_LOG" 2>&1 || { cat "$BUILD_LOG" >&2; die "forge build failed"; }

# --- what to verify -----------------------------------------------------------
#
# The instruction sender is not in the addresses file — pre-build.sh records it
# in config/extension.env, because it is redeployed far more often than the
# store and the feed.
SENDER=""
if [[ -f "$PROJECT_DIR/config/extension.env" ]]; then
    # shellcheck disable=SC1091
    SENDER=$(grep -E '^INSTRUCTION_SENDER=' "$PROJECT_DIR/config/extension.env" | cut -d= -f2 | tr -d '"' || true)
fi

lookup() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0] // empty' "$ADDRESSES_FILE"; }

declare -a NAMES PATHS ADDRS
add() {
    local name="$1" path="$2" addr="$3"
    # `return 0`, not bare `return`: a bare one propagates the failed test's
    # status, and under `set -e` that exits the whole script instead of skipping
    # a contract.
    [[ -z "$ONLY" || "$ONLY" == "$name" ]] || return 0
    [[ -n "$addr" ]] || { warn "$name: no address found, skipping"; return 0; }
    NAMES+=("$name"); PATHS+=("$path"); ADDRS+=("$addr")
}

add "UsdxOracleInstructionSender" "contracts/InstructionSender.sol:UsdxOracleInstructionSender" "$SENDER"
add "UsdxPriceStore"              "contracts/UsdxPriceStore.sol:UsdxPriceStore"                 "$(lookup UsdxPriceStore)"
add "UsdxCustomFeed"              "contracts/UsdxCustomFeed.sol:UsdxCustomFeed"                 "$(lookup UsdxCustomFeed)"

[[ ${#NAMES[@]} -gt 0 ]] || die "nothing to verify${ONLY:+ (no contract named '$ONLY')}"

# --- constructor args from the deployment -------------------------------------
constructor_args() {
    local addr="$1" artifact="$2"
    local created
    created=$(curl -sS --max-time 60 \
        "$EXPLORER/api?module=contract&action=getcontractcreation&contractaddresses=$addr" \
        | jq -r '.result[0].creationBytecode // empty')
    [[ -n "$created" ]] || { echo "ERR:no creation bytecode from the explorer"; return; }

    python3 - "$created" "$artifact" <<'PY'
import json, sys
created, artifact = sys.argv[1], sys.argv[2]
code = json.load(open(artifact))["bytecode"]["object"]
if not created.startswith(code):
    # Either the source drifted from what was deployed, or the compiler settings
    # did. Submitting anyway would fail verification with a confusing error.
    print("ERR:local build does not reproduce the deployed initcode — "
          "check out the commit that was deployed, or confirm foundry.toml is unchanged")
else:
    print("0x" + created[len(code):])
PY
}

is_verified() {
    curl -sS --max-time 30 "$EXPLORER/api/v2/addresses/$1" | jq -r '.is_verified // false'
}

# --- run ----------------------------------------------------------------------
echo -e "\n${CYAN}Chain:${NC}    $CHAIN"
echo -e "${CYAN}Explorer:${NC} $EXPLORER\n"

FAILED=0
for i in "${!NAMES[@]}"; do
    name="${NAMES[$i]}"; path="${PATHS[$i]}"; addr="${ADDRS[$i]}"
    artifact="out/$(basename "${path%%:*}")/${path##*:}.json"

    echo -e "${CYAN}=== $name ===${NC}"
    log "address:  $addr"

    if [[ "$FORCE" != "true" ]] && [[ "$(is_verified "$addr")" == "true" ]]; then
        log "already verified — skipping (use --force to re-submit)"
        echo ""
        continue
    fi

    [[ -f "$artifact" ]] || { warn "no artifact at $artifact, skipping"; FAILED=$((FAILED+1)); continue; }

    args=$(constructor_args "$addr" "$artifact")
    if [[ "$args" == ERR:* ]]; then
        warn "${args#ERR:}"
        FAILED=$((FAILED+1)); echo ""; continue
    fi
    log "constructor args: $(( (${#args} - 2) / 2 )) bytes"

    # --chain is passed as a NUMBER on purpose. Foundry auto-loads `.env` from
    # the project root, where this repo sets CHAIN=coston2 for its own scripts —
    # forge reads that as its own --chain flag and rejects it, since its chain
    # list has no such name. An explicit numeric id overrides it.
    #
    # --watch blocks until the explorer reports a result, so the exit status
    # means something.
    if forge verify-contract "$addr" "$path" \
        --chain "$CHAIN_ID" \
        --verifier blockscout \
        --verifier-url "$EXPLORER/api/" \
        --constructor-args "$args" \
        --watch; then
        log "verified: $EXPLORER/address/$addr#code"
    else
        warn "verification failed for $name"
        FAILED=$((FAILED+1))
    fi
    echo ""
done

if [[ $FAILED -gt 0 ]]; then
    die "$FAILED contract(s) failed to verify"
fi
log "All contracts verified."
