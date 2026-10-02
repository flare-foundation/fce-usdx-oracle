#!/usr/bin/env bash
#
# upload-hex-key.sh — hand the Hex backing credentials to one TEE machine.
#
# Run by the party that holds the credentials. Three steps:
#
#   1. verify the machine is running a build you accept, and read its attested
#      public key                                   (on-chain, not self-reported)
#   2. for each credential: encrypt it to that key, sign the envelope with every
#      backing admin key held here, submit it over the direct channel and wait
#      for the machine's own verdict                (ECIES; plaintext never leaves this shell)
#   3. confirm from the machine's readiness report that nothing is still missing
#
# Two credentials fall under the backing set, and the machine uses the cash
# source only once it holds both:
#
#   hex_api_key    the Hex API key, from HEX_API_KEY (SECRET_VALUE is accepted too)
#   hex_cash_url   the restricted cash URL, from HEX_CASH_URL and HEX_CASH_SALT. Uploaded
#                  only when HEX_CASH_URL is set, and it must be the URL the published
#                  SET_ENDPOINTS committed to — publish-local.sh commits from the same
#                  variables, so on the simulation the two agree by construction.
#
# An envelope needs as many distinct backing admins' signatures as the backing
# set's threshold: 1 on the simulation and on the test deployments use-chain.sh
# sets up, more on a production deployment (Songbird's set is 2-of-3). The run
# signs with every key in ADMIN_KEYS, or with ADMIN_KEY alone, and submits once
# they reach the threshold. Holding fewer distinct keys than the threshold, it
# stops before building anything, since nothing it built could be submitted.
#
# The party holding the credentials need not be a backing admin. With
# --build-only the run stops after step 2's encryption: it writes each envelope
# out unsigned, reads no admin key — not even one set in .env — and never
# contacts the direct channel. The backing admins sign what it writes, and
# whoever holds the proxy API key submits it.
#
# Nothing here touches the chain except to READ it. The credentials are never in a
# transaction, never on disk in plaintext, and never in argv — every secret is
# passed to the tools through the environment, because argv is readable by any
# other process on this machine via `ps`.
#
# Usage:
#   ./deploy/scripts/upload-hex-key.sh
#   ./deploy/scripts/upload-hex-key.sh --threshold 2  # match a 2-of-N backing set
#   ./deploy/scripts/upload-hex-key.sh --no-verify    # mocknode only — see step 1
#   ./deploy/scripts/upload-hex-key.sh --build-only   # encrypt and hand off; sign and submit nothing
#
# Configuration is read from .env and the recorded local stack, then from the
# environment; anything still unset and required is prompted for. Secrets are
# ONLY ever taken from the environment or a hidden prompt, never from a
# command-line argument:
#
#   PROXY_URL          extension proxy base URL       (default: EXT_PROXY_URL, the recorded stack's)
#   CHAIN_ID           target chain id                (default: CHAIN_ID from .env)
#   EXPECTED_CODE_HASH optional: pins the upload to one build on top of the registry's
#                      acceptance (a SIMULATED_TEE=true machine defaults to tee-node's test constant)
#   ADMIN_KEYS         backing admin private keys held HERE, hex, comma-separated
#                      (ADMIN_KEY is accepted as the single-key form; default on the
#                      simulation: the dev backing admin); ignored with --build-only
#   BACKING_THRESHOLD  signatures the backing set needs (default: backing.threshold in
#                      deploy/config/admins/<chain>.json, else 1); --threshold overrides it
#   DIRECT_API_KEY     proxy API key for the direct channel
#   HEX_API_KEY        the Hex API key itself
#   HEX_CASH_URL       the restricted cash URL, https  (optional; enables the cash source)
#   HEX_CASH_SALT      32-byte hex salt of its commitment (required with HEX_CASH_URL)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROJECT_DIR="$REPO_ROOT"
source "$REPO_ROOT/deploy/scripts/lib/chains.sh"
source "$REPO_ROOT/deploy/scripts/lib/env.sh"; load_env

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }
step() { printf '\n--- %s ---------------------------------------------------\n\n' "$1"; }

VERIFY=true; BUILD_ONLY=false; THRESHOLD_ARG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --no-verify) VERIFY=false; shift ;;
    --build-only) BUILD_ONLY=true; shift ;;
    --threshold) [ $# -ge 2 ] || die "--threshold needs a number"
                 THRESHOLD_ARG="$2"; shift 2 ;;
    -h|--help) awk 'NR>2 && /^#/ {sub(/^# ?/, ""); print; next} NR>2 {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "unknown argument: $1 (try --help)" ;;
  esac
