#!/usr/bin/env bash
#
# use-chain.sh — Activate a chain-specific .env file.
#
# Reads .env.<chain> from the project root and copies it to .env. All
# deployment scripts (full-setup, post-build, test) auto-load .env.
#
# Usage:
#   ./deploy/scripts/use-chain.sh <chain>       Copy .env.<chain> → .env (e.g. coston).
#   ./deploy/scripts/use-chain.sh --list        List available .env.<chain> files.
#   ./deploy/scripts/use-chain.sh -h | --help   Show this help.
#
#   DEPLOYMENT_PRIVATE_KEY=0x… EXT_PROXY_URL=https://… ./deploy/scripts/use-chain.sh coston
#       On a live chain, .env.<chain> is created from .env.example when missing,
#       and every value the funded key implies is filled in where the file has
#       none: INITIAL_OWNER (derived), PROXY_PRIVATE_KEY and DIRECT_API_KEY
#       (generated), USDXPROV_KEY / USDXPROV_KEYS (the deployer, plus a generated
#       second provider admin) and deploy/config/admins/<chain>.json with the
#       matching addresses. A value already in the file is never overwritten, so
#       later switches are just `use-chain.sh coston`, and the file stays editable.
#
#   HEX_API_KEY=… HEX_CASH_URL=… ./deploy/scripts/use-chain.sh local
#       Also write the Hex backing credentials into .env. They, and the
#       HEX_CASH_SALT publish-local.sh records next to them, are carried over
#       from the previous .env on later switches, so the tracked .env.<chain>
#       never holds them and the file needs no editing by hand. --no-hex drops them.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
SCRIPT_NAME="$(basename "$0")"
source "$SCRIPT_DIR/lib/chains.sh"

log() { echo "[use-chain] $*"; }
die() { echo "[use-chain] ERROR: $*" >&2; exit 1; }

usage() {
    cat <<EOF
use-chain.sh — Activate a chain-specific .env file.

Usage:
  $SCRIPT_NAME <chain>       Copy .env.<chain> → .env (local, coston, coston2, songbird, flare).
  $SCRIPT_NAME --list        List available .env.<chain> files.
  $SCRIPT_NAME -h | --help   Show this help.

  DEPLOYMENT_PRIVATE_KEY=0x… EXT_PROXY_URL=https://… $SCRIPT_NAME coston
                             Live chain: create .env.coston when missing and fill in
                             everything the funded key implies (owner, proxy key,
                             DIRECT_API_KEY, provisioning keys, the admin set).
                             Present values are kept.
  HEX_API_KEY=… HEX_CASH_URL=… $SCRIPT_NAME <chain>
                             Also write the Hex backing credentials into .env.
                             They (and HEX_CASH_SALT) carry over from the previous
                             .env on later switches; --no-hex drops them.

The active .env is auto-loaded by every deployment script in scripts/.
EOF
}

# Discover .env.<chain> files in the project root, skipping helper files.
list_chains() {
    local any=0 f name
    for f in "$PROJECT_DIR"/.env.*; do
        [[ -f "$f" ]] || continue
        name="${f##*/.env.}"
        case "$name" in example|template|sample) continue ;; esac
        echo "  $name"
        any=1
    done
    [[ $any -eq 1 ]] || echo "  (none — create .env.<chain> from .env.example)"
}

# Extract a value from $DST for the given KEY:
#   - strip the "KEY=" prefix
#   - strip trailing " # comment"
#   - trim trailing whitespace
#   - strip surrounding double quotes
get_val() {
    grep -E "^$1=" "$DST" 2>/dev/null | head -1 \
        | sed -E "s/^[^=]*=//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//" \
        | tr -d '"'
}

# --- Argument parsing ---

[[ $# -ge 1 ]] || { usage; exit 1; }
KEEP_HEX=true; CHAIN=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --list)    log "available chains:"; list_chains; exit 0 ;;
        --no-hex)  KEEP_HEX=false; shift ;;
        -*)        die "unknown flag: $1 (try --help)" ;;
        *)         [[ -z "$CHAIN" ]] || die "expected one chain name (got $CHAIN and $1); try --help"; CHAIN="$1"; shift ;;
    esac
done
[[ -n "$CHAIN" ]] || die "expected a chain name; try --help"
SRC="$PROJECT_DIR/.env.$CHAIN"
DST="$PROJECT_DIR/.env"

LIVE=false
chain_known "$CHAIN" && [[ "$CHAIN" != local ]] && LIVE=true
if [[ ! -f "$SRC" ]]; then
    if [[ "$LIVE" == true ]]; then
        # The template, with the chain pinned. Only when the file is missing: it is
        # the one place the deployment's keys live, and nothing recovers a lost
        # deployer key.
        cp "$PROJECT_DIR/.env.example" "$SRC"
        sed -i '' -E "s/^FLARE_CHAIN=.*/FLARE_CHAIN=$CHAIN/" "$SRC"
        log "created .env.$CHAIN from .env.example"
    else
        log "no .env.$CHAIN in $PROJECT_DIR"
        log "available chains:"
        list_chains
        exit 1
    fi
