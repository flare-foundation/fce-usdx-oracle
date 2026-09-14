# USDX Oracle — Machine Lifecycle: Register, Pause, Replace, Upgrade

Everything that happens to a TEE machine *after* it is built and *before* it is provisioned:
getting it registered, taking it out of service, replacing it, and moving the fleet to a new
build. The provisioning itself — endpoints, admins, credentials — is
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

---

## The states, and who enforces them

`MachineManager.getTeeMachineStatus(teeId)` returns a `uint8`:

| Value | State | What it means |
| --- | --- | --- |
| 1 | `REGISTERED` | Pre-registered and attested, but not yet serving. Not dispatched to. |
| 2 | `PRODUCTION` | Live. Dispatched to, and the only status the store accepts. |
| 4 | `PAUSED` | Retired. Still registered, never dispatched to, prices rejected. |

`TeeOracleFeedStore` delegates the check to `Fdc2Verification.verifyTeeSignature`, which
recovers the signer and requires it to be registered to the store's extension and in
`PRODUCTION` status (reverting otherwise), and also rejects while the extension is emergency
paused. That placement matters: **pausing is enforced on-chain, not by asking the machine to
stop.** A paused machine keeps
running, keeps observing and keeps signing perfectly valid reports — it simply cannot get one
accepted. Verified directly: a paused machine produced a *newer* signed observation than the one
that had just been accepted, and the store refused it with

```
UnknownSigner: machine 0xc551…6630 is in status 4, the store requires 2 (PRODUCTION)
```

A machine that has been compromised cannot decline to be paused, which is the entire point.

Read the fleet as the registry sees it — this excludes paused machines, so it is the honest answer
to "what is actually serving":

```
cd tools && go run ./cmd/fleet-status -a ../deploy/config/<chain>/deployed-addresses.json -c $CHAIN_URL
```

---

## Registering a machine

`register-tee` runs a four-step sequence, selected by the letters in `-command`. They are separable
because each can fail independently and re-running the whole thing is sometimes wrong.

| Letter | Step | Notes |
| --- | --- | --- |
| `r` | pre-registration | `register()`. Skipped with a fresh attestation request if the machine is already registered. |
| `R` | request TEE attestation | Issues a **fresh challenge**. |
| `a` | availability check | Asks the FTDC providers to confirm the machine answers, and returns a signed proof. |
| `p` | to production | `toProduction(proof)` — status becomes 2. |

The normal invocation is all four:

```
cd tools && go run ./cmd/register-tee -a ../deploy/config/coston2/deployed-addresses.json -c $CHAIN_URL -p <PROXY_URL> -h <PROXY_URL> -ep $NORMAL_PROXY_URL -command rRap -state ../config/register-tee-<name>.state
```

`-h` is the URL written on-chain, and it must be reachable **by the FTDC data providers**, not just
by you: the availability check is an inbound request. `-state` gives each machine its own resume
file; sharing one across machines makes a re-run resume the wrong machine's progress.

`SIMULATED_TEE=true` must be exported for a simulated build, or the tooling tries to parse
`magic_pass` as a JWT.

---

## Pausing a machine

Governance action, one transaction:

```
cast send <FlareTeeManager> "pause(address)" <TEE_ID> --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL
```

Authorisation is `NotOwnerOrPauser`, and the owner is the `_claimBackAddress` passed at
registration — which `fccutils.RegisterNode` sets to the deploying key. **Whoever registered a
machine can retire it.** There is also `ban()` / `unban()` for the stronger action.

`tools/cmd/retire-tee` wraps the call: `-p <proxy>` pauses the machine behind a proxy (its id
read from the attested `/info`), `-tee <id>` one by id, and `-stale` sweeps every active
machine of the extension whose registered URL does not answer or answers as a different
machine — what a stopped or restarted stack leaves behind. `stop-services.sh` runs the `-p`
form before it takes a stack down, because the identity is the enclave key in the running
process and nothing can name the machine once it is gone.

Pause when: the machine is being replaced, it is misbehaving, its host is being
decommissioned, or a rebuild has orphaned its identity.

### There is no `unpause`

