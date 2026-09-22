#!/usr/bin/env bash
#
# build-keeper-image.sh — build the keeper image and push it to a registry.
#
# The deliberate opposite of build-image.sh. That one builds the image a
# Confidential Space VM attests, where the image ID becomes a code hash on chain
# and every byte has to be re-derivable. None of that applies here: the keeper
# registers nothing and holds no authority — both of its transactions are
# permissionless, and the store verifies TEE signatures against the machine
# registry itself — so no contract has ever heard of this image. There is no code
# hash to check, no SOURCE_DATE_EPOCH, and a rebuild is free to differ.
#
# What it does instead is keep an operator able to answer three questions about a
# container running on a server: which commit it was built from (the labels, and
# the record file below), which deployment's addresses are baked into it (checked
# and printed after the build), and whether the tag it is pinned to has been moved
# under it since (a published tag is not overwritten without --force).
#
# Usage:
#   ./deploy/scripts/build-keeper-image.sh                   # build v0.1.0, check it, push
#   ./deploy/scripts/build-keeper-image.sh --tag v0.2.0
#   ./deploy/scripts/build-keeper-image.sh --no-push         # build and check only
#   ./deploy/scripts/build-keeper-image.sh --chain coston2   # bake that chain's addresses
#   ./deploy/scripts/build-keeper-image.sh --allow-dirty     # publish a tag naming a commit the tree has moved past
#   ./deploy/scripts/build-keeper-image.sh --force           # move a tag that is already published
#
# The two escapes are separate on purpose: a dirty tree costs traceability, while
# moving a published tag changes what every server pinned to it runs on its next
# pull, and nobody should get the second by asking for the first.
#
# Inputs (env vars, or .env):
#   IMAGE_REPO     registry path without the tag (default: the staging path below)
#   IMAGE_TAG      tag to publish (default: v0.1.0)
#   KEEPER_CHAIN   the chain whose deployed-addresses.json is baked in (default: coston)
#
# Writes out/images/keeper-<tag>.env — the commit, the chain and the digest the
# tag now points at, so a keeper on a server can be traced back to a tree.
#
# Prerequisites: docker, jq, and push access
# (`gcloud auth configure-docker europe-west1-docker.pkg.dev` for Artifact Registry).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[build-keeper-image]${NC} $*"; }
step() { echo -e "\n${CYAN}=== $* ===${NC}"; }
warn() { echo -e "${YELLOW}[build-keeper-image] WARN:${NC} $*" >&2; }
die()  { echo -e "${RED}[build-keeper-image] ERROR:${NC} $*" >&2; exit 1; }

# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"; load_env

# The keeper image does not share the extension image's repository: one is
# attested and identified by its code hash, the other is ordinary software
# identified by a tag, and confusing the two is how an unattested image ends up
# named as if a VM could run it.
DEFAULT_IMAGE_REPO="europe-west1-docker.pkg.dev/flare-network-staging/containers/fce-usdx-keeper"

IMAGE_REPO="${IMAGE_REPO:-$DEFAULT_IMAGE_REPO}"
# A version tag rather than the commit build-image.sh defaults to: nothing derives
# an identity from this image, so what the tag has to carry is which release an
# operator asked for. The commit is kept in the labels and the record file.
IMAGE_TAG="${IMAGE_TAG:-v0.1.0}"
CHAIN="${KEEPER_CHAIN:-coston}"
PUSH=true
FORCE=false
ALLOW_DIRTY=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --image)       IMAGE_REPO="${2:?--image needs a registry path}"; shift 2 ;;
        --tag)         IMAGE_TAG="${2:?--tag needs a tag}"; shift 2 ;;
        --chain)       CHAIN="${2:?--chain needs a chain name}"; shift 2 ;;
        --no-push)     PUSH=false; shift ;;
        --force)       FORCE=true; shift ;;
        --allow-dirty) ALLOW_DIRTY=true; shift ;;
        -h|--help)     sed -n '2,40p' "$0"; exit 0 ;;
        *)             die "unknown argument: $1 (try --help)" ;;
    esac
done

command -v docker >/dev/null || die "docker not found"
command -v jq >/dev/null     || die "jq not found"

IMAGE="$IMAGE_REPO:$IMAGE_TAG"
ADDRESSES="deploy/config/$CHAIN/deployed-addresses.json"
[[ -f "$PROJECT_DIR/$ADDRESSES" ]] \
    || die "no $ADDRESSES — --chain names a directory under deploy/config that holds a deployed-addresses.json"

step "Source"

git -C "$PROJECT_DIR" rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository: $PROJECT_DIR"
COMMIT="$(git -C "$PROJECT_DIR" rev-parse HEAD)"
DIRTY=false
[[ -n "$(git -C "$PROJECT_DIR" status --porcelain)" ]] && DIRTY=true

