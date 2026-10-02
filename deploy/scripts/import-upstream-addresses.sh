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
# Nothing is written until everything has been read and checked. The new
# addresses file and the indexer configuration are assembled in a temporary
# directory, and replace the files in deploy/config only once the deploys file,
# the addresses file and every address taken from them have passed. Oracle
# entries the addresses file already holds are replaced only by entries the
# deploys file supplies: a deploys file that lists no oracle contracts, as
# upstream's does for a network whose oracle was recorded here by hand, leaves
# them as they are. A file whose contents would not change is not rewritten.
#
# Usage: ./deploy/scripts/import-upstream-addresses.sh <network> [path-to-flare-smart-contracts-v2]
#        SIM_DUMP=<path> ./deploy/scripts/import-upstream-addresses.sh local
# Example: ./deploy/scripts/import-upstream-addresses.sh coston
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ $# -lt 1 ]]; then
    echo "Usage: $0 <network> [path-to-flare-smart-contracts-v2]" >&2
    echo "Example: $0 coston" >&2
    exit 2
fi

NETWORK="$1"
UPSTREAM="${2:-$PROJECT_DIR/../../../fsp/flare-smart-contracts-v2}"

# The chain rows (RPC per network) and .env, for CHAIN_URL and UPSTREAM_DIR.
# shellcheck source=lib/chains.sh
source "$SCRIPT_DIR/lib/chains.sh"
# shellcheck source=/dev/null
[[ -f "$PROJECT_DIR/.env" ]] && { set -a; . "$PROJECT_DIR/.env"; set +a; }

die() { echo "ERROR: $*" >&2; exit 1; }
for t in jq python3; do command -v "$t" >/dev/null || die "$t is required"; done

# The upstream deploys file is named after the CHAIN, which is not the row name:
# the simulation's is scdev.json, and coston_local reads coston's.
UPSTREAM_NETWORK="${UPSTREAM_NETWORK:-$(chain_upstream "$NETWORK" 2>/dev/null || echo "$NETWORK")}"
# DEPLOYS_SRC names a deploys file of the same shape written elsewhere, as
# deploy-oracle.sh does for its own deploy.
SRC="${DEPLOYS_SRC:-$UPSTREAM/deployment/deploys/$UPSTREAM_NETWORK.json}"
DEST="$PROJECT_DIR/deploy/config/$NETWORK/deployed-addresses.json"

[[ -n "${DEPLOYS_SRC:-}" || -d "$UPSTREAM/deployment" ]] || die "no deployment/ under $UPSTREAM"
[[ -f "$SRC" ]] || die "$SRC not found — did the upstream deploy run for $NETWORK?"
jq -e 'type == "array" and all(.[]; type == "object")' "$SRC" >/dev/null 2>&1 \
    || die "$SRC is not a deploys file: a JSON array of {name, contractName, address} entries"

# Everything below is assembled here and moved into place at the end.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
STAGED="$WORK/deployed-addresses.json"

