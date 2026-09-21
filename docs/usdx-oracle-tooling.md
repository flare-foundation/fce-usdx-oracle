# USDX Oracle — Tooling Reference

What every script and command does, and what is actually happening when you run it.

Companion to [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md), which puts
these in order for a live deployment. This one is the index: reach for it when you know the
stage and need the tool, or know the tool and need what it touches.

Three families:

| Family | What it is | Language-neutral? |
| --- | --- | --- |
| `deploy/scripts/*.sh` | orchestration — sequences the Go tools and Docker | yes |
| `tools/cmd/*` | Go CLIs that talk to the chain and the proxy | **yes** — wire format only, no extension internals |
| `cmd/*` | the extension binaries, plus `usdxprov` | no — this is the Go implementation |

`tools/` never imports `internal/`. It re-declares the wire layout instead, so it works
against an extension written in any language — and so a bug in one cannot hide a bug in the
other.

---

## Patterns you will see everywhere

Six mechanisms recur across the tools. Knowing them makes most flags self-explanatory.

**Flags common to nearly every `tools/cmd`.** Each falls back to an environment variable, which
`deploy/scripts/use-chain.sh` puts in `.env`:

| Flag | Is | Default |
| --- | --- | --- |
| `-a` | deployed addresses file | `$ADDRESSES_FILE` |
| `-c` | chain RPC url | `$CHAIN_URL` |
| `-p` | extension proxy url | `$EXT_PROXY_URL` |

**The `teeId` is derived, never read.** Tools that address one machine take its proxy URL and
derive the `teeId` from the **attested public key** in `GET /info` — not from a field in that
JSON. A proxy cannot therefore claim to be a machine it is not, and `-p` is what selects *which*
machine an instruction goes to.

**Two channels, and the choice is a security control.** An **instruction** goes on-chain and is
authorised by the sender contract's owner check; a **direct** action is `POST /direct` on the
proxy, authorised by `DIRECT_API_KEY` and, for credentials, by admin signatures. Every command
is valid over exactly one of them, and the extension refuses the other:

| Command | Channel | Why the other is refused |
| --- | --- | --- |
| `GET_FEED` | instruction | direct is free and unrecorded, which would leave the permissionless path unexercised |
| `SET_ENDPOINTS`, `SET_ADMINS` | instruction | direct would skip the sender's governance check |
| `SET_SECRET` | direct | on-chain would put a ciphertext in permanent public storage |
| `GET_READINESS` | direct | a status answer stale within minutes is not worth a fee and permanent calldata |

**Fees are read, never hard-coded.** Anything that raises an on-chain instruction reads the
current fee from the registry's `OperationFees` for the `(TEE_ORACLE, command)` pair. The op
type and command are file-level constants upstream (no longer per-contract getters), so the tools
pin their own copy in `pkg/utils` — a divergence would revert every request with nothing in the
repo to explain it.

**Fire-and-forget, so publishers confirm.** An instruction may never be delivered, and the
extension may reject it. `send-endpoints` and `send-admins` therefore ask the machine for its
signed readiness report until it names the hash they just published, and fail if it does not. Skipping that
(`-wait 0`) leaves the chain expecting one config and the machine running another — silently.

**`-dry-run` before spending gas.** Both publishers validate the payload against the extension's
real schema and print the hash without sending anything. Worth the extra command every time: a
rejected payload costs a transaction *and* leaves the machine unable to land prices.

---

## The lifecycle, in order

| Stage | Run | Doc |
| --- | --- | --- |
| 1. deploy the oracle contracts | upstream `deploy-tee-oracle.sh` + governance follow-ups | [deployment-steps.md](deployment-steps.md) |
| 2. build + start the containers | `full-setup.sh` → `start-services.sh` | |
| 3. register the machine on-chain | `post-build.sh` | |
| 4. publish endpoints + admins | `send-endpoints`, `send-admins` | [production setup](usdx-oracle-production-setup.md) |
| 5. install credentials | `upload-hex-key.sh`, `upload-rpc-keys.sh` | |
| 6. commission + run the feed | `keeper`, `oracle-cycle.sh` | [keeper](usdx-oracle-keeper.md) |
| 7. retire, replace or upgrade a machine | `pause`, `register-tee`, `addTeeVersion` | [lifecycle](usdx-oracle-machine-lifecycle.md) |

`full-setup.sh` pins the extension identity (the reserved id 1), starts the stack, and runs
post-build. Everything after stage 3 is deliberately manual: it moves credentials and money.

