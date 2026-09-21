#!/usr/bin/env bash
#
# check-versions.sh — fail fast when dependency pins drift apart.
#
# The extension module and the tools module both pin tee-node and
# go-flare-common; if they diverge, the node and the tooling disagree on
# signature and ABI formats and the symptom is an opaque failure at runtime,
# long after the build. Beyond those two, every module both files name is held
# at one version, so the tooling is built against the same library bytes as the
# enclave. tee-proxy is pinned twice as well: tools/go.mod and the tag of
# tee-proxy's published image in deploy/docker-compose.yaml.
#
# Run standalone; full-setup.sh and post-build.sh do not invoke it.
#
# Usage: ./deploy/scripts/check-versions.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; NC='\033[0m'
log()  { echo -e "${GREEN}[check-versions]${NC} $*"; }
warn() { echo -e "${YELLOW}[check-versions] WARN:${NC} $*" >&2; }
die()  { echo -e "${RED}[check-versions] ERROR:${NC} $*" >&2; exit 1; }

# shellcheck source=lib/versions.sh
# shellcheck source=lib/versions.sh
source "$SCRIPT_DIR/lib/versions.sh"
load_versions "$PROJECT_DIR" || die "could not load versions"

EXT_GOMOD="$PROJECT_DIR/go.mod"
TOOLS_GOMOD="$PROJECT_DIR/tools/go.mod"

pin() { grep -E "^[[:space:]]*${2//\//\\/}[[:space:]]+v" "$1" 2>/dev/null | head -1 | awk '{print $2}'; }

FAILED=0

# --- 1. tee-node must match between the extension module and tools ---
EXT_NODE="$(pin "$EXT_GOMOD" github.com/flare-foundation/tee-node)"
TOOLS_NODE="$(pin "$TOOLS_GOMOD" github.com/flare-foundation/tee-node)"
if [[ -z "$EXT_NODE" ]]; then
    die "no tee-node pin in $EXT_GOMOD"
elif [[ -z "$TOOLS_NODE" ]]; then
    warn "no tee-node pin in $TOOLS_GOMOD — skipping cross-check"
elif [[ "$EXT_NODE" != "$TOOLS_NODE" ]]; then
    echo -e "${RED}  tee-node mismatch:${NC}" >&2
    echo "    $EXT_GOMOD:   $EXT_NODE" >&2
    echo "    $TOOLS_GOMOD: $TOOLS_NODE" >&2
    FAILED=1
else
    log "tee-node       $EXT_NODE (extension == tools)"
fi

# --- 2. go-flare-common must match too (drift here breaks ABI encoding) ---
EXT_COMMON="$(pin "$EXT_GOMOD" github.com/flare-foundation/go-flare-common)"
TOOLS_COMMON="$(pin "$TOOLS_GOMOD" github.com/flare-foundation/go-flare-common)"
if [[ -n "$EXT_COMMON" && -n "$TOOLS_COMMON" && "$EXT_COMMON" != "$TOOLS_COMMON" ]]; then
    echo -e "${RED}  go-flare-common mismatch:${NC}" >&2
    echo "    $EXT_GOMOD:   $EXT_COMMON" >&2
    echo "    $TOOLS_GOMOD: $TOOLS_COMMON" >&2
    FAILED=1
elif [[ -n "$EXT_COMMON" ]]; then
    log "go-flare-common $EXT_COMMON (extension == tools)"
fi

# --- 3. every module both go.mod files name must be at one version ---
# tools requires the extension module, so the two dependency sets overlap. The
# resolver would quietly take the higher version wherever they differ, which
# means the tooling and the enclave could be built against different bytes of
# the same library without either file saying so. Held identical instead.
DRIFT=0
while read -r m; do
    a="$(pin "$EXT_GOMOD" "$m")"; b="$(pin "$TOOLS_GOMOD" "$m")"
    if [[ "$a" != "$b" ]]; then
        [[ "$DRIFT" -eq 0 ]] && echo -e "${RED}  shared dependencies at different versions:${NC}" >&2
        echo "    $m: extension $a, tools $b" >&2
        DRIFT=1
    fi
done < <(comm -12 <(grep -E '^[[:space:]]+[^ ]+ v' "$EXT_GOMOD" | awk '{print $1}' | sort) \
                  <(grep -E '^[[:space:]]+[^ ]+ v' "$TOOLS_GOMOD" | awk '{print $1}' | sort))
if [[ "$DRIFT" -ne 0 ]]; then
    FAILED=1
else
    log "shared modules  every module both files name is at one version"
fi

# --- 4. tee-proxy: tools/go.mod must match the image tag docker-compose.yaml runs ---
TOOLS_PROXY="$(pin "$TOOLS_GOMOD" github.com/flare-foundation/tee-proxy)"
if [[ -z "$TEE_PROXY_VERSION" ]]; then
    warn "no ghcr.io/flare-foundation/tee-proxy:<tag> in deploy/docker-compose.yaml — skipping cross-check"
elif [[ -z "$TOOLS_PROXY" ]]; then
    warn "no tee-proxy pin in $TOOLS_GOMOD — skipping cross-check"
elif [[ "$TOOLS_PROXY" != "$TEE_PROXY_VERSION" ]]; then
    echo -e "${RED}  tee-proxy mismatch:${NC}" >&2
    echo "    $TOOLS_GOMOD:        $TOOLS_PROXY" >&2
    echo "    docker-compose.yaml: $TEE_PROXY_VERSION" >&2
    FAILED=1
else
    log "tee-proxy      $TOOLS_PROXY (tools == docker-compose.yaml)"
fi

if [[ "$FAILED" -ne 0 ]]; then
    echo "" >&2
    die "version pins are inconsistent; align them before building"
fi

log "all version pins consistent"
