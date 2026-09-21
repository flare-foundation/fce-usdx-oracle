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
│   ├── coston/, coston_local/, coston2/   contract addresses per chain row (tracked)
│   └── local/          the simulation's addresses (generated, git-ignored)
├── testdata/           conformance fixtures and ABI wire vectors used by the tests
├── docker-compose.yaml            extension TEE + proxy + redis
├── docker-compose.siblings.yaml   overlay: node built from the sibling tee-node checkout
├── config/indexer/coston/          configuration of the Coston chain indexer (both Coston rows)
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

## The test suites

Start with the checks that need no chain, no TEE and no credentials. They catch most breakage in
seconds:

```bash
go test ./... && (cd tools && go test ./...) && (cd contracts && forge test)
```

| Suite | Covers |
| --- | --- |
| `go test ./...` at the root and in `tools/` | The quorum procedure, price arithmetic, decimal parsing, the provisioning verifier, the wire encodings and the keeper cycle |
| `forge test` in `contracts/` | Solidity's own encoding of the payloads that cross the extension boundary, written to `testdata/wire/` and asserted against by the Go codecs |
| `TestConformance` in `internal/extension` | Replays the golden fixtures in `testdata/conformance/` against a running extension and diffs the responses. This is the acceptance test for an implementation in another language |

[docs/testing.md](../docs/testing.md) covers what each layer proves, how to drive the feed end to
end, and how to read the outcomes that look like failures but are not.

## Testing on the local simulation

Prerequisites: the e2e repository's simulation up (Hardhat on 8545, its redis instances,
the standard TEE, proxy and relay), a sibling checkout of `flare-smart-contracts-v2`, and
`forge`, `cast`, `jq`, `go`, `docker`. The full runbook, with what each step proves and a
troubleshooting table, is [docs/local-e2e.md](../docs/local-e2e.md).

```bash
./deploy/scripts/use-chain.sh local                 # .env.local → .env
#   with a private Hex token (see "Private Hex credentials" below):
#   HEX_API_KEY=<token> HEX_CASH_URL=https://… ./deploy/scripts/use-chain.sh local
./deploy/scripts/deploy-oracle.sh --chain local             # oracle contracts on the sim, extension 1 reserved
(cd tools && go run ./cmd/wire-extension)           # owner wiring: instructions sender, and the machines' owner on the allowlist

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

`deploy-oracle.sh --chain local` deploys upstream's contracts from the sibling checkout through
`contracts/script/DeployTeeOracle.s.sol`, leaving the sender governed by the deployer
(no production mode), so publications execute at once instead of after the simulation's
hour-long governance timelock. Nothing here touches the chain clock.

After a simulation restart, run the sequence again from `deploy-oracle.sh --chain local`; the
local stack is recreated on every start so no state from the old chain survives, and the
e2e relay, TEE and proxy must be restarted with the simulation too.

## Testing on a testnet (`coston_local`, Coston2)

A deployment of our own on a testnet: the same sequence as the simulation, with the machine on
your laptop behind a tunnel and contracts this repository deploys. On Coston that is the chain
`coston_local`, which shares the chain and its indexer with `coston` and differs in the
contracts it addresses. `coston` is the official USDX deployment under the reserved extension 1,
which nobody deploys beside; it is the next section. Prerequisites: one funded key (`cast wallet new`, then
`https://faucet.flare.network/<chain>`), a static ngrok domain
(`https://dashboard.ngrok.com/domains`) with ngrok logged in, the sibling checkout of
`flare-smart-contracts-v2`, and `forge`, `cast`, `jq`, `go`, `docker`. That key and that
domain are all you provide; the chain switch derives or generates the rest.

