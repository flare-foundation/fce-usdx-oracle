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

> This repository checks in **only the Go implementation**. The scaffold's `LANGUAGE=<dir>`
> dispatch is still wired up throughout the scripts (a directory is an implementation iff
> `<dir>/language.env` exists), so `LANGUAGE` resolves to `go` and stays there unless you
> add one.

---

## Quick start

With local infrastructure running, one command runs the whole lifecycle:

```bash
./scripts/full-setup.sh
```

1. **Identity** — pin `EXTENSION_ID` (the reserved id 1) to `config/extension.env`
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
./scripts/start-services.sh
```

Resolves `LANGUAGE` from `.env`, builds the matching extension image (and the `tee-node`
base / `local/tee-proxy` images when needed), then brings up Docker Compose. Prefer this
over a bare `docker compose up`, which defaults to `go/Dockerfile` and ignores `LANGUAGE`.

> The base compose file joins the external `docker_default` network created by the e2e
> infrastructure. Start the e2e stack first, or this fails with "network not found".
> `full-setup.sh` handles it for you.

Three containers come up:

- **redis** — queue storage for the proxy
- **ext-proxy** — TEE proxy, using `config/proxy/extension_proxy.docker.toml`
- **extension-tee** — tee-node + the extension server, built from source

The compose file reads `EXTENSION_ID` from `config/extension.env` (written by full-setup)
and joins `docker_default` so the proxy can reach `indexer-db` and `node`.

```bash
docker compose ps
docker compose logs -f extension-tee
./scripts/stop-services.sh
```

### 2. Post-build

```bash
./scripts/post-build.sh
```

Waits for the proxy to be ready, then allows the TEE version, registers the extension's
TEE **governance** (below), and registers the TEE machine on-chain. It reads
`EXT_PROXY_URL` and `NORMAL_PROXY_URL` from your `.env`.

### Proving the round trip

With the machine registered and configuration published, run one keeper cycle against the
deployed stack:

```bash
./scripts/oracle-cycle.sh -tee <TEE1> --dry-run    # preflight only, no submission
./scripts/oracle-cycle.sh -tee <TEE1>              # request, poll, submit
```

The wire contract itself (independent of any chain) is checked by
`./scripts/test-conformance.sh`. For the full feed walkthrough see [testing.md](testing.md).

### Running single steps

```bash
cd tools && go run ./cmd/allow-tee-version -a /path/to/deployed-addresses.json -p http://localhost:6674
```

```bash
cd tools && go run ./cmd/register-tee -a /path/to/deployed-addresses.json -p http://localhost:6674 -ep http://localhost:6662 -l
```

```bash
./scripts/vendor-interfaces.sh && ./scripts/generate-bindings.sh
```

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ADDRESSES_FILE` | auto-detected | Path to `deployed-addresses.json` |
| `CHAIN_URL` | `http://127.0.0.1:8545` | Chain RPC endpoint |
| `DEPLOYMENT_PRIVATE_KEY` | Hardhat dev key | Funded private key for transactions |
| `PROXY_PRIVATE_KEY` | Hardhat dev key | Proxy signing key (used by start-services) |
| `EXTENSION_ID` | from `config/extension.env` | Extension ID (the reserved id 1, written by full-setup) |
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
| 5501 | TEE config | Set proxy URL, initial owner, extension ID |
| 6382 | Redis | Queue storage for proxy |
| 6673 | Proxy internal | TEE polls actions from the proxy queue |
| 6674 | Proxy external | Clients query results, `/info` endpoint |
| 7701 | TEE sign port | Extension calls TEE for signing/decrypting |
| 7702 | Extension server | TEE forwards `POST /action` to the extension |
| 8100 | Types server | Decodes raw instruction data to JSON |

---

## Docker Compose details

`docker-compose.yaml` uses `build.context: .` and the build is self-contained: `go.mod`
pins `tee-node`, which is fetched from the network and verified against `go.sum`, so no
sibling `tee-node/` checkout is needed.

