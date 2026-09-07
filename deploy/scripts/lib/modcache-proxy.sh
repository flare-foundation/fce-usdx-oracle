#!/usr/bin/env bash
# Serve the host's Go module cache to Docker builds.
#
# The module cache's cache/download tree is a complete GOPROXY file layout, so a
# static file server over it is a module proxy. Builds from sibling checkouts
# download several hundred modules; on a slow link that takes far longer than the
# compile, while the host cache already holds everything a workspace build
# resolved. Steps inside a build reach the host through host.docker.internal,
# which for build sandboxes maps to the VM gateway rather than the host loopback,
# so the server listens on all interfaces; it serves public module archives
# read-only and only for the duration of the calling script.
#
# Callers source this file and call modcache_proxy_start, which exports
# GOPROXY_BUILD: a GOPROXY value listing the host cache first and the public proxy
# as fallback, for use as a build argument. Without a usable cache it keeps the
# public-only default, so builds still work, just slower. The server stops on
# modcache_proxy_stop or when the calling shell exits. Expects log() and warn()
# from the calling script.

MODCACHE_PROXY_PID=""
export GOPROXY_BUILD="${GOPROXY_BUILD:-https://proxy.golang.org,direct}"

# Arguments: directory to serve, port. Serves on all interfaces (see above).
MODCACHE_PROXY_SERVER='
import http.server, os, sys
os.chdir(sys.argv[1])
class Handler(http.server.SimpleHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *_):
        pass
class Server(http.server.ThreadingHTTPServer):
    request_queue_size = 256
    daemon_threads = True
Server(("0.0.0.0", int(sys.argv[2])), Handler).serve_forever()
'

modcache_proxy_start() {
    local dir port i
    [[ -n "$MODCACHE_PROXY_PID" ]] && return 0
    if ! command -v go >/dev/null 2>&1 || ! command -v python3 >/dev/null 2>&1; then
        warn "go or python3 not on PATH; builds download modules from the public proxy"
        return 0
    fi
    dir="$(go env GOMODCACHE)/cache/download"
    if [[ ! -d "$dir" ]]; then
        warn "no module cache at $dir; builds download modules from the public proxy"
        return 0
    fi
    port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
    # A persistent-connection server with a deep listen backlog: go fetches
    # several hundred files in parallel, and one connection per file through the
    # VM gateway overflows the stock server's backlog of five, which the client
    # sees as dial timeouts.
    python3 -c "$MODCACHE_PROXY_SERVER" "$dir" "$port" >/dev/null 2>&1 &
    MODCACHE_PROXY_PID=$!
    for i in $(seq 20); do
        curl -sf -o /dev/null "http://127.0.0.1:$port/" && break
        sleep 0.25
    done
    if ! curl -sf -o /dev/null "http://127.0.0.1:$port/"; then
        warn "module cache server did not come up; builds download modules from the public proxy"
        modcache_proxy_stop
        return 0
    fi
    export GOPROXY_BUILD="http://host.docker.internal:$port,https://proxy.golang.org,direct"
    trap modcache_proxy_stop EXIT
    log "Serving host module cache to builds: $dir on 127.0.0.1:$port"
}

modcache_proxy_stop() {
    if [[ -n "$MODCACHE_PROXY_PID" ]]; then
        kill "$MODCACHE_PROXY_PID" 2>/dev/null || true
        wait "$MODCACHE_PROXY_PID" 2>/dev/null || true
        MODCACHE_PROXY_PID=""
    fi
}