fi

# --- What the funded key implies, filled into .env.<chain> where it has nothing ---
#
# A live chain needs a handful of values that are all derived from, or generated
# next to, the deployer key: its address; a signing key for the proxy; the key
# guarding /direct; the provisioning admin keys (the deployer signs as the
# backing admin and as the first provider admin, a throwaway key as the second,
# since that set needs two signatures). Given on this command line, the deployer
# key and the machine's public URL are written too. Present values are kept.
val_in() {  # val_in FILE KEY → the value, unquoted, without a trailing comment
    grep -E "^$2=" "$1" 2>/dev/null | head -1 \
        | sed -E "s/^[^=]*=//; s/[[:space:]]+#.*$//; s/[[:space:]]+$//" | tr -d '"'
}
fill() {  # fill KEY VALUE: set KEY in .env.<chain> unless it already has a value
    local key="$1" value="$2" quoted escaped
    [[ -n "$value" && -z "$(val_in "$SRC" "$key")" ]] || return 0
    quoted="$value"; [[ "$value" =~ [^A-Za-z0-9_./:-] ]] && quoted="\"$value\""
    # & is the whole match in a sed replacement, \ and the delimiter are special.
    escaped="$(printf '%s' "$quoted" | sed 's/[&|\\]/\\&/g')"
    if grep -qE "^$key=" "$SRC"; then
        sed -i '' -E "s|^$key=.*|$key=$escaped|" "$SRC"
    elif grep -qE "^# ?$key=" "$SRC"; then
        sed -i '' -E "s|^# ?$key=.*|$key=$escaped|" "$SRC"
    else
        printf '\n%s=%s\n' "$key" "$quoted" >> "$SRC"
    fi
    FILLED="$FILLED $key"
}
FILLED=""
if [[ "$LIVE" == true ]]; then
    fill DEPLOYMENT_PRIVATE_KEY "${DEPLOYMENT_PRIVATE_KEY:-}"
    fill EXT_PROXY_URL "${EXT_PROXY_URL:-}"
    DEPLOYER_KEY="$(val_in "$SRC" DEPLOYMENT_PRIVATE_KEY)"
    if [[ -n "$DEPLOYER_KEY" ]]; then
        command -v cast >/dev/null || die "cast (Foundry) is required to derive the owner and generate the keys"
        DEPLOYER_KEY="0x${DEPLOYER_KEY#0x}"
        [[ "$DEPLOYER_KEY" =~ ^0x[0-9a-fA-F]{64}$ ]] || die "DEPLOYMENT_PRIVATE_KEY in .env.$CHAIN is not a 32-byte hex key"
        fill INITIAL_OWNER "$(cast wallet address --private-key "$DEPLOYER_KEY")"
        fill PROXY_PRIVATE_KEY "$(cast wallet new | awk '/^Private key:/{print $3}')"
        fill DIRECT_API_KEY "$(openssl rand -hex 32)"
        fill USDXPROV_KEY "$DEPLOYER_KEY"
        fill USDXPROV_KEYS "$DEPLOYER_KEY,$(cast wallet new | awk '/^Private key:/{print $3}')"
    fi
    fill LOCAL_MODE false
    fill SIMULATED_TEE true
    [[ -n "$FILLED" ]] && log "filled in .env.$CHAIN:$FILLED"
fi

# --- Hex backing credentials ---
#
# These live in no tracked .env.<chain>. They arrive on the call
# (HEX_API_KEY=… HEX_CASH_URL=… use-chain.sh local) or are already in the .env
# about to be replaced, and either way they survive the switch. The salt travels
# with them because it binds the commitment publish-local.sh publishes to the
# URL the machine is later handed; losing it would orphan the published config.
# Taken before the copy, from the file being replaced.
carry() {
    local v="${!1:-}"
    [[ -n "$v" ]] || v="$(get_val "$1")"
    printf '%s' "$v"
}
HEX_API_KEY_V="$(carry HEX_API_KEY)"
HEX_CASH_URL_V="$(carry HEX_CASH_URL)"
HEX_CASH_SALT_V="$(carry HEX_CASH_SALT)"

# --- Swap ---

# Every script sources .env as shell, so a value the shell cannot read — an
# unquoted <placeholder>, an unbalanced quote — would stop all of them with a
# line number and no context. Checked here, where the file is chosen.
if ! ERR="$(bash -c 'set -e; . "$1"' _ "$SRC" 2>&1 >/dev/null)"; then
    die "$SRC does not source as shell: ${ERR#*: line }\n  quote values with unusual characters, e.g. EXT_PROXY_URL=\"https://…\""
fi
cp "$SRC" "$DST" || die "failed to copy $SRC → $DST"
# The scripts resolve the chain from this name (scripts/lib/chains.sh); pinned
# here so a .env.<chain> that predates the variable still names its chain. Not
# CHAIN: Foundry's cast reads that variable as its --chain option.
sed -i '' -E 's/^CHAIN=/FLARE_CHAIN=/' "$DST"
grep -qE '^FLARE_CHAIN=' "$DST" || printf '\n# The chain this file targets, pinned by use-chain.sh.\nFLARE_CHAIN=%s\n' "$CHAIN" >> "$DST"
log "active chain: $CHAIN"

