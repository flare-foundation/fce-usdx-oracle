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
# Which refs are uploaded comes from the MACHINE, not from a file here: its
# signed readiness report names missingSecretRefs, computed from the endpoint
# configuration it actually applied. A ref exists because a provider URL named
# it, so asking the machine is the only way to be sure the two agree.
#
# Usage:
#   ./deploy/scripts/upload-rpc-keys.sh                   # every ref the machine still wants
#   ./deploy/scripts/upload-rpc-keys.sh --all             # every ref in the config — i.e. rotate
#   ./deploy/scripts/upload-rpc-keys.sh --refs ankr_eth_key,alchemy_flare_key
#   ./deploy/scripts/upload-rpc-keys.sh --threshold 3     # match a 3-of-N providers set
#   ./deploy/scripts/upload-rpc-keys.sh --no-verify       # mocknode only — see below
#
# Everything can be preset in the environment to run unattended; anything unset
# and required is prompted for. Secrets are ONLY ever taken from the environment
# or a hidden prompt, never from a command-line argument — argv is readable by
# any other process on this machine through `ps`.
#
#   PROXY_URL          extension proxy base URL
#   EXPECTED_CODE_HASH optional: pins the upload to one build on top of the registry's
#                      acceptance (a SIMULATED_TEE=true machine defaults to tee-node's test constant)
#   ADMIN_KEYS         provider admin private keys held HERE, comma-separated
#                      (ADMIN_KEY is accepted as the single-key form)
#   DIRECT_API_KEY     proxy API key for the direct channel
#   <REF_IN_CAPS>      the credential for each ref — ANKR_FLARE_KEY for
#                      ankr_flare_key, and so on
#   OUT_DIR            where a handed-off envelope is written
#   ENDPOINTS_FILE     the published endpoint configuration, for --all only
#   PROVIDER_THRESHOLD signatures to collect before submitting (default 2)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

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

ENDPOINTS_FILE="${ENDPOINTS_FILE:-$REPO_ROOT/deploy/config/endpoints/coston2.json}"

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
  name="$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]' | tr -c 'A-Z0-9_' '_')"
  case "$name" in
    [A-Z_]*) printf '%s' "$name" ;;
    *) printf '' ;;
  esac
}

# readMachineReport asks the machine for its signed readiness report
# (read_machine_readiness, lib/env.sh) and leaves it in REPORT_JSON.
#
# It reports failure through its exit status rather than dying, because a
# subshell is where every caller would run it — and `exit` inside $( ) ends the
# subshell only, which would turn an unreachable proxy into an empty ref list and
# a cheerful "nothing to upload".
# .env and the recorded stack, as upload-hex-key.sh loads them: the proxy URL,
# the chain, the /direct key and the admin keys default from there.
source "$REPO_ROOT/deploy/scripts/lib/chains.sh"
source "$REPO_ROOT/deploy/scripts/lib/env.sh"; load_env
REPORT_JSON=""
readMachineReport() {
  # Cleared first: a failed read must not leave the previous, now stale, report
  # behind for a caller that is about to report on it.
  REPORT_JSON=""
  REPORT_JSON="$(read_machine_readiness "$PROXY_URL" "$DIRECT_API_KEY")" || return 1
  [ -n "$REPORT_JSON" ] || return 1
}

missingRefs() {
  printf '%s' "$REPORT_JSON" | jq -r '.readiness.missingSecretRefs[]?' 2>/dev/null || true
}

# configuredRefs is every provider secret named in the endpoint configuration —
# the rotation list, since a ref already installed is absent from
# missingSecretRefs but still needs the new value.
#
# It comes from the file rather than the machine because the report names only
# what is MISSING, by design: the full list would name the credentials a machine
# holds. So the caller has to supply the configuration, and confirm it is the
# one that was published — the machine's endpointsHash below is what that
# confirmation compares against.
configuredRefs() {
  jq -r '.[].endpoints[]?.secret // empty' "$ENDPOINTS_FILE" | sort -u
}

