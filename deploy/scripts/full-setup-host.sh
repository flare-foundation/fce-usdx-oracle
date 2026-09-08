#!/usr/bin/env bash
# full-setup-host.sh — full-setup.sh --chain local with the extension TEE and its
# proxy as host processes (tools/cmd/local-stack) instead of containers.
#
# Usage:
#   USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh   # against the sibling tee-node, as a develop simulation needs
#   ./deploy/scripts/full-setup-host.sh                        # against the pinned tee-node
#   ./deploy/scripts/full-setup-host.sh --attach               # then keep the terminal on the proxy and node logs; Ctrl-C stops the stack
#   LOCAL_STACK_REDIS=:6390 ./deploy/scripts/full-setup-host.sh   # another redis for the proxy queue
#
# The proxy binds 6684 on the host; start-services.sh records it in
# deploy/config/stack.env, which the follow-up scripts and tools read.
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
while [[ $# -gt 0 ]]; do
    case "$1" in
        --attach) ATTACH=true; shift ;;
        *) echo "usage: $0 [--attach]   (LOCAL_STACK_REDIS=<addr> to pick the redis)" >&2; exit 2 ;;
    esac
done

if [[ "$ATTACH" == true ]]; then
    "$SCRIPT_DIR/full-setup.sh" --chain local --local || exit 1
    exec "$SCRIPT_DIR/stack-logs.sh" --stop
fi
exec "$SCRIPT_DIR/full-setup.sh" --chain local --local
