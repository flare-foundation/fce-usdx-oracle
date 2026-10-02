# Running the stack — local dev and the testnets via Docker Compose

Everything about *bringing the containers up*: prerequisites, the phase scripts, ports,
environment variables, TEE governance, and the live-chain overlay.

This is the **Docker Compose** path — three containers on your machine, talking to a
chain. It is not how a production machine runs: that is a Confidential Space VM with
real attestation, and it lives in [deployment-steps.md](deployment-steps.md). Once a
machine is up, [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) is
the order to provision it in, and [usdx-oracle-tooling.md](usdx-oracle-tooling.md) is
what every command does.

---

## Prerequisites

- **Foundry** (`forge`, `cast`) — deploys the oracle contracts on the simulation or a test
  deployment, and the scripts read and send with `cast`. `contracts/` needs forge-std in its
  git-ignored `lib/`: `(cd contracts && forge install --no-git foundry-rs/forge-std)`
- **jq**, **python3**, **openssl** and **curl** — the scripts parse JSON with jq, pick redis's
  free port and merge the addresses files with python3, generate keys and salts with openssl,
  and poll the proxy with curl
- **Go 1.25.8** — `tools/go.mod` needs it; the extension module at the repository root and the
  operator tooling in `tools/` are both built with it
- **Docker** — builds and runs the extension image
- **A sibling checkout of `flare-smart-contracts-v2`** on its `tee-oracle-extension` branch, at
  `../../../fsp/flare-smart-contracts-v2` (`UPSTREAM_DIR`): the oracle contracts are compiled
  from it, and `import-upstream-addresses.sh` reads its deploys files
- **The local infrastructure**, for the simulation only — the e2e repository's Hardhat node with
  the Flare contracts deployed, its redis instances and indexer database (`docker compose up -d`
  in `tee/e2e/docker`), and its standard TEE, proxy and relay
  ([local-e2e.md](local-e2e.md), step 1)
- **Sibling checkouts of `tee-node` and `tee-proxy`** in `tee/`, for `USE_LOCAL_SIBLINGS=1`
  only, which a simulation built from develop needs

---

## Quick start

For the complete local run — simulation, oracle contracts, publications and a keeper cycle —
follow [local-e2e.md](local-e2e.md). With the local infrastructure running, the stack comes up
after the chain switch, the contracts and the owner wiring:

```bash
./deploy/scripts/use-chain.sh local                          # .env.local → .env
./deploy/scripts/deploy-oracle.sh --chain local              # oracle contracts on the sim, extension 1 reserved, addresses imported
(cd tools && go run ./cmd/wire-extension)                    # instructions sender, and the machines' owner on the allowlist
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local
```

`full-setup.sh` runs three phases:

1. **Identity** — record `EXTENSION_ID` from `.env` (1, the reserved id, when `.env` names none)
   in `deploy/config/extension.env`, which the compose file passes to the node
2. **Docker Compose** — start redis, the proxy, and the extension TEE as containers
3. **Post-build** — allow the TEE version, set the TEE governance, register the TEE machine
   on-chain and take it to production

The oracle contracts (`TeeOracleInstructionsSender` + `TeeOracleFeedStore`) are not deployed by
`full-setup.sh`. On the simulation and on a test deployment (`coston_local`, `coston2`)
`deploy-oracle.sh` deploys them; on `coston` and the mainnets they are upstream's, deployed by
`flare-smart-contracts-v2`'s `deploy-tee-oracle.sh` and brought here by
`import-upstream-addresses.sh` ([deployment-steps.md](deployment-steps.md)).

---

## The phases individually

### 1. Start services

```bash
./deploy/scripts/start-services.sh
```

Builds the extension image, then brings up Docker Compose; the proxy is tee-proxy's published
image at the tag pinned in `deploy/docker-compose.yaml` (with `USE_LOCAL_SIBLINGS=1`, a build of
the sibling checkout instead).

> The base compose file declares the external `docker_default` network, which the e2e
> infrastructure's `docker compose up` creates. Start the e2e stack first, or this fails with
> "network not found". When the e2e stack is taken down and up again, the network is a new one:
> run `./deploy/scripts/stop-services.sh` and start again.

