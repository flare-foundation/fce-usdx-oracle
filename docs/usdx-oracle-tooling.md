# USDX Oracle — Tooling Reference

What every script and command does, and what is actually happening when you run it.

Companion to [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md), which puts
these in order for a live deployment. This one is the index: reach for it when you know the
stage and need the tool, or know the tool and need what it touches.

Three families:

| Family | What it is |
| --- | --- |
| `deploy/scripts/*.sh` | orchestration — sequences the Go tools, Docker and `cast`; `deploy/scripts/lib/*.sh` holds what they share |
| `tools/cmd/*` | Go CLIs that talk to the chain and the proxy, `send-secret` among them |
| `cmd/*` | the extension binaries: the image entry point and the development runners |

`tools/` is its own module and imports the extension's `pkg/` rather than restating it: the
feed update, the readiness report, the configuration payloads, the provisioning envelope and the
protocol's constants each have one implementation, which the tooling follows by construction.
The payloads that cross the contract boundary — the feed update request, the feed update and the
two configuration payloads — are also pinned to Solidity's own encoding in `deploy/testdata/wire`.
The readiness report and the envelope have no contract-side encoder, so their only reference is
the extension's codec and its tests.

---

## Patterns you will see everywhere

Six mechanisms recur across the tools. Knowing them makes most flags self-explanatory.

**Flags common to nearly every `tools/cmd`.** Each falls back to an environment variable, which
`deploy/scripts/use-chain.sh` puts in `.env`, and then to the active chain's default:

| Flag | Is | Default |
| --- | --- | --- |
| `-a` | deployed addresses file | `$ADDRESSES_FILE`, else `deploy/config/<chain>/deployed-addresses.json` |
| `-c` | chain RPC url | `$CHAIN_URL`, else the chain's public RPC |
| `-p` | extension proxy url | `$EXT_PROXY_URL`, else `http://localhost:6674` |

The chain is `FLARE_CHAIN` (`local`, `coston`, `coston_local`, `coston2`, `songbird` or `flare`),
else the one `CHAIN_ID` names, else `local` — or `coston2` where `LOCAL_MODE=false`. Chain id 16
names `coston`, so a `coston_local` deployment is selected by `FLARE_CHAIN`, which `use-chain.sh`
writes; it shares Coston's RPC and has its own addresses file. The tools load the repository's
`.env` (a variable already in the environment wins) and then `deploy/config/stack.env`, which
`start-services.sh` writes for the simulation's stack and which wins over both. A relative path
resolves against the repository root unless it exists from the working directory. `audit-deploy`
is the exception: it takes only its own flags.

