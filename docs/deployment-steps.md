# TEE Extension Deployment — Step by Step

Linear recipe to put this extension on a Confidential Space machine and register it on Coston,
Coston2 or Songbird (Flare once its contracts exist). Run the steps top to bottom. Step 9 takes
the machine to a first price; [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md)
is the full provisioning runbook from there, and
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md) covers what follows:
pausing, replacing and upgrading machines.

A machine on your laptop behind a tunnel, against contracts of your own, is the test in
[deploy/README.md](../deploy/README.md#testing-on-a-testnet-coston_local-coston2) ("Testing on a
testnet"), not this page.

## Which chain

| Chain | `FLARE_CHAIN` | Chain id | Oracle contracts | Extension id | Attestation |
| --- | --- | --- | --- | --- | --- |
| Coston | `coston` | 16 | upstream's (`flare-smart-contracts-v2`), finished by Flare governance | 1, reserved | real; the testnet registry also accepts simulated |
| Coston2 | `coston2` | 114 | a deployment of your own, `deploy-oracle.sh`; upstream configures no feed on Coston2 | the id the registry mints for you, or one Flare governance reserved | real; simulated accepted |
| Songbird | `songbird` | 19 | upstream's; the addresses are tracked in `deploy/config/songbird/` | 1, reserved | real only |
| Flare | `flare` | 14 | none yet: upstream configures no feed, its deploys file lists no oracle contracts, and there is no `deploy/config/flare/` | 1, reserved | real only |

## Prerequisites

- Docker (Linux containers) with Buildx
- Go 1.25.8 (`tools/go.mod` needs it)
- Foundry (`forge`, `cast`)
- `jq`, `python3`, `openssl`, `curl`, `xxd`
- Bash, with BSD or GNU `sed`: the scripts run on macOS, Linux, and Git Bash on Windows
- A sibling checkout of `flare-smart-contracts-v2` at `../../../fsp/flare-smart-contracts-v2`
  (`UPSTREAM_DIR`): `import-upstream-addresses.sh` reads its `deployment/deploys/<chain>.json`,
  and on Coston2 `deploy-oracle.sh` compiles the contracts from it, with forge-std in
  `contracts/lib` (`(cd contracts && forge install --no-git foundry-rs/forge-std)`)
- To build the image yourself: push access to its registry
  (`gcloud auth configure-docker europe-west1-docker.pkg.dev` for Artifact Registry)
- No indexer credentials: Coston and Coston2 have a bundled indexer
  (`deploy/docker-compose.<chain>-indexer.yaml`, its configuration rendered by
  `import-upstream-addresses.sh`); Songbird's is rendered by hand (step 6)

## 1. Get the extension repo

The default build is **self-contained**: `go.mod` pins `tee-node`, fetched at build
time, and `deploy/docker-compose.yaml` runs tee-proxy's published image at a pinned
tag. You only need this repo and the contracts checkout above — no sibling `tee-node/` or
`tee-proxy/` checkouts.

```text
<workspace>/
├── tee/extension-examples/<your-extension>/   # this repo — builds standalone
└── fsp/flare-smart-contracts-v2/              # the contracts, on the tee-oracle-extension branch
```

> **Developing `tee-node`/`tee-proxy` locally?** Place them as siblings and use
> the opt-in toggle, which builds the node + proxy from your on-disk checkouts
> instead of the pinned versions. That build is for local iteration and is not the image a
> machine runs:
>
> ```text
> <workspace>/tee/
> ├── tee-node/         # github.com/flare-foundation/tee-node
> ├── tee-proxy/        # github.com/flare-foundation/tee-proxy
> └── extension-examples/
>     └── <your-extension>/
> ```
>
> ```bash
> USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh --chain coston2
> ```

## 2. The keys

| Key | Who holds it | What it does |
| --- | --- | --- |
| `EXTENSION_OWNER_KEY` | you; funded on the chain | owns the extension (on Coston, Songbird and Flare, Flare governance's `registerReserved(1, <its address>)` names it), wires it, allows the image's code hash, registers the TEE governance set; the key every tool signs with unless it names another role's |
| `MACHINE_OWNER_KEY` | the machines' owner | the address the machines are launched with (`INITIAL_OWNER` on the VM); the registry takes a machine's registration, its move to production and its pause only from it. Needed only where it is not the extension owner |
| `PROXY_PRIVATE_KEY` | the administrator | the proxy's signing key; never funded, never leaves their side |
| `DIRECT_API_KEY` | both sides | guards the proxy's `POST /direct`, through which credentials are installed and the machine's readiness report is read |
| admin keys | the backing and provider admins | sign the credential envelopes; their addresses are the published admin sets, and the keys stay with the admins |

Generate a key and fund its address:

```bash
cast wallet new
cast wallet address --private-key 0x<private-key>
```

| Chain   | Faucet                                 |
| ------- | -------------------------------------- |
| Coston  | `https://faucet.flare.network/coston`  |
| Coston2 | `https://faucet.flare.network/coston2` |

Songbird and Flare have no faucet: fund the address with SGB or FLR.

## 3. Create `.env.<chain>`

`use-chain.sh` creates `.env.<chain>` from `.env.example` when it is missing, pins
`FLARE_CHAIN`, and copies the file to `.env`, which every script and tool loads. Give it what
this deployment decides:

```bash
EXTENSION_OWNER_KEY=0x… MACHINE_OWNER_KEY=0x… DIRECT_API_KEY=<the proxy's key> SIMULATED_TEE=false \
    ./deploy/scripts/use-chain.sh <chain>
```

- `EXTENSION_OWNER_KEY` is filled only into a file that has none, and the switch refuses a
  different one: the file is the only place the deployment's keys live. `INITIAL_OWNER` is filled
  in as its address.
- `DIRECT_API_KEY` is the one the proxy will hold. Given on the call it replaces the file's; left
  out, the switch generates one (`openssl rand -hex 32`), and that is the value to hand the
  administrator in step 6.
- `SIMULATED_TEE=false`: the machine attests for real. The template's value is `true`, the
  laptop test's.
- `EXTENSION_ID=1` is in the template, the reserved id of Coston, Songbird and Flare. On Coston2,
  `deploy-oracle.sh` records the id it registers (step 4).
- On a chain without an admins file (`deploy/config/admins/<chain>.json`) the switch also writes
  admin keys and a test admin set from your key. For a deployment, write the real set's addresses
  to that file before the first switch, or replace what it wrote; Songbird's is tracked.
- A `PROXY_PRIVATE_KEY` is generated too; the remote proxy does not use it.

Three more values go into `.env.<chain>` by hand once they are known: `EXPECTED_CODE_HASH` (step
5), and `GOVERNANCE_SIGNERS` and `GOVERNANCE_THRESHOLD`, the TEE governance set that signs the
machines' path lists, which the VM is launched with (step 6) and `post-build.sh` registers on
chain (step 8). For a single operator that is the extension owner's address and 1; on a chain
where machines already run, it is the set they were launched with. After editing the file, switch
again so `.env` carries the change:

```bash
./deploy/scripts/use-chain.sh <chain>
```

The switch prints what it filled in and the values that matter, with the secrets masked.

## 4. The oracle contracts and the extension id

| Fact | Value | Where it comes from |
| ---- | ----- | ------------------- |
| Extension id | `1` on Coston, Songbird and Flare; on Coston2 the id `deploy-oracle.sh` registers | the environment: `EXTENSION_ID` in `.env`, and in the VM's launch environment (step 6). Upstream reserves 1 (`teeOracleExtensionId` in its chain configuration); the extension has no default and refuses to start without one |
| Feed id (USDX/USD) | `0x21555344582f555344…` (category `0x21` + `USDX/USD`) | pinned in the build: `pkg/protocol` |

### Coston, Songbird, Flare: upstream's contracts

The `TeeOracleInstructionsSender` and one `TeeOracleFeedStore` per feed are deployed by
`flare-smart-contracts-v2`'s own script, which needs `<NETWORK>_RPC` and `DEPLOYER_PRIVATE_KEY`
in that repository's `.env`:

```bash
# in flare-smart-contracts-v2
./deployment/scripts/deploy-tee-oracle.sh <network>               # a dry run against a fork of the network; sends nothing
./deployment/scripts/deploy-tee-oracle.sh <network> --broadcast   # deploys sender + store(s), switches them to production mode
```

then the follow-ups, which do not share one caller: `registerReserved(extensionId, owner)` and
`FtsoV2.addCustomFeeds` by Flare governance (timelocked); `setExtensionContracts` and the
machine-owner allowlist by the extension owner (direct; `wire-extension` below); the `TEE_ORACLE`
operation-fee rows with the rest of the payment configuration. That repository's
`docs/specs/FCC/TeeOracle.md` has the full list.

Import the addresses, so the tooling finds them by name (`TeeOracleFeedStore`,
`TeeOracleInstructionsSender`):

```bash
./deploy/scripts/import-upstream-addresses.sh <chain>
```

It refreshes the system contracts (`FlareTeeManager`, `Relay`, `VoterRegistry`,
`FlareSystemsManager`, `AddressUpdater`, `GovernanceSettings`) from the network's
`FlareContractRegistry`, takes the oracle contracts from upstream's
`deployment/deploys/<chain>.json`, and on Coston renders the indexer configuration. When the
deploys file lists no oracle contracts it keeps the ones the addresses file holds; that is
Songbird's case, whose two oracle addresses are tracked here. With neither, it stops. Flare has
no addresses file here yet, so it stops there too.

Then wire the extension — the extension-owner share, idempotent, and a prerequisite for
`register-tee`'s availability check (`setExtensionContracts` publishes the instructions sender).
The tool reads the addresses file and the chain from `.env`:

```bash
(cd tools && go run ./cmd/wire-extension)
```

It publishes the sender the addresses file names and allowlists the machines' owner
(`MACHINE_OWNER_KEY`'s address, else `INITIAL_OWNER`, or `-machineOwner`); where both are done it
sends nothing and reports a skip for each.

### Coston2: a deployment of your own

Upstream configures no USDX feed on Coston2, so the contracts are this repository's test
deployment:

```bash
./deploy/scripts/import-upstream-addresses.sh coston2   # system contracts from the registry, and the Coston2 indexer's configuration
./deploy/scripts/deploy-oracle.sh --chain coston2       # the oracle contracts, deployer-governed; the extension checked or registered
(cd tools && go run ./cmd/wire-extension)
```

`import-upstream-addresses.sh coston2` leaves any oracle entries in upstream's `coston2.json`
out: they are not this deployment's. `deploy-oracle.sh` imports its own. It needs an id the
deployer owns: one Flare governance reserved for it, or, with the deployer on the registry's
extension-owner allowlist, a public id (>= 65536) the registry mints, which it records as
`EXTENSION_ID` in `.env` and `.env.coston2`. The contracts stay governed by the deployer and are
never switched to production mode, so publications are direct and need no timelock: a test
arrangement, not one to serve a live feed from.

No state verifier is registered, and none is needed. tee-node reports the extension's
`GET /state` in the attestation the registry checks; this extension answers that route with the
empty state, and with no verifier the registry accepts exactly that while refusing tee-node's
"could not read the extension" sentinel and any other state.

## 5. Build the image

The image is built from the `Dockerfile` at the repo root, which is the Go module. Its code hash
is the image's config digest: what Confidential Space reports as the image id and tee-node
registers as `codeHash`.

**A tag pipeline** is the build to deploy. A tag on `main` publishes
`europe-west1-docker.pkg.dev/flare-network-staging/containers/tee-usdx:<tag>` through GitLab's
BuildKit build with `rewrite-timestamp`; its `record-images` job exports `TEE_IMAGE` (the image
by digest) and `EXPECTED_CODE_HASH` in its `deploy.env` artifact, and the manual
`verify-reproducible` job rebuilds the image cold and fails unless the hashes agree. A tag also
builds the image on GitHub. [REPRODUCIBILITY.md](../REPRODUCIBILITY.md) describes both builds and
how an auditor rebuilds the hash; do not register a hash that failed that check.

**`build-image.sh`** builds and pushes from your checkout:

```bash
./deploy/scripts/build-image.sh                  # tag: the short commit, -dirty for an unclean tree
./deploy/scripts/build-image.sh --tag v0.1.0
./deploy/scripts/build-image.sh --no-push        # build and print the code hash only
```

It builds for `linux/amd64` with `SOURCE_DATE_EPOCH` from git, checks what ended up in the image
(below), pushes it, confirms the registry's config digest equals the built image's, and writes
`out/images/<tag>.env` with `TEE_IMAGE` and `EXPECTED_CODE_HASH`. It is not a reproducible
build: the default builder applies no `rewrite-timestamp`, so nobody else can re-derive the hash,
and the commit only identifies the tree. Use it for a test; deploy a tag pipeline's image.

Either way, put the code hash into `.env.<chain>` and switch again (step 3):

```bash
EXPECTED_CODE_HASH=0x<code hash>
```

### Attestation mode

`MODE` selects the attestation backend:

| Value | Meaning | Used for |
|---|---|---|
| `0` | Production attestation | a real Confidential Space VM — **the image default** |
| `1` | Simulated attestation (test code hash) | the local simulation and laptop tests |

**The Dockerfile ships `MODE=0`**, the same default as the tee-node image, so the image a Confidential Space VM launches attests for real without any launch-time setting. The compose stack sets `MODE=1` explicitly (`MODE=${MODE:-1}` in `deploy/docker-compose.yaml` and `docker-compose.live.yaml`), which is why the same image also runs against the simulation.

`MODE` is **not** in the `tee.launch_policy.allow_env_override` label, on purpose: the attestation backend is part of the attested identity. FTDC rejects simulated attestation, and a launch must not be able to turn it on without changing the image. Verify what ended up in the image before registering its hash on-chain. `build-image.sh` makes these checks itself; for a pipeline's image, pull it first:

```bash
docker pull --platform linux/amd64 <TEE_IMAGE>
docker image inspect <TEE_IMAGE> --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^MODE='
# MODE=0
docker image inspect <TEE_IMAGE> --format '{{index .Config.Labels "tee.launch_policy.allow_env_override"}}'
# MODE must NOT appear in this list
docker image inspect <TEE_IMAGE> --format '{{json .Config.ExposedPorts}}'
# {"5500/tcp":{}} — Confidential Space opens every exposed port in the VM firewall;
# the sign server and the extension's /action bind loopback and must not be listed
docker buildx imagetools inspect <TEE_IMAGE> --raw | jq -r .config.digest
# sha256:<the code hash without its 0x>
```

## 6. Hand off the machine

To the administrator, who runs the VM and everything beside it:

**The image** — `TEE_IMAGE`, by digest, as `tee-image-reference`. The VM's service account needs
`roles/artifactregistry.reader` on the repository.

**The launch environment**, as `tee-env-*` metadata. The launch-policy label allows `LOG_LEVEL`,
`PROXY_URL`, `INITIAL_OWNER`, `EXTENSION_ID`, `CHAIN_URL`, `CHAIN_ID`, `GOVERNANCE_SIGNERS`,
`GOVERNANCE_THRESHOLD`, `GOVERNANCE_SAFE`, `GOVERNANCE_TEE_MANAGER`, `SIGN_PORT` and
`EXTENSION_PORT`, and nothing else:

| Variable | Value |
| --- | --- |
| `EXTENSION_ID` | mandatory: `EXTENSION_ID` from your `.env` (step 4) |
| `CHAIN_ID` | mandatory: 16 Coston, 114 Coston2, 19 Songbird, 14 Flare. The node binds every signature to it, and the extension refuses to start without it |
| `INITIAL_OWNER` | the machines' owner: `MACHINE_OWNER_KEY`'s address, or the extension owner's where they are one key |
| `PROXY_URL` | `http://<proxy host>:6663`, the proxy's internal port |
| `GOVERNANCE_SIGNERS`, `GOVERNANCE_THRESHOLD` | the values in your `.env.<chain>` (step 3). Set both: without them the node signs a placeholder governance hash, and registration reverts `InvalidGovernanceHash` |
| `GOVERNANCE_SAFE`, `GOVERNANCE_TEE_MANAGER` | only for Safe-backed governance (tee-node's `.env.template`); omit otherwise |
| `LOG_LEVEL` | optional |
| `SIGN_PORT`, `EXTENSION_PORT` | optional: the two loopback ports inside the VM (default 7701, 7702), for a collision only |

`CHAIN_URL` is in the label but nothing in the image reads it; leave it out.

**The proxy** — tee-proxy's published image at the tag `deploy/docker-compose.yaml` pins
(`ghcr.io/flare-foundation/tee-proxy:v0.0.24`), with:

- its configuration, `/app/config/config.toml`, rendered for the chain from
  `deploy/config/proxy/extension_proxy.live.toml`. `start-services.sh` renders it only as part of
  starting a stack of its own, so render it directly, from the repository root, with the
  addresses file `import-upstream-addresses.sh` refreshed and the redis and the indexer database
  the administrator runs:

  ```bash
  C=<chain>; CHAIN_ID=<chain id>; REDIS=<redis host>:6379; DB_HOST=<indexer db host>; DB_PORT=3306
  A=deploy/config/$C/deployed-addresses.json
  addr() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0]' "$A"; }
  mkdir -p out/proxy
  sed -e "s#^chain_id = .*#chain_id = $CHAIN_ID#" \
      -e "s#^flare_systems_manager = .*#flare_systems_manager = \"$(addr FlareSystemsManager)\"#" \
      -e "s#^relay = .*#relay = \"$(addr Relay)\"#" \
      -e "s#^voter_registry = .*#voter_registry = \"$(addr VoterRegistry)\"#" \
      -e "s#^redis_port = .*#redis_port = \"$REDIS\"#" \
      -e "s#^host = .*#host = \"$DB_HOST\"#" \
      -e "s#^port = .*#port = $DB_PORT#" \
      deploy/config/proxy/extension_proxy.live.toml > out/proxy/extension_proxy.$C.toml
  ```

  The database name and credentials are the template's (`db`, `root`/`root`, the bundled
  indexer's); edit the `[db]` block for an indexer of their own. `[direct]` is enabled and reads
  its key from `DIRECT_API_KEY`.
- `PROXY_PRIVATE_KEY`, a key they generate and never fund
- `DIRECT_API_KEY`, the value in your `.env.<chain>`, exchanged over a secure channel; the upload
  scripts authenticate with the same key

**Redis** — a plain redis 7 with persistence off (the compose stack runs
`redis-server --save "" --appendonly no`), reachable from the proxy at the `redis_port` above.

**The chain's indexer** — the proxy reads signing policies, voter registrations and the
`FlareTeeManager`'s instructions from a C-chain indexer's database:

- Coston and Coston2: `deploy/docker-compose.<chain>-indexer.yaml`, with the configuration
  `import-upstream-addresses.sh <chain>` rendered into `deploy/config/indexer/<chain>/`
  (`docker compose -f deploy/docker-compose.<chain>-indexer.yaml up -d`; its database is
  `<chain>-indexer-db`).
- Songbird (and Flare): nothing is bundled. Render the template, from the repository root, with
  a history of at least ten days, which a mainnet indexer requires, and run it beside a MySQL the
  way the Coston file does:

  ```bash
  C=songbird; RPC=https://songbird-api.flare.network/ext/C/rpc
  A=deploy/config/$C/deployed-addresses.json
  addr() { jq -r --arg n "$1" '[.[] | select(.name == $n) | .address][0]' "$A"; }
  mkdir -p deploy/config/indexer/$C
  sed -e "s#__FLARE_SYSTEMS_MANAGER__#$(addr FlareSystemsManager)#g" \
      -e "s#__VOTER_REGISTRY__#$(addr VoterRegistry)#g" \
      -e "s#__FLARE_TEE_MANAGER__#$(addr FlareTeeManager)#g" \
      -e "s#__RELAY__#$(addr Relay)#g" \
      -e "s#__NODE_URL__#$RPC#g" \
      -e "s#__DB_HOST__#$C-indexer-db#g" \
      -e "s#__HISTORY_DROP__#864000#g" \
      deploy/config/indexer/indexer-config.toml.template > deploy/config/indexer/$C/indexer-config.toml
  sed -e "s/coston-indexer/$C-indexer/g" -e "s#config/indexer/coston/#config/indexer/$C/#" \
      -e 's/127.0.0.1:3316/127.0.0.1:3318/' -e "s/fce-usdx-oracle-coston/fce-usdx-oracle-$C/" \
      deploy/docker-compose.coston-indexer.yaml > deploy/docker-compose.$C-indexer.yaml
  docker compose -f deploy/docker-compose.$C-indexer.yaml up -d
  ```

  Or name an indexer of their own in the proxy's `[db]` block. On a fresh database the indexer
  needs its whole history before the proxy serves.

**Networking** — public HTTPS routed to the proxy's external port 6664; the TEE reaches the
proxy's internal port 6663, and the proxy reaches redis and the indexer database, on a private
network. Nothing else is exposed.

You receive back the **public proxy URL**. Write it into `.env.<chain>`; given on the call it
replaces the file's value:

```bash
EXT_PROXY_URL=https://<public proxy URL> ./deploy/scripts/use-chain.sh <chain>
```

## 7. Verify the proxy `/info`

```bash
curl -s https://<public proxy URL>/info | jq '.machineData'
```

Required values:

| Field            | Expected                                                          |
| ---------------- | ----------------------------------------------------------------- |
| `platform`       | starts with `0x4743505f414d445f534556…` (GCP_AMD_SEV)             |
| `codeHash`       | `EXPECTED_CODE_HASH` (**not** `0x194844cf…` — that is simulated)  |
| `extensionId`    | your `EXTENSION_ID`, as a 32-byte word                            |
| `initialOwner`   | the machines' owner                                               |
| `governanceHash` | the hash of your governance set (below); `0xffff…ffff` means the VM was launched without `GOVERNANCE_SIGNERS` and `GOVERNANCE_THRESHOLD` |

```bash
cast keccak "$(cast abi-encode 'f(address[],uint256)' '[<signer>,…]' <threshold>)"   # the governanceHash the set makes
```

If `extensionId`, `initialOwner` or `governanceHash` is wrong, ask the administrator to restart
the container with the corrected launch environment (no image rebuild: they are launch-policy
overrides). A restart is a new machine, with a new key.

## 8. Register the TEE machine

```bash
./deploy/scripts/post-build.sh                                # the machine EXT_PROXY_URL names
./deploy/scripts/post-build.sh --proxy https://<machine 2>    # every further machine
```

- `allow-tee-version` (the extension owner) allows the code hash for your extension — the one in
  `EXPECTED_CODE_HASH`, which the machine's report must match; without it, a real machine is
  allowed on its own word.
- `set-governance` (the extension owner) registers `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD`
  as the extension's TEE governance, skipping it when the latest set already matches.
- `register-tee` (the machines' owner: `MACHINE_OWNER_KEY`, else the extension owner key)
  pre-registers the TEE, requests attestation, runs the availability check through the chain's
  normal proxy and promotes the machine to production; a machine already in production is left
  alone.

Then confirm against the chain and keep the machine's id:

```bash
(cd tools && go run ./cmd/verify-tee -printTeeId)                          # the machine EXT_PROXY_URL names
(cd tools && go run ./cmd/verify-tee -p https://<machine 2> -printTeeId)
```

The store on Coston and Songbird takes a batch only from two distinct production machines
(`requiredSignatures` 2); read the live policy with
`cast call <TeeOracleFeedStore> 'getSubmissionPolicy()((uint8,uint16,uint64))' --rpc-url <RPC>`.
Register every machine before step 9: a publication reaches the machines active when it executes.

## 9. End-to-end test

Getting a price on-chain takes three steps — publish the endpoint configuration
and the admin set, install the credentials in every machine, then run a keeper
cycle that requests an observation and relays the signed updates into the
`TeeOracleFeedStore`. The store checks that each update commits to the feed's latest published
endpoint and admin hashes, so a machine missing either configuration cannot land an update.

### Publish

`setEndpoints` and `setAdmins` are the sender's governance's calls. Each is one publication for
the feed (`-feedId` defaults to USDX/USD), dispatched at execution to every active machine of the
extension; neither names a machine. Which governance that is depends on the sender:

```bash
SENDER=$(jq -r '.[] | select(.name == "TeeOracleInstructionsSender") | .address' deploy/config/<chain>/deployed-addresses.json)
cast call "$SENDER" 'productionMode()(bool)' --rpc-url <RPC>
cast call "$SENDER" 'governance()(address)' --rpc-url <RPC>
```

**In production mode** — upstream's deploy ends with the switch — they are Flare governance's
timelocked calls. Build the governance hand-off, with the executor's wallet as the payer of
record, and give the files to the governance executor as written:

```bash
mkdir -p out/publications && cd tools
go run ./cmd/send-endpoints -config ../deploy/config/endpoints/<chain>.json -claimBack <executor wallet> -out ../out/publications/endpoints.json
go run ./cmd/send-admins    -config ../deploy/config/admins/<chain>.json    -claimBack <executor wallet> -out ../out/publications/admins.json
```

Each file carries the target, the calldata, the fee quoted for the machines active at that moment
(`valueWei`) and the predicted `payloadHash`. The executor records the call, waits out the
timelock and runs `executeGovernanceCall` with the fee attached, read again from
`getEndpointsPublicationFee` / `getAdminsPublicationFee` in the block it executes in. Afterwards
the sender's `latestEndpointsHash` / `latestAdminsHash` for the feed equals the file's
`payloadHash`.

**Before production mode**, and on Coston2's deployer-governed sender, the sender's `governance()`
publishes directly, one transaction each, when `EXTENSION_OWNER_KEY` is that key:

```bash
./deploy/scripts/publish-local.sh endpoints deploy/config/endpoints/<chain>.json
./deploy/scripts/publish-local.sh admins                    # deploy/config/admins/<chain>.json
```

Name the endpoints file: the default is the simulation's `public-only.json`. With
`HEX_CASH_URL` in `.env`, `publish-local.sh` commits the `hex_cash` group to it instead of the
file's `urlHash`.

Either way, `(cd tools && go run ./cmd/fleet-status)` then shows each machine at the published
configuration. A machine registered after a publication is not configured for the feed, and a
request naming it reverts `TeeIdNotConfigured`, until it is brought up to date by the sender's
permissionless `pushEndpoints` / `pushAdmins` or by a new publication
([usdx-oracle-production-setup.md](usdx-oracle-production-setup.md#a-machine-that-joins-after-a-publication)).

### Install the credentials

**Two uploads, by two different parties, under two different admin sets** — the split is the
point, so whoever holds the Hex credential never needs an RPC key:

```bash
./deploy/scripts/upload-hex-key.sh      # hex_cash_url and hex_api_key, under the backing set
./deploy/scripts/upload-rpc-keys.sh     # every provider ref the machine reports missing, under the providers set
```

Both verify the machine against the chain before encrypting anything to it, read
every secret from the environment or a hidden prompt rather than argv, and
confirm afterwards from the machine's own signed readiness report. Each signs with every admin
key in `ADMIN_KEYS` and submits once the set's threshold is met: the backing threshold comes from
`deploy/config/admins/<chain>.json` (`--threshold` overrides it), the providers threshold from
`PROVIDER_THRESHOLD` (default 2). Holding fewer keys, run them with `--build-only` and have the
admins `send-secret sign` the envelopes in `deploy/envelopes/` before they are submitted;
production-setup has the hand-off. On Songbird both sets are 2-of-3.

The cash URL is accepted only against the published commitment: with `HEX_CASH_URL` and
`HEX_CASH_SALT` set, `(cd tools && go run ./cmd/send-secret commit)` must print the `hex_cash`
`urlHash` of the published endpoints file.

> **For a live feed, follow [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md)
> instead of the commands above** — it covers who holds which key, the multi-admin
> signing hand-off, and what to do on restart and rotation. [testing.md](testing.md)
> is the same flow with throwaway keys.

> **Per machine, and lost on restart.** Each machine has its own TEE key, so an
> envelope built for one cannot be replayed into another — run both scripts once
> per machine, with that machine's proxy (`PROXY_URL=https://<machine 2> ./deploy/scripts/…`).
> Credentials live in memory only, so a restarted machine needs them again.

### Run a cycle

Run one keeper cycle — it requests a feed update from the named machines, polls each for its
signed result, and submits the batch to the store:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE1>,<TEE2> --dry-run   # requests and collects (the fee is paid), rehearses, submits nothing
./deploy/scripts/oracle-cycle.sh -tee <TEE1>,<TEE2>
```

`--loop 10m` keeps cycling on a timer. The full walkthrough, including what to check at each hop
and what each revert means, is in [testing.md](testing.md) under "Running the USDX
oracle end to end yourself".

---

## When the extension image changes

A new image is a new code hash, and a new VM is a new machine, with a new key and teeId, which
the old machine's registration and credentials do not carry over to.
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md) ("Upgrading the code") is
the full procedure; in outline, per machine:

1. Build and publish the new image (step 5), and put its `EXPECTED_CODE_HASH` in `.env.<chain>`.
2. Have it launched on a new VM with the same launch environment (step 6), and check its `/info`
   (step 7).
3. `./deploy/scripts/post-build.sh --proxy https://<new machine>`: allows the new code hash,
   registers the machine, takes it to production.
4. Bring it up to the feed's configuration (`pushEndpoints` / `pushAdmins`, or a new publication,
   step 9), and install its credentials (`PROXY_URL=https://<new machine>` for both uploads).
5. Put its id in the keeper's `-tee` list and confirm a cycle lands. Then retire the old machine,
   `(cd tools && go run ./cmd/retire-tee -tee <old id>)` with the machines' owner key, and drop its
   id from `-tee`: the keeper's list is static, and a request naming a machine that is paused or
   not configured reverts. Keep at least `requiredSignatures` machines in production throughout.

Disabling the old code hash (`disableCodeHashPlatforms`, the extension owner's) pauses every active
machine still running it in the same transaction, so it comes after the last one is replaced.

## When the `FlareTeeManager` diamond is re-deployed

All extension registrations on that chain are wiped. Because the USDX extension
is a *reserved* id, it is re-registered by governance at the same id rather than
minted afresh:

1. Re-run the upstream deploy and governance follow-ups (step 4) — `registerReserved`
   restores extension id `1`, and the sender and store addresses may change.
2. Re-run `./deploy/scripts/import-upstream-addresses.sh <chain>`: it takes the new
   `FlareTeeManager` and the other system contracts from the contract registry, the oracle
   contracts from upstream's deploys file, and re-renders the bundled indexer's configuration
   (restart the indexer to pick it up). Where upstream's deploys file lists no oracle contracts,
   as Songbird's does, the addresses file keeps its old entries: write the new ones into
   `deploy/config/<chain>/deployed-addresses.json` by hand. Re-render the proxy's and the
   hand-rendered indexer's configurations (step 6) if `FlareSystemsManager`, `Relay`,
   `VoterRegistry` or `FlareTeeManager` moved, and have the administrator restart the indexer and
   the proxy.
3. `(cd tools && go run ./cmd/wire-extension)` on the new diamond.
4. The `EXTENSION_ID` the VM was launched with is unchanged (`1`), and the enclave holds no
   contract address, so the machine needs no restart. Re-curl `/info` and confirm `extensionId`
   is still `1`.
5. `./deploy/scripts/post-build.sh` for every machine.
6. Re-publish endpoints and admins (step 9) — the wipe clears the published
   commitments — then run a keeper cycle.