done

command -v jq >/dev/null 2>&1 || die "jq is required"

# The envelopes hold only ciphertext, but they are still removed on every exit
# path rather than left in a working directory someone later tars up.
WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

# ask reads a visible value; ask_secret reads a hidden one. Both leave the value
# in a variable and never in argv or the shell history.
ask() {
  local var="$1" prompt="$2" default="${3:-}"
  if [ -n "${!var:-}" ]; then return; fi
  local reply
  if [ -n "$default" ]; then
    read -r -p "$prompt [$default]: " reply
    reply="${reply:-$default}"
  else
    read -r -p "$prompt: " reply
  fi
  [ -n "$reply" ] || die "$var is required"
  printf -v "$var" '%s' "$reply"
  export "${var?}"
}

ask_secret() {
  local var="$1" prompt="$2"
  if [ -n "${!var:-}" ]; then return; fi
  local reply
  read -r -s -p "$prompt: " reply
  echo
  [ -n "$reply" ] || die "$var is required"
  printf -v "$var" '%s' "$reply"
  export "${var?}"
}

# A recorded local stack (deploy/config/stack.env) is the simulation: its proxy
# accepts any /direct key, its machine is registered under the simulated code
# hash, and its backing admin is the first well-known dev key
# (deploy/config/admins/dev-wellknown-keys.json). Those default here so the run
# is unattended; a testnet has none of them and prompts for what .env lacks.
LOCAL_SIM=false
[ -n "${STACK:-}" ] && LOCAL_SIM=true
if [ "$LOCAL_SIM" = true ]; then
  : "${DIRECT_API_KEY:=local}"
  : "${ADMIN_KEY:=0x0000000000000000000000000000000000000000000000000000000000000001}"
fi
: "${SECRET_VALUE:=${HEX_API_KEY:-}}"
: "${PROXY_URL:=${EXT_PROXY_URL:-}}"
export DIRECT_API_KEY ADMIN_KEY SECRET_VALUE PROXY_URL

echo "=== USDX oracle — upload the Hex backing credentials ==="

ask PROXY_URL          "Extension proxy URL"    "http://localhost:6674"
# The chain id is the active chain's (lib/chains.sh); asked only when .env names
# no chain at all.
: "${CHAIN_ID:=$(chain_id "$(resolve_chain "")" 2>/dev/null || true)}"
ask CHAIN_ID           "Chain id"
# A machine run with SIMULATED_TEE=true attests with tee-node's fixed test code
# hash rather than a build's, so when .env says the machine is simulated and
# names no hash, that constant is what the check can expect. A real Confidential
# Space machine has a hash of its own, obtained out of band, and is asked for.
SIMULATED_CODE_HASH=0x194844cf417dde867073e5ab7199fa4d21fd82b5dbe2bdea8b3d7fc18d10fdc2
if [ "${SIMULATED_TEE:-}" = true ] && [ -z "${EXPECTED_CODE_HASH:-}" ]; then
  EXPECTED_CODE_HASH="$SIMULATED_CODE_HASH"
  echo "expected code hash: tee-node's simulated-machine constant (SIMULATED_TEE=true in .env)"
fi
# The machine's build is vouched for by the registry: the check below refuses a
# machine whose code hash the extension has not accepted, and post-build.sh
# accepts only the hash given to it. EXPECTED_CODE_HASH pins the upload to that
# exact build on top; it is optional.

# How many distinct backing admins must sign. The machine holds the real
# threshold, published with SET_ADMINS; this run's copy comes from --threshold,
# BACKING_THRESHOLD, or the admins file that publication is made from, and is 1,
# the floor, when none names one. Like PROVIDER_THRESHOLD in upload-rpc-keys.sh,
# it says when to stop collecting signatures, never that the machine will accept.
ADMINS_FILE="$REPO_ROOT/deploy/config/admins/$(resolve_chain "").json"
if [ -n "$THRESHOLD_ARG" ]; then
  BACKING_THRESHOLD="$THRESHOLD_ARG"; THRESHOLD_FROM="--threshold"
elif [ -n "${BACKING_THRESHOLD:-}" ]; then
  THRESHOLD_FROM="BACKING_THRESHOLD"
elif [ -f "$ADMINS_FILE" ] && BACKING_THRESHOLD="$(jq -er '.backing.threshold' "$ADMINS_FILE" 2>/dev/null)"; then
  THRESHOLD_FROM="${ADMINS_FILE#"$REPO_ROOT"/}"
