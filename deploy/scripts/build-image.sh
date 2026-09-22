#!/usr/bin/env bash
#
# build-image.sh — build the extension TEE image and push it to a registry.
#
# The image's ID is what the machine attests: Confidential Space reports it as
# submods.container.image_id, and tee-node turns that claim into the codeHash it
# registers on chain (tee-node pkg/attestation/attestation_token.go, CodeHash()).
# The ID is the *config* digest, which a push leaves untouched, so the code hash
# is known here — before the VM boots — and is checked against the registry after
# the push.
#
# This is a plain build, deliberately NOT a reproducible one: the code hash it
# produces identifies the image but cannot be re-derived from source by anyone
# else. For a build an auditor can reproduce, see REPRODUCIBILITY.md (a
# docker-container buildx builder with rewrite-timestamp on top of this).
#
# SOURCE_DATE_EPOCH is passed even so, because the Dockerfile requires it rather
# than merely benefiting from it — see the Build step below.
#
# Usage:
#   ./deploy/scripts/build-image.sh                    # tag from git, build, verify, push
#   ./deploy/scripts/build-image.sh --tag v0.1.0
#   ./deploy/scripts/build-image.sh --no-push          # build and print the code hash only
#   ./deploy/scripts/build-image.sh --image <ref>      # a registry other than the default
#
# Inputs (env vars, or .env):
#   IMAGE_REPO   registry path without the tag (default: the staging path below)
#   IMAGE_TAG    tag to publish (default: the short commit, plus -dirty if unclean)
#
# Writes out/images/<tag>.env — the hand-off for the rest of the flow: the image
# reference and the code hash (EXPECTED_CODE_HASH, which upload-rpc-keys.sh and
# upload-hex-key.sh ask for).
#
# Prerequisites: docker, jq, and push access
# (`gcloud auth configure-docker <registry-host>` for Artifact Registry).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[build-image]${NC} $*"; }
step() { echo -e "\n${CYAN}=== $* ===${NC}"; }
warn() { echo -e "${YELLOW}[build-image] WARN:${NC} $*" >&2; }
die()  { echo -e "${RED}[build-image] ERROR:${NC} $*" >&2; exit 1; }

source "$SCRIPT_DIR/lib/env.sh"; load_env
source "$SCRIPT_DIR/lib/versions.sh"

# Only the extension image is attested; the proxy image is not, so it is not built
# here (start-services.sh builds it from deploy/proxy/Dockerfile).
DEFAULT_IMAGE_REPO="europe-west1-docker.pkg.dev/flare-network-staging/containers/tee-usdx"

IMAGE_REPO="${IMAGE_REPO:-$DEFAULT_IMAGE_REPO}"
IMAGE_TAG="${IMAGE_TAG:-}"
PUSH=true

while [[ $# -gt 0 ]]; do
    case "$1" in
        --image)   IMAGE_REPO="${2:?--image needs a registry path}"; shift 2 ;;
        --tag)     IMAGE_TAG="${2:?--tag needs a tag}"; shift 2 ;;
        --no-push) PUSH=false; shift ;;
        -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
        *)         die "unknown argument: $1 (try --help)" ;;
    esac
done

command -v docker >/dev/null || die "docker not found"
command -v jq >/dev/null     || die "jq not found"

step "Source"

git -C "$PROJECT_DIR" rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository: $PROJECT_DIR"
COMMIT="$(git -C "$PROJECT_DIR" rev-parse HEAD)"
DIRTY=false
[[ -n "$(git -C "$PROJECT_DIR" status --porcelain)" ]] && DIRTY=true

# The default tag names the commit, so a machine's code hash can always be traced
# back to a tree — the one thing still worth having without a reproducible build.
if [[ -z "$IMAGE_TAG" ]]; then
    IMAGE_TAG="$(git -C "$PROJECT_DIR" rev-parse --short=12 HEAD)"
    [[ "$DIRTY" == true ]] && IMAGE_TAG="$IMAGE_TAG-dirty"
fi

IMAGE="$IMAGE_REPO:$IMAGE_TAG"

# The Dockerfile uses SOURCE_DATE_EPOCH unguarded in two places, so it is a hard
# requirement rather than a reproducibility nicety: it is the apt snapshot date
# that pins the cgo toolchain, and the argument to a `touch -h -d @$SOURCE_DATE_EPOCH`
# over /app. Unset, the touch fails with "invalid date format '@'"; set to 0, apt
# asks snapshot.debian.org for a 1970 snapshot. The commit timestamp is the only
# value that works, and it costs nothing here.
SOURCE_DATE_EPOCH="$(git -C "$PROJECT_DIR" log -1 --format=%ct)"

load_versions "$PROJECT_DIR"

log "Image:        $IMAGE"
log "Commit:       $COMMIT$([[ "$DIRTY" == true ]] && echo ' (uncommitted changes)')"
log "Epoch:        $SOURCE_DATE_EPOCH"
log "tee-node pin: $TEE_NODE_VERSION"

step "Build"