Stages 3, 5 and 7 repeat **per machine**. Stages 4 and 6 are fleet-wide: `send-endpoints` /
`send-admins` reach every machine active on the extension in one call (the sender reads that set itself), and one keeper serves them all. Stage 7 is
not exceptional — a rebuild mints a new enclave key, so replacing a machine is the ordinary
consequence of shipping a change, and the identity it replaces has to be paused or it stays
registered forever.

---

## Setup and deployment

| Tool | What it does |
| --- | --- |
| `use-chain.sh <chain>` | Copies `.env.<chain>` → `.env`, which every script auto-loads. Run it first, and again whenever you edit the chain's env. Coston has two rows: `coston` is the official USDX deployment under extension 1, `coston_local` a test deployment of our own on the same chain. |
| `import-upstream-addresses.sh <chain>` | Copies the sender + feed store addresses from a flare-smart-contracts-v2 deploy into `deploy/config/<chain>/deployed-addresses.json`, and refreshes the chain's system contracts and its indexer configuration. Re-run after every upstream deploy. On a chain this repository deploys itself, the oracle entries come from that deploy instead and are left out here. |
| `full-setup.sh` | Pins the extension identity (`EXTENSION_ID=1`, reserved), starts the stack, runs post-build — in one command. |
| `start-services.sh` | Builds the extension image and brings up the compose stack (extension-tee, proxy, redis). `--local` runs the host-process variant instead. |
| `post-build.sh` | `--proxy <url>` addresses one machine of a fleet. Whitelists the image's code hash (`allow-tee-version`), sets TEE governance (`set-governance`) and registers the machine (`register-tee`). |
| `stop-services.sh` | Retires the stack's machine on chain (`retire-tee -p`), then tears the stack down; `--keep-registered` skips the retirement. |

For a production deployment the oracle contracts (`TeeOracleInstructionsSender` +
`TeeOracleFeedStore`) are deployed upstream — `deploy-tee-oracle.sh` plus governance follow-ups
(`registerReserved`, the `TEE_ORACLE` fee rows, `addCustomFeeds`); see
[deployment-steps.md](deployment-steps.md). For the simulation and for a test deployment on a
testnet, `deploy-oracle.sh --chain <chain>` deploys the same contracts deployer-governed and
checks or registers the extension (deploy/README, "Testing on Coston"). After the upstream deploy,
`import-upstream-addresses.sh <chain>` slots the deployed addresses into
`deploy/config/<chain>/deployed-addresses.json` so the tools resolve them by name, and
`wire-extension` performs the extension-owner share (`setExtensionContracts`, machine-owner
allowlist, EVM key type) once governance has run `registerReserved`.

### The Go commands behind them

