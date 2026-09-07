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
    [[ -f "$PROJECT_DIR/.env" ]] && { set -a; source "$PROJECT_DIR/.env"; set +a; }
    [[ -f "$PROJECT_DIR/deploy/config/stack.env" ]] && { set -a; source "$PROJECT_DIR/deploy/config/stack.env"; set +a; }
    return 0
}

# record_stack <docker|host> <proxy-url> [state-url] — what start-services.sh
# brought up: the proxy the follow-up scripts address and, for host processes,
# where the extension serves GET /state (the compose stack's is reached through
# its container).
record_stack() {
    mkdir -p "$PROJECT_DIR/deploy/config"
    {
        echo "# Written by start-services.sh for the local simulation: the stack it brought up"
        echo "# last. The scripts and the Go tools read it after .env and the environment, so"
        echo "# the follow-up commands are the same for either stack. Removed by"
        echo "# stop-services.sh."
        echo "STACK=$1"
        echo "EXT_PROXY_URL=$2"
        [[ -n "${3:-}" ]] && echo "EXTENSION_STATE_URL=$3"
    } > "$PROJECT_DIR/deploy/config/stack.env"
}
