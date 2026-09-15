# Running the stack — local dev and Coston2 via Docker Compose

Everything the README used to carry about *bringing the containers up*: prerequisites,
the phase scripts, ports, environment variables, TEE governance, and the Coston2
overlay.

This is the **Docker Compose** path — three containers on your machine, talking to a
chain. It is not how a production machine runs: that is a Confidential Space VM with
real attestation, and it lives in [deployment-steps.md](deployment-steps.md). Once a
machine is up, [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) is
the order to provision it in, and [usdx-oracle-tooling.md](usdx-oracle-tooling.md) is
what every command does.

---

## Prerequisites

- **Foundry** (`forge`) — compiles the Solidity contracts
- **jq** — extracts ABI/bytecode from Foundry output, and drives the conformance harness
- **Go 1.25.1+** — both `go/` and the deployment tooling in `tools/`
- **Docker** — builds and runs the extension image
- **Running local infrastructure** — a Hardhat node with the Flare contracts deployed
  (`docker compose up` from the `e2e/` repo). Not needed for the Coston2 path.


---

## Quick start

For the complete local run — simulation, oracle contracts, publications and a keeper cycle —
follow [local-e2e.md](local-e2e.md). The rest of this page is the Docker Compose stack on its own.

With local infrastructure running, one command runs the whole lifecycle:

```bash
./deploy/scripts/full-setup.sh
```

1. **Identity** — pin `EXTENSION_ID` (the reserved id 1) to `deploy/config/extension.env`
2. **Docker Compose** — start redis, the proxy, and the extension TEE as containers
3. **Post-build** — register the TEE version, the governance, and the TEE machine on-chain

The oracle contracts (`TeeOracleInstructionsSender` + `TeeOracleFeedStore`) are **not**
deployed here — that is upstream's `deploy-tee-oracle.sh` plus governance follow-ups (see
[deployment-steps.md](deployment-steps.md)). Once they are on-chain, slot their addresses into
`deployed-addresses.json`.

---

## The phases individually

### 1. Start services

```bash
./deploy/scripts/start-services.sh
```

Builds the extension image, then brings up Docker Compose; the proxy is tee-proxy's published
image at the tag pinned in `deploy/docker-compose.yaml` (with `USE_LOCAL_SIBLINGS=1`, a build of
the sibling checkout instead).

> The base compose file joins the external `docker_default` network created by the e2e
> infrastructure. Start the e2e stack first, or this fails with "network not found".
> `full-setup.sh` handles it for you.

Three containers come up:

- **redis** — queue storage for the proxy
- **ext-proxy** — TEE proxy, using `deploy/config/proxy/extension_proxy.docker.toml`
- **extension-tee** — tee-node + the extension server, built from source

The compose file reads `EXTENSION_ID` from `deploy/config/extension.env` (written by full-setup)
and joins `docker_default` so the proxy can reach `indexer-db` and `node`.

```bash
docker compose ps
docker compose logs -f extension-tee
./deploy/scripts/stop-services.sh
```

### 2. Post-build

```bash
./deploy/scripts/post-build.sh
```

Waits for the proxy to be ready, then allows the TEE version, registers the extension's
TEE **governance** (below), and registers the TEE machine on-chain. It reads
`EXT_PROXY_URL` and `NORMAL_PROXY_URL` from your `.env`.

### Proving the round trip

With the machine registered and configuration published, run one keeper cycle against the
deployed stack:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE1> --dry-run    # preflight only, no submission
./deploy/scripts/oracle-cycle.sh -tee <TEE1>              # request, poll, submit
```

The wire contract itself (independent of any chain) is checked by `TestConformance`
(`go test ./internal/extension/`). For the full feed walkthrough see [testing.md](testing.md).

### Running single steps

```bash
cd tools && go run ./cmd/allow-tee-version -a /path/to/deployed-addresses.json -p http://localhost:6674
```

```bash
cd tools && go run ./cmd/register-tee -a /path/to/deployed-addresses.json -p http://localhost:6674 -ep http://localhost:6662 -l
```

```bash
./deploy/scripts/generate-bindings.sh
```

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ADDRESSES_FILE` | auto-detected | Path to `deployed-addresses.json` |
| `CHAIN_URL` | `http://127.0.0.1:8545` | Chain RPC endpoint |
| `DEPLOYMENT_PRIVATE_KEY` | Hardhat dev key | Funded private key for transactions |
| `PROXY_PRIVATE_KEY` | Hardhat dev key | Proxy signing key (used by start-services) |
| `EXTENSION_ID` | from `deploy/config/extension.env` | Extension ID (the reserved id 1, written by full-setup) |
| `INITIAL_OWNER` | derived from `DEPLOYMENT_PRIVATE_KEY` | Initial contract owner address |
| `EXT_PROXY_URL` | `http://localhost:6674` | Extension proxy URL (post-build, test) |
| `NORMAL_PROXY_URL` | `http://localhost:6662` | Normal/FTDC proxy URL (post-build) |
| `EXTENSION_OWNER_KEY` | falls back to `DEPLOYMENT_PRIVATE_KEY` | Private key override for AddTeeVersion |
| `TEE_VERSION` | `v0.1.0` | Version string for TEE registration |
| `GOVERNANCE_SIGNERS` | `INITIAL_OWNER` (deployer) | Comma-separated 0x addresses that govern this extension's TEE machines |
| `GOVERNANCE_THRESHOLD` | `1` | Minimum distinct governance signatures required |
| `LOCAL_MODE` | `true` | Skip attestation in local dev |
| `WAIT_TIMEOUT` | `120` | Service wait timeout in seconds |

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