| Command | What it does | Key flags |
| --- | --- | --- |
| `wire-extension` | Wires a RESERVED extension after governance runs `registerReserved`: `setExtensionContracts` (the sender — required before `register-tee`'s availability check) and the machine-owner allowlist. The allowlisted address is the machines' owner, not the caller: `-machineOwner`, else `MACHINE_OWNER_KEY`'s address, else `INITIAL_OWNER`. Idempotent. | `-extensionId`, `-sender`, `-machineOwner` |
| `set-governance` | Publishes the TEE governance signer set + threshold from `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD`. **Must match what the TEE node was launched with**, or `register-tee` fails `InvalidGovernanceHash`. | |
| `allow-tee-version` | Whitelists a code hash + platform for the extension, read from the proxy's `/info`. Idempotent — skips if already active. | `-version` |
| `register-tee` | Registers the machine and takes it to `PRODUCTION`, in four resumable steps. Signs as the machine's initial owner, which the registry requires as the sender: `MACHINE_OWNER_KEY`, or the deployment key where they are one. It refuses before sending when the key is not the owner the machine reports. | `-command`, `-state`, `-resume` |
| `retire-tee` | Pauses a machine in the registry, the one-way retirement: `-p <proxy>` the machine behind a proxy (its id read from the attested `/info`), `-tee <id>` by id, `-stale` every active machine whose URL does not answer as itself — the leftovers of stopped stacks. Owner-only; idempotent on a paused machine. | `-p`, `-tee`, `-stale`, `-dry-run` |

`register-tee -command` is a string of step letters, run in order:

| Letter | Step |
| --- | --- |
| `r` | pre-register the machine on-chain |
| `R` | request a **fresh** attestation and challenge |
| `a` | run the FTDC availability check (data providers cosign) |
| `p` | promote to `PRODUCTION` |

issuing one once the machine is already registered, so every re-run (new image, retry) would
revert `ChallengeExpired`. `R` issues it unconditionally. Progress is written to the `-state`
file so a failed run resumes instead of repeating on-chain steps.

---

## Publishing configuration on-chain

Both take `-config <file>` (JSON, for operator convenience), build the ABI-encoded payload from
it, and target every machine named by `-tee` in one call. The sender assigns the version, records
the payload hash per (feed, machine), and dispatches one instruction per target. Neither carries
a secret — the payloads are public forever. On a production deployment `setEndpoints`/`setAdmins`
are governance-only — the sender is in production mode from the moment upstream's deploy script
finishes — so the normal path here is `-out <file>`: the tool predicts the version and payload
hash, resolves the fee, and writes the exact calldata to a JSON **hand-off** for the Flare
governance executor, who runs it through the timelock and attaches the fee at execution. The
direct path (no `-out`) sends the call itself and works only while the caller is still
governance — before `switchToProductionMode`, i.e. local and test deployments.

### `send-endpoints` — which RPC sources the machine reads

```bash
cd tools && go run ./cmd/send-endpoints -dry-run -feedId <FEED_ID> -config <ENDPOINTS_JSON>
```

The sender resolves from the `TeeOracleInstructionsSender` entry in the addresses file (or
`-instructionSender`). For the governance path, add `-out <file>` and hand the file over.

The JSON names each group (a tagged endpoint list), its quorum threshold, and each endpoint as
PUBLIC (`url`) or PRIVATE (`urlHash` + `secretRef`). The chain RPC groups are public; the
restricted `hex_cash` reserve endpoint is private, so its URL never lands in calldata. Values
never appear; only names and commitments.

What happens: the tool ABI-encodes `Endpoints{version, feedId, groups}`, predicts the version
(`endpointsVersion(feedId) + 1`) and the hash, and calls
`setEndpoints(feedId, teeIds, groups, claimBack)`. The sender writes
`latestEndpointsHash[feedId]` before dispatch and marks each named machine as sent it. The extension validates and applies it,
and from then on every update it signs carries that hash. With `-wait`, the tool polls until the
machine reports the matching `endpointsHash` — a new config hash decommissions the machine until
it lands an update under it.

Validation mirrors the extension's exactly: known groups only, every pinned chain present,
`1 ≤ threshold ≤ endpoints`, https only, `{secret}` and `secretRef` used together, `hex_api_key`
refused as a provider secret, and `hex_cash` required to be a single PRIVATE endpoint under
`hex_cash_url`.

### `send-admins` — who may install credentials in the machine

```bash
cd tools && go run ./cmd/send-admins -dry-run -feedId <FEED_ID> -config <ADMINS_JSON>
```

The JSON is two role-tagged signer sets: `backing` (governs `hex_api_key`, floor 1) and
`providers` (governs every other ref, floor 2). Addresses and thresholds only — private keys go
nowhere near this tool, and the operator does not pick the version (the contract assigns it).

What happens: identical shape to `send-endpoints` — `Admins{version, feedId, roles}` is encoded,
`latestAdminsHash[feedId]` written, the instruction dispatched, the machine confirmed.
The extension installs all roles or none, so a rejected half cannot leave it running a mixture of
old and new authorities.

**Which set governs which ref is pinned in the extension build and is not read from this
payload.** Otherwise whoever publishes admins could move a credential under whichever set they
hold, and the split would authorise nothing.

---

## Installing credentials

Never on-chain. The credential is encrypted to the enclave's attested key and delivered over
`POST /direct`. Full flow and who holds what: [production setup](usdx-oracle-production-setup.md).

| Tool | What it does |
| --- | --- |
| `verify-tee` | **Run before any credential.** Answers: is this proxy fronting an enclave running code we accept? |
| `usdxprov build` | Reads the attested key, encrypts the credential to it, emits an unsigned envelope. |
| `usdxprov sign` | One admin signs the envelope body. Refuses a duplicate signer — M-of-N needs distinct ones. |
| `usdxprov submit` | Combines signatures and POSTs to `/direct`. Must land within one hour of the build. |
| `upload-hex-key.sh` | The three above, for `hex_api_key`, gated on `verify-tee`. Backing set, so one signature can finish it. |
| `upload-rpc-keys.sh` | The same for every RPC ref the machine reports missing, with handoff when it cannot meet the providers threshold. |

**Why `verify-tee` is not optional.** The next step encrypts a credential to a public key the
proxy served; a proxy that lied about that key receives the plaintext. Reading a code hash from
that same proxy proves nothing — the same untrusted source supplied both halves. `verify-tee`
breaks the circularity against the chain:

```
/info → attestation → code hash        (parsed from the quote, not the JSON)
/info → public key  → teeId            (derived, not asserted)
chain → that teeId is registered, PRODUCTION, in extension E
chain → extension E accepts that code hash on that platform
you   → the code hash is the one you were told, out of band
```

Substitute the key and the derived `teeId` stops matching a registered machine; substitute the
attestation and the code hash stops matching what governance accepts. It exits non-zero on the
first failure so a script can gate on it. Without `-code-hash` it proves the machine is *some*
accepted machine, not that it runs your build — and it says so.

**Secrets come from the environment, never argv.** `usdxprov` reads `USDXPROV_VALUE`,
`USDXPROV_KEY` and `USDXPROV_API_KEY`, and warns if you use the equivalent flag: argv is readable
by any local process through `ps`. The upload scripts prompt hidden for anything unset.

---

## Running the feed

| Tool | What it does |
| --- | --- |
| `keeper` | The production process. Requests a feed update, polls every addressed machine for its signed result, relays the agreeing ones to the store in one batch, emits one JSON record per cycle. |
| `oracle-cycle.sh` | Wraps one keeper cycle against the deployed stack, loading `.env`. `-tee <a,b,c>`, `--dry-run`, `--loop 10m`. |

Every cycle requests on-chain through `requestFeedUpdate(feedId, teeIds)`, dispatching one
instruction to each machine named by `-tee`. Each machine answers on its own proxy, so the keeper
polls every named machine and submits the updates that agree on `observedAt` together — the store
wants `requiredSignatures` of them and takes the median. Before paying it asks those machines over `GET_READINESS`
whether they would sign at all; if none would, the cycle ends `request_skipped` and no fee is
spent.

| Flag | Effect |
| --- | --- |
| `-tee` | comma-separated target machine addresses (required); the fee scales with the count |
| `-store` | the `TeeOracleFeedStore` (default: the addresses-file entry); the sender is derived from it |
| `-feedId` | the feed to request (default USDX/USD) |
| `-interval` | how often a cycle starts (default 10m) |
| `-once` | a single cycle, for a cron-driven deployment |
| `-directApiKey` | key for the `GET_READINESS` preflight; without it every cycle pays unasked |
| `-records <file>` | append cycle records somewhere other than stdout |
| `-keeperId` | tells instances apart in the records — run at least two |

The keeper preflights the store's guards before spending gas, so a rejection arrives named —
`StaleEndpoints`, `StaleAdmins`, `NotNewer`, `TooFarAhead` — instead of as a bare revert.

---

## Inspecting

| Tool | Answers |
| --- | --- |
| `readiness` | Prints one machine's signed readiness report as JSON: the provisioning block, observation age, would-sign, counters and source health, with the signer. The scripts read a machine through it (`read_machine_readiness`, `lib/env.sh`); `-extension` asks a dev server directly. Needs the proxy's `DIRECT_API_KEY`. |
| `oracle-watch` | Polls one machine's signed readiness report on a timer, records every sample and prints the changes: source health, last error class, ready, would-sign, failed observations. The between-cycles view the keeper's records cannot give. |
| `fleet-status` | "Which machines are configured for the feed, and are they all running the same thing?" Registry status, `isTeeIdConfigured`, the sender's expected endpoints/admins hashes vs each machine's live readiness report (`-directApiKey`) — for every machine at once. `-store`/`-feedId`, `-json`. |
| `oracle-status.sh` | What the oracle publishes right now: the feed, the store, the registry, and the machine's readiness report. Reads only. |
| `oracle-observe.sh` | Runs the real observation against live sources with no chain, Docker or TEE. The highest-value local check: if this is wrong, everything downstream is. |
| `audit-deploy` | Sanity-checks `deployed-addresses.json` against a chain: code present, supported platforms and code hashes, and replays a failing `addTeeVersion` as `eth_call` to decode the custom error. |
| `verify-deploy` | Post-deploy assertions by stage: `-step register\|services\|tee-version\|tee-machine\|all`, `-checks D5,R2` to narrow, `-json` for machines. |

`fleet-status` is the one to reach for first — it is built around the three sources disagreeing.
A machine can be registered, sent a config it rejected, and still report `ready: true` while
running the *previous* config; the "config proven" column is what makes that visible without
trusting the machine's own account of itself. Do not pass `-p` to it: that overrides every
machine's registered URL.

Its table prints only **unhealthy** sources as notes, so a clean run says nothing about the
sources that are fine. For the full per-source matrix — including everything reporting `ok` —
use `-json` and read `sourceHealth`. Note that log lines share stdout with the JSON, so strip
anything before the first line that is exactly `[`.

### Environment overrides worth knowing

| Variable | Effect |
| --- | --- |
| `SIMULATED_TEE=true` | expected code hash and platform are the simulated constants rather than parsed from an attestation token |
| `SIMULATED_CODE_HASH` | overrides that expected hash, for addressing a fleet running two builds mid-upgrade. Validated as exactly 32 bytes, and **ignored unless `SIMULATED_TEE=true`** — under real attestation the hash comes from the token and must not be overridable |
| `EXTENSION_OWNER_KEY` | the extension owner's key, which every tool signs with unless it names another role's: the wiring, `addTeeVersion`, the governance set, the configuration publications, the keeper's fees |
| `MACHINE_OWNER_KEY` | signs the machine owner's calls: registration, the move to production, and pausing. The registry requires the machine's own initial owner as the sender, so this is needed wherever that address is not the deployer |
| `DIRECT_API_KEY` | the direct channel gate: what `fleet-status`, `send-endpoints`, `send-admins`, `oracle-watch`, `readiness` and the scripts ask a machine with (`GET_READINESS`) |

### When a transaction just says `execution reverted`

`register-tee` replays the failing call as an `eth_call` and decodes the revert against the custom
errors of Verification, Fdc2Hub, MachineManager and ExtensionManager — so prefer its output to the
raw error. It covers both the availability-check request and `toProduction`, which is where
promotion failures surface. `audit-deploy` does the same for `addTeeVersion`. The reverts you are
most likely to meet are tabulated in
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md).