log "Image:  $IMAGE"
log "Commit: $COMMIT$([[ "$DIRTY" == true ]] && echo ' (uncommitted changes)')"
log "Chain:  $CHAIN ($ADDRESSES)"

# A tag names a tree someone can check out again. Publishing one from a dirty
# working copy breaks that quietly, and it only surfaces months later, when the
# commit in the labels turns out not to be what is running.
if [[ "$DIRTY" == true ]]; then
    if [[ "$PUSH" == true && "$ALLOW_DIRTY" != true ]]; then
        die "the working tree has uncommitted changes, so $IMAGE_TAG would name a commit that does not contain them — commit them, or pass --allow-dirty to publish anyway"
    fi
    warn "building from a dirty working tree: $COMMIT is not what goes into this image"
fi

# Whether the tag is already published, asked before the build rather than after
# it: overwriting a tag moves it under every server pinned to it. A client that is
# unauthenticated or offline cannot tell "not published" from "cannot look", so
# this only ever blocks on a definite yes.
if [[ "$PUSH" == true && "$FORCE" != true ]]; then
    if docker buildx imagetools inspect "$IMAGE" >/dev/null 2>&1; then
        die "$IMAGE is already published — pick another --tag, or pass --force to move the tag and with it every server pinned to it"
    fi
fi

step "Build"

# BuildKit is not optional here. Dockerfile.keeper needs a wider context than the
# extension image — tools/ is a module of its own that replaces fce-usdx-oracle in
# from ../ — and it gets one through Dockerfile.keeper.dockerignore, which only
# BuildKit reads. The legacy builder would apply the root .dockerignore, which
# admits no tools/, and the build would fail at COPY.
export DOCKER_BUILDKIT=1

# The platform is fixed, as in build-image.sh, but for a different reason: the
# Dockerfile cross-compiles GOARCH=amd64 whatever the host is, so on an arm64
# machine the default would produce an image whose base and manifest say arm64
# around an amd64 binary — an image that runs nowhere.
docker build \
    --platform linux/amd64 \
    --build-arg "CHAIN=$CHAIN" \
    --label "org.opencontainers.image.title=fce-usdx-keeper" \
    --label "org.opencontainers.image.version=$IMAGE_TAG" \
    --label "org.opencontainers.image.revision=$COMMIT" \
    --label "usdx.keeper.chain=$CHAIN" \
    -t "$IMAGE" \
    -f "$PROJECT_DIR/Dockerfile.keeper" \
    "$PROJECT_DIR" \
    || die "build failed"

# ------------------------------------------------------------------
# What ended up in the image. None of this is a reproducibility check —
# each one is an image that would fail on a server at 3am rather than here.
# ------------------------------------------------------------------
step "Image checks"

platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$IMAGE")"
[[ "$platform" == "linux/amd64" ]] \
    || die "image is $platform, expected linux/amd64 — the binary inside is compiled for amd64 and would not run"
log "Platform: $platform"

user="$(docker image inspect --format '{{.Config.User}}' "$IMAGE")"
[[ "$user" == "65532:65532" ]] \
    || die "image runs as '${user:-root}', expected 65532:65532 — the records directory is chowned to that uid, and nothing here needs what root can do"
log "User: $user (nonroot)"

ports="$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$IMAGE")"
[[ "$ports" == "null" ]] \
    || die "image exposes $ports — the keeper listens on nothing, so an exposed port is a mistake in the Dockerfile"
log "Exposed ports: none"

entrypoint="$(docker image inspect --format '{{json .Config.Entrypoint}}' "$IMAGE")"
[[ "$entrypoint" == '["/usr/local/bin/keeper"]' ]] \
    || die "entrypoint is $entrypoint, expected the keeper binary"

image_env() {
    docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$IMAGE" \
        | grep -E "^$1=" | head -1 || true
}

addresses_env="$(image_env ADDRESSES_FILE)"
[[ "$addresses_env" == "ADDRESSES_FILE=/opt/usdx/deployed-addresses.json" ]] \
    || die "image bakes '${addresses_env:-<unset>}' — without an absolute ADDRESSES_FILE the keeper resolves it against a source tree that does not exist on a server"

chain_env="$(image_env FLARE_CHAIN)"
[[ "$chain_env" == "FLARE_CHAIN=$CHAIN" ]] \
    || die "image bakes '${chain_env:-<unset>}', expected FLARE_CHAIN=$CHAIN — unset, the tools take a run for the local simulation and dial 127.0.0.1:8545"
log "Environment: $addresses_env, $chain_env"

