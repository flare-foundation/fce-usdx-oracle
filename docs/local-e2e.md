# Local end-to-end — the whole oracle on a Hardhat simulation

This runs everything: the e2e repository's Flare simulation, a standard TEE with its proxy and
relay, this extension in Docker behind its own proxy, the oracle contracts deployed on the
simulated chain, and one keeper cycle that lands a `submitFeedUpdates` on your own store. Nothing
leaves your machine except the observation itself, which reads the live Flare, Ethereum, Songbird
and Hex sources.

Five repositories take part, checked out as siblings:

```
git/
  tee/e2e                          the simulation and the standard TEE stack
  tee/extension-examples/fce-usdx-oracle   this repository
  tee/tee-node, tee/tee-proxy      the node and the proxy, built from source with USE_LOCAL_SIBLINGS=1
  fsp/flare-smart-contracts-v2     the contracts, on the tee-oracle-extension branch
```

Tools: Docker, Go 1.25.8 (`tools/go.mod` needs it), Foundry (`forge`, `cast`), `jq`, `python3`,
`openssl`, `curl`, and Node with Yarn for the contracts image. `contracts/` needs forge-std in its
git-ignored `lib/`, once after cloning:

```bash
(cd contracts && forge install --no-git foundry-rs/forge-std)
```

## 1. The simulation (e2e repository)

The contracts image must be built from the **current** `tee-oracle-extension` checkout: the oracle
deploy script wires into `FlareTeeManager` facets that an older image does not have.

```bash
# in fsp/flare-smart-contracts-v2
yarn install && yarn c
# from the parent directory of tee/ and fsp/; the build context is the contracts checkout
docker build -f tee/e2e/docker/Dockerfile -t local/flare-smart-contracts-v2 fsp/flare-smart-contracts-v2
# in tee/e2e/docker
docker compose up -d
```

Then, from `tee/e2e`, the standard TEE stack in this order (governance before the node):

```bash
go run ./cmd/setup/teegovernance
go run ./cmd/tee
PRIVATE_KEY=0x983760a4ebf75b2ac3a93531168a0f225d01e5dc6e3568adbd46233ba1fb4fa4 go run cmd/proxy/proxy.go
go run ./cmd/relay
go run ./cmd/setup -l
```

The relay is what delivers cosigned instructions to *this* extension's proxy too, so it must stay up.

## 2. This repository

```bash
./deploy/scripts/use-chain.sh local            # .env.local → .env (Hardhat test keys only)
./deploy/scripts/deploy-oracle.sh --chain local        # oracle contracts on the sim, extension 1 reserved, addresses imported
(cd tools && go run ./cmd/wire-extension)      # owner wiring: instructions sender, and the machines' owner on the allowlist
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup.sh --chain local   # extension + proxy in Docker, version, governance, machine registered
./deploy/scripts/publish-local.sh endpoints    # SET_ENDPOINTS, executed at once: the local sender is deployer-governed
./deploy/scripts/publish-local.sh admins       # SET_ADMINS, dev-only well-known keys
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # two minutes after the publications; requests and pays, submits nothing
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID>
./deploy/scripts/oracle-status.sh
./deploy/scripts/stop-services.sh              # stops whichever stack was started
```

`USE_LOCAL_SIBLINGS=1` builds the node and the proxy from the sibling `tee-node` and `tee-proxy`
checkouts, which a simulation built from develop needs; without it the stack runs the versions
`go.mod` and `deploy/docker-compose.yaml` pin. `publish-local.sh` prints the machines the sender
will dispatch to; that is the `<TEE_ID>`. Credentials are not needed for a first price: the
observation runs cash-less and publishes at par.

### Without Docker: extension and proxy as host processes

The same flow runs with the extension TEE and its proxy as plain processes, the way the e2e
repository runs the standard TEE and proxy. In place of `full-setup.sh`:

