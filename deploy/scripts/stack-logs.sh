#!/usr/bin/env bash
# stack-logs.sh — follow the host-process stack's log in the terminal.
#
# The stack (tools/cmd/local-stack: the proxy in-process, the extension TEE as
# its child) runs detached, because post-build and the follow-up scripts talk
# to it while it comes up. This attaches a terminal to what it prints, from the
# top of the current run, and says so when the stack exits underneath it.
#
# Usage:
#   ./deploy/scripts/stack-logs.sh          # follow; Ctrl-C detaches, the stack keeps running
#   ./deploy/scripts/stack-logs.sh --stop   # follow; Ctrl-C stops the stack
#
# --stop is what full-setup-host.sh --attach uses: once the setup is done the
# terminal shows the proxy and node logs and Ctrl-C ends them, the way a
# foreground process would.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
LOGFILE="$PROJECT_DIR/out/logs/local-stack.log"
PIDFILE="$PROJECT_DIR/out/pids/local-stack.pid"

STOP=false
case "${1:-}" in
    "") ;;
    --stop) STOP=true ;;
    *) echo "usage: $0 [--stop]" >&2; exit 2 ;;
esac

note() { echo -e "\033[0;36m[stack-logs]\033[0m $*" >&2; }
die()  { echo -e "\033[0;31m[stack-logs]\033[0m $*" >&2; exit 1; }

[[ -f "$LOGFILE" ]] || die "no host stack log at out/logs/local-stack.log — start one with ./deploy/scripts/full-setup-host.sh"

PID=""
if [[ -f "$PIDFILE" ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
    PID="$(cat "$PIDFILE")"
fi
if [[ -z "$PID" ]]; then
    note "the stack is not running; this is the log of its last run"
    cat "$LOGFILE"
    exit 0
fi

if [[ "$STOP" == true ]]; then
    note "following local-stack (pid $PID); Ctrl-C stops the stack"
else
    note "following local-stack (pid $PID); Ctrl-C detaches and leaves it running"
fi
echo >&2

# tail runs in the background so the stack can be watched alongside it: a
# stack that exits on its own would otherwise leave a silent tail waiting for
# lines that never come.
tail -n +1 -F "$LOGFILE" &
TAIL_PID=$!
(
    while kill -0 "$PID" 2>/dev/null; do sleep 2; done
    echo >&2
    note "local-stack (pid $PID) has exited"
    kill "$TAIL_PID" 2>/dev/null
) &
WATCH_PID=$!

# Ctrl-C reaches tail as well, so the trap only has to make sure it is gone.
trap 'kill "$TAIL_PID" 2>/dev/null' INT TERM
wait "$TAIL_PID" 2>/dev/null || true
kill "$WATCH_PID" 2>/dev/null || true
trap - INT TERM

echo >&2
if [[ "$STOP" == true ]]; then
    if kill -0 "$PID" 2>/dev/null; then
        note "stopping the host stack"
        exec "$SCRIPT_DIR/stop-services.sh" --local
    fi
    note "the stack had already exited; nothing to stop"
else
    if kill -0 "$PID" 2>/dev/null; then
        note "detached; the stack keeps running (stop it with ./deploy/scripts/stop-services.sh)"
    fi
fi
