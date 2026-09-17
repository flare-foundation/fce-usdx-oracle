#!/usr/bin/env bash
# deploy-oracle.sh — put the TEE oracle contracts on a chain, deployer-governed,
# and make the extension ours.
#
# The contracts are upstream's (flare-smart-contracts-v2), compiled from the
# sibling checkout by contracts/script/DeployTeeOracle.s.sol, which repeats
# upstream's deploy except for its last step: the sender and the feed store stay
# governed by the deployer instead of being switched to production mode, so
# publications execute at once and one key runs the whole flow. That makes this
# the deploy for the simulation and for a test deployment on a testnet; a
# production deployment is upstream's own script plus governance, and nothing
# here.
#
# The deployer is the key the tools sign with (EXTENSION_OWNER_KEY). The
# extension it deploys for is EXTENSION_ID, and how that id becomes ours depends
# on the chain:
#
#   local          governance is the simulation's own key (LOCAL_GOVERNANCE_KEY), so
#                  the reserved id is reserved here with registerReserved.
#   coston/coston2 a reserved id (below 65536) must already be owned by the
#                  deployer — Flare governance's registerReserved(id, owner) — and
#                  the script checks that and stops otherwise. If the deployer is
#                  on the registry's extension-owner allowlist instead, the
#                  registry mints a public id: the contracts are deployed under
#                  the id it will assign (nextPublicExtensionId), register() is
#                  called, and the id is recorded in .env as EXTENSION_ID.
#
# Every deployed address lands in deploy/config/<chain>/deployed-addresses.json
# through import-upstream-addresses.sh, where the tools resolve them by name.
#
# Reads .env: CHAIN_URL, EXTENSION_OWNER_KEY, INITIAL_OWNER, EXTENSION_ID,
# UPSTREAM_DIR, and for local LOCAL_GOVERNANCE_KEY and SIM_DUMP. Needs forge,
# cast and jq.
#
# Usage:
#   ./deploy/scripts/deploy-oracle.sh --chain local               # the simulation (deploy-oracle.sh --chain local is this)
#   ./deploy/scripts/deploy-oracle.sh --chain coston              # deploy once, reuse if still on chain
#   ./deploy/scripts/deploy-oracle.sh --chain coston --refresh    # redeploy
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[deploy-oracle]${NC} $*"; }
warn() { echo -e "${YELLOW}[deploy-oracle] WARN:${NC} $*" >&2; }
die()  { echo -e "${RED}[deploy-oracle] ERROR:${NC} $*" >&2; exit 1; }
section() { echo -e "\n${CYAN}=== $* ===${NC}"; }

# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"