# The addresses file the keeper will actually read, taken back out of the image
# rather than read from the tree: this is the deployment the container drives, and
# it is the one thing in here that a rebuild after a redeployment silently
# changes. `docker cp` reads a created container's filesystem, so it needs no
# shell in the image and never starts it.
TMP_DIR="$(mktemp -d)"
CID=""
cleanup() {
    [[ -n "$CID" ]] && docker rm -f "$CID" >/dev/null 2>&1
    rm -rf "$TMP_DIR"
    return 0
}
trap cleanup EXIT

CID="$(docker create "$IMAGE")"
docker cp "$CID:/opt/usdx/deployed-addresses.json" "$TMP_DIR/addresses.json" >/dev/null \
    || die "no /opt/usdx/deployed-addresses.json in the image"

# The records page ships in the same image as a second binary, and the pod runs it
# as its own container. Missing, the keeper would still run and the deployment's
# web container would crash-loop on a rollout.
docker cp "$CID:/usr/local/bin/cycles-web" "$TMP_DIR/cycles-web" >/dev/null \
    || die "no /usr/local/bin/cycles-web in the image — the records page container would not start"
log "Binaries: keeper, cycles-web"

STORE="$(jq -r '.[] | select(.name == "TeeOracleFeedStore") | .address' "$TMP_DIR/addresses.json")"
SENDER="$(jq -r '.[] | select(.name == "TeeOracleInstructionsSender") | .address' "$TMP_DIR/addresses.json")"
[[ -n "$STORE" ]] \
    || die "the addresses file in the image has no TeeOracleFeedStore entry — the keeper resolves the store from it and would exit at startup"
# Not fatal: the keeper derives the sender from the store's own
# instructionsSender() on chain, and reads this entry only when -instructionSender
# is passed by hand.
[[ -n "$SENDER" ]] || warn "no TeeOracleInstructionsSender entry in the baked addresses file"
log "Feed store: $STORE"
log "Sender:     ${SENDER:-<derived from the store on chain>}"

echo
log "Built: $IMAGE"

# ------------------------------------------------------------------
# Push
# ------------------------------------------------------------------
DIGEST=""
if [[ "$PUSH" == true ]]; then
    step "Push"

    registry_host="${IMAGE_REPO%%/*}"
    log "Pushing $IMAGE ..."
    if ! docker push "$IMAGE"; then
        die "push failed. For Artifact Registry, authenticate the docker client once with:\n  gcloud auth configure-docker $registry_host"
    fi

    # The digest the registry assigned. A tag can be moved, this cannot, so it is
    # what a server should pin and what the record below keeps.
    DIGEST="$(docker image inspect --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' "$IMAGE" || true)"
    if [[ -z "$DIGEST" ]]; then
        warn "could not read the pushed digest — read it back with: docker buildx imagetools inspect $IMAGE"
    else
        log "Published: $DIGEST"
    fi
else
    step "Push skipped (--no-push)"
    log "Publish it later with: docker push $IMAGE"
fi

# ------------------------------------------------------------------
# Hand-off
# ------------------------------------------------------------------
RECORD_DIR="$PROJECT_DIR/out/images"
mkdir -p "$RECORD_DIR"
RECORD="$RECORD_DIR/keeper-$IMAGE_TAG.env"
{
    echo "# Written by build-keeper-image.sh: the build behind a keeper on a server."
    echo "# Nothing on chain refers to this image — these are for tracing a running"
    echo "# container back to a tree, and for pinning a server to a digest rather"
    echo "# than to a tag, which can be moved."
    echo "KEEPER_IMAGE=$IMAGE"
    echo "KEEPER_IMAGE_DIGEST=$DIGEST"
    echo "KEEPER_IMAGE_COMMIT=$COMMIT"
    echo "KEEPER_IMAGE_DIRTY=$DIRTY"
    echo "KEEPER_IMAGE_CHAIN=$CHAIN"
    echo "KEEPER_FEED_STORE=$STORE"
    echo "PUSHED=$PUSH"
} > "$RECORD"

step "Done"
log "Recorded: ${RECORD#"$PROJECT_DIR"/}"

cat <<NEXT

Run it on the server (one line each):

  docker pull ${DIGEST:-$IMAGE}
  docker run -d --name usdx-keeper --restart unless-stopped --stop-timeout 90 --env-file /opt/usdx/keeper.env -v usdx-records:/var/log/usdx ${DIGEST:-$IMAGE} -tee <machine>,<machine> -interval 10m -records /var/log/usdx/cycles.jsonl -keeperId <host>

keeper.env holds KEEPER_KEY, DIRECT_API_KEY, CHAIN_URL and EXT_PROXY_URL; the
addresses file and FLARE_CHAIN=$CHAIN are baked into the image. Allow the 90
seconds on stop: SIGTERM unwinds the cycle in flight, and docker's default 10
would send the second, killing signal.
NEXT