To build the node and proxy from on-disk sibling checkouts instead (while developing
`tee-node`/`tee-proxy`), run `USE_LOCAL_SIBLINGS=1 ./scripts/start-services.sh`, which
selects `Dockerfile.siblings` (build context `tee/`) via `docker-compose.siblings.yaml`.
Caveats in [usdx-oracle-tooling.md](usdx-oracle-tooling.md#local-development).

Rebuild the extension image after code changes:

```bash
./scripts/start-services.sh
```

```bash
./scripts/start-services.sh --chain coston2
```

Overrides, in the shell or `.env`:

```bash
REGISTRY=registry.gitlab.com/flarenetwork/tee/e2e docker compose up -d
```

```bash
LOG_LEVEL=DEBUG docker compose up -d
```

> `start-services.sh` builds the `local/tee-proxy` image automatically if it is missing.
> To build it by hand: `docker build -f proxy/Dockerfile -t local/tee-proxy proxy/`

---

## Deploying to Coston2

Coston2 is Flare's public testnet. Unlike local dev it connects to the live testnet RPC,
uses real signing policies, and needs a publicly reachable proxy URL.

### Prerequisites

- A **funded Coston2 account** — a private key with testnet C2FLR, from the
  [Coston2 faucet](https://faucet.flare.network/coston2)
- **ngrok** or similar — the TEE proxy must be publicly reachable so data providers can
  deliver cosigned responses
- **Indexer DB credentials** — the proxy fetches signing policies from a Flare indexer
  database

### 1. Configure `.env`

```bash
cp .env.example .env
```

| Variable | Value | Notes |
|----------|-------|-------|
| `DEPLOYMENT_PRIVATE_KEY` | Your funded Coston2 private key | Deployments and on-chain calls |
| `INITIAL_OWNER` | Address derived from it | Owner of deployed contracts |
| `PROXY_PRIVATE_KEY` | Same or another funded key | Proxy signing |
| `CHAIN_URL` | `https://coston2-api.flare.network/ext/C/rpc` | Coston2 RPC |
| `ADDRESSES_FILE` | `./config/coston2/deployed-addresses.json` | Pre-populated |
| `LOCAL_MODE` | `false` | **Must be `false`** on live networks — this is what enables attestation |
| `NORMAL_PROXY_URL` | `https://tee-proxy-coston2-1.flare.rocks` | Flare's Coston2 normal/FTDC proxy |
| `EXT_PROXY_URL` | `https://<your-subdomain>.ngrok-free.dev` | Your tunnel URL, from step 3 |

### 2. Configure the proxy

Proxy configs live in `config/proxy/`. Coston2 needs two — one for inside Docker
(references the `redis` service name) and one for running the proxy outside it:

```bash
cp config/proxy/extension_proxy.coston2.docker.toml.example config/proxy/extension_proxy.coston2.docker.toml
```

```bash
cp config/proxy/extension_proxy.coston2.toml.example config/proxy/extension_proxy.coston2.toml
```

Then fill in `[db]` with your indexer credentials:

```toml
[db]
host = "<indexer-db-host>"
port = 3306
database = "<indexer-db-name>"
username = "<indexer-db-user>"
password = "<indexer-db-password>"
log_queries = false
```

The Coston2 contract addresses (`flare_systems_manager`, `relay`, `voter_registry`) are
already correct in the examples, and `chain_id` is `114`.

> Both files are gitignored: they hold database credentials.

### 3. Start ngrok

```bash
ngrok http 6674
```

Copy the HTTPS URL into `EXT_PROXY_URL`.

### 4–6. Deploy

```bash
./scripts/full-setup.sh --chain coston2
```

(or the phases individually: `start-services.sh --chain coston2`, then `post-build.sh`).
The oracle contracts deploy upstream; slot their addresses into
`config/coston2/deployed-addresses.json`, then prove the round trip with
`./scripts/oracle-cycle.sh -tee <TEE1>`.

Compared to a bare `docker compose up`, `start-services.sh --chain coston2`:

- sets `EXTENSION_DOCKERFILE` from `LANGUAGE`
- mounts `extension_proxy.coston2.docker.toml` instead of the local proxy config
- sets `CHAIN_URL` / `CHAIN_ID` for Coston2
- creates its own network (`extension-scaffold-coston2`) instead of joining `docker_default`

```bash
docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml ps
```

```bash
docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml logs -f extension-tee
```

```bash
./scripts/stop-services.sh --chain coston2
```

---

## Local dev vs Coston2

| | Local dev | Coston2 |
|---|-----------|---------|
| Chain | Hardhat (`localhost:8545`) | `coston2-api.flare.network` |
| `LOCAL_MODE` | `true` (skip attestation) | `false` (real attestation) |
| Proxy config | `extension_proxy.docker.toml` | `extension_proxy.coston2.docker.toml` |
| Indexer DB | Local `indexer-db` container | Remote Flare indexer |
| Network | Joins `docker_default` (e2e infra) | Own `extension-scaffold-coston2` network |
| Proxy accessibility | `localhost:6674` | Public URL via ngrok |
| Normal proxy | `localhost:6662` | `https://tee-proxy-coston2-1.flare.rocks` |
| Start services | `./scripts/start-services.sh` | `./scripts/start-services.sh --chain coston2` |

---

_Companion docs: [deployment-steps.md](deployment-steps.md) (a real Confidential Space
machine) · [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (provisioning
it, in order) · [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (every command) ·
[testing.md](testing.md) (what to run when)._
