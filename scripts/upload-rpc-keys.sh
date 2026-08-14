#!/usr/bin/env bash
#
# upload-rpc-keys.sh — hand the RPC provider credentials to one TEE machine.
#
# The counterpart of upload-hex-key.sh, and different in the three ways that
# make the provider credentials their own job rather than a loop around that one:
#
#   · SEVERAL credentials, not one. Each {secret} named in the published endpoint
#     configuration is its own ref, its own envelope and its own version.
#   · The PROVIDERS set governs them, and it is at least 2-of-N. The backing set
#     that governs hex_api_key can be 1-of-N, so that upload finishes on one
#     laptop and this one usually cannot.
#   · So it hands off what it cannot finish: the envelope is written out for a
#     second admin to sign and submit. It holds ciphertext only — the plaintext
#     credential never leaves the shell that read it.
#
# Which refs are uploaded comes from the MACHINE, not from a file here: it
# publishes missingSecretRefs in its own state, computed from the endpoint
# configuration it actually applied. A ref exists because a provider URL named
# it, so asking the machine is the only way to be sure the two agree.
#
# Usage:
#   ./scripts/upload-rpc-keys.sh                   # every ref the machine still wants
#   ./scripts/upload-rpc-keys.sh --all             # every ref in the config — i.e. rotate
#   ./scripts/upload-rpc-keys.sh --refs ankr_eth_key,alchemy_flare_key
#   ./scripts/upload-rpc-keys.sh --threshold 3     # match a 3-of-N providers set
#   ./scripts/upload-rpc-keys.sh --no-verify       # mocknode only — see below
#
# Everything can be preset in the environment to run unattended; anything unset
# and required is prompted for. Secrets are ONLY ever taken from the environment
# or a hidden prompt, never from a command-line argument — argv is readable by
# any other process on this machine through `ps`.
#
#   PROXY_URL          extension proxy base URL
#   CHAIN_ID           target chain id (Coston2 = 114, Flare = 14)
#   EXPECTED_CODE_HASH the build you expect, obtained OUT OF BAND
#   USDXPROV_KEYS      provider admin private keys held HERE, comma-separated
#                      (USDXPROV_KEY is accepted as the single-key form)
#   USDXPROV_API_KEY   proxy API key for the direct channel
#   <REF_IN_CAPS>      the credential for each ref — ANKR_FLARE_KEY for
#                      ankr_flare_key, and so on
#   OUT_DIR            where a handed-off envelope is written
#   ENDPOINTS_FILE     the published endpoint configuration, for --all only
#   PROVIDER_THRESHOLD signatures to collect before submitting (default 2)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# hex_api_key is the other script's job: it is the one ref the backing set
# governs, and signing it with provider keys would produce an envelope no
# machine can accept.
BACKING_REF="hex_api_key"

# How many signatures to collect before submitting.
#
# The default is the floor SetAdmins enforces for the providers set, which is
# the least that could ever be accepted. A machine running a HIGHER published
# threshold refuses an envelope that only meets the floor, so set
# PROVIDER_THRESHOLD to what you published and the envelope is handed on to the
# next signer instead of spending a round trip to be refused.
#
# Only the machine knows its real threshold, so this is when to stop collecting,
# never proof that the envelope will be accepted.
PROVIDER_FLOOR="${PROVIDER_THRESHOLD:-2}"

ENDPOINTS_FILE="${ENDPOINTS_FILE:-$REPO_ROOT/testdata/endpoints/coston2.json}"

die() { printf '\nerror: %s\n' "$1" >&2; exit 1; }

for tool in jq xxd curl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

REFS_ARG=""; ALL=false; VERIFY=true
while [ $# -gt 0 ]; do
  case "$1" in
    --refs) [ $# -ge 2 ] || die "--refs needs a comma-separated list"
            REFS_ARG="$2"; shift 2 ;;
    --all)  ALL=true; shift ;;
    --threshold) [ $# -ge 2 ] || die "--threshold needs a number"
            PROVIDER_FLOOR="$2"; shift 2 ;;
    # For a local mocknode, which has no attestation to check and no registration
    # on any chain. Never for a machine that will hold a real credential.
    --no-verify) VERIFY=false; shift ;;
    -h|--help) awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# Envelopes that get submitted here are temporary; one that is handed off is
