# deploy/ — running and deploying the USDX oracle extension

Everything an operator runs lives here: the compose stack, the proxy configuration,
the operator inputs (endpoint groups, admin signer sets), and the scripts that take an
extension from a fresh checkout to a machine publishing prices. The Go tooling the
scripts call lives in `tools/`; the extension itself is the module at the repository
root.

```
deploy/
├── scripts/            the operator scripts, listed below
├── scripts/lib/        helpers sourced by the scripts (version pins, module-cache proxy)
├── config/
│   ├── endpoints/      endpoint-group configurations published with SET_ENDPOINTS
│   ├── admins/         admin signer sets published with SET_ADMINS
│   ├── proxy/          tee-proxy TOMLs: the local devnet one, and Coston/Coston2 examples
│   ├── coston/, coston2/   upstream contract addresses per network (tracked)
│   └── local/          the simulation's addresses (generated, git-ignored)
├── testdata/           conformance fixtures and ABI wire vectors used by the tests
├── proxy/Dockerfile    the pinned tee-proxy image
├── docker-compose.yaml            extension TEE + proxy + redis
├── docker-compose.siblings.yaml   overlay: node built from the sibling tee-node checkout
├── config/indexer/coston/          configuration of the Coston chain indexer
├── docker-compose.coston-indexer.yaml  the Coston chain indexer and its DB (runs on its own too)
└── docker-compose.coston*.yaml    overlays: chain ids and proxy configs for the testnets
```

Two things hold everywhere:

- **`.env` is the active configuration.** `use-chain.sh <chain>` copies `.env.<chain>` to `.env`;
  every script sources it. `.env.local` (tracked, Hardhat test keys only) is the simulation;
  create `.env.coston` / `.env.coston2` from `.env.example` for the testnets.
- **The started stack names its proxy.** On the simulation, `start-services.sh` records the
  proxy URL of the stack it brought up (6674 for the compose stack, 6684 for host processes)
  in `deploy/config/stack.env`; every follow-up script and Go tool reads it after `.env`, so
  the commands after the start are the same for both stacks. The recorded stack outranks
  `.env` and an `EXT_PROXY_URL` inherited from the shell; the tools' `-p` flag is the explicit
  override. `stop-services.sh` stops that stack and removes the record. On a live chain nothing is recorded and
  `EXT_PROXY_URL` in `.env` (the machine's public proxy URL) applies.

## Testing on the local simulation

Prerequisites: the e2e repository's simulation up (Hardhat on 8545, its redis instances,
the standard TEE, proxy and relay), a sibling checkout of `flare-smart-contracts-v2`, and
`forge`, `cast`, `jq`, `go`, `docker`. The full runbook, with what each step proves and a
troubleshooting table, is [docs/local-e2e.md](../docs/local-e2e.md).

```bash
./deploy/scripts/use-chain.sh local                 # .env.local → .env
#   with a private Hex token (see "Private Hex credentials" below):
#   HEX_API_KEY=<token> HEX_CASH_URL=https://… ./deploy/scripts/use-chain.sh local
./deploy/scripts/deploy-oracle-local.sh             # oracle contracts on the sim, extension 1 reserved
(cd tools && go run ./cmd/wire-extension)           # owner wiring: sender + state verifier, allowed machine owners, key types

# Start the extension TEE and its proxy, and register the machine. Two ways,
# use one or the other; the commands after them are the same:
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local   # a) in Docker: compose stack, proxy on 6674
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh            # b) as host processes: no Docker, proxy on 6684
#   b) with the proxy and node logs in this terminal afterwards (see "Attached mode" below):
#   USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh --attach

./deploy/scripts/publish-local.sh endpoints         # SET_ENDPOINTS, one transaction
./deploy/scripts/publish-local.sh admins            # SET_ADMINS, dev-only well-known keys
./deploy/scripts/upload-hex-key.sh                  # only with a private Hex token: hand the credentials to the machine
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # two minutes after the last publication or upload
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID>
./deploy/scripts/oracle-status.sh

./deploy/scripts/stop-services.sh                   # stops whichever stack was started, a) or b)
```

**a) Docker** builds the images (from the sibling tee-node and tee-proxy checkouts with
`USE_LOCAL_SIBLINGS=1`, which a simulation built from develop needs) and brings up the
compose stack, recreated on every start so no state from an old chain survives.

**b) Host processes** run the same extension the way e2e runs the standard TEE and proxy:
`tools/cmd/local-stack` starts the proxy in-process and the extension TEE as its child,
built from the module root, with redis on the simulation's fourth instance (emptied at start).
Logs and pids sit under `out/`. The proxy binds
6684 directly, a block clear of the e2e simulation's own processes; the start records it,
so the follow-up scripts find it by themselves.