```bash
EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://<name>.ngrok-free.app ./deploy/scripts/use-chain.sh coston_local
#   creates .env.coston_local when missing and fills in what the key implies: INITIAL_OWNER, a
#   proxy key, DIRECT_API_KEY, the provisioning keys and deploy/config/admins/coston_local.json.
#   Present values are kept, so later switches are just: ./deploy/scripts/use-chain.sh coston_local
#   with a private Hex token, as on the simulation:
#   HEX_API_KEY=<token> HEX_CASH_URL=https://… ./deploy/scripts/use-chain.sh coston_local
./deploy/scripts/import-upstream-addresses.sh coston_local   # the chain's addresses from its contract registry, and the indexer's config
docker compose -f deploy/docker-compose.coston-indexer.yaml up -d   # the chain's indexer, shared with coston; ~20 min to "Up to date" on a fresh database
ngrok http 6674 --url=https://<name>.ngrok-free.app     # in another terminal, left running (6684 for b)
./deploy/scripts/deploy-oracle.sh --chain coston_local  # oracle contracts on the testnet; extension checked or registered, its id recorded
(cd tools && go run ./cmd/wire-extension)               # owner wiring: instructions sender, and the machines' owner on the allowlist

USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain coston_local        # a) in Docker: compose stack, proxy on 6674
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh --chain coston_local   # b) as host processes: proxy on 6684 (--attach as on the simulation)

./deploy/scripts/publish-local.sh endpoints             # SET_ENDPOINTS, one transaction (hex_cash commits to HEX_CASH_URL when set)
./deploy/scripts/publish-local.sh admins                # SET_ADMINS, the admin set use-chain.sh wrote
./deploy/scripts/upload-hex-key.sh                      # only with a private Hex token
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # two minutes after the last publication or upload
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID>          # pays the request fee in CFLR
./deploy/scripts/oracle-status.sh

./deploy/scripts/stop-services.sh --chain coston_local  # retires the machine on chain, stops the stack (--local for b); the indexer keeps running
```

What differs from the simulation, and why: the chain needs its own indexer for the proxy's
signing policies, which the compose file runs and `start-services.sh` waits for; the machine's
URL is written on chain, so it is a static tunnel domain; `deploy-oracle.sh` deploys on the
testnet, and the registry mints a public id (>= 65536) for the deployer, a test identity
recorded in `.env` and `.env.coston_local`;
and the admin set is your key rather than the dev keys. The machine attests with the simulated
code hash (`SIMULATED_TEE=true`), which the testnet registries accept, so the upload scripts
assume tee-node's test constant for `EXPECTED_CODE_HASH`. `<TEE_ID>` is printed by
`full-setup.sh` and recoverable with
`(cd tools && go run ./cmd/verify-tee -p http://localhost:6674 -print-tee-id)`.

`.env.coston_local` is the only place the deployment's keys live, and a lost deployer key loses
the extension, its contracts and the funds: never copy `.env.example` over it, and keep the key
backed up. This is a test, not a deployment: the contracts stay governed by the deployer and
the machine is simulated. The deployment is the next section; `deploy-oracle.sh` refuses every
chain whose contracts are upstream's, `coston` and the mainnets among them.

## A remote machine

This is the deployment: the TEE and its proxy run on a Confidential Space VM that a system
administrator operates, and this repository is used from your workstation for the on-chain and
provisioning steps only. On Coston it is the chain `coston`, the official USDX deployment: the
contracts are upstream's, the extension is the reserved **1**, and the feed is
`0x21555344582f555344…`. It is also the only form the mainnets take, since their registries
accept no simulated machine. Split by side:

**The administrator deploys**, next to each other (the first is the enclave):

