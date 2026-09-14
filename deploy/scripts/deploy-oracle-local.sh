#!/usr/bin/env bash
# deploy-oracle-local.sh — deploy-oracle.sh --chain local: the TEE oracle
# contracts and the state verifier on the Hardhat simulation, deployer-governed,
# with reserved extension 1 made ours by the simulation's governance key.
#
# Usage:
#   ./deploy/scripts/deploy-oracle-local.sh              # deploy once, reuse if still on chain
#   ./deploy/scripts/deploy-oracle-local.sh --refresh    # redeploy (after a new sim run)
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$SCRIPT_DIR/deploy-oracle.sh" --chain local "$@"
