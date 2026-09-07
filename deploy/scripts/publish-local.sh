#!/usr/bin/env bash
# publish-local.sh — publish SET_ENDPOINTS or SET_ADMINS through the local
# simulation's governance timelock, end to end.
#
# On a sender governed by its deployer, as deploy-oracle-local.sh leaves the
# simulation's, a publication is one transaction from the tools' key, and this
# script only runs the tool. On a production-mode sender both calls are
# governance-only, so a publication is two transactions an hour apart: governance RECORDS the call (the sender is in
# production mode and timelocks it), and once the timelock has passed the
# executor EXECUTES it with the publication fee attached. send-endpoints /
# send-admins produce the exact calldata and fee (-out); this script records it
# and saves the hand-off with its release time under out/publications/. Run it
# again with --execute <file> after that time, or pass --wait to keep polling the
# chain until the timelock has passed and then execute in the same run. Either
# way it finishes by waiting for the machine to report the new hash, which proves
# the relay delivered the instruction and the extension applied it.
#
# The simulation's clock and governance parameters are never touched: the proxy
# rejects an instruction whose event block is ahead of wall time, and Hardhat
# cannot rewind, so a clock jump would end delivery for the rest of the
# simulation's life. The hour is GovernanceSettings' genesis timelock in the
# simulation; a shorter one is the simulation's own configuration to make.
#
# Reads .env (activate .env.local first): CHAIN_URL, ADDRESSES_FILE, EXT_PROXY_URL,
# LOCAL_GOVERNANCE_KEY. Needs cast, jq, python3, go.
#
# Usage:
#   ./deploy/scripts/publish-local.sh endpoints [config] [--wait]
#   ./deploy/scripts/publish-local.sh admins    [config] [--wait]
#   ./deploy/scripts/publish-local.sh endpoints|admins --execute out/publications/<file>.json
#
# The sender dispatches to every machine active on the extension when the call
# executes; the hand-off lists the machines it quoted the fee for.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
# The environment outranks .env for the proxy URL: a host-process stack serves 6664
# while .env.local names the compose stack's port.
PRESET_EXT_PROXY_URL="${EXT_PROXY_URL:-}"
[[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }
[[ -n "$PRESET_EXT_PROXY_URL" ]] && EXT_PROXY_URL="$PRESET_EXT_PROXY_URL"

GREEN='\033[0;32m'; RED='\033[0;31m'; CYAN='\033[0;36m'; NC='\033[0m'
log() { echo -e "${GREEN}[publish-local]${NC} $*"; }
die() { echo -e "${RED}[publish-local] ERROR:${NC} $*" >&2; exit 1; }
lower() { tr "[:upper:]" "[:lower:]" <<<"$1"; }  # bash 3 has no ${var,,}
section() { echo -e "\n${CYAN}=== $* ===${NC}"; }

KIND="${1:-}"; shift || true
case "$KIND" in
    endpoints) TOOL=send-endpoints; DEFAULT_CONFIG="deploy/config/endpoints/public-only.json"; HASH_FIELD=endpointsHash ;;
    admins)    TOOL=send-admins;    DEFAULT_CONFIG="deploy/config/admins/dev-wellknown-keys.json"; HASH_FIELD=adminsHash ;;
    *) die "usage: $0 endpoints|admins [config] [--wait] | --execute <handoff.json>" ;;
esac
CONFIG=""; WAIT=0; EXEC_FILE=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --wait) WAIT=1; shift ;;
        --execute) [[ $# -ge 2 ]] || die "--execute needs the saved hand-off file"; EXEC_FILE="$2"; shift 2 ;;
        -*) die "unknown argument: $1" ;;
        *) CONFIG="$1"; shift ;;
    esac