```bash
USE_LOCAL_SIBLINGS=1 ./deploy/scripts/full-setup-host.sh   # local-stack in the background, then post-build; follow-ups as above
./deploy/scripts/publish-local.sh endpoints
./deploy/scripts/publish-local.sh admins
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run
./deploy/scripts/stop-services.sh                # when done; it stops the recorded stack
```

`full-setup-host.sh` is `full-setup.sh --chain local --local`: it starts
`tools/cmd/local-stack` under `out/pids/` (proxy in-process, extension TEE as its child; the
runner can also be run by hand with `(cd tools && go run ./cmd/local-stack)`, Ctrl-C stopping
both), waits for the proxy, records its URL in `deploy/config/stack.env` for every follow-up
script and tool, and runs `post-build.sh` against it.

`local-stack` builds `cmd/dev/start-tee` from the module root (against the sibling tee-node when
`start-services.sh` has written a `go.work`, else the pinned module), renders the local devnet
proxy config into `out/` with the simulation's addresses and chain id, and uses the simulation's
fourth redis (`:6382`, which e2e reserves for an extension proxy; `-redis` picks another). The
proxy binds 6683 and 6684 on the host directly (clear of e2e's own processes), and the start records that in
`deploy/config/stack.env`, so the follow-up commands are the same as for the compose stack.
The two stacks do not clash, and the record names whichever was started last.

## What each script does, and why

- **`deploy-oracle.sh --chain local`** — deploys upstream's oracle contracts from their sources in the sibling
  checkout through `contracts/script/DeployTeeOracle.s.sol`, which repeats upstream's own deploy
  for this network (`deployment/chain-config/scdev.json`: extension 1, the USDX/USD feed under
  custom-feed category 33, `0x21`, and its submission policy of one required signature and a
  100 BIPS spread bound) except for its last step: the sender and the feed store are not switched
  to production mode. The feed id is the one the extension signs for (`pkg/protocol`), and the
  script stops if the deployed store serves another. The contracts stay governed by the deployer,
  the key the tools sign with (`EXTENSION_OWNER_KEY`), so publications execute at once instead of
  waiting out the simulation's hour-long governance timelock. Production mode is one-way, so this
  is decided here, at deploy time, and only for the simulation. The script then reserves
  extension 1 for `INITIAL_OWNER` and imports every address into `deploy/config/local/`. It
  reuses contracts that are still on chain when they are this deployer's, for this extension and
  this feed; `--refresh` redeploys, and a new simulation (a new `AddressUpdater`) triggers a
  redeploy by itself. Two environment quirks it absorbs: upstream's Foundry dependencies come through
  Soldeer and are installed on first use, and the sim's Hardhat rejects cast's default call objects,
  so reads go through raw `eth_call` and sends carry an explicit gas limit.
- **`publish-local.sh`** — on the deployer-governed sender it is one transaction: it runs
  `send-endpoints` / `send-admins`, which execute `setEndpoints` / `setAdmins` with the sender's fee
  quote attached and then wait until the machine reports the new hash, which proves the relay
  delivered the instruction and the extension applied it. Against a production-mode sender (an
  upstream deploy on the simulation) the same script takes the governance route with
  `LOCAL_GOVERNANCE_KEY`: it records the call, saves the hand-off with its release time under
  `out/publications/`, and executes it after the real timelock, in the same run with `--wait` or
  later with `--execute <file>`. It never touches the chain clock: the proxy rejects events from
  the future, and Hardhat cannot rewind.
- **`import-upstream-addresses.sh local`** — seeds the local addresses file from the e2e sim
  dump before slotting the oracle contracts in. The file is generated and git-ignored: the sim's
  addresses change on every run.

## Keys

Everything in `.env.local` is a public Hardhat test key.

| Key | Address | Role |
| --- | --- | --- |
| `LOCAL_GOVERNANCE_KEY` (account 0) | `0xc783…1eB6` | the simulation's contract governance and sole timelock executor; reserves extension 1 (`registerReserved`), and records and executes a publication on a production-mode sender |
| `EXTENSION_OWNER_KEY` (the funded sim key) | `0xF4E0…3a64` | deploys the oracle contracts and stays their governance; owner of reserved extension 1, owner of the TEE machine (`INITIAL_OWNER`), the proxy's signer (`PROXY_PRIVATE_KEY` is the same key), the keeper's wallet (`.env.local` sets no `KEEPER_KEY`) |

`publish-local.sh` decides its route by the sender's `productionMode()`: false on a
`deploy-oracle.sh --chain local` deploy, true on upstream's, whose governance is then account 0.

## When it goes wrong

| Symptom | Cause | Fix |
| --- | --- | --- |
| `deploy-oracle.sh --chain local`: no GovernanceSettings code at the genesis address | the chain at `CHAIN_URL` is not the Flare simulation | point `CHAIN_URL` at the e2e Hardhat node |
| `registerReserved would revert` | extension 1 already owned by another address | a new simulation, or reuse that owner as `INITIAL_OWNER` |
| the oracle deploy fails inside `forge script` | contracts image older than the checkout | rebuild the image (step 1) |
| `forge script` cannot find `forge-std/Script.sol` | forge-std is not in `contracts/lib` | `(cd contracts && forge install --no-git foundry-rs/forge-std)` |
| `full-setup.sh`: network not found | the e2e stack is not up | `docker compose up -d` in `tee/e2e/docker` |
| `docker compose up`: `network <id> not found` | our containers still reference the `docker_default` the e2e stack had before it was recreated | `./deploy/scripts/stop-services.sh`, then start again; run it whenever the e2e stack is taken down and up |
| `full-setup.sh`: `USE_LOCAL_SIBLINGS set but …/tee-node is missing` | no sibling checkout | clone `tee-node` and `tee-proxy` into `tee/`, or run without `USE_LOCAL_SIBLINGS` |
| the proxy never serves `/info` | the extension container died; the proxy waits for the TEE | `docker logs deploy-extension-tee-1` and read the first error, not the retries |
| `register-tee` times out on the availability check (404 on 6662) and the node logs `policy of the given reward epoch not in the storage` | a proxy container from before a simulation restart still holds the old chain's signing-policy state | rerun `full-setup.sh --chain local`; in local mode `start-services.sh` recreates every container on each start. Restart the e2e relay, TEE and proxy with the simulation too |
| `publish-local.sh`: the extension has no active machine | registration did not finish | check `post-build.sh` output and that the standard proxy on 6662 answers |
| `publish-local.sh`: the machine still reports the old hash | relay not running, or the machine registered with an unreachable URL | start `go run ./cmd/relay` in e2e; check the URL in the registry |
| a sibling image build sits on `go mod download` for many minutes | slow link; each checkout's graph is several hundred modules | the build reads the host module cache when `go` and `python3` are on `PATH` (check the `Serving host module cache` line); populate it once with `go mod download` in the workspace |
| the proxy panics with `unknown field in toml initial_signing_policy_offset` | a proxy built from a develop checkout (`USE_LOCAL_SIBLINGS=1`): the field is gone, its backfill depth lives in `[voting] history_size` | the committed local TOMLs do not carry it; remove it from any copy of your own (the pinned `v0.0.24` proxy then uses its default of 3) |
| the relay logs `400 … voting started before the event` and the machine never reports the hash | the chain clock is ahead of wall time: something advanced Hardhat's clock (an `evm_increaseTime` timelock shortcut); the proxy allows 15 s of future and Hardhat cannot go back | restart the simulation and redeploy; nothing in this repository touches the chain clock or the timelock, so this cannot recur from here |
| `oracle-cycle.sh` reverts `NotEnoughSignatures` | the store wants more machines than answered | one machine is enough for the sim's policy (`requiredSignatures: 1`); check the record's `teeIds` |