Either way `<TEE_ID>` is the machine `post-build.sh` registers, printed at the end and listed
by `fleet-status`. The machine observes on a 2-minute timer and nothing re-triggers it when a
configuration lands, so give it one interval after the last publication (or upload) before
the first cycle. A cycle inside that interval is refused once, as `StaleEndpoints` or as
`extension is not provisioned` when the observation predates the admin set, and costs the
request fee.

### Options

**Private Hex credentials.** Without them the machine runs cash-less: the public reserves
endpoint needs no key, the price counts bonds only, and `sourceHealth` shows
`hex_cash: not_configured`. To use the restricted cash endpoint, give the two credentials to
the chain switch once:

```bash
HEX_API_KEY=<token> HEX_CASH_URL=https://… ./deploy/scripts/use-chain.sh local
```

`use-chain.sh` writes them into `.env`, and every later switch carries them, together with the
salt, over from the `.env` it replaces, so the file is never edited by hand; `--no-hex` drops
them. From there the normal sequence does the rest:

- `publish-local.sh endpoints` commits the `hex_cash` group to that URL instead of the config's
  placeholder, generating a random salt on the first run and recording it as `HEX_CASH_SALT`.
  The salt binds the published commitment to the URL the machine is later handed, so it must
  not change between the two; treat URL and salt as one confidential pair.
- `upload-hex-key.sh` hands both credentials to the machine: the URL as the commitment
  preimage, then the key. On the recorded local stack everything defaults (proxy, chain id,
  API key, the dev backing admin key), so it runs unattended, waits for the machine's own
  verdict on each upload and ends with `ready: true`.
- The confirmation that the cash figure is in the price is `hex_cash: ok` in the machine's
  `sourceHealth`, shown by `oracle-status.sh` after the first served cycle. The figure itself
  is never published, by design.

Credentials are memory-only: a restarted stack is a new machine and needs the publications
and the upload again. `stop-services.sh` retires the old machine on chain as it stops the
stack, so the sender stops dispatching to it; `tools/cmd/retire-tee -stale` sweeps any that
were left behind. On the simulation the token is used from your machine against Hex's
production endpoint.

**Attached mode: the stack's logs in the terminal.** The host stack starts detached, because
`post-build.sh` registers the machine against it while it runs, and restarting it in the
foreground afterwards would mint a new machine identity. To end up with the proxy and node
logs in front of you anyway:

```bash
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh --attach   # setup as usual, then follow the log
./deploy/scripts/stack-logs.sh                                       # attach to a running stack later
```

With `--attach` the setup runs exactly as without it; once `post-build.sh` is done the
terminal switches to the stack's log from the top of the run and stays there, and Ctrl-C then
stops the stack, the way ending a foreground process would. `stack-logs.sh` attaches to a
running stack at any time and tells you when the stack exits underneath it; its Ctrl-C detaches
and leaves the stack running, or stops it with `--stop`. Run the publications and cycles from
a second terminal while attached.

`deploy-oracle-local.sh` deploys upstream's contracts from the sibling checkout through
`contracts/script/DeployTeeOracle.s.sol`, leaving the sender governed by the deployer
(no production mode), so publications execute at once instead of after the simulation's
hour-long governance timelock. Nothing here touches the chain clock.

After a simulation restart, run the sequence again from `deploy-oracle-local.sh`; the
local stack is recreated on every start so no state from the old chain survives, and the
e2e relay, TEE and proxy must be restarted with the simulation too.

## Running on a live chain (Coston, Coston2, Songbird, Flare)