liveEndpointsHash() {
  printf '%s' "$REPORT_JSON" | jq -r '.readiness.endpointsHash // ""' 2>/dev/null || true
}

echo "=== USDX oracle — upload the RPC provider credentials ==="
echo
echo "You will need: the proxy URL, at least one provider admin private key, the"
echo "credential for each ref, and — only if you hold enough keys to submit — the"
echo "proxy API key. EXPECTED_CODE_HASH, if set, pins the upload to that build."
echo

# The machine's proxy and the /direct key default from .env, as in
# upload-hex-key.sh; the prompt only covers what neither .env nor the
# environment names.
: "${PROXY_URL:=${EXT_PROXY_URL:-}}"
ask PROXY_URL          "Extension proxy URL"    "http://localhost:6674"
# A machine run with SIMULATED_TEE=true attests with tee-node's fixed test code
# hash rather than a build's, so when .env says the machine is simulated and
# names no hash, that constant is what the check can expect. A real Confidential
# Space machine has a hash of its own, obtained out of band, and is asked for.
SIMULATED_CODE_HASH=0x194844cf417dde867073e5ab7199fa4d21fd82b5dbe2bdea8b3d7fc18d10fdc2
if [ "${SIMULATED_TEE:-}" = true ] && [ -z "${EXPECTED_CODE_HASH:-}" ]; then
  EXPECTED_CODE_HASH="$SIMULATED_CODE_HASH"
  echo "expected code hash: tee-node's simulated-machine constant (SIMULATED_TEE=true in .env)"
fi
# As in upload-hex-key.sh: the registry vouches for the build, EXPECTED_CODE_HASH
# pins one on top and is optional.

# ADMIN_KEY is what send-secret itself reads, so a single-key operator can use
# the same variable for both scripts.
if [ -z "${ADMIN_KEYS:-}" ]; then
  if [ -n "${ADMIN_KEY:-}" ]; then
    ADMIN_KEYS="$ADMIN_KEY"
  else
    ask_secret ADMIN_KEYS "Provider admin private key(s), comma-separated (hidden)"
  fi
fi

OLD_IFS="$IFS"; IFS=','; read -r -a ADMIN_KEYS <<< "$ADMIN_KEYS"; IFS="$OLD_IFS"
[ "${#ADMIN_KEYS[@]}" -gt 0 ] || die "no admin key given"

# Only whoever submits needs the proxy API key, and an admin holding fewer keys
# than the threshold will only ever hand off. Asked here rather than at the
# submit itself so nobody is hunting for a key while the freshness window runs.
if [ "${#ADMIN_KEYS[@]}" -ge "$PROVIDER_FLOOR" ]; then
  ask_secret DIRECT_API_KEY "Proxy API key (hidden)"
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
( cd "$REPO_ROOT/tools" && go build -o "$BIN_DIR/send-secret" ./cmd/send-secret ) \
  || die "could not build send-secret"

ATTESTED_TEE_ID=""
if [ "$VERIFY" = true ]; then
  ( cd "$REPO_ROOT/tools" && go build -o "$BIN_DIR/verify-tee" ./cmd/verify-tee ) \
    || die "could not build verify-tee"
  # -printTeeId puts the ATTESTED teeId on stdout and the human report on
  # stderr. Every build below pins to it, so a proxy that serves an honest key
  # to this check and a substituted one to an encryption a moment later is
  # refused rather than handed a credential. One id for the whole run: every ref
  # goes to the same machine.
  ATTESTED_TEE_ID="$("$BIN_DIR/verify-tee" -p "$PROXY_URL" ${EXPECTED_CODE_HASH:+-codeHash "$EXPECTED_CODE_HASH"} -printTeeId)" \
    || die "verification failed — no credential was sent"
  [ -n "$ATTESTED_TEE_ID" ] \
    || die "verification passed but printed no teeId — no credential was sent"
