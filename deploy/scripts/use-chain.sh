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
#   EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://… ./deploy/scripts/use-chain.sh coston
#       On a live chain, .env.<chain> is created from .env.example when missing,
#       and every value the funded key implies is filled in where the file has
#       none: INITIAL_OWNER (derived), PROXY_PRIVATE_KEY and DIRECT_API_KEY
#       (generated), ADMIN_KEY / ADMIN_KEYS (the deployer, plus a generated
#       second provider admin; skipped for a chain whose admins file already
#       exists, whose admins hold their own keys) and deploy/config/admins/<chain>.json with the
#       matching addresses. Derived values never overwrite what the file has, so
#       later switches are just `use-chain.sh coston`, and the file stays editable.
#
#   EXT_PROXY_URL=https://… DIRECT_API_KEY=… SIMULATED_TEE=false ./deploy/scripts/use-chain.sh coston
#       Given on the call, these three are written into .env.<chain>, replacing
#       the file's values: they are what changes when the machine is not the
#       laptop stack but a remote one, whose proxy has a key of its own. The
#       deployer key is never replaced from the call.
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
# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"
# edit_in_place: the portable form of sed -i.
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"

log() { echo "[use-chain] $*"; }
die() { echo "[use-chain] ERROR: $*" >&2; exit 1; }

usage() {
    cat <<EOF
use-chain.sh — Activate a chain-specific .env file.

Usage:
  $SCRIPT_NAME <chain>       Copy .env.<chain> → .env (local, coston, coston_local, coston2,
                             songbird, flare). On Coston, "coston" is the official USDX
                             deployment under extension 1 and "coston_local" a test
                             deployment of our own on the same chain.
  $SCRIPT_NAME --list        List available .env.<chain> files.
  $SCRIPT_NAME -h | --help   Show this help.

  EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://… $SCRIPT_NAME coston
                             Live chain: create .env.coston when missing and fill in
                             everything the extension owner's key implies (owner,
                             proxy key, DIRECT_API_KEY, provisioning keys, the admin
                             set). Derived values never replace present ones.
  MACHINE_OWNER_KEY=0x… $SCRIPT_NAME <chain>
                             The key of the machines' INITIAL_OWNER, which the
                             registry requires as the sender of a registration.
                             Needed only where that address is not the extension
                             owner, which is the usual arrangement.
  EXT_PROXY_URL=… DIRECT_API_KEY=… SIMULATED_TEE=false $SCRIPT_NAME coston
                             Given on the call, these are written (replacing): the
                             remote machine's URL, its proxy's key, real attestation.
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
        edit_in_place "$SRC" -E "s/^FLARE_CHAIN=.*/FLARE_CHAIN=$CHAIN/" || die "could not pin FLARE_CHAIN in $SRC"
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
        edit_in_place "$SRC" -E "s|^$key=.*|$key=$escaped|" || die "could not write $key into $SRC"
    elif grep -qE "^# ?$key=" "$SRC"; then
        edit_in_place "$SRC" -E "s|^# ?$key=.*|$key=$escaped|" || die "could not write $key into $SRC"
    else
        printf '\n%s=%s\n' "$key" "$quoted" >> "$SRC"
    fi
    FILLED="$FILLED $key"
}
given() {  # given KEY: a value passed on this command line is written, replacing what the file has
    local key="$1" value="${!1:-}" quoted escaped
    [[ -n "$value" ]] || return 0
    [[ "$(val_in "$SRC" "$key")" == "$value" ]] && return 0
    quoted="$value"; [[ "$value" =~ [^A-Za-z0-9_./:-] ]] && quoted="\"$value\""
    escaped="$(printf '%s' "$quoted" | sed 's/[&|\\]/\\&/g')"
    if grep -qE "^# ?$key=" "$SRC" && ! grep -qE "^$key=" "$SRC"; then
        edit_in_place "$SRC" -E "s|^# ?$key=.*|$key=$escaped|" || die "could not write $key into $SRC"
    elif grep -qE "^$key=" "$SRC"; then
        edit_in_place "$SRC" -E "s|^$key=.*|$key=$escaped|" || die "could not write $key into $SRC"
    else
        printf '\n%s=%s\n' "$key" "$quoted" >> "$SRC"
    fi
    FILLED="$FILLED $key"
}
FILLED=""
if [[ "$LIVE" == true ]]; then
    # The deployer key is filled only into a file that has none: it owns the
    # extension, the contracts and the funds, and a file with another key is a
    # different deployment, which this switch must not quietly replace.
    HAVE_KEY="$(val_in "$SRC" EXTENSION_OWNER_KEY)"
    if [[ -n "${EXTENSION_OWNER_KEY:-}" && -n "$HAVE_KEY" && "0x${EXTENSION_OWNER_KEY#0x}" != "0x${HAVE_KEY#0x}" ]]; then
        die ".env.$CHAIN already holds a different EXTENSION_OWNER_KEY; it is not replaced from the command line — edit the file, or move it aside for a new deployment"
    fi
    fill EXTENSION_OWNER_KEY "${EXTENSION_OWNER_KEY:-}"
    # What a machine's placement decides, given on the call: the public URL the
    # machine is registered under, the /direct key its proxy holds (the laptop
    # stack's is generated below; a remote proxy's is the operator's), and
    # whether it attests for real.
    given EXT_PROXY_URL
    given DIRECT_API_KEY
    given SIMULATED_TEE
    # The machines' owner is a role of its own wherever the extension owner is
    # not an allowed machine owner, so its key is written like the rest rather
    # than edited in by hand afterwards.
    given MACHINE_OWNER_KEY
    DEPLOYER_KEY="$(val_in "$SRC" EXTENSION_OWNER_KEY)"
    if [[ -n "$DEPLOYER_KEY" ]]; then
        command -v cast >/dev/null || die "cast (Foundry) is required to derive the owner and generate the keys"
        DEPLOYER_KEY="0x${DEPLOYER_KEY#0x}"
        [[ "$DEPLOYER_KEY" =~ ^0x[0-9a-fA-F]{64}$ ]] || die "EXTENSION_OWNER_KEY in .env.$CHAIN is not a 32-byte hex key"
        fill INITIAL_OWNER "$(cast wallet address --private-key "$DEPLOYER_KEY")"
        fill PROXY_PRIVATE_KEY "$(cast wallet new | awk '/^Private key:/{print $3}')"
        fill DIRECT_API_KEY "$(openssl rand -hex 32)"
        # A chain that already has an admins file has admins of its own, holding
        # their own keys. Generating provisioning keys here would write keys into
        # .env that sign nothing the machines accept — the deployer is not one of
        # them, and a fresh key is nobody's.
        if [[ ! -f "$PROJECT_DIR/deploy/config/admins/$CHAIN.json" ]]; then
            fill ADMIN_KEY "$DEPLOYER_KEY"
            fill ADMIN_KEYS "$DEPLOYER_KEY,$(cast wallet new | awk '/^Private key:/{print $3}')"
        fi
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
edit_in_place "$DST" -E 's/^CHAIN=/FLARE_CHAIN=/' || die "could not pin FLARE_CHAIN in $DST"
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
    OWNER_ADDR="$(val_in "$SRC" INITIAL_OWNER)"; SECOND_KEY="$(val_in "$SRC" ADMIN_KEYS | cut -d, -f2)"
    if [[ -n "$OWNER_ADDR" && -n "$SECOND_KEY" ]]; then
        SECOND_ADDR="$(cast wallet address --private-key "$SECOND_KEY")"
        jq -n --arg d "$OWNER_ADDR" --arg s "$SECOND_ADDR" \
            '{backing: {threshold: 1, admins: [$d]}, providers: {threshold: 2, admins: [$d, $s]}}' > "$ADMINS"
        log "wrote ${ADMINS#"$PROJECT_DIR"/}: backing [$OWNER_ADDR], providers [$OWNER_ADDR, $SECOND_ADDR]"
    fi