# written to OUT_DIR instead, and outlives this process by design.
WORKDIR="$(mktemp -d)"
BIN_DIR="$WORKDIR/bin"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

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

# envVarFor is the environment variable a ref's credential is read from:
# ankr_flare_key → ANKR_FLARE_KEY. Returns nothing for a ref that is not a legal
# variable name, so the caller falls back to prompting by ref instead.
envVarFor() {
  local name
  name="$(printf '%s' "$1" | tr 'a-z' 'A-Z' | tr -c 'A-Z0-9_' '_')"
  case "$name" in
    [A-Z_]*) printf '%s' "$name" ;;
    *) printf '' ;;
  esac
}

# readMachineState pulls the extension's own /state out of the proxy's /info,
# where it is carried as hex bytes, and leaves it in STATE_JSON.
#
# It reports failure through its exit status rather than dying, because a
# subshell is where every caller would run it — and `exit` inside $( ) ends the
# subshell only, which would turn an unreachable proxy into an empty ref list and
# a cheerful "nothing to upload".
STATE_JSON=""
readMachineState() {
  local info
  # Cleared first: a failed read must not leave the previous, now stale, state
  # behind for a caller that is about to report on it.
  STATE_JSON=""
  info="$(curl -fsS "${PROXY_URL%/}/info")" || return 1
  STATE_JSON="$(printf '%s' "$info" | jq -r '.teeInfo.state.state' | sed 's/^0x//' | xxd -r -p)" || return 1
  [ -n "$STATE_JSON" ] || return 1
}

missingRefs() {
  printf '%s' "$STATE_JSON" | jq -r '.state.readiness.missingSecretRefs[]?' 2>/dev/null || true
}

# configuredRefs is every provider secret named in the endpoint configuration —
# the rotation list, since a ref already installed is absent from
# missingSecretRefs but still needs the new value.
#
# It comes from the file rather than the machine because /state publishes only
# what is MISSING, by design: the full list would name the credentials a machine
# holds, on a world-readable surface. So the caller has to supply the
# configuration, and confirm it is the one that was published — the machine's
# endpointsHash below is what that confirmation compares against.
configuredRefs() {
  jq -r '.[].providers[]?.secret // empty' "$ENDPOINTS_FILE" | sort -u
}

liveEndpointsHash() {
  printf '%s' "$STATE_JSON" | jq -r '.state.readiness.endpointsHash // ""' 2>/dev/null || true
}

echo "=== USDX oracle — upload the RPC provider credentials ==="
echo
echo "You will need: the proxy URL, the expected code hash (given to you out of"
echo "band), at least one provider admin private key, the credential for each"
echo "ref, and — only if you hold enough keys to submit — the proxy API key."
echo

ask PROXY_URL          "Extension proxy URL"    "http://localhost:8080"
ask CHAIN_ID           "Chain id"               "114"
if [ "$VERIFY" = true ]; then
  ask EXPECTED_CODE_HASH "Expected code hash (0x…)"
fi

# USDXPROV_KEY is what usdxprov itself reads, so a single-key operator can use
# the same variable for both scripts.
if [ -z "${USDXPROV_KEYS:-}" ]; then
  if [ -n "${USDXPROV_KEY:-}" ]; then
    USDXPROV_KEYS="$USDXPROV_KEY"
  else
    ask_secret USDXPROV_KEYS "Provider admin private key(s), comma-separated (hidden)"
  fi
fi

OLD_IFS="$IFS"; IFS=','; read -r -a ADMIN_KEYS <<< "$USDXPROV_KEYS"; IFS="$OLD_IFS"
[ "${#ADMIN_KEYS[@]}" -gt 0 ] || die "no admin key given"

# Only whoever submits needs the proxy API key, and an admin holding fewer keys
# than the threshold will only ever hand off. Asked here rather than at the
# submit itself so nobody is hunting for a key while a 15-minute window runs.
if [ "${#ADMIN_KEYS[@]}" -ge "$PROVIDER_FLOOR" ]; then
  ask_secret USDXPROV_API_KEY "Proxy API key (hidden)"