else
  echo "  SKIPPED (--no-verify). Nothing here has checked that the key these"
  echo "  credentials are encrypted to belongs to an enclave at all, and with no"
  echo "  attested teeId the encryptions below cannot be pinned to one either."
  echo "  Only ever do this against a mocknode."
fi

echo
echo "--- 2/4  Working out which refs to upload -------------------------"
echo
# Ordered so that only the branch which genuinely needs the machine's answer is
# gated on asking it. --refs and --all both name the refs from something the
# operator already holds, so requiring the machine's answer there would fail a
# run that never had a question to ask.
if [ -n "$REFS_ARG" ]; then
  OLD_IFS="$IFS"; IFS=','; read -r -a REFS <<< "$REFS_ARG"; IFS="$OLD_IFS"
  echo "  from --refs: ${REFS[*]}"
elif [ "$ALL" = true ]; then
  [ -f "$ENDPOINTS_FILE" ] || die "no endpoints configuration at $ENDPOINTS_FILE — pass ENDPOINTS_FILE or --refs"
  # shellcheck disable=SC2207
  REFS=($(configuredRefs))
  echo "  every ref named in $ENDPOINTS_FILE (rotation)"
  # Best effort: the list came from the file, so a machine that does not answer
  # costs the cross-check but not the run.
  if readMachineReport && [ -n "$(liveEndpointsHash)" ]; then
    echo "  the machine is running endpointsHash $(liveEndpointsHash)"
    echo "  confirm that is this file's hash before trusting the list:"
  else
    echo "  the machine did not answer, so it cannot be asked which config it runs;"
    echo "  confirm this file is the published one before trusting the list:"
  fi
  echo "    cd tools && go run ./cmd/send-endpoints -dry-run -config $ENDPOINTS_FILE"
elif ! readMachineReport; then
  die "this machine did not answer its readiness query (proxy unreachable, wrong /direct key, or \
the machine down), so it cannot be asked which credentials it wants — name them with --refs <a,b>, \
or --all to take every ref from ENDPOINTS_FILE"
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
    echo "  skipping $ref — the backing set governs it; use ./deploy/scripts/upload-hex-key.sh"
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
upload it by hand, from tools/: SECRET_VALUE=… go run ./cmd/send-secret build -ref $ref …"
  ask_secret "$var" "Credential for $ref (hidden, or set $var)"
done

echo
echo "--- 3/4  Building, signing and submitting --------------------------"