done
CONFIG="${CONFIG:-$PROJECT_DIR/$DEFAULT_CONFIG}"
[[ "$CONFIG" != /* ]] && CONFIG="$PROJECT_DIR/$CONFIG"
[[ -f "$CONFIG" ]] || die "no such config: $CONFIG"

for t in cast jq python3 go; do command -v "$t" >/dev/null || die "$t is required"; done
CHAIN_URL="${CHAIN_URL:-http://127.0.0.1:8545}"
EXT_PROXY_URL="${EXT_PROXY_URL:-http://localhost:6674}"
GOV_KEY="${LOCAL_GOVERNANCE_KEY:-}"; [[ -n "$GOV_KEY" ]] || die "LOCAL_GOVERNANCE_KEY is not set — activate .env.local"
ADDR_FILE="${ADDRESSES_FILE:-deploy/config/local/deployed-addresses.json}"
[[ "$ADDR_FILE" != /* ]] && ADDR_FILE="$PROJECT_DIR/$ADDR_FILE"
[[ -f "$ADDR_FILE" ]] || die "addresses file not found: $ADDR_FILE — run deploy-oracle-local.sh first"
addr() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0] // empty' "$ADDR_FILE"; }
GS="$(addr GovernanceSettings)"; GS="${GS:-0x1000000000000000000000000000000000000007}"
GOV_ADDR="$(cast wallet address --private-key "$GOV_KEY")"

# The sim's Hardhat rejects cast's call objects, which carry both `input` and
# `data`, so reads go through a hand-built eth_call and sends carry an explicit
# gas limit, which keeps cast from asking the node to estimate.
eth_call() {  # eth_call <to> <sig> [args...] → raw hex return data; sets CALL_ERR on revert
    local to="$1" sig="$2"; shift 2
    local data; data="$(cast calldata "$sig" "$@")"
    CALL_ERR=""
    local out
    out="$(cast rpc eth_call "{\"from\":\"$GOV_ADDR\",\"to\":\"$to\",\"data\":\"$data\"}" latest --rpc-url "$CHAIN_URL" 2>&1 || true)"
    if [[ "$out" == '"0x'* ]]; then echo "${out//\"/}"; else CALL_ERR="$out"; echo ""; fi
}
GAS_LIMIT="${GAS_LIMIT:-3000000}"

# Account 0 is the simulation's busiest sender, so a transaction from it can lose
# the nonce it was built with. cast picks the nonce up again on every attempt.
send_retry() {  # send_retry <key> <cast send args...>
    local key="$1"; shift
    local attempt out
    for attempt in 1 2 3 4 5; do
        if out="$(cast send "$@" --rpc-url "$CHAIN_URL" --private-key "$key" --gas-limit "$GAS_LIMIT" --json 2>&1)"; then
            # An explicit gas limit skips estimation, so a revert surfaces only in the receipt.
            [[ "$(jq -r .status <<<"$out" 2>/dev/null)" == "0x1" ]] || { echo "reverted: $(jq -c '{transactionHash, status}' <<<"$out" 2>/dev/null || echo "$out")"; return 1; }
            echo "$out"; return 0
        fi
        grep -qiE 'nonce|replacement|underpriced|already known' <<<"$out" || { echo "$out"; return 1; }
        sleep 1
    done
    echo "$out"; return 1
}

SENDER_ADDR="$(addr TeeOracleInstructionsSender)"; [[ -n "$SENDER_ADDR" ]] || die "no TeeOracleInstructionsSender in $ADDR_FILE — run deploy-oracle-local.sh first"
if [[ -z "$EXEC_FILE" && "$(eth_call "$SENDER_ADDR" 'productionMode()')" == *0 ]]; then
    section "Direct publication"
    # deploy-oracle-local.sh leaves the simulation's sender governed by its
    # deployer, the key the tools sign with, so the publication executes in the
    # transaction that submits it: no recording, no timelock, no executor. The
    # tool confirms delivery over the machine's readiness query; the local proxy
    # accepts any /direct key (api_key_optional), hence the placeholder.
    ( cd "$PROJECT_DIR/tools" && go run "./cmd/$TOOL" -a "$ADDR_FILE" -c "$CHAIN_URL" -p "$EXT_PROXY_URL" -config "$CONFIG" -directApiKey "${DIRECT_API_KEY:-local}" ) \
        || die "$TOOL failed"
    exit 0
fi

when() { python3 -c 'import datetime, sys; print(datetime.datetime.fromtimestamp(int(sys.argv[1])).strftime("%Y-%m-%d %H:%M:%S"))' "$1"; }
chain_now() { cast block latest --field timestamp --rpc-url "$CHAIN_URL"; }

if [[ -z "$EXEC_FILE" ]]; then
    section "Hand-off from $TOOL"
    # The executor below is the governance key, so it is the payer of record the
    # sender writes into the dispatch.
    WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
    ( cd "$PROJECT_DIR/tools" && go run "./cmd/$TOOL" -a "$ADDR_FILE" -c "$CHAIN_URL" -config "$CONFIG" -claimBack "$GOV_ADDR" -out "$WORK/handoff.json" ) \
        || die "$TOOL could not build the publication"
    log "the sender will dispatch to: $(jq -r '.teeIds | join(", ")' "$WORK/handoff.json")"
    TARGET="$(jq -r .target "$WORK/handoff.json")"
    CALLDATA="$(jq -r .calldata "$WORK/handoff.json")"
    VALUE="$(jq -r .valueWei "$WORK/handoff.json")"
    EXPECT="$(jq -r .payloadHash "$WORK/handoff.json")"
    VERSION="$(jq -r .predictedVersion "$WORK/handoff.json")"
    log "target $TARGET  version $VERSION  fee ${VALUE} wei"
    log "payload hash $EXPECT"

    section "Governance records the call"
    # Production mode: the sender timelocks a governance call instead of executing
    # it. Value is refused at this step; the executor attaches it. In production
    # mode the sender's governance is GovernanceSettings' governance address.
    SENDER_GOV="$(cast abi-decode 'f()(address)' "$(eth_call "$TARGET" 'governance()')" 2>/dev/null || true)"
    [[ -z "$SENDER_GOV" || "$(lower "$SENDER_GOV")" == "$(lower "$GOV_ADDR")" ]] \
        || die "the sender's governance is $SENDER_GOV, not LOCAL_GOVERNANCE_KEY ($GOV_ADDR)"
    RECORD_KEY="$GOV_KEY"
    OUT="$(send_retry "$RECORD_KEY" "$TARGET" "$CALLDATA")" || die "recording reverted:\n$OUT"
    REC_TX="$(jq -r .transactionHash <<<"$OUT")"
    REC_TS="$(cast block "$(jq -r .blockNumber <<<"$OUT")" --field timestamp --rpc-url "$CHAIN_URL")"
    TIMELOCK="$(cast --to-dec "$(eth_call "$GS" 'getTimelock()')")"
    [[ "$TIMELOCK" =~ ^[0-9]+$ ]] || die "reading the timelock from $GS: $CALL_ERR"
    ALLOWED=$(( REC_TS + TIMELOCK ))
    log "recorded in tx $REC_TX"

    # The hand-off outlives this run: the executor needs the exact calldata, and
    # the release time says when it may send it.
    SAVED="$PROJECT_DIR/out/publications/$KIND-v$VERSION-${EXPECT:2:8}.json"
    mkdir -p "$(dirname "$SAVED")"
    jq --argjson a "$ALLOWED" --arg tx "$REC_TX" '. + {allowedAfter: $a, recordedIn: $tx}' "$WORK/handoff.json" > "$SAVED"
    log "executable after $(when "$ALLOWED") (timelock ${TIMELOCK}s); hand-off saved to ${SAVED#$PROJECT_DIR/}"
    if (( ! WAIT )); then
        echo -e "\n  Then: $0 $KIND --execute ${SAVED#$PROJECT_DIR/}\n  (or rerun with --wait to poll until then and execute in one go)"
        exit 0
    fi
else
    section "Saved hand-off"
    [[ "$EXEC_FILE" != /* ]] && EXEC_FILE="$PROJECT_DIR/$EXEC_FILE"
    [[ -f "$EXEC_FILE" ]] || die "no such hand-off: $EXEC_FILE"
    TARGET="$(jq -r .target "$EXEC_FILE")"
    CALLDATA="$(jq -r .calldata "$EXEC_FILE")"
    VALUE="$(jq -r .valueWei "$EXEC_FILE")"
    EXPECT="$(jq -r .payloadHash "$EXEC_FILE")"
    ALLOWED="$(jq -r '.allowedAfter // empty' "$EXEC_FILE")"
    [[ "$ALLOWED" =~ ^[0-9]+$ ]] || die "$EXEC_FILE carries no release time; it was not saved by this script's record step"
    log "target $TARGET  version $(jq -r .predictedVersion "$EXEC_FILE")  fee ${VALUE} wei  recorded in $(jq -r '.recordedIn // "?"' "$EXEC_FILE")"
fi

section "Timelock"
# Chain time, not wall time: the sender compares block.timestamp with the release
# time, and the two can differ. The simulation mines continuously, so polling the
# latest block tracks it closely.
while :; do
    NOW="$(chain_now)"
    (( NOW >= ALLOWED )) && break
    log "chain time $(when "$NOW"); executable at $(when "$ALLOWED"), $(( (ALLOWED - NOW + 59) / 60 )) min to go"
    sleep 30
done
log "timelock passed"

section "Executor executes it, fee attached"
[[ "$(eth_call "$GS" 'isExecutor(address)' "$GOV_ADDR")" == *1 ]] \
    || die "$GOV_ADDR is not a GovernanceSettings executor"
OUT="$(send_retry "$GOV_KEY" "$TARGET" 'executeGovernanceCall(bytes)' "$CALLDATA" --value "$VALUE")" || die "execution reverted:\n$OUT"
log "executed in tx $(jq -r .transactionHash <<<"$OUT") — the instruction is dispatched"

section "Delivery"
# The proxy's /info carries the extension's GET /state body as hex; the readiness
# block in it is what the machine says it is running.
DEADLINE=$(( $(date +%s) + 120 ))
while :; do
    GOT="$(curl -sf -m 5 "$EXT_PROXY_URL/info" 2>/dev/null | HASH_FIELD="$HASH_FIELD" python3 -c '
import json, os, sys
try:
    info = json.load(sys.stdin)
    raw = bytes.fromhex(info["teeInfo"]["state"]["state"][2:])
    print(json.loads(raw)["state"]["readiness"][os.environ["HASH_FIELD"]])
except Exception:
    print("")' || true)"
    if [[ "$(lower "$GOT")" == "$(lower "$EXPECT")" ]]; then
        echo -e "  ${GREEN}the machine reports $HASH_FIELD = $EXPECT${NC}"
        break
    fi
    (( $(date +%s) >= DEADLINE )) && die "the machine still reports $HASH_FIELD=${GOT:-?} after 120s — is the e2e relay running, and was the machine registered with this proxy's URL?"
    echo "  waiting for the relay to deliver (machine reports ${GOT:-nothing yet})"
    sleep 5
done
