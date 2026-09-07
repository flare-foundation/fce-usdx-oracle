#!/usr/bin/env bash
#
# test-unit.sh — run the unit tests of both Go modules: the extension at the repo
# root and the operator tooling in tools/.
#
# For the wire-format conformance replay, run
#   go test ./internal/extension/ -run TestConformance
# For the on-chain end-to-end cycle, see ./deploy/scripts/oracle-cycle.sh.
#
# Usage:
#   ./deploy/scripts/test-unit.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

RED='\033[0;31m'; GREEN='\033[0;32m'; CYAN='\033[0;36m'; NC='\033[0m'
log() { echo -e "${GREEN}[test-unit]${NC} $*"; }
die() { echo -e "${RED}[test-unit] ERROR:${NC} $*" >&2; exit 1; }

if [[ -f "$PROJECT_DIR/.env" ]]; then
    set -a; source "$PROJECT_DIR/.env"; set +a
fi

FAILED=()
for module in "" tools; do
    dir="$PROJECT_DIR/${module}"
    echo ""
    echo -e "${CYAN}=== ${module:-extension} ===${NC}"
    log "go test ./..."
    ( cd "$dir" && go test ./... ) || FAILED+=("${module:-extension}")
done

echo ""
[[ ${#FAILED[@]} -eq 0 ]] || die "unit tests FAILED for: ${FAILED[*]}"
log "unit tests passed"
