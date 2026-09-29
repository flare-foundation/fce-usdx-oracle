#!/usr/bin/env bash
#
# check-endpoints.sh — try every provider in an endpoint configuration with the
# credential that would be installed for it, before anything is published or
# uploaded.
#
# A wrong credential is otherwise discovered only after it has been encrypted,
# signed by a threshold of admins and installed — and then only as that provider
# reading `down`. This finds it first: for each endpoint it substitutes the
# credential its ref names, asks the provider which chain it serves, and compares
# the answer with the chain the group reads. A wrong key, a wrong parameter name
# and a URL for the wrong network all show up as FAIL.
#
# Usage:
#   AFLABS_KEY=… QUICKNODE_FLARE_KEY=… ./deploy/scripts/check-endpoints.sh deploy/config/endpoints/songbird.json
#
# Credentials come from the environment only. Each ref's variable is its name in
# capitals — aflabs_key is read from AFLABS_KEY — the same variable
# upload-rpc-keys.sh reads when it uploads it. .env is deliberately not loaded:
# sourcing it would override whatever was set in front of the command. An
# endpoint whose variable is unset is skipped rather than failed, so keys can be
# checked a few at a time. A key reaches curl on stdin, never in argv, and
# neither a key nor a URL with one substituted is ever printed.
#
# Exits non-zero when any endpoint fails.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
export PROJECT_DIR
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"

die() { printf 'error: %s\n' "$1" >&2; exit 1; }
for tool in jq curl; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

CONFIG="${1:-}"
[ -n "$CONFIG" ] || die "usage: $0 <endpoints.json>"
[ -f "$CONFIG" ] || die "no such file: $CONFIG"
jq -e 'type == "object"' "$CONFIG" >/dev/null 2>&1 || die "$CONFIG is not an endpoint configuration"

# The chain each group reads supply from, as eth_chainId reports it.
chain_id_for() {
  case "$1" in
    flare)    printf '0xe' ;;
    songbird) printf '0x13' ;;
    ethereum) printf '0x1' ;;
    *)        printf '' ;;
  esac
}

# The characters the enclave refuses in a credential, because each can
# restructure the URL it lands in (urlUnsafe in internal/provisioning/store).
# Refused here, it costs nothing; refused by the enclave, it costs a signing round.
unsafe_credential() {
  case "$1" in
    *' '*|*$'\t'*|*$'\r'*|*$'\n'*|*'/'*|*'?'*|*'#'*|*'&'*) return 0 ;;
  esac
  return 1
}

# The provider's host, for the report: identifies it without printing the key.
host_of() {
  local rest="${1#https://}"
  rest="${rest%%/*}"
  printf '%s' "${rest%%\?*}"
}

report() {  # status label ref detail
  printf '%-4s %-16s %-26s %s\n' "$1" "$2" "$3" "$4"
}

ok=0; failed=0; skipped=0

# One line per endpoint: group, its 1-based position (the machine's health key
# uses the same one), the URL template and the ref. hex_cash is left out: its
# URL is itself the credential, and the enclave checks it against the published
# commitment rather than against a chain.
while IFS=$'\t' read -r group index url ref; do
  label="rpc_${group}_${index}"
  shown_ref="${ref:-(no key)}"
  want="$(chain_id_for "$group")"
  if [ -z "$want" ]; then
    report FAIL "$label" "$shown_ref" "unknown group — not a chain this build reads"
    failed=$((failed + 1)); continue
  fi
  if [ -z "$url" ]; then
    report skip "$label" "$shown_ref" "private endpoint — its URL is the credential"
    skipped=$((skipped + 1)); continue
  fi
  host="$(host_of "$url")"

  target="$url"
  if [ -n "$ref" ]; then
    var="$(env_var_for "$ref")"
    if [ -z "$var" ]; then
      report FAIL "$label" "$ref" "$host — the ref is not a legal variable name"
      failed=$((failed + 1)); continue
    fi
    key="${!var:-}"
    if [ -z "$key" ]; then
      report skip "$label" "$ref" "$host — $var is not set"
      skipped=$((skipped + 1)); continue
    fi
    if unsafe_credential "$key"; then
      report FAIL "$label" "$ref" "$host — $var contains a space, / ? # or &, which the enclave refuses"
      failed=$((failed + 1)); continue
    fi
    target="${url/\{secret\}/$key}"
  fi

  response="$(printf 'url = "%s"\n' "$target" | curl -s -m 12 -K - -w '\n%{http_code}' -X POST \
      -H 'Content-Type: application/json' \
      --data '{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}' || true)"
  code="${response##*$'\n'}"
  got="$(printf '%s' "${response%$'\n'*}" | jq -r '.result // empty' 2>/dev/null || true)"

  if [ "$got" = "$want" ]; then
    report ok "$label" "$shown_ref" "$host — chain $got"
    ok=$((ok + 1))
  elif [ -n "$got" ]; then
    report FAIL "$label" "$shown_ref" "$host — serves chain $got, the group reads $want"
    failed=$((failed + 1))
  else
    report FAIL "$label" "$shown_ref" "$host — HTTP ${code:-none}, no JSON-RPC answer"
    failed=$((failed + 1))
  fi
done < <(jq -r '
  to_entries[] | select(.key != "hex_cash") | .key as $group
  | .value.endpoints | to_entries[]
  | [$group, (.key + 1 | tostring), (.value.url // ""), (.value.secret // "")]
  | @tsv' "$CONFIG")

echo
echo "$ok ok, $failed failed, $skipped skipped"
[ "$failed" -eq 0 ]
