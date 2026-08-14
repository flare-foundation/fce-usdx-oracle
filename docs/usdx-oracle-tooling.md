# USDX Oracle — Tooling Reference

What every script and command does, and what is actually happening when you run it.

Companion to [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md), which puts
these in order for a live deployment. This one is the index: reach for it when you know the
stage and need the tool, or know the tool and need what it touches.

Three families:

| Family | What it is | Language-neutral? |
| --- | --- | --- |
| `scripts/*.sh` | orchestration — sequences the Go tools and Docker | yes |
| `tools/cmd/*` | Go CLIs that talk to the chain and the proxy | **yes** — wire format only, no extension internals |
| `go/cmd/*` | the extension binaries, plus `usdxprov` | no — this is the Go implementation |

`tools/` never imports `go/internal`. It re-declares the wire layout instead, so it works
against an extension written in any language — and so a bug in one cannot hide a bug in the
other.

---

## Patterns you will see everywhere

Six mechanisms recur across the tools. Knowing them makes most flags self-explanatory.

**Flags common to nearly every `tools/cmd`.** Each falls back to an environment variable, which
`scripts/use-chain.sh` puts in `.env`:

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
proxy, authorised by `DIRECT_API_KEY` and, for credentials, by admin signatures. `SET_ENDPOINTS`
and `SET_ADMINS` are refused over direct — that would skip the owner check. `SET_SECRET` is
refused over instruction — that would put a ciphertext in permanent public storage.

**Fees are read, never hard-coded.** Anything that raises an on-chain instruction reads the
current fee from the registry, using the op type and command taken from the deployed contract's
own constants. A stale constant would revert every request with nothing in the repo to explain
it.

**Fire-and-forget, so publishers confirm.** An instruction may never be delivered, and the
extension may reject it. `send-endpoints` and `send-admins` therefore poll the machine's own
`/state` until it reports the hash they just published, and fail if it does not. Skipping that
(`-wait 0`) leaves the chain expecting one config and the machine running another — silently.

**`-dry-run` before spending gas.** Both publishers validate the payload against the extension's
real schema and print the hash without sending anything. Worth the extra command every time: a
rejected payload costs a transaction *and* leaves the machine unable to land prices.

---

## The lifecycle, in order

| Stage | Run | Doc |
| --- | --- | --- |
| 1. deploy contracts, register extension | `pre-build.sh` → `extension-setup.sh` | [deployment-steps.md](deployment-steps.md) |
| 2. build + start the containers | `start-services.sh` | |
| 3. register the machine on-chain | `post-build.sh` | |
| 4. prove the round trip | `test.sh` | |
| 5. publish endpoints + admins | `send-endpoints`, `send-admins` | [production setup](usdx-oracle-production-setup.md) |
| 6. install credentials | `upload-hex-key.sh`, `upload-rpc-keys.sh` | |
| 7. commission + run the feed | `keeper` | [keeper](usdx-oracle-keeper.md) |

`full-setup.sh` runs 1–4 end to end. Everything after that is deliberately manual: it moves
credentials and money.

---

## Setup and deployment

| Tool | What it does |
| --- | --- |
| `use-chain.sh <chain>` | Copies `.env.<chain>` → `.env`, which every script auto-loads. Run it first, and again whenever you edit the chain's env. |
| `pre-build.sh` | Deploys `InstructionSender` and registers the extension on-chain. Writes `EXTENSION_ID` and `INSTRUCTION_SENDER` to `config/extension.env`. |
| `extension-setup.sh` | Runs between pre-build and Docker: deploys `UsdxPriceStore` + `UsdxCustomFeed` via `deploy-store` and records their addresses. |
| `start-services.sh` | Builds the extension image and brings up the compose stack (extension-tee, proxy, redis). `--local` runs the host-process variant instead. |
| `post-build.sh` | Whitelists the image's code hash (`allow-tee-version`) and registers the machine (`register-tee`). |
| `extension-post-setup.sh` | Extension-specific hook after post-build, once the machine is live and registered. |
| `test.sh` | Sends instructions on-chain, polls for results, validates the round trip. Stops at the proxy — it proves the machine signed a valid report, and puts nothing on-chain. |
| `full-setup.sh` | pre-build → start → post-build → test, in one command. |
| `stop-services.sh` | Tears the stack down. |

### The Go commands behind them

