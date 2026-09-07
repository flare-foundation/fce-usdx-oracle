#!/usr/bin/env bash
# full-setup-host.sh — full-setup.sh --chain local with the extension TEE and its
# proxy as host processes (tools/cmd/local-stack) instead of containers.
#
# Usage:
#   USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh   # against the sibling tee-node, as a develop simulation needs
#   ./deploy/scripts/full-setup-host.sh                        # against the pinned tee-node
#   LOCAL_STACK_REDIS=:6390 ./deploy/scripts/full-setup-host.sh   # another redis for the proxy queue
#
# The proxy binds 6664 on the host, so the follow-up scripts need
# EXT_PROXY_URL=http://localhost:6664; post-build.sh gets it here.
# Stop: ./deploy/scripts/stop-services.sh --local
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
[[ $# -eq 0 ]] || { echo "usage: $0 (LOCAL_STACK_REDIS=<addr> to pick the redis)" >&2; exit 2; }
EXT_PROXY_URL="http://localhost:6664" exec "$SCRIPT_DIR/full-setup.sh" --chain local --local
