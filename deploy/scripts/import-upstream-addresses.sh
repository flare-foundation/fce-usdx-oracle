#!/usr/bin/env bash
# import-upstream-addresses.sh — Copy the TEE oracle contract addresses from a
# flare-smart-contracts-v2 deploy into this repo's addresses files.
#
# The oracle contracts are deployed upstream (deployment/scripts/deploy-tee-oracle.sh
# in flare-smart-contracts-v2), which records what it deployed in
# deployment/deploys/<network>.json there. The tooling in this repo resolves
# contracts BY NAME from config/<network>/deployed-addresses.json, so after each
# upstream deploy the two addresses are slotted in here:
#
#   TeeOracleInstructionsSender  — the extension's sender proxy
#   TeeOracleFeedStore           — alias for the single USDX feed store (what
#                                  the keeper and fleet-status resolve)
#   <registryName>               — each store also under its upstream registry
#                                  name (e.g. UsdxFeedStore)
#
# For the local simulation there is no committed addresses file: the sim's
# contracts change address on every run. `local` therefore seeds
# deploy/config/local/deployed-addresses.json from the e2e repository's sim dump
# (SIM_DUMP, a flat name→address map) before slotting the oracle contracts in.
#
# Usage: ./deploy/scripts/import-upstream-addresses.sh <network> [path-to-flare-smart-contracts-v2]
#        SIM_DUMP=<path> ./deploy/scripts/import-upstream-addresses.sh local
# Example: ./deploy/scripts/import-upstream-addresses.sh coston2
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <network> [path-to-flare-smart-contracts-v2]" >&2
    echo "Example: $0 coston2" >&2
    exit 2
fi

NETWORK="$1"
UPSTREAM="${2:-$PROJECT_DIR/../../../fsp/flare-smart-contracts-v2}"

# Upstream's forge script names Hardhat's chain `scdev`, so the local simulation's
# deploys file is deployment/deploys/scdev.json while ours is deploy/config/local/.
UPSTREAM_NETWORK="${UPSTREAM_NETWORK:-$([[ "$NETWORK" == "local" ]] && echo scdev || echo "$NETWORK")}"
# DEPLOYS_SRC names a deploys file of the same shape written elsewhere, as
# deploy-oracle-local.sh does for its own deploy.
SRC="${DEPLOYS_SRC:-$UPSTREAM/deployment/deploys/$UPSTREAM_NETWORK.json}"
DEST="$PROJECT_DIR/deploy/config/$NETWORK/deployed-addresses.json"

[[ -n "${DEPLOYS_SRC:-}" || -d "$UPSTREAM/deployment" ]] || { echo "ERROR: no deployment/ under $UPSTREAM" >&2; exit 1; }
[[ -f "$SRC" ]]  || { echo "ERROR: $SRC not found — did the upstream deploy run for $NETWORK?" >&2; exit 1; }
if [[ "$NETWORK" == "local" ]]; then
    SIM_DUMP="${SIM_DUMP:-$PROJECT_DIR/../../e2e/docker/sim_dump/deployed-addresses.json}"
    [[ "$SIM_DUMP" != /* ]] && SIM_DUMP="$PROJECT_DIR/$SIM_DUMP"
    [[ -f "$SIM_DUMP" ]] || { echo "ERROR: sim dump $SIM_DUMP not found — is the e2e stack up?" >&2; exit 1; }
    mkdir -p "$(dirname "$DEST")"
    # GovernanceSettings is not in the dump: the sim installs it at Flare's genesis
    # address with hardhat_setCode rather than deploying it. publish-local.sh reads
    # the timelock from it.
    GS_ADDR="${GOVERNANCE_SETTINGS_ADDRESS:-0x1000000000000000000000000000000000000007}"
    # Re-seeded every time: the dump is the truth for the sim's own contracts, and
    # a stale FlareTeeManager address would fail every registration call.
    jq --arg gs "$GS_ADDR" 'to_entries | map({name: .key, contractName: (.key + ".sol"), address: .value}) + [{name: "GovernanceSettings", contractName: "GovernanceSettings.sol", address: $gs}]' "$SIM_DUMP" > "$DEST"
    echo "Seeded $DEST from $SIM_DUMP ($(jq length "$DEST") contracts)"
fi
[[ -f "$DEST" ]] || { echo "ERROR: $DEST not found — unknown network $NETWORK" >&2; exit 1; }

python3 - "$SRC" "$DEST" <<'PYEOF'
import json, sys

src_path, dest_path = sys.argv[1], sys.argv[2]

with open(src_path) as f:
    src = json.load(f)
with open(dest_path) as f:
    dest = json.load(f)

def is_impl(e):
    return e.get("name", "").endswith("Implementation")

# save-deployed-addresses.ts records contractName without the .sol suffix while
# the hand-maintained files carry it; compare on the bare name.
def contract(e):
    return e.get("contractName", "").removesuffix(".sol")

# The deploy script logs the sender proxy as "TeeOracleInstructionsSender" and
# each feed store proxy under its registry name (contractName
# TeeOracleFeedStore.sol); implementations get "<name>Implementation".
sender = next((e for e in src
               if e.get("name") == "TeeOracleInstructionsSender" and not is_impl(e)), None)
stores = [e for e in src
          if contract(e) == "TeeOracleFeedStore" and not is_impl(e)]

if sender is None or not stores:
    sys.exit(f"ERROR: no TEE oracle entries in {src_path} — run "
             "deployment/scripts/deploy-tee-oracle.sh <network> upstream first")

upserts = [{
    "name": "TeeOracleInstructionsSender",
    "contractName": "TeeOracleInstructionsSender.sol",
    "address": sender["address"],
}]
for store in stores:
    upserts.append({
        "name": store["name"],
        "contractName": "TeeOracleFeedStore.sol",
        "address": store["address"],
    })
# The keeper and fleet-status resolve the single feed store by the generic
# name; keep that alias only when there is exactly one store, so the name
# never silently picks a feed.
if len(stores) == 1:
    upserts.append({
        "name": "TeeOracleFeedStore",
        "contractName": "TeeOracleFeedStore.sol",
        "address": stores[0]["address"],
    })
else:
    names = ", ".join(s["name"] for s in stores)
    print(f"NOTE: {len(stores)} feed stores deployed ({names}); no generic "
          "TeeOracleFeedStore alias written — pass -store explicitly")

by_name = {e.get("name"): i for i, e in enumerate(dest)}
for entry in upserts:
    i = by_name.get(entry["name"])
    if i is None:
        dest.append(entry)
        action = "added"
    else:
        dest[i] = entry
        action = "updated"
    print(f"  {action} {entry['name']}: {entry['address']}")

with open(dest_path, "w") as f:
    json.dump(dest, f, indent=2)
    f.write("\n")
PYEOF

echo ""
echo "Imported into $DEST"
echo "The sender is also derived on-chain from the store's instructionsSender()"
echo "pointer at runtime; the file entry is what the flag-free tooling resolves."
