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
  override. `stop-services.sh` stops that stack and removes the record. On the testnets nothing is recorded and
  `EXT_PROXY_URL` in `.env` (the public proxy URL) applies.

## Testing on the local simulation

Prerequisites: the e2e repository's simulation up (Hardhat on 8545, its redis instances,
the standard TEE, proxy and relay), a sibling checkout of `flare-smart-contracts-v2`, and
`forge`, `cast`, `jq`, `go`, `docker`. The full runbook, with what each step proves and a
troubleshooting table, is [docs/local-e2e.md](../docs/local-e2e.md).

```bash
./deploy/scripts/use-chain.sh local                 # .env.local → .env
./deploy/scripts/deploy-oracle-local.sh             # oracle contracts on the sim, extension 1 reserved
(cd tools && go run ./cmd/wire-extension)           # owner wiring: sender, allowed machine owners, key types

# Start the extension TEE and its proxy, and register the machine. Two ways,
# use one or the other; the commands after them are the same:
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local   # a) in Docker: compose stack, proxy on 6674
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh            # b) as host processes: no Docker, proxy on 6684

./deploy/scripts/publish-local.sh endpoints         # SET_ENDPOINTS, one transaction
./deploy/scripts/publish-local.sh admins            # SET_ADMINS, dev-only well-known keys
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # wait two minutes after the publications first
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
configuration lands, so give it one interval between the publications and the first cycle, or
the dry run reports `StaleEndpoints` once.

`deploy-oracle-local.sh` deploys upstream's contracts from the sibling checkout through
`contracts/script/DeployTeeOracleLocal.s.sol`, leaving the sender governed by the deployer
(no production mode), so publications execute at once instead of after the simulation's
hour-long governance timelock. Nothing here touches the chain clock.

After a simulation restart, run the sequence again from `deploy-oracle-local.sh`; the
local stack is recreated on every start so no state from the old chain survives, and the
e2e relay, TEE and proxy must be restarted with the simulation too.

## Deploying to Coston / Coston2

Prerequisites, keys, the proxy configuration and the tunnel are covered step by step in
[docs/deployment-steps.md](../docs/deployment-steps.md) and, for a real machine,
[docs/usdx-oracle-production-setup.md](../docs/usdx-oracle-production-setup.md). In outline:

```bash
./deploy/scripts/use-chain.sh coston2               # .env.coston2 → .env
./deploy/scripts/import-upstream-addresses.sh coston2 [path/to/flare-smart-contracts-v2]
./deploy/scripts/full-setup.sh --chain coston2      # start the stack, allow the version, set governance, register
(cd tools && go run ./cmd/send-endpoints -config ../deploy/config/endpoints/coston2.json -out /tmp/endpoints.json -claimBack <EXECUTOR>)
(cd tools && go run ./cmd/send-admins   -config <ADMINS_JSON> -out /tmp/admins.json -claimBack <EXECUTOR>)
./deploy/scripts/upload-rpc-keys.sh                 # provider credentials over the direct channel
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --loop 10m
```

On the testnets the sender is in production mode: `send-endpoints -out` / `send-admins -out`
produce the calldata and fee for the Flare governance executor, who records the call and
executes it after the timelock. The stack keeps its containers across restarts there; the
machine identity is the registered one.

## The scripts

| Script | Purpose |
| --- | --- |
| `use-chain.sh <chain>` | Activate `.env.<chain>` as `.env`; `--list` shows the available ones. |
| `deploy-oracle-local.sh [--refresh]` | Simulation only: deploy the oracle contracts deployer-governed, reserve extension 1, import addresses. |
| `import-upstream-addresses.sh <network> [upstream]` | Merge upstream's deployed addresses into `config/<network>/deployed-addresses.json`; for `local` it seeds the file from the sim dump. |
| `full-setup.sh [--chain c] [--local]` | Start the stack (`start-services.sh`), then `post-build.sh`. `--local` runs the extension TEE and proxy as host processes. |
| `full-setup-host.sh` | `full-setup.sh --chain local --local`. |
| `start-services.sh [--chain c] [--local]` | Build images if needed and bring the compose stack up (recreated every run on `local`), or start `tools/cmd/local-stack` as host processes. `USE_LOCAL_SIBLINGS=1` builds node and proxy from the sibling checkouts. |
| `stop-services.sh [--chain c] [--local]` | Stop the stack recorded by the last start (`deploy/config/stack.env`); `--local` forces the host processes under `out/pids/`, `--chain` the compose stack. Removes the record. |
| `post-build.sh` | Allow the TEE version, set the extension's TEE governance, register the machine and take it to production. Re-runnable: a machine already in production is left alone. |
| `publish-local.sh endpoints\|admins [config]` | Simulation only: publish a configuration. One transaction on a deployer-governed sender; on a production-mode sender it records, saves the hand-off under `out/publications/`, and executes after the real timelock (`--wait`, or later `--execute <file>`). |
| `oracle-cycle.sh -tee <a,b> [--dry-run] [--loop 10m]` | One keeper cycle against the deployed stack: request a feed update, collect the results, submit. |
| `oracle-status.sh [--store 0x…]` | Read what the oracle publishes: store, feed, machine registration, extension health. |
| `oracle-observe.sh` | Local observation harness against a devserver and mocknode, no chain. |
| `set-endpoints.sh`, `set-admins.sh` | Post a configuration payload straight to a running dev extension, no chain. |
| `upload-rpc-keys.sh`, `upload-hex-key.sh` | Provision credentials into the enclave over the direct channel, signed by the admins. |
| `check-versions.sh` | Fail when the tee-node, go-flare-common or tee-proxy pins drift between the two Go modules and the proxy image. |
| `generate-bindings.sh` | Regenerate the Go bindings in `tools/pkg/contracts` from the upstream interfaces compiled in `contracts/`. |
| `regen-wire-fixtures.sh` | Regenerate the ABI wire vectors under `testdata/wire` from the Foundry tests. |
| `test-unit.sh` | Unit tests of both Go modules. The wire-contract replay is `go test ./internal/extension/ -run TestConformance`. |
| `e2e.sh` | Start, stop and wait on the host-process stack's background processes. |

Helpers under `scripts/lib/`: `versions.sh` exposes the dependency pins the checks compare;
`modcache-proxy.sh` serves the host's Go module cache to sibling image builds, so a slow link
does not turn a build into an hour of downloads.

## Configuration files

- `config/endpoints/*.json` — endpoint groups by name with thresholds, public URLs or
  credential references. `public-only.json` is the simulation's; `coston2.json` the testnet's.
- `config/admins/*.json` — role-tagged admin signer sets. `dev-wellknown-keys.json` holds
  the Hardhat test keys and is for the simulation only.
- `config/proxy/extension_proxy.docker.toml` — the local devnet proxy configuration; the
  scripts render it per run with the simulation's addresses and redis port. The
  `*.coston*.toml.example` files are the templates for the testnets; the real ones are
  git-ignored because they carry database credentials.
- `config/<network>/deployed-addresses.json` — upstream's contract addresses. Coston and
  Coston2 are tracked; `local/` is written by `deploy-oracle-local.sh` on every run.
- `config/extension.env` — written by `full-setup.sh`, carries the extension id into the
  container.
