#!/usr/bin/env bash
# env.sh — how the scripts load their configuration.
#
# Sourced by the scripts; not executable on its own. Expects PROJECT_DIR.
#
# load_env reads .env, the active chain, and then deploy/config/stack.env, which
# start-services.sh writes for the local simulation with the proxy URL of the
# stack it brought up (6674 for the compose stack, 6684 for host processes) and
# stop-services.sh removes. The recorded stack is authoritative: it outranks .env
# and an EXT_PROXY_URL already in the environment, which the e2e repository's
# scripts also use, so a stray value from that shell cannot redirect these. The
# tools' -p flag is the explicit override. Without a recorded stack, as on the
# testnets, the environment and .env apply as before.

load_env() {
    # shellcheck source=/dev/null
    [[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }
    # shellcheck source=/dev/null
    [[ -f "$PROJECT_DIR/deploy/config/stack.env" ]] && { set -a; source "$PROJECT_DIR/deploy/config/stack.env"; set +a; }
    return 0
}

# record_stack <docker|host> <proxy-url> — what start-services.sh brought up:
# the proxy the follow-up scripts address. Everything about the machine,
# including its state, is read through that proxy.
record_stack() {
    mkdir -p "$PROJECT_DIR/deploy/config"
    {
        echo "# Written by start-services.sh for the local simulation: the stack it brought up"
        echo "# last. The scripts and the Go tools read it after .env and the environment, so"
        echo "# the follow-up commands are the same for either stack. Removed by"
        echo "# stop-services.sh."
        echo "STACK=$1"
        echo "EXT_PROXY_URL=$2"
    } > "$PROJECT_DIR/deploy/config/stack.env"
}

# read_machine_readiness <proxy-url> [api-key] — the machine's signed readiness
# report as JSON: the provisioning block (configuration hashes, ready flag,
# missing secret refs) and the poller's own view (observation age, whether it
# would sign, its counters, source health), with the signer. It is the one way
# the scripts read a machine: GET_READINESS over the proxy's direct channel,
# which needs the proxy's key (DIRECT_API_KEY unless given; the simulation's
# proxy accepts any). The extension publishes nothing else about itself — its
# GET /state carries the empty state.
#
# Exit status: 0 with the JSON on stdout; 1 when the machine could not be asked
# or did not answer, with the reason on stderr.
read_machine_readiness() {
    local root="${PROJECT_DIR:-${REPO_ROOT:?}}" key="${2:-${DIRECT_API_KEY:-}}"
    ( cd "$root/tools" && go run ./cmd/readiness -p "${1%/}" -directApiKey "$key" -timeout 20s )
}

# read_extension_readiness <extension-url> — the same report, asked of a dev
# server's own /action: the development harness has no proxy.
read_extension_readiness() {
    local root="${PROJECT_DIR:-${REPO_ROOT:?}}"
    ( cd "$root/tools" && go run ./cmd/readiness -extension "${1%/}" -timeout 20s )
}