| Command | What it does | Key flags |
| --- | --- | --- |
| `deploy-contract` | Deploys `InstructionSender`, checking the registry addresses have code first. | `-o` output file, `-preflight-only` |
| `deploy-store` | Deploys `UsdxPriceStore` and `UsdxCustomFeed` and wires them to the extension. | `-extensionId` `-instructionSender` `-decimals` `-minPrice` `-maxPrice` `-governance` `-feedName` |
| `register-extension` | Registers the extension against the sender, mints the `EXTENSION_ID`. | `-instructionSender` `-governanceHash` |
| `set-governance` | Publishes the TEE governance signer set + threshold from `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD`. **Must match what the TEE node was launched with**, or `register-tee` fails `InvalidGovernanceHash`. | |
| `allow-tee-version` | Whitelists a code hash + platform for the extension, read from the proxy's `/info`. Idempotent — skips if already active. | `-version` |
| `register-tee` | Registers the machine and takes it to `PRODUCTION`, in four resumable steps. | `-command`, `-state`, `-resume` |

`register-tee -command` is a string of step letters, run in order:

| Letter | Step |
| --- | --- |
| `r` | pre-register the machine on-chain |
| `R` | request a **fresh** attestation and challenge |
| `a` | run the FTDC availability check (data providers cosign) |
| `p` | promote to `PRODUCTION` |

Use `rRap`, not the default `rap`. The availability check needs a one-shot challenge; `r` skips
issuing one once the machine is already registered, so every re-run (new image, retry) would
revert `ChallengeExpired`. `R` issues it unconditionally. Progress is written to the `-state`
file so a failed run resumes instead of repeating on-chain steps.

---

## Publishing configuration on-chain

Both take `-config <file>`, deliver it **verbatim** as calldata, and record its keccak256 on the
sender so the store can check what a machine proves against what was published. Both address one
machine, chosen by `-p`. Neither carries a secret — the payloads are public forever.

### `send-endpoints` — which RPC sources the machine reads

```bash
cd tools && go run ./cmd/send-endpoints -dry-run -p <PROXY_URL> -instructionSender <SENDER> -config <ENDPOINTS_JSON>
```

The payload names each provider URL, its chain, the quorum threshold, and — for private
endpoints — the **ref** whose credential is substituted for `{secret}` at dial time. Values
never appear; only names.

What happens: `setEndpoints(teeId, cfg)` writes `expectedConfigHash[teeId]`, then dispatches the
instruction through the registry. The extension validates and applies it, and from then on every
report it signs carries that hash. The tool then polls until the machine reports the matching
`endpointsHash`, and tells you to re-commission — a new config hash decommissions the machine
until it lands a price under it.

Validation mirrors the extension's exactly: pinned chains only, every pinned chain present,
`1 ≤ threshold ≤ providers`, https only, `{secret}` and `secret` used together, and
`hex_api_key` refused as a provider secret.

### `send-admins` — who may install credentials in the machine

```bash
cd tools && go run ./cmd/send-admins -dry-run -p <PROXY_URL> -instructionSender <SENDER> -config <ADMINS_JSON>
```

The payload is a `version`, and two signer sets: `backing` (governs `hex_api_key`, floor 1) and
`providers` (governs every other ref, floor 2). Addresses and thresholds only — private keys go
nowhere near this tool.

What happens: identical shape to `send-endpoints` — `expectedAdminsHash[teeId]` is written, the
instruction dispatched, the machine confirmed. The extension installs both sets or neither, so a
rejected half cannot leave it running a mixture of old and new authorities.

**Which set governs which ref is pinned in the extension build and is not read from this
payload.** Otherwise whoever publishes admins could move a credential under whichever set they
hold, and the split would authorise nothing.

### `set-store-extension`

Repoints `UsdxPriceStore` at a new extension generation after a re-registration, so you do not
redeploy the store — which would reset the stored price and invalidate the custom-feed
registration. Governance-only, and both values move together: `-extensionId` and
`-instructionSender`.

---

## Installing credentials

Never on-chain. The credential is encrypted to the enclave's attested key and delivered over
`POST /direct`. Full flow and who holds what: [production setup](usdx-oracle-production-setup.md).

| Tool | What it does |
| --- | --- |
| `verify-tee` | **Run before any credential.** Answers: is this proxy fronting an enclave running code we accept? |
| `usdxprov build` | Reads the attested key, encrypts the credential to it, emits an unsigned envelope. |
| `usdxprov sign` | One admin signs the envelope body. Refuses a duplicate signer — M-of-N needs distinct ones. |
| `usdxprov submit` | Combines signatures and POSTs to `/direct`. Must land within 15 minutes of the build. |
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
| `keeper` | The production process. Requests a price, polls for the signed result, relays it to the store, emits one JSON record per cycle. |
| `submit-price` | One keeper cycle by hand, with the decoded report and what a consumer sees afterwards. |
| `oracle-cycle.sh` | Wraps both against the deployed stack, loading `.env`. `--dry-run`, `--loop 10m`, `--on-chain`. |

The keeper defaults to the **direct** channel — no registry fee, no trigger transaction — and
raises one on-chain instruction every `-onChainEvery` (2h) to keep the permissionless path
proven. Before its first cycle it **sweeps** the fleet: one direct request per registered
machine, submitted oldest-observation-first so all of them land and are commissioned. Then it
rotates over the machines, one per cycle.

