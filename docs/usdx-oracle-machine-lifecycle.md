# USDX Oracle — Machine Lifecycle: Register, Pause, Replace, Upgrade

Everything that happens to a TEE machine *after* it is built and *before* it is provisioned:
getting it registered, keeping it in service, taking it out of service, replacing it, and moving
the fleet to a new build. The provisioning itself — endpoints, admins, credentials — is
[usdx-oracle-production-setup.md](usdx-oracle-production-setup.md); this doc is what surrounds it.

The one sentence to take away: **a machine's identity is its enclave key, and that key does not
survive a container restart.** Every rebuild, every `docker compose down`, every crash-and-recover
produces a machine the chain has never seen. Registration is therefore not a one-off setup step
but a routine part of deployment, and the old identity has to be retired deliberately or it
lingers forever.

Everything else follows from that. In particular there is **no in-place upgrade**: you cannot
pause a machine, change its code, and resume it. Upgrading is *replacement* — retire the old
identity, register the new one — and the fleet is what provides continuity while an individual
machine is being swapped. A consumer reading the feed cannot tell the difference, which is the
result that matters; see [Replacement *is* the upgrade
mechanism](#replacement-is-the-upgrade-mechanism) for exactly what carries over and what does not.

The fleet has a floor: the store takes a batch only from `requiredSignatures` distinct
`PRODUCTION` machines — **two** on Songbird, which runs three. Every procedure below is ordered so
that at least that many stay serving, and so that the keepers keep running throughout.

The `cast` commands use the variables `A`, `SENDER`, `TM` (the `FlareTeeManager`), `STORE`, `FEED`
and `RPC` set as in the production runbook's
[Conventions](usdx-oracle-production-setup.md#conventions), plus `EXT_ID`, the `EXTENSION_ID` (1 on
Songbird). `cast send` takes `--interactive` and prompts for the key.

---

## The states, and who moves a machine between them

`getTeeMachineStatus(teeId)` returns a `uint8`, upstream's `IMachineManager.TeeStatus`:

| Value | State | What it means |
| --- | --- | --- |
| 0 | `NONE` | Not registered. |
| 1 | `INITIALIZED` | Registered and attested, not yet promoted. Not dispatched to. |
| 2 | `PRODUCTION` | In the extension's active set: dispatched to, and the only status the store accepts. |
| 3 | `SUSPENDED` | Out of the active set because its availability check expired, or failed. |
| 4 | `PAUSED` | Out of the active set: its owner paused it, or its build was disabled. |
| 5 | `BANNED` | Out of the active set by the extension owner's decision. |

| Call | Who may | Transition |
| --- | --- | --- |
| `register` | the machine's initial owner, which must be on the extension's owner allowlist | `NONE` → `INITIALIZED` |
| `toProduction(proof)` | the owner from `INITIALIZED` or `PAUSED`; **anyone** from `SUSPENDED` | → `PRODUCTION`, and the availability check is extended |
| `pause` | the owner | `PRODUCTION` or `SUSPENDED` → `PAUSED` |
| `pause` | **anyone**, once the availability check has expired (not while the extension is emergency paused or in its grace period) | `PRODUCTION` → `SUSPENDED` |
| `pauseWithProof(proof)` | anyone holding a failed availability proof | `PRODUCTION` → `SUSPENDED` |
| `confirmAvailability(proof)` | anyone holding a fresh availability proof | stays `PRODUCTION`; the availability check is extended |
| `ban` / `unban` | the extension owner | `PRODUCTION`, `SUSPENDED` or `PAUSED` → `BANNED`; `BANNED` → `PAUSED` |
| `disableCodeHashPlatforms` | the extension owner | every active machine on that (code hash, platform) → `PAUSED`, in the same transaction |

`toProduction` and `confirmAvailability` both require the machine's (code hash, platform) to be
still allowed, so a machine on a disabled build can never return.

`TeeOracleFeedStore` delegates the signer check to `Fdc2Verification.verifyTeeSignature`, which
recovers the signer and requires it to be registered to the store's extension and in `PRODUCTION`
(`TeeMachineNotAvailable` otherwise), and also rejects while the extension is emergency paused.
That placement matters: **pausing is enforced on-chain, not by asking the machine to stop.** A
paused machine keeps running, keeps observing and keeps signing perfectly valid reports — it
simply cannot get one accepted. Verified directly: a paused machine produced a *newer* signed
observation than the one that had just been accepted, and the store refused it. A machine that has
been compromised cannot decline to be paused, which is the entire point.

Leaving `PRODUCTION` has a second effect, on the keepers. `requestFeedUpdate` dispatches to every
machine in the keeper's static `-tee` list, and the diamond rejects a target that is not
`PRODUCTION` (`TeeMachineNotAvailable`), which reverts the whole request — every cycle, until the
list is changed. **Take a machine out of every keeper's `-tee` list before it leaves
`PRODUCTION`**, and put a new one in only once it is configured and provisioned. The keeper runs as
a container built from `keeper.Dockerfile` (`build-keeper-image.sh`), so changing the list is a
restart — `docker stop -t 90`, then `docker run` with the new `-tee` — which is always safe:
cycles are stateless. Restart the keepers one at a time, so one is always running
([production runbook, step 5](usdx-oracle-production-setup.md#step-5--run-the-feed)).

Read the fleet as the registry sees it — the active set, so the honest answer to "what is actually
serving":

```bash
cd tools && go run ./cmd/fleet-status
```

---

## Availability expiry, and `SUSPENDED`

A promotion records that the machine is available until the proof's timestamp plus the chain's
`teeAvailabilityCheckValidityDurationSeconds`: **302400 s (3.5 days) on Songbird and Flare**, 21600
s (6 h) on Coston and Coston2 (`deployment/chain-config/<chain>.json` upstream; read it live with
`getSettings()`). From then on **anyone may call `pause(teeId)`**, which suspends the machine. A
suspended machine has left the active set: it is not dispatched to, the store rejects what it
signs, and a keeper naming it reverts every request. On Songbird two suspensions stop the feed.

Nothing extends the period on its own. `confirmAvailability(proof)` extends it without leaving
`PRODUCTION`, and `toProduction` extends it on the way back; **no tool in this repository calls
`confirmAvailability`**, and `register-tee` stops at a machine that is already in `PRODUCTION`. So
the operator has to:

1. **Watch the deadline** of every active machine, and alert well before it:

   ```bash
   cast call "$TM" "getAvailabilityCheckValidity(address)(uint64,uint32)" <TEE_ID> --rpc-url "$RPC"   # endTs, then the signing policy
   cast call "$TM" "getSettings()(uint256,uint256)" --rpc-url "$RPC"                                     # validity period, challenge validity
   ```

2. **Refresh one machine at a time, before its deadline**, by cycling it through `PAUSED`, which
   costs a few minutes out of service and nothing else:
   1. check that at least `requiredSignatures` other machines are `PRODUCTION`, `configured` and
      `ready` (`fleet-status`);
   2. take it out of every keeper's `-tee` list;
   3. pause it: `cd tools && go run ./cmd/retire-tee -tee <TEE_ID>`;
   4. promote it again — `register-tee` re-attests it, runs the availability check and calls
      `toProduction`, and the fresh proof restarts the period:
      `cd tools && go run ./cmd/register-tee -p <PROXY_URL> -ep <NORMAL_PROXY_URL>`
      ([Registering a machine](#registering-a-machine) explains the flags);
   5. put it back in the `-tee` lists.

   The process never stopped, so the machine keeps its key, its configuration and its
   credentials, and the sender's record of it is unchanged — unless a publication executed while
   it was out, in which case [push](usdx-oracle-production-setup.md#a-machine-that-joins-after-a-publication)
   it before step 5.

3. **Return a machine that was suspended** the same way, from step 4: `toProduction` from
   `SUSPENDED` is anyone's call, though `register-tee` still signs as the machine owner.

A suspended or paused machine whose process has stopped cannot come back at all: there is no key
left to answer the availability check with. Replace it.

---

## Registering a machine

`register-tee` runs up to four steps, selected by the letters in `-command` (default `rap`). They
are separable because each can fail independently and re-running the whole thing is sometimes
wrong.

| Letter | Step | Notes |
| --- | --- | --- |
| `r` | pre-registration | `register()`, which also issues the attestation challenge. For a machine already registered and not in `PRODUCTION` it requests a fresh attestation instead; for one in `PRODUCTION` the run ends there with nothing to do. |
| `R` | request TEE attestation | Reuses a challenge younger than `teeChallengeValidityDurationSeconds` (600 s on every chain), else mints a new one. |
| `a` | availability check | Asks the FTDC providers to confirm the machine answers at its registered URL; needs an unexpired challenge. |
| `p` | to production | Fetches the signed proof and calls `toProduction(proof)` — status becomes 2. |

The usual way to register a new machine is `post-build.sh`, which first allows the build
(`allow-tee-version`) and publishes the TEE governance set (`set-governance`) — each skipped when
the chain already has it — and then runs `register-tee` with the chain's normal (FTDC) proxy:

```bash
EXPECTED_CODE_HASH=<code hash> ./deploy/scripts/post-build.sh --proxy <PROXY_URL>
```

`set-governance` publishes `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD` (default: `INITIAL_OWNER`
alone, threshold 1) whenever they differ from the chain's, signed by the extension owner, so the
environment must carry the values the machines were launched with. To promote a machine that is
already registered, run `register-tee` alone, which has no such side effect.

By hand, from `tools/`:

```bash
cd tools && go run ./cmd/register-tee -p <PROXY_URL> -h <PUBLIC_PROXY_URL> -ep <NORMAL_PROXY_URL> -state ../deploy/config/register-tee-<name>.state
```

`-h` is the URL written on-chain by `register()` (default: `-p`), and it must be reachable **by the
FTDC data providers**, not just by you: the availability check is an inbound request. A machine
already registered keeps the URL it registered with. `-ep` is the chain's normal proxy, from
`deploy/scripts/lib/chains.sh` (Songbird's is `https://tee-proxy-songbird-1.flare.rocks`); its
default, `http://localhost:6662`, is the simulation's. `-state` records the completed steps (default `../deploy/config/register-tee.state`,
relative to `tools/`). It is deleted at the start of every run unless `-resume` is given, and
again when the run completes, so a run continues an interrupted one only with `-resume` — give
each machine its own file, or a resume picks up another machine's progress.

`register()` is accepted only from the machine's **initial owner** — the `INITIAL_OWNER` it was
launched with — and only if the extension owner has allowed that address as a machine owner
(`wire-extension`, from `MACHINE_OWNER_KEY`'s address, `INITIAL_OWNER` or `-machineOwner`).
`register-tee` signs with `MACHINE_OWNER_KEY` (else `EXTENSION_OWNER_KEY`) and refuses up front,
naming both addresses, when that key is not the owner the machine reports. On a live chain
`EXTENSION_OWNER_KEY` must be set too, even when `MACHINE_OWNER_KEY` signs.

`SIMULATED_TEE` must match the machine: `true` for a simulated build, or the tooling tries to parse
`magic_pass` as a JWT; `false` for a real one, which is every mainnet machine.

---

## Pausing a machine

The machine owner's action, one transaction:

```bash
cd tools && go run ./cmd/retire-tee -tee <TEE_ID>
```

`retire-tee` signs with `MACHINE_OWNER_KEY` (or the extension owner's key where they are one
address), checks the machine is that key's, and pauses a `PRODUCTION` or `SUSPENDED` machine; one
already `PAUSED` is reported and skipped. `-p <PROXY_URL>` pauses the machine behind a proxy, its
id read from `/info` while the proxy still answers, and `-stale [-dry-run]` sweeps every active
machine of the extension whose registered URL does not answer or answers as a different machine —
what a stopped or restarted stack leaves behind. The bare call is
`cast send "$TM" "pause(address)" <TEE_ID> --rpc-url "$RPC" --interactive` from the owner's key; on
a machine that is already paused it reverts `InvalidTeeStatus`.

`stop-services.sh` runs `retire-tee -p` before it takes a compose stack down (unless
`--keep-registered`), because the identity is the enclave key in the running process and nothing
can name the machine once it is gone. Do not pause that machine again by hand.

Pause when: the machine is being replaced, it is misbehaving, its host is being decommissioned, a
rebuild has orphaned its identity, or its availability check is due. Take it out of every
keeper's `-tee` list first.

For a machine that must not come back on its owner's say-so — a compromised one — the extension
owner bans it: `cast send "$TM" "ban(address)" <TEE_ID> --rpc-url "$RPC" --interactive` takes a
`PRODUCTION`, `SUSPENDED` or `PAUSED` machine to `BANNED`, and `unban(address)` returns it to
`PAUSED`. No tool wraps either.

### There is no `unpause`

The `MachineManager` ABI has `pause`, `pauseWithProof`, `ban` and `unban` — and no `unpause`. A
paused machine returns to service through `toProduction(proof)`, i.e. by proving availability
again, which is the right shape: coming back requires demonstrating you are actually there.

`register-tee` with its default `rap` does it
(`cd tools && go run ./cmd/register-tee -p <PROXY_URL> -ep <NORMAL_PROXY_URL>`): for a registered
machine that is not in `PRODUCTION`, `r` requests a fresh attestation and the run continues through
`a` and `p`. `-command ap` alone works only while the last challenge is under ten minutes old;
after that it fails with

```
diag: revert -> Verification.ChallengeExpired([1786801334])
```

Verified: status 4 → 2.

### Resuming only works if the process never stopped

`node.Initialize` calls `crypto.GenerateKey()` at start-up and never persists the result. The
identity is therefore held **only in the running process's memory**: it is not sealed to disk, not
derived from the image, and not recoverable. Redis holds the *published* machine data, not the key,
which is why a partial restart produces a machine whose `/info` disagrees with what it signs.

The consequence is worth stating on its own, because it is the question everyone asks:

> **A paused machine can be resumed only while its container is still running.
> Pause → upgrade → resume is not a sequence that exists.**

An upgrade means a new container, a new container means a new key, and a new key means a machine
the chain has never seen. The paused identity cannot come back — there is nothing left to
authenticate it with. So the real shape is not pause-upgrade-resume but:

```
register the new one  →  push and provision it  →  add it to the keepers  →  retire the old one
```

The old registration is retired permanently; service resumes on a *different* machine, which may
run on the same host.

Pause-and-resume is still worth having. It covers taking a suspect machine out of service while you
investigate and putting it back when it turns out to be fine, and it is how availability is
refreshed — as long as you do not restart it in between. Restart it and you are in the replace
path, whatever your intention was.

---

## Replacing a machine (the routine case)

For a single machine coming back after a crash, a host move, or any restart at all. **If you are
changing the code, use [the rolling upgrade runbook](#the-rolling-upgrade-step-by-step) instead** —
it is this same sequence, ordered across a fleet and with the version calls around it.

Any restart mints a new enclave key. Where the machine runs decides who stops and starts it: a
compose stack you run is `stop-services.sh` / `start-services.sh`, while a Confidential Space VM —
every mainnet machine — is the administrator's to stop and redeploy (deploy/README.md, "A remote
machine"). The sequence, and the order matters:

1. **Check the fleet can spare it.** At least `requiredSignatures` *other* machines must be
   `PRODUCTION`, `configured` and `ready` in `fleet-status`. If not, bring the replacement up on
   another host first and come back to step 2 once it serves ([below](#adding-before-removing)).
2. **Take the old machine out of every keeper's `-tee` list.**
3. **Pause the old identity while its proxy can still name it**:
   `cd tools && go run ./cmd/retire-tee -p <PROXY_URL>`. `stop-services.sh --chain <chain>` does
   this itself before it stops a compose stack. Left `PRODUCTION`, the old identity stays in the
   active set forever: every publication is dispatched and charged to it, and nothing can answer.
4. **Stop it completely, never a partial restart.** Restarting only `extension-tee` while redis
   survives leaves the published machine data pointing at an enclave key that no longer exists —
   `/info` reports one identity while the node signs as another. On a compose stack only
   `stop-services.sh` clears it.
5. **Start the new one**: `./deploy/scripts/start-services.sh --chain <chain>`, or the administrator
   redeploys the VM. Check `curl -s <PROXY_URL>/info | jq .machineData` reports a public key
   different from before.
6. **Register the new identity**: `./deploy/scripts/post-build.sh --proxy <PROXY_URL>`. It prints
   the new `teeId`, and the machine appears in `fleet-status`.
7. **Bring it to the feed's configuration with a push** — the endpoints, then the admin sets
   ([production runbook](usdx-oracle-production-setup.md#a-machine-that-joins-after-a-publication)).
   Not a new publication: the feed's configuration has not changed, only the machine.
8. **Provision it**: steps 3–4 of the production runbook, for this machine. All of it is
   memory-only and none of it survived the restart.
9. **Confirm**: `fleet-status` shows it `configured true`, `live` equal to `endpoints`, `ready yes`.
10. **Add the new `teeId` to every keeper's `-tee` list**, one keeper at a time, and check its next
    cycle record names the machine among its `teeIds`.

### The same-host constraint

The old and new identities share a registered URL, and the host can run only one of them, so on
the same host the old one is necessarily gone before the new one starts. Pause it first, as in
step 3: in practice, promotion of the new identity has failed while the old was still
`PRODUCTION` at the same URL — pre-registration succeeds, only `toProduction` fails.

### Adding before removing

With a spare host — a new VM with its own proxy URL — run steps 5–10 there first, and only then
steps 2–4 on the old machine. The fleet never drops below its size, and the same-host constraint
does not arise.

---

## Upgrading the code

A build is identified on-chain by its **code hash**, and an extension only accepts machines whose
code hash is on its allow-list. The calls that manage that list are the **extension owner's**
(`EXTENSION_OWNER_KEY`), not governance's.

Allow a build with `allow-tee-version`. It reads the code hash and platform from a machine running
the build — `-p`; a machine can be started, just not registered, before its build is allowed — and
refuses unless the hash equals `-codeHash` (default `EXPECTED_CODE_HASH`); a pair already allowed
is skipped:

```bash
cd tools && go run ./cmd/allow-tee-version -p <NEW_BUILD_PROXY_URL> -codeHash <NEW_CODE_HASH> -version v0.2.0
```

The bare call allows several platforms at once; a code hash can be added only once
(`VersionAlreadyExists`):

```bash
cast send "$TM" "addTeeVersion(uint256,bytes32,bytes32,bytes32[])" "$EXT_ID" $(cast format-bytes32-string "v0.2.0") <NEW_CODE_HASH> "[<PLATFORM>,…]" --rpc-url "$RPC" --interactive
```

Read the list back, and a hash's version and platforms:

```bash
cast call "$TM" "getSupportedCodeHashes(uint256)(bytes32[])" "$EXT_ID" --rpc-url "$RPC"
cast call "$TM" "getCodeHashInfo(uint256,bytes32)(bytes32,bytes32[])" "$EXT_ID" <CODE_HASH> --rpc-url "$RPC"
```

`isCodeHashPlatformSupported(extId, hash, platform)` and `isCodeHashPlatformDisabled(...)` answer
per pair, and `getTeeMachineWithAttestationData(teeId)` gives the code hash and platform a machine
registered with:

```bash
cast call "$TM" "getTeeMachineWithAttestationData(address)((address,address,string,bytes32,bytes32))" <TEE_ID> --rpc-url "$RPC"
```

Retiring a build is `disableCodeHashPlatforms(extId, codeHash, platforms)`, and it does two
things:

- **It pauses every active machine running that (code hash, platform), in the same
  transaction.** The facet deployed on Songbird does this. Disabling the build the fleet runs
  takes the whole fleet out of service at once.
- **It is permanent.** It sets a disabled flag; nothing clears it, and `addTeeVersion` refuses a
  hash already on the list. A machine on a disabled build can never return to `PRODUCTION`.

So it is the last step of an upgrade, never the first, and never a way to "neutralise" a hash some
machine still runs.

### Replacement *is* the upgrade mechanism

There is no in-place upgrade, and no way to carry a machine across one. Because the identity dies
with the process, upgrading a machine and replacing a machine are the same operation — the only
difference is whether the new container was built from new source. Nothing is lost by this, but be
clear about what carries over and what does not:

| Carries over | Does not |
| --- | --- |
| the extension id, the sender, the store, the feed | the `teeId` — a new key, every time |
| the published endpoint config and admin sets *(as on-chain facts, and in the publication logs)* | the machine's copy of them — memory-only, delivered to the new machine by a push |
| the store's latest value and its `observedAt` | the retiring machine's credentials — the new machine starts unprovisioned |
| consumers, who see one continuous feed | a place in the keepers' `-tee` lists — the new `teeId` has to be added |

So a consumer reading `getCurrentFeed()` cannot tell an upgrade happened, provided at least
`requiredSignatures` machines keep serving throughout. That is the result worth preserving, and
rolling one machine at a time is what preserves it.

### The rolling upgrade, step by step

Two code hashes are allowed to be live at once, which is what makes this safe. Do **one machine at a
time**, let the others carry the feed, and keep the keepers running throughout — they only ever
have their `-tee` lists edited.

**Once, before the first registration** — allow the new build, or registration fails
`VersionNotSupported`. `allow-tee-version` reads the hash from a machine running the new build, so
it runs once the first new machine is up (after step 4 below, or earlier on a spare host);
`post-build.sh` in step 5 does it for you, pinned by `EXPECTED_CODE_HASH`, and skips it for every
later machine. Confirm both hashes are then accepted:

```bash
cast call "$TM" "getSupportedCodeHashes(uint256)(bytes32[])" "$EXT_ID" --rpc-url "$RPC"
```

**Then, per machine:**

1. **Check the fleet can spare it**: at least `requiredSignatures` other machines `PRODUCTION`,
   `configured` and `ready` in `fleet-status`. On Songbird, with three machines and two required,
   there is exactly one to spare — so one at a time, and add before removing where a spare host
   exists.
2. **Take it out of every keeper's `-tee` list, and pause it** while its proxy still answers:
   `cd tools && go run ./cmd/retire-tee -p <PROXY_URL>`.
3. **Take it down completely and start the new build** — on a compose stack
   `./deploy/scripts/stop-services.sh --chain <chain>` (which also pauses, so step 2's pause is
   skipped as already done) and `./deploy/scripts/start-services.sh --chain <chain>`; on a
   Confidential Space VM, the administrator redeploys it with the new image. Never a partial
   restart.
4. **Check the new identity**: `curl -s <PROXY_URL>/info | jq .machineData` — the `codeHash` should
   be the new one, and the public key different from before.
5. **Register it**: `EXPECTED_CODE_HASH=<NEW_CODE_HASH> ./deploy/scripts/post-build.sh --proxy <PROXY_URL>`.
6. **Push and provision it** — the endpoints and admin sets by
   [push](usdx-oracle-production-setup.md#a-machine-that-joins-after-a-publication), then steps 3–4
   of the [production runbook](usdx-oracle-production-setup.md) for this machine.
7. **Confirm**: `fleet-status` shows it `configured true`, `live` equal to `endpoints` and
   `ready yes`, and `verify-tee -p <PROXY_URL> -codeHash <NEW_CODE_HASH>` passes.
8. **Add it to every keeper's `-tee` list**, one keeper at a time, and watch a cycle land with it
   among the record's `teeIds`.

Repeat 1–8 for the next machine. **Do not start the next one until the previous is landing prices**
— that is the whole point of rolling.

**Once, at the end** — when no active machine runs the old build
(`getTeeMachineWithAttestationData` for each `teeId` in `fleet-status`), disable it, with every
platform `getCodeHashInfo` lists for it:

```bash
cast send "$TM" "disableCodeHashPlatforms(uint256,bytes32,bytes32[])" "$EXT_ID" <OLD_CODE_HASH> "[<PLATFORM>,…]" --rpc-url "$RPC" --interactive
```

Any machine still on the old build is paused by this call, which is why the check comes first. It
is the step that makes the exercise an upgrade rather than an accumulation — nothing can register
under the old build again — and it is the one most easily forgotten because everything already
works without it.

**Rollback** is the same procedure with the hashes swapped, and it is available only while the old
hash is still enabled. Disabling it is irreversible, which is the reason it comes last and only
once the new build has served for long enough to trust.

### This cannot be completed with `SIMULATED_TEE=true`

Worth stating plainly, because everything up to the last step *looks* like it is working.

In simulated mode (`MODE=1`) the code hash is not a measurement. It is a constant compiled into
tee-node — `settings.TestCodeHash`, `0x194844cf417dde867073e5ab7199fa4d21fd82b5dbe2bdea8b3d7fc18d10fdc2` —
with no environment override, in the version this repository pins (v0.0.27-…) as in every earlier
one. Changing the extension's own source does not change it. It becomes a real measurement only
under `MODE=0`, where `attestation.go` reads it from the Confidential Space PKI token claims.

You can patch the constant in a local tee-node checkout and build with `USE_LOCAL_SIBLINGS=1`, and
the machine will genuinely report the new hash — and `register()` will genuinely accept it. But
promotion still fails:

```
diag: eth_call toProduction teeId=0x854E…  codeHash=0x194844cf…  platform=TEST_PLATFORM  status=0
diag: revert -> Verification.InvalidResponseData([])
```

The code hash in that proof is `0x194844cf…`, the canonical constant — **not** what the machine
reports. The proof is assembled by the FTDC data providers, who run their own stock build and
assert the canonical simulated hash for any simulated machine. Nothing on the extension side can
change what they sign.

So on a simulated fleet: `addTeeVersion`, registration under a new hash, `pause` and
`disableCodeHashPlatforms` are all testable and all work. **Promotion to `PRODUCTION` under a new
code hash requires real attestation.** Plan the first genuine upgrade rehearsal for Confidential
Space, and note what that implies — AMD SEV (`n2d`) or Intel TDX (`c3`) hardware, the image
published to a registry, and exactly one workload container per confidential VM, so the proxy and
redis have to live elsewhere.

### Addressing a mixed fleet from the tooling

Mid-upgrade the fleet runs two builds. Under real attestation every tool takes the code hash from
the machine — `verify-tee` from the registry's record of it — and the expected one is given per
invocation (`-codeHash`, `EXPECTED_CODE_HASH`). On a simulated fleet the tooling's expected hash is
tee-node's single constant, which makes it unusable against whichever half it does not match.
Override it per invocation:

```bash
SIMULATED_CODE_HASH=0x<64 hex> go run ./cmd/register-tee …
```

It is validated as exactly 32 bytes (a typo would otherwise be silently left-padded into a valid
looking hash for a build nobody runs) and it is honoured **only** when `SIMULATED_TEE=true`. Under
real attestation the hash comes from the token and nothing can override it — otherwise the flag
would be a way to tell the tooling to accept a machine running something else.

---

## Diagnosing a failed transition

`register-tee` decodes what it can: a `register()` revert against `MachineManager`'s custom
errors, and a failed availability-check request or `toProduction` against the custom errors of
`Verification`, `Fdc2Hub`, `MachineManager` and `ExtensionManager`, printed as `diag: revert -> …`.
Anything else arrives as raw revert data; `cast decode-error <data>` names it, or `cast sig
"<Error>()"` gives a selector to compare. The ones to expect:

| Revert | Cause | Fix |
| --- | --- | --- |
| `Verification.ChallengeExpired` | the attestation challenge is older than 600 s | re-run with `r` or `R` first — the default `rap` does |
| `Verification.InvalidResponseData` | proof's code hash ≠ the machine's registered hash | see the simulated-mode limit above |
| `VersionNotSupported` | the build's (code hash, platform) is not allowed, or is disabled | `allow-tee-version` first; a disabled pair cannot be allowed again |
| `OwnerNotAllowed` | the machine's initial owner is not on the extension's owner allowlist | `wire-extension` (`-machineOwner`) |
| `OnlyOwner` | the signing key is not the machine's owner | set `MACHINE_OWNER_KEY` (`register-tee` reports this before sending) |
| `InvalidTeeStatus` | the call does not apply to the machine's status — pausing a paused machine, promoting one in `PRODUCTION` | check `getTeeMachineStatus` |
| `AvailabilityCheckTimestampInvalid` | the proof predates the machine's last status change | re-run from `a` |
| `InvalidGovernanceHash` | `GOVERNANCE_SIGNERS` differ from what `set-governance` published | make the container env match the chain |
| `OnlyInstructionsSender` (`0x552fdce2`) | the sender dispatching is not the one the target machine's extension has registered — one from another deployment | check the addresses file against `getTeeExtensionInstructionsSender(extId)`; `wire-extension` sets it |

Re-running a failed registration with the default `rap` is safe: a machine already registered is
not registered again, and one already in `PRODUCTION` ends the run. Check where it stands first:

```bash
cast call "$TM" "getTeeMachineStatus(address)(uint8)" <TEE_ID> --rpc-url "$RPC"
```

---

## What has actually been exercised

Recorded because "documented" and "verified" are not the same thing, and the gaps here are the
useful part. Run on a three-machine Coston2 fleet, extension 66281, 15–16 Aug 2026, unless noted.

| Path | Status | Evidence |
| --- | --- | --- |
| register a machine | **verified** | six times across two generations |
| pause | **verified** | 2 → 4, three orphans plus two live machines |
| pause is enforced at the store | **verified** | paused machine signed a *newer* observation; the store refused it |
| fleet shrinks when a machine is paused | **verified** | `getActiveTeeMachines` and the keeper both reported 2 of 3 |
| resume a paused machine (`Rap`) | **verified** | 4 → 2, feed resumed |
| resume with `ap` after the challenge expired | **fails as designed** | `Verification.ChallengeExpired` |
| replace a machine (pause old, register new, re-provision) | **verified** | whole fleet once, single machine twice |
| `addTeeVersion`, two versions live at once | **verified** | old and new both `supported`, neither `disabled` |
| register under a **new** code hash | **verified** | machine reported `0xae19aebc…`, `register()` accepted it |
| **promote** under a new code hash | **blocked in simulated mode** | `Verification.InvalidResponseData` — see above |
| `disableCodeHashPlatforms` flags the pair | **verified** | `supported → false`, `disabled → true`, the entry kept |
| `disableCodeHashPlatforms` pauses active machines on the pair | **not exercised** | in upstream's `ExtensionManagerFacet` and in the facet deployed on Songbird; never triggered |
| expiry → `SUSPENDED` → `toProduction` | **not exercised** | — |
| push to a machine registered after a publication | **simulated** | `pushEndpoints` and `pushAdmins` as `eth_call` on Songbird, 2 Oct 2026, with the values decoded from the publication logs: both accepted |
| a full upgrade ending in a promoted new build | **not verified** | needs real attestation |

Two things are therefore documented but unproven end-to-end: **promotion under a new code hash**,
and consequently **a complete upgrade**. Everything either side of that step works. The table
predates the Songbird deployment, whose machines registered and promoted under real attestation
(`MODE=0`); none of the transitions above has been rehearsed there.

---

## Fleet notes

**Publications are per feed; credentials are per machine.** `setEndpoints` and `setAdmins` reach
every machine in the active set when they execute, and a machine that joins later gets them by
push. Every credential envelope is ECIES-sealed to one enclave's key, so three machines need three
of each.

Run everything from **one control node** rather than from each machine. It holds the operator's
keys — `DIRECT_API_KEY`, `MACHINE_OWNER_KEY`, `EXTENSION_OWNER_KEY` — and none of the admin keys,
which stay one per admin on their own machines. The machines themselves need nothing but their
image and their own proxy key, which matters when they publish a port to the internet.

The keeper targets **only its static `-tee` list**: it does not discover the fleet. The registry
supplies each machine's registered proxy URL, which is where its result is polled. Every
replacement therefore means editing the list of every keeper — the old `teeId` out before it
leaves `PRODUCTION`, the new one in once it is ready. Do not pass `-p` with more than one machine —
it overrides the registry URL for every machine, sending all traffic to one of them.

---

_Companion docs: [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (provisioning
a registered machine) · [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (what each command does) ·
[deployment-steps.md](deployment-steps.md) (first deployment) ·
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md) (what breaks the feed and how it
shows) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (running the feed)._