Three containers come up:

- **redis** — queue storage for the proxy
- **ext-proxy** — TEE proxy, with `deploy/config/proxy/extension_proxy.docker.toml` rendered
  into `out/extension_proxy.docker.local.toml` with the current simulation's addresses
- **extension-tee** — tee-node + the extension server, built from source

The node reads `EXTENSION_ID` from `deploy/config/extension.env` (written by `full-setup.sh`).
The proxy reaches the simulation's indexer database through `host.docker.internal:3306`, and the
node reaches the proxy as `ext-proxy:6663` on the compose network.

The compose project is `deploy`, the directory of the compose files, so its containers are
`deploy-extension-tee-1`, `deploy-ext-proxy-1` and `deploy-redis-1`. Naming the project reads
the running stack without interpolating any compose file:

```bash
docker compose -p deploy ps
docker compose -p deploy logs -f extension-tee
./deploy/scripts/stop-services.sh
```

### 2. Post-build

```bash
./deploy/scripts/post-build.sh
```

Waits for the extension proxy and the normal (FTDC) proxy to answer, then allows the TEE
version, registers the extension's TEE **governance** (below), and registers the TEE machine
on-chain. It reads `EXT_PROXY_URL` and `NORMAL_PROXY_URL` from your `.env`, or the recorded
stack's proxy on the simulation; `--proxy <url>` addresses another machine.

### Proving the round trip