if [[ "$KEEP_HEX" == true ]]; then
    hex_header_written=false
    # add_hex appends KEY="VALUE" unless the copied file already defines KEY.
    # Quoted, because .env is sourced by the shell and a URL's & or ? would
    # otherwise be parsed rather than assigned.
    add_hex() {
        [[ -n "$2" ]] || return 0
        grep -q "^$1=" "$DST" && return 0
        if [[ "$hex_header_written" == false ]]; then
            printf '\n# Hex backing credentials, written by use-chain.sh from its call or carried over\n# from the previous .env. See .env.example; use-chain.sh --no-hex drops them.\n' >> "$DST"
            hex_header_written=true
        fi
        printf '%s="%s"\n' "$1" "$2" >> "$DST"
    }
    add_hex HEX_API_KEY   "$HEX_API_KEY_V"
    add_hex HEX_CASH_URL  "$HEX_CASH_URL_V"
    add_hex HEX_CASH_SALT "$HEX_CASH_SALT_V"
fi

# --- The admin set that matches the provisioning keys ---
# Addresses only. Written once; a set of your own replaces it by hand.
ADMINS="$PROJECT_DIR/deploy/config/admins/$CHAIN.json"
if [[ "$LIVE" == true && ! -f "$ADMINS" ]]; then
    OWNER_ADDR="$(val_in "$SRC" INITIAL_OWNER)"; SECOND_KEY="$(val_in "$SRC" USDXPROV_KEYS | cut -d, -f2)"
    if [[ -n "$OWNER_ADDR" && -n "$SECOND_KEY" ]]; then
        SECOND_ADDR="$(cast wallet address --private-key "$SECOND_KEY")"
        jq -n --arg d "$OWNER_ADDR" --arg s "$SECOND_ADDR" \
            '{backing: {threshold: 1, admins: [$d]}, providers: {threshold: 2, admins: [$d, $s]}}' > "$ADMINS"
        log "wrote ${ADMINS#$PROJECT_DIR/}: backing [$OWNER_ADDR], providers [$OWNER_ADDR, $SECOND_ADDR]"
    fi
elif [[ "$LIVE" == true && -f "$ADMINS" ]]; then
    # An admin set left from another deployer would have the machine refuse this
    # one's uploads; said here, not at the first rejected credential.
    OWNER_ADDR="$(val_in "$SRC" INITIAL_OWNER | tr '[:upper:]' '[:lower:]')"
    BACKING="$(jq -r '.backing.admins[0] // empty' "$ADMINS" 2>/dev/null | tr '[:upper:]' '[:lower:]')"
    if [[ -n "$OWNER_ADDR" && -n "$BACKING" && "$BACKING" != "$OWNER_ADDR" ]]; then
        log "WARN: ${ADMINS#$PROJECT_DIR/} names $BACKING as backing admin, not this deployer ($OWNER_ADDR); delete the file to have it rewritten, or edit it"
    fi
fi

# --- Summary ---

key_val=$(get_val DEPLOYMENT_PRIVATE_KEY)
log "  CHAIN_URL              = $(v=$(get_val CHAIN_URL);        echo "${v:-<unset>}")"
log "  EXT_PROXY_URL          = $(v=$(get_val EXT_PROXY_URL);    echo "${v:-<unset: the machine public URL, registered on chain by post-build.sh, which refuses to guess it>}")"
log "  NORMAL_PROXY_URL       = $(v=$(get_val NORMAL_PROXY_URL); echo "${v:-<unset>}")"
log "  INITIAL_OWNER          = $(v=$(get_val INITIAL_OWNER);    echo "${v:-<unset>}")"
log "  DEPLOYMENT_PRIVATE_KEY = $([[ -n "$key_val" ]] && echo "<set>" || echo "<unset — the funded key: DEPLOYMENT_PRIVATE_KEY=0x… $SCRIPT_NAME $CHAIN>")"
if [[ "$LIVE" == true ]]; then
    log "  PROXY_PRIVATE_KEY      = $([[ -n "$(get_val PROXY_PRIVATE_KEY)" ]] && echo "<set>" || echo "<unset>")"
    log "  DIRECT_API_KEY         = $([[ -n "$(get_val DIRECT_API_KEY)" ]] && echo "<set>" || echo "<unset>")"
    log "  EXTENSION_ID           = $(v=$(get_val EXTENSION_ID); echo "${v:-<unset>}")"
fi
log "  HEX_API_KEY            = $([[ -n "$(get_val HEX_API_KEY)" ]]   && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_URL           = $([[ -n "$(get_val HEX_CASH_URL)" ]]  && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_SALT          = $([[ -n "$(get_val HEX_CASH_SALT)" ]] && echo "<set>" || echo "<unset — publish-local.sh generates it>")"