---

## Local development

| Tool | What it does |
| --- | --- |
| `cmd/dev/devserver` | The extension with real provisioning and observation, no chain. |
| `cmd/dev/mocknode` | Stands in for tee-node: `/sign`, `/decrypt`, `/info`, key from a fixed seed. **Not secure, not for deployment.** |
| `cmd/dev/start-tee` | Host-process runner behind `start-services.sh --local`. |
| `set-config.sh endpoints\|admins` | Posts a `SET_ENDPOINTS` or `SET_ADMINS` payload straight to a running extension's `/action`, no chain. |
| `local-stack` | Runs the extension TEE and its proxy as host processes on the local simulation: the proxy in-process, `cmd/dev/start-tee` as a child. No Docker; see [local-e2e.md](local-e2e.md). |
| `full-setup-host.sh` | `full-setup.sh --chain local` with the stack as host processes: starts `local-stack` under `out/pids/`, then runs `post-build.sh` against it. |
| `deploy-oracle.sh --chain local` | Deploys upstream's oracle contracts on the simulation through `contracts/script/DeployTeeOracle.s.sol`, deployer-governed (no production mode), reserves extension 1 and imports the addresses. |
| `publish-local.sh` | Publishes endpoints or admin sets on the simulation: one transaction on a deployer-governed sender; record, real timelock and execute on a production-mode one. |
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
cannot help, because the siblings build's context root is `tee/`, not the repo.