if [[ "$NETWORK" == "local" ]]; then
    SIM_DUMP="${SIM_DUMP:-$PROJECT_DIR/../../e2e/docker/sim_dump/deployed-addresses.json}"
    [[ "$SIM_DUMP" != /* ]] && SIM_DUMP="$PROJECT_DIR/$SIM_DUMP"
    [[ -f "$SIM_DUMP" ]] || die "sim dump $SIM_DUMP not found — is the e2e stack up?"
    jq -e 'type == "object"' "$SIM_DUMP" >/dev/null 2>&1 || die "$SIM_DUMP is not a name → address map"
    # GovernanceSettings is not in the dump: the sim installs it at Flare's genesis
    # address with hardhat_setCode rather than deploying it. publish-local.sh reads
    # the timelock from it.
    GS_ADDR="${GOVERNANCE_SETTINGS_ADDRESS:-0x1000000000000000000000000000000000000007}"
    # Re-seeded every time: the dump is the truth for the sim's own contracts, and
    # a stale FlareTeeManager address would fail every registration call. Oracle
    # entries recorded against an earlier simulation go with it; they arrive again
    # from deploy-oracle.sh's deploy (DEPLOYS_SRC).
    jq --arg gs "$GS_ADDR" 'to_entries | map({name: .key, contractName: (.key + ".sol"), address: .value}) + [{name: "GovernanceSettings", contractName: "GovernanceSettings.sol", address: $gs}]' "$SIM_DUMP" > "$STAGED"
    echo "Seeding from $SIM_DUMP ($(jq length "$STAGED") contracts)"
else
    [[ -f "$DEST" ]] || die "$DEST not found — unknown network $NETWORK"
    cp "$DEST" "$STAGED"
fi
jq -e 'type == "array" and all(.[]; type == "object")' "$STAGED" >/dev/null 2>&1 \
    || die "${DEST#"$PROJECT_DIR"/} is not an addresses file: a JSON array of {name, contractName, address} entries"

# On a testnet the network's FlareContractRegistry, at the same address on every
# Flare chain, has the last word on the system contracts: upstream's deploys
# file lists what that repository deployed at the time, and Flare redeploys
# some of them (the Relay, on Coston, in 2026), which leaves a copied file
# pointing at a contract that no longer emits anything. Two of the names predate
# the deploys file altogether: the AddressUpdater the oracle contracts are wired
# to and GovernanceSettings, whose timelock publish-local.sh reads (a genesis
# contract on most chains, a deployed one on Coston). The simulation seeds them
# above. An entry already present keeps its place in the file and has only its
# address replaced; a missing one is appended.
INDEXER_OUT=""
if [[ "$NETWORK" != "local" ]]; then
    RPC="${CHAIN_URL:-$(chain_rpc "$NETWORK" 2>/dev/null || true)}"
    if [[ -n "$RPC" ]] && command -v cast >/dev/null; then
        for name in AddressUpdater GovernanceSettings Relay VoterRegistry FlareSystemsManager FlareTeeManager; do
            addr="$(cast call 0xaD67FE66660Fb8dFE9d6b1b4240d8650e30F6019 'getContractAddressByName(string)(address)' "$name" --rpc-url "$RPC" 2>/dev/null || true)"
            [[ "$addr" =~ ^0x[0-9a-fA-F]{40}$ && "$addr" != 0x0000000000000000000000000000000000000000 ]] || { echo "WARN: $name not in the contract registry at $RPC — left as it is" >&2; continue; }
            jq --arg n "$name" --arg a "$addr" \
                'if any(.[]; .name == $n) then map(if .name == $n then .address = $a else . end)
                 else . + [{name: $n, contractName: ($n + ".sol"), address: $a}] end' \
                "$STAGED" > "$STAGED.next" && mv "$STAGED.next" "$STAGED"
            echo "  registry $name: $addr"
        done
    else
        echo "WARN: no RPC or no cast — the registry's system addresses were not refreshed" >&2
    fi

    # The chain's C-chain indexer reads the same contracts, so its configuration
    # is rendered from the addresses just refreshed (docker-compose.<chain>-indexer.yaml
    # mounts it). Rendered rather than tracked: a redeployed Relay then reaches
    # the indexer on the next import, instead of leaving it listening to a
    # contract that no longer emits.
    INDEXER_CHAIN="$(chain_indexer "$NETWORK" 2>/dev/null || echo "$NETWORK")"
    if [[ -n "$INDEXER_CHAIN" && -f "$PROJECT_DIR/deploy/docker-compose.$INDEXER_CHAIN-indexer.yaml" ]]; then
        TEMPLATE="$PROJECT_DIR/deploy/config/indexer/indexer-config.toml.template"
        INDEXER_OUT="$PROJECT_DIR/deploy/config/indexer/$INDEXER_CHAIN/indexer-config.toml"
        addr_of() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0] // empty' "$STAGED"; }
        MISSING=""
        for name in FlareSystemsManager VoterRegistry FlareTeeManager Relay; do
            [[ "$(addr_of "$name")" =~ ^0x[0-9a-fA-F]{40}$ ]] || MISSING="$MISSING $name"
        done
        if [[ -n "$MISSING" ]]; then
            echo "WARN: no indexer config written —$MISSING missing from the addresses" >&2
            INDEXER_OUT=""
        else
            sed -e "s#__FLARE_SYSTEMS_MANAGER__#$(addr_of FlareSystemsManager)#g" \
                -e "s#__VOTER_REGISTRY__#$(addr_of VoterRegistry)#g" \
                -e "s#__FLARE_TEE_MANAGER__#$(addr_of FlareTeeManager)#g" \
                -e "s#__RELAY__#$(addr_of Relay)#g" \
                -e "s#__NODE_URL__#${RPC:-$(chain_rpc "$NETWORK")}#g" \
                -e "s#__DB_HOST__#$INDEXER_CHAIN-indexer-db#g" \
                -e "s#__HISTORY_DROP__#${INDEXER_HISTORY_DROP:-604800}#g" \
                "$TEMPLATE" > "$WORK/indexer-config.toml"
        fi
    fi
fi

# Whether the oracle contracts in the deploys file are this chain's. For a chain
# we deploy ourselves, upstream's file describes the CHAIN, whose official
# contracts belong to the other row: coston_local reads coston.json, which
# carries the extension-1 deployment. Its own entries arrive through DEPLOYS_SRC,
# which deploy-oracle.sh sets to its deploy output.
TAKE_ORACLE=true
if [[ -z "${DEPLOYS_SRC:-}" ]] && chain_deploys_own_oracle "$NETWORK" 2>/dev/null; then
    TAKE_ORACLE=false
fi
# The file as it stands, for the check that no oracle entry is lost; the
# simulation's is re-seeded on purpose, so it has nothing to keep.
KEEP_FROM=""
[[ "$NETWORK" != "local" ]] && KEEP_FROM="$DEST"

python3 - "$SRC" "$STAGED" "$TAKE_ORACLE" "$KEEP_FROM" <<'PYEOF'
import json, re, sys

src_path, staged_path, take_oracle, keep_from = sys.argv[1], sys.argv[2], sys.argv[3] == "true", sys.argv[4]

with open(src_path) as f:
    src = json.load(f)
with open(staged_path) as f:
    dest = json.load(f)

ADDRESS = re.compile(r"^0x[0-9a-fA-F]{40}$")

def is_impl(e):
    return e.get("name", "").endswith("Implementation")

# save-deployed-addresses.ts records contractName without the .sol suffix while
# the hand-maintained files carry it; compare on the bare name. A trailing
# "Proxy" goes too: upstream's deploy records a proxy under its own contract
# (TeeOracleFeedStoreProxy.sol), ours under the contract it fronts, and both
# name the same thing.
def contract(e):
    return e.get("contractName", "").removesuffix(".sol").removesuffix("Proxy")

# The oracle entries of an addresses file: the two names the tools resolve, and
# every feed store under its registry name.
def oracle_names(entries):
    return {e.get("name") for e in entries
            if e.get("name") in ("TeeOracleInstructionsSender", "TeeOracleFeedStore")
            or (contract(e) == "TeeOracleFeedStore" and not is_impl(e))}

def write(entries):
    with open(staged_path, "w") as f:
        json.dump(entries, f, indent=2)
        f.write("\n")

# The deploy script logs the sender proxy as "TeeOracleInstructionsSender" and
# each feed store proxy under its registry name (UsdxFeedStore upstream);
# implementations get "<name>Implementation".
sender = next((e for e in src
               if e.get("name") == "TeeOracleInstructionsSender" and not is_impl(e)), None)
stores = [e for e in src
          if contract(e) == "TeeOracleFeedStore" and not is_impl(e)]

if not take_oracle:
    # The oracle contracts of a chain we deploy ourselves come from that deploy,
    # not from upstream's file for the same chain.
    print(f"NOTE: oracle entries in {src_path} left out — they are the chain's own "
          f"deployment, not this one's; deploy/scripts/deploy-oracle.sh writes ours")
    upserts = []
elif sender is None or not stores:
    # Nothing to import. That is an error only when the addresses file has no
    # oracle addresses either: a standalone run refreshes the system addresses
    # and the indexer configuration above, which is reason enough to run it.
    if oracle_names(dest):
        print(f"NOTE: no TEE oracle entries in {src_path}; the addresses file keeps "
              f"the ones it has ({', '.join(sorted(oracle_names(dest)))})")
        upserts = []
    else:
        sys.exit(f"ERROR: no TEE oracle entries in {src_path} — run "
                 "deployment/scripts/deploy-tee-oracle.sh <network> upstream first, "
                 "or deploy/scripts/deploy-oracle.sh --chain <chain> for a test deployment. "
                 "Nothing was written.")
else:
    upserts = [{
        "name": "TeeOracleInstructionsSender",
        "contractName": "TeeOracleInstructionsSender.sol",
        "address": sender.get("address", ""),
    }]
    for store in stores:
        upserts.append({
            "name": store.get("name", ""),
            "contractName": "TeeOracleFeedStore.sol",
            "address": store.get("address", ""),
        })
    # The keeper and fleet-status resolve the single feed store by the generic
    # name; keep that alias only when there is exactly one store, so the name
    # never silently picks a feed.
    if len(stores) == 1:
        upserts.append({
            "name": "TeeOracleFeedStore",
            "contractName": "TeeOracleFeedStore.sol",
            "address": stores[0].get("address", ""),
        })
    else:
        names = ", ".join(s.get("name", "?") for s in stores)
        print(f"NOTE: {len(stores)} feed stores deployed ({names}); no generic "
              "TeeOracleFeedStore alias written — pass -store explicitly")

# Every address is checked before any is used.
bad = [f"{u['name'] or '<unnamed>'}: {u['address'] or '<none>'}" for u in upserts
       if not u["name"] or not ADDRESS.match(u["address"])]
if bad:
    sys.exit(f"ERROR: {src_path} names malformed oracle entries ({'; '.join(bad)}). Nothing was written.")

by_name = {e.get("name"): i for i, e in enumerate(dest)}
for entry in upserts:
    i = by_name.get(entry["name"])
    if i is None:
        dest.append(entry)
        print(f"  added {entry['name']}: {entry['address']}")
    elif dest[i].get("address", "").lower() == entry["address"].lower():
        print(f"  unchanged {entry['name']}: {entry['address']}")
    else:
        print(f"  updated {entry['name']}: {dest[i].get('address', '?')} → {entry['address']}")
        dest[i] = entry

# Belt and braces: whatever happened above, an oracle entry the addresses file
# held is still there.
if keep_from:
    with open(keep_from) as f:
        before = json.load(f)
    lost = oracle_names(before) - {e.get("name") for e in dest}
    if lost:
        sys.exit(f"ERROR: the import would drop {', '.join(sorted(lost))}. Nothing was written.")

write(dest)
PYEOF

# --- Everything checked: into place ---
mkdir -p "$(dirname "$DEST")"
if [[ -f "$DEST" ]] && python3 -c 'import json, sys; sys.exit(0 if json.load(open(sys.argv[1])) == json.load(open(sys.argv[2])) else 1)' "$DEST" "$STAGED" 2>/dev/null; then
    echo ""
    echo "${DEST#"$PROJECT_DIR"/} is unchanged"
else
    # Copied over rather than moved, so a tracked file keeps its permissions.
    cat "$STAGED" > "$DEST"
    echo ""
    echo "Imported into ${DEST#"$PROJECT_DIR"/}"
fi
if [[ -n "$INDEXER_OUT" && -f "$WORK/indexer-config.toml" ]]; then
    mkdir -p "$(dirname "$INDEXER_OUT")"
    if cmp -s "$WORK/indexer-config.toml" "$INDEXER_OUT"; then
        echo "${INDEXER_OUT#"$PROJECT_DIR"/} is unchanged"
    else
        cp "$WORK/indexer-config.toml" "$INDEXER_OUT"
        echo "Wrote ${INDEXER_OUT#"$PROJECT_DIR"/} (seven days of history; restart the indexer to pick it up)"
    fi
fi
echo "The sender is also derived on-chain from the store's instructionsSender()"
echo "pointer at runtime; the file entry is what the flag-free tooling resolves."