else
  BACKING_THRESHOLD=1; THRESHOLD_FROM="the backing set's floor"
fi
case "$BACKING_THRESHOLD" in
  ''|*[!0-9]*) die "the backing threshold must be a positive whole number, not '$BACKING_THRESHOLD' ($THRESHOLD_FROM)" ;;
esac
[ "$BACKING_THRESHOLD" -ge 1 ] || die "the backing threshold must be at least 1 ($THRESHOLD_FROM)"

# A build-only run reads neither keys nor the proxy API key: it signs nothing
# and sends nothing, so it asks for nothing it would not use.
KEYS=()
if [ "$BUILD_ONLY" = true ]; then
  # Emptied rather than left unread: .env may name admin keys, and a build-only
  # run must not sign with them just because they happen to be there.
  unset ADMIN_KEY ADMIN_KEYS
else
  # ADMIN_KEY is what send-secret itself reads, so a single-key operator can use
  # the same variable for both upload scripts.
  if [ -z "${ADMIN_KEYS:-}" ]; then
    if [ -n "${ADMIN_KEY:-}" ]; then
      ADMIN_KEYS="$ADMIN_KEY"
    else
      ask_secret ADMIN_KEYS "Backing admin private key(s), comma-separated (hidden)"
    fi
  fi
  OLD_IFS="$IFS"; IFS=','; read -r -a KEYS <<< "$ADMIN_KEYS"; IFS="$OLD_IFS"

  # Distinct signers, not keys as typed: one key written twice, with and without
  # its 0x prefix or in another case, is one signer, and the machine counts
  # signers. Refused here, before anything is built, because an envelope that
  # cannot reach the threshold can never be submitted.
  distinct="$(for key in "${KEYS[@]:-}"; do
      key="$(printf '%s' "$key" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')"
      key="${key#0x}"; key="$(printf '%s' "$key" | sed 's/^0*//')"
      if [ -n "$key" ]; then printf '%s\n' "$key"; fi
    done | sort -u | wc -l | tr -d ' ')"
  if [ "$distinct" -lt "$BACKING_THRESHOLD" ]; then
    die "this run holds $distinct distinct backing admin key(s), and the backing set needs $BACKING_THRESHOLD signatures ($THRESHOLD_FROM). Nothing was built or sent.
       Give every key held here in ADMIN_KEYS (comma-separated), or run with --build-only
       and have $BACKING_THRESHOLD backing admins sign each envelope it writes before it is submitted."
  fi
  ask_secret DIRECT_API_KEY "Proxy API key (hidden)"
fi
ask_secret SECRET_VALUE   "Hex API key (hidden)"

# Named by machine as well as ref: a build-only run for each machine in turn
# must not overwrite the envelope an earlier one left waiting for its signers.
# RUN_TEE is the machine the envelopes are bound to, read back from the first.
OUT_DIR="${OUT_DIR:-$REPO_ROOT/deploy/envelopes}"
RUN_TEE=""
handoff_file() { printf '%s/%s-%s.json' "$OUT_DIR" "$RUN_TEE" "$1"; }

# The URL goes first: the machine accepts the blob only against the published
# commitment, so a mismatch surfaces before the key is spent on a machine that
# cannot use it.
if [ -n "${HEX_CASH_URL:-}" ]; then
  [ -n "${HEX_CASH_SALT:-}" ] || die "HEX_CASH_URL is set but HEX_CASH_SALT is not — the salt the published commitment used is required (publish-local.sh records it in .env)"
  REFS=(hex_cash_url hex_api_key)
else
  REFS=(hex_api_key)
fi