## Build and housekeeping

| Tool | What it does |
| --- | --- |
| `generate-bindings.sh` | Compiles the TEE oracle interfaces out of the sibling flare-smart-contracts-v2 checkout (`contracts/`) and regenerates the Go bindings. **Run when upstream moves.** |
| `regen-wire-fixtures.sh` | Regenerates `deploy/testdata/wire/` from the Forge vector tests (the ABI ground truth both Go codecs assert against). |
| `check-versions.sh` | Fails when the dependency pins drift apart. |
| `go test ./...`, in the root and in `tools/` | A language implementation's unit tests, one module at a time. |
| `go test ./internal/extension/ -run TestConformance` | Replays the wire fixtures in `deploy/testdata/conformance` against the extension, no chain and no tee-node; the check behind [extension-contract.md](extension-contract.md). |

---

## Which extension binary runs where

| Binary | For |
| --- | --- |
| `cmd/docker` | **production** — what the Dockerfile builds (tee-node + extension in one process) |
| `cmd/` | `docker` (production entrypoint), `devserver` + `mocknode` (local dev), `start-tee` (host-process runner), `usdxprov` (provisioning CLI) |
| `cmd/dev/devserver` · `cmd/dev/mocknode` · `cmd/dev/start-tee` | local dev |
| `cmd/usdxprov` | ops: build / sign / submit a `SET_SECRET` envelope |

---

_Companion docs: [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (these in
order, for a live deployment) · [deployment-steps.md](deployment-steps.md) (getting a machine
registered) · [testing.md](testing.md) (the same ground with throwaway keys) ·
[usdx-oracle-codebase.md](usdx-oracle-codebase.md) (what the code does once an instruction
arrives) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the feed's reliability semantics)._
