# USDX Oracle — Keeper Specification

**Status:** ✅ **BUILT** — the cycle (`tools/pkg/keeper`), the process (`tools/cmd/keeper`), the
by-hand wrapper (`deploy/scripts/oracle-cycle.sh`), the image (`keeper.Dockerfile`,
`deploy/scripts/build-keeper-image.sh`) and the records page (`tools/cmd/cycles-web`) ·
**Raised:** 2026-08-05

_The cycle lives in `tools/pkg/keeper`: it triggers, polls with a bounded deadline and
exponential backoff, treats pending as pending, decodes `data` into the signed feed update and
relays it. It checks every answer and the batch against the store's guards before spending gas,
so a rejection arrives named — `StaleEndpoints`, `NotNewer` — instead of as a bare revert.
`tools/cmd/keeper` wraps it in the timer; `deploy/scripts/oracle-cycle.sh` runs the same code
once by hand, so what an operator proves interactively is what runs unattended._

_Every cycle raises its request on-chain through `requestFeedUpdate(feedId, teeIds)`, naming
the target machines explicitly: by default the extension's active machines at the feed's latest
configuration, read from the chain every cycle, or a fixed `-tee` list. Each machine answers on its own
proxy; the updates that agree on `observedAt` go up together in one `submitFeedUpdates` call,
and each update's own signature says which machine produced it. Before paying the fee it asks the
machines over `GET_READINESS` whether they would sign at all — a fee spent on a machine with a
stale observation buys a predictable failure._

