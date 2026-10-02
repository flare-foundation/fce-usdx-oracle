#!/usr/bin/env bash
# chains.sh — the networks the scripts know, in one place.
#
# Sourced by the scripts; not executable on its own. Every per-chain fact the
# scripts need is a row here: the chain id, the public RPC, Flare's normal
# (FTDC) proxy the availability proof is fetched from, the name of upstream's
# deploys file, the faucet, the kind of network, and the host port the chain's
# bundled indexer database is published on (docker-compose.<chain>-indexer.yaml).
# .env values override the defaults taken from here (CHAIN_URL, NORMAL_PROXY_URL).
#
# What a row does not say is where the chain's system contracts are: the
# addresses file and the network's contract registry say that
# (import-upstream-addresses.sh, deploy-oracle.sh). Songbird and Flare are
# mainnets: their registries accept no simulated machine, so a stack there is a
# real Confidential Space machine (deploy/README.md, "A remote machine").
#
# `oracle` says where the TEE oracle contracts come from, which decides whether
# this repository may deploy them:
#
#   own       a deployer-governed deployment of our own, made by deploy-oracle.sh
#             for a test: publications execute at once and one key runs the flow.
#   upstream  the real deployment, made by flare-smart-contracts-v2 and finished
#             by Flare governance. deploy-oracle.sh refuses these, and the
#             addresses arrive through import-upstream-addresses.sh.
#
# Coston therefore has two rows: `coston` addresses the official USDX deployment
# under the reserved extension 1, and `coston_local` a test deployment of our own
# on the same chain. They share the chain and its indexer, and differ in the
# addresses file, the admin set and the .env they carry (deploy/README.md).

CHAINS="local coston coston_local coston2 songbird flare"
# shellcheck disable=SC2034  # read by the scripts that source this file
CHAINS_USAGE="local|coston|coston_local|coston2|songbird|flare"

# chain_row <chain>: sets id, rpc, proxy, upstream, faucet, kind, indexer,
# indexer_port and oracle in the caller's scope; returns 1 for an unknown chain.
# `indexer` names the chain whose indexer files serve this one, since two rows
# on the same chain share one indexer.
chain_row() {
    case "$1" in
        local)        id=31337; rpc=http://127.0.0.1:8545;                          proxy=http://localhost:6662;                     upstream=scdev;    faucet=;                                    kind=simulation; indexer=;        indexer_port= ;    oracle=own ;;
        coston)       id=16;    rpc=https://coston-api.flare.network/ext/C/rpc;    proxy=https://tee-proxy-coston-1.flare.rocks;    upstream=coston;   faucet=https://faucet.flare.network/coston;  kind=testnet;    indexer=coston;  indexer_port=3316; oracle=upstream ;;
        coston_local) id=16;    rpc=https://coston-api.flare.network/ext/C/rpc;    proxy=https://tee-proxy-coston-1.flare.rocks;    upstream=coston;   faucet=https://faucet.flare.network/coston;  kind=testnet;    indexer=coston;  indexer_port=3316; oracle=own ;;
        coston2)      id=114;   rpc=https://coston2-api.flare.network/ext/C/rpc;   proxy=https://tee-proxy-coston2-1.flare.rocks;   upstream=coston2;  faucet=https://faucet.flare.network/coston2; kind=testnet;    indexer=coston2; indexer_port=3317; oracle=own ;;
        songbird)     id=19;    rpc=https://songbird-api.flare.network/ext/C/rpc;  proxy=https://tee-proxy-songbird-1.flare.rocks;  upstream=songbird; faucet=;                                    kind=mainnet;    indexer=;        indexer_port= ;    oracle=upstream ;;
        flare)        id=14;    rpc=https://flare-api.flare.network/ext/C/rpc;     proxy=;                                          upstream=flare;    faucet=;                                    kind=mainnet;    indexer=;        indexer_port= ;    oracle=upstream ;;
        *) return 1 ;;
    esac
}

chain_known()        { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1"; }
chain_id()           { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$id"; }
chain_rpc()          { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$rpc"; }
chain_normal_proxy() { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$proxy"; }
chain_upstream()     { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$upstream"; }
chain_faucet()       { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$faucet"; }
chain_kind()         { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$kind"; }
chain_indexer()      { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$indexer"; }
chain_indexer_port() { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$indexer_port"; }
chain_oracle()       { local id rpc proxy upstream faucet kind indexer indexer_port oracle; chain_row "$1" && echo "$oracle"; }
chain_is_mainnet()   { [[ "$(chain_kind "$1" 2>/dev/null)" == mainnet ]]; }
# True when this repository deploys the oracle contracts for the chain itself.
chain_deploys_own_oracle() { [[ "$(chain_oracle "$1" 2>/dev/null)" == own ]]; }

# chain_compose_network <chain>: the Docker network a live chain's stack runs
# on, which is the network of the chain's bundled indexer
# (docker-compose.<indexer>-indexer.yaml), so the proxy resolves
# <indexer>-indexer-db by name. Two rows on one chain share it: coston_local's is
# fce-usdx-oracle-coston. A chain without a bundled indexer gets one of its own
# name. COMPOSE_NETWORK, when set, overrides this wherever the scripts use it.
chain_compose_network() {
    local id rpc proxy upstream faucet kind indexer indexer_port oracle
    chain_row "$1" || return 1
    echo "fce-usdx-oracle-${indexer:-$1}"
}

# chain_from_id <chain id>: the chain with that id, or nothing. Coston's two rows
# share an id, so this answers with the official one; a test deployment is named
# by FLARE_CHAIN, which use-chain.sh writes into .env and resolve_chain prefers.
chain_from_id() {
    local c
    for c in $CHAINS; do [[ "$(chain_id "$c")" == "$1" ]] && { echo "$c"; return 0; }; done
    return 1
}

# resolve_chain [explicit]: the chain a script works on. An explicit --chain
# value wins; then FLARE_CHAIN, which use-chain.sh writes into .env; then the
# CHAIN_ID there; then the simulation, which is what an .env without any of them
# is. The variable is not called CHAIN because the scripts export .env wholesale
# and Foundry's cast reads CHAIN as its --chain option, where "local" or
# "coston" is not a chain it knows and every send fails.
resolve_chain() {
    local c="${1:-${FLARE_CHAIN:-}}"
    [[ -n "$c" ]] || c="$(chain_from_id "${CHAIN_ID:-}" 2>/dev/null || true)"
    [[ -n "$c" ]] || { [[ "${LOCAL_MODE:-true}" == true ]] && c=local || c=coston2; }
    echo "$c"
}
