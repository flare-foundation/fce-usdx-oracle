# chains.sh — the networks the scripts know, in one place.
#
# Sourced by the scripts; not executable on its own. Every per-chain fact the
# scripts need is a row here: the chain id, the public RPC, Flare's normal
# (FTDC) proxy the availability proof is fetched from, the name of upstream's
# deploys file, the faucet, the kind of network, and the host port the chain's
# bundled indexer database is published on (docker-compose.<chain>-indexer.yaml).
# .env values override the defaults taken from here (CHAIN_URL, NORMAL_PROXY_URL).
#
# What a row does not say is whether the TEE contracts are on the chain:
# deploy-oracle.sh asks the network's contract registry. Songbird and Flare are
# listed so the scripts address them; their registries carry no FlareTeeManager
# yet, and as mainnets they accept no simulated machine, so a stack there is a
# real Confidential Space machine (deploy/README.md, "A remote machine").

CHAINS="local coston coston2 songbird flare"
CHAINS_USAGE="local|coston|coston2|songbird|flare"

# chain_row <chain>: sets id, rpc, proxy, upstream, faucet, kind, indexer_port
# in the caller's scope; returns 1 for an unknown chain.
chain_row() {
    case "$1" in
        local)    id=31337; rpc=http://127.0.0.1:8545;                          proxy=http://localhost:6662;                     upstream=scdev;    faucet=;                                    kind=simulation; indexer_port= ;;
        coston)   id=16;    rpc=https://coston-api.flare.network/ext/C/rpc;    proxy=https://tee-proxy-coston-1.flare.rocks;    upstream=coston;   faucet=https://faucet.flare.network/coston;  kind=testnet;    indexer_port=3316 ;;
        coston2)  id=114;   rpc=https://coston2-api.flare.network/ext/C/rpc;   proxy=https://tee-proxy-coston2-1.flare.rocks;   upstream=coston2;  faucet=https://faucet.flare.network/coston2; kind=testnet;    indexer_port=3317 ;;
        songbird) id=19;    rpc=https://songbird-api.flare.network/ext/C/rpc;  proxy=https://tee-proxy-songbird-1.flare.rocks;  upstream=songbird; faucet=;                                    kind=mainnet;    indexer_port= ;;
        flare)    id=14;    rpc=https://flare-api.flare.network/ext/C/rpc;     proxy=;                                          upstream=flare;    faucet=;                                    kind=mainnet;    indexer_port= ;;
        *) return 1 ;;
    esac
}

chain_known()        { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1"; }
chain_id()           { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$id"; }
chain_rpc()          { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$rpc"; }
chain_normal_proxy() { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$proxy"; }
chain_upstream()     { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$upstream"; }
chain_faucet()       { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$faucet"; }
chain_kind()         { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$kind"; }
chain_indexer_port() { local id rpc proxy upstream faucet kind indexer_port; chain_row "$1" && echo "$indexer_port"; }
chain_is_mainnet()   { [[ "$(chain_kind "$1" 2>/dev/null)" == mainnet ]]; }

# chain_from_id <chain id>: the chain with that id, or nothing.
chain_from_id() {
    local c
    for c in $CHAINS; do [[ "$(chain_id "$c")" == "$1" ]] && { echo "$c"; return 0; }; done
    return 1
}

# resolve_chain [explicit]: the chain a script works on. An explicit --chain
# value wins; then CHAIN, which use-chain.sh writes into .env; then the CHAIN_ID
# there; then the simulation, which is what an .env without any of them is.
resolve_chain() {
    local c="${1:-${CHAIN:-}}"
    [[ -n "$c" ]] || c="$(chain_from_id "${CHAIN_ID:-}" 2>/dev/null || true)"
    [[ -n "$c" ]] || { [[ "${LOCAL_MODE:-true}" == true ]] && c=local || c=coston2; }
    echo "$c"
}
