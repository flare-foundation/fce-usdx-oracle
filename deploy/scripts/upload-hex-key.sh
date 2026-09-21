#!/usr/bin/env bash
#
# upload-hex-key.sh — hand the Hex backing credentials to one TEE machine.
#
# Run by the party that holds the credentials. Three steps:
#
#   1. verify the machine is running a build you accept, and read its attested
#      public key                                   (on-chain, not self-reported)
#   2. for each credential: encrypt it to that key, sign the envelope with the
#      backing admin key, submit it over the direct channel and wait for the
#      machine's own verdict                        (ECIES; plaintext never leaves this shell)
#   3. confirm from the machine's readiness report that nothing is still missing
#
# Two credentials fall under the backing set, and the machine uses the cash
# source only once it holds both:
#
#   hex_api_key    the Hex API key, from HEX_API_KEY (USDXPROV_VALUE is accepted too)
#   hex_cash_url   the restricted cash URL, from HEX_CASH_URL and HEX_CASH_SALT. Uploaded
#                  only when HEX_CASH_URL is set, and it must be the URL the published
#                  SET_ENDPOINTS committed to — publish-local.sh commits from the same
#                  variables, so on the simulation the two agree by construction.
#
# Nothing here touches the chain except to READ it. The credentials are never in a
# transaction, never on disk in plaintext, and never in argv — every secret is
# passed to the tools through the environment, because argv is readable by any
# other process on this machine via `ps`.
#
# Usage:
#   ./deploy/scripts/upload-hex-key.sh
#   ./deploy/scripts/upload-hex-key.sh --no-verify    # mocknode only — see step 1
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
#   USDXPROV_KEY       backing admin private key, hex (default on the simulation: the dev backing admin)
#   USDXPROV_API_KEY   proxy API key for the direct channel (default: DIRECT_API_KEY)
#   HEX_API_KEY        the Hex API key itself
#   HEX_CASH_URL       the restricted cash URL, https  (optional; enables the cash source)
#   HEX_CASH_SALT      32-byte hex salt of its commitment (required with HEX_CASH_URL)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROJECT_DIR="$REPO_ROOT"
source "$REPO_ROOT/deploy/scripts/lib/chains.sh"
source "$REPO_ROOT/deploy/scripts/lib/env.sh"; load_env

VERIFY=true
while [ $# -gt 0 ]; do
  case "$1" in
    --no-verify) VERIFY=false; shift ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 1 ;;
  esac
done

# The envelopes hold only ciphertext, but they are still removed on every exit
# path rather than left in a working directory someone later tars up.
WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }
step() { printf '\n--- %s ---------------------------------------------------\n\n' "$1"; }

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
  : "${USDXPROV_API_KEY:=${DIRECT_API_KEY:-local}}"
  : "${USDXPROV_KEY:=0x0000000000000000000000000000000000000000000000000000000000000001}"
else
  : "${USDXPROV_API_KEY:=${DIRECT_API_KEY:-}}"
fi
: "${USDXPROV_VALUE:=${HEX_API_KEY:-}}"
: "${PROXY_URL:=${EXT_PROXY_URL:-}}"
export USDXPROV_API_KEY USDXPROV_KEY USDXPROV_VALUE PROXY_URL

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