# The platform is fixed: Confidential Space is AMD SEV, and the Dockerfile compiles
# GOARCH=amd64 whatever the host is. The default builder is fine; what this build
# skips relative to REPRODUCIBILITY.md is rewrite-timestamp, which needs a
# docker-container builder and only matters for a hash someone else must re-derive.
docker build \
    --platform linux/amd64 \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    -t "$IMAGE" \
    -f "$PROJECT_DIR/Dockerfile" \
    "$PROJECT_DIR" \
    || die "build failed"

CODE_HASH_RAW="$(docker image inspect --format '{{.Id}}' "$IMAGE")"
CODE_HASH="0x${CODE_HASH_RAW#sha256:}"

# ------------------------------------------------------------------
# What ended up in the image, before it is published or registered.
# These are not reproducibility checks — each one is a deployment that
# fails at attestation rather than at build, which is far more expensive.
# ------------------------------------------------------------------
step "Image checks"

# MODE=0 is production attestation, and it is deliberately absent from the
# override label: the attestation backend is part of the attested identity, so a
# launch must not be able to turn simulation on without changing the image.
mode="$(docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$IMAGE" | grep -E '^MODE=' || true)"
[[ "$mode" == "MODE=0" ]] \
    || die "image bakes '${mode:-<unset>}', expected MODE=0 — this image would not attest for real on a Confidential Space VM"
log "MODE=0 (production attestation)"

label="$(docker image inspect --format '{{index .Config.Labels "tee.launch_policy.allow_env_override"}}' "$IMAGE")"
[[ -n "$label" && "$label" != "<no value>" ]] \
    || die "no tee.launch_policy.allow_env_override label — Confidential Space would reject every operator env override at attestation time"
IFS=',' read -ra overridable <<< "$label"
for v in "${overridable[@]}"; do
    [[ "$v" == "MODE" ]] && die "MODE appears in the launch-policy label — a launch could switch off real attestation without changing the image"
done
log "Launch policy allows: $label"

# Confidential Space opens every exposed port in the VM firewall, so this list is
# a security boundary: the sign server and the extension's /action and /state bind
# loopback and must not appear.
ports="$(docker image inspect --format '{{json .Config.ExposedPorts}}' "$IMAGE")"
[[ "$ports" == '{"5500/tcp":{}}' ]] \
    || die "exposed ports are $ports, expected only 5500/tcp — anything else is opened in the VM firewall"
log "Exposed ports: 5500/tcp only (tee-node's configuration port)"

echo
log "Code hash (this is what gets registered on chain):"
echo -e "  ${CYAN}${CODE_HASH}${NC}"

# ------------------------------------------------------------------
# Push, then confirm the registry holds the same identity
# ------------------------------------------------------------------
if [[ "$PUSH" == true ]]; then
    step "Push"

    registry_host="${IMAGE_REPO%%/*}"
    log "Pushing $IMAGE ..."
    if ! docker push "$IMAGE"; then
        die "push failed. For Artifact Registry, authenticate the docker client once with:\n  gcloud auth configure-docker $registry_host"
    fi

    # A push rewrites the manifest and may recompress layers, but not the config
    # blob — so the registry's config digest must still be the code hash above.
    # If it is not, the image the VM pulls is not the image checked here.
    remote_config="$(docker buildx imagetools inspect "$IMAGE" --raw 2>/dev/null | jq -r '.config.digest // empty' || true)"
    if [[ -z "$remote_config" ]]; then
        warn "could not read the registry's config digest — check by hand: docker pull $IMAGE && docker image inspect --format '{{.Id}}' $IMAGE"
    elif [[ "$remote_config" != "$CODE_HASH_RAW" ]]; then
        die "the registry holds config digest $remote_config, but the built image is $CODE_HASH_RAW — do not register this build"
    else
        log "Registry config digest matches the built image"
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
RECORD="$RECORD_DIR/$IMAGE_TAG.env"
{
    echo "# Written by build-image.sh: the build behind a registered machine."
    echo "# EXPECTED_CODE_HASH is what upload-rpc-keys.sh and upload-hex-key.sh ask for,"
    echo "# and what allow-tee-version whitelists. Not a reproducible build: the commit"
    echo "# below identifies the tree, it does not let anyone re-derive the hash."
    echo "TEE_IMAGE=$IMAGE"
    echo "EXPECTED_CODE_HASH=$CODE_HASH"
    echo "IMAGE_COMMIT=$COMMIT"
    echo "IMAGE_DIRTY=$DIRTY"
    echo "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"
    echo "TEE_NODE_VERSION=$TEE_NODE_VERSION"
    echo "PUSHED=$PUSH"
} > "$RECORD"

step "Done"
log "Recorded: ${RECORD#"$PROJECT_DIR"/}"

cat <<NEXT

Next:
  1. Launch it on a Confidential Space VM with
       tee-image-reference=$IMAGE
     and tee-env-* overrides drawn from: $label
     The VM's service account needs roles/artifactregistry.reader on the repository.
  2. Confirm what the machine reports:
       curl -s "\$EXT_PROXY_URL/info" | jq '.machineData'
       codeHash must equal $CODE_HASH
       platform must start with 0x4743505f414d445f534556 (GCP_AMD_SEV)
  3. Register it:  ./deploy/scripts/post-build.sh
NEXT
