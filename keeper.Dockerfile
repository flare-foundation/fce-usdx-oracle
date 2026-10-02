#checkov:skip=CKV_DOCKER_2:HEALTHCHECK not applicable, the keeper listens on nothing — what says it is alive is the records file and the age of observedAt on chain
# The keeper as a container: the process that drives the USDX feed on a timer —
# request, collect the machines' signed updates, preflight, submit. What it needs
# to run unattended is docs/usdx-oracle-keeper.md § "Running it unattended"; this
# file is that list, packaged.
#
# It carries a second binary, cycles-web, which serves the keeper's records file
# and the page that reads it. Run beside the keeper over a shared volume holding
# that file, it is the keeper's dashboard; run without it, the keeper alone is
# the whole deployment and the second binary sits unused.
#
# It is not the extension image, and deliberately shares none of its build. Nothing
# here is attested, so the apt snapshot, the SOURCE_DATE_EPOCH clamping and the
# mtime normalisation in ./Dockerfile are all absent: those exist so an auditor can
# re-derive the code hash a machine registers, and a keeper registers nothing. It
# holds no authority either — both of its transactions are permissionless and the
# store verifies TEE signatures against the machine registry itself — so the one
# secret in here is a key that pays gas.
#
# Build, from the repository root:
#
#   docker build -f keeper.Dockerfile -t usdx-keeper:latest .
#
# The context is the root because tools/ is a module of its own that replaces
# fce-usdx-oracle => ../, so the build needs both modules' sources. The root
# .dockerignore admits only the extension module's — and the attested image copies
# the whole context with `COPY . .`, so widening it would change that image's
# layers and with them the code hash a machine registers. This build therefore
# brings its own ignore file, keeper.Dockerfile.dockerignore, which BuildKit
# prefers over .dockerignore when it sits beside the named Dockerfile. BuildKit is
# docker's default builder since 23; under the legacy builder the root
# .dockerignore applies instead and the build fails at `COPY tools/` rather than
# producing a wrong image.
#
# The name is keeper.Dockerfile and not Dockerfile.keeper because CI lints every
# tracked path whose basename starts with "Dockerfile" — the regex is
# `(^|/)\.?[Dd]ockerfile[^/]*$` — which swept up the .dockerignore beside it, and
# hadolint cannot parse one of those. An ignore file must be named after its
# Dockerfile, so the Dockerfile is what had to move out of the way. The cost is
# that CI's hadolint does not see this file either; it is checked by hand with
# `docker run --rm -i hadolint/hadolint hadolint - < keeper.Dockerfile`.

# Not pinned by digest, unlike the extension image: this one is not attested, and a
# long-running keeper is better off taking the base's security fixes on a rebuild.
# Pinned to the minor rather than the patch because tools/go.mod asks for go 1.25.8,
# and golang:1.25.1 — what the extension image pins — would send the build off to
# download a toolchain.
FROM golang:1.25-trixie AS builder

# WORKDIR is the tooling module for the whole stage; the root module's two files
# are copied to an absolute path beside it, so no build step has to change
# directory to reach one or the other.
WORKDIR /src/tools

# Both manifests before any source, so the dependency download is cached apart from
# the code. tools/go.mod resolves `replace fce-usdx-oracle => ../` through the root
# manifest, which therefore has to be in place before `go mod download` runs.
COPY go.mod go.sum /src/
COPY tools/go.mod tools/go.sum ./
RUN go mod download

# pkg/ is the half the tooling shares with the machines: the wire formats, the feed
# id, the op tags. Nothing under internal/ is reachable from tools/, so it is not
# copied.
COPY pkg/ /src/pkg/
COPY tools/ ./

# CGO_ENABLED=0, where the extension image deliberately turns cgo on: the native
# libsecp256k1 earns its toolchain and static-link dance on a machine that signs
# constantly, and buys nothing in a process that signs two transactions every ten
# minutes. Off, the build needs no gcc and the binary needs no libc at runtime.
# -trimpath keeps build-host paths out of it, -s -w drop the symbol and dwarf tables.
#
# Two binaries, one image, because they run as two containers of one deployment
# over a shared volume: the keeper writes the records and cycles-web serves them.
# Separate processes, so only one of them is ever given KEEPER_KEY; separate
# images would only mean two things to build, push and keep in step for what is
# one deployment.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /out/keeper ./cmd/keeper \
  && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /out/cycles-web ./cmd/cycles-web