The `MachineManager` ABI has `pause`, `pauseWithProof`, `ban` and `unban` — and no `unpause`. A
paused machine returns to service through `toProduction(proof)`, i.e. by proving availability
again, which is the right shape: coming back requires demonstrating you are actually there.

**Use `-command Rap`, not `ap`.** The attestation challenge issued at registration is one-shot, so
skipping `R` fails with

```
diag: revert -> Verification.ChallengeExpired([1786801334])
```

The `R` step mints a fresh challenge and the promotion then succeeds. Verified: status 4 → 2.

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
pause the old identity  →  register the new one  →  provision it  →  commission it
```

The old registration is retired permanently; service resumes on a *different* machine that happens
to run on the same host. Plan capacity accordingly: during a rolling upgrade the fleet is one
machine short, not one machine paused.

Pause-and-resume is still worth having. It covers taking a suspect machine out of service while you
investigate, and putting it back when it turns out to be fine — as long as you do not restart it in
between. Restart it and you are in the replace path, whatever your intention was.

---

## Replacing a machine (the routine case)

For a single machine coming back after a crash, a host move, or any restart at all. **If you are
changing the code, use [the rolling upgrade runbook](#the-rolling-upgrade-step-by-step) instead** —
it is this same sequence, ordered across a fleet and with the version calls around it.

Any restart mints a new enclave key. The sequence, and the order matters:

1. **Stop the keeper first.** `deploy/scripts/stop-services.sh` does *not* touch the systemd unit, and a
   keeper left running spends gas against a generation that no longer exists.
   `sudo systemctl disable --now usdx-keeper`
2. **Full `down`, never a partial restart.** Restarting only `extension-tee` while redis survives
   leaves the published machine data pointing at an enclave key that no longer exists — `/info`
   reports one identity while the node signs as another. Only `stop-services.sh` clears it.
3. Rebuild and start: `./deploy/scripts/start-services.sh --chain coston2`
4. Register the new identity (a **new** `-state` file).
5. **Pause the old identity.** Otherwise it stays `PRODUCTION` forever and on-chain instructions
   are dispatched to a machine that cannot answer.
6. Re-provision — steps 1–4 of the production runbook, per machine. All of it is memory-only.
7. Restart the keeper; the fleet re-commissions itself as cycles are dispatched to each machine.

### The same-host constraint

The old and new identities share a registered URL, and the new one **cannot be promoted while the
old is still `PRODUCTION`**. Pause the old one first. This is easy to miss because pre-registration
succeeds — only `toProduction` fails.

---

## Upgrading the code

A build is identified on-chain by its **code hash**, and an extension only accepts machines whose
attested hash is on its allow-list. Two calls manage that list, both governance-only:

```
cast send <FlareTeeManager> "addTeeVersion(uint256,bytes32,bytes32,bytes32[])" <EXT_ID> $(cast format-bytes32-string "v0.2.0") <NEW_CODE_HASH> "[<PLATFORM>]" --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL
```

```
cast send <FlareTeeManager> "disableCodeHashPlatforms(uint256,bytes32,bytes32[])" <EXT_ID> <OLD_CODE_HASH> "[<PLATFORM>]" --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL
```

Read the list back:

```
cast call <FlareTeeManager> "getSupportedCodeHashes(uint256)(bytes32[])" <EXT_ID> --rpc-url $CHAIN_URL
```

`isCodeHashPlatformSupported(extId, hash, platform)` and `isCodeHashPlatformDisabled(...)` answer
per pair. **Versions are additive and permanent** — `disableCodeHashPlatforms` flips a disabled
flag, it does not delete the entry. Adding a wrong hash is not reversible, only neutralisable.

### Replacement *is* the upgrade mechanism

There is no in-place upgrade, and no way to carry a machine across one. Because the identity dies
with the process, upgrading a machine and replacing a machine are the same operation — the only
difference is whether the new container was built from new source. Nothing is lost by this, but be
clear about what carries over and what does not:

| Carries over | Does not |
| --- | --- |
| the extension id, the sender, the store, the feed | the `teeId` — a new key, every time |
| the published endpoint config and admin set *(as on-chain facts)* | the machine's copy of them — memory-only, must be re-sent |
| the store's latest value and its `observedAt` | the retiring machine's readiness state — the new machine starts unprovisioned |
| consumers, who see one continuous feed | fleet capacity during the window — one short, not one paused |

So a consumer reading `getCurrentFeed()` cannot tell an upgrade happened, provided at least one
machine keeps serving throughout. That is the result worth preserving, and rolling one machine at a
time is what preserves it.

### The rolling upgrade, step by step

Two code hashes are allowed to be live at once, which is what makes this safe. Do **one machine at a
time** and let the others carry the feed.

**Once, before touching any machine** — allow the new build, or every registration below fails
`VersionNotSupported`:

```
cast send <FlareTeeManager> "addTeeVersion(uint256,bytes32,bytes32,bytes32[])" <EXT_ID> $(cast format-bytes32-string "v0.2.0") <NEW_CODE_HASH> "[<PLATFORM>]" --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL
```

Confirm both hashes are now accepted before going further:

```
cast call <FlareTeeManager> "getSupportedCodeHashes(uint256)(bytes32[])" <EXT_ID> --rpc-url $CHAIN_URL
```

**Then, per machine.** Note the keeper is stopped only once, for the whole rollout — it addresses
the fleet, not a machine, and a keeper running against a half-migrated fleet spends gas on
identities that no longer exist.

```
sudo systemctl disable --now usdx-keeper
```

1. **Take the machine down completely.** Never a partial restart: leaving redis up while
   `extension-tee` restarts leaves published machine data pointing at a dead key, so `/info`
   reports one identity while the node signs as another.
   `./deploy/scripts/stop-services.sh --chain coston2`
2. **Deploy the new build and start it.**
   `./deploy/scripts/start-services.sh --chain coston2`
3. **Record the new identity.** `curl -s <PROXY_URL>/info` — the `machineData.codeHash` should be
   the new one, and the public key different from before.
4. **Pause the old identity**, before registering the new one — they share a URL, and the new one
   cannot be promoted while the old is `PRODUCTION`:
   `cast send <FlareTeeManager> "pause(address)" <OLD_TEE_ID> --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL`
5. **Register the new identity**, with its own state file:
   `cd tools && go run ./cmd/register-tee -a ../deploy/config/coston2/deployed-addresses.json -c $CHAIN_URL -p <PROXY_URL> -h <PROXY_URL> -ep $NORMAL_PROXY_URL -command rRap -state ../config/register-tee-<name>-v2.state`
6. **Re-provision it** — steps 1–4 of the [production runbook](usdx-oracle-production-setup.md),
   addressed at this machine. All of it is memory-only and none of it survived the restart:
   `send-endpoints`, `send-admins`, then the RPC credentials and the Hex credential.
7. **Confirm** before moving on: `getActiveTeeMachines` lists the new id and not the old, and
   `fleet-status` shows `ready=yes` with the config proven.

Repeat 1–7 for the next machine. **Do not start the next one until the previous is landing prices**
— that is the whole point of rolling.

**Once, at the end.** Start the keeper and let ordinary cycles commission the migrated fleet —
each request is dispatched to every machine named in `-tee`, so every machine contributes:

```
sudo systemctl enable --now usdx-keeper
```

Then retire the old build, so nothing can register under it again:

```
cast send <FlareTeeManager> "disableCodeHashPlatforms(uint256,bytes32,bytes32[])" <EXT_ID> <OLD_CODE_HASH> "[<PLATFORM>]" --private-key $DEPLOYMENT_PRIVATE_KEY --rpc-url $CHAIN_URL
```

Machines already in `PRODUCTION` are unaffected by this; what it prevents is anything
*re-registering* under the old build. It is the step that makes the exercise an upgrade rather than
an accumulation, and it is the one most easily forgotten because everything already works without
it.

**Rollback** is the same procedure with the hashes swapped, and it is available only while the old
hash is still enabled — which is the reason step 6 is last rather than first.

### This cannot be completed with `SIMULATED_TEE=true`

Worth stating plainly, because everything up to the last step *looks* like it is working.

In simulated mode (`MODE=1`) the code hash is not a measurement. It is a constant compiled into
tee-node — `settings.TestCodeHash`, `0x194844cf417dde867073e5ab7199fa4d21fd82b5dbe2bdea8b3d7fc18d10fdc2` —
with no environment override, in every version through v0.0.24. Changing the extension's own source
does not change it. It becomes a real measurement only under `MODE=0`, where
`attestation.go` reads it from the Confidential Space PKI token claims.

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

Mid-upgrade the fleet runs two builds, and the tooling's expected hash is a single constant —
which makes it unusable against whichever half it does not match. Override it per invocation:

```
SIMULATED_CODE_HASH=0x<64 hex> go run ./cmd/register-tee …
```

It is validated as exactly 32 bytes (a typo would otherwise be silently left-padded into a valid
looking hash for a build nobody runs) and it is honoured **only** when `SIMULATED_TEE=true`. Under
real attestation the hash comes from the token and nothing can override it — otherwise the flag
would be a way to tell the tooling to accept a machine running something else.

---

## Diagnosing a failed transition

`register-tee` decodes reverts against every TEE facet's custom errors, so prefer its output to a
bare `execution reverted`. The ones seen in practice:

| Revert | Cause | Fix |
| --- | --- | --- |
| `Verification.ChallengeExpired` | promoting with a stale challenge | use `-command Rap` |
| `Verification.InvalidResponseData` | proof's code hash ≠ the machine's registered hash | see the simulated-mode limit above |
| `VersionNotSupported` | build's hash not on the allow-list, or disabled | `addTeeVersion` first |
| `InvalidGovernanceHash` | `GOVERNANCE_SIGNERS` differ from what `set-governance` published | make the container env match the chain |
| `0x552fdce2` | cross-extension addressing | you are using a sender from another generation |

If a promotion reverts with nothing decoded, the machine is usually still at status 1 — check with
`getTeeMachineStatus` before re-running, since `r` is skipped when already registered.

---

## What has actually been exercised

Recorded because "documented" and "verified" are not the same thing, and the gaps here are the
useful part. Run on a three-machine Coston2 fleet, extension 66281, 15–16 Aug 2026.

| Path | Status | Evidence |
| --- | --- | --- |
| register a machine | **verified** | six times across two generations |
| pause | **verified** | 2 → 4, three orphans plus two live machines |
| pause is enforced at the store | **verified** | paused machine signed a *newer* observation; store refused it `UnknownSigner … status 4` |
| fleet shrinks when a machine is paused | **verified** | `getActiveTeeMachines` and the keeper both reported 2 of 3 |
| resume a paused machine (`Rap`) | **verified** | 4 → 2, feed resumed |
| resume with `ap` | **fails as designed** | `Verification.ChallengeExpired` |
| replace a machine (pause old, register new, re-provision) | **verified** | whole fleet once, single machine twice |
| `addTeeVersion`, two versions live at once | **verified** | old and new both `supported`, neither `disabled` |
| register under a **new** code hash | **verified** | machine reported `0xae19aebc…`, `register()` accepted it |
| **promote** under a new code hash | **blocked in simulated mode** | `Verification.InvalidResponseData` — see below |
| `disableCodeHashPlatforms` | **verified** | `supported → false`, `disabled → true`, old hash untouched |
| a full upgrade ending in a promoted new build | **not verified** | needs real attestation |

Two things are therefore documented but unproven end-to-end: **promotion under a new code hash**,
and consequently **a complete upgrade**. Everything either side of that step works. Nothing here
has been exercised under `MODE=0` at all.

---

## Fleet notes

Provisioning is **per machine**: `send-endpoints` and `send-admins` are addressed to one `teeId`,
and every credential envelope is ECIES-sealed to one enclave's public key. Three machines means
three of everything. There is no fleet-wide broadcast.

Run the whole thing from **one control node** rather than from each machine. Only that node needs
the funded deployer key and the provisioning admin keys; the machines themselves need nothing but
Docker and their own proxy key, which matters when they publish a port to the internet.

The keeper discovers the fleet from the registry and reaches each machine at **its own registered
URL**. Do not pass `-p` with more than one machine — it overrides the registry URL for every
machine, sending all traffic to one of them.

---

_Companion docs: [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (provisioning
a registered machine) · [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (what each command does) ·
[deployment-steps.md](deployment-steps.md) (first deployment) ·
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md) (what breaks the feed and how it
shows) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (running the feed)._