1. **The image**, built from this repository at an agreed commit with `SOURCE_DATE_EPOCH` from
   git, so you can rebuild it and know the code hash; `MODE=0` is baked in, `EXPOSE 5500` is
   the only port the VM firewall opens
   ([docs/deployment-steps.md](../docs/deployment-steps.md) step 5). Launch environment, as
   `tee-env-*` metadata (the launch-policy label allows exactly these):

   | variable | value |
   | --- | --- |
   | `EXTENSION_ID` | the extension's id (1 when governance reserved it, else the minted one) |
   | `CHAIN_ID`, `CHAIN_URL` | the chain's (`scripts/lib/chains.sh`) |
   | `INITIAL_OWNER` | your deployer's address |
   | `PROXY_URL` | `http://<proxy host>:6663` |
   | `GOVERNANCE_SIGNERS`, `GOVERNANCE_THRESHOLD` | omit for the owner alone, threshold 1 |
   | `GOVERNANCE_SAFE`, `GOVERNANCE_TEE_MANAGER` | only for Safe-backed governance (tee-node's `.env.template`); omit otherwise |

2. **The tee-proxy container**, with the configuration this repository renders
   (`start-services.sh` writes `out/proxy/extension_proxy.<chain>.docker.toml`; hand it over
   and adjust the `[db]` host to their indexer database), `PROXY_PRIVATE_KEY` (a key they
   generate, never funded) and `DIRECT_API_KEY` (the value from your `.env`, exchanged over a
   secure channel — your upload scripts authenticate with the same key).
3. **Redis** and **the chain's indexer** (`docker-compose.<chain>-indexer.yaml` with
   `config/indexer/<chain>/`, or their own indexer).
4. **Networking**: public HTTPS to the proxy's external port 6664; TEE → proxy 6663, proxy →
   redis and the indexer database private. They give you back the public proxy URL.

**You run**, the same commands as the testnet test minus the deploy, the chain switch carrying
what the remote machine changes: its URL, its proxy's `/direct` key, real attestation. Given on
the call they are written into `.env.<chain>`; the deployer key is filled only into a file that
has none. Shown for Coston; `<chain>` elsewhere.

```bash
EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://<machine> DIRECT_API_KEY=<its proxy's key> SIMULATED_TEE=false ./deploy/scripts/use-chain.sh coston
#   EXTENSION_ID=1 is the reserved id; the template already carries it
./deploy/scripts/import-upstream-addresses.sh coston   # the official sender and UsdxFeedStore, from upstream's deploys file
(cd tools && go run ./cmd/wire-extension)              # publishes the sender the addresses file names and allowlists the machines' owner
#   (MACHINE_OWNER_KEY's address, or -machineOwner). Where governance has already done both it sends
#   nothing and reports a skip for each, which is the check that the addresses file matches the chain.
EXPECTED_CODE_HASH=<measured hash> ./deploy/scripts/post-build.sh   # registers THAT machine: allows the build, governance, register, production
#   a second machine, without editing .env: post-build.sh --proxy https://<machine 2>
./deploy/scripts/publish-local.sh admins             # the chain's admin set (deploy/config/admins/<chain>.json); a machine holds
./deploy/scripts/publish-local.sh endpoints          # publications in memory, so every newly registered machine needs both again
./deploy/scripts/upload-hex-key.sh
./deploy/scripts/upload-rpc-keys.sh --refs <ref,…>
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run
```

No `deploy-oracle.sh`: the contracts are upstream's and the script refuses the chain. No
`start-services`, `full-setup` or `stop-services`: the machine is not yours to start.
`EXPECTED_CODE_HASH` is the deployed image's id (on Confidential Space the code hash is the
container image id, `docker image inspect --format '{{.Id}}'`), obtained out of band from whoever
built and deployed it, or from your own rebuild of the agreed commit. It gates the allowance
`post-build.sh` grants: without it a real machine is allowed on its own word. The uploads then
rely on the registry accepting the machine's build, and take `EXPECTED_CODE_HASH` too only when
you want them pinned to that one build. Never take it from the machine you are verifying. Retirement is on chain only:
`(cd tools && go run ./cmd/retire-tee -p "$EXT_PROXY_URL")` when the machine is decommissioned.

**On Coston** the store takes a batch only from **two** distinct production machines, carrying
the same observed timestamp and prices within 1%, so the flow above is run once per machine, the
second with `--proxy`, and the keeper is given both machine ids. The machines' `INITIAL_OWNER`
must be an address the extension owner has allowed as a machine owner, and the registry takes the
registration only from that address itself: put its key in `MACHINE_OWNER_KEY` when it is not the
deployer, which is the usual case, since the extension owner is typically not a machine owner. A machine's extension is fixed when it registers, so a
machine registered for a test extension cannot be moved to extension 1: retire it and register
it again, or use a fresh one.