submitted=(); need_signers=(); rejected=(); proxy_refused=()
for ref in "${REFS[@]}"; do
  echo
  echo "  $ref"

  var="$(envVarFor "$ref")"
  envelope="$WORKDIR/$ref.json"

  # Strictly increasing per ref. Wall-clock seconds always increase, and stay
  # correct across a restart that resets the enclave's in-memory counter to zero.
  version="$(date +%s)"

  # -tee pins the encryption to the machine the attestation named. The expansion
  # is unquoted-by-design so an empty value passes no flag at all (--no-verify);
  # a teeId is a hex address, so word splitting has nothing to split.
  SECRET_VALUE="${!var}" "$BIN_DIR/send-secret" build \
      -p "$PROXY_URL" -ref "$ref" \
      -version "$version" ${ATTESTED_TEE_ID:+-tee "$ATTESTED_TEE_ID"} \
      -out "$envelope" \
    || die "could not build the envelope for $ref"

  # Every key held HERE signs. send-secret refuses a duplicate signer, so a key
  # that already signed is a warning rather than a second useless signature.
  for key in "${ADMIN_KEYS[@]}"; do
    [ -n "$key" ] || continue
    ADMIN_KEY="$key" "$BIN_DIR/send-secret" sign -in "$envelope" \
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
    echo "        their own key in ADMIN_KEY:"
    echo "          (cd tools && go run ./cmd/send-secret sign -in <file> && go run ./cmd/send-secret submit -in <file> -p $PROXY_URL)"
  }

  sigs="$(jq -r '.signatures | length' "$envelope")"
  if [ "$sigs" -lt "$PROVIDER_FLOOR" ]; then
    echo "        $sigs signature(s), and the providers set needs at least $PROVIDER_FLOOR."
    need_signers+=("$ref")
    keep
    continue
  fi

  submit_out=""; submit_rc=0
  submit_out="$("$BIN_DIR/send-secret" submit -in "$envelope" -p "$PROXY_URL" 2>&1)" || submit_rc=$?
  printf '%s\n' "$submit_out"
  if [ "$submit_rc" -eq 0 ]; then
    submitted+=("$ref")
    continue
  fi

  # A refusal by the PROXY and a refusal by the ENCLAVE are different failures
  # with disjoint causes, and reporting both as "refused by the machine" sends
  # an operator to audit thresholds, admin sets and versions when the envelope
  # never got past the front door.
  case "$submit_out" in
    *"proxy returned 401"*|*unauthorized*)
      echo "        the PROXY refused this before the enclave saw it: the proxy API key is wrong."
      echo "        Nothing about the signatures or the admin set is implicated."
      proxy_refused+=("$ref")
      ;;
    *"proxy returned"*)
      echo "        the PROXY refused this before the enclave saw it — see the status above."
      proxy_refused+=("$ref")
      ;;
    *)
      # Enough signatures to be worth sending, and the enclave still refused. It
      # reports one reason for every rejection by design, so the cause is not in
      # this output: the usual ones are a threshold higher than the floor, an
      # admin key that is not in the published set, and a stale version.
      echo "        submitted with $sigs signature(s) and refused by the machine."
      rejected+=("$ref")
      ;;
  esac
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
#
# The report is the machine's live answer, but a submitted secret is installed
# by the poller's next observation (every two minutes), so the report is polled
# for up to two and a half minutes until it no longer lists a submitted ref as
# missing.
still_missing=""
for attempt in $(seq 1 30); do
  if ! readMachineReport; then
    echo "  ?     the machine did not answer its readiness query — the uploads above were not confirmed"
    break
  fi
  still_missing="$(missingRefs)"
  pending=false
  for ref in "${submitted[@]:-}"; do
    [ -n "$ref" ] && printf '%s\n' "$still_missing" | grep -qx "$ref" && pending=true
  done
  [ "$pending" = true ] || break
  [ "$attempt" -eq 1 ] && echo "  waiting for the machine's next observation to install the uploads..."
  [ "$attempt" -lt 30 ] && sleep 5
done
for ref in "${submitted[@]:-}"; do
  [ -n "$ref" ] || continue
  if [ -z "$REPORT_JSON" ]; then
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
for ref in "${proxy_refused[@]:-}"; do
  [ -n "$ref" ] || continue
  echo "  FAIL  $ref never reached the machine — the proxy refused it, check the proxy API key — $OUT_DIR/$ref.json"
done

if [ -n "$still_missing" ]; then
  echo
  echo "  still outstanding on this machine:"
  for m in $still_missing; do printf '        %s\n' "$m"; done
  if printf '%s\n' "$still_missing" | grep -qx "$BACKING_REF"; then
    echo "        $BACKING_REF is the backing credential — ./deploy/scripts/upload-hex-key.sh"
  fi
fi

echo
# Only claimed when something actually landed. Printing it unconditionally told
# an operator whose every upload was refused that their credentials were
# installed, which is the one thing the run had just disproved.
if [ "${#submitted[@]}" -gt 0 ]; then
  echo "Done. What was accepted is installed in this machine only, and only in"
  echo "memory: it is lost on restart and must be uploaded again."
else
  echo "Nothing was installed: every upload above failed. The credentials this"
  echo "machine holds are unchanged."
fi