elif [[ "$LIVE" == true && -f "$ADMINS" ]]; then
    OWNER_ADDR="$(val_in "$SRC" INITIAL_OWNER | tr '[:upper:]' '[:lower:]')"
    IS_BACKING="$(jq -r --arg o "$OWNER_ADDR" '[.backing.admins[]? | ascii_downcase] | index($o) != null' "$ADMINS" 2>/dev/null || echo false)"
    if git -C "$PROJECT_DIR" ls-files --error-unmatch "${ADMINS#"$PROJECT_DIR"/}" >/dev/null 2>&1; then
        # A tracked admins file is a deployment's published set, whose admins
        # hold their own keys; nothing here generates or replaces it.
        log "${ADMINS#"$PROJECT_DIR"/} is this chain's published admin set: its admins sign uploads with their own keys (ADMIN_KEY / ADMIN_KEYS)"
    elif [[ -n "$OWNER_ADDR" && "$IS_BACKING" != true ]]; then
        # An admin set left by another deployer of a test deployment would have
        # the machine refuse this one's uploads; said here, not at the first
        # rejected credential.
        log "WARN: ${ADMINS#"$PROJECT_DIR"/} does not name this deployer ($OWNER_ADDR) as a backing admin; delete the file to have it rewritten, or edit it"
    fi
fi

# --- Summary ---

key_val=$(get_val EXTENSION_OWNER_KEY)
log "  CHAIN_URL              = $(v=$(get_val CHAIN_URL);        echo "${v:-<unset>}")"
log "  EXT_PROXY_URL          = $(v=$(get_val EXT_PROXY_URL);    echo "${v:-<unset: the machine public URL, registered on chain by post-build.sh, which refuses to guess it>}")"
log "  NORMAL_PROXY_URL       = $(v=$(get_val NORMAL_PROXY_URL); echo "${v:-<unset>}")"
log "  INITIAL_OWNER          = $(v=$(get_val INITIAL_OWNER);    echo "${v:-<unset>}")"
log "  EXTENSION_OWNER_KEY    = $([[ -n "$key_val" ]] && echo "<set>" || echo "<unset — the funded key: EXTENSION_OWNER_KEY=0x… $SCRIPT_NAME $CHAIN>")"
if [[ "$LIVE" == true ]]; then
    log "  PROXY_PRIVATE_KEY      = $([[ -n "$(get_val PROXY_PRIVATE_KEY)" ]] && echo "<set>" || echo "<unset>")"
    log "  DIRECT_API_KEY         = $([[ -n "$(get_val DIRECT_API_KEY)" ]] && echo "<set>" || echo "<unset>")"
    log "  EXTENSION_ID           = $(v=$(get_val EXTENSION_ID); echo "${v:-<unset>}")"
    log "  SIMULATED_TEE          = $(v=$(get_val SIMULATED_TEE); echo "${v:-<unset>}")"
    log "  MACHINE_OWNER_KEY      = $([[ -n "$(get_val MACHINE_OWNER_KEY)" ]] && echo "<set>" || echo "<unset — the extension owner registers the machines>")"
fi
log "  HEX_API_KEY            = $([[ -n "$(get_val HEX_API_KEY)" ]]   && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_URL           = $([[ -n "$(get_val HEX_CASH_URL)" ]]  && echo "<set>" || echo "<unset — cash-less>")"
log "  HEX_CASH_SALT          = $([[ -n "$(get_val HEX_CASH_SALT)" ]] && echo "<set>" || echo "<unset — publish-local.sh generates it>")"
