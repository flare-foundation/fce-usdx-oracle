# deploy/ — running and deploying the USDX oracle extension

Everything an operator runs lives here: the compose stack, the proxy configuration,
the operator inputs (endpoint groups, admin signer sets), and the scripts that take an
extension from a fresh checkout to a machine publishing prices. The Go tooling the
scripts call lives in `tools/`; the extension itself is the module at the repository
root.

```
deploy/
├── scripts/            the operator scripts, listed below
├── scripts/lib/        helpers sourced by the scripts (chain table, .env loading, version pins, module-cache proxy)
├── config/
│   ├── endpoints/      endpoint-group configurations published with SET_ENDPOINTS
│   ├── admins/         admin signer sets published with SET_ADMINS
│   ├── proxy/          tee-proxy TOMLs: the simulation's, and the template every live chain's is rendered from
│   ├── indexer/        the chain indexer's template and database init; <chain>/ is rendered (git-ignored)
│   ├── coston/, coston_local/, coston2/, songbird/   contract addresses per chain row (tracked)
│   └── local/          the simulation's addresses (generated, git-ignored)
├── envelopes/          credential envelopes waiting for admin signatures (git-ignored)
├── testdata/           conformance fixtures and ABI wire vectors used by the tests
├── docker-compose.yaml                  extension TEE + proxy + redis
├── docker-compose.siblings.yaml         overlay: node built from the sibling tee-node checkout
├── docker-compose.live.yaml             overlay for every live chain: chain id, keys, the indexer's network
├── docker-compose.coston-indexer.yaml   the Coston chain indexer and its database (both Coston rows)
└── docker-compose.coston2-indexer.yaml  the Coston2 chain indexer and its database
```

Two things hold everywhere:

