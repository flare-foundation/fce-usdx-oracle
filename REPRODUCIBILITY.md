# Reproducible Builds

The image's code hash is what gets registered on-chain, so build determinism is
a security property rather than a nicety.

## What is guaranteed

Be precise about what is actually guaranteed. Overclaiming here is worse than
underclaiming, because the on-chain registration is what depends on it.

This repository ships a single Go implementation, and the image is
**bit-for-bit reproducible across machines**: one statically linked binary built
with `-trimpath -buildid=` on a digest-pinned distroless base, from tee-node's
own Dockerfile with only the package built, the launch-policy label and the
command changed. Nothing host-specific survives, so an auditor on different
hardware can independently reproduce the code hash that gets registered
on-chain.

tee-node is not a separate binary: the extension embeds it as the Go module
`go.mod` pins, and `deploy/scripts/check-versions.sh` fails when the pins of
the two Go modules and of the proxy image drift apart.

## How it works

- `SOURCE_DATE_EPOCH` is set to the commit timestamp and passed as a build arg
  to clamp all timestamps and to select the Debian snapshot below. It is
  required: a build without it fails, and a zero would ask for a 1970 snapshot
- Go binary is built with `-trimpath -ldflags="-buildid= -s -w"` and
  `-buildvcs=false` to strip non-deterministic metadata; `CGO_ENABLED=1` with
  `-linkmode=external -extldflags=-static` links go-ethereum's native
  libsecp256k1 and still produces one static binary, the `netgo,osusergo` tags
  keep the resolvers pure Go, and the cgo toolchain (`gcc`, `libc6-dev`) comes
  from the same Debian snapshot as everything else
- Both base images are pinned by digest in the Dockerfile: the golang builder
  and `distroless/static-debian12`, whose own trust store the binary uses
- Debian package versions are pinned via apt's native snapshot support
  (Debian 13+): `Snapshot: true` in the sources file plus
  `apt-get install --snapshot <SOURCE_DATE_EPOCH>` redirects every fetch to
  [snapshot.debian.org](https://snapshot.debian.org) at the exact instant of
  the commit, so the same `SOURCE_DATE_EPOCH` always yields the same package
  bytes. Adapted from
  [reproducible-containers/repro-sources-list.sh](https://github.com/reproducible-containers/repro-sources-list.sh/blob/master/alternative/Dockerfile.debian-13)
- CI uses BuildKit's [`rewrite-timestamp=true`](https://github.com/moby/buildkit/pull/4057)
  exporter option to normalize layer timestamps, and on every tag offers a
  manual `verify-reproducible` job that rebuilds the published image cold and
  compares code hashes

## Build context

The default build is self-contained: the build context is the repo root
(`deploy/docker-compose.yaml` sets `context: ..`, `dockerfile: Dockerfile` — the root is the
Go module). `go.mod` pins
`github.com/flare-foundation/tee-node` to a released version and fetches it from
the network (verified against `go.sum`), so the build needs only this repo's own
sources — no sibling `tee-node/` checkout.

.dockerignore admits only the module's own sources — `go.mod`, `go.sum`,
`cmd/`, `internal/` and `pkg/` — and excludes everything else, in tee-node's
form. This is not only a build-speed concern: anything reachable in the context
can perturb layer hashes, so a stray local file would otherwise undermine
determinism.

> **Developing `tee-node`/`tee-proxy` locally?** Run
> `USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh`, which builds from on-disk
> sibling checkouts via `Dockerfile.siblings.dev` (build context `tee/`). That
> path is for local iteration — it uses whatever is checked out and is **not**
> reproducible; the reproducible build uses the tee-node ref pinned in
> `go.mod` instead.

## Verifying a remote image

The default Docker builder does not properly support `rewrite-timestamp`
([moby/buildkit#4230](https://github.com/moby/buildkit/issues/4230)). You need
a BuildKit builder using the `docker-container` driver.

Create the builder (one-time setup):

```sh
docker buildx create --driver=docker-container --name=moby-buildkit --driver-opt image=moby/buildkit --bootstrap
```

Clone the extension repository (self-contained — no sibling `tee-node/` needed;
the pinned module is fetched from the network at build time):

```sh
git clone https://github.com/flare-foundation/extension-examples.git
```

Checkout the tag you want to verify, build locally, and compare the image ID
against the registry image. Run from `extension-examples/fce-usdx-oracle/`:

```sh
TAG=$(git describe --tags --abbrev=0)
git checkout "$TAG"

docker buildx build --builder moby-buildkit --platform linux/amd64 --no-cache --build-arg SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) --output "type=docker,rewrite-timestamp=true" -t local/fce-usdx-oracle:verify --load -f Dockerfile .

# Where CI publishes it (.gitlab-ci.yml); the ID below is the code hash registered on chain.
REGISTRY_IMAGE=europe-west1-docker.pkg.dev/flare-network-staging/containers/tee-usdx
docker pull --platform linux/amd64 "$REGISTRY_IMAGE:$TAG"

docker inspect --format='{{.Id}}' local/fce-usdx-oracle:verify
docker inspect --format='{{.Id}}' "$REGISTRY_IMAGE:$TAG"
```

Both IDs should be identical.

## Upstream references

- [moby/buildkit#3180](https://github.com/moby/buildkit/issues/3180) -
  `rewrite-timestamp` only clamps timestamps *down* to `SOURCE_DATE_EPOCH`,
  older timestamps are left unchanged. The Dockerfile works around this with
  an explicit `find + touch` to normalize all timestamps before COPY.
- [moby/buildkit#4057](https://github.com/moby/buildkit/pull/4057) - PR that
  added `rewrite-timestamp` support to BuildKit exporters
- [moby/buildkit#4230](https://github.com/moby/buildkit/issues/4230) - open
  issue tracking `rewrite-timestamp` incompatibility with the default Docker
  builder and `--load` (`unpack` conflict)
