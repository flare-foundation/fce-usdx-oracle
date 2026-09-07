#!/usr/bin/env bash
#
# versions.sh — the dependency pins the scripts compare.
#
# Sourced by check-versions.sh; not executable on its own.
#
# Exports:
#   TEE_NODE_VERSION  the tee-node module pin in go.mod, e.g. v0.0.21-0.20260619120252-31fc839ae6d2
#   TEE_PROXY_VERSION the tee-proxy tag deploy/proxy/Dockerfile builds

# Extract a module's version from a go.mod require line.
_versions_pin() {
    local gomod="$1" module="$2"
    grep -E "^[[:space:]]*${module//\//\\/}[[:space:]]+v" "$gomod" 2>/dev/null \
        | head -1 | awk '{print $2}'
}

# load_versions <project_dir> — sets and exports the version variables.
load_versions() {
    local root="${1:?load_versions requires the project dir}"
    local gomod="$root/go.mod"
    [[ -f "$gomod" ]] || { echo "[versions] ERROR: no go.mod at $gomod" >&2; return 1; }
    TEE_NODE_VERSION="$(_versions_pin "$gomod" "github.com/flare-foundation/tee-node")"
    if [[ -z "$TEE_NODE_VERSION" ]]; then
        echo "[versions] ERROR: no tee-node require line in $gomod" >&2
        return 1
    fi
    TEE_PROXY_VERSION="$(grep -E '^ARG TEE_PROXY_VERSION=' "$root/deploy/proxy/Dockerfile" 2>/dev/null | head -1 | cut -d= -f2)"
    export TEE_NODE_VERSION TEE_PROXY_VERSION
}