else
  echo "Holding ${#ADMIN_KEYS[@]} of the $PROVIDER_FLOOR signatures needed, so this run will hand"
  echo "the envelopes off rather than submit them. No proxy API key required."
fi

OUT_DIR="${OUT_DIR:-${TMPDIR:-/tmp}/usdx-envelopes}"

echo
echo "--- 1/4  Verifying the machine ------------------------------------"
echo
# Gating step, exactly as in upload-hex-key.sh: the next step encrypts a
# credential to a key this same proxy serves, so nothing runs until the chain
# agrees the machine behind it is real and running an accepted build.
mkdir -p "$BIN_DIR"
( cd "$REPO_ROOT/go" && go build -o "$BIN_DIR/usdxprov" ./cmd/usdxprov ) \
  || die "could not build usdxprov"

if [ "$VERIFY" = true ]; then
  ( cd "$REPO_ROOT/tools" && go build -o "$BIN_DIR/verify-tee" ./cmd/verify-tee ) \
    || die "could not build verify-tee"
  "$BIN_DIR/verify-tee" -p "$PROXY_URL" -code-hash "$EXPECTED_CODE_HASH" \
    || die "verification failed — no credential was sent"
else
  echo "  SKIPPED (--no-verify). Nothing here has checked that the key these"
  echo "  credentials are encrypted to belongs to an enclave at all. Only ever"
  echo "  do this against a mocknode."
fi

echo
echo "--- 2/4  Working out which refs to upload -------------------------"
echo
if [ -n "$REFS_ARG" ]; then
  OLD_IFS="$IFS"; IFS=','; read -r -a REFS <<< "$REFS_ARG"; IFS="$OLD_IFS"
  echo "  from --refs: ${REFS[*]}"
elif ! readMachineState; then
  die "cannot read ${PROXY_URL%/}/info — without it there is no way to know which \
credentials this machine wants; pass --refs to say explicitly"
elif [ "$ALL" = true ]; then
  [ -f "$ENDPOINTS_FILE" ] || die "no endpoints configuration at $ENDPOINTS_FILE — pass ENDPOINTS_FILE or --refs"
  # shellcheck disable=SC2207
  REFS=($(configuredRefs))
  echo "  every ref named in $ENDPOINTS_FILE (rotation)"
  echo "  the machine is running endpointsHash $(liveEndpointsHash)"
  echo "  confirm that is this file's hash before trusting the list:"
  echo "    cd tools && go run ./cmd/send-endpoints -dry-run -config $ENDPOINTS_FILE"
else
  # shellcheck disable=SC2207
  REFS=($(missingRefs))
  echo "  every ref the machine still reports missing"
fi

# The backing credential is the other script's, whichever list it arrived in.
FILTERED=()
for ref in "${REFS[@]:-}"; do
  [ -n "$ref" ] || continue
  if [ "$ref" = "$BACKING_REF" ]; then
    echo "  skipping $ref — the backing set governs it; use ./scripts/upload-hex-key.sh"
    continue
  fi
  FILTERED+=("$ref")
done
REFS=("${FILTERED[@]:-}")

if [ -z "${REFS[*]:-}" ]; then
  echo
  echo "Nothing to upload: no provider credential is outstanding."
  echo "To replace the ones the machine already holds, re-run with --all."
  exit 0
fi
echo "  → ${REFS[*]}"

# Collected before anything is sent, so a missing credential stops the run at the
# prompt rather than half way through the fleet's refs.
echo
for ref in "${REFS[@]}"; do
  var="$(envVarFor "$ref")"
  [ -n "$var" ] || die "ref $ref does not map to a legal environment variable name; \
upload it by hand with: USDXPROV_VALUE=… go run ./cmd/usdxprov build -ref $ref …"
  ask_secret "$var" "Credential for $ref (hidden, or set $var)"
done

echo
echo "--- 3/4  Building, signing and submitting --------------------------"

