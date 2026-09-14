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

log() { echo "[use-chain] $*"; }
die() { echo "[use-chain] ERROR: $*" >&2; exit 1; }

usage() {
    cat <<EOF
use-chain.sh — Activate a chain-specific .env file.

Usage:
  $SCRIPT_NAME <chain>       Copy .env.<chain> → .env (local, coston, coston2, songbird, flare).
  $SCRIPT_NAME --list        List available .env.<chain> files.
  $SCRIPT_NAME -h | --help   Show this help.

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
KEEP_HEX=true
while [[ $# -gt 0 && "$1" == -* ]]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --list)    log "available chains:"; list_chains; exit 0 ;;
        --no-hex)  KEEP_HEX=false; shift ;;
        *)         die "unknown flag: $1 (try --help)" ;;
    esac
done
[[ $# -eq 1 ]] || die "expected one chain name (got $#); try --help"

CHAIN="$1"
SRC="$PROJECT_DIR/.env.$CHAIN"
DST="$PROJECT_DIR/.env"

if [[ ! -f "$SRC" ]]; then
    log "no .env.$CHAIN in $PROJECT_DIR"
    log "available chains:"
    list_chains
    exit 1
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

cp "$SRC" "$DST" || die "failed to copy $SRC → $DST"
# The scripts resolve the chain from this name (scripts/lib/chains.sh); pinned
# here so a .env.<chain> that predates the variable still names its chain.
grep -qE '^CHAIN=' "$DST" || printf '\n# The chain this file targets, pinned by use-chain.sh.\nCHAIN=%s\n' "$CHAIN" >> "$DST"
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

# --- Summary ---

key_val=$(get_val DEPLOYMENT_PRIVATE_KEY)
log "  CHAIN_URL              = $(v=$(get_val CHAIN_URL);        echo "${v:-<unset>}")"
log "  EXT_PROXY_URL          = $(v=$(get_val EXT_PROXY_URL);    echo "${v:-<unset>}")"
log "  NORMAL_PROXY_URL       = $(v=$(get_val NORMAL_PROXY_URL); echo "${v:-<unset>}")"
log "  INITIAL_OWNER          = $(v=$(get_val INITIAL_OWNER);    echo "${v:-<unset>}")"
log "  DEPLOYMENT_PRIVATE_KEY = $([[ -n "$key_val" ]] && echo "<set>" || echo "<unset>")"
log "  HEX_API_KEY            = $([[ -n "$(get_val HEX_API_KEY)" ]]   && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_URL           = $([[ -n "$(get_val HEX_CASH_URL)" ]]  && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_SALT          = $([[ -n "$(get_val HEX_CASH_SALT)" ]] && echo "<set>" || echo "<unset — publish-local.sh generates it>")"