# The records directory, made here because the final image has no shell to make one
# in, and the keeper's own MkdirAll cannot create it: /var/log is root-owned and the
# process is not. Ownership carries into a fresh named volume, which docker seeds
# from the image; a bind mount brings the host's ownership instead and has to be
# chowned to 65532 there.
# hadolint ignore=DL3059
RUN mkdir -p /records

# distroless/static carries what a static Go binary needs and nothing else: the CA
# bundle for the TLS to the chain node and to the proxies, the nsswitch/passwd/hosts
# stubs the pure-Go resolvers read, no shell, no package manager. :nonroot is that
# image with the unprivileged uid preset.
FROM gcr.io/distroless/static-debian12:nonroot

# The chain this image is built for: it picks the addresses file baked in below and
# the RPC the tools fall back to. Another network is the same build with
# --build-arg CHAIN=coston2.
ARG CHAIN=coston

COPY --from=builder /out/keeper /usr/local/bin/keeper
COPY --from=builder /out/cycles-web /usr/local/bin/cycles-web
COPY --from=builder --chown=65532:65532 /records /var/log/usdx

# The only repository file the keeper reads at runtime. Baked, so the image names
# the deployment it drives and a run needs no mount; a deployment of its own is
# `-v /opt/usdx/deployed-addresses.json:/opt/usdx/deployed-addresses.json:ro`.
# Rebuild after a redeployment: the store address in here is the one it will use.
COPY deploy/config/${CHAIN}/deployed-addresses.json /opt/usdx/deployed-addresses.json

# ADDRESSES_FILE has to be absolute and set: the compiled-in default resolves
# against the source tree as it stood at build time, which does not exist in this
# image. FLARE_CHAIN is what CHAIN_URL falls back to when it is not passed — unset,
# the tools take the run for the local simulation and dial 127.0.0.1:8545.
ENV ADDRESSES_FILE=/opt/usdx/deployed-addresses.json \
  FLARE_CHAIN=${CHAIN}

# Passed per run, never baked: KEEPER_KEY, the funded key and the only secret the
# process requires (through --env-file rather than -e, so it stays out of the shell
# history and out of `ps`); DIRECT_API_KEY, which buys the readiness preflight —
# without it every cycle pays the registry fee before asking the machines whether
# they would sign at all; CHAIN_URL; and EXT_PROXY_URL.

# distroless's nonroot uid, set explicitly because the records directory above is
# chowned to it. Nothing here wants root: no listening socket, no privileged port,
# one file written.
USER 65532:65532

# SIGTERM cancels the cycle in flight, which unwinds at its next checkpoint and
# exits 0. Allow around 90 seconds for that — docker's default stop timeout is 10,
# and a second signal kills a process that was winding down cleanly:
# `--stop-timeout 90`, or stop_grace_period: 90s under compose.
STOPSIGNAL SIGTERM

# No EXPOSE: the keeper listens on nothing. Outbound only, HTTPS to the chain node
# and to each machine's proxy. The records page does listen, on 8080, but it is a
# different container with a different command — a port declared here would claim
# the keeper opens one.
ENTRYPOINT ["/usr/local/bin/keeper"]

# Arguments given to `docker run` REPLACE this list rather than adding to it, and
# -tee has no default worth guessing, so a real invocation restates them:
#   docker run ... usdx-keeper -tee 0xA,0xB -interval 10m -records /var/log/usdx/cycles.jsonl
# Worth adding there: -keeperId. It defaults to the hostname, which under docker is
# the container id: new whenever the container is recreated (not on a plain
# `docker restart`), so without one records stop being attributable across redeploys.
CMD ["-interval", "10m", "-records", "/var/log/usdx/cycles.jsonl"]