- **`.env` is the active configuration.** `use-chain.sh <chain>` copies `.env.<chain>` to `.env`;
  every script sources it, and the Go tools load it themselves. `.env.local` (tracked, Hardhat
  test keys only) is the simulation. For a live chain `use-chain.sh` creates `.env.<chain>` from
  `.env.example` when it is missing.
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
GOWORK=off go test ./... && (cd tools && GOWORK=off go test ./...) && (cd contracts && forge test)
```

| Suite | Covers |
| --- | --- |
| `go test ./...` at the root and in `tools/` | The quorum procedure, price arithmetic, decimal parsing, the provisioning verifier, the wire encodings and the keeper cycle |
| `forge test` in `contracts/` | The vector tests: Solidity's own encoding of the payloads that cross the extension boundary. `regen-wire-fixtures.sh` writes their output to `testdata/wire/`, which the Go codecs are asserted against |
| `TestConformance` in `internal/extension` | Replays the 13 golden fixtures in `testdata/conformance/` against the extension in process (`NewExtension` and its HTTP handler) and compares the responses. A Go test of this implementation's wire behaviour; it does not drive another implementation |

What each one needs:

- **`GOWORK=off`**, because `start-services.sh` writes a git-ignored `go.work` for
  `USE_LOCAL_SIBLINGS=1`, and with it the suites build against the sibling tee-node rather than
  the version `go.mod` pins.
- **No `EXTENSION_ID` in the environment.** An exported one (from sourcing a `.env`, for example)
  fails `TestConformance` on fixtures 01 and 02, whose expected bytes carry extension 1.
- **For `forge test`**: forge-std in the git-ignored `contracts/lib`
  (`(cd contracts && forge install --no-git foundry-rs/forge-std)`), and the sibling checkout of
  `flare-smart-contracts-v2` on its `tee-oracle-extension` branch at
  `../../../fsp/flare-smart-contracts-v2`, with its Soldeer dependencies installed
  (`forge soldeer install` there; `deploy-oracle.sh` installs them when missing). CI runs the Go
  suites only.

[docs/testing.md](../docs/testing.md) covers what each layer proves, how to drive the feed end to
end, and how to read the outcomes that look like failures but are not.

## Testing on the local simulation

Prerequisites: the e2e repository's simulation up (Hardhat on 8545, its redis instances and
indexer database, and the standard TEE, proxy and relay); sibling checkouts of
`flare-smart-contracts-v2`, and of `tee-node` and `tee-proxy` for `USE_LOCAL_SIBLINGS=1`;
forge-std in `contracts/lib` (above); and `forge`, `cast`, `jq`, `go` (1.25.8, which `tools/go.mod`
needs), `docker`, `python3`, `openssl` and `curl`. The full runbook, with what each step proves
and a troubleshooting table, is [docs/local-e2e.md](../docs/local-e2e.md).

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
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run   # two minutes after the last publication or upload; pays the request, submits nothing
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

Either way `<TEE_ID>` is the machine `post-build.sh` registers: `register-tee` prints it
(`Registered TEE node with id …`), `publish-local.sh` lists it among the machines the sender
dispatches to, and `fleet-status` shows it. The machine observes on a 2-minute timer and nothing
re-triggers it when a configuration lands, so give it one interval after the last publication
(or upload) before the first cycle. A cycle inside that interval is refused once, as
`StaleEndpoints` or as `extension is not provisioned` when the observation predates the admin
set, and costs the request fee. So does `--dry-run`: it requests and collects for real, rehearses
the submission against the store, and stops before submitting.

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
  API key, the dev backing admin key, the backing threshold of 1), so it runs unattended, waits
  for the machine's own verdict on each upload and ends with `ready: true`.
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
`contracts/script/DeployTeeOracle.s.sol`, which takes upstream's simulation configuration
(`scdev.json`: the USDX/USD feed under category 33, one required signature, a 100 BIPS spread
bound) and leaves the sender governed by the deployer (no production mode), so publications
execute at once instead of after the simulation's hour-long governance timelock. Nothing here
touches the chain clock.

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
`flare-smart-contracts-v2`, and the tools the simulation needs. That key and that
domain are all you provide; the chain switch derives or generates the rest.

```bash
EXTENSION_OWNER_KEY=0x… EXT_PROXY_URL=https://<name>.ngrok-free.app ./deploy/scripts/use-chain.sh coston_local
#   creates .env.coston_local when missing and fills in what the key implies: INITIAL_OWNER, a
#   proxy key, DIRECT_API_KEY, the provisioning keys and deploy/config/admins/coston_local.json.
#   Present values are kept, so later switches are just: ./deploy/scripts/use-chain.sh coston_local
#   with a private Hex token, as on the simulation:
#   HEX_API_KEY=<token> HEX_CASH_URL=https://… ./deploy/scripts/use-chain.sh coston_local
./deploy/scripts/import-upstream-addresses.sh coston_local   # the system contracts from the chain's registry, and the indexer's config
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
signing policies, which the indexer compose file runs; `start-services.sh` reads its lag once
at start and refuses a database more than a minute behind the chain (one that is not running
yet only draws a warning, and the proxy then waits for it). The compose stack joins the
indexer's network, `fce-usdx-oracle-coston` for both Coston rows, so the proxy reaches
`coston-indexer-db` by name. The machine's URL is written on chain, so it is a static tunnel
domain. `deploy-oracle.sh` deploys on the testnet, and the registry mints a public id
(>= 65536) when the deployer is on its extension-owner allowlist; the id is recorded in `.env`
and `.env.coston_local` (without the allowlist, the deployer needs an id Flare governance
reserved for it). `import-upstream-addresses.sh coston_local` leaves the oracle entries in
upstream's `coston.json` out, since those are the `coston` row's; this deployment's arrive from
`deploy-oracle.sh`. The admin set is your key rather than the dev keys. The machine attests with
the simulated code hash (`SIMULATED_TEE=true`), which the testnet registries accept, so the
upload scripts assume tee-node's test constant for `EXPECTED_CODE_HASH`. `<TEE_ID>` is printed by
`full-setup.sh` and recoverable with
`(cd tools && go run ./cmd/verify-tee -p http://localhost:6674 -printTeeId)` (6684 for b).

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
`0x21555344582f555344…`. Songbird is the same arrangement on a mainnet, and is live; Flare will be
once its contracts exist. A mainnet registry accepts no simulated machine, so this is the only
form a machine there takes. [docs/deployment-steps.md](../docs/deployment-steps.md) is the full
recipe, step by step; split by side, it comes to this.