**The `teeId` is derived, never read.** A machine's id is the address of the public key its proxy
serves in `GET /info` (`fccutils.TeeProxyId`); no field of that JSON is trusted for it. A proxy
therefore cannot name an id without serving the key behind it, and whatever is encrypted to that
key opens only where its private key is. It says nothing about which host is answering: any
proxy can serve a copy of another machine's `/info`. That the key belongs to an enclave running
an accepted build is what [`verify-tee`](#what-verify-tee-establishes-and-what-it-does-not) checks
against the registry, and `send-secret build -tee` is what keeps a credential on that key. `-p`
selects the machine a command reads, registers or retires: the one whose key that proxy serves.

**Two channels, and the choice is a security control.** An **instruction** goes on-chain through
the sender contract, which admits a publication only from governance and a feed request for its
fee; a **direct** action is `POST /direct` on the
proxy, authorised by `DIRECT_API_KEY` and, for credentials, by admin signatures. Every command
is valid over exactly one of them, and the extension refuses the other:

| Command | Channel | Why the other is refused |
| --- | --- | --- |
| `GET_FEED` | instruction | direct is free and unrecorded, which would leave the permissionless path unexercised |
| `SET_ENDPOINTS`, `SET_ADMINS` | instruction | direct would skip the sender's governance check |
| `SET_SECRET` | direct | on-chain would put a ciphertext in permanent public storage |
| `GET_READINESS` | direct | a status answer stale within minutes is not worth a fee and permanent calldata |

**Fees are read, never hard-coded.** A `requestFeedUpdate` pays the registry's `OperationFees` row
for `(TEE_ORACLE, GET_FEED)` — the registry's default fee while no row is set — once per named
machine. A publication pays the sender's own quote for its active set
(`getEndpointsPublicationFee`, `getAdminsPublicationFee`). The op type and command identifiers are
constants upstream rather than contract getters, so the tools take them from the extension's
`pkg/protocol`; a divergence would price the wrong row and every request would revert for an
underpaid fee.

**Publishers confirm, against one machine.** An instruction may never be delivered, and the
extension may reject it. After sending, `send-endpoints` and `send-admins` ask the machine behind
`-p` for its signed readiness report until it names the hash just published (`-wait`, default
90 s) and exit 1 if it does not. That is one machine; the rest of the fleet is
[`fleet-status`](#fleet-status)'s job. Without `DIRECT_API_KEY` (or `-directApiKey`) the check
cannot run: the tool warns and exits 0. `-wait 0` skips it the same way, leaving the chain
expecting one configuration and the machine possibly running another. With `-out` nothing is
sent, so there is nothing to confirm until the governance executor has run the call.

**`-dry-run` before spending gas.** Both publishers validate the payload, read the sender's
current version and fee quote, and print the predicted version, hash, fee and the active machines
the publication would go to, sending nothing. It still needs the chain, the sender, at least one
active machine (the quote refuses an extension with none) and, on a live chain,
`EXTENSION_OWNER_KEY` ([below](#which-key-each-command-needs)). Worth the extra command every
time: a payload the machine rejects costs a transaction and leaves the feed's latest hash naming
a configuration no machine runs, so every update is rejected until a good one is published.

### Which key each command needs

| Command | Signs with | Notes |
| --- | --- | --- |
| `wire-extension`, `allow-tee-version`, `set-governance` | `EXTENSION_OWNER_KEY` | the extension owner's calls. `wire-extension` also reads `MACHINE_OWNER_KEY`, for its address only |
| `register-tee`, `retire-tee` | `MACHINE_OWNER_KEY`, else `EXTENSION_OWNER_KEY` | the registry takes these only from the machine's owner. `EXTENSION_OWNER_KEY` must be set on a live chain even when `MACHINE_OWNER_KEY` signs, and for `retire-tee -dry-run` |
| `send-endpoints`, `send-admins` | `EXTENSION_OWNER_KEY` | sending works only while that key is the sender's governance. Required for `-dry-run` and `-out` too, which sign nothing; its address is the default `-claimBack`. `-encode` needs no key and no chain |
| `keeper` | `KEEPER_KEY`, else `EXTENSION_OWNER_KEY` with a warning | a funded throwaway: both its transactions are permissionless |
| `verify-deploy` | `EXTENSION_OWNER_KEY` | for `-step deploy`, `register` and `all`; the other steps need none |
| `send-secret sign` | `ADMIN_KEY` | one admin of the set that governs the envelope's ref |
| `send-secret build`, `commit`, `signers`, `submit` | none | `submit` needs `DIRECT_API_KEY`; `commit` and `build -ref hex_cash_url` need `HEX_CASH_URL` and `HEX_CASH_SALT` |
| `verify-tee`, `fleet-status`, `audit-deploy`, `readiness`, `oracle-watch`, `cycles-web` | none | read-only. `fleet-status`, `readiness` and `oracle-watch` need `DIRECT_API_KEY` for what they ask a machine |
| `local-stack` | none | needs `PROXY_PRIVATE_KEY`, and `INITIAL_OWNER` or `EXTENSION_OWNER_KEY` for the machine's owner address |

Every command that signs is built on one bootstrap (`support.DefaultSupport`), which reads
`EXTENSION_OWNER_KEY` first. On `local` it falls back to a hard-coded Hardhat key, funded only
there, with a warning; on any other chain it refuses to start without the variable, even where
the command then signs with another role's key or signs nothing at all. The legacy name
`DEPLOYMENT_PRIVATE_KEY` is refused with a message to rename it. The read-only commands are built
on a bootstrap that holds no key (`support.ReadOnlySupport`), so an admin or a coordinator who is
not the extension owner can run them.

The scripts use the same keys: `deploy-oracle.sh` the extension owner's (and on `local` the
simulation's `LOCAL_GOVERNANCE_KEY`); `publish-local.sh` `LOCAL_GOVERNANCE_KEY`, else
`EXTENSION_OWNER_KEY`; `post-build.sh` and `stop-services.sh` those of the tools they run; the
upload scripts every key in `ADMIN_KEYS` (comma-separated), or the single `ADMIN_KEY`, and none
with `--build-only`. `DIRECT_API_KEY` is not a signing key: it is the proxy's gate for the direct
channel.

---

## The lifecycle, in order

| Stage | Run | Doc |
| --- | --- | --- |
| 1. deploy the oracle contracts | production: upstream `deploy-tee-oracle.sh` plus governance follow-ups; a test deployment: `deploy-oracle.sh` | [deployment-steps.md](deployment-steps.md) |
| 2. build and start the machine | production: `build-image.sh`, then a Confidential Space VM; the simulation and tests: `full-setup.sh` → `start-services.sh` | [deployment-steps.md](deployment-steps.md) |
| 3. register the machine on-chain | `post-build.sh` | |
| 4. publish endpoints and admins | `send-endpoints`, `send-admins`: the `-out` hand-off on production, `publish-local.sh` on a deployer-governed sender | [production setup](usdx-oracle-production-setup.md) |
| 5. install credentials | `upload-rpc-keys.sh`, `upload-hex-key.sh` | |
| 6. commission and run the feed | `keeper`, `oracle-cycle.sh` | [keeper](usdx-oracle-keeper.md) |
| 7. retire, replace or upgrade a machine | `retire-tee`, `post-build.sh`, `allow-tee-version` | [lifecycle](usdx-oracle-machine-lifecycle.md) |

`full-setup.sh` writes `deploy/config/extension.env` from `EXTENSION_ID` in `.env` (the reserved
id 1 when unset), starts the stack and runs `post-build.sh`. Everything after stage 3 is
deliberately manual: it moves credentials and money.

Stages 3, 5 and 7 repeat **per machine**. Stage 4 is **per feed**: one publication goes to every
machine active on the extension when it executes, and a machine that reaches PRODUCTION later
has to be brought up to date separately
([below](#a-machine-that-joins-after-a-publication)). Stage 6 is fleet-wide: one keeper serves the
machines on its `-tee` list. Stage 7 is not exceptional — a rebuild or a restart mints a new
enclave key, so replacing a machine is the ordinary consequence of shipping a change, and the
identity it replaces has to be retired. Left alone it stays in the active set, dispatched to and
paid for on every publication, until its availability check expires and someone suspends it.

---

## Setup and deployment

| Tool | What it does |
| --- | --- |
| `use-chain.sh <chain>` | Copies `.env.<chain>` → `.env`, which every script auto-loads; `--list` lists the files there are. Run it first, and again whenever you edit the chain's env. On a live chain it also creates and fills in files — see below. Coston has two rows: `coston` is the official USDX deployment under extension 1, `coston_local` a test deployment of our own on the same chain. |
| `import-upstream-addresses.sh <chain> [upstream]` | Copies the sender and feed store addresses from a flare-smart-contracts-v2 deploy (`deployment/deploys/<network>.json` in `upstream`, default the sibling checkout) into `deploy/config/<chain>/deployed-addresses.json`. On a live chain it also refreshes the system contracts from the network's `FlareContractRegistry` and renders the chain's indexer configuration from them; for `local` it seeds the file from the e2e simulation's dump (`SIM_DUMP`). Re-run after every upstream deploy. On a chain this repository deploys itself, the oracle entries come from that deploy instead and are left out here. |
| `deploy-oracle.sh --chain <chain> [--refresh]` | A test deployment: upstream's oracle contracts, deployed through `contracts/script/DeployTeeOracle.s.sol` and left deployer-governed, so publications execute at once. On `local` it reserves the extension id; on a testnet (`coston_local`, `coston2`) it checks that the reserved `EXTENSION_ID` is the deployer's, or has the registry mint a public id for an allowlisted deployer and records it in `.env` and `.env.<chain>`. Imports the addresses. Reuses a deployment still on chain unless `--refresh`, or unless its store serves a feed other than `pkg/protocol`'s. Refuses every chain whose contracts are upstream's (`coston`, `songbird`, `flare`). |
| `full-setup.sh [--chain c] [--local]` | Writes `deploy/config/extension.env` from `EXTENSION_ID`, starts the stack, runs `post-build.sh`. `--chain` (default the chain `.env` names) first switches to `.env.<chain>` through `use-chain.sh` when it exists; `--local` runs the stack as host processes. On a mainnet it refuses `SIMULATED_TEE=true` or a `MODE` other than 0. |
| `full-setup-host.sh [--chain c] [--attach]` | `full-setup.sh --local`: the extension TEE and its proxy as host processes (`tools/cmd/local-stack`, proxy on 6684); `--attach` then follows their log, Ctrl-C stopping the stack. `LOCAL_STACK_REDIS` names another redis. |
| `start-services.sh [--chain c] [--local]` | Builds the extension image and brings up the compose stack (extension TEE, proxy, redis; on a live chain with `docker-compose.live.yaml` and the proxy configuration rendered for the chain), or `--local` the host-process stack. A live chain needs its indexer running and caught up (`docker-compose.<chain>-indexer.yaml`, or `INDEXER_DB_*` naming one of your own); it refuses one that lags, and the stack joins the indexer's Docker network (`fce-usdx-oracle-<indexer chain>`, so `coston_local` shares Coston's; `COMPOSE_NETWORK` overrides it). Records the simulation's stack in `deploy/config/stack.env`. `USE_LOCAL_SIBLINGS=1` builds node and proxy from sibling checkouts. |
| `post-build.sh [--proxy <url>]` | Waits for the extension proxy and the normal (FTDC) proxy, then allows the image's code hash (`allow-tee-version -version $TEE_VERSION`), sets the extension's TEE governance (`set-governance`) and registers the machine (`register-tee`). `--proxy` addresses one machine of a fleet without editing `.env`. Re-runnable: a machine already in production is left alone. |
| `stop-services.sh [--chain c] [--local] [--keep-registered]` | Retires the stack's machine on chain (`retire-tee -p`) while its proxy can still name it, then stops the stack `start-services.sh` recorded; `--keep-registered` skips the retirement. The chain's indexer is left running. |
| `build-image.sh`, `build-keeper-image.sh` | Build and push the extension image and the keeper image — [below](#building-the-images). |

**`use-chain.sh` writes more than `.env`.** On a live chain (any but `local`) it creates
`.env.<chain>` from `.env.example` when missing, then fills in, only where the file has no value:
`EXTENSION_OWNER_KEY` from the call (a different key from the file's is refused), `INITIAL_OWNER`
derived from it, a generated `PROXY_PRIVATE_KEY` and `DIRECT_API_KEY`, `LOCAL_MODE=false` and
`SIMULATED_TEE=true`. Only when `deploy/config/admins/<chain>.json` does not exist, it also fills
`ADMIN_KEY` (the deployer) and `ADMIN_KEYS` (the deployer and a generated second key), and writes
that admins file: backing 1-of-1 the deployer, providers 2-of-2 the deployer and the second key. A
chain that has an admins file — `coston` and `songbird` among them — has admins who hold their
own keys, so none are generated; when the file names a backing admin other than this deployer it
warns. `EXT_PROXY_URL`, `DIRECT_API_KEY`, `SIMULATED_TEE` and `MACHINE_OWNER_KEY` given on the
call are written into `.env.<chain>`, replacing the file's values. `HEX_API_KEY`, `HEX_CASH_URL`
and `HEX_CASH_SALT`, from the call or from the `.env` being replaced, are carried into the new
`.env` and never into `.env.<chain>`; `--no-hex` drops them. It needs `cast`, `openssl` and `jq`
on a live chain, and refuses a `.env.<chain>` that does not source as shell.

For a production deployment the oracle contracts (`TeeOracleInstructionsSender` +
`TeeOracleFeedStore`) are deployed upstream — `deploy-tee-oracle.sh` plus governance follow-ups
(`registerReserved`, the `TEE_ORACLE` fee rows, `addCustomFeeds`); see
[deployment-steps.md](deployment-steps.md). For the simulation and for a test deployment on a
testnet, `deploy-oracle.sh --chain <chain>` deploys the same contracts deployer-governed and
checks or registers the extension
([deploy/README, "Testing on a testnet"](../deploy/README.md#testing-on-a-testnet-coston_local-coston2)).
After the upstream deploy, `import-upstream-addresses.sh <chain>` slots the deployed addresses
into `deploy/config/<chain>/deployed-addresses.json` so the tools resolve them by name, and
`wire-extension` performs the extension owner's share (`setExtensionContracts` and the
machine-owner allowlist) once governance has run `registerReserved`. It leaves the extension's
supported key types alone: they serve wallet-key operations, which this extension has none of.

### Building the images

| Script | What it does |
| --- | --- |
| `build-image.sh [--tag t] [--image repo] [--no-push]` | Builds the extension image (`Dockerfile`, `linux/amd64`, `SOURCE_DATE_EPOCH` from the commit) and checks what a launch depends on: `MODE=0` baked in, a launch-policy label that does not list `MODE`, and `5500/tcp` the only exposed port. Its code hash is the image's config digest; after the push it checks the registry holds the same digest. The tag defaults to the short commit, with `-dirty` for an unclean tree (`IMAGE_TAG`, `IMAGE_REPO`). Not a reproducible build — for one, see [REPRODUCIBILITY.md](../REPRODUCIBILITY.md). Needs docker, jq and push access. |
| `build-keeper-image.sh [--tag t] [--chain c] [--image repo] [--no-push] [--allow-dirty] [--force]` | Builds the keeper image (`keeper.Dockerfile`, BuildKit) carrying `keeper` and `cycles-web`, with `deploy/config/<chain>/deployed-addresses.json` (`--chain`, `KEEPER_CHAIN`, default `coston`) baked in at `/opt/usdx/deployed-addresses.json` and `ADDRESSES_FILE` and `FLARE_CHAIN` set to match; it refuses a baked file with no `TeeOracleFeedStore`. The tag defaults to `v0.1.0`. It refuses to push from a dirty tree without `--allow-dirty`, and to move a published tag without `--force`. Nothing on chain refers to this image. |

`build-image.sh` writes `out/images/<tag>.env`, the hand-off to the rest of the flow:

| Variable | Is |
| --- | --- |
| `TEE_IMAGE` | the image reference to launch |
| `EXPECTED_CODE_HASH` | the code hash the machine will attest and register |
| `IMAGE_COMMIT`, `IMAGE_DIRTY` | the tree it was built from |
| `SOURCE_DATE_EPOCH`, `TEE_NODE_VERSION` | the build inputs |
| `PUSHED` | whether it went to the registry |

`EXPECTED_CODE_HASH` is what pins the rest of the flow to this build: `allow-tee-version` reads
it as its `-codeHash` default, and the upload scripts pass it to `verify-tee -codeHash`. Nothing
reads the file by itself; put the value in `.env` before `post-build.sh` and the uploads.
`build-keeper-image.sh` writes `out/images/keeper-<tag>.env` the same way: `KEEPER_IMAGE`, the
pushed `KEEPER_IMAGE_DIGEST` to pin a server to, the commit, the chain and the feed store it was
baked for.

### The Go commands behind them

| Command | What it does | Key flags |
| --- | --- | --- |
| `wire-extension` | Wires a RESERVED extension after governance runs `registerReserved`: `setExtensionContracts` (the sender — required before `register-tee`'s availability check — and no state verifier) and the machine-owner allowlist. The allowlisted address is the machines' owner, not the caller: `-machineOwner`, else `MACHINE_OWNER_KEY`'s address, else `INITIAL_OWNER`, else the caller. Refuses unless the key is the extension's registered owner. Idempotent. | `-extensionId` (default `EXTENSION_ID`), `-sender` (default the addresses-file entry), `-machineOwner` |
| `set-governance` | Publishes the TEE governance signer set and threshold from `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD` (default `INITIAL_OWNER`, else the caller, at 1) for the extension the machine at `-p` reports. **Must match what the TEE node was launched with**, or `register-tee` fails `InvalidGovernanceHash`. | `-p` |
| `allow-tee-version` | Allows a code hash and platform for the extension, all three read from the proxy's `/info`. Skips a pair already allowed. | `-codeHash` (default `EXPECTED_CODE_HASH`), `-version` (default `v0.1.0`) |
| `register-tee` | Registers the machine and takes it to `PRODUCTION`, in resumable steps. Signs as the machine's initial owner, which the registry requires as the sender, and refuses before sending when the key is not the owner the machine reports. | `-command` (default `rap`), `-state`, `-resume`, `-h`, `-ep`, `-i` |
| `retire-tee` | Pauses a machine in the registry: `-p <proxy>` the machine behind a proxy, `-tee <id>` by id, `-stale` every active machine of `-extensionId` whose registered URL does not answer as itself — the leftovers of stopped stacks. Only the owner may, and only a PRODUCTION or SUSPENDED machine; an already paused one is skipped. A paused machine is never dispatched to and its updates are rejected. Its owner could return it with `toProduction` and a fresh availability proof, which a stopped machine, whose key is gone, can never produce: for a stopped stack the pause is final. | `-p`, `-tee`, `-stale`, `-extensionId`, `-dry-run` |

**Pin the code hash before allowing it.** The pair `allow-tee-version` allows is what the
registry will accept for the extension, so it must be the build you mean, not whatever the
machine says it runs. With `-codeHash` (or `EXPECTED_CODE_HASH`) the machine's report must match,
or nothing is sent. Without it, on a real machine, the tool allows the code hash the machine
reports about itself and only warns. For production, set `EXPECTED_CODE_HASH` from
[`out/images/<tag>.env`](#building-the-images) first.

`register-tee -command` is a string of step letters, run in order:

| Letter | Step |
| --- | --- |
| `r` | pre-register the machine on-chain, which also issues its attestation challenge |
| `R` | request a **fresh** attestation and challenge |
| `a` | run the FTDC availability check (data providers cosign); first checks the FTDC proxy's signing policy is in step with the chain |
| `p` | promote to `PRODUCTION`, with the availability proof from the FTDC proxy |

`r` on a machine that is already registered requests a fresh attestation instead: the challenge
is time-limited, and re-running the original one (a new image, a retry) would revert
`ChallengeExpired`. A machine already at PRODUCTION ends the run with nothing to do. `R` requests
a fresh attestation unconditionally. `-h` is the URL registered on chain as the machine's address
(default `-p`), `-ep` the normal (FTDC) proxy (default `http://localhost:6662`), and `-i` an
availability-check instruction id for a run with `p` but not `a`; because `-h` is taken, help is
`-help`. Progress is written to the `-state` file (default `../deploy/config/register-tee.state`,
relative to `tools/`), which a run **deletes at its start unless `-resume`** is given, and again
once every step has succeeded.

---

## Publishing configuration on-chain

Both publishers take `-config <file>` (JSON, for operator convenience), build the ABI-encoded
payload from it, and call `setEndpoints(feedId, groups, claimBack)` or
`setAdmins(feedId, roles, claimBack)`. The calls name no machine: there is one publication per
feed. The sender assigns the version, records `latestEndpointsHash(feedId)` /
`latestAdminsHash(feedId)`, and dispatches the payload to every machine active on the extension
when the call executes; the fee is its quote for that set. Neither carries a secret —
the payloads are public forever.

`setEndpoints` and `setAdmins` are governance's calls, and on a production deployment the sender
is in production mode from the moment upstream's deploy script finishes, so they are Flare
governance's, through the timelock. The normal path there is `-out <file>`: the tool predicts the
version and the hash, takes the sender's fee quote, and writes the exact calldata to a JSON
**hand-off** for the governance executor, who runs it through `executeGovernanceCall` and
attaches the fee at execution.

| Hand-off field | Is |
| --- | --- |
| `command`, `target`, `calldata` | the call, byte for byte |
| `valueWei` | the fee to attach at execution |
| `payloadHash`, `predictedVersion` | what the sender will record; after execution `latestEndpointsHash` / `latestAdminsHash` must equal `payloadHash` |
| `feedId`, `teeIds` | the feed, and the active set the fee was quoted for |
| `note` | the executor's instructions |

The prediction holds only if no other publication for the feed lands first, and the quote only
if the active set does not change before execution: a machine registered or paused in between
changes the fee, and a short fee reverts. With `-out` the default `-claimBack` is the tool's key;
name the executor's wallet instead. The direct path (no `-out`) sends the call itself and works
only while the caller is governance — a deployer-governed deployment from `deploy-oracle.sh`,
which `publish-local.sh` drives.

### `send-endpoints` — which RPC sources the machine reads

```bash
cd tools && go run ./cmd/send-endpoints -dry-run -config <ENDPOINTS_JSON>
```

The sender resolves from the `TeeOracleInstructionsSender` entry in the addresses file (or
`-instructionSender`); `-feedId` defaults to USDX/USD. For the governance path, add `-out <file>`
and hand the file over. `-encode -version <n>` prints the payload bytes for a dev server's
`/action` and touches no chain.

The JSON names each group (a tagged endpoint list), its quorum threshold, and each endpoint as
PUBLIC (`url`) or PRIVATE (`urlHash` + `secret`). `secret` names the credential ref a `{secret}`
in a public URL is replaced with, or, for a private endpoint, the ref that delivers its URL off
chain. The chain RPC groups are public; the restricted `hex_cash` reserve endpoint is private, so
its URL never lands in calldata: `send-secret commit` prints the `urlHash` to put there. Values
never appear; only names and commitments.

What happens: the tool ABI-encodes `Endpoints{version, feedId, groups}`, predicts the version
(`endpointsVersion(feedId) + 1`) and the hash, and sends `setEndpoints`. The sender records the
hash and dispatches. The extension validates and applies the payload, and from then on every
update it signs carries that hash; the store rejects any update whose hash is not the feed's
latest, so a machine that has not applied a new configuration publishes nothing until it does.

Validation follows the enclave's own rules (`validate()` in `internal/provisioning/store`): known
groups only, every pinned chain group and `hex_cash` present, at least one endpoint and
`1 ≤ threshold ≤ endpoints` per group, https only, `{secret}` and `secret` used together with the
placeholder after the host, an endpoint either PUBLIC or PRIVATE and never both, `hex_cash`
exactly one PRIVATE endpoint at threshold 1 under `hex_cash_url`, and no backing ref
(`hex_api_key`, `hex_cash_url`) as the secret of a chain group, public or private. What only the
machine knows — the version it last applied, which credentials have arrived — stays the
machine's verdict, which the delivery check reports.

### `send-admins` — who may install credentials in the machine

```bash
cd tools && go run ./cmd/send-admins -dry-run -config <ADMINS_JSON>
```

The JSON is two role-tagged signer sets and nothing else (no `version`: the sender assigns it):

```json
{
  "backing":   { "threshold": 1, "admins": ["0x…"] },
  "providers": { "threshold": 2, "admins": ["0x…", "0x…"] }
}
```

`backing` governs `hex_api_key` and `hex_cash_url` (floor 1); `providers` governs every other ref
(floor 2). Each set holds 1–32 distinct non-zero addresses, and its threshold lies between the
floor and the smaller of the set's size and 16, the extension's signature cap. Addresses and
thresholds only — private keys go nowhere near this tool. `deploy/config/admins/<chain>.json`
holds each chain's set.

What happens: the same shape as `send-endpoints` — `Admins{version, feedId, roles}` is encoded,
`latestAdminsHash(feedId)` recorded, the instruction dispatched, the machine asked. The extension
installs both roles or neither, so a rejected half cannot leave it running a mixture of old and
new authorities.

**Which set governs which ref is pinned in the extension build and is not read from this
payload.** Otherwise whoever publishes admins could move a credential under whichever set they
hold, and the split would authorise nothing.

### A machine that joins after a publication

A publication reaches the machines that were active when it executed. A machine that reaches
PRODUCTION later — including every replacement — holds nothing, and until it does,
`requestFeedUpdate` naming it reverts `TeeIdNotConfigured` (or `FeedNotConfigured` while the
feed has no publication at all). It is brought up to date by the sender's permissionless,
payable `pushEndpoints(feedId, teeIds, groups)` and `pushAdmins(feedId, teeIds, roles)`, fed the
groups and roles verbatim from the latest `EndpointsPublished` / `AdminsPublished` event;
`getEndpointsPushTargets(feedId)` and `getAdminsPushTargets(feedId)` list the machines that are
behind, and the diamond's `calculateFeeByTeeIds` prices the call. The same push re-delivers an
instruction that never reached its enclave. **No tool in this repository wraps them**; send them
with a contract client of your own.

---

## Installing credentials

Never on-chain. The credential is encrypted to the enclave's attested key and delivered over
`POST /direct`. Full flow and who holds what: [production setup](usdx-oracle-production-setup.md).

| Tool | What it does |
| --- | --- |
| `verify-tee` | **Run before any credential.** Is the key this proxy serves a PRODUCTION machine of your extension, running an accepted build? `-printTeeId` passes the answer on. |
| `send-secret build` | Reads the machine's key from `/info`, encrypts the credential to it, emits an unsigned envelope bound to the chain, the machine, the ref and a version. `-tee` refuses any key but the attested one. |
| `send-secret sign` | One admin signs the envelope body (EIP-191). Refuses a signer already on it — M-of-N needs distinct ones. |
| `send-secret signers` | Who has signed an envelope, recovered offline; with `-admins <file>`, each against the set that governs its ref, and whether the distinct admins reach that set's threshold (non-zero exit when they do not). |
| `send-secret submit` | Combines the signatures, POSTs to `/direct` and waits for the machine's verdict (`-wait`, default 60 s). Refuses fewer signatures than the ref's floor, more than 16, or an envelope older than an hour. `-extension <url>` posts to a dev server instead. |
| `send-secret commit` | Prints the `urlHash` the `hex_cash` group must publish for `HEX_CASH_URL` under `HEX_CASH_SALT`. |
| `upload-hex-key.sh [--threshold n] [--build-only] [--no-verify]` | The backing credentials for one machine: `hex_api_key` from `HEX_API_KEY` and, when `HEX_CASH_URL` is set, `hex_cash_url` from `HEX_CASH_URL` and `HEX_CASH_SALT`, which must be the URL and salt the published commitment was made from. Signs with every key in `ADMIN_KEYS`, or `ADMIN_KEY`. The backing threshold is `--threshold` / `BACKING_THRESHOLD`, else `backing.threshold` in `deploy/config/admins/<chain>.json`, else 1; with fewer distinct signers than that it stops before building and points to `--build-only` (Songbird's backing set is 2-of-3). Then submits, and confirms from the machine's readiness report. |
| `upload-rpc-keys.sh [--refs a,b] [--all] [--threshold n] [--build-only] [--no-verify]` | The provider credentials: every ref the machine's readiness report lists as missing, the refs `--refs` names, or with `--all` every ref in `ENDPOINTS_FILE` (default `deploy/config/endpoints/coston2.json`), to rotate. Backing refs are skipped. Each credential is read from its ref in capitals (`ankr_flare_key` → `ANKR_FLARE_KEY`). Signs with every key in `ADMIN_KEYS` (or `ADMIN_KEY`); short of `--threshold` / `PROVIDER_THRESHOLD` signatures (default 2, the providers floor) it keeps the envelope for the other signers instead of submitting. |
| `check-endpoints.sh <endpoints.json>` | Before anything is published or uploaded: tries every public provider in a configuration with the credential that would be installed for it — read from its ref's variable in capitals, set in front of the command, since `.env` is not loaded; an unset one is skipped — and compares the chain each answers with the one its group reads. A credential the enclave would refuse (a space, `/`, `?`, `#`, `&`) fails here. Keys reach curl on stdin and are never printed; exits non-zero on any failure. |

Both upload scripts read `.env` and the recorded stack, prompt hidden for anything required and
unset, and take `PROXY_URL` (default `EXT_PROXY_URL`), `EXPECTED_CODE_HASH` (optional; on a
`SIMULATED_TEE=true` machine tee-node's test constant), `DIRECT_API_KEY` and `OUT_DIR`. Each runs
`verify-tee -printTeeId` first and pins every build to the id it prints; the version of each
envelope is the current Unix time. `--no-verify` is for a mocknode, which has no attestation and
no registration, and never for a machine that will hold a real credential.

### What `verify-tee` establishes, and what it does not

The next step encrypts a credential to a public key the proxy served; a proxy that lied about
that key receives the plaintext. Reading a code hash from that same proxy proves nothing — the
same untrusted source supplied both halves — so `verify-tee` takes everything but the key from
the chain:

```
/info  → public key → teeId                     (derived, not asserted)
chain  → that teeId is registered, at PRODUCTION
chain  → in the expected extension               (-extensionId, default EXTENSION_ID; neither: refused)
chain  → the registry's record of it: code hash and platform
chain  → the extension accepts that code hash on that platform
you    → the code hash is the one you were told, out of band   (-codeHash)
/info  → the attestation token's claims agree with that record (a cross-check)
```

It exits non-zero on the first failure, so a script can gate on it. `-printTeeId` writes only the
teeId to stdout, and only on success, with the report on stderr; `-quiet` prints only failures.

**A pass establishes** that the key this proxy served to this request belongs to a machine the
registry admitted to PRODUCTION under your extension. Registration verified an attestation for
that key, so the code hash and platform on record are what the enclave holding it runs; the
extension still accepts that pair; and with `-codeHash` it is the build you were told to expect.
A credential encrypted to that key opens only inside such an enclave.

**It does not establish:**

- **Anything about the next request.** `send-secret build` fetches `/info` again, and a proxy can
  serve an honest key to the check and a substituted one to the encryption a moment later.
  `-printTeeId` → `build -tee` closes that gap; the upload scripts do it, and by hand you must.
- **Which build, without `-codeHash`**: only that it is one your extension accepts. It warns.
- **More than the chain node says.** The check is as good as the RPC behind `-c`; use one you
  trust.
- **The attestation itself.** The token is parsed only to cross-check the registry's record, and
  its signature is not verified here: the result rests on the verification registration did.
- **That the machine is configured or healthy.** Whether it holds the latest publication is
  `fleet-status`; whether it would sign is `readiness`.
- **Anything about a real enclave under `SIMULATED_TEE=true`**, where the code hash is tee-node's
  test constant. It warns.

### By hand: pass the attested id on

```bash
cd tools
TEE_ID="$(go run ./cmd/verify-tee -p "$PROXY" -codeHash "$EXPECTED_CODE_HASH" -printTeeId)" || exit 1
read -rs SECRET_VALUE && export SECRET_VALUE        # the credential, never on a command line
go run ./cmd/send-secret build -p "$PROXY" -tee "$TEE_ID" -ref ankr_flare_key \
    -version "$(date +%s)" -out envelope.json
go run ./cmd/send-secret sign -in envelope.json      # each admin, with their key in ADMIN_KEY
go run ./cmd/send-secret signers -in envelope.json -admins ../deploy/config/admins/<chain>.json
go run ./cmd/send-secret submit -in envelope.json -p "$PROXY"   # DIRECT_API_KEY from the environment
```

`-tee` is required for a real credential: without it `build` encrypts to whatever key `/info`
serves at that moment, and the verification before it spoke about a different request. `-p` and
`-c` default to the `.env` the other tools read; the envelope is bound to the chain id the node at
`-c` reports. `-version` must strictly increase per ref on the machine: the default, 1, is right
only for a machine's first upload of that ref. For `hex_cash_url` the credential is not
`SECRET_VALUE` (set, it is refused) but `abi.encode(salt, url)`, built from `HEX_CASH_URL` (https)
and `HEX_CASH_SALT` (32 non-zero bytes of hex) — the preimage of the `urlHash` that
`send-secret commit` prints from the same two variables. The machine accepts it only against that
published commitment.

### Handing envelopes to other signers

`--build-only` on either upload script verifies the machine, encrypts each credential to it and
writes the envelopes unsigned to `deploy/envelopes/<teeId>-<ref>.json` (git-ignored; `OUT_DIR`
moves it), reading no admin key — not even one set in `.env` — and submitting nothing.
`upload-rpc-keys.sh` writes there too whenever it holds fewer signatures than the threshold, and
keeps an envelope the machine refused. The files hold ciphertext only and may travel over any
channel, but expire an hour after `build`. Each admin runs `send-secret sign -in <file>` with
their own key in `ADMIN_KEY`; `send-secret signers -in <file> -admins <admins file>` says whether
the file is ready, since the enclave answers every shortfall with the same one-line rejection; and
whoever holds `DIRECT_API_KEY` runs `send-secret submit -in <file> -p <proxy>`. A build-only run
still reads the chain (through `verify-tee`), and `upload-rpc-keys.sh` without `--refs` or `--all`
asks the machine which refs are missing, which needs `DIRECT_API_KEY`.

### Secrets come from the environment, never argv

`send-secret` reads `SECRET_VALUE`, `HEX_CASH_URL`, `HEX_CASH_SALT`, `ADMIN_KEY` and
`DIRECT_API_KEY` from the environment and has no flag form for any of them: argv is readable by
any local process through `ps`, and lands in shell history. The upload scripts prompt hidden for
anything unset and pass every secret on through the environment, `DIRECT_API_KEY` included. The
other Go tools read `DIRECT_API_KEY` by default; their `-directApiKey` flag is an override that
puts the key on argv, so prefer the variable.

---

## Running the feed

| Tool | What it does |
| --- | --- |
| `keeper` | The production process. Requests a feed update from the machines on its `-tee` list, collects each one's signed result, relays the ones that agree to the store in one batch, emits one JSON record per cycle. Holds no authority: both its transactions are permissionless. |
| `oracle-cycle.sh -tee <a,b> [--dry-run] [--loop 10m] [--records <file>]` | Builds the keeper and runs one cycle (`-once`) against the deployed stack, or with `--loop` stays up and cycles at that interval. The store comes from the addresses file; `PROXY_OVERRIDE` addresses every machine at one proxy. It does no read-back of its own: the record and the log are the result. |
| `cycles-web` | Serves a keeper's records file and the page that reads it, over HTTP: `/` the page, `/cycles.jsonl` the most recent records (`-max`, default 2000), `/healthz`. `-records` (default `/var/log/usdx/cycles.jsonl`) is the file the keeper writes with `-records`; `-addr` defaults to `:8080`. No key, no chain client, no state. It ships in the keeper image to run beside the keeper over a shared volume. |

Every cycle calls `requestFeedUpdate(feedId, teeIds)` with the `-tee` list, which is static: the
keeper never adds a machine, and one on the list that is not at the feed's latest publication
reverts the whole request `TeeIdNotConfigured`. The registry supplies only where each machine is
reached: results are polled at each machine's registered URL, and at `-p` (default
`EXT_PROXY_URL`) first. Each machine answers on its own proxy; the keeper submits the updates
that share one `observedAt`, and the store wants `requiredSignatures` of them and takes the
median. Before paying, it asks those proxies over `GET_READINESS` whether their machines would
sign; only when every one answers and none would does the cycle end `request_skipped`, with no
fee spent. Before submitting, it rehearses the store's guards, so a rejection arrives named —
`StaleEndpoints`, `StaleAdmins`, `NotNewer` — instead of as a bare revert. The outcomes and the
record are in [the keeper spec](usdx-oracle-keeper.md); running it as a service is
[running it unattended](usdx-oracle-keeper.md#running-it-unattended).

| Flag | Default | Effect |
| --- | --- | --- |
| `-tee` | — required | comma-separated target machine addresses; the fee scales with the count |
| `-store` | the addresses-file entry | the `TeeOracleFeedStore`; the sender is derived from its `instructionsSender()` |
| `-instructionSender` | derived from the store | the sender, given explicitly |
| `-feedId` | USDX/USD | the feed to request |
| `-interval` | `10m` | how often a cycle starts |
| `-once` | off | a single cycle, for a cron-driven deployment; exits non-zero when the cycle failed and the feed did not advance |
| `-dry-run` | off | stop after the store's rehearsal, before `submitFeedUpdates` |
| `-pollTimeout`, `-requestTimeout`, `-submitTimeout` | `5m`, `2m`, `3m` | the waits for the results, the request's receipt and the submission's receipt |
| `-minBalance` | `5` | warn when the paying account holds fewer coins; `0` disables |
| `-directApiKey` | `$DIRECT_API_KEY` | the key for the `GET_READINESS` preflight; without it every cycle pays unasked |
| `-p` | `$EXT_PROXY_URL` | where results are looked for first. Given explicitly, it overrides every machine's registered URL — right only with one machine |
| `-records` | stdout | append the cycle records to this file |
| `-keeperId` | the hostname | tells instances apart in the records — run at least two |
| `-a`, `-c` | `.env` | the addresses file and the chain |

The keeper spec's [Flags](usdx-oracle-keeper.md#flags) gives the reasoning behind each default.

**`-dry-run` is not free.** It sends and pays for `requestFeedUpdate` — the fee times the `-tee`
count — collects the results and rehearses the submission, then stops before
`submitFeedUpdates`, recording `dry_run`. So does `oracle-cycle.sh --dry-run`. What it saves is
the submission's gas, and what it proves is that the whole path works up to the store.

---

## Inspecting

| Tool | Answers |
| --- | --- |
| `readiness` | Prints one machine's signed readiness report as JSON: the signer recovered from its signature, the request id it is bound to, the provisioning block under `readiness` (`endpointsHash`, `adminsHash`, `ready`, `missingSecretRefs`), observation age, would-sign, the poller's counters, the last error class, source health and the chain id. `-p` asks over the proxy's direct channel and needs `DIRECT_API_KEY`; `-extension <url>` asks a dev server directly. The scripts read every machine through it (`read_machine_readiness`, `lib/env.sh`). |
| `oracle-watch` | Polls one machine's signed readiness report on a timer (`-interval`, default 2m, the machine's own observation interval; `-once`), records every sample or the error reading it (`-records`, default stdout), and prints the changes: source health, last error class, ready, would-sign, failed observations. Requires `DIRECT_API_KEY`. The between-cycles view the keeper's records cannot give. `oracle-watch.sh [--interval 2m] [--records file] [--once]` runs it with `.env`'s proxy and key. |
| `fleet-status` | Every active machine of the extension against what the chain says it should be running — [below](#fleet-status). |
| `oracle-status.sh [--store 0x…]` | What the oracle publishes right now: the store's feed and `observedAt`, the machine's registry status and its readiness report. Reads only. |
| `oracle-observe.sh [endpoints.json]` | Runs the real observation against live sources with no chain, Docker or TEE: a mocknode and a dev server on high ports (`EXT_PORT`, `SIGN_PORT`), provisioned with the endpoints and the dev admin set (`ADMINS` names another). The highest-value local check: if this is wrong, everything downstream is. |
| `audit-deploy` | An addresses file against a chain, with its own flags only and Coston defaults: `-a` (`../deploy/config/coston/deployed-addresses.json`) and `-rpc` (Coston's public RPC). Checks for code at `FlareTeeManager`, `FlareSystemsManager` and `Fdc2Hub` and lists the registry's supported platforms and next public extension id; with `-ext <id>`, that extension's owner, allowed code hashes and key types; with `-ext`, `-codeHash` and `-platform` as well, replays `addTeeVersion` from `-from` as an `eth_call` and decodes the custom error. It does not look at the oracle contracts. |
| `verify-deploy` | Post-deploy assertions by stage: `-step deploy\|register\|services\|tee-version\|tee-machine\|test\|all` (default `all`), `-checks D5,R2` to narrow, `-config <file>` the env file carrying `EXTENSION_ID` that several checks read, `-json` for machines. Exits non-zero on a failed check. |
| `stack-logs.sh [--stop]` | Follows the host-process stack's log (`out/logs/local-stack.log`) from the top of the current run; Ctrl-C detaches, or with `--stop` stops the stack. |

### `fleet-status`

"Which machines are at the feed's latest configuration, and are they running it?" No key and no
`-p`: every machine is asked at the URL it registered. It lists the extension's active set — its
PRODUCTION machines — so a paused, suspended or banned machine does not appear. The extension is
the one the sender serves, and the sender is the store's (`-store`, default the addresses-file
entry) unless `-instructionSender` names it; `-feedId` defaults to USDX/USD.

| Column | Source |
| --- | --- |
| `tee id` | the registry's active set |
| `status` | the registry's status: `NONE`, `INITIALIZED`, `PRODUCTION`, `SUSPENDED`, `PAUSED` or `BANNED` |
| `configured` | `isTeeIdConfigured(feedId, teeId)`: the sender dispatched the latest endpoints and admins to it, the precondition `requestFeedUpdate` enforces per target. Dispatch, not proof they arrived |
| `endpoints`, `admins` | the feed's `latestEndpointsHash` and `latestAdminsHash`, the same on every row |
| `live` | the `endpointsHash` in the machine's signed readiness report |
| `ready` | that report's `ready` flag |

Below the table it names the machines whose live endpoints hash differs from the published one,
and prints the store's `observedAt` and its age. The live columns read `—` without
`DIRECT_API_KEY` (or `-directApiKey`), and for a machine that cannot be asked. `-json` prints the
rows as one JSON array on a single line, with what the table leaves out: both versions, the
machine's live `adminsHash` — an admin drift is not flagged in the table — and its full
`sourceHealth` map.

It is the one to reach for first, because it is built around the sources disagreeing. A machine
can be dispatched a configuration it rejected and still report `ready: true` while running the
*previous* one: `configured` then reads true and every submission is rejected `StaleEndpoints`.
`live` against `endpoints` is what makes that visible.

### Environment overrides worth knowing

| Variable | Effect |
| --- | --- |
| `SIMULATED_TEE=true` | `register-tee` takes the code hash and platform as tee-node's simulated constants rather than parsing an attestation token; `verify-tee` and `allow-tee-version` say that nothing is learnt about a real enclave; the upload scripts expect tee-node's test hash when `EXPECTED_CODE_HASH` is unset |
| `SIMULATED_CODE_HASH` | overrides that simulated hash, for addressing a fleet running two builds mid-upgrade. Validated as exactly 32 bytes, and **ignored unless `SIMULATED_TEE=true`** — under real attestation the hash comes from the token and must not be overridable |
| `EXPECTED_CODE_HASH` | the build you expect, obtained out of band ([`build-image.sh`](#building-the-images) records it): `allow-tee-version`'s `-codeHash` default, and what the upload scripts pass to `verify-tee` |
| `DIRECT_API_KEY` | the direct channel's gate: what `fleet-status`, `send-endpoints`, `send-admins`, `keeper`, `oracle-watch`, `readiness`, `send-secret submit` and the scripts ask a machine with |

### When a transaction just says `execution reverted`

`register-tee` replays the failing call as an `eth_call` and decodes the revert against the custom
errors of Verification, Fdc2Hub, MachineManager and ExtensionManager — so prefer its output to the
raw error. It covers both the availability-check request and `toProduction`, which is where
promotion failures surface. `audit-deploy` does the same for `addTeeVersion`, and the publishers
and the keeper decode the sender's own errors from a simulated call. The reverts you are most
likely to meet are tabulated in
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md).

---

## Local development

| Tool | What it does |
| --- | --- |
| `cmd/dev/devserver` | The extension with real provisioning and observation, no chain. |
| `cmd/dev/mocknode` | Stands in for tee-node: `/sign`, `/decrypt`, `/info`, key from a fixed seed. **Not secure, not for deployment.** |
| `cmd/dev/start-tee` | The host-process runner `tools/cmd/local-stack` builds and starts. |
| `set-config.sh endpoints\|admins [config] [port]` | Posts a `SET_ENDPOINTS` or `SET_ADMINS` payload straight to a running dev extension's `/action` (port default `EXTENSION_PORT`, else 7702), no chain. The defaults are `public-only.json` and the dev admin set; `CONFIG_VERSION` must exceed the last version applied. |
| `local-stack` | Runs the extension TEE and its proxy as host processes: the proxy in-process, `cmd/dev/start-tee` as a child, redis the simulation's fourth (`-redis`, default `:6382`). Renders the proxy configuration from `-template` into `out/`; needs `EXTENSION_ID` (or `-extensionID`) and `PROXY_PRIVATE_KEY`. No Docker; see [local-e2e.md](local-e2e.md). |
| `full-setup-host.sh` | `full-setup.sh --local`: starts `local-stack` under `out/pids/`, then runs `post-build.sh` against it. |
| `deploy-oracle.sh --chain local` | Deploys upstream's oracle contracts on the simulation through `contracts/script/DeployTeeOracle.s.sol`, deployer-governed (no production mode), reserves extension 1 and imports the addresses. |
| `publish-local.sh endpoints\|admins [config] [--wait]` | Publishes on a deployer-governed sender — the simulation's or a test deployment's — in one transaction, and waits for the machine to report the new hash. On a production-mode sender it records the call through the timelock, saves the hand-off under `out/publications/`, and executes it after the timelock (`--wait`, or later `--execute <file>`). The defaults are `public-only.json` and, on the simulation, the dev admin set; elsewhere `deploy/config/admins/<chain>.json`. With `HEX_CASH_URL` set, `endpoints` commits `hex_cash` to it, generating `HEX_CASH_SALT` into `.env` on first use. |
| `e2e.sh` | Starts, stops and waits on the host-process stack's background processes (pids and logs under `out/`). |

The upload scripts take `--no-verify` for a mocknode, which has no attestation and no on-chain
registration. Never against a machine that will hold a real credential.

**Building against on-disk tee-node / tee-proxy.** `USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh`
swaps the pinned node build for one compiled from sibling checkouts. The in-image module downloads
are served from the host's module cache while the build runs (`deploy/scripts/lib/modcache-proxy.sh`),
so `go` and `python3` must be on `PATH`; without them the builds fall back to the public proxy.
Two things to know: the
sibling layout is resolved as `$PROJECT_DIR/../..`, so the repo has to sit at
`tee/extension-examples/<repo>` with `tee/tee-node` and `tee/tee-proxy` beside it — both must exist
even if only one is being changed. And `start-services.sh` writes a `go.work` at the repo root for
*host* builds which travels into the Docker context and takes precedence over the image's own
`replace` directive; `Dockerfile.siblings.dev` removes it in-layer for that reason. `.dockerignore`
cannot help, because the siblings build's context root is `tee/`, not the repo. The same `go.work`
points host builds and tests at the sibling checkouts too: run `go` with `GOWORK=off` to use the
pinned versions.

## Build and housekeeping

| Tool | What it does |
| --- | --- |
| `generate-bindings.sh` | Compiles the TEE oracle interfaces out of the sibling flare-smart-contracts-v2 checkout (`contracts/`) and regenerates the Go bindings in `tools/pkg/contracts` and `pkg/contracts/teeoraclestructs`. **Run when upstream moves.** |
| `regen-wire-fixtures.sh` | Regenerates `deploy/testdata/wire/` from the Forge vector tests (the ABI ground truth the Go codecs assert against). |
| `check-versions.sh` | Fails when the dependency pins drift apart: tee-node, go-flare-common and every other module both `go.mod` files name, and tee-proxy between `tools/go.mod` and the compose image tag. |
| `go test ./...`, in the root and in `tools/` | Each module's unit tests, one module at a time. |
| `go test ./internal/extension/ -run TestConformance` | Replays the 13 fixtures in `deploy/testdata/conformance` against the extension in process, no chain and no tee-node; the check behind [extension-contract.md](extension-contract.md). An `EXTENSION_ID` other than 1 exported in the shell makes fixtures 01 and 02 fail. |

The scripts share four files under `deploy/scripts/lib/`, sourced rather than run:

| File | Holds |
| --- | --- |
| `chains.sh` | the chain table: one row per chain with its id, public RPC, normal (FTDC) proxy, upstream deploys name, faucet, kind, indexer and indexer port, and whether its oracle contracts are this repository's (`own`) or upstream's. `resolve_chain` picks the chain the same way the tools do: an explicit `--chain`, then `FLARE_CHAIN`, then `CHAIN_ID`, then the simulation. `chain_compose_network` is the Docker network a live chain's stack joins, its indexer's. |
| `env.sh` | `load_env` (`.env`, then `deploy/config/stack.env`, both over the environment), `record_stack`, `read_machine_readiness` and `read_extension_readiness`, `env_var_for`, the ref → variable rule (`ankr_flare_key` → `ANKR_FLARE_KEY`) the credential scripts share, and `edit_in_place`, a `sed -i` that works with both BSD and GNU sed |
| `versions.sh` | `load_versions`: the tee-node pin in `go.mod` and the tee-proxy image tag in `deploy/docker-compose.yaml`, for `check-versions.sh` and `build-image.sh` |
| `modcache-proxy.sh` | serves the host's Go module cache to sibling image builds |

---

## Which extension binary runs where

| Binary | For |
| --- | --- |
| `cmd/extension-tee` | **production** — what the Dockerfile builds (tee-node and the extension in one process) |
| `cmd/dev/devserver` · `cmd/dev/mocknode` | local development without tee-node |
| `cmd/dev/start-tee` | the host-process stack's TEE, started by `tools/cmd/local-stack` |
| `tools/cmd/send-secret` | ops: build / sign / submit a `SET_SECRET` envelope |

---

_Companion docs: [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (these in
order, for a live deployment) · [deployment-steps.md](deployment-steps.md) (getting a machine
registered) · [testing.md](testing.md) (the same ground with throwaway keys) ·
[usdx-oracle-codebase.md](usdx-oracle-codebase.md) (what the code does once an instruction
arrives) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the feed's reliability semantics)._