One sequence for every live chain. `<chain>` below is `coston`, `coston2`, `songbird` or
`flare`; each chain is a row in `scripts/lib/chains.sh` (id, RPC, normal proxy, faucet, the
indexer database's host port), and nothing chain-specific is edited by hand: the proxy
configuration is rendered per run from `config/proxy/extension_proxy.live.toml`, the indexer's
from `config/indexer/indexer-config.toml.template`, both with the chain's addresses.

What must be true before you start:

- **The extension is yours.** Either Flare governance has run `registerReserved(EXTENSION_ID,
  INITIAL_OWNER)` for your deployer's address on the chain's `FlareTeeManager`, or that address
  is on the registry's extension-owner allowlist, in which case `deploy-oracle.sh` registers a
  public id (>= 65536) and records it in `.env` and `.env.<chain>`.
- **A funded deployer key** (the chain's faucet for the testnets) and a separate proxy key;
  `openssl rand -hex 32` for `DIRECT_API_KEY`. Who holds which key is spelled out at the top
  of `.env.example`.
- **The chain's addresses are current.** `import-upstream-addresses.sh <chain>` refreshes the
  system contracts from the network's own `FlareContractRegistry` — the authority, since Flare
  redeploys contracts now and then (Coston's Relay in 2026 was found this way) — and renders
  the indexer configuration from them.
- **The chain's indexer is running and caught up.** The proxy reads signing policies from a
  C-chain indexer; `docker-compose.<chain>-indexer.yaml` runs one with its database as a stack
  of its own (no variables needed). It indexes the last seven days at start — the signing
  policies and voter registrations of the recent reward epochs — which takes some minutes on a
  fresh database; `start-services.sh` refuses to start the proxy against one that is behind.
  An indexer of your own goes in `.env` as `INDEXER_DB_HOST` / `INDEXER_DB_PORT` / … instead.
- **A public URL for the machine.** The URL is registered on chain as the machine's address, so
  it must survive restarts. Laptop: an ngrok static domain in front of the proxy's external
  port. Remote machine: its own URL (see "A remote machine" below).

```bash
cp .env.example .env.coston                          # fill in the keys and the URL (see the file)
cp deploy/config/admins/coston.example.json deploy/config/admins/coston.json   # the test admin set; your own addresses for a real deployment
./deploy/scripts/use-chain.sh coston                 # .env.coston → .env  (HEX_API_KEY=… HEX_CASH_URL=… in front for the cash source)
./deploy/scripts/import-upstream-addresses.sh coston # registry-fresh addresses + the indexer's config
docker compose -f deploy/docker-compose.coston-indexer.yaml up -d    # then wait for "Up to date" in its logs
ngrok http --url=<your-static>.ngrok-free.app 6674   # in another terminal (6684 for host processes)

./deploy/scripts/deploy-oracle.sh --chain coston     # oracle contracts + verifier, deployer-governed; extension checked or registered
(cd tools && go run ./cmd/wire-extension)            # owner wiring: sender + verifier, allowed machine owners, key types
./deploy/scripts/full-setup.sh --chain coston        # compose stack, then register the machine (availability check through the tunnel)
./deploy/scripts/full-setup-host.sh --chain coston --attach   # or as host processes on 6684, the logs staying in this terminal

./deploy/scripts/publish-local.sh admins deploy/config/admins/coston.json      # BEFORE the uploads: the admins authorise them
./deploy/scripts/publish-local.sh endpoints deploy/config/endpoints/public-only.json   # or your own endpoints file
./deploy/scripts/upload-hex-key.sh                   # with HEX_* in .env; prompts for what is missing
./deploy/scripts/upload-rpc-keys.sh --refs <ref,…>   # provider credentials, if the endpoints file names any
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # two minutes after the last publication or upload
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID>       # pays the request fee in the chain's native token
./deploy/scripts/oracle-status.sh

./deploy/scripts/stop-services.sh --chain coston     # retires the machine on chain, then stops the stack (the indexer keeps running)
```

Order matters once: publish the admins before any upload, because the machine verifies a
credential's signatures against the admin set it holds — the published set, not the file on
your disk. The machine's TEE id, if mislaid, is
`(cd tools && go run ./cmd/verify-tee -p http://localhost:6684 -print-tee-id)`, or
`retire-tee -stale -dry-run` for every machine registered to the extension.

The differences from the simulation: the sender is yours and deployer-governed, so
`publish-local.sh` publishes directly despite its name (it signs with
`DEPLOYMENT_PRIVATE_KEY`); on the testnets the machine attests with the test code hash
(`SIMULATED_TEE=true`, `MODE=1`), which their registries accept as `TEST_PLATFORM` — the
upload scripts then assume tee-node's test constant for `EXPECTED_CODE_HASH`; and the
machine's registered URL is public, which the data providers reach for the availability proof
and the keeper for results.

### Songbird and Flare

The same scripts address the mainnets, with two differences of kind:

- **No simulated machines.** Their registries accept only real attestation (`GCP_*` platforms),
  so the machine is a Confidential Space VM with `SIMULATED_TEE=false` and `MODE=0`;
  `full-setup.sh` refuses `SIMULATED_TEE=true` there. That usually means the machine is remote
  — the next section.
- **No test deployment.** `deploy-oracle.sh` refuses mainnets: its contracts stay
  deployer-governed, which is a test arrangement. The production contracts are deployed by
  `flare-smart-contracts-v2`'s `deployment/scripts/deploy-tee-oracle.sh`, switched to
  production mode, and finished by Flare governance (`registerReserved`, fee rows,
  `FtsoV2.addCustomFeeds`); `import-upstream-addresses.sh <chain>` then brings the addresses
  here. On a production-mode sender `publish-local.sh` records the governance call, waits out
  the real timelock and executes it (`--wait`, or later `--execute`).

There is no bundled indexer file for the mainnets yet; copy `docker-compose.coston-indexer.yaml`,
rename the services, give the database a fresh host port, and run
`import-upstream-addresses.sh <chain>` to render its configuration — or name your own indexer
in `.env` (`INDEXER_DB_*`). Songbird's indexer enforces a 10-day minimum history
(`INDEXER_HISTORY_DROP=864000` when rendering).

### A remote machine

When the TEE and its proxy run elsewhere — a Confidential Space VM an operator deployed from
this image ([docs/deployment-steps.md](../docs/deployment-steps.md) steps 5–8 are that
hand-off) — the only scripts that fall away are the ones that start and stop the local stack.
Everything else already addresses the machine through `EXT_PROXY_URL` and the chain through
`CHAIN_URL`:

```bash
# .env.<chain>: EXT_PROXY_URL=<the machine's public proxy URL>; no tunnel, no indexer here —
# the machine's own proxy has one where it runs. SIMULATED_TEE=false, MODE is the image's 0.
./deploy/scripts/use-chain.sh <chain>
./deploy/scripts/import-upstream-addresses.sh <chain>
./deploy/scripts/deploy-oracle.sh --chain <chain>    # testnets; mainnets: upstream's deploy, see above
(cd tools && go run ./cmd/wire-extension)
./deploy/scripts/post-build.sh                       # registers THAT machine: version, governance, register, production
# publish-local.sh, upload-*.sh, oracle-cycle.sh, oracle-status.sh — unchanged
```

Two things change in substance, not in commands: `EXPECTED_CODE_HASH` for the upload scripts
is the real measured hash, obtained out of band (`verify-tee` against the machine prints it),
never the test constant; and retirement is not tied to a stop —
`(cd tools && go run ./cmd/retire-tee -p "$EXT_PROXY_URL")` when the machine is decommissioned.
The machine's own launch environment (`EXTENSION_ID`, `CHAIN_URL`, `CHAIN_ID`,
`INITIAL_OWNER`, `GOVERNANCE_*`, `PROXY_URL`) is the VM operator's side of the hand-off.

## The scripts

| Script | Purpose |
| --- | --- |
| `use-chain.sh <chain>` | Activate `.env.<chain>` as `.env` and pin `CHAIN=<chain>` in it; `--list` shows the available ones. Hex credentials given on the call, or present in the `.env` being replaced, are written into the new one; `--no-hex` drops them. |
| `deploy-oracle.sh --chain c [--refresh]` | Deploy the oracle contracts deployer-governed and the extension's state verifier, make the extension ours (the simulation reserves it; on a testnet it checks the reservation, or registers a public id for an allowlisted key and records it in `.env` and `.env.<chain>`), import addresses. Refuses mainnets: there the production deploy is upstream's. `deploy-oracle-local.sh` is `--chain local`. |
| `import-upstream-addresses.sh <network> [upstream]` | Merge upstream's deployed addresses into `config/<network>/deployed-addresses.json`; for `local` it seeds the file from the sim dump. On a live chain it also refreshes the system contracts from the network's `FlareContractRegistry` (the authority — Flare redeploys contracts) and renders the chain's indexer configuration from them. |
| `full-setup.sh [--chain c] [--local]` | Start the stack (`start-services.sh`), then `post-build.sh`. `--local` runs the extension TEE and proxy as host processes. |
| `full-setup-host.sh [--chain c] [--attach]` | `full-setup.sh --chain c --local` (default `local`); on a live chain the host proxy's redis is the base compose file's and the tunnel forwards to 6684. `--attach` then follows the stack's log in the terminal, Ctrl-C stopping the stack. |
| `stack-logs.sh [--stop]` | Follow the host-process stack's log from the top of the current run. Ctrl-C detaches, or with `--stop` stops the stack. |
| `start-services.sh [--chain c] [--local]` | Build images if needed and bring the compose stack up (recreated every run on `local`; on a live chain `docker-compose.live.yaml` with the proxy configuration rendered for the chain), or start `tools/cmd/local-stack` as host processes. Refuses to start against a lagging chain indexer. `USE_LOCAL_SIBLINGS=1` builds node and proxy from the sibling checkouts. |
| `stop-services.sh [--chain c] [--local] [--keep-registered]` | Retire the stack's machine on chain (its identity dies with the process), then stop the stack recorded by the last start (`deploy/config/stack.env`); `--local` forces the host processes under `out/pids/`, `--chain` the compose stack, `--keep-registered` skips the retirement. Removes the record; the chain's indexer stack is left running. |
| `tools/cmd/retire-tee` | Pause a machine in the registry: `-p <proxy>` the one behind a proxy, `-tee <id>` by id, `-stale` every active machine whose URL does not answer as itself (the leftovers of stopped stacks); `-dry-run` previews. |
| `post-build.sh` | Allow the TEE version, set the extension's TEE governance, register the machine and take it to production. Re-runnable: a machine already in production is left alone. |
| `publish-local.sh endpoints\|admins [config]` | Publish a configuration on any deployer-governed sender, the simulation's or a test deployment's: one transaction, confirmed from the machine's state. Off the simulation the admins file must be named (the default is the well-known dev keys). On a production-mode sender it records, saves the hand-off under `out/publications/`, and executes after the real timelock (`--wait`, or later `--execute <file>`). |
| `oracle-cycle.sh -tee <a,b> [--dry-run] [--loop 10m]` | One keeper cycle against the deployed stack: request a feed update, collect the results, submit. |
| `oracle-status.sh [--store 0x…]` | Read what the oracle publishes: store, feed, machine registration, extension health. |
| `upload-rpc-keys.sh`, `upload-hex-key.sh` | Provision credentials into the enclave over the direct channel, signed by the admins — the published set, so publish `SET_ADMINS` first. The Hex one reads `HEX_API_KEY`, `HEX_CASH_URL` and `HEX_CASH_SALT` from `.env`. With `SIMULATED_TEE=true` the expected code hash defaults to tee-node's test constant; a real machine's is given out of band. |

Not part of the chain flow — development, tests and pins:

| Script | Purpose |
| --- | --- |
| `oracle-observe.sh`, `set-endpoints.sh`, `set-admins.sh` | The chainless harness: a devserver and mocknode stand in for the sources and the chain, and configuration payloads are posted straight to a running dev extension. For observing prices and iterating on the extension, never for a deployment. |
| `check-versions.sh` | Fail when the tee-node, go-flare-common or tee-proxy pins drift between the two Go modules and the proxy image. |
| `generate-bindings.sh` | Regenerate the Go bindings in `tools/pkg/contracts` from the upstream interfaces compiled in `contracts/`. |
| `regen-wire-fixtures.sh` | Regenerate the ABI wire vectors under `testdata/wire` from the Foundry tests. |
| `test-unit.sh` | Unit tests of both Go modules. The wire-contract replay is `go test ./internal/extension/ -run TestConformance`. |
| `e2e.sh` | Internal: start, stop and wait on the host-process stack's background processes; the stack scripts call it. |

Helpers under `scripts/lib/`: `chains.sh` is the chain table every script resolves the target
network from (one row per chain: id, RPC, normal proxy, faucet, indexer port); `env.sh` loads
`.env` and the recorded stack; `versions.sh` exposes the dependency pins the checks compare;
`modcache-proxy.sh` serves the host's Go module cache to sibling image builds, so a slow link
does not turn a build into an hour of downloads.

## Configuration files

- `config/endpoints/*.json` — endpoint groups by name with thresholds, public URLs or
  credential references. `public-only.json` is the simulation's; `coston2.json` the testnet's.
- `config/admins/*.json` — role-tagged admin signer sets. `dev-wellknown-keys.json` holds
  the Hardhat test keys and is for the simulation only.
- `config/proxy/extension_proxy.docker.toml` — the local devnet proxy configuration; the
  scripts render it per run with the simulation's addresses and redis port.
- `config/proxy/extension_proxy.live.toml` — the template for every live chain;
  `start-services.sh` renders it into `out/proxy/` per run with the chain's id and addresses,
  this run's redis, and the indexer database. Nothing per-chain is copied or edited by hand.
- `config/indexer/indexer-config.toml.template` — the chain indexer's template;
  `import-upstream-addresses.sh <chain>` renders `config/indexer/<chain>/indexer-config.toml`
  from the chain's addresses (git-ignored, regenerated on every import), and
  `docker-compose.<chain>-indexer.yaml` mounts it.
- `config/<network>/deployed-addresses.json` — the chain's contract addresses: upstream's
  deploys merged with the network registry's system contracts. Coston and Coston2 are tracked;
  `local/` is written by `deploy-oracle-local.sh` on every run.
- `config/extension.env` — written by `full-setup.sh`, carries the extension id into the
  container.