**The administrator deploys**, next to each other (the first is the enclave):

1. **The image**, a tag pipeline's or `build-image.sh`'s (deployment-steps,
   [step 5](../docs/deployment-steps.md#5-build-the-image)), launched with the `tee-env-*`
   values of [step 6](../docs/deployment-steps.md#6-hand-off-the-machine). `EXTENSION_ID` and
   `CHAIN_ID` are mandatory; `GOVERNANCE_SIGNERS` and `GOVERNANCE_THRESHOLD` must be the set
   `post-build.sh` registers, or registration reverts `InvalidGovernanceHash`. `MODE=0` is baked
   in and outside the launch policy; `EXPOSE 5500` is the only port the VM firewall opens.
2. **The tee-proxy container**: tee-proxy's published image at the tag
   `docker-compose.yaml` pins, with the configuration rendered for the chain from
   `config/proxy/extension_proxy.live.toml` (no script renders it without starting a stack, so
   step 6 gives the commands), `PROXY_PRIVATE_KEY` (a key they generate, never funded) and
   `DIRECT_API_KEY` (the value in your `.env.<chain>`, exchanged over a secure channel; your
   upload scripts authenticate with the same key).
3. **Redis** and **the chain's indexer**: on Coston and Coston2
   `docker-compose.<chain>-indexer.yaml` with `config/indexer/<chain>/`, rendered by
   `import-upstream-addresses.sh <chain>`; on Songbird and Flare, where nothing is bundled, the
   template rendered by hand with at least ten days of history (step 6), or an indexer of their own.
4. **Networking**: public HTTPS to the proxy's external port 6664; TEE → proxy 6663, proxy →
   redis and the indexer database private. They give you back the public proxy URL.

**You run**, the same commands as the testnet test minus the deploy, the chain switch carrying
what the remote machine changes: its URL, its proxy's `/direct` key, real attestation. Given on
the call they are written into `.env.<chain>`; the deployer key is filled only into a file that
has none.

```bash
EXTENSION_OWNER_KEY=0x… MACHINE_OWNER_KEY=0x… EXT_PROXY_URL=https://<machine> DIRECT_API_KEY=<its proxy's key> SIMULATED_TEE=false \
    ./deploy/scripts/use-chain.sh <chain>
#   EXTENSION_ID=1 is the reserved id; the template already carries it. Then add EXPECTED_CODE_HASH
#   (the image's code hash) and GOVERNANCE_SIGNERS / GOVERNANCE_THRESHOLD (the values the VM was
#   launched with) to .env.<chain>, and switch again: ./deploy/scripts/use-chain.sh <chain>
./deploy/scripts/import-upstream-addresses.sh <chain>   # system contracts from the registry; oracle contracts from upstream's deploys file
(cd tools && go run ./cmd/wire-extension)              # publishes the sender the addresses file names and allowlists the machines' owner
#   (MACHINE_OWNER_KEY's address, or -machineOwner). Where that is done already it sends
#   nothing and reports a skip for each, which is the check that the addresses file matches the chain.
./deploy/scripts/post-build.sh                         # registers THAT machine: allows the build, governance, register, production
./deploy/scripts/post-build.sh --proxy https://<machine 2>   # each further machine, without editing .env
#   publish the endpoints and the admin sets (below), once every machine is registered
./deploy/scripts/upload-hex-key.sh                     # per machine; PROXY_URL=https://<machine 2> for the next
./deploy/scripts/upload-rpc-keys.sh
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID_1>,<TEE_ID_2> --dry-run
```

No `deploy-oracle.sh`: the contracts are upstream's and the script refuses the chain. No
`start-services`, `full-setup` or `stop-services`: the machine is not yours to start.
`EXPECTED_CODE_HASH` is the deployed image's code hash, its config digest (what Confidential
Space reports as the image id): `EXPECTED_CODE_HASH` in a tag pipeline's `record-images` output
or in `out/images/<tag>.env` from `build-image.sh`, or your own rebuild of the agreed commit
([REPRODUCIBILITY.md](../REPRODUCIBILITY.md)). It gates the allowance `post-build.sh` grants:
without it a real machine is allowed on its own word. The uploads then rely on the registry
accepting the machine's build, and take `EXPECTED_CODE_HASH` too only when you want them pinned
to that one build. Never take it from the machine you are verifying. Retirement is on chain only:
`(cd tools && go run ./cmd/retire-tee -p "$EXT_PROXY_URL")` when the machine is decommissioned.

**Publishing.** `setEndpoints` and `setAdmins` belong to the sender's governance, and each is
one publication for the feed, dispatched to every active machine of the extension when it
executes, so register every machine first. Which governance that is depends on the sender:

```bash
SENDER=$(jq -r '.[] | select(.name == "TeeOracleInstructionsSender") | .address' deploy/config/<chain>/deployed-addresses.json)
cast call "$SENDER" 'productionMode()(bool)' --rpc-url <the chain's RPC>
cast call "$SENDER" 'governance()(address)' --rpc-url <the chain's RPC>
```

- **In production mode** (upstream's deploy ends with the switch) both are Flare governance's
  timelocked calls. Build the hand-off and give the files, as written, to the governance
  executor, who records the call, waits out the timelock and executes it with the publication
  fee attached:

  ```bash
  mkdir -p out/publications && cd tools
  go run ./cmd/send-endpoints -config ../deploy/config/endpoints/<chain>.json -claimBack <executor wallet> -out ../out/publications/endpoints.json
  go run ./cmd/send-admins    -config ../deploy/config/admins/<chain>.json    -claimBack <executor wallet> -out ../out/publications/admins.json
  ```

  Each file carries the target, the calldata, the fee quoted for the active set at that moment
  (`valueWei`; the executor reads `getEndpointsPublicationFee` / `getAdminsPublicationFee` again
  in the block it executes in) and the predicted `payloadHash`, which the sender's
  `latestEndpointsHash` / `latestAdminsHash` for the feed must equal afterwards.
  `publish-local.sh` does not take this route on a live chain: it records and executes only with
  the governance key in hand, which here is Flare's.
- **Before production mode** the sender's `governance()` executes them directly:
  `publish-local.sh endpoints deploy/config/endpoints/<chain>.json` and `publish-local.sh admins`
  publish in one transaction each when `EXTENSION_OWNER_KEY` is that key. Name the endpoints file;
  the default is the simulation's `public-only.json`.

`fleet-status` then shows every machine at the published configuration. A machine registered
after the latest publication is not configured for the feed, and `requestFeedUpdate` naming it
reverts `TeeIdNotConfigured`, until someone calls the sender's permissionless, payable
`pushEndpoints` / `pushAdmins` for it with the payload of the `EndpointsPublished` /
`AdminsPublished` event (`getEndpointsPushTargets` / `getAdminsPushTargets` list the machines that
are behind), or a new publication reaches it. No tool here wraps the push.

**On Coston** the store takes a batch only from **two** distinct production machines
(`requiredSignatures` 2 in upstream's configuration), carrying one common observed timestamp,
with the two values bracketing the median within 1% (`maxSpreadBIPS` 100); read the live policy
with `cast call <store> 'getSubmissionPolicy()((uint8,uint16,uint64))'`. So the flow above is
run once per machine, the second with `--proxy`, and the keeper is given both machine ids. The
machines' `INITIAL_OWNER` must be an address the extension owner has allowed as a machine owner,
and the registry takes the registration only from that address itself: put its key in
`MACHINE_OWNER_KEY` when it is not the deployer, which is the usual case, since the extension
owner is typically not a machine owner. A machine's extension is fixed when it registers, so a
machine registered for a test extension cannot be moved to extension 1: retire it and register
it again, or use a fresh one.

### Songbird

The tracked files are the live deployment's:

- `config/songbird/deployed-addresses.json`: the system contracts, and the oracle's
  `TeeOracleInstructionsSender` (`0xEa3D5ee6F86C7084faac27FDcB8204B58BBdbc21`) and
  `TeeOracleFeedStore` (`0x56312b655f29e0CC1E9Fb44F9183c453F7e44640`). Upstream's
  `deployment/deploys/songbird.json` lists no oracle contracts, so
  `import-upstream-addresses.sh songbird` refreshes the system contracts from the chain's
  contract registry and keeps these two entries as they are. It renders no indexer configuration:
  there is no bundled Songbird indexer, so a stack started with `start-services.sh` needs
  `INDEXER_DB_HOST` and `INDEXER_DB_PORT` (and the `INDEXER_DB_*` credentials) in `.env`, naming
  an indexer database of your own; without them it stops with `no indexer database for songbird`.
- `config/endpoints/songbird.json`, the published endpoint configuration: Flare, Ethereum and
  Songbird supply each read at 2-of-3 providers, and `hex_cash` commits to the cash URL by
  `urlHash` at 1-of-1. The `HEX_CASH_URL` and `HEX_CASH_SALT` you upload must be the pair behind
  that hash: `(cd tools && go run ./cmd/send-secret commit)` prints the hash they make, and must
  print the file's.
- `config/admins/songbird.json`, the published admin sets: the same three admins govern the
  backing and the providers sets, 2-of-3 each.

What that changes in the flow:

- `use-chain.sh songbird` finds the admins file, so it writes no `ADMIN_KEY`/`ADMIN_KEYS` and no
  admin set; the admins hold their own keys. Because the file's first backing admin is not this
  deployer (`INITIAL_OWNER`), it prints a WARN suggesting the file be deleted or edited. On
  Songbird ignore it: the file is the published set.
- Both uploads need two of the three admins. `upload-hex-key.sh` reads the backing threshold from
  `config/admins/songbird.json` and signs with every key in `ADMIN_KEYS`; holding fewer than two,
  run it with `--build-only` and have two backing admins `send-secret sign` each envelope before
  it is submitted. `upload-rpc-keys.sh` already collects two provider signatures
  (`PROVIDER_THRESHOLD`) and hands off what it cannot finish. Name `ENDPOINTS_FILE` for `--all`;
  its default is Coston2's file.
- The store requires two signatures (`requiredSignatures` 2), so the keeper names two machines.
- Real attestation only: `SIMULATED_TEE=false`. `full-setup.sh` refuses a mainnet unless
  `SIMULATED_TEE=false` and `MODE=0`, but a mainnet machine is not started from here at all: the
  image bakes `MODE=0`, and the launch policy cannot change it.

**Flare** has no addresses file in this repository, and upstream configures no feed there and
lists no oracle contracts, so `import-upstream-addresses.sh flare` stops (`unknown network`).
The flow is Songbird's once those exist.

## The scripts

| Script | Purpose |
| --- | --- |
| `use-chain.sh <chain> [--no-hex] \| --list` | Activate `.env.<chain>` as `.env`. On a live chain it creates the file from the template when missing and fills in what `EXTENSION_OWNER_KEY` (given on the call or in the file) implies where the file has nothing: `INITIAL_OWNER`, a generated `PROXY_PRIVATE_KEY` and `DIRECT_API_KEY`, and, only for a chain without an admins file, `ADMIN_KEY`/`ADMIN_KEYS` and `config/admins/<chain>.json` (backing 1-of-1, providers 2-of-2). Given on the call, `EXT_PROXY_URL`, `DIRECT_API_KEY`, `SIMULATED_TEE` and `MACHINE_OWNER_KEY` replace the file's values. Where an admins file exists and its first backing admin is not `INITIAL_OWNER` it prints a WARN. Hex credentials given on the call, or present in the `.env` being replaced, are carried; `--no-hex` drops them. |
| `deploy-oracle.sh --chain c [--refresh]` | Deploy the oracle contracts deployer-governed, make the extension ours (the simulation reserves it; on a testnet it checks the reservation, or registers a public id for an allowlisted key and records it in `.env` and `.env.<chain>`), import addresses. Reuses a recorded deployment only when it is this deployer's, for this extension and this feed id. Refuses every chain whose contracts are upstream's — `coston` and the mainnets — where the deploy is upstream's own. |
| `import-upstream-addresses.sh <network> [upstream]` | Merge upstream's deployed oracle addresses into `config/<network>/deployed-addresses.json`; for `local` it seeds the file from the sim dump. On a live chain it also refreshes the system contracts from the network's `FlareContractRegistry` (the authority — Flare redeploys contracts) and, for a chain with a bundled indexer (Coston, both rows, and Coston2), renders the indexer configuration from them. A chain this repository deploys itself (`coston_local`, `coston2`) takes its oracle entries from `deploy-oracle.sh`, never from upstream's file; an upstream chain whose deploys file lists none (Songbird) keeps the ones it has. Nothing is written unless every check passes. |
| `full-setup.sh [--chain c] [--local]` | Record `EXTENSION_ID` in `config/extension.env`, start the stack (`start-services.sh`), then `post-build.sh`. `--local` runs the extension TEE and proxy as host processes. Refuses a mainnet unless `SIMULATED_TEE=false` and `MODE=0`. |
| `full-setup-host.sh [--chain c] [--attach]` | `full-setup.sh --chain c --local` (default `local`); on a live chain the host proxy's redis is the base compose file's and the tunnel forwards to 6684. `--attach` then follows the stack's log in the terminal, Ctrl-C stopping the stack. |
| `stack-logs.sh [--stop]` | Follow the host-process stack's log from the top of the current run. Ctrl-C detaches, or with `--stop` stops the stack. |
| `start-services.sh [--chain c] [--local]` | Build images if needed and bring the compose stack up (recreated every run on `local`; on a live chain with `docker-compose.live.yaml`, the proxy configuration rendered for the chain into `out/proxy/`, and the indexer's network), or start `tools/cmd/local-stack` as host processes. Reads the bundled indexer's lag once and refuses to start when it is more than a minute behind the chain. `USE_LOCAL_SIBLINGS=1` builds node and proxy from the sibling checkouts. |
| `stop-services.sh [--chain c] [--local] [--keep-registered]` | Retire the stack's machine on chain (its identity dies with the process), then stop the stack recorded by the last start (`deploy/config/stack.env`); `--local` forces the host processes under `out/pids/`, `--chain` the compose stack, `--keep-registered` skips the retirement. Removes the record; the chain's indexer stack is left running. |
| `tools/cmd/retire-tee` | Pause a machine in the registry: `-p <proxy>` the one behind a proxy, `-tee <id>` by id, `-stale` every active machine whose URL does not answer as itself (the leftovers of stopped stacks); `-dry-run` previews. |
| `post-build.sh [--proxy url]` | Allow the TEE version (`EXPECTED_CODE_HASH`), set the extension's TEE governance (`GOVERNANCE_SIGNERS`/`GOVERNANCE_THRESHOLD`), register the machine and take it to production. Re-runnable: a machine already in production is left alone. `--proxy` addresses another machine of the fleet. |
| `publish-local.sh endpoints\|admins [config] [--wait \| --execute file]` | Publish a configuration through a sender whose governance key the tools hold. On a sender not in production mode (a `deploy-oracle.sh` deployment, or an upstream one before the switch) it is one transaction from `EXTENSION_OWNER_KEY`, confirmed from the machine's readiness report. On a production-mode sender whose governance key is at hand (the simulation's `LOCAL_GOVERNANCE_KEY`) it records the call, saves the hand-off under `out/publications/`, and executes after the timelock (`--wait`, or later `--execute <file>`); where the governance is Flare's it stops, and the hand-off comes from `send-endpoints`/`send-admins -out`. The endpoints default is `public-only.json` on every chain; the admins default is `dev-wellknown-keys.json` on the simulation and `config/admins/<chain>.json` elsewhere, refused when missing. |
| `oracle-cycle.sh [-tee <a,b>] [--dry-run] [--loop 10m] [--records file]` | One keeper cycle against the deployed stack: request a feed update, collect the results, submit. Without `-tee` the targets are read from the chain every cycle — the extension's active machines at the feed's latest configuration; `-tee` fixes them. `--dry-run` pays for the request and stops before submitting. |
| `oracle-watch.sh [--interval 2m] [--records file] [--once]` | Follow the machine's own account of itself: every interval read the signed readiness report over `/direct`, append one JSON sample, print each change in a source's health, the last error class, the ready and would-sign flags. Reads only; no fee. |
| `oracle-status.sh [--store 0x…]` | Read what the oracle publishes: store, feed, machine registration, and the machine's signed readiness report. |
| `check-endpoints.sh <endpoints.json>` | Try every provider in an endpoint configuration with the credential that would be installed for it, each read from its ref's variable in capitals set in front of the command (`.env` is not loaded), and compare the chain each answers with the one its group reads. Keys reach curl on stdin and are never printed; exits non-zero on any failure. |
| `upload-hex-key.sh [--threshold n] [--build-only] [--no-verify]` | Hand `hex_cash_url` (with `HEX_CASH_URL` set) and `hex_api_key` to one machine under the backing set: verify it on chain, encrypt to its attested key, sign with every key in `ADMIN_KEYS` (or `ADMIN_KEY`), submit, confirm. The threshold is `--threshold`, `BACKING_THRESHOLD`, `backing.threshold` in `config/admins/<chain>.json`, else 1; holding fewer distinct keys it stops before building and points to `--build-only`, which encrypts and writes the envelopes to `deploy/envelopes/` for the admins to sign. Reads `HEX_API_KEY`, `HEX_CASH_URL` and `HEX_CASH_SALT` from `.env` or the environment. |
| `upload-rpc-keys.sh [--refs a,b \| --all] [--threshold n] [--build-only] [--no-verify]` | The same for the provider credentials under the providers set: by default every ref the machine reports missing, each credential from its ref's variable in capitals. It signs with every key in `ADMIN_KEYS`, submits what reaches `PROVIDER_THRESHOLD` (default 2) and hands off the rest. |
| `build-image.sh [--tag t] [--image repo] [--no-push]` | Build the extension TEE image for `linux/amd64` with `SOURCE_DATE_EPOCH` from git, check that it bakes `MODE=0`, keeps `MODE` out of the launch-policy label and exposes only 5500, push it, compare the registry's config digest with the built image, and write `out/images/<tag>.env` (`TEE_IMAGE`, `EXPECTED_CODE_HASH`). The default tag is the short commit, with `-dirty` for an unclean tree. Not a reproducible build ([REPRODUCIBILITY.md](../REPRODUCIBILITY.md)). |
| `build-keeper-image.sh [--tag v] [--chain c] [--no-push] [--allow-dirty] [--force]` | Build the keeper image (`keeper.Dockerfile`) with the chain's addresses file baked in (default `coston`, `KEEPER_CHAIN`), check it, push it, and write `out/images/keeper-<tag>.env`. Refuses to publish from a dirty tree without `--allow-dirty`, or over a published tag without `--force`. Nothing on chain refers to this image. |

With `SIMULATED_TEE=true` the upload scripts default the expected code hash to tee-node's test
constant; a real machine's is given out of band. Every secret they read comes from the
environment or a hidden prompt, never from argv.

Not part of the chain flow — development, tests and pins:

| Script | Purpose |
| --- | --- |
| `oracle-observe.sh`, `set-config.sh endpoints\|admins [config] [port]` | The chainless harness: a devserver and mocknode stand in for the sources and the chain, and configuration payloads are posted straight to a running dev extension. For observing prices and iterating on the extension, never for a deployment. |
| `check-versions.sh` | Fail when the tee-node, go-flare-common or tee-proxy pins drift between the two Go modules and the proxy image. |
| `generate-bindings.sh` | Regenerate the Go bindings in `tools/pkg/contracts` and `pkg/contracts` from the upstream interfaces compiled in `contracts/`. |
| `regen-wire-fixtures.sh` | Regenerate the ABI wire vectors under `testdata/wire` from the Foundry vector tests. |
| `e2e.sh` | Internal: start, stop and wait on the host-process stack's background processes; the stack scripts call it. |

Helpers under `scripts/lib/`: `chains.sh` is the chain table every script resolves the target
network from (one row per chain: id, RPC, normal proxy, upstream deploys file, faucet, kind,
indexer chain and port, and whether the oracle contracts are upstream's or our own), with the
compose network a live chain's stack runs on; `env.sh` loads `.env` and the recorded stack, reads
a machine's readiness report and edits files in place portably; `versions.sh` exposes the
dependency pins the checks compare; `modcache-proxy.sh` serves the host's Go module cache to
sibling image builds, so a slow link does not turn a build into an hour of downloads.

## Configuration files

- `config/endpoints/*.json` — endpoint groups by name with thresholds, public URLs or
  credential references. `public-only.json` is the simulation's, and `publish-local.sh`'s
  default everywhere; `coston2.json` and `coston.json` the testnets'; `songbird.json` is published
  on Songbird.
- `config/admins/*.json` — role-tagged admin signer sets. `dev-wellknown-keys.json` holds
  the Hardhat test keys and is for the simulation only; `songbird.json` is Songbird's published
  set; `coston.example.json` is a reference. The sets `use-chain.sh` writes for a test deployment
  (`coston.json`, `coston_local.json`) are git-ignored.
- `config/proxy/extension_proxy.docker.toml` — the local devnet proxy configuration; the
  scripts render it per run with the simulation's addresses and redis port.
- `config/proxy/extension_proxy.live.toml` — the template for every live chain;
  `start-services.sh` renders it into `out/proxy/extension_proxy.<chain>.<docker|host>.toml` per
  run with the chain's id and addresses, this run's redis, and the indexer database. Nothing
  per-chain is copied or edited by hand.
- `config/indexer/indexer-config.toml.template` — the chain indexer's template;
  `import-upstream-addresses.sh <chain>` renders `config/indexer/<indexer chain>/indexer-config.toml`
  from the chain's addresses for Coston (both rows) and Coston2 (git-ignored, regenerated on every
  import), and `docker-compose.<indexer chain>-indexer.yaml` mounts it.
- `config/<network>/deployed-addresses.json` — the chain's contract addresses: upstream's
  deploys merged with the network registry's system contracts. Coston, `coston_local`, Coston2
  and Songbird are tracked; `local/` is written by `deploy-oracle.sh --chain local` on every run.
- Written by the scripts and git-ignored: `config/extension.env` (`full-setup.sh`'s
  `EXTENSION_ID` and `start-services.sh`'s `REDIS_BIND`, read by the compose stack),
  `config/stack.env` (the simulation's recorded stack) and `config/register-tee.state`
  (`register-tee`'s progress, resumed with `-resume`).
