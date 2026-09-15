#!/usr/bin/env bash
# full-setup-host.sh — full-setup.sh --local: the extension TEE and its proxy as
# host processes (tools/cmd/local-stack) instead of containers, on the local
# simulation by default or on a testnet with --chain.
#
# Usage:
#   USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh   # against the sibling tee-node, as a develop simulation needs
#   ./deploy/scripts/full-setup-host.sh                        # against the pinned tee-node
#   ./deploy/scripts/full-setup-host.sh --attach               # then keep the terminal on the proxy and node logs; Ctrl-C stops the stack
#   ./deploy/scripts/full-setup-host.sh --chain <chain> --attach  # a live chain: needs the chain's .env, its indexer compose
#                                                                 # file up, and the tunnel behind EXT_PROXY_URL forwarding to 6684
#   LOCAL_STACK_REDIS=:6390 ./deploy/scripts/full-setup-host.sh   # another redis for the proxy queue
#
# The proxy binds 6684 on the host. On the simulation start-services.sh records
# it in deploy/config/stack.env for the follow-up scripts; on a testnet they use
# EXT_PROXY_URL from .env, the public URL the machine is registered under.
#
# The stack starts detached either way, because post-build.sh registers the
# machine against it while it runs. --attach follows its log once the setup is
# done (stack-logs.sh --stop), so from then on the terminal behaves as if the
# proxy and node were running in the foreground. Without --attach the stack
# keeps running in the background and stack-logs.sh attaches at any time.
# Stop: ./deploy/scripts/stop-services.sh --local
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

ATTACH=false
CHAIN=local
while [[ $# -gt 0 ]]; do
    case "$1" in
        --attach) ATTACH=true; shift ;;
        --chain) [[ $# -ge 2 ]] || { echo "--chain requires a value (local|coston|coston2|songbird|flare)" >&2; exit 2; }
                 CHAIN="$2"; shift 2 ;;
        --chain=*) CHAIN="${1#--chain=}"; shift ;;
        *) echo "usage: $0 [--chain local|coston|coston2|songbird|flare] [--attach]   (LOCAL_STACK_REDIS=<addr> to pick the redis)" >&2; exit 2 ;;
    esac
done

if [[ "$ATTACH" == true ]]; then
    "$SCRIPT_DIR/full-setup.sh" --chain "$CHAIN" --local || exit 1
    exec "$SCRIPT_DIR/stack-logs.sh" --stop
fi
exec "$SCRIPT_DIR/full-setup.sh" --chain "$CHAIN" --local