| Flag | Effect |
| --- | --- |
| `-interval` | how often a cycle starts (default 10m) |
| `-onChainEvery` | how often a cycle goes on-chain instead (default 2h, `0` never) |
| `-direct=false` | every cycle on-chain; disables the sweep |
| `-sweep` | the startup sweep (default on; off under `-once`) |
| `-once` | a single cycle, for a cron-driven deployment |
| `-records <file>` | append cycle records somewhere other than stdout |
| `-keeperId` | tells instances apart in the records — run at least two |

`submit-price` is the same code path plus operator detail. `-direct -p <PROXY_URL>` addresses one
named machine, which is what commissions it; `-instructionId 0x…` relays a result you already
have, costing no fee; `-dry-run` runs every store guard and sends nothing.

Both preflight the store's guards before spending gas, so a rejection arrives named —
`StaleConfig`, `NotNewer`, `TooOld` — instead of as a bare revert.

---

## Inspecting

| Tool | Answers |
| --- | --- |
| `fleet-status` | "Which machines are configured, and are they all running the same thing?" Registry status, config sent vs proven, live state, commissioned — for every machine at once. `-json` for machines. |
| `oracle-status.sh` | What the oracle publishes right now: the feed, the store, the registry, and the machine's `/state`. Reads only. |
| `oracle-observe.sh` | Runs the real observation against live sources with no chain, Docker or TEE. The highest-value local check: if this is wrong, everything downstream is. |
| `query-tee` | Raw registry lookup — `-ext <id>` lists the active machines and their URLs. |
| `audit-deploy` | Sanity-checks `deployed-addresses.json` against a chain: code present, supported platforms and code hashes, and replays a failing `addTeeVersion` as `eth_call` to decode the custom error. |
| `verify-deploy` | Post-deploy assertions by stage: `-step deploy\|register\|services\|tee-version\|tee-machine\|test\|all`, `-checks D5,R2` to narrow, `-json` for machines. |
| `run-test` | Sets the extension id if needed, sends a `GET_PRICE` instruction, validates the result. What `test.sh` calls. |

`fleet-status` is the one to reach for first — it is built around the three sources disagreeing.
A machine can be registered, sent a config it rejected, and still report `ready: true` while
running the *previous* config; the "config proven" column is what makes that visible without
trusting the machine's own account of itself. Do not pass `-p` to it: that overrides every
machine's registered URL.

---

## Local development

| Tool | What it does |
| --- | --- |
| `go/cmd/devserver` | The extension with real provisioning and observation, no chain. |
| `go/cmd/mocknode` | Stands in for tee-node: `/sign`, `/decrypt`, `/info`, key from a fixed seed. **Not secure, not for deployment.** |
| `go/cmd/start-tee` | Host-process runner behind `start-services.sh --local`. |
| `set-endpoints.sh` | Posts a `SET_ENDPOINTS` payload straight to a running extension's `/action`, no chain. |
| `start-proxy` | Temporary: runs the extension proxy as a Go process. |
| `e2e.sh` | Temporary: manages background Go processes for local dev. |

The upload scripts take `--no-verify` for a mocknode, which has no attestation and no on-chain
registration. Never against a machine that will hold a real credential.

## Build and housekeeping

| Tool | What it does |
| --- | --- |
| `generate-bindings.sh` | Compiles the Solidity and regenerates the Go bindings. **Run after editing any contract.** |
| `build-node-base.sh` | Builds the shared tee-node base image, needed by non-Go language images. |
| `check-versions.sh` | Fails when the dependency pins drift apart. |
| `test-unit.sh` | A language implementation's unit tests. |
| `test-conformance.sh` | Verifies an implementation against [extension-contract.md](extension-contract.md). |
| `verify-contracts.sh` | Publishes contract source to the block explorer, so an address can be read rather than trusted. |

---

## Which extension binary runs where

| Binary | For |
| --- | --- |
| `go/cmd/docker` | **production** — what the Dockerfile builds (tee-node + extension in one process) |
| `go/cmd` | conformance harness only — stub mode, unsigned $1.00, no sources |
| `go/cmd/devserver` · `go/cmd/mocknode` · `go/cmd/start-tee` | local dev |
| `go/cmd/usdxprov` | ops: build / sign / submit a `SET_SECRET` envelope |

---

_Companion docs: [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (these in
order, for a live deployment) · [deployment-steps.md](deployment-steps.md) (getting a machine
registered) · [testing.md](testing.md) (the same ground with throwaway keys) ·
[usdx-oracle-codebase.md](usdx-oracle-codebase.md) (what the code does once an instruction
arrives) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the feed's reliability semantics)._