**On Songbird and Flare** two more things differ. The registries accept only real attestation,
so `SIMULATED_TEE=false` and `MODE=0` are the only option, which `full-setup.sh` also enforces.
And `deploy-oracle.sh` refuses them, as it refuses `coston`: its deployer-governed contracts are
a test arrangement. The
production contracts are deployed by `flare-smart-contracts-v2`'s
`deployment/scripts/deploy-tee-oracle.sh`, switched to production mode and finished by Flare
governance (`registerReserved`, fee rows, `FtsoV2.addCustomFeeds`); `import-upstream-addresses.sh
<chain>` brings the addresses here. On a production-mode sender `publish-local.sh` records the
governance call, waits out the real timelock and executes it (`--wait`, or later `--execute`).
There is no bundled indexer file for the mainnets: copy `docker-compose.coston-indexer.yaml`,
rename the services and the database's host port, and render its configuration with
`INDEXER_HISTORY_DROP=864000 ./deploy/scripts/import-upstream-addresses.sh <chain>` (Songbird's
indexer enforces a ten-day minimum), or name an indexer of your own in `.env` (`INDEXER_DB_*`).

## The scripts

| Script | Purpose |
| --- | --- |
| `use-chain.sh <chain>` | Activate `.env.<chain>` as `.env`. On a live chain it creates the file from the template when missing and fills in what `EXTENSION_OWNER_KEY` (given on the call or in the file) implies: owner, proxy key, `DIRECT_API_KEY`, provisioning keys, the chain's admin set; present values are kept. Hex credentials given on the call, or present in the `.env` being replaced, are carried; `--no-hex` drops them. |
| `deploy-oracle.sh --chain c [--refresh]` | Deploy the oracle contracts deployer-governed, make the extension ours (the simulation reserves it; on a testnet it checks the reservation, or registers a public id for an allowlisted key and records it in `.env` and `.env.<chain>`), import addresses. Refuses every chain whose contracts are upstream's — `coston` and the mainnets — where the deploy is upstream's own. |
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
| `oracle-watch.sh [--interval 2m] [--records file] [--once]` | Follow the machine's own account of itself: every interval read the state in `/info` and the signed readiness report over `/direct`, append one JSON sample, print each change in a source's health, the last error class, the ready and would-sign flags. Reads only; no fee. |
| `oracle-status.sh [--store 0x…]` | Read what the oracle publishes: store, feed, machine registration, extension health. |
| `upload-rpc-keys.sh`, `upload-hex-key.sh` | Provision credentials into the enclave over the direct channel, signed by the admins — the published set, so publish `SET_ADMINS` first. The Hex one reads `HEX_API_KEY`, `HEX_CASH_URL` and `HEX_CASH_SALT` from `.env`. With `SIMULATED_TEE=true` the expected code hash defaults to tee-node's test constant; a real machine's is given out of band. |

Not part of the chain flow — development, tests and pins:

| Script | Purpose |
| --- | --- |
| `oracle-observe.sh`, `set-config.sh endpoints\|admins [config] [port]` | The chainless harness: a devserver and mocknode stand in for the sources and the chain, and configuration payloads are posted straight to a running dev extension. For observing prices and iterating on the extension, never for a deployment. |
| `check-versions.sh` | Fail when the tee-node, go-flare-common or tee-proxy pins drift between the two Go modules and the proxy image. |
| `generate-bindings.sh` | Regenerate the Go bindings in `tools/pkg/contracts` from the upstream interfaces compiled in `contracts/`. |
| `regen-wire-fixtures.sh` | Regenerate the ABI wire vectors under `testdata/wire` from the Foundry tests. |
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
  `local/` is written by `deploy-oracle.sh --chain local` on every run.
- `config/extension.env` — written by `full-setup.sh`, carries the extension id into the
  container.