Unset, both default to **the deployer (`INITIAL_OWNER`) as sole signer, threshold 1** —
fine for development. During post-build the `set-governance` step registers the set
on-chain idempotently before `register-tee`, and the same values are passed to the node
container via Docker Compose.

> This is *not* the same thing as the oracle's `SET_ADMINS` signer sets. Governance
> authorises FCC machine operations; the admin sets authorise credential installation
> inside the enclave. Different parties, different payloads, different enforcement.

---

## Ports (local dev)

| Port | Service | Purpose |
|------|---------|---------|
| 5500 | TEE config | Set proxy URL, initial owner, extension ID |
| chosen per run | Redis | Host-side debug binding for the proxy's queue; `start-services.sh` picks a free port each run, prints it and records it in `deploy/config/extension.env` (`REDIS_BIND` overrides) |
| 6673 | Proxy internal | TEE polls actions from the proxy queue |
| 6674 | Proxy external | Clients query results, `/info` endpoint |
| 7701 | TEE sign port | Extension calls TEE for signing/decrypting |
| 7702 | Extension server | TEE forwards `POST /action` to the extension |

A host-process stack (`start-services.sh --local`) binds its own block on the host instead:
5521 (TEE config), 7721 (TEE sign), 7722 (extension), 6683/6684 (proxy internal/external),
clear of the e2e simulation's 5500–5503, 6661–6668 and 7701/7702.
| 8100 | Types server | Decodes raw instruction data to JSON |

---

## Docker Compose details

In local mode every `start-services.sh` run recreates the containers: redis comes up on a fresh
port each time, and a proxy that outlives its redis or its simulation keeps stale signing-policy
state. A recreated node can come up under a new identity, which is why `full-setup.sh`
registers the machine again right after; a stale identity stays registered on the simulation.

`deploy/docker-compose.yaml` uses `build.context: .` and the build is self-contained: `go.mod`
pins `tee-node`, which is fetched from the network and verified against `go.sum`, so no
sibling `tee-node/` checkout is needed.

To build the node and proxy from on-disk sibling checkouts instead (while developing
`tee-node`/`tee-proxy`), run `USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh`, which
selects `Dockerfile.siblings` (build context `tee/`) via `deploy/docker-compose.siblings.yaml`.
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

Overrides, in the shell or `.env`:

```bash
TEE_PROXY_IMAGE=ghcr.io/flare-foundation/tee-proxy:main docker compose up -d   # another proxy image
```

```bash
LOG_LEVEL=DEBUG docker compose up -d
```

> The proxy image is pulled, not built; `check-versions.sh` keeps its tag aligned with the
> tee-proxy module the tooling pins.

---

## Deploying to a live chain

The live-chain flow lives in [deploy/README.md](../deploy/README.md#testing-on-a-testnet-coston-coston2)
rather than being repeated here, with every point that needs your input marked; "A remote
machine" there is the deployment. In outline, with `<chain>` any of them:

```bash
./deploy/scripts/use-chain.sh <chain>                      # .env.<chain> → .env
./deploy/scripts/import-upstream-addresses.sh <chain>      # addresses + the indexer's config
docker compose -f deploy/docker-compose.<chain>-indexer.yaml up -d
./deploy/scripts/deploy-oracle.sh --chain <chain>          # test deployment only
(cd tools && go run ./cmd/wire-extension)
./deploy/scripts/full-setup.sh --chain <chain>             # or full-setup-host.sh --chain <chain>
```

What differs from the simulation is the same for every live chain: the proxy needs the chain's
C-chain indexer, the machine's registered URL must be publicly reachable, and `LOCAL_MODE=false`
turns attestation on. Nothing chain-specific is committed any more: the proxy configuration is
rendered per run from `config/proxy/extension_proxy.live.toml`, and the indexer's from
`config/indexer/indexer-config.toml.template`, both with the chain's addresses.

```bash
docker compose -f deploy/docker-compose.yaml -f deploy/docker-compose.live.yaml ps
docker compose -f deploy/docker-compose.yaml -f deploy/docker-compose.live.yaml logs -f extension-tee
./deploy/scripts/stop-services.sh --chain <chain>
```

---

## Local dev vs a live chain

| | Local dev | Live chain |
|---|-----------|------------|
| Chain | Hardhat (`localhost:8545`) | the chain's public RPC |
| `LOCAL_MODE` | `true` (skip attestation) | `false` (real attestation) |
| Proxy config | `extension_proxy.docker.toml` | rendered from `extension_proxy.live.toml` per run |
| Indexer DB | the simulation's `indexer-db` container | `docker-compose.<chain>-indexer.yaml`, or `INDEXER_DB_*` for one of your own |
| Compose overlay | none | `docker-compose.live.yaml` |
| Network | joins `docker_default` (e2e infra) | own `fce-usdx-oracle-<chain>` network |
| Proxy accessibility | `localhost:6674` | public URL (tunnel, or the machine's own) |
| Normal proxy | `localhost:6662` | `https://tee-proxy-<chain>-1.flare.rocks` |
| Start services | `./deploy/scripts/start-services.sh` | `./deploy/scripts/start-services.sh --chain <chain>` |

---

_Companion docs: [deployment-steps.md](deployment-steps.md) (a real Confidential Space
machine) · [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (provisioning
it, in order) · [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (every command) ·
[testing.md](testing.md) (what to run when)._
