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

# read_machine_state <proxy-or-node-url> — the extension's State as JSON, read
# out of GET /info. tee-node reads the extension's GET /state on every info
# request and carries the state bytes, attested, in teeInfo.state.state; the
# bytes are the JSON encoding of the state. This is the one way the scripts
# read the state: the extension's own port is loopback-only inside the machine,
# and mocknode folds the dev server's state in the same way for the harness.
#
# Exit status: 0 with the JSON on stdout; 1 when the URL did not answer or
# carries no state (a tee-node that does not fold it in); 2 when tee-node
# reported it could not read its extension (StateVersion is the all-ones
# sentinel), which clears once the extension answers again.
read_machine_state() {
    local info ver hex
    info="$(curl -fsS -m 10 "${1%/}/info" 2>/dev/null)" || return 1
    ver="$(jq -r '.teeInfo.state.stateVersion // empty' <<<"$info" 2>/dev/null)" || return 1
    hex="$(jq -r '.teeInfo.state.state // empty' <<<"$info" 2>/dev/null)" || return 1
    [[ "$ver" == "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" ]] && return 2
    [[ -n "$hex" && "$hex" != "0x" ]] || return 1
    printf '%s' "${hex#0x}" | xxd -r -p
}