_**Not built:** the chain-age watcher, which by design must run outside this process's failure
domain (see [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md)), and a deployment running
two or more keepers. **Deliberately not implemented:** replace-by-fee (see
[Stuck transactions](#stuck-transactions))._

_Companion docs: architecture and flow in [usdx-oracle-spec.md](usdx-oracle-spec.md) §4 and WP5,
missed-update alerting in [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md)._

---

## Scope

The keeper is the only moving part between an instruction and a stored feed value. It does
three things, on a timer:

1. **Trigger** — raise `GET_FEED` through `requestFeedUpdate(feedId, teeIds)` on the
   `TeeOracleInstructionsSender` ("the sender").
2. **Collect** — poll each target machine's proxy for its `ActionResult`.
3. **Relay** — decode the signed payloads, keep the ones that agree on `observedAt`, and call
   `submitFeedUpdates()` on the `TeeOracleFeedStore` ("the store") with all of them.

Plus the keeper-side half of the alerting specified in `usdx-oracle-monitoring.md`: one record
per cycle.

That is the whole job. The functionality is simple; **the reliability semantics are the
substance of this document**, and they are where a straightforward implementation goes wrong.

### Non-goals

The keeper holds no authority. The store verifies each TEE signature through
`Fdc2Verification` against the TEE machine registry — the signer must be a registered
PRODUCTION machine on the store's extension — so a compromised keeper costs gas, not
correctness. Anyone may submit, and colliding submissions are harmless: `observedAt` must
strictly increase, so the store accepts at most one update per request timestamp and every later
submission at the same or an older stamp reverts `NotNewer`. It must therefore never:

- compute, derive, adjust or sanity-"fix" a price;
- cache a price, or resubmit an old result on a later cycle;
- submit anything from a `status != 1` result;
- hold a secret beyond its own hot-wallet key.

---

## The cycle

```
every T:                                  # single-flight — an overrun starts the next cycle at once
    fleet   = machineRegistry.getActiveTeeMachines(store.extensionId())  # proxy urls, re-read each tick
    targets = -tee if given, else fleet less sender.getEndpointsPushTargets(feedId)
                                        less sender.getAdminsPushTargets(feedId)    # re-read each tick
    proxies = dedupe([-p] + fleet.urls)

    # 0. preflight — ask before paying
    if every proxy answered GET_READINESS and none reports wouldSign:    # unknown means "try"
        emit(cycle, outcome = "request_skipped")                         # no fee spent
        continue

    # 1. trigger
    fee     = operationFees.getOperationFee(TEE_ORACLE, GET_FEED) × len(targets)
    tx      = sender.requestFeedUpdate{value: fee}(feedId, targets)
    receipt = waitMined(tx, deadline = T_request)
    id      = receipt.logs.first(FeedUpdateRequested).instructionId

    # 2. collect — every addressed machine files its own result on its own proxy
    results = poll(proxies, "/action/result/" + id.hex() + "?submissionTag=threshold",
                   deadline = T_poll, backoff = 1 s doubling to 16 s,
                   until    = len(targets) proxies answered with status in {0, 1})
    updates = [abi.decode(r.data, (FeedUpdate, Signature)) for r in results if r.status == 1]
    if not updates:
        emit(cycle, outcome = "extension_failed", errorClass = first status-0 log)   # fail closed
        continue

    # 3. relay — one submission, one signed update per contributing machine
    screened = [u for u in updates                        # each answer judged on its own:
                if signer(u) in targets                   # a failing one is dropped and named,
                and u.extensionId == store.extensionId()  # the rest go on
                and u.feedId == FEED_ID
                and u.endpointsHash == sender.latestEndpointsHash(feedId)
                and u.adminsHash == sender.latestAdminsHash(feedId)]
    batch = largest group of screened sharing one observedAt (newest on a tie), one per signer
    preflight(batch)                    # requiredSignatures, the 32 cap, the ratchet, then
                                        # eth_call submitFeedUpdates(batch)
    submit(store.submitFeedUpdates(batch), deadline = T_submit)
```

---

## Where each part lives

| Concern | Code |
| --- | --- |
| One cycle, trigger → collect → relay | `keeper.RunCycle` — `tools/pkg/keeper/cycle.go` |
| Trigger and collect: pays for the request, submits nothing | `keeper.Collect` — `tools/pkg/keeper/cycle.go` |
| Screening, batching, preflight and submit | `keeper.Relay` — `tools/pkg/keeper/cycle.go` |
| Store guards, named before gas is spent | `keeper.Screen`, `keeper.Preflight` — `tools/pkg/keeper/preflight.go` |
| Timer and fleet refresh | `keeper.Run`, `keeper.RunOnce` — `tools/pkg/keeper/keeper.go` |
| The readiness preflight | `keeper.wouldRefuse`, `keeper.QueryReadiness` — `tools/pkg/keeper/readiness.go` |
| Listing the fleet | `keeper.ActiveMachines`, `fleet` — `tools/pkg/keeper/fleet.go` |
| Cycle records and the outcome taxonomy | `keeper.Record`, `keeper.Outcome` — `tools/pkg/keeper/record.go` |
| Long-running process, signals, flags | `tools/cmd/keeper` |
| One cycle by hand, or a loop | `deploy/scripts/oracle-cycle.sh` (builds `tools/cmd/keeper`; `-once`, or `--loop <interval>`) |
| The container image | `keeper.Dockerfile`, `deploy/scripts/build-keeper-image.sh` |
| The records page | `tools/cmd/cycles-web` |

`RunCycle` never exits the process and returns a `Record` for every outcome, which is what
lets the same code serve a one-shot and a timer. It is `Collect` then `Relay`, split so a caller
can inspect the decoded updates before deciding to submit them. `-dry-run`
(`oracle-cycle.sh --dry-run`) runs everything but the submission: the request is sent and its
fee paid, the answers are collected, screened and rehearsed against the store, and the cycle
ends with the outcome `dry_run`. It costs a real request; it only saves the submission's gas.

Run it, from `tools/`:

```
go run ./cmd/keeper -tee 0x…,0x…,0x… -interval 10m -keeperId keeper-a
```

The store resolves from `-store` or the `TeeOracleFeedStore` entry in the addresses file, and
the instructions sender is derived from the store's own `instructionsSender()` unless given
explicitly with `-instructionSender`. `-records <file>` appends the JSON records somewhere other
than stdout.

`oracle-cycle.sh` takes `-tee` (optional, as for the keeper), `--dry-run`, `--loop <interval>` (stay up and cycle, instead of
`-once`) and `--records <file>`; `PROXY_OVERRIDE` in the environment becomes `-p`. It resolves
the store from the chain's addresses file and passes it as `-store`. It reads nothing back
after the cycle: the record is the result.

### `-once`

`-once` runs a single cycle, emits its record and exits, for a cron-driven deployment where the
process is what repeats. It exits **0** when the cycle advanced the feed (`accepted`,
`race_lost`) or stopped cleanly at `dry_run`, and **1** on every other outcome —
`request_skipped` included — and when a SIGTERM cuts the cycle short. A cron wrapper that alerts
on a non-zero exit therefore alerts on a skip; route on the record's `outcome` instead.

### Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `-tee` | read from the chain | Comma-separated TEE machine addresses to request from: the targets of every request, fixed for the life of the process. Omitted, the keeper picks them every cycle; see [the target list](#the-target-list). The fee scales with the count. |
| `-interval` | `10m` | How often a cycle starts. Match the source refresh; faster costs a fee per cycle and buys nothing. A cycle that runs longer is followed by the next at once. |
| `-pollTimeout` | `5m` | How long to wait for the machines' terminal `ActionResult`s. The wait ends early once as many proxies have answered as there are targets; a machine that never answers costs the whole timeout, and the cycle goes on with the answers it has. |
| `-requestTimeout` | `2m` | How long to wait for the `requestFeedUpdate` receipt. Cycles are single-flight, so this is the bound that keeps an unmined trigger from stopping the keeper rather than costing it one cycle. |
| `-submitTimeout` | `3m` | How long to wait for the `submitFeedUpdates` receipt. |
| `-minBalance` | `5` | Warn each cycle when the paying account falls below this many coins. `0` disables it. A keeper that runs dry keeps cycling and failing at the request, which reads like a chain fault rather than an empty wallet. |
| `-directApiKey` | `$DIRECT_API_KEY` | Key for the `GET_READINESS` preflight. Without it every cycle pays the fee unasked. Prefer the variable: a flag value shows in `ps`. |
| `-p` | `$EXT_PROXY_URL`, else `http://localhost:6674` | The proxy polled and asked first. Given explicitly, it overrides *every* machine's registered url (right only with one machine); otherwise each machine is reached at its own registered url and this one is asked in addition, and used alone when the registry lists none. `oracle-cycle.sh` passes it only when `PROXY_OVERRIDE` is set. |
| `-keeperId` | the hostname | Stable identifier for this instance, so records from two keepers can be told apart. |
| `-records` | stdout | Append the JSON cycle records to this file. |
| `-bondsLog` | off | At the start of every cycle, fetch the public Hex bonds endpoint the machines read (`current_reserves_amount`) and append its answer to this file, one JSON line per fetch: the keeper's request time, the HTTP status, the body exactly as received (`raw`, or `rawBase64` when it is not valid UTF-8) with its SHA-256 (`rawSha256`) and, when it is JSON, a parsed copy for `jq` (`response`), or the error. The machines keep nothing of what they read and Hex serves only the current figure, so this is the only history of the bonds input. Never affects the cycle. |
| `-once` | off | Run one cycle and exit; see above. |
| `-dry-run` | off | Stop after the rehearsal, before submitting. The request is still sent and paid for. |
| `-store` | the `TeeOracleFeedStore` entry of the addresses file | The store to submit to. |
| `-instructionSender` | the store's `instructionsSender()` | The sender to request through. |
| `-feedId` | the USDX/USD id (`pkg/protocol.FeedID`) | The feed named in the request. The extension serves only its pinned feed and refuses any other with `UnexpectedPayload`, and screening checks the answers against the pinned id whatever this says, so there is no reason to set it. |
| `-a` | `$ADDRESSES_FILE`, else `deploy/config/<chain>/deployed-addresses.json` | The addresses file. |
| `-c` | `$CHAIN_URL`, else the chain's public RPC | The chain node. |

`-directApiKey` is **not** required — the preflight is a saving, not a gate, and a cycle without
it simply requests. The keeper warns at startup when it is missing, because the symptom otherwise
is fees quietly spent on machines that were always going to refuse. The key reaches the proxy's
`/direct` endpoint, which on this extension carries `GET_READINESS` and `SET_SECRET`; a feed
update never goes that way.

---

## Step 1 — trigger

### Getting the instruction ID

`requestFeedUpdate(feedId, teeIds)` returns `bytes32 _instructionId` and emits it in
`FeedUpdateRequested(address indexed requester, bytes21 indexed feedId, bytes32 indexed
instructionId)`. Both exist because they serve different callers: the return value is for
contract callers, and **a transaction returns nothing to an off-chain caller**, so a keeper
reads the event.

The registry's own `TeeInstructionsSent` carries the same id, but the sender's own event is
preferred: parsing the registry's logs would couple every caller to the registry's event ABI,
and the sender's event also proves which feed was requested.

Scan the receipt for the event rather than indexing by position — the receipt also contains the
registry's logs, and a positional assumption breaks the moment the emit order changes. The
keeper fails the cycle loudly if no matching log is found, rather than proceeding with a
zero hash. `tools/pkg/utils/instructions.go` is the reference.

### The fee

`requestFeedUpdate()` is `payable`; `msg.value` is the instruction fee, and it is what bounds
spam on the permissionless path. Read the fee; never hard-code it:

```
fee = operationFees.getOperationFee(TEE_ORACLE, GET_FEED) × len(teeIds)
      ?: operationFees.getDefaultFee() × len(teeIds)
```

The fee is governance-settable, and a stale constant fails closed in the worst way: every
request reverts, no feed update is published, and nothing in the repo changed to explain it.
The op type and commands are file-level `bytes32 constant`s in the upstream
`ITeeOracleInstructionsSender` — shared by every deployed instance — so the lookup keys on
exactly the identifiers the instruction carries; the same constants are pinned in
`pkg/protocol`, and a mismatch prices the wrong row and the instruction is rejected for
an underpaid fee (`FeeTooLow`). Upstream's deployment leaves the `TEE_ORACLE` rows unset, so the
default fee is the normal state on a fresh deployment; the lookup falls back to it rather than
erroring.

The fee scales with the target count — one request dispatches to every named machine, so a
single-machine fee would underpay a fleet-wide request and revert.

### Named machines per request

`requestFeedUpdate(feedId, teeIds)` dispatches the request to **the machines the caller
names**, and the target list is what routes the cycle. Every named machine answers, and each result is
retrievable only from that machine's own proxy. Consequences the keeper must respect:

- **Poll every proxy, and wait for as many answers as there are targets.** The keeper polls
  `-p` and every registry-listed machine's url (`Config.ResultProxies`, re-read each tick),
  concurrently, each request bounded at 10 s, backing off from 1 s and doubling to 16 s. The
  round ends once as many proxies have answered as there are targets, or at `-pollTimeout`. The
  record's `proxy` is the first proxy *in that list order* (`-p` first, then the registry's
  order) that returned a terminal result — not the first in time — and is `-p` when none did.
- **`teeIds` come from the signatures, not the request.** The recovered signers are the only thing
  that says which machines actually produced the updates. An answer whose signature recovers to a
  machine the cycle did not ask is dropped before submission, under the guard `NotAddressed`.
  That is keeper policy, not a store rule: the store would accept a signature from any PRODUCTION
  machine on the extension, but a proxy must not be able to slip an unasked machine's answer into
  the round, and dropping it costs the honest answers nothing.
- **Every target must be requestable, or the whole request reverts.** The sender reverts
  `FeedNotConfigured` when the feed has no endpoints or no admin sets published at all, and
  `TeeIdNotConfigured` when any named machine is not at the feed's latest published version of
  both (`isTeeIdConfigured(feedId, teeId)`; `fleet-status` shows it per machine as
  `configured`). The diamond reverts `TeeMachineNotAvailable` when a target is not PRODUCTION,
  and the sender `TeeIdNotInExtension` (or the registry `TeeNotFound`) when the first target is
  not one of the extension's machines. One bad entry fails the request for all of them, every
  cycle: the outcome is `request_failed`, and the record's `detail` carries the revert data.
  These are the sender's and the diamond's custom errors, so they arrive as their 4-byte selector
  in hex: `TeeIdNotConfigured` `0x2e34f7b3`, `FeedNotConfigured` `0x7454c9a2`,
  `TeeMachineNotAvailable` `0xea552ba8`, `TeeIdNotInExtension` `0x7a1795e0`, `TeeNotFound`
  `0xceb05b68`, `FeeTooLow` `0x732f9413`, `EmergencyPauseActive(uint256)` `0x480cf216`.
- **A machine that registers after a publication is behind until it is pushed.**
  `setEndpoints(feedId, groups, claimBack)` and `setAdmins(feedId, roles, claimBack)` — Flare
  governance's timelocked calls on a live deployment — dispatch to the extension's active set as
  it stands when they execute, and record each of those machines at the new version. A machine
  registered later is brought up to date by the permissionless, payable
  `pushEndpoints(feedId, teeIds, groups)` / `pushAdmins(feedId, teeIds, roles)`, fed the payload
  from the `EndpointsPublished` / `AdminsPublished` event; `getEndpointsPushTargets` /
  `getAdminsPushTargets` list the machines that are behind. No tool in this repository wraps
  `push*`. Until it is pushed, a request naming the machine reverts `TeeIdNotConfigured`: a
  keeper that picks its own targets leaves it out until then, while a `-tee` list that names it
  stops every request.
- A failed cycle may mean one bad machine, not a broken data source. This is why
  `usdx-oracle-monitoring.md` requires cross-machine correlation before a single `status == 0`
  is allowed to page.
- Running ≥2 keepers means ≥2 independent requests per interval, each paying its own fee, which
  is redundancy at the keeper level. Requests whose blocks carry different timestamps produce
  different `observedAt` stamps: both land if the earlier-stamped batch is submitted first, and
  otherwise the earlier-stamped one reverts `NotNewer` and records `race_lost`. Two requests whose
  blocks share a timestamp share a stamp, and only the first submission carrying it lands. That
  is a feature, and it is the reason the wasted gas from losing a race is worth paying.

What several machines buy is set by the store's submission policy (see
[Threshold submissions](#threshold-submissions)). At `requiredSignatures = 1` any one PRODUCTION
machine on the extension can land a value alone, so a second machine covers the first being
down; it does not corroborate its number. From 2 up, a submission needs that many distinct
machines signing the same instant, the stored value is their median, and the spread bound
rejects a batch whose middle values disagree. Upstream's deployment configuration sets
`requiredSignatures` 2, `maxSpreadBIPS` 100 (1%) and `maxSpreadAbsolute` 0 for the USDX feed on
Coston and Songbird (1 on its scdev network, and the local `contracts/script/DeployTeeOracle.s.sol`
uses 1); governance changes them with `setSubmissionPolicy`. What a threshold does not defend
against: every machine takes its configuration from the same governance publication (findings
§15), so a hostile configuration pointing all of them at the same bad provider passes any
threshold. It defends against one machine being compromised or malfunctioning.

### One channel, and why

The proxy also exposes `POST <proxy>/direct`, which raises an action on **that** machine without
a fee or a transaction. `GET_FEED` is not served over it: it is instruction-only, and the
extension refuses a direct `GET_FEED` with `WrongActionType`
(`error: command arrived over the wrong action type`).

The saving would be real — at a 10-minute interval, paying the registry fee ~144 times a day
instead of never — and it would buy the one thing the on-chain path cannot give you, which is the
ability to address a machine without a transaction. What it costs is the property the on-chain
path exists for:

| | instruction (`requestFeedUpdate`) | direct (not offered for `GET_FEED`) |
| --- | --- | --- |
| Who can call it | anyone, by paying the instruction fee | whoever holds `DIRECT_API_KEY` |
| Cost per cycle | fee + trigger tx + submit tx | submit tx |
| Which machine serves it | the machines named in the request | the one behind the proxy URL |
| On the record | the request is a transaction | nothing on-chain |

A direct `GET_FEED` could not forge a price — the enclave signs only what it observed on its own
timer, and `submitFeedUpdates` verifies that signature against the TEE machine registry however
it was asked for. What it would do is hand the same signed update to anyone holding one API key,
for free and with no on-chain trace of the request. Running the feed that way means the
permissionless path is the one nobody uses, and a path nobody uses is a path nobody notices has
broken.

So there is one channel, and every cycle exercises it.

`/direct` itself stays enabled and stays keyed: `SET_SECRET` and `GET_READINESS` — both
commands under the `TEE_ORACLE` op type — have no on-chain counterpart and go through it.

### The target list

The registry's active list for the store's extension is re-read every tick. It always says
**where to look**: the proxies results are polled on and the machines the readiness preflight
asks. Who is **asked** depends on `-tee`:

- **Without `-tee`, the keeper picks the targets every tick**: the active machines, less those
  the sender's `getEndpointsPushTargets` / `getAdminsPushTargets` list as behind the feed's latest
  publication — the same per-machine records `requestFeedUpdate` checks, so the request it sends
  names no machine it would revert on. A machine that restarts under a new id, or is paused,
  suspended or banned, drops out at the next tick; a new machine is asked from the first tick
  after it is at the feed's latest configuration. Every change is logged. A machine that is
  configured but not yet holding its credentials is still asked, and its fee paid: it answers at
  once that it is not configured, and the other machines carry the round.
- **With `-tee`, the targets are fixed** for the life of the process. A machine that is
  replaced, paused, suspended or banned must be taken out of `-tee` and the keeper restarted, or
  every request reverts — `TeeMachineNotAvailable` for a machine that is not PRODUCTION, and the
  feed stops even though the remaining machines could carry it. A new machine is asked only once
  it is added.

A registry read that fails, or lists no reachable machine, keeps the previous lists, because an
empty one would leave only `-p` to poll and nobody to ask. Machines the registry lists **without
a URL are dropped**, with a warning — there is nowhere to poll for their results, so they are not
asked either.

### The readiness preflight

Before paying, the keeper asks over `GET_READINESS` whether the machines would sign right now.
It asks `-p` and every registry-listed machine's proxy, deduplicated — the same list it later
polls for results, which is the extension's whole active set rather than just the targets.
The cycle ends `request_skipped`, with no fee spent, only when **every** proxy asked answered
and **none** reports `wouldSign`: one willing machine is enough to make the request worth paying
for.

Every uncertainty resolves to "request anyway": no API key, a proxy that cannot be reached, a
refused or unverifiable report, a query that runs past its 15 s bound. One unreadable proxy is
enough. "Not known" is not "do not try", and a preflight that suppressed real requests would be
worse than the fee it saves. It has a consequence for the default: `-p` falls back to
`http://localhost:6674`, and on a host where nothing answers there, that one proxy is always
unreadable and the preflight never skips. Set `EXT_PROXY_URL` to a proxy that answers — one of
the machines' — for the skip to work; the `sourceHealth` it records works either way.

`wouldSign` is true when the machine's cached observation is within `MaxObservationAge`
(10 min) by its own clock and was taken under an installed admin set. It cannot foresee the
per-request clock check (`ClockBehindChain`, `MissingRequestTime`), which depends on the
request's own timestamp, so a machine that reports `wouldSign` can still refuse.

Every proxy is asked, concurrently, rather than stopping at the first that would sign. A
readiness query costs no fee and no gas, so asking them all costs one round trip's latency
instead of the slowest sum — and it is the only way the cycle record can say *which* machine saw
a source fail. Two machines read the same providers independently and routinely disagree about
one of them, so `sourceHealth` is a map per machine, keyed by the teeId the report's signature
recovers to:

```json
"sourceHealth": {
  "0x708f515f87da98E424c352030Cce4f9DdF0db5ab": { "rpc_flare": "ok", "rpc_flare_3": "lagging", "...": "ok" },
  "0x962d1D7264A2385d083B64a58eC18052470232bD": { "rpc_flare": "ok", "rpc_flare_3": "ok",      "...": "ok" }
}
```

The keys are positional: `rpc_<chain>` is the chain's verdict, `rpc_<chain>_<i>` each of its
providers in the published order (from 1), and `hex_bonds` / `hex_cash` the reserve sources. A
machine present with an empty map answered but has observed nothing yet; one that is absent
could not be asked at all, which is never the same as healthy.

---

## Threshold submissions

The store's submission policy (`getSubmissionPolicy()`, governance-set) says how many distinct
machines must sign one submission — `requiredSignatures`, 1 to 32 — and how far their values may
diverge (`maxSpreadBIPS` of the median plus `maxSpreadAbsolute`). One `submitFeedUpdates` call
carries one `SignedFeedUpdate{feedUpdate, signature}` per contributing machine; the store checks
the batch's shape, takes the median, judges the two values bracketing it against the bound,
stores the median, and names the machines whose value lay beyond the bound in a `FeedOutliers`
event. A one-element batch is judged by the same rules.

What that means for the keeper:

- **Every update in a batch must carry the same `observedAt`**, or the store reverts
  `ObservedAtMismatch`. The enclave signs the timestamp of the on-chain event that requested the
  update, which is how the store defines the field, so machines answering one request agree by
  construction. The keeper still groups the decoded updates by `observedAt` and submits the
  largest group (the newest on a tie): another keeper's request in a different block carries a
  different stamp, and an answer to it is left out rather than sinking this round.
- **Every answer is judged on its own before the batch is formed.** A signature that recovers
  to nobody (`BadSignature`) or to a machine the cycle did not ask (`NotAddressed`), a wrong
  extension or feed (`WrongExtensionId`, `WrongFeedId`), a configuration commitment behind the
  feed's latest publication (`StaleEndpoints`, `StaleAdmins`), a second answer from one machine
  (`DuplicateTeeId`), a stamp outside the chosen group (`ObservedAtMismatch`): each such answer is
  dropped and named in the record's `dropped` list, and the rest go on. One machine that missed
  a configuration push costs itself the round, not the fleet. A round with nothing left is
  rejected under the one guard everything failed, or `NoUsableUpdate`. A feed with nothing
  published (`NoEndpointsPublished`, `NoAdminsPublished`) or a sender that cannot be read
  (`SenderUnavailable`) rejects the whole round before any answer is judged.
- **Fewer agreeing machines than `requiredSignatures`** is a named preflight rejection,
  `NotEnoughSignatures`, before any gas is spent; more than 32 is `TooManySignatures`. The
  record's `teeIds` says who made the batch and `dropped` who answered but was left out.
- **The ratchet is checked against the store's `observedAt`** (`NotNewer`), and classified as
  in [Classifying the outcome](#classifying-the-outcome).
- **The preflight ends with the contract's own dry run.** An `eth_call` of `submitFeedUpdates`
  with the batch enforces every rule a real submission would — signatures and registry status,
  the pause, the spread bound — and a revert is decoded to its guard name when the error is one
  the store's ABI declares, so `SpreadTooBig`, `DecimalsSpreadTooBig` and `TooFarAhead` arrive
  named without the keeper reimplementing the aggregation. The signature checks run inside
  `Fdc2Verification`, whose errors are not in the store's ABI: `ExtensionEmergencyPaused`
  (`0x1b344be4`), `InvalidTeeMachineExtensionId` (`0xfec67cd1`), `TeeMachineNotAvailable`
  (`0xea552ba8`) and OpenZeppelin's `ECDSAInvalidSignature` (`0xf645eedf`),
  `ECDSAInvalidSignatureLength` (`0xfce698f7`), `ECDSAInvalidSignatureS` (`0xd78bce0c`) arrive as
  guard `Reverted`, with the revert data in hex in the record's `detail`.
- **A machine that never answers costs the poll timeout, not the cycle.** The round waits for
  as many answers as there are targets until `-pollTimeout`, then goes on with what arrived.

The keeper-side names — `BadSignature`, `NotAddressed`, `NoUsableUpdate`, `SenderUnavailable`,
`Reverted` — are the keeper's own and appear in no contract; every other guard is the store's
error of that name.

---

## Commissioning a machine

A machine is *commissioned* once it has signed one feed update the store accepted — its teeId
appears in a `FeedUpdated` event. The store keeps no per-machine bookkeeping: one global
`observedAt` stamp is all it holds, so commissioning is an event-log fact, not a storage one.
This is deliberately lean: `submitFeedUpdates` already proves both facts it needs — it verified
the signature through the machine registry, and it checked the signer's configuration
commitments against the published ones — so a separate attestation entry point would be more
machinery for something that falls out of the normal one.

`requestFeedUpdate(feedId, teeIds)` takes the target list, so commissioning a particular machine
is a matter of asking it: a keeper that picks its own targets does so from the first cycle after
the machine is at the feed's latest configuration, and a fixed list does once the machine is
named in `-tee`. `isTeeIdConfigured(feedId, teeId)` is the sender's check that
the machine is at the feed's latest published endpoints and admin sets, and it is the
precondition `requestFeedUpdate` enforces per target — a machine without configuration cannot
even be asked, which is what makes commissioning state visible before any fee is spent.

Order matters when bringing a machine up, because each step is what makes the next possible:

1. Register the machine and get it to `PRODUCTION` in the TEE machine registry —
   `post-build.sh` runs `allow-tee-version` → `set-governance` → `register-tee`.
2. Get the feed's endpoint configuration to it. A publication (`setEndpoints`, through
   `send-endpoints`; on a live deployment the governance hand-off it exports with `-out`)
   reaches every machine active when it executes; a machine registered after the latest one
   needs `pushEndpoints` (see [Named machines per request](#named-machines-per-request)).
3. The same for the admin sets (`setAdmins`, `send-admins`, `pushAdmins`). Both kinds are
   required: the sender will not request from a machine that is behind on either
   (`TeeIdNotConfigured`), and the enclave refuses to sign without an admin set (`NotConfigured`).
4. `upload-hex-key.sh` and `upload-rpc-keys.sh` — deposit its credentials. Two uploads under
   two admin sets: the backing set governs `hex_api_key` and `hex_cash_url` (the cash URL whose
   commitment the endpoint configuration carries), the providers set governs the RPC
   credentials. Where a set's threshold needs more than the signers at hand, the scripts stop and
   point to `--build-only`, so the other admins can sign. Each machine has its own TEE key, so
   envelopes are per machine and cannot be reused.
5. Confirm `configured` and `ready` in `fleet-status`, which reads the sender's commitments
   on-chain and asks each machine directly over `GET_READINESS`, and so does not depend on it
   having landed anything. `go run ./cmd/readiness -p <proxy>` shows what `fleet-status` does
   not: `.readiness.missingSecretRefs`, which must be empty.
6. A keeper that picks its own targets is already asking the machine; one with a fixed `-tee`
   needs it added and a restart. `oracle-cycle.sh -tee <machine>` drives one cycle against it
   alone. Its first accepted update is its commissioning.

Two things to expect at step 6:

- **`race_lost` beside another keeper.** `observedAt` must strictly increase, and every update
  carries its request's block timestamp. With another keeper running — or a cycle by hand next to
  the unattended one — the store may already hold a later stamp when this cycle's batch arrives;
  the keeper reads the store again, finds it at or past its own, and records `race_lost`. The
  feed advanced; there is nothing to do.
- **A new publication strands every machine on the old one until it applies the new.** The
  sender's `latestEndpointsHash` moves the moment `setEndpoints` executes, so an answer computed
  under the previous configuration is dropped as `StaleEndpoints` until the machine catches up.
  `send-endpoints` checks delivery and says so on success; `fleet-status` reconciles the fleet.

For liveness, ask each machine over `GET_READINESS` — which is what `fleet-status` does.

---

## Step 2 — collect

Poll `GET <proxy>/action/result/<instructionId>?submissionTag=threshold` until the status is
terminal. `threshold` is the tag an instruction's result is filed under, and it happens to be the
endpoint's default — but pass it explicitly: a readiness query files its result under `submit`,
and asking for the wrong one 404s for the whole poll window rather than erroring.

| `status` | Meaning | Keeper action |
| --- | --- | --- |
| `1` | Success | Decode it; it goes to screening in step 3 |
| `0` | That machine refused; `log` is `error: <reason>` | Leave that machine's answer out. The first such `log` becomes the record's `errorClass`; the other machines' answers go on. Only when no machine produced a usable update does the cycle end `extension_failed` |
| anything else | Still in progress | Keep polling until `T_poll` expires. tee-node also files status `3` when the extension misses its deadline; it is not terminal to the keeper, so that machine costs the poll timeout |

Three properties the keeper's poll (`pollResults`, `direct.Await`) has and the test helper
`fccutils.ActionResult` does not:

- **A bounded deadline, not a fixed repeat count.** The helper sleeps 2 s a fixed number of
  times. A keeper needs a wall-clock deadline (`T_poll`) so the cycle's total duration is
  known and bounded — see the budget below.
- **Exponential backoff.** 1 s, doubling to 16 s, capped at what is left of the deadline. A
  tight fixed interval against the proxy for the whole poll window is wasteful and, with ≥2
  keepers, doubles for no benefit.
- **Pending is not an error.** The helper retries on non-200 but has no concept of a *pending*
  result, which returns 200 with a non-terminal status. Treating pending as terminal would
  discard results that were merely slow.

A poll that expires without any terminal status is cause 3 in the monitoring taxonomy — TEE
down, proxy down, or instruction dropped. FCC is fire-and-forget: there is no delivery
guarantee, so this is an expected outcome, not an exceptional one. It is recorded as `no_result`
and ends the cycle.

---

## Step 3 — relay

### Decode before submitting

`ActionResult` has **no `signature` field**. The extension returns:

```
update = FeedUpdate struct                  # 7 static words, 224 bytes
data   = abi.encode(update, signature)      # ActionResult.data — 10 words, 320 bytes
```

so the keeper ABI-decodes `result.data` into the update and its signature and hands them on as
one `SignedFeedUpdate{feedUpdate, signature}` element of `submitFeedUpdates`. `feedupdate.Decode` in
`tools/pkg/feedupdate` is exactly this: it decodes through the extension's own
`pkg/wire/feedupdate`, so there is one layout, and adds what the enclave never does with its
own output, recovering the machine that signed. The layout is pinned to Solidity's bytes in
`deploy/testdata/wire`.

Every `FeedUpdate` field is static, so its ABI encoding is canonical — exactly one valid byte
string per set of values — and the store itself re-encodes from calldata, so the keeper may
reshape the decoded update into calldata. The verbatim rule binds for the dynamic
`SET_ENDPOINTS` and `SET_ADMINS` payloads, which must never be re-serialised by a relayer.

### Classifying the outcome

A revert is not necessarily a failure. `NotNewer` is the expected result for a keeper whose batch
was overtaken: the store accepts at most one update per request timestamp, and only a strictly
newer one. The full revert-classification table lives in `usdx-oracle-monitoring.md`
§"Cause 6"; the keeper's obligation is to distinguish, not to alert.

Every `NotNewer` — from the preflight's ratchet check, from the rehearsal, at gas estimation, or
from a submission that was mined and reverted — is settled by reading the store's `observedAt`
again:

```
if guard == NotNewer:
    stored = store.observedAt()                # read again, after the refusal
    if stored >= batch.observedAt:
        outcome = race_lost                    # the feed is at or past ours: advanced, never alert
    else:
        outcome = rejected, guard NotNewer     # the ratchet only moves forward, so only a
                                               # lagging node or a failed read gets here
```

A submission that is **mined and reverts** is replayed at its block to name the guard, and
settled the same way: `race_lost` when the store now holds an `observedAt` at least as new as
the batch's, whatever guard the replay names; otherwise `rejected` under that guard, or under
`Reverted` when the replay names none. `not_mined` means only that no receipt arrived within
`-submitTimeout`.

---

## Reliability model

### The keeper is stateless, and that is the reliability argument

A keeper that crashes mid-cycle needs no recovery, no journal and no resumption, because
**every partial cycle is already harmless**:

| Crash point | Consequence | Recovery needed |
| --- | --- | --- |
| After the request tx | Instruction is dispatched; TEE computes; result sits in the proxy unread | None — one wasted fee |
| After polling, before submit | A signed result is discarded | None — the co-keeper's own cycle, or this keeper's next, publishes a fresh one |
| After submit is broadcast | Tx lands or does not; either way the store's monotonicity check settles it | None |

Nothing in the cycle is a two-phase commit. Nothing needs to be exactly-once. The next cycle
supersedes the last one, and `observedAt` monotonicity makes duplicate submissions safe by
construction.

**Do not add persistence to make the keeper "more reliable".** A write-ahead log or a resumable
cycle adds a corruptible dependency and buys nothing that the ≥2-instance design and the next
tick do not already provide. The one thing the keeper does persist is its **emitted cycle
records** — write-only, for monitoring, never read back to make a decision.

### Single-flight

A cycle never overlaps its successor. Cycles run one after another: the wait before the next is
the interval less the cycle's own duration, and a cycle that runs past the interval is followed
by the next **at once** — the missed tick is not queued, and two cycles never run concurrently.
Overlapping cycles would produce two in-flight instructions from one keeper, two
nonce-consuming transactions, and a real chance of submitting updates out of order — burning
fees to accomplish nothing, since the store accepts only the newer one.

### The deadline budget is a liveness budget, not an acceptance one

The upstream store has no acceptance window — no maximum-age bound, no minimum-age bound. Its
only time rules are that `observedAt` strictly increases (`NotNewer`) and is strictly older than
the accepting block (`TooFarAhead`). `observedAt` is the block timestamp of the instruction that
requested the update, always in the past by the time an answer can be submitted, so
`TooFarAhead` is unreachable for an honestly produced update. A slow cycle therefore does not
revert on age; it lands, or loses to a later-stamped update that landed first. `T_poll` and
`T_submit` bound liveness, not acceptability: keep them tight so a cycle lands before the next
one supersedes it, not because the store will reject a late one.

### Stuck transactions

A submission that misses its window is not worth chasing — the next cycle supersedes it. So the
implementation waits for the receipt until `-submitTimeout`, records `not_mined`, and gives up.

**Replace-by-fee is deliberately not implemented.** Bumping a transaction that is about to be
superseded buys nothing, and the bounded-ceiling machinery would be state the keeper otherwise
does not have.

What a stuck transaction *does* cost is the nonce behind it: a looping keeper eventually queues
every later cycle behind an unmined one, which a one-shot never sees. The cycle therefore
records `pendingNonceGap` — the distance between the mined and pending nonces — and the loop
logs it separately, because that failure degrades into a total outage rather than a bad cycle.
It is reported, not retried into: the fix is operational.

---

## Configuration

| Key | Notes |
| --- | --- |
| `T` | Cycle interval, `-interval`, default 10 min to match the source refresh; polling faster costs a fee and gas per cycle and buys nothing. |
| `T_request`, `T_poll`, `T_submit` | Per-step deadlines (`-requestTimeout` 2 min, `-pollTimeout` 5 min, `-submitTimeout` 3 min). Liveness bounds, not acceptance bounds — see the deadline budget above. |
| Smaller bounds | Each proxy request 10 s; each readiness query 15 s; each chain-node request 30 s; result-poll backoff 1 s doubling to 16 s. |
| Instruction fee | Read from `OperationFees` (`TEE_ORACLE`, `GET_FEED`), not hard-coded; scales with the target count. See step 1. |
| Feed id | `0x21555344582f555344000000000000000000000000` — category 0x21 + "USDX/USD", 21 bytes, pinned in `pkg/protocol`. Named in the request and inside the signed update; a mismatch with the store reverts. |
| `TeeOracleInstructionsSender`, `TeeOracleFeedStore` addresses | From `deploy/config/<chain>/deployed-addresses.json`; the keeper resolves the store entry and derives the sender from the store's `instructionsSender()`. |
| Target machines | Named explicitly in every request: by default the extension's active machines at the feed's latest configuration, re-read every cycle; with `-tee`, that fixed list. |
| Proxy URLs | Read from the registry per machine, for `/action/result/<id>` and the readiness preflight. `EXT_PROXY_URL` (`-p`'s default) is polled and asked as well, and is all there is when the registry lists none; `-p` given explicitly (the wrapper's `PROXY_OVERRIDE`) overrides all of them. |
| Chain RPC | The keeper's own endpoint — a failure here is cause 2, distinct from the extension's supply RPCs. |
| Hot wallet key | `KEEPER_KEY`: low-value, monitored, funded. Both of the keeper's transactions are permissionless and the store verifies TEE signatures against the machine registry itself, so this address has no authority at all — compromise costs the gas on it. Falling back to `EXTENSION_OWNER_KEY` works and is warned about; that key can also configure the extension, which nothing here needs. |
| `keeperId` | Stable identifier, so cross-keeper correlation and dedup can tell instances apart. |

---

## Running it unattended

Everything a system administrator needs to run the keeper as a service. It is a long-running
process that holds one key, keeps no state and listens on nothing.

**What to install.** One binary, `go build ./cmd/keeper` from `tools/`, or the image (see
[Deployment](#deployment)), and one file from the repository:
`deploy/config/<chain>/deployed-addresses.json`, the only one it needs. The files it reads:

- the addresses file (`ADDRESSES_FILE`, or `-a`) — the store, unless `-store` is given, and
  the `FlareTeeManager` diamond the registry and fee reads go through;
- `.env` and `deploy/config/stack.env` in the source tree the binary was compiled from, when they
  exist. `stack.env` overrides the environment, so a binary run on a development host that has a
  simulation stack recorded takes that stack's `EXT_PROXY_URL`. Neither exists on a server or in
  the image;
- only when `KEEPER_KEY` is unset, `.env` in the working directory or its parent, for
  `EXTENSION_OWNER_KEY`.

**Environment.** Two secrets and four settings:

| Variable | |
| --- | --- |
| `KEEPER_KEY` | the only secret the process requires. Both of its transactions are permissionless and the store verifies TEE signatures against the machine registry itself, so this address has no authority: it holds gas and nothing else. Without it the keeper falls back to `EXTENSION_OWNER_KEY` and warns — that key can also configure the extension, and no part of this process needs to. |
| `DIRECT_API_KEY` | optional. Buys the readiness preflight; without it every cycle pays the registry fee without asking the machines first whether they would sign. |
| `FLARE_CHAIN` | the chain: `local`, `coston`, `coston_local`, `coston2`, `songbird` or `flare`. It picks the default `CHAIN_URL` (the chain's public RPC; `coston_local` uses Coston's) and the default addresses file (`deploy/config/<chain>/deployed-addresses.json`), and with neither `KEEPER_KEY` nor `EXTENSION_OWNER_KEY` set it refuses the development-key fallback anywhere but `local`. Unset, `CHAIN_ID` names the chain (14, 16, 19, 114, 31337; 16 is `coston`), else the local simulation at `127.0.0.1:8545` unless `LOCAL_MODE=false`, which gives `coston2`. |
| `CHAIN_URL` | the chain node, as an absolute URL. |
| `ADDRESSES_FILE` | absolute path to the addresses JSON. The compiled-in default resolves against the source tree the binary was built from, which does not exist on a server, so this must be set explicitly. |
| `EXT_PROXY_URL` | a proxy polled for results and asked by the preflight in addition to each machine's registered URL, and the only one when the registry lists none. For the preflight to be able to skip, it must answer — see [The readiness preflight](#the-readiness-preflight). |

No argument is required. Leave `-tee` out so the keeper follows the registry: a machine that
restarts or is paused then needs no change here. `-keeperId` defaults to the
hostname and only matters when two keepers run — but under Docker the hostname is the container
id, which changes whenever the container is recreated, so pass it explicitly there.

**Network.** Outbound HTTPS to the chain node and to each machine's proxy. No inbound ports, no
listening socket.

**Logs.** Everything goes to **stdout** — the human log lines and the JSON cycle records
together, interleaved. Anything that parses the records should therefore be given `-records
<file>`, which writes them to a file of their own (roughly 100 KB a day at a ten-minute
interval, and it needs rotating); otherwise a consumer has to select the lines beginning with
`{`. The log level is DEBUG and there is no flag to change it.

**Stopping and restarting.** SIGTERM cancels the cycle in flight, which unwinds at its next
checkpoint and exits **0**; a second SIGTERM kills a process still winding down, so allow around
90 seconds before sending it. A startup misconfiguration — an unreadable addresses file, a
`-tee` that names no machine, a chain node that does not answer — exits **1** before any cycle runs, and
restarting will not fix it; the log line above the exit names the cause. (`-once` has its own
exit codes; see [`-once`](#-once).)

A restart is always safe: cycles are stateless, `observedAt` on chain is a ratchet, and a
duplicate submission is rejected rather than harmful. So a supervisor that restarts on exit is
right. A failed cycle is not an exit — the process logs it, records it and waits for the next
tick.

**Funds.** The keeper spends and never tops itself up. Every cycle records `keeperBalance` in wei
and warns below `-minBalance` (default 5 coins). At the measured ~0.2 CFLR per cycle on Coston, a
ten-minute interval is about 29 CFLR a day.

**Why it does not hang.** Every wait is bounded: the chain client caps each request at 30 s —
`ethclient.Dial` leaves that unbounded, which is the default `support.DialChain` deliberately
replaces — each proxy request at 10 s and each readiness query at 15 s, and the trigger, the
result poll and the submission each carry their own deadline. A node that stops answering
mid-call, or a transaction that is broadcast and never mined, costs one cycle rather than the
process.

**What to alert on**, from the records rather than from the process: `pendingNonceGap` above zero
for more than a cycle or two, `keeperBalance` approaching the floor, and — the only one that
matters to consumers — the age of `observedAt` on chain.

---

## Deployment

### The image

`keeper.Dockerfile` packages the list above. Build it from the repository root:

```
docker build -f keeper.Dockerfile -t usdx-keeper:latest .
docker build -f keeper.Dockerfile --build-arg CHAIN=songbird -t usdx-keeper:songbird .
```

- **BuildKit is required.** The build needs both modules' sources, and gets them through
  `keeper.Dockerfile.dockerignore` (which admits `go.mod`, `go.sum`, `pkg/`, `tools/` and
  `deploy/config/*/deployed-addresses.json`, and nothing holding a secret). Only BuildKit reads
  an ignore file named after its Dockerfile; the legacy builder applies the root `.dockerignore`
  and fails at `COPY tools/`.
- **Two static binaries**, `keeper` and `cycles-web`, built in `golang:1.25-trixie` for
  `linux/amd64` with cgo off, on `gcr.io/distroless/static-debian12:nonroot`: no shell, no
  package manager, uid 65532.
- **The chain is baked.** `--build-arg CHAIN=<chain>` (default `coston`) copies
  `deploy/config/<chain>/deployed-addresses.json` to `/opt/usdx/deployed-addresses.json` and
  sets `ADDRESSES_FILE` to it and `FLARE_CHAIN=<chain>`. Rebuild after a redeployment: the store
  address in the image is the one it uses.
- `/var/log/usdx` is created owned by 65532 for the records. `ENTRYPOINT` is the keeper, `CMD`
  is `-interval 10m -records /var/log/usdx/cycles.jsonl`, `STOPSIGNAL` is SIGTERM, and there is
  no `EXPOSE` and no `HEALTHCHECK`: the keeper listens on nothing.
- It is not the extension image and shares none of its reproducibility machinery: nothing here is
  attested, so a rebuild is free to differ.

`deploy/scripts/build-keeper-image.sh` is the release path. It builds with
`--platform linux/amd64` and labels (`org.opencontainers.image.version` / `.revision`,
`usdx.keeper.chain`), checks what ended up in the image (platform, user 65532, no exposed port,
the entrypoint, `ADDRESSES_FILE` and `FLARE_CHAIN`, a `TeeOracleFeedStore` entry in the baked
addresses file, the `cycles-web` binary), pushes, and writes `out/images/keeper-<tag>.env`
(`KEEPER_IMAGE`, `KEEPER_IMAGE_DIGEST`, `KEEPER_IMAGE_COMMIT`, `KEEPER_IMAGE_DIRTY`,
`KEEPER_IMAGE_CHAIN`, `KEEPER_FEED_STORE`, `PUSHED`).

| Option | Default | |
| --- | --- | --- |
| `--image` / `IMAGE_REPO` | `europe-west1-docker.pkg.dev/flare-network-staging/containers/fce-usdx-keeper` | registry path, without the tag |
| `--tag` / `IMAGE_TAG` | `v0.1.0` | the tag to publish |
| `--chain` / `KEEPER_CHAIN` | `coston` | the chain whose addresses are baked in |
| `--no-push` | push | build and check only |
| `--allow-dirty` | refuse | publish from a working tree with uncommitted changes |
| `--force` | refuse | move a tag that is already published |

On tag pipelines CI publishes the same image (`CHAIN=$KEEPER_CHAIN`, `coston`) to
`fce-usdx-keeper:<tag>`, and the `record-images` job exports `KEEPER_IMAGE_DIGEST`.

### Running the container

The line `build-keeper-image.sh` prints:

```
docker run -d --name usdx-keeper --restart unless-stopped --stop-timeout 90 \
  --env-file /opt/usdx/keeper.env -v usdx-records:/var/log/usdx \
  <image@digest> -interval 10m \
  -records /var/log/usdx/cycles.jsonl -keeperId <host>
```

- `keeper.env` holds `KEEPER_KEY`, `DIRECT_API_KEY`, `CHAIN_URL` and `EXT_PROXY_URL` — an
  env file, so the key stays out of the shell history and `ps`. The addresses file and
  `FLARE_CHAIN` come from the image.
- Arguments given to `docker run` replace `CMD`, so the real invocation restates `-interval` and
  `-records`, and adds `-keeperId` — and `-tee` only to fix the targets.
- `--stop-timeout 90`, because docker's default 10 s sends the killing signal while a cycle is
  still unwinding.
- A named volume inherits the records directory's ownership from the image; a bind mount has to
  be chowned to 65532 on the host.
- Pin the digest, not the tag: a tag can be moved.

### The records page

`cycles-web` serves the keeper's records file and an embedded page that reads it. It holds no
key, no chain client and no state, and never receives `KEEPER_KEY`: run it as a second container
from the same image, sharing the records volume read-only.

```
docker run -d --name usdx-cycles-web --restart unless-stopped -p 8080:8080 \
  -v usdx-records:/var/log/usdx:ro --entrypoint /usr/local/bin/cycles-web \
  <image@digest> -records /var/log/usdx/cycles.jsonl -addr :8080
```

| Flag | Default | |
| --- | --- | --- |
| `-records` | `/var/log/usdx/cycles.jsonl` | the keeper's `-records` file |
| `-addr` | `:8080` | listen address |
| `-max` | `2000` | how many of the most recent records `/cycles.jsonl` serves |

Routes: `/` (the page, which polls `/cycles.jsonl` every 30 s and plots the median price, the
outcome strip, feed age from `storeObservedAtAfter`, the balance and each cycle's detail),
`/cycles.jsonl` (the newest records, oldest first, from at most the file's last 8 MiB) and
`/healthz`. It needs the keeper to run with `-records`; without it the records go to stdout and
there is nothing to serve. Its transaction and address links go to the Coston explorer. It
shares the keeper's failure domain, so it is a view for operators, not the external watcher.

---

## Monitoring hooks

The design, taxonomy and routing tables are in `usdx-oracle-monitoring.md`. What the keeper
owes it is one **cycle record per tick**, emitted regardless of outcome — implemented as
`keeper.Record`, one JSON object per line.

### Outcomes

`outcome` is the cycle's terminal classification (`keeper.Outcome`). "Advanced" is
`Outcome.Advanced()`: the feed holds a price at least as new as this cycle's.

| `outcome` | Advanced | When | `submitOutcome` |
| --- | --- | --- | --- |
| `accepted` | yes | Our `submitFeedUpdates` was mined and succeeded. | `accepted` |
| `race_lost` | yes | The store refused the batch `NotNewer` — or a mined submission reverted — and the store's `observedAt`, read again, is at or past ours. Another submission landed first. Never alert. | `NotNewer`, or the guard a mined revert's replay names |
| `request_skipped` | no | Every proxy asked answered the readiness preflight and none would sign. No request, no fee. | `not_attempted` |
| `request_failed` | no | The trigger failed: the fee could not be read, `requestFeedUpdate` reverted or could not be sent, no receipt within `-requestTimeout`, or no `FeedUpdateRequested` in it. | `not_attempted` |
| `no_result` | no | No proxy returned a terminal result within `-pollTimeout`. | `not_attempted` |
| `extension_failed` | no | Every machine that answered refused (status 0) or returned data that would not decode. | `not_attempted` |
| `rejected` | no | The store will not take the answers, under a named guard: the whole round (`NoEndpointsPublished`, `NoAdminsPublished`, `SenderUnavailable`); every answer dropped (their common guard, else `NoUsableUpdate`); the batch preflight (`NotEnoughSignatures`, `TooManySignatures`); the rehearsal or gas estimation (the store's error name, else `Reverted`); a mined submission that reverted while the store stayed behind it (the replay's guard, else `Reverted`); a `NotNewer` while the store reads lower than ours, which only a lagging node or a failed read produces; or a transaction that could not be sent for a reason naming no guard. | the guard, or `send_failed` |
| `dry_run` | no | `-dry-run`: requested (and paid), collected, screened and rehearsed; stopped before submitting. | `dry_run` |
| `not_mined` | no | Submitted; no receipt within `-submitTimeout`. Not chased. | `not_mined` |
| `internal` | no | A keeper-side fault: no target machine to ask, an unreadable store or submission policy, a binding or encoding error. | `not_attempted` |

### The record

| Field | Set | Meaning |
| --- | --- | --- |
| `keeperId` | always | `-keeperId` |
| `cycleStartedAt`, `cycleEndedAt` | always | RFC 3339, UTC |
| `durationMs` | always | the cycle's wall-clock duration |
| `outcome` | always | see above |
| `proxy` | always | the first proxy, in poll order (`-p`, then the registry's), that returned a terminal result; `-p` when the cycle ended before any did |
| `instructionId` | the request was mined | the on-chain instruction id |
| `requestTxStatus` | a request was attempted | `mined` or `failed` |
| `resultStatus` | the poll ran | `1` when at least one usable update arrived, `0` when none did, `timeout` when nothing terminal arrived |
| `errorClass` | some machine answered status 0 | the first such `ActionResult.Log`, verbatim: `error: <reason>`, one reason from the closed set in `internal/failure`. Route it through the reason → class table in [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md#cause-4--routing-a-status-0-result). Present also on a cycle that went on with the other machines' answers |
| `sourceHealth` | the preflight read at least one machine | per machine, keyed by teeId: source → `ok` / `down` / `disagree` / `lagging` / `not_configured` / `unknown` |
| `submitOutcome` | always | `not_attempted`, `accepted`, `dry_run`, `send_failed`, `not_mined`, or the guard that refused the batch |
| `submitTx` | a submission was broadcast | its transaction hash |
| `teeIds` | a batch was formed | the batch's signers, recovered from their signatures |
| `prices` | a batch was formed | each contributor's value rendered at its own scale, in `teeIds` order; the keeper never aggregates them — the store takes the median |
| `observedAt` | a batch was formed | the batch's common stamp: the requesting instruction's block timestamp |
| `storeObservedAtAfter` | always | the store's `observedAt` at the end of the cycle, read from the chain; 0 when unreadable or before the first update |
| `dropped` | an answer was left out | `[{teeId, guard, detail}]`: `BadSignature` (no `teeId`), `NotAddressed`, `WrongExtensionId`, `WrongFeedId`, `StaleEndpoints`, `StaleAdmins`, `DuplicateTeeId`, `ObservedAtMismatch` |
| `pendingNonceGap` | non-zero | this keeper's transactions broadcast but not mined, at cycle start |
| `keeperBalance` | the balance was readable | the paying account at cycle start, wei as a decimal string |
| `detail` | the cycle ended on an error | human-readable; never parse it — route on `outcome`, `submitOutcome` and `errorClass` |

`teeIds` are recovered from the updates' own signatures, which is the only thing that names the
machines that actually answered. Together with `proxy` and `prices`, they are what attribute a
failure to one machine rather than to the feed. `dropped` completes the picture: a machine that
answered under a stale configuration, or a signature from a machine the cycle never asked,
appears there by guard name while the batch went up without it. Which machine a status-0
`errorClass` came from is in the keeper's log line for it, not in the record.

Two constraints carried over from the monitoring doc, because they are keeper-side:

- **The keeper must not be the only thing watching.** The page comes from on-chain feed age,
  evaluated by a process outside the keepers' failure domain — a dead keeper reports nothing,
  and its silence is indistinguishable from health.
- **`race_lost` must never alert.** It is the designed outcome of the ≥2-instance topology: the
  store already holds an update at least as new as ours, so the feed advanced. A `rejected` with
  guard `NotNewer` is the one worth a look, and it points at the RPC node the keeper reads, not
  at the feed.

---

## To decide

The cycle interval and the deviation trigger are decided in spec WP5: `T` = 10 minutes (the
`-interval` default), and no off-schedule update on deviation, because the keeper sees a price
only in the signed result it has already paid to request.

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 3 | Where does the keeper run, and who operates it? Open as spec WP5.7. Interacts with the monitoring doc's requirement that the chain-age watcher sit in a different failure domain. | Infra | Deployment |
