# USDX Price Oracle — FCC Extension

A TEE extension that publishes a **backing-based price for USDX** as an FTSO custom feed.

USDX's only real market venue (BitMart) is down, so there is no market price to feed into FTSO.
The next-best truth is collateralisation: the extension reads USDX supply on three chains, asks
Hex Trust for reserve figures, and publishes `min(1, backing / supply)`. Because it runs inside
a TEE pinned by a registered code hash, consumers verify **what** computed the price rather
than trusting an operator.

```
supply  = USDX totalSupply on Flare + Ethereum + Songbird
backing = bond reserves (public API) + cash reserves (private API)
price   = min(1, backing / supply)        capped at 1: USDX redeems at par
```

Live on Coston2 and returning `$1.00000000`:

| | |
| --- | --- |
| `UsdxCustomFeed` | [`0x00c22a4e…8028`](https://coston2-explorer.flare.network/address/0x00c22a4e5f915de82C30aa6cd74E9d722D1e8028#code) — feed id `USDX/USD`, category 32 |
| `UsdxPriceStore` | [`0x786307b2…cD60`](https://coston2-explorer.flare.network/address/0x786307b2bd21A5BfE4524Ff14C0B80F9c618cD60#code) |
| `UsdxOracleInstructionSender` | [`0x8A03da10…54F0`](https://coston2-explorer.flare.network/address/0x8A03da101770A58f3a3f2737b875A3aB0ef754F0#code) |

All three are verified, so you can call `latest()` from the explorer's Read Contract tab.

> **Design docs:** [plan & TODO](docs/usdx-oracle-plan.md) · [spec](docs/usdx-oracle-spec.md) ·
> [rationale](docs/usdx-oracle-rationale.md) ·
> **[implementation findings](docs/usdx-oracle-findings.md)** — read the findings before touching
> the extension. It records the security issues found while building, each pinned by a named
> test, plus the two constraints that only surfaced on-chain (§22, §23).
>
> The [rationale doc](docs/usdx-oracle-rationale.md) explains *why* the code is shaped the way it
> is — the supply quorum and how it pins a block, the closed error vocabulary, what is compiled in
> rather than configurable. The code keeps a one-line pointer wherever that reasoning is
> load-bearing.

## How it works

```
keeper ──(1) requestPriceUpdate() ──► UsdxOracleInstructionSender ──► TeeExtensionRegistry
                                                                            │ data providers
                                                                            │ cosign + deliver
                                                                            ▼
                                                              TEE machine (Confidential Space)
    every 2 min, in the background:  3 chains × N RPC providers → totalSupply
                                     Hex public API  → bond reserves
                                     Hex private API → cash reserves
                                     price = min(1, B/S), cached
                                                                            │
    on the instruction:              sign the cached observation for THIS instructionId
                                                                            ▼
keeper ──(2) poll /action/result/<id> ──► (3) submitPrice(body, sig) ──► UsdxPriceStore
                                                                            ▼
                                                        UsdxCustomFeed ──► FtsoV2 ──► consumers
```

Five properties are load-bearing, and each exists because of something that went wrong:

**Observation runs on a timer, not per request.** tee-node allows an extension **2 seconds** to
answer an action — a `const` in its internal package — and a full observation measures 2.0–2.9s.
Every request timed out until the two were split. A request now costs one signature; the slow
work happens in `internal/observe`. Staleness is bounded at 10 minutes, past which the extension
publishes nothing (findings §23).

**Fail closed, always.** A source down, a value implausible, an observation too old: publish
nothing and let the on-chain timestamp age. A frozen feed is the designed failure mode; a
confidently wrong price is not recoverable downstream. **Consumers must check the timestamp** —
the feed cannot revert, because `FtsoV2` forwards the call and a revert would take down every
consumer at once.

**The price rounds half-up, and is capped at par.** Hex quotes reserves to the cent while supply
carries 6–18 decimals, so truncation turned a $0.004 quantisation artifact into a permanent
sub-par print. Half a ULP is $0.127 at current supply, so rounding absorbs the artifact while the
shortfall it can conceal stays bounded (findings §21).

**A missing cash figure does not stop the feed — but never changes the number.** Cash is
non-negative and the price caps at par, so a bonds-only price that already reaches par *is* the
full result. Below par it fails closed rather than publishing a degraded figure.

**The signed report binds the configuration it was computed under.** `submitPrice` rejects any
report whose `endpointsHash` differs from what `setEndpoints` published, so a machine running a
stale configuration cannot publish at all.

### Trust model in one paragraph

The keeper holds **no authority** — the store verifies the TEE signature against
`TeeMachineRegistry`, so a compromised keeper costs gas, not correctness, and anyone may submit.
What the store trusts is that the signer is a machine currently registered to this extension and
in PRODUCTION status, which transitively pins the attested code hash. The **owner key is
price-controlling by decision**: `setEndpoints` has no host allowlist, so whoever holds it
chooses the RPC sources. That trade is deliberate and written up in findings §15.

## Language

This repository implements the oracle in **Go only**. The scaffold it derives from supports
Python and TypeScript via `LANGUAGE=<dir>` (valid iff `<dir>/language.env` exists), and the
tooling, contracts and conformance harness here are still language-neutral — but no other
implementation is checked in, and the wire fixtures in `testdata/conformance/` are the acceptance
test if you add one.

> The normative spec an implementation must satisfy is
> [docs/extension-contract.md](docs/extension-contract.md).

## Repository Structure

The repo splits into a **language-neutral spine** (contracts, deployment tooling, scripts) and **pluggable language implementations**. You customize one language directory plus the Solidity contract.

```
├── go/                                 # ── Go implementation
│   ├── cmd/main.go                     # ★ Extension server entry point (standalone, for dev)
│   ├── cmd/docker/main.go              # Combined TEE node + extension (single-process image)
│   ├── cmd/start-tee/main.go           # Host-process runner for --local mode
│   ├── internal/config/config.go       # ★ OPType constants, version, port defaults
│   ├── internal/extension/extension.go # ★ MAIN CUSTOMIZATION POINT: processAction routing
│   ├── internal/extension/utils.go     # Boilerplate: actionHandler, buildResult
│   ├── pkg/types/types.go              # ★ Request/response types
│   ├── Dockerfile                      # Single-process image (distroless)
│   └── language.env                    # Language manifest (marks this dir as an implementation)
├── python/                             # ── Python implementation
│   ├── base/                           # Framework: server, wire types, encoding, node client
│   ├── app/config.py                   # ★ OPType constants and version
│   ├── app/handlers.py                 # ★ MAIN CUSTOMIZATION POINT: your handlers
│   ├── app/abi.py                      # ★ ABI decoding for non-JSON payloads
│   ├── tests/                          # pytest suite
│   ├── Dockerfile                      # Two-process image (tee-node binary + python)
│   └── language.env
├── typescript/                         # ── TypeScript implementation
│   ├── src/base/                       # Framework: server, wire types, encoding, node client
│   ├── src/app/config.ts               # ★ OPType constants and version
│   ├── src/app/handlers.ts             # ★ MAIN CUSTOMIZATION POINT: your handlers
│   ├── src/app/abi.ts                  # ★ ABI decoding for non-JSON payloads
│   ├── src/__tests__/                  # vitest suite
│   ├── Dockerfile                      # Two-process image (tee-node binary + node)
│   └── language.env
│
├── contracts/InstructionSender.sol     # ★ Your extension's on-chain entry point (shared)
├── docker/node-base.Dockerfile         # Shared tee-node builder for non-Go images
├── testdata/conformance/               # Golden wire fixtures, asserted against every language
├── config/
│   ├── extension.env                   # Generated by pre-build (gitignored)
│   └── proxy/extension_proxy.toml      # Proxy config (Redis, DB, ports, addresses)
├── scripts/                            # ── Language-neutral
│   ├── full-setup.sh                   # Chains all phases: pre-build → compose → post-build → test
│   ├── pre-build.sh                    # Compile + deploy + register → writes config
│   ├── post-build.sh                   # Allow TEE version + register TEE on-chain
│   ├── test.sh                         # On-chain end-to-end test (identical for all languages)
│   ├── test-unit.sh                    # Unit tests, dispatched via language.env
│   ├── test-conformance.sh             # Wire-contract conformance, no chain required
│   ├── check-versions.sh               # Fails when dependency pins drift apart
│   ├── build-node-base.sh              # Builds the shared tee-node base image
│   ├── generate-bindings.sh            # Compile contract → generate Go bindings
│   └── lib/{language,versions}.sh      # Language resolution + version derivation
├── tools/                              # ── Language-neutral deployment tooling (Go)
│   ├── cmd/deploy-contract/            # Deploys InstructionSender to chain
│   ├── cmd/register-extension/         # Registers extension on TeeExtensionRegistry
│   ├── cmd/allow-tee-version/          # Registers TEE code hash as allowed version
│   ├── cmd/register-tee/               # Registers extension TEE machine on-chain
│   ├── cmd/run-test/                   # Sends instructions and verifies results
│   └── pkg/utils/instructions.go       # Deploy, SetExtensionId, SendSayHello helpers
├── docker-compose.yaml                 # Redis + proxy + extension-tee
├── foundry.toml                        # Foundry config for compiling contracts
└── .env.example                        # Sample env vars, including LANGUAGE

★ = Files developers MUST modify for their extension
```

`tools/` is deliberately independent of every language implementation, which is what lets one deployment and test path serve all of them.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ADDRESSES_FILE` | auto-detected | Path to `deployed-addresses.json` |
| `CHAIN_URL` | `http://127.0.0.1:8545` | Chain RPC endpoint |
| `DEPLOYMENT_PRIVATE_KEY` | Hardhat dev key | Funded private key for transactions |
| `PROXY_PRIVATE_KEY` | Hardhat dev key | Proxy signing key (used by start-services) |
| `EXTENSION_ID` | from `config/extension.env` | Extension ID (bytes32 hex, set by pre-build) |
| `INITIAL_OWNER` | derived from `DEPLOYMENT_PRIVATE_KEY` | Initial contract owner address |
| `EXT_PROXY_URL` | `http://localhost:6674` | Extension proxy URL (post-build, test) |
| `NORMAL_PROXY_URL` | `http://localhost:6662` | Normal/FTDC proxy URL (post-build) |
| `EXTENSION_OWNER_KEY` | (empty, falls back to `DEPLOYMENT_PRIVATE_KEY`) | Private key override for AddTeeVersion |
| `TEE_VERSION` | `v0.1.0` | Version string for TEE registration |
| `GOVERNANCE_SIGNERS` | `INITIAL_OWNER` (deployer) | Comma-separated 0x addresses that govern this extension's TEE machines |
| `GOVERNANCE_THRESHOLD` | `1` | Minimum distinct governance signatures required |
| `LOCAL_MODE` | `true` | Skip attestation in local dev |
| `WAIT_TIMEOUT` | `120` | Service wait timeout in seconds |
| `INSTRUCTION_SENDER` | from `config/extension.env` | InstructionSender contract address (test) |

### TEE governance

Every TEE machine registers under a **governance** — a set of signer addresses
and a threshold that authorize governance actions for the extension (e.g.
machine-path-list updates). Two parties must agree on this set, or `register-tee`
reverts with `InvalidGovernanceHash`:

- the **TEE node**, which signs its machine data with a `governanceHash` derived
  from `(signers, threshold)`, and
- the **on-chain registry**, where the governance is registered.

The scaffold keeps them consistent by reading both from one place — your `.env`:

```bash
GOVERNANCE_SIGNERS="0xAbc...,0xDef..."   # comma-separated 0x addresses
GOVERNANCE_THRESHOLD=2
```

If unset, both default to **the deployer (`INITIAL_OWNER`) as the sole signer,
threshold 1** — fine for development. During `post-build`, the `set-governance`
step registers the set on-chain (idempotently) before `register-tee`, and the
same values are passed to the node container via Docker Compose.

## Prerequisites

Always required:

- **Foundry** (`forge`) — compiles the Solidity contract
- **jq** — extracts ABI/bytecode from Foundry output, and drives the conformance harness
- **Go 1.25.1+** — the deployment tooling in `tools/` is Go regardless of your extension language
- **Docker** — builds and runs the extension image
- **Running local infrastructure** — Hardhat node with deployed Flare contracts (via `docker compose up` from the `e2e/` repo)

Additionally, for your chosen `LANGUAGE`:

| Language | Also needs |
|---|---|
| `go` | nothing beyond the above |
| `python` | Python 3.11+ (a venv is created automatically) |
| `typescript` | Node 22+ and npm |

## Creating Your Extension

The scaffold ships with a working Hello World. To build your extension you modify four things: the operation constants, the handlers, the Solidity contract, and the test assertions.

| # | File | What you do |
|---|------|-------------|
| 1 | `<lang>` config — `go/internal/config/config.go`, `python/app/config.py`, or `typescript/src/app/config.ts` | Define your OPType and OPCommand constants |
| 2 | `<lang>` handlers — `go/internal/extension/extension.go`, `python/app/handlers.py`, or `typescript/src/app/handlers.ts` | Implement your action handlers and state |
| 3 | `contracts/InstructionSender.sol` | Add matching `bytes32` constants and send functions |
| 4 | `tools/cmd/run-test/main.go` | Write test payloads and response assertions |

Go additionally has `go/pkg/types/types.go` for request/response structs; Python and TypeScript declare those shapes inline in the handlers.

The key link between your Solidity contract and your handlers is the **OPType** and **OPCommand** pair, which must match exactly across every layer:

```
Solidity:    bytes32 constant OP_TYPE_GREETING      = bytes32("GREETING");
             bytes32 constant OP_COMMAND_SAY_HELLO  = bytes32("SAY_HELLO");

Go:          OPTypeGreeting    = "GREETING"        // internal/config/config.go
             OPCommandSayHello = "SAY_HELLO"

Python:      OP_TYPE_GREETING     = "GREETING"     # app/config.py
             OP_COMMAND_SAY_HELLO = "SAY_HELLO"

TypeScript:  OP_TYPE_GREETING     = "GREETING"     // src/app/config.ts
             OP_COMMAND_SAY_HELLO = "SAY_HELLO"
```

A mismatch means the action falls through to "unsupported op type" (HTTP 501).

Every handler follows the same 4-step pattern in all three languages: decode the request, validate it, execute your logic, return a result.

> ### **→ [Read the Extension Development Guide](docs/extension-guide.md)** for a detailed walkthrough, and **[docs/extension-contract.md](docs/extension-contract.md)** for the normative wire and container contract.

## Quick Start

With local infrastructure running (`docker compose up` from `e2e/`), run everything in one shot:

```bash
./scripts/full-setup.sh --test
```

This runs the full lifecycle:
1. **Pre-build** — compile contracts, deploy, register extension, write `config/extension.env`
2. **Docker Compose** — start redis, proxy, and extension TEE as containers
3. **Post-build** — register TEE version and TEE machine on-chain
4. **Test** — send instructions and verify results

## Making It Your Own

This repository works out of the box as a Hello World extension. When you're ready to build your own extension, you'll rename the HelloWorld placeholders to your own names and replace the SAY_HELLO logic with your own operations.

> ### **→ [Follow the Making It Your Own guide](docs/manual-setup.md)** for step-by-step renaming instructions.
>
> Using [Claude Code](https://claude.ai/code)? Run `/rename-scaffold` to do it automatically.

### Step-by-step

If you prefer running each phase individually:

#### 1. Pre-build

```bash
./scripts/pre-build.sh
```

Deploys your contract, registers the extension, and writes `config/extension.env` with `EXTENSION_ID` and `INSTRUCTION_SENDER`.

To override defaults:
```bash
ADDRESSES_FILE=/path/to/deployed-addresses.json CHAIN_URL=http://your-node:8545 ./scripts/pre-build.sh
```

#### 2. Start services

```bash
./scripts/start-services.sh
```

Resolves `LANGUAGE` from `.env`, builds the matching extension image (and `tee-node` base / `local/tee-proxy` when needed), then brings up Docker Compose. Prefer this over a bare `docker compose up` — raw compose defaults to `go/Dockerfile` and ignores `LANGUAGE=typescript` / `python`.

> **Note:** The base compose file joins the external `docker_default` network created by the e2e infrastructure. Make sure the e2e stack is running (`docker compose up` from the `e2e/` repo) before this step, or the command will fail with a "network not found" error. If you used `full-setup.sh`, this is handled automatically.

This starts three containers:
- **redis** — queue storage for the proxy
- **ext-proxy** — TEE proxy using `config/proxy/extension_proxy.docker.toml`
- **extension-tee** — your extension: tee-node + extension server built from source

The compose file reads `EXTENSION_ID` from `config/extension.env` (written by pre-build). It joins the infrastructure network (`docker_default`) so the proxy can reach `indexer-db` and `node`.

Check status or stop:
```bash
docker compose ps
docker compose logs -f extension-tee
./scripts/stop-services.sh
```

#### 3. Post-build

```bash
./scripts/post-build.sh
```

Waits for the proxy to be ready, then registers the TEE version and TEE machine on-chain.

#### 4. Test

```bash
./scripts/test.sh
```

## Running Individual Steps

You can run the deploy and register steps independently:

```bash
# Deploy only
cd tools && go run ./cmd/deploy-contract -a /path/to/deployed-addresses.json

# Register with a specific contract address
cd tools && go run ./cmd/register-extension \
  -a /path/to/deployed-addresses.json \
  --instructionSender 0xYourContractAddress
```

You can also run binding generation standalone:

```bash
./scripts/generate-bindings.sh
```

Post-build steps can also be run individually:

```bash
# Allow TEE version
cd tools && go run ./cmd/allow-tee-version \
  -a /path/to/deployed-addresses.json \
  -p http://localhost:6674

# Register TEE machine
cd tools && go run ./cmd/register-tee \
  -a /path/to/deployed-addresses.json \
  -p http://localhost:6674 \
  -ep http://localhost:6662 \
  -l
```

### Docker Compose details

The `docker-compose.yaml` uses `build.context: .` — the build is self-contained: `go.mod` pins `tee-node` and it is fetched from the network (verified against `go.sum`), so no sibling `tee-node/` checkout is needed. To build the node + proxy from on-disk sibling checkouts instead (while developing `tee-node`/`tee-proxy`), run `USE_LOCAL_SIBLINGS=1 ./scripts/start-services.sh`, which selects `Dockerfile.siblings` (build context `tee/`) via `docker-compose.siblings.yaml`.

To rebuild the extension image after code changes:
```bash
./scripts/start-services.sh            # local
./scripts/start-services.sh --chain coston2
```

Environment variable overrides (set in shell or `.env`):
```bash
# Use a remote image registry instead of locally-built images
REGISTRY=registry.gitlab.com/flarenetwork/tee/e2e docker compose up -d

# Verbose logging
LOG_LEVEL=DEBUG docker compose up -d
```

> **Building the proxy image locally:** The `tee-proxy` image is built automatically by `start-services.sh` if it doesn't exist. To build it manually, run `docker build -f proxy/Dockerfile -t local/tee-proxy proxy/` from the extension root.

### Ports (local dev)

| Port | Service | Purpose |
|------|---------|---------|
| 5501 | TEE config | Set proxy URL, initial owner, extension ID |
| 6382 | Redis | Queue storage for proxy |
| 6673 | Proxy internal | TEE polls actions from proxy queue |
| 6674 | Proxy external | Clients query results, `/info` endpoint |
| 7701 | TEE sign port | Extension calls TEE for signing/encrypting |
| 7702 | Extension server | TEE forwards `POST /action` to extension |
| 8100 | Types server | Decodes raw instruction data to JSON |

## Testing Your Extension

> ### **→ [Read the Testing Guide](docs/testing.md)** — including a full walkthrough,
> *"Running the USDX oracle end to end yourself"*, with what to check at each hop, how to read
> the result, and what every revert means.

**Start with the checks that need no chain** — they catch most breakage in seconds:

```bash
cd go && go test ./... && cd .. && ./scripts/test-conformance.sh go && forge test
```

**Then prove a real observation locally.** This is the highest-value local test: the real
observation path against live Flare, Ethereum, Songbird and Hex, with no chain, no TEE and no
credentials. Run `cmd/mocknode` and `cmd/devserver`, apply a config with
`./scripts/set-endpoints.sh testdata/endpoints/public-only.json 8099`, then POST a GET_PRICE
action. Healthy output is `lastPrice: "100000000"` with `hex_cash: not_configured`.

**On-chain, the full path** is five commands once the machine is registered and at PRODUCTION:

```bash
cd tools && go run ./cmd/send-endpoints -instructionSender <SENDER> -config ../testdata/endpoints/public-only.json
```

```bash
cd tools && go run ./cmd/send-admins -instructionSender <SENDER> -config ../testdata/admins/dev-wellknown-keys.json
```

> `dev-wellknown-keys.json` names the addresses of private keys 1, 2 and 3 — fine for local
> and testnet work, never for a live feed. It publishes two sets: `backing` (1-of-1, for
> `hex_api_key`) and `providers` (2-of-3, for the RPC credentials). Both are memory-only, so
> they must be republished after every extension restart, and before any `SET_SECRET`.

```bash
cd tools && go run ./cmd/run-test -instructionSender <SENDER>
```

```bash
cd tools && go run ./cmd/submit-price -instructionSender <SENDER>
```

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <FEED> 'getCurrentFeed()(uint256,int8,uint64)'
```

`run-test` validates the wire format without submitting; `submit-price` runs the whole keeper
cycle — request, poll, decode, preflight every store guard, submit — and `-dry-run` stops before
the transaction. The final call returns `(value, decimals, observedAt)`; read it as
`value / 10^decimals`, and **always check the age**.

Three things that are easy to misread as failures:

| You see | It means |
| --- | --- |
| `NotNewer: the store already holds observedAt=…` | Correct. The store takes strictly increasing timestamps, which is what makes replay and out-of-order delivery harmless. With ≥2 keepers exactly one submission wins per cycle |
| `attempt 1: proxy returned 404` then success | The result was not in the proxy's storage yet. The bounded poll waited it out |
| Two instructions with the **same** `observedAt` | The observation cache. Both requests signed the same cached observation with different instruction ids — see "How it works" |

**Publish the contracts to the explorer** so the acceptance rules are readable by anyone
relying on them:

```bash
./scripts/verify-contracts.sh
```

### The three test layers

The scaffold tests at three levels, cheapest first. Only the last needs a chain.

**1. Unit tests** — your implementation's own tests, dispatched by `LANGUAGE`:

```bash
./scripts/test-unit.sh              # the language from .env
./scripts/test-unit.sh --all        # every language
```

**2. Conformance** — replays the golden wire fixtures in `testdata/conformance/` against a running extension and diffs the responses. No chain, no proxy, no Docker; runs in seconds:

```bash
./scripts/test-conformance.sh --all
```

This is what guarantees the three implementations stay byte-identical on the wire, and it is the acceptance test for any new language. If you change a handler's output shape, regenerate the fixtures with `./python/.venv/bin/python testdata/conformance/gen_fixtures.py`.

**3. On-chain end-to-end** — `./scripts/test.sh`, identical for every language. It stops at the proxy, so it proves the machine produced a valid signed report but puts nothing on-chain. To drive the full feed — endpoints published, price relayed into `UsdxPriceStore`, `getCurrentFeed()` returning it — follow "Running the USDX oracle end to end yourself" in [docs/testing.md](docs/testing.md).

Deployment tooling has its own tests:

```bash
cd tools && go test ./...                                      # unit, no chain
cd tools && go test -tags integration ./integration/ -v -count=1  # needs a chain node
```

See the [Testing Guide](docs/testing.md) for what each layer covers and how to configure the integration tests.

## Deploying to Coston2

Coston2 is Flare's public testnet. Unlike local dev (which runs against a Hardhat node with pre-deployed contracts), Coston2 deployment connects to the live testnet RPC, uses real signing policies, and requires a publicly accessible proxy URL.

### Prerequisites

- A **funded Coston2 account** — you need a private key with testnet C2FLR for gas. Get testnet tokens from the [Coston2 faucet](https://faucet.flare.network/coston2).
- **ngrok** (or similar tunneling tool) — the TEE proxy must be publicly reachable so data providers can deliver cosigned responses. Install from [ngrok.com](https://ngrok.com).
- **Indexer DB credentials** — the proxy needs access to a Flare indexer database to fetch signing policies. Ask the Flare team for credentials, or use the values in the example config.

### 1. Configure `.env`

Copy `.env.example` and fill in your values:

```bash
cp .env.example .env
```

Key values to set:

| Variable | Value | Notes |
|----------|-------|-------|
| `DEPLOYMENT_PRIVATE_KEY` | Your funded Coston2 private key | Used for contract deployments and on-chain calls |
| `INITIAL_OWNER` | Address derived from `DEPLOYMENT_PRIVATE_KEY` | Owner of deployed contracts |
| `PROXY_PRIVATE_KEY` | Same or different funded key | Used by the proxy for signing |
| `CHAIN_URL` | `https://coston2-api.flare.network/ext/C/rpc` | Coston2 RPC endpoint |
| `ADDRESSES_FILE` | `./config/coston2/deployed-addresses.json` | Pre-populated with Coston2 contract addresses |
| `LOCAL_MODE` | `false` | **Must be `false`** on live networks (enables attestation) |
| `NORMAL_PROXY_URL` | `https://tee-proxy-coston2-1.flare.rocks` | Flare's Coston2 normal/FTDC proxy |
| `EXT_PROXY_URL` | `https://<your-subdomain>.ngrok-free.dev` | Your ngrok tunnel URL (see step 3) |

### 2. Configure the proxy

The proxy config files live in `config/proxy/`. For Coston2, there are two files:

- **`extension_proxy.coston2.docker.toml`** — used inside Docker (references `redis` service name)
- **`extension_proxy.coston2.toml`** — used when running the proxy outside Docker

Copy from the example files if starting fresh:

```bash
cp config/proxy/extension_proxy.coston2.docker.toml.example config/proxy/extension_proxy.coston2.docker.toml
cp config/proxy/extension_proxy.coston2.toml.example config/proxy/extension_proxy.coston2.toml
```

Then fill in the `[db]` section with your indexer database credentials:

```toml
[db]
host = "<indexer-db-host>"
port = 3306
database = "<indexer-db-name>"
username = "<indexer-db-user>"
password = "<indexer-db-password>"
log_queries = false
```

The Coston2 contract addresses (`flare_systems_manager`, `relay`, `voter_registry`) are already set correctly in the example files. The `chain_id` is `114` (Coston2).

### 3. Start ngrok

The extension proxy must be publicly accessible so that data providers can reach it. Start an ngrok tunnel pointing to the proxy's external port (6674):

```bash
ngrok http 6674
```

Copy the generated HTTPS URL (e.g., `https://abc123.ngrok-free.dev`) and set it as `EXT_PROXY_URL` in your `.env`.

### 4. Pre-build (deploy & register)

```bash
./scripts/pre-build.sh
```

This deploys your `InstructionSender` contract to Coston2 and registers your extension on the `TeeExtensionRegistry`. The scripts auto-detect Coston2 addresses when `LOCAL_MODE=false`.

### 5. Start services

Use `start-services.sh` — do **not** call `docker compose` directly. The script resolves `LANGUAGE` from `.env` (so TypeScript/Python actually build), builds the `tee-node` base image and `local/tee-proxy` when needed, attaches the Coston2 compose overlay, and waits for the proxy to be ready:

```bash
./scripts/start-services.sh --chain coston2
```

Compared to a bare `docker compose up`, this:
- Sets `EXTENSION_DOCKERFILE` from `LANGUAGE` (raw compose defaults to `go/Dockerfile`)
- Mounts `config/proxy/extension_proxy.coston2.docker.toml` instead of the local proxy config
- Sets `CHAIN_URL` / `CHAIN_ID` for Coston2
- Creates its own network (`extension-scaffold-coston2`) instead of joining the local e2e `docker_default` network

Check status:
```bash
docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml ps
docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml logs -f extension-tee
```

### 6. Post-build (register TEE)

```bash
./scripts/post-build.sh
```

This allows the TEE version, registers the extension's TEE **governance** (see [TEE governance](#tee-governance)), and registers the TEE machine on-chain. It reads `EXT_PROXY_URL` and `NORMAL_PROXY_URL` from your `.env`, so make sure ngrok is running and the URL is correct.

### 7. Test

```bash
./scripts/test.sh
```

Sends instructions on-chain via your deployed `InstructionSender` and polls the proxy for results.

### Stopping Coston2 services

```bash
./scripts/stop-services.sh --chain coston2
```

### Coston2 vs Local Dev — Key Differences

| | Local Dev | Coston2 |
|---|-----------|---------|
| Chain | Hardhat (`localhost:8545`) | `coston2-api.flare.network` |
| `LOCAL_MODE` | `true` (skip attestation) | `false` (real attestation) |
| Proxy config | `extension_proxy.docker.toml` | `extension_proxy.coston2.docker.toml` |
| Indexer DB | Local `indexer-db` container | Remote Flare indexer |
| Network | Joins `docker_default` (e2e infra) | Own `extension-scaffold-coston2` network |
| Proxy accessibility | `localhost:6674` | Public URL via ngrok |
| Normal proxy | `localhost:6662` | `https://tee-proxy-coston2-1.flare.rocks` |
| Start services | `./scripts/start-services.sh` | `./scripts/start-services.sh --chain coston2` |

## Further Reading

**The oracle**

- **[Implementation findings](docs/usdx-oracle-findings.md)** — every issue found while building, each pinned by a named test. Read this first
- **[Production setup runbook](docs/usdx-oracle-production-setup.md)** — configuration on-chain, credentials under two admin sets, and starting the feed. Who holds what, and in which order
- [Plan & TODO](docs/usdx-oracle-plan.md) — decisions, open questions, milestones, and the current Coston2 deployment
- [Spec](docs/usdx-oracle-spec.md) — price definition, inputs, verified addresses, work packages
- [Keeper specification](docs/usdx-oracle-keeper.md) — the cycle and its reliability semantics
- [Source availability](docs/usdx-oracle-availability.md) — what happens when a source is down, and why degradation was rejected
- [Monitoring](docs/usdx-oracle-monitoring.md) · [mint/redeem dynamics](docs/usdx-oracle-mint-redeem-dynamics.md)
- Google-Doc-ready HTML of the above: [docs/usdx-oracle-index.html](docs/usdx-oracle-index.html)

**Building your extension**

- [Extension Development Guide](docs/extension-guide.md) — how the code works and how to add your own logic
- [Working in Multiple Languages](docs/languages.md) — choosing a language, working in each, and **adding your own**
- [Making It Your Own](docs/manual-setup.md) — renaming the scaffold to your own extension
- [InstructionSender Contract](docs/instruction-sender.md) — how the on-chain contract works and how to customize it

**Reference**

- [Extension Container Contract](docs/extension-contract.md) — the normative wire format and container spec every implementation must satisfy
- [Testing Guide](docs/testing.md) — the test layers, conformance fixtures, and what to run when
- [Reproducibility](REPRODUCIBILITY.md) — what each language's build actually guarantees

**Per-language**

- [go/README.md](go/README.md) — the only implementation checked in here; see [Working in Multiple Languages](docs/languages.md) to add another