echo
echo "machine   $PROXY_URL  (chain $CHAIN_ID)"
echo "uploads   ${REFS[*]}"
echo "signers   $BACKING_THRESHOLD of the backing set ($THRESHOLD_FROM)$([ "$BUILD_ONLY" = true ] || printf '; %s distinct key(s) held here' "$distinct")"
if [ ${#REFS[@]} -eq 1 ]; then
  echo "          HEX_CASH_URL is not set, so only the key goes up and the machine keeps running cash-less"
fi

# Strictly increasing per ref. Wall-clock seconds always increase, which is what
# the enclave requires, and it stays correct across a restart that resets its
# in-memory counter to zero.
VERSION="${VERSION:-$(date +%s)}"

( cd "$REPO_ROOT/tools" && go build -o "$WORKDIR/send-secret" ./cmd/send-secret ) \
  || die "could not build send-secret"

step "1/3  Verifying the machine"
# Gating step, and the source of the teeId step 2 pins the encryption to. If
# this fails, the key on the next line may not belong to an enclave at all, so
# nothing after it runs. On the simulation no code hash is expected: the check
# still derives the teeId from the attestation and confirms the registration.
ATTESTED_TEE_ID=""
if [ "$VERIFY" = true ]; then
  # -printTeeId puts the ATTESTED teeId on stdout and the human report on
  # stderr, so the operator still sees every check while the id is captured.
  #
  # Capturing it matters because this command and the encryption below are
  # separate processes making separate requests to the same proxy. Verifying
  # here proves something about the key THIS request received; passing the id to
  # build is what makes build refuse a different key on its own request.
  ATTESTED_TEE_ID="$( cd "$REPO_ROOT/tools" && go run ./cmd/verify-tee \
      -p "$PROXY_URL" ${EXTENSION_ID:+-extensionId "$EXTENSION_ID"} \
      ${EXPECTED_CODE_HASH:+-codeHash "$EXPECTED_CODE_HASH"} -printTeeId )" \
    || die "verification failed — nothing was sent"
  [ -n "$ATTESTED_TEE_ID" ] \
    || die "verification passed but printed no teeId — nothing was sent"
else
  echo "  SKIPPED (--no-verify). Nothing here has checked that the key these"
  echo "  credentials are encrypted to belongs to an enclave at all, and with no"
  echo "  attested teeId the encryption below cannot be pinned to one either."
  echo "  Only ever do this against a mocknode."
fi

n=0
for REF in "${REFS[@]}"; do
  n=$((n + 1))
  envelope="$WORKDIR/$REF.json"
  if [ "$BUILD_ONLY" = true ]; then
    step "2/3  Building $REF ($n of ${#REFS[@]})"
  else
    step "2/3  Uploading $REF ($n of ${#REFS[@]})"
  fi

  # build reads the public key from /info and encrypts the credential to it. -tee
  # is what ties that key to the attestation checked above: build refuses outright
  # if what it is served derives to a different machine. The expansion is
  # unquoted-by-design so an empty value passes no flag at all (--no-verify); a
  # teeId is a hex address, so word splitting has nothing to split.
  #
  # The plaintext exists only in this shell and inside the target enclave. For
  # hex_cash_url the value is derived from HEX_CASH_URL and HEX_CASH_SALT, so the
  # API key in SECRET_VALUE is cleared for that one build.
  echo "» build"
  value_env=""
  [ "$REF" = hex_cash_url ] && value_env="SECRET_VALUE="
  ( cd "$REPO_ROOT" && env $value_env "$WORKDIR/send-secret" build \
      -p "$PROXY_URL" -ref "$REF" \
      -version "$VERSION" ${ATTESTED_TEE_ID:+-tee "$ATTESTED_TEE_ID"} \
      -out "$envelope" ) \
    || die "could not build the envelope for $REF"
  RUN_TEE="$(jq -r '.teeId' "$envelope")"

  if [ "$BUILD_ONLY" = true ]; then
    mkdir -p "$OUT_DIR"
    cp "$envelope" "$(handoff_file "$REF")"
    echo
    echo "  kept at $(handoff_file "$REF")"
    continue
  fi

  # Every key held here signs: the backing set governs both refs, and the
  # envelope needs BACKING_THRESHOLD distinct backing admins. send-secret refuses
  # a duplicate signer, so a key that already signed is a warning rather than a
  # second useless signature. The envelope is ciphertext plus signatures, so one
  # that falls short is kept for the other backing admins to sign.
  echo
  echo "» sign"
  for key in "${KEYS[@]:-}"; do
    [ -n "$key" ] || continue
    ADMIN_KEY="$key" "$WORKDIR/send-secret" sign -in "$envelope" \
      || echo "  (that key did not add a signature)"
  done
  sigs="$(jq -r '.signatures | length' "$envelope")"
  if [ "$sigs" -lt "$BACKING_THRESHOLD" ]; then
    mkdir -p "$OUT_DIR"
    cp "$envelope" "$(handoff_file "$REF")"
    die "$REF carries $sigs signature(s), and the backing set needs $BACKING_THRESHOLD; nothing was submitted.
       The envelope is kept at $(handoff_file "$REF") for the other backing admins to sign
       (cd tools && go run ./cmd/send-secret sign -in <file>) before it is submitted."
  fi

  # Direct channel: no gas, no transaction, no permanent record of the ciphertext.
  # Must land within the one-hour freshness window opened at build time. submit
  # waits for the machine's own result, so "applied" below means the enclave
  # holds the value, not merely that the proxy took the envelope.
  echo
  echo "» submit"
  if ! "$WORKDIR/send-secret" submit -in "$envelope" -p "$PROXY_URL"; then
    mkdir -p "$OUT_DIR"
    cp "$envelope" "$(handoff_file "$REF")"
    admins_arg=""
    if [ -f "$ADMINS_FILE" ]; then admins_arg=" -admins $ADMINS_FILE"; fi
    signers_hint="check who signed against the published set: (cd tools && go run ./cmd/send-secret signers -in $(handoff_file "$REF")$admins_arg)"
    if [ "$REF" = hex_cash_url ]; then
      die "$REF was not applied — the usual cause is a published hex_cash commitment that was not made from this HEX_CASH_URL and HEX_CASH_SALT; otherwise $signers_hint"
    fi
    die "$REF was not applied — the enclave reports one reason for every failure: a signer outside the backing set, a threshold above $BACKING_THRESHOLD, or a stale version; $signers_hint"
  fi
done

if [ "$BUILD_ONLY" = true ]; then
  step "3/3  Handing off"
  for REF in "${REFS[@]}"; do
    echo "  wait  $REF needs signers — $(handoff_file "$REF")"
  done
  echo
  echo "Built for machine $RUN_TEE. Nothing was signed and nothing was sent."
  echo
  echo "Send each file to the backing admins — it holds ciphertext only. Before the"
  echo "expiry printed above, each of them runs, with their own key in ADMIN_KEY:"
  echo "    cd tools && go run ./cmd/send-secret sign -in <file>"
  echo "and once a file carries $BACKING_THRESHOLD signature(s) ($THRESHOLD_FROM), whoever holds the"
  echo "proxy API key:"
  echo "    cd tools && go run ./cmd/send-secret submit -in <file> -p $PROXY_URL"
  exit 0
fi

step "3/3  Confirming from the machine's readiness report"
# Each submit above already waited for the machine's verdict; this asks the
# machine for its signed readiness report as the cross-check
# (read_machine_readiness, lib/env.sh). The report is read on its own before the
# ref list is taken from it: no report and a report naming nothing both yield an
# empty list, and treating them alike once reported "no longer missing" for a
# machine that had said nothing at all. Polled for up to two and a half minutes:
# the report is the machine's live answer and the secret is installed as soon as
# SET_SECRET applies, but the proxy can take a moment to relay the answer.
report_json=""
missing=""
outstanding=""
for attempt in $(seq 1 30); do
  report_json="$(read_machine_readiness "$PROXY_URL" "$DIRECT_API_KEY" 2>/dev/null || true)"
  [ -n "$report_json" ] || break
  missing="$(printf '%s' "$report_json" | jq -r '.readiness.missingSecretRefs[]?' 2>/dev/null || true)"
  outstanding=""
  for REF in "${REFS[@]}"; do
    printf '%s\n' "$missing" | grep -qx "$REF" && outstanding="$outstanding $REF"
  done
  [ -n "$outstanding" ] || break
  [ "$attempt" -eq 1 ] && echo "  waiting for the machine's next observation to install the uploads..."
  [ "$attempt" -lt 30 ] && sleep 5
done

confirmed=false
if [ -z "$report_json" ]; then
  echo "  ?     the machine did not answer its readiness query (proxy unreachable, wrong"
  echo "        /direct key, or the machine down)."
  echo "        Every submit above was applied by the machine itself, which is the"
  echo "        verdict that counts; oracle-status.sh repeats this cross-check later."
elif [ -n "$outstanding" ]; then
  die "the machine still reports as missing:$outstanding after 2.5 minutes — although it reported the uploads applied. The machine installs a secret on its next observation: ./deploy/scripts/oracle-status.sh repeats this check; otherwise see the machine's logs"
else
  for REF in "${REFS[@]}"; do
    echo "  ok    $REF is installed"
  done
  confirmed=true
  if [ -n "$missing" ]; then
    echo "  note  still outstanding, for whoever holds those credentials:"
    for m in $missing; do printf '        %s\n' "$m"; done
  fi
  ready="$(printf '%s' "$report_json" | jq -r '.readiness.ready' 2>/dev/null || true)"
  [ "$ready" = true ] && echo "  ok    the machine reports ready: true"
fi

echo
if [ "$confirmed" = true ]; then
  echo "Done. The credentials are installed in this machine only, and only in memory:"
  echo "they are lost on restart and must be uploaded again."
else
  echo "Applied, per the machine's own results. Whatever this machine holds is in"
  echo "memory only: it is lost on restart and must be uploaded again."
fi