submitted=(); need_signers=(); rejected=()
for ref in "${REFS[@]}"; do
  echo
  echo "  $ref"

  var="$(envVarFor "$ref")"
  envelope="$WORKDIR/$ref.json"

  # Strictly increasing per ref. Wall-clock seconds always increase, and stay
  # correct across a restart that resets the enclave's in-memory counter to zero.
  version="$(date +%s)"

  USDXPROV_VALUE="${!var}" "$BIN_DIR/usdxprov" build \
      -url "$PROXY_URL" -chain-id "$CHAIN_ID" -ref "$ref" \
      -version "$version" -out "$envelope" \
    || die "could not build the envelope for $ref"

  # Every key held HERE signs. usdxprov refuses a duplicate signer, so a key
  # that already signed is a warning rather than a second useless signature.
  for key in "${ADMIN_KEYS[@]}"; do
    [ -n "$key" ] || continue
    USDXPROV_KEY="$key" "$BIN_DIR/usdxprov" sign -in "$envelope" \
      || echo "        (that key did not add a signature)"
  done

  # The envelope is kept whatever happens next: it is ciphertext plus
  # signatures, safe to send over any channel, and re-building costs another
  # round trip and a new freshness window.
  keep() {
    mkdir -p "$OUT_DIR"
    cp "$envelope" "$OUT_DIR/$ref.json"
    echo "        kept at $OUT_DIR/$ref.json"
    echo "        Before the expiry printed above, the next admin runs this with"
    echo "        their own key in USDXPROV_KEY:"
    echo "          cd go && go run ./cmd/usdxprov sign -in <file> && go run ./cmd/usdxprov submit -in <file> -url $PROXY_URL"
  }

  sigs="$(jq -r '.signatures | length' "$envelope")"
  if [ "$sigs" -lt "$PROVIDER_FLOOR" ]; then
    echo "        $sigs signature(s), and the providers set needs at least $PROVIDER_FLOOR."
    need_signers+=("$ref")
    keep
    continue
  fi

  if "$BIN_DIR/usdxprov" submit -in "$envelope" -url "$PROXY_URL"; then
    submitted+=("$ref")
    continue
  fi

  # Enough signatures to be worth sending, and it was still refused. The enclave
  # reports one reason for every rejection by design, so the cause is not in this
  # output: the usual ones are a threshold higher than the floor, an admin key
  # that is not in the published set, and a wrong proxy API key.
  echo "        submitted with $sigs signature(s) and refused."
  rejected+=("$ref")
  keep
done

echo
echo "--- 4/4  Confirming ------------------------------------------------"
echo
# The machine's own account of itself, not the submit results: an accepted
# envelope and an installed credential are not the same claim.
#
# A read failure here is reported, never fatal — the uploads already happened,
# and exiting non-zero would say they did not.
still_missing=""
if readMachineState; then
  still_missing="$(missingRefs)"
else
  echo "  ?     cannot re-read ${PROXY_URL%/}/info — the uploads above were not confirmed"
fi
for ref in "${submitted[@]:-}"; do
  [ -n "$ref" ] || continue
  if [ -z "$STATE_JSON" ]; then
    echo "  ?     $ref was accepted, unconfirmed"
  elif printf '%s\n' "$still_missing" | grep -qx "$ref"; then
    echo "  FAIL  $ref was accepted but the machine still reports it missing"
  else
    echo "  ok    $ref installed"
  fi
done
for ref in "${need_signers[@]:-}"; do
  [ -n "$ref" ] || continue
  echo "  wait  $ref needs another signer — $OUT_DIR/$ref.json"
done
for ref in "${rejected[@]:-}"; do
  [ -n "$ref" ] || continue
  echo "  FAIL  $ref was refused by the machine — $OUT_DIR/$ref.json"
done

if [ -n "$still_missing" ]; then
  echo
  echo "  still outstanding on this machine:"
  printf '        %s\n' $still_missing
  if printf '%s\n' "$still_missing" | grep -qx "$BACKING_REF"; then
    echo "        $BACKING_REF is the backing credential — ./scripts/upload-hex-key.sh"
  fi
fi

echo
echo "Done. These credentials are installed in this machine only, and only in"
echo "memory: they are lost on restart and must be uploaded again."