With the machine registered and configuration published, run one keeper cycle against the
deployed stack:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE1> --dry-run    # request and collect (the request fee is paid), rehearse the submission, submit nothing
./deploy/scripts/oracle-cycle.sh -tee <TEE1>              # request, collect, submit
```

The wire contract itself (independent of any chain) is checked by `TestConformance`
(`GOWORK=off go test ./internal/extension -run TestConformance`). For the full feed walkthrough
see [testing.md](testing.md).

### Running single steps

The tools read `.env` and the recorded stack themselves, so the addresses file and the chain
come from there; the flags below override them.

```bash
(cd tools && go run ./cmd/allow-tee-version -p http://localhost:6674)
(cd tools && go run ./cmd/set-governance -p http://localhost:6674)
(cd tools && go run ./cmd/register-tee -p http://localhost:6674 -ep http://localhost:6662)
```

`register-tee` runs register, availability check and production (`-command rap`, the default),
expects simulated attestation when `SIMULATED_TEE=true`, and starts afresh unless given `-resume`
(its state file is `deploy/config/register-tee.state`).

```bash
./deploy/scripts/generate-bindings.sh
```

---

## Environment variables

What the stack scripts (`full-setup.sh`, `start-services.sh`, `stop-services.sh`,
`post-build.sh`) and the compose files read, from the shell or `.env`. A live chain's defaults
come from its row in `deploy/scripts/lib/chains.sh`.

| Variable | Default | Description |
|----------|---------|-------------|
| `FLARE_CHAIN` | written by `use-chain.sh` | The chain the scripts work on; without it, the chain `CHAIN_ID` names, else `local` while `LOCAL_MODE` is not `false` |
| `CHAIN_URL` | the chain's public RPC (`http://127.0.0.1:8545` locally) | Chain RPC for the tools; the live overlay also requires it, and passes it to the node, which does not read it |
| `CHAIN_ID` | the chain's id; `31337` in the base compose file | The chain the node and the extension sign for; `start-services.sh` refuses a value that is not the chain's |
| `ADDRESSES_FILE` | `deploy/config/<chain>/deployed-addresses.json` | Path to `deployed-addresses.json` |
| `EXTENSION_ID` | `1` | Recorded in `deploy/config/extension.env` by `full-setup.sh`; mandatory in the node and the extension, which have no default |
| `INITIAL_OWNER` | the funded simulation key's address in the compose file; derived from `EXTENSION_OWNER_KEY` by `use-chain.sh` on a live chain | The machines' initial owner, and the default TEE governance signer |
| `EXTENSION_OWNER_KEY` | the Hardhat dev key, on `local` only | The extension owner's key, which every tool signs with unless it names another role's; refused as missing on a live chain |
| `MACHINE_OWNER_KEY` | unset | The machine owner's key, which `register-tee` and `retire-tee` sign with when the machines' owner is not the extension owner |
| `PROXY_PRIVATE_KEY` | a Hardhat dev key locally; required by the live overlay | The proxy's signing key |
| `DIRECT_API_KEY` | unset; required on a live chain | The key guarding the proxy's `POST /direct`; the simulation's proxy accepts any |
| `EXT_PROXY_URL` | the recorded stack's on the simulation, else `http://localhost:6674` | The machine's proxy, and the URL `post-build.sh` registers on chain (required on a live chain) |
| `EXT_PROXY_HOST_URL` | `EXT_PROXY_URL` | The URL registered on chain, when it differs from the one `/info` is read from |
| `NORMAL_PROXY_URL` | the chain's (`http://localhost:6662` locally) | Flare's normal (FTDC) proxy, which the availability check goes through |
| `LOCAL_MODE` | `true`; `full-setup.sh` sets it from the chain | Which chain the scripts resolve when nothing names one, and whether `post-build.sh` may guess the local proxy port. It does not switch attestation |
| `MODE` | `1` in both compose files; the image bakes `0` | The node's attestation backend: `1` simulated, `0` production |
| `SIMULATED_TEE` | `true` | The tools expect tee-node's test code hash and platform (`register-tee`, `allow-tee-version`, `verify-tee`, the upload scripts); `full-setup.sh` refuses it on a mainnet |
| `SIMULATED_CODE_HASH` | unset | With `SIMULATED_TEE=true`, the test code hash `register-tee` expects instead of tee-node's (a rolling upgrade on a simulated fleet) |
| `EXPECTED_CODE_HASH` | unset | The build `allow-tee-version` allows, which the machine's report must match; the upload scripts pin to it |
| `GOVERNANCE_SIGNERS` | `INITIAL_OWNER` | Comma-separated 0x addresses that govern this extension's TEE machines |
| `GOVERNANCE_THRESHOLD` | `1` | Minimum distinct governance signatures required |
| `TEE_VERSION` | `v0.1.0` | Version string for TEE registration |
| `WAIT_TIMEOUT` | `120` | `post-build.sh`'s wait for the proxies, in seconds |
| `USE_LOCAL_SIBLINGS` | unset | `1` builds node and proxy from the sibling checkouts |
| `TEE_PROXY_IMAGE` | `ghcr.io/flare-foundation/tee-proxy:v0.0.24` | Another proxy image |
| `SOURCE_DATE_EPOCH` | the commit's timestamp | The image build's epoch, which the Dockerfile requires |
| `LOG_LEVEL` | `INFO` | The node's log level |
| `REDIS_BIND` | a free port on `127.0.0.1`, new every run | Host-side binding of the stack's redis |
| `EXT_PROXY_INTERNAL_BIND` | `0.0.0.0:6673` (`.env.example`: `127.0.0.1:6673`) | Host binding of the proxy's internal port |
| `EXT_PROXY_EXTERNAL_BIND` | `0.0.0.0:6674` | Host binding of the proxy's external port |
| `COMPOSE_NETWORK` | `docker_default` locally; the chain indexer's network live | The Docker network the stack joins |
| `INDEXER_DB_HOST`, `INDEXER_DB_PORT`, `INDEXER_DB_NAME`, `INDEXER_DB_USER`, `INDEXER_DB_PASSWORD` | the bundled indexer's | An indexer database of your own, for the rendered proxy configuration |
| `INDEXER_DB_BIND` | `127.0.0.1:3316` (Coston), `127.0.0.1:3317` (Coston2) | Host binding of the bundled indexer's database |
| `LOCAL_STACK_REDIS` | `:6382` (the simulation's fourth redis); the compose redis on a live chain | The host-process stack's redis |
| `SIM_DUMP` | `../../e2e/docker/sim_dump/deployed-addresses.json` | The simulation's addresses, for the local proxy configuration |

These are the *deployment* variables. They are unrelated to the oracle's own runtime
configuration — RPC providers, admin sets and credentials never appear in the
environment, because they arrive over the instruction and direct channels after the
machine is up. See [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md).

---

## TEE governance

Every TEE machine registers under a **governance** — a set of signer addresses and a
threshold authorising governance actions for the extension (machine-path-list updates,
for example). Two parties must agree on this set, or `register-tee` reverts with
`InvalidGovernanceHash`:

- the **TEE node**, which signs its machine data with a `governanceHash` derived from
  `(signers, threshold)`, and
- the **on-chain registry**, where the governance is registered.

Both are read from one place — your `.env`:

```bash
GOVERNANCE_SIGNERS="0xAbc...,0xDef..."   # comma-separated 0x addresses
GOVERNANCE_THRESHOLD=2
```

Unset, both default to **the machines' owner (`INITIAL_OWNER`) as sole signer, threshold 1** —
fine for development. During post-build the `set-governance` step registers the set on-chain
before `register-tee`, skipping it when the extension's latest set already matches, and the
compose file passes the same values to the node container. A node launched with neither variable
(a VM whose launch environment omits them) signs a placeholder hash, which no registered set
matches.

> This is *not* the same thing as the oracle's `SET_ADMINS` signer sets. Governance
> authorises FCC machine operations; the admin sets authorise credential installation
> inside the enclave. Different parties, different payloads, different enforcement.

---

## Ports (local dev)

The compose stack:

| Port | Where | Service |
|------|-------|---------|
| 6674 → 6664 | published on the host (`EXT_PROXY_EXTERNAL_BIND`) | Proxy external: `/info`, results, `POST /direct` |
| 6673 → 6663 | published on the host (`EXT_PROXY_INTERNAL_BIND`) | Proxy internal: the action queue the TEE polls, which the node reaches as `ext-proxy:6663` |
| a free port → 6379 | published on `127.0.0.1` (`REDIS_BIND`) | Redis, host side for debugging; `start-services.sh` picks it each run, prints it and records it in `deploy/config/extension.env` |
| 5500 | inside the extension container, not published | tee-node's configuration server |
| 7701 | loopback inside the extension container | tee-node's sign server, which the extension calls (`SIGN_PORT`) |
| 7702 | loopback inside the extension container | The extension's `/action` and `/state`, which tee-node calls (`EXTENSION_PORT`) |

A host-process stack (`start-services.sh --local`) binds its own block on the host instead:
5521 (TEE config), 7721 (TEE sign), 7722 (extension), 6683/6684 (proxy internal/external),
clear of the e2e simulation's 5500–5503, 6661–6668 and 7701/7702, with redis on the
simulation's `:6382`.

---

## Docker Compose details

In local mode every `start-services.sh` run recreates the containers: redis comes up on a fresh
port each time, and a proxy that outlives its redis or its simulation keeps stale signing-policy
state. A recreated node can come up under a new identity, which is why `full-setup.sh`
registers the machine again right after; a stale identity stays registered on the simulation.

`deploy/docker-compose.yaml` builds the node with `build.context: ..` (the repository root,
one level above the compose file, which is the Go module) and `dockerfile: Dockerfile`. The
build is self-contained: `go.mod` pins `tee-node`, which is fetched from the network and verified
against `go.sum`, so no sibling `tee-node/` checkout is needed.

To build the node and proxy from on-disk sibling checkouts instead (while developing
`tee-node`/`tee-proxy`), run `USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh`, which
selects `Dockerfile.siblings.dev` (build context `tee/`) via `deploy/docker-compose.siblings.yaml`.
Both sibling images download their Go modules from the host's module cache, which the script
serves for the duration of the build with the public proxy as fallback
(`deploy/scripts/lib/modcache-proxy.sh`); the checkouts' dependency graphs run to several hundred
modules, so on a slow link this is the difference between a minute and an hour.
Caveats in [usdx-oracle-tooling.md](usdx-oracle-tooling.md#local-development).

Rebuild the extension image after code changes:

```bash
./deploy/scripts/start-services.sh
```

```bash
./deploy/scripts/start-services.sh --chain coston2
```

Overrides go in `.env` or in front of the script, which passes them to compose:

```bash
TEE_PROXY_IMAGE=ghcr.io/flare-foundation/tee-proxy:main ./deploy/scripts/start-services.sh   # another proxy image
```

```bash
LOG_LEVEL=DEBUG ./deploy/scripts/start-services.sh
```

> The proxy image is pulled, not built; `check-versions.sh` keeps its tag aligned with the
> tee-proxy module the tooling pins.

---

## Deploying to a live chain

The live-chain flow lives in [deploy/README.md](../deploy/README.md#testing-on-a-testnet-coston_local-coston2)
rather than being repeated here, with every point that needs your input marked; "A remote
machine" there is the deployment. In outline, for a test deployment of our own
(`coston_local`, `coston2`):

```bash
EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://<tunnel> ./deploy/scripts/use-chain.sh <chain>   # creates .env.<chain> when missing → .env
./deploy/scripts/import-upstream-addresses.sh <chain>       # system addresses + the indexer's config
docker compose -f deploy/docker-compose.<indexer chain>-indexer.yaml up -d   # coston for coston_local, coston2 for coston2
./deploy/scripts/deploy-oracle.sh --chain <chain>           # our own oracle contracts
(cd tools && go run ./cmd/wire-extension)
./deploy/scripts/full-setup.sh --chain <chain>              # or full-setup-host.sh --chain <chain>
```

What differs from the simulation is the same for every live chain: the proxy needs the chain's
C-chain indexer, the machine's registered URL must be publicly reachable, and the proxy's
`/direct` needs a key. Nothing chain-specific is committed: the proxy configuration is rendered
per run from `config/proxy/extension_proxy.live.toml` into `out/proxy/`, and the indexer's from
`config/indexer/indexer-config.toml.template`, both with the chain's addresses. Only Coston (both
rows share it) and Coston2 have a bundled indexer compose file; Songbird and Flare have none, and
a stack there is a Confidential Space machine in any case, since their registries accept only
real attestation. A testnet stack still attests with the simulated code hash (`MODE=1` and
`SIMULATED_TEE=true` unless `.env` says otherwise), which the testnet registries accept.

The live stack runs on the chain indexer's Docker network, `fce-usdx-oracle-<indexer chain>`
(`fce-usdx-oracle-coston` for both Coston rows), which `start-services.sh` and
`stop-services.sh` set as `COMPOSE_NETWORK`. The live overlay interpolates the keys and the chain
from the environment, so read a running stack by its project rather than through the files:

```bash
docker compose -p deploy ps
docker compose -p deploy logs -f extension-tee
./deploy/scripts/stop-services.sh --chain <chain>
```

---

## Local dev vs a live chain

| | Local dev | Live chain |
|---|-----------|------------|
| Chain | Hardhat (`localhost:8545`) | the chain's public RPC |
| Attestation | `MODE=1`, `SIMULATED_TEE=true` | the same on a testnet test; a Confidential Space machine (`MODE=0`, `SIMULATED_TEE=false`) for a deployment, and always on a mainnet |
| Proxy config | `extension_proxy.docker.toml` | rendered from `extension_proxy.live.toml` per run |
| Indexer DB | the simulation's `indexer-db`, through `host.docker.internal` | `docker-compose.coston-indexer.yaml` or `docker-compose.coston2-indexer.yaml`, or `INDEXER_DB_*` for one of your own |
| Compose overlay | none | `docker-compose.live.yaml` |
| Network | joins `docker_default` (e2e infra) | the indexer's `fce-usdx-oracle-<indexer chain>` network |
| Proxy accessibility | `localhost:6674` | public URL (tunnel, or the machine's own) |
| Normal proxy | `localhost:6662` | `https://tee-proxy-<chain>-1.flare.rocks` (none listed for Flare) |
| Start services | `./deploy/scripts/start-services.sh` | `./deploy/scripts/start-services.sh --chain <chain>` |

---

_Companion docs: [deployment-steps.md](deployment-steps.md) (a real Confidential Space
machine) · [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (provisioning
it, in order) · [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (every command) ·
[testing.md](testing.md) (what to run when)._