CHAIN="${CHAIN:-}"; REFRESH=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --chain) [[ $# -ge 2 ]] || die "--chain needs a value ($CHAINS_USAGE)"; CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        --refresh) REFRESH=true; shift ;;
        *) die "unknown argument: $1 (usage: $0 --chain $CHAINS_USAGE [--refresh])" ;;
    esac
done
CHAIN="${CHAIN:-local}"
chain_known "$CHAIN" || die "unknown chain: $CHAIN ($CHAINS_USAGE)"
WANT_CHAIN_ID="$(chain_id "$CHAIN")"
# These contracts stay governed by their deployer, which is what makes the test
# flow one key and no timelock. A production deployment is upstream's script,
# which ends in switchToProductionMode; that is one-way, so the choice is made
# here: a chain that runs upstream's contracts is refused rather than quietly
# given a test deployment beside them.
if ! chain_deploys_own_oracle "$CHAIN"; then
    ALT=""
    [[ "$CHAIN" == coston ]] && ALT="\n  For a test deployment of our own on the same chain, use --chain coston_local."
    die "$CHAIN runs the oracle contracts deployed by flare-smart-contracts-v2, which this script must not deploy beside.\n" \
        "  Its deployment/scripts/deploy-tee-oracle.sh $CHAIN makes them, Flare governance finishes them, and\n" \
        "  ./deploy/scripts/import-upstream-addresses.sh $CHAIN brings the addresses here. See docs/deployment-steps.md.$ALT"
fi

# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"; load_env

CHAIN_URL="${CHAIN_URL:-$(chain_rpc "$CHAIN")}"
UPSTREAM="${UPSTREAM_DIR:-$PROJECT_DIR/../../../fsp/flare-smart-contracts-v2}"
[[ "$UPSTREAM" != /* ]] && UPSTREAM="$PROJECT_DIR/$UPSTREAM"
ADDRS="$PROJECT_DIR/deploy/config/$CHAIN/deployed-addresses.json"
DEPLOYER_KEY="${EXTENSION_OWNER_KEY:-}"
# forge reads the key as a uint and insists on the 0x prefix; .env may omit it.
[[ -z "$DEPLOYER_KEY" || "$DEPLOYER_KEY" == 0x* ]] || DEPLOYER_KEY="0x$DEPLOYER_KEY"
EXTENSION_ID="${EXTENSION_ID:-1}"

for t in forge cast jq; do command -v "$t" >/dev/null || die "$t is required"; done
[[ -n "$DEPLOYER_KEY" ]] || die "EXTENSION_OWNER_KEY is not set — activate .env.$CHAIN (use-chain.sh $CHAIN)"
[[ -d "$UPSTREAM/contracts" ]] || die "no flare-smart-contracts-v2 checkout at $UPSTREAM (UPSTREAM_DIR)"
GOT_CHAIN_ID="$(cast chain-id --rpc-url "$CHAIN_URL" 2>/dev/null)" || die "no chain at $CHAIN_URL"
[[ "$GOT_CHAIN_ID" == "$WANT_CHAIN_ID" ]] || die "the chain at $CHAIN_URL has id $GOT_CHAIN_ID; $CHAIN is $WANT_CHAIN_ID — is .env the right one?"
DEPLOYER_ADDR="$(cast wallet address --private-key "$DEPLOYER_KEY")"
OWNER="${INITIAL_OWNER:-$DEPLOYER_ADDR}"
[[ "$(tr '[:upper:]' '[:lower:]' <<<"$OWNER")" == "$(tr '[:upper:]' '[:lower:]' <<<"$DEPLOYER_ADDR")" ]] \
    || warn "INITIAL_OWNER ($OWNER) is not the deployer ($DEPLOYER_ADDR); the extension is wired by its owner, so wire-extension must run with EXTENSION_OWNER_KEY"

# Reads go through a hand-built eth_call: the simulation's Hardhat rejects
# cast's call objects, which carry both `input` and `data`, and the form works
# everywhere. Sends carry an explicit gas limit, so cast never asks the node to
# estimate a transaction that would revert.
eth_call() {  # eth_call <from> <to> <sig> [args...] → raw hex return data; sets CALL_ERR on revert
    local from="$1" to="$2" sig="$3"; shift 3
    local data; data="$(cast calldata "$sig" "$@")"
    CALL_ERR=""
    local out
    out="$(cast rpc eth_call "{\"from\":\"$from\",\"to\":\"$to\",\"data\":\"$data\"}" latest --rpc-url "$CHAIN_URL" 2>&1 || true)"
    if [[ "$out" == '"0x'* ]]; then echo "${out//\"/}"; else CALL_ERR="$out"; echo ""; fi
}
word_addr() { echo "0x${1: -40}"; }               # last 20 bytes of a 32-byte word
word_uint() { printf '%d' "$((16#${1#0x}))"; }    # a 32-byte word as decimal
GAS_LIMIT="${GAS_LIMIT:-3000000}"

# The simulation's account 0 is its busiest sender, so a transaction from it can
# lose the nonce it was built with; cast picks the nonce up again on every attempt.
send_retry() {  # send_retry <key> <cast send args...>
    local key="$1"; shift
    local out
    for _ in 1 2 3 4 5; do
        if out="$(cast send "$@" --rpc-url "$CHAIN_URL" --private-key "$key" --gas-limit "$GAS_LIMIT" --json 2>&1)"; then
            [[ "$(jq -r .status <<<"$out" 2>/dev/null)" == "0x1" ]] || { echo "reverted: $(jq -c '{transactionHash, status}' <<<"$out" 2>/dev/null || echo "$out")"; return 1; }
            echo "$out"; return 0
        fi
        grep -qiE 'nonce|replacement|underpriced|already known' <<<"$out" || { echo "$out"; return 1; }
        sleep 1
    done
    echo "$out"; return 1
}

# --- The network's system contracts the oracle contracts are wired to ---
if [[ "$CHAIN" == "local" ]]; then
    SIM_DUMP="${SIM_DUMP:-$PROJECT_DIR/../../e2e/docker/sim_dump/deployed-addresses.json}"
    [[ "$SIM_DUMP" != /* ]] && SIM_DUMP="$PROJECT_DIR/$SIM_DUMP"
    [[ -f "$SIM_DUMP" ]] || die "no sim dump at $SIM_DUMP — bring the e2e stack up first (SIM_DUMP)"
    GOV_KEY="${LOCAL_GOVERNANCE_KEY:-}"
    [[ -n "$GOV_KEY" ]] || die "LOCAL_GOVERNANCE_KEY is not set — activate .env.local"
    sysaddr() { jq -r ".$1 // empty" "$SIM_DUMP"; }
else
    [[ -f "$ADDRS" ]] || die "no addresses file at ${ADDRS#"$PROJECT_DIR"/} — run import-upstream-addresses.sh $CHAIN first"
    # The addresses file mirrors upstream's deploys file, which lists what that
    # repository deployed; contracts that predate it, the AddressUpdater among
    # them, are looked up in the network's FlareContractRegistry, at the same
    # address on every Flare chain.
    CONTRACT_REGISTRY=0xaD67FE66660Fb8dFE9d6b1b4240d8650e30F6019
    sysaddr() {
        local addr; addr="$(jq -r "[.[] | select(.name==\"$1\") | .address][0] // empty" "$ADDRS")"
        if [[ -z "$addr" ]]; then
            addr="$(word_addr "$(eth_call "$DEPLOYER_ADDR" "$CONTRACT_REGISTRY" 'getContractAddressByName(string)' "$1")")"
            [[ "$addr" == "0x0000000000000000000000000000000000000000" || ${#addr} -ne 42 ]] && addr=""
        fi
        echo "$addr"
    }
fi
ADDRESS_UPDATER="$(sysaddr AddressUpdater)"; FTM="$(sysaddr FlareTeeManager)"
FDC2_VERIFICATION="$(sysaddr Fdc2Verification)"; FEE_CALCULATOR="$(sysaddr FeeCalculator)"
# GovernanceSettings is a genesis contract on the simulation, Coston2 and the
# mainnets, but a deployed one on Coston; the registry knows where.
if [[ "$CHAIN" == "local" ]]; then
    GS_ADDR="${GOVERNANCE_SETTINGS_ADDRESS:-0x1000000000000000000000000000000000000007}"
else
    GS_ADDR="${GOVERNANCE_SETTINGS_ADDRESS:-$(sysaddr GovernanceSettings)}"
fi
for v in ADDRESS_UPDATER FTM FDC2_VERIFICATION FEE_CALCULATOR GS_ADDR; do
    [[ -n "${!v}" ]] || die "$v is not in the $CHAIN addresses — the file predates the TEE contracts?"
done

# --- The extension: whose is it, and which id ---
section "Extension $EXTENSION_ID on $CHAIN"
REGISTER_PUBLIC=false
CURRENT_OWNER="$(word_addr "$(eth_call "$DEPLOYER_ADDR" "$FTM" 'getExtensionOwner(uint256)' "$EXTENSION_ID")")"
if [[ "$(tr '[:upper:]' '[:lower:]' <<<"$CURRENT_OWNER")" == "$(tr '[:upper:]' '[:lower:]' <<<"$OWNER")" ]]; then
    log "extension $EXTENSION_ID is owned by $OWNER"
elif [[ "$CURRENT_OWNER" != "0x0000000000000000000000000000000000000000" ]]; then
    die "extension $EXTENSION_ID is owned by $CURRENT_OWNER, not by $OWNER — pick the id governance reserved for you (EXTENSION_ID in .env)"
elif [[ "$CHAIN" == "local" ]]; then
    log "extension $EXTENSION_ID is unowned; the simulation's governance reserves it below"
else
    ALLOWED="$(eth_call "$DEPLOYER_ADDR" "$FTM" 'isAllowedExtensionOwner(address)' "$DEPLOYER_ADDR")"
    if [[ "$ALLOWED" == *1 ]]; then
        NEXT_ID="$(word_uint "$(eth_call "$DEPLOYER_ADDR" "$FTM" 'nextPublicExtensionId()')")"
        (( NEXT_ID < 65536 )) && NEXT_ID=65536
        if [[ "$EXTENSION_ID" != "$NEXT_ID" && "$EXTENSION_ID" != "1" ]]; then
            die "extension $EXTENSION_ID is unowned and cannot be reserved from here; the registry would mint $NEXT_ID for this key — unset EXTENSION_ID in .env to take it"
        fi
        EXTENSION_ID="$NEXT_ID"; REGISTER_PUBLIC=true
        log "$DEPLOYER_ADDR is an allowed extension owner; the registry will mint extension $EXTENSION_ID for it (register)"
    else
        die "extension $EXTENSION_ID is unowned and $DEPLOYER_ADDR may not register one. Flare governance must run\n" \
            "  registerReserved($EXTENSION_ID, $OWNER)   on the FlareTeeManager at $FTM ($CHAIN),\n" \
            "  or add $DEPLOYER_ADDR to the extension-owner allowlist so this script can register a public id."
    fi
fi

# --- Oracle contracts ---
section "Oracle contracts"
mkdir -p "$PROJECT_DIR/out"
DEPLOY_OUT="$PROJECT_DIR/out/deploy-oracle.$CHAIN.json"
SENDER="$(jq -r '[.[] | select(.name=="TeeOracleInstructionsSender") | .address][0] // empty' "$ADDRS" 2>/dev/null || true)"
if [[ "$CHAIN" == "local" && -f "$ADDRS" && "$(jq -r '[.[] | select(.name=="AddressUpdater") | .address][0] // empty' "$ADDRS")" != "$ADDRESS_UPDATER" ]]; then
    # A new simulation has a new AddressUpdater; every oracle address recorded
    # against the old one is stale, so the deploy runs again.
    REFRESH=true
fi
# A recorded sender is reused only when it is this deployment's: deployed for
# this extension id (immutable in the contract) and governed by this deployer,
# who publishes through it. One left by another key or another id, as the file
# holds after a change of deployer, is replaced.
if [[ "$REFRESH" == "false" && -n "$SENDER" && "$(cast code "$SENDER" --rpc-url "$CHAIN_URL")" != "0x" ]]; then
    SENDER_EXT="$(word_uint "$(eth_call "$DEPLOYER_ADDR" "$SENDER" 'extensionId()')" 2>/dev/null || echo "")"
    SENDER_GOV="$(word_addr "$(eth_call "$DEPLOYER_ADDR" "$SENDER" 'governance()')" 2>/dev/null || echo "")"
    if [[ "$SENDER_EXT" != "$EXTENSION_ID" ]]; then
        log "the recorded sender $SENDER was deployed for extension ${SENDER_EXT:-?}, not $EXTENSION_ID — deploying anew"
        REFRESH=true
    elif [[ "$(tr '[:upper:]' '[:lower:]' <<<"$SENDER_GOV")" != "$(tr '[:upper:]' '[:lower:]' <<<"$DEPLOYER_ADDR")" ]]; then
        log "the recorded sender $SENDER is governed by ${SENDER_GOV:-?}, not by this deployer — deploying anew"
        REFRESH=true
    fi
fi
if [[ "$REFRESH" == "false" && -n "$SENDER" && "$(cast code "$SENDER" --rpc-url "$CHAIN_URL")" != "0x" ]]; then
    log "TeeOracleInstructionsSender already on chain at $SENDER — keeping it (pass --refresh to redeploy)"
    jq '[.[] | select(.name | test("TeeOracle|FeedStore"))]' "$ADDRS" > "$DEPLOY_OUT"
else
    [[ "$(cast code "$GS_ADDR" --rpc-url "$CHAIN_URL")" != "0x" ]] || die "no GovernanceSettings code at $GS_ADDR — not a Flare network?"
    # The contracts are upstream's sources; their OpenZeppelin and flare-periphery
    # imports resolve through upstream's Soldeer dependencies/, which a fresh
    # checkout does not have.
    MISSING_DEPS=""
    while IFS= read -r line; do
        name="$(echo "$line" | cut -d= -f1 | tr -d ' "')"; ver="$(echo "$line" | cut -d= -f2 | tr -d ' "')"
        [[ -n "$name" && -n "$ver" && ! -d "$UPSTREAM/dependencies/$name-$ver" ]] && MISSING_DEPS="$MISSING_DEPS $name-$ver"
    done < <(awk '/^\[dependencies\]/{f=1; next} /^\[/{f=0} f && /=/' "$UPSTREAM/foundry.toml")
    if [[ -n "$MISSING_DEPS" ]]; then
        log "installing upstream's Foundry dependencies (missing:$MISSING_DEPS)"
        ( cd "$UPSTREAM" && forge soldeer install >/dev/null ) || die "forge soldeer install failed in $UPSTREAM"
    fi
    log "deploying from $DEPLOYER_ADDR for extension $EXTENSION_ID; the deployer stays the sender's governance (no production mode, no timelock)"
    FORGE_LOG="$PROJECT_DIR/out/deploy-oracle.$CHAIN.forge.log"
    # --slow confirms each transaction before the next: forge precomputes the
    # CREATE addresses, and a nonce moving under it would leave proxies pointing
    # at implementations that never land.
    ( cd "$PROJECT_DIR/contracts" && \
        DEPLOYER_PRIVATE_KEY="$DEPLOYER_KEY" EXTENSION_ID="$EXTENSION_ID" GOVERNANCE_SETTINGS="$GS_ADDR" \
        ADDRESS_UPDATER="$ADDRESS_UPDATER" FLARE_TEE_MANAGER="$FTM" \
        FDC2_VERIFICATION="$FDC2_VERIFICATION" FEE_CALCULATOR="$FEE_CALCULATOR" \
        forge script script/DeployTeeOracle.s.sol:DeployTeeOracle \
            --rpc-url "$CHAIN_URL" --private-key "$DEPLOYER_KEY" --broadcast --slow --non-interactive ) 2>&1 | tee "$FORGE_LOG" \
        || die "deploy failed (log: ${FORGE_LOG#"$PROJECT_DIR"/})"
    # One DEPLOYED line per contract, in upstream's format; they become a deploys
    # file in upstream's shape for the importer.
    grep -E '^[[:space:]]*DEPLOYED:' "$FORGE_LOG" \
        | sed -E 's/^[[:space:]]*DEPLOYED:[[:space:]]*([^,]+),[[:space:]]*([^:]+):[[:space:]]*(0x[0-9a-fA-F]{40}).*/\1|\2|\3/' \
        | jq -Rn '[inputs | split("|") | {name: .[0], contractName: .[1], address: .[2]}]' > "$DEPLOY_OUT"
    SENDER="$(jq -r '[.[] | select(.name=="TeeOracleInstructionsSender") | .address][0] // empty' "$DEPLOY_OUT")"
    [[ -n "$SENDER" ]] || die "deploy ran but printed no TeeOracleInstructionsSender address (log: ${FORGE_LOG#"$PROJECT_DIR"/})"
    log "sender at $SENDER ($(jq length "$DEPLOY_OUT") contracts)"
fi
# --- Making the extension ours ---
section "Extension ownership"
if [[ "$CHAIN" == "local" ]]; then
    GOV_ADDR="$(cast wallet address --private-key "$GOV_KEY")"
    # Preflight as governance: a clean call means the registration would land;
    # the one revert worth recognising is that it already has. Hardhat reports a
    # custom error by name when it can and by selector when it cannot.
    ALREADY_SEL="$(cast sig 'ReservedExtensionIdAlreadyAssigned()')"
    eth_call "$GOV_ADDR" "$FTM" 'registerReserved(uint256,address)' "$EXTENSION_ID" "$OWNER" >/dev/null
    if [[ -z "$CALL_ERR" ]]; then
        OUT="$(send_retry "$GOV_KEY" "$FTM" 'registerReserved(uint256,address)' "$EXTENSION_ID" "$OWNER")" \
            || die "registerReserved failed:\n$OUT"
        log "extension $EXTENSION_ID reserved for $OWNER"
    elif grep -qiE "ReservedExtensionIdAlreadyAssigned|${ALREADY_SEL#0x}" <<<"$CALL_ERR"; then
        log "extension $EXTENSION_ID is already reserved — leaving it"
    else
        die "registerReserved would revert: $CALL_ERR"
    fi
elif [[ "$REGISTER_PUBLIC" == "true" ]]; then
    # No state verifier: the extension answers GET /state with the empty state,
    # which the registry accepts on its own, refusing tee-node's "could not read
    # the extension" sentinel as a verifier would.
    send_retry "$DEPLOYER_KEY" "$FTM" 'register(address,address)' \
        0x0000000000000000000000000000000000000000 "$SENDER" >/dev/null \
        || die "register(verifier, sender) failed"
    MINTED_OWNER="$(word_addr "$(eth_call "$DEPLOYER_ADDR" "$FTM" 'getExtensionOwner(uint256)' "$EXTENSION_ID")")"
    [[ "$(tr '[:upper:]' '[:lower:]' <<<"$MINTED_OWNER")" == "$(tr '[:upper:]' '[:lower:]' <<<"$DEPLOYER_ADDR")" ]] \
        || die "the registry minted a different id than predicted ($EXTENSION_ID is owned by $MINTED_OWNER); the sender was deployed for $EXTENSION_ID — redeploy with --refresh"
    log "extension $EXTENSION_ID registered to $DEPLOYER_ADDR with sender $SENDER and no state verifier"
    # The stack and every tool read the id from .env; a public id is not the
    # default 1, so it is recorded there, and in the chain's own file, which
    # use-chain.sh copies over .env on every chain switch and full-setup.sh
    # --chain run.
    for envfile in "$PROJECT_DIR/.env" "$PROJECT_DIR/.env.$CHAIN"; do
        [[ -f "$envfile" ]] || continue
        if grep -qE '^EXTENSION_ID=' "$envfile"; then
            sed -i '' -E "s/^EXTENSION_ID=.*/EXTENSION_ID=$EXTENSION_ID/" "$envfile"
        else
            printf '\n# The extension id the registry minted for this key, recorded by deploy-oracle.sh.\nEXTENSION_ID=%s\n' "$EXTENSION_ID" >> "$envfile"
        fi
        log "EXTENSION_ID=$EXTENSION_ID recorded in ${envfile#"$PROJECT_DIR"/}"
    done
else
    log "extension $EXTENSION_ID is reserved to its owner; wire-extension publishes the sender"
fi

section "Addresses → deploy/config/$CHAIN"
if [[ "$CHAIN" == "local" ]]; then
    DEPLOYS_SRC="$DEPLOY_OUT" SIM_DUMP="$SIM_DUMP" "$SCRIPT_DIR/import-upstream-addresses.sh" local "$UPSTREAM"
else
    DEPLOYS_SRC="$DEPLOY_OUT" "$SCRIPT_DIR/import-upstream-addresses.sh" "$CHAIN" "$UPSTREAM"
fi

echo ""
if [[ "$CHAIN" == "local" ]]; then
    log "next: (cd tools && go run ./cmd/wire-extension) then USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local"
else
    log "next: (cd tools && go run ./cmd/wire-extension) then ./deploy/scripts/full-setup.sh --chain $CHAIN (with the tunnel up)"
fi