ask_secret USDXPROV_KEY     "Backing admin private key (hidden)"
ask_secret USDXPROV_API_KEY "Proxy API key (hidden)"
ask_secret USDXPROV_VALUE   "Hex API key (hidden)"

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
if [ ${#REFS[@]} -eq 1 ]; then
  echo "          HEX_CASH_URL is not set, so only the key goes up and the machine keeps running cash-less"
fi

# Strictly increasing per ref. Wall-clock seconds always increase, which is what
# the enclave requires, and it stays correct across a restart that resets its
# in-memory counter to zero.
VERSION="${VERSION:-$(date +%s)}"

( cd "$REPO_ROOT/tools" && go build -o "$WORKDIR/usdxprov" ./cmd/usdxprov ) \
  || die "could not build usdxprov"

step "1/3  Verifying the machine"
# Gating step, and the source of the teeId step 2 pins the encryption to. If
# this fails, the key on the next line may not belong to an enclave at all, so
# nothing after it runs. On the simulation no code hash is expected: the check
# still derives the teeId from the attestation and confirms the registration.
ATTESTED_TEE_ID=""
if [ "$VERIFY" = true ]; then
  # -print-tee-id puts the ATTESTED teeId on stdout and the human report on
  # stderr, so the operator still sees every check while the id is captured.
  #
  # Capturing it matters because this command and the encryption below are
  # separate processes making separate requests to the same proxy. Verifying
  # here proves something about the key THIS request received; passing the id to
  # build is what makes build refuse a different key on its own request.
  ATTESTED_TEE_ID="$( cd "$REPO_ROOT/tools" && go run ./cmd/verify-tee \
      -p "$PROXY_URL" ${EXPECTED_CODE_HASH:+-code-hash "$EXPECTED_CODE_HASH"} -print-tee-id )" \
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
  step "2/3  Uploading $REF ($n of ${#REFS[@]})"

  # build reads the public key from /info and encrypts the credential to it. -tee
  # is what ties that key to the attestation checked above: build refuses outright
  # if what it is served derives to a different machine. The expansion is
  # unquoted-by-design so an empty value passes no flag at all (--no-verify); a
  # teeId is a hex address, so word splitting has nothing to split.
  #
  # The plaintext exists only in this shell and inside the target enclave. For
  # hex_cash_url the value is derived from HEX_CASH_URL and HEX_CASH_SALT, so the
  # API key in USDXPROV_VALUE is cleared for that one build.
  echo "» build"
  value_env=""
  [ "$REF" = hex_cash_url ] && value_env="USDXPROV_VALUE="
  ( cd "$REPO_ROOT" && env $value_env "$WORKDIR/usdxprov" build \
      -url "$PROXY_URL" -chain-id "$CHAIN_ID" -ref "$REF" \
      -version "$VERSION" ${ATTESTED_TEE_ID:+-tee "$ATTESTED_TEE_ID"} \
      -out "$envelope" ) \
    || die "could not build the envelope for $REF"

  # One signature: the backing set governs both refs at 1-of-1. Every other
  # credential falls under the providers set and needs two distinct signers.
  echo
  echo "» sign"
  "$WORKDIR/usdxprov" sign -in "$envelope" \
    || die "could not sign the envelope for $REF"

  # Direct channel: no gas, no transaction, no permanent record of the ciphertext.
  # Must land within the one-hour freshness window opened at build time. submit
  # waits for the machine's own result, so "applied" below means the enclave
  # holds the value, not merely that the proxy took the envelope.
  echo
  echo "» submit"
  if ! "$WORKDIR/usdxprov" submit -in "$envelope" -url "$PROXY_URL"; then
    if [ "$REF" = hex_cash_url ]; then
      die "$REF was not applied — the usual cause is a published hex_cash commitment that was not made from this HEX_CASH_URL and HEX_CASH_SALT"
    fi
    die "$REF was not applied — the enclave reports one reason for every failure, so check its logs"
  fi
done

step "3/3  Confirming from the machine's readiness report"
# Each submit above already waited for the machine's verdict; this asks the
# machine for its signed readiness report as the cross-check
# (read_machine_readiness, lib/env.sh). The report is read on its own before the
# ref list is taken from it: no report and a report naming nothing both yield an
# empty list, and treating them alike once reported "no longer missing" for a
# machine that had said nothing at all. Polled for up to two and a half minutes:
# the report is the machine's live answer, but the secret is installed by the
# poller's next observation.
report_json=""
missing=""
outstanding=""
for attempt in $(seq 1 30); do
  report_json="$(read_machine_readiness "$PROXY_URL" "$USDXPROV_API_KEY" 2>/dev/null || true)"
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
