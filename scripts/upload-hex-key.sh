#!/usr/bin/env bash
#
# upload-hex-key.sh — hand the Hex backing credential to one TEE machine.
#
# Run by the party that holds the credential. Five steps:
#
#   1. verify the machine is running a build you accept        (on-chain, not self-reported)
#   2. read its attested public key                            (same attestation as step 1)
#   3. encrypt the credential to that key                      (ECIES; plaintext never leaves this shell)
#   4. sign the envelope with the backing admin key            (the backing set is 1-of-1)
#   5. submit it over the direct channel and confirm it landed
#
# Nothing here touches the chain except to READ it. The credential is never in a
# transaction, never on disk in plaintext, and never in argv — every secret is
# passed to the tools through the environment, because argv is readable by any
# other process on this machine via `ps`.
#
# Usage:
#   ./scripts/upload-hex-key.sh
#   ./scripts/upload-hex-key.sh --no-verify    # mocknode only — see step 1
#
# Everything can be preset in the environment to run unattended; anything unset
# and required is prompted for. Secrets are ONLY ever taken from the environment
# or a hidden prompt, never from a command-line argument:
#
#   PROXY_URL          extension proxy base URL
#   CHAIN_ID           target chain id (Coston2 = 114, Flare = 14)
#   EXPECTED_CODE_HASH the build you expect, obtained OUT OF BAND
#   USDXPROV_KEY       backing admin private key, hex
#   USDXPROV_API_KEY   proxy API key for the direct channel
#   USDXPROV_VALUE     the Hex API key itself
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REF="hex_api_key"

VERIFY=true
while [ $# -gt 0 ]; do
  case "$1" in
    # For a local mocknode, which has no attestation to check and no
    # registration on any chain. Never for a machine that will hold a real
    # credential.
    --no-verify) VERIFY=false; shift ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; exit 1 ;;
  esac
done

# The envelope holds only ciphertext, but it is still removed on every exit path
# rather than left in a working directory someone later tars up.
WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }

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

echo "=== USDX oracle — upload the Hex backing credential ==="
echo
echo "You will need: the proxy URL, the expected code hash (given to you"
echo "out of band), the backing admin private key, the proxy API key, and"
echo "the Hex API key itself."
echo

ask PROXY_URL          "Extension proxy URL"    "http://localhost:8080"
ask CHAIN_ID           "Chain id"               "114"
if [ "$VERIFY" = true ]; then
  ask EXPECTED_CODE_HASH "Expected code hash (0x…)"
fi

ask_secret USDXPROV_KEY     "Backing admin private key (hidden)"
ask_secret USDXPROV_API_KEY "Proxy API key (hidden)"
ask_secret USDXPROV_VALUE   "Hex API key (hidden)"

# Strictly increasing per ref. Wall-clock seconds always increase, which is what
# the enclave requires, and it stays correct across a restart that resets its
# in-memory counter to zero.
VERSION="${VERSION:-$(date +%s)}"

echo
echo "--- 1/5  Verifying the machine ------------------------------------"
echo
# Gating step, and the source of the teeId step 2 pins the encryption to. If
# this fails, the key on the next line may not belong to an enclave at all, so
# nothing after it runs.
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
      -p "$PROXY_URL" -code-hash "$EXPECTED_CODE_HASH" -print-tee-id )" \
    || die "verification failed — the credential was NOT sent"
  [ -n "$ATTESTED_TEE_ID" ] \
    || die "verification passed but printed no teeId — the credential was NOT sent"
else
  echo "  SKIPPED (--no-verify). Nothing here has checked that the key this"
  echo "  credential is encrypted to belongs to an enclave at all, and with no"
  echo "  attested teeId the encryption below cannot be pinned to one either."
  echo "  Only ever do this against a mocknode."
fi

echo
echo "--- 2/5  Building the envelope ------------------------------------"
echo
# build reads the public key from /info and encrypts the credential to it. -tee
# is what ties that key to the attestation checked above: build refuses outright
# if what it is served derives to a different machine. The expansion is
# unquoted-by-design so an empty value passes no flag at all (--no-verify); a
# teeId is a hex address, so word splitting has nothing to split.
#
# The plaintext exists only in this shell and inside the target enclave.
( cd "$REPO_ROOT/go" && go run ./cmd/usdxprov build \
    -url "$PROXY_URL" -chain-id "$CHAIN_ID" -ref "$REF" \
    -version "$VERSION" ${ATTESTED_TEE_ID:+-tee "$ATTESTED_TEE_ID"} \
    -out "$WORKDIR/envelope.json" ) \
  || die "could not build the envelope"

echo
echo "--- 3/5  Signing ---------------------------------------------------"
echo
# One signature: the backing set governs hex_api_key at 1-of-1. Every other
# credential falls under the providers set and needs two distinct signers.
( cd "$REPO_ROOT/go" && go run ./cmd/usdxprov sign -in "$WORKDIR/envelope.json" ) \
  || die "could not sign the envelope"

echo
echo "--- 4/5  Submitting ------------------------------------------------"
echo
# Direct channel: no gas, no transaction, no permanent record of the ciphertext.
# Must land within the one-hour freshness window opened at build time.
( cd "$REPO_ROOT/go" && go run ./cmd/usdxprov submit \
    -in "$WORKDIR/envelope.json" -url "$PROXY_URL" ) \
  || die "submission was rejected — the enclave reports one reason for every failure, so check its logs"

echo
echo "--- 5/5  Confirming ------------------------------------------------"
echo
# The enclave accepted the envelope; confirm it actually installed the value by
# reading its own published state rather than trusting the submit result.
missing=""
confirmed=false
if command -v jq >/dev/null 2>&1 && command -v xxd >/dev/null 2>&1; then
  # The state body is read on its own before the ref list is taken from it. An
  # empty body and a body naming nothing both yield an empty list, and treating
  # them alike reported "no longer missing" for a machine that had said nothing
  # at all — a success claim resting on no evidence.
  state_json="$(curl -fsS "${PROXY_URL%/}/info" \
    | jq -r '.teeInfo.state.state' \
    | sed 's/^0x//' \
    | xxd -r -p 2>/dev/null || true)"

  if [ -z "$state_json" ]; then
    echo "  ?     this machine publishes no readable state, so whether $REF was installed"
    echo "        cannot be confirmed here. The upload above was accepted by the proxy;"
    echo "        that is not the same as the enclave holding the credential."
  else
    missing="$(printf '%s' "$state_json" | jq -r '.state.readiness.missingSecretRefs[]?' 2>/dev/null || true)"
    if printf '%s\n' "$missing" | grep -qx "$REF"; then
      die "the machine still reports $REF as missing"
    fi
    confirmed=true
    echo "  ok    the machine no longer reports $REF as missing"
    if [ -n "$missing" ]; then
      echo "  note  still outstanding, for whoever holds those credentials:"
      printf '        %s\n' $missing
    fi
  fi
else
  echo "  skipped (needs jq and xxd) — check manually:"
  echo "  curl -s ${PROXY_URL%/}/info | jq -r '.teeInfo.state.state' | sed 's/^0x//' | xxd -r -p | jq .state.readiness"
fi

echo
if [ "$confirmed" = true ]; then
  echo "Done. The credential is installed in this machine only, and only in memory:"
  echo "it is lost on restart and must be uploaded again."
else
  echo "Submitted, but not confirmed installed. Whatever this machine holds is in"
  echo "memory only: it is lost on restart and must be uploaded again."
fi
