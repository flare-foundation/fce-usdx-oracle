# USDX Oracle — Keeper Monitoring: Missed Updates

**Status:** 🟡 **PARTIALLY BUILT** · **Raised:** 2026-08-04 — the keeper's per-cycle records and
their classification (`tools/pkg/keeper`), the records page (`tools/cmd/cycles-web`), the
per-machine readiness follower (`tools/cmd/oracle-watch`) and the fleet snapshot
(`tools/cmd/fleet-status`) are built; the chain-age watcher, paging, routing and deduplication
are not. Per phase in [Build phases](#build-phases).

_Companion docs: the fail-closed decision and its consequences in
[usdx-oracle-availability.md](usdx-oracle-availability.md), design intent in
[usdx-oracle-spec.md](usdx-oracle-spec.md), implementation findings in
[usdx-oracle-findings.md](usdx-oracle-findings.md), the mechanism behind each failure in
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md). **The keeper that emits these
signals is specified in [usdx-oracle-keeper.md](usdx-oracle-keeper.md)** — this doc is its
alerting half._

---

## Why this doc exists

Fail-closed is only a safe default if somebody finds out. An unreachable source produces a
failed observation, a `sourceHealth` entry and a status-0 `ActionResult` — and pages nobody.
`usdx-oracle-availability.md` records the decision that this alerting belongs in the keeper
rather than the extension; this document is the design that decision deferred.

The plan doc carries the requirement as one line: *feed freshness (> 2× interval)*. That line
hides a design problem worth writing down before anyone implements it:

> **"No price landed on chain" has seven distinct causes. They have different owners, different
> urgency, and different fixes — and one of them is not a fault at all.**

An implementation that collapses them into a single "update failed" alert will page the
on-call for a routine two-keeper race several times an hour, and that alert will be muted
within a week. The taxonomy is the deliverable here, not the threshold.

---

## Principle: alert on the effect, diagnose with the causes

The keeper is the only component that sees the whole round trip, which is why the alerting
belongs there. But it is also a component that can die, and **a dead keeper reports nothing** —
its silence is indistinguishable from a healthy, quiet system.

So the alerting cannot be built out of keeper-reported success and failure alone. It needs one
signal that is true whether or not any keeper is alive.

That signal is the on-chain observation timestamp:

```
timestamp = TeeOracleFeedStore.observedAt()     ← free view; getCurrentFeed() is a paid read
age = block.timestamp - timestamp
```

`age` is what consumers actually experience. It is written by the store, not by a keeper; it
survives every keeper being down; and it cannot be fabricated, because advancing it requires
valid TEE signatures from PRODUCTION machines. `observedAt` is the block timestamp of the
instruction that requested the update, so `age` includes the request's own round trip. Two rules
fall out:

1. **The page comes from `age`, evaluated by something that is not the keeper.** A separate
   watcher process, an external uptime check, or a cron on different infrastructure — the
   requirement is only that it does not share a failure domain with the keepers it is watching.
2. **Everything else is diagnosis.** The causes below explain an alert that already fired and
   route it to the right owner. Most of them should not page on their own.

There are exceptions to rule 2 — a few causes are *silent but serious*, meaning they will not
move `age` for a long time and must page early. They are marked in the table.

### Corollary: never alert on the absence of a bad signal

Each machine observes every 2 minutes on its own timer, whether or not anyone requests, and its
signed `GET_READINESS` report carries the newest observation's `sourceHealth`. But a monitor only
ever sees that report through something that asks for it: the keeper's readiness preflight,
once per cycle, into the record's `sourceHealth`; and `oracle-watch`, every 2 minutes, into its
samples. When the asker stops, or a machine cannot be reached, the map is simply absent — it
never turns `down`. A monitor that pages when `sourceHealth` goes `down` will stay perfectly
quiet through a total keeper outage or an unreachable machine. This is the same failure shape as
finding 8, one level up.

---

## What "a missed update" actually means

Ordered by where they occur in one keeper cycle.

| # | Cause | Detected by | Signal | On its own |
| --- | --- | --- | --- | --- |
| **1** | Keeper never fired — process dead, scheduler wedged, container not restarted | External watcher only | Cycle heartbeat absent | **page** — silent but serious |
| **2** | `requestFeedUpdate()` never landed — wallet empty, fee wrong, gas, RPC down, a `-tee` target not requestable (`FeedNotConfigured`, `TeeIdNotConfigured`, `TeeMachineNotAvailable`) | Keeper tx receipt | `request_failed` | warn → page if sustained; a target that is not requestable fails every cycle until `-tee` or the configuration is fixed |
| **3** | No `ActionResult` returned — TEE down, proxy down, instruction dropped, machine offline | Keeper poll timeout | `no_result` | warn → page if sustained |
| **4** | Extension failed the observation — a source is down or answered badly, or the machine cannot sign | `ActionResult.status == 0` | `extension_failed`, `errorClass` = `error: <reason>` | route by reason (below) |
| **5** | `submitFeedUpdates` refused `NotNewer` and the store is at or past our update — another submission landed first | Store `observedAt`, read again | `race_lost` | **never alerts — normal operation** |
| **6** | `submitFeedUpdates` refused, or reverted, for any other reason | Guard name | `rejected` (`submitOutcome` names the guard), `not_mined` | varies — several are page |
| **7** | Everything above succeeded and the feed is still stale | On-chain `age` | `age > 2 × T` | **page** — the consumer-facing truth |

`request_skipped` is cause 4 seen before the fee: every machine asked said it would not sign, so
no request was made. The record's `sourceHealth` names the failing source and `detail` the
observation age and `lastErrorClass` of the first machine that declined.

Causes 1 and 7 are the two that must exist before the others are worth building. 1 catches the
keeper dying; 7 catches everything, including causes nobody anticipated.

---

## Cause 4 — routing a status-0 result

On a status-0 result, `ActionResult.Log` is `error: ` followed by one reason from the closed
vocabulary in `internal/failure` — every public refusal is one of the 28 values in
`failure.All`, and the text is a compile-time constant. The keeper copies the first such `Log`
verbatim into the record's `errorClass`. **The reason text is the alert key**; each reason has a
class, and the class picks the route. The readiness report carries only the class of the
poller's last failed observation, as `lastErrorClass`.

Route on the reason, not the class: a class can hold faults that need different people.
`source_invalid`, for one, holds both "Hex changed its response" and "the instruction carried no
timestamp".

### Reason → class

Generated from `internal/failure.All`, in its order. "GET_FEED" says whether a `GET_FEED` result
can carry it.

| `errorClass` | Failure | Class | GET_FEED |
| --- | --- | --- | --- |
| `error: instruction carries an unexpected payload` | `UnexpectedPayload` | `source_invalid` | yes — a request that is not one `FeedUpdateRequest` for this feed |
| `error: command arrived over the wrong action type` | `WrongActionType` | `source_invalid` | no — a command on the wrong channel |
| `error: provisioning envelope rejected` | `ProvisionRejected` | `source_invalid` | no — `SET_SECRET` only |
| `error: extension is not provisioned` | `NotConfigured` | `not_configured` | yes |
| `error: backing source unavailable` | `BackingUnavailable` | `source_unavailable` | yes |
| `error: backing source timed out` | `BackingTimeout` | `source_unavailable` | yes |
| `error: backing source returned a malformed response` | `BackingMalformed` | `source_invalid` | yes |
| `error: backing source reported an unexpected currency` | `BackingCurrency` | `source_invalid` | yes |
| `error: backing source reported an unexpected metric` | `BackingMetric` | `source_invalid` | yes |
| `error: backing source reported a timestamp in the future` | `BackingTimestamp` | `source_invalid` | yes |
| `error: backing source returned an implausible value` | `BackingImplausible` | `implausible_value` | defensive — the parser refuses a negative figure first |
| `error: supply source unavailable` | `SupplyUnavailable` | `source_unavailable` | yes |
| `error: supply source timed out` | `SupplyTimeout` | `source_unavailable` | yes |
| `error: supply source returned a malformed response` | `SupplyMalformed` | `source_invalid` | yes |
| `error: supply providers did not reach quorum` | `SupplyNoQuorum` | `supply_quorum_failed` | yes |
| `error: supply read produced an implausible value` | `SupplyImplausible` | `implausible_value` | defensive — an unsigned read cannot be negative |
| `error: supply block is too far behind` | `SupplyBlockStale` | `source_invalid` | yes |
| `error: observation window exceeded` | `WindowExceeded` | `source_invalid` | cannot fire — the 30 s window outlasts the 20 s deadline |
| `error: observation deadline exceeded` | `Deadline` | `source_unavailable` | never returned — a deadline fails as `SupplyTimeout` / `BackingTimeout` |
| `error: no observation available yet` | `NoObservation` | `source_unavailable` | yes — before the first observation completes |
| `error: no recent observation` | `ObservationStale` | `source_unavailable` | yes |
| `error: instruction carries no block timestamp` | `MissingRequestTime` | `source_invalid` | yes |
| `error: machine clock is behind chain time` | `ClockBehindChain` | `clock_skew` | yes |
| `error: supply is zero` | `SupplyZero` | `implausible_value` | yes |
| `error: computed price is outside the plausible range` | `PriceImplausible` | `implausible_value` | never returned — see [Cause 6](#cause-6--classifying-a-submitfeedupdates-revert) |
| `error: signing service unavailable` | `SignUnavailable` | `sign_failed` | yes |
| `error: signing service rejected the request` | `SignRejected` | `sign_failed` | yes |
| `error: internal error` | `Internal` | `internal` | yes |

A `Log` that is not `error: ` plus one of these is not from the vocabulary: a command the build
does not serve reaches `Log` as the extension's 501 body, and tee-node writes its own text for
faults outside the extension. Route it as `internal`. tee-node's status 3 (the extension missed
its deadline) is not terminal to the keeper; that machine costs the poll timeout and shows as
`no_result` when no other machine answered.

### Class → route

| Class | Failures | Meaning | Route |
| --- | --- | --- | --- |
| `source_unavailable` | `BackingUnavailable`, `BackingTimeout`, `SupplyUnavailable`, `SupplyTimeout`, `NoObservation`, `ObservationStale` (`Deadline`) | An upstream is unreachable, or every observation since the last good one failed | warn; page on N consecutive |
| `supply_quorum_failed` | `SupplyNoQuorum` | Providers answered but fewer than the chain group's threshold agree, after stepping down through the candidate heads | page — should be near-impossible at a pinned block |
| `source_invalid` | source faults `BackingMalformed`, `BackingCurrency`, `BackingMetric`, `BackingTimestamp`, `SupplyMalformed`, `SupplyBlockStale` (`WindowExceeded`); instruction faults `UnexpectedPayload`, `MissingRequestTime`, `WrongActionType`, `ProvisionRejected` | The source answered and we refused it — or the request itself was malformed | **page differently** — for a source fault a human must read the response; an instruction fault is a caller's bug |
| `clock_skew` | `ClockBehindChain` | This machine's clock is behind chain time | page the **host**, not the upstreams — the fix is NTP |
| `implausible_value` | `SupplyZero` (`BackingImplausible`, `SupplyImplausible`, `PriceImplausible`) | Total supply read as zero | page — a provider set reads an empty or wrong token |
| `not_configured` | `NotConfigured` | No endpoint configuration applied, no admin set installed, or a credential an endpoint needs missing — expected after every restart, since secrets are memory-only by design | page — needs provisioning, not a retry |
| `sign_failed` | `SignUnavailable`, `SignRejected` | tee-node signing path broken | page |
| `internal` | `Internal` | Bug | page |

Failures in parentheses cannot reach a `GET_FEED` result in practice (see the table above).

`clock_skew` is the odd one out: nothing upstream is wrong and nothing in the code is wrong, so
routing it with the source classes sends someone to read a Hex response that is perfectly fine.
It fires when the requesting instruction's block timestamp says the cached observation has aged
out while the machine's own clock still says it is fresh — see
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md#the-10-minute-bound-is-measured-twice).
Expect it to arrive on one machine at a time. The readiness preflight cannot foresee it, so a
machine reporting `wouldSign` can still refuse with it.

`source_unavailable` and `supply_quorum_failed` are reachability. `source_invalid` is a
different kind of problem — the source answered but said something we refuse to use — and a
shape change from Hex needs a human reading the payload, not a retry. Route them to different
places.

**One failure may be one machine.** The keeper asks several machines per request, and a
status-0 from one does not stop the others: `errorClass` records the first refusal even on a
cycle whose batch went up. `errorClass` on an `accepted` or `race_lost` cycle is one machine;
`extension_failed` means every machine that answered refused, and `errorClass` is the first of
them. Which machine refused is in the keeper's log line, not the record. A single status-0 does
not distinguish "Hex is down" from "one TEE machine has lost its egress route"; correlating the
machines' answers does. Until that correlation exists, treat a single status-0 as a warn
regardless of class.

---

## Cause 5 — what must *not* page

The plan calls for **≥2 idempotent keepers**, deliberately, so that no single keeper is a
liveness dependency. Each keeper raises its own request, and every update answering a request
carries that request's block timestamp as `observedAt`. The store accepts only a strictly newer
`observedAt`, so it takes **at most one update per request timestamp**: requests whose blocks
carry different timestamps can both land, in order; whichever arrives second with an older stamp
reverts `NotNewer`; and two requests whose blocks share a timestamp share a stamp, so only the
first submission carrying it lands. A `NotNewer` whose store is already at or past our update means the feed
advanced at least as far as this cycle would have taken it. That is the system working.

The keeper therefore records it as `race_lost`, which `Outcome.Advanced()` counts with
`accepted`. Concretely, for every `NotNewer` — from its preflight, its rehearsal, gas estimation,
or a submission mined and reverted:

```
if guard == NotNewer:
    stored = store.observedAt()               # read again, after the refusal
    if stored >= our batch's observedAt:
        record race_lost                      # do NOT alert
    else:
        record rejected, guard NotNewer
```

The re-read matters only to keep a bad read from passing as success. The ratchet only moves
forward, so a store that refused `NotNewer` reads at or past our stamp; `rejected` with guard
`NotNewer` means the read failed or reached a lagging node, and points at the keeper's RPC, not
at the feed. A submission that was mined and reverted for another reason is `race_lost` by the
same test when the store is at or past our stamp, and `rejected` under the guard its replay
names otherwise.

---

## Cause 6 — classifying a `submitFeedUpdates` revert

The guard order in `submitFeedUpdates` is diagnostic: each revert tells you everything that
already passed. The batch is judged first — its shape, the spread of its values and the
`observedAt` ratchet — and only then are the contributors verified one by one, so by the time
`StaleEndpoints` fires the batch was well formed, in agreement and newer than the store.

| Revert | What it already proves | Meaning | Route |
| --- | --- | --- | --- |
| `NotEnoughSignatures`, `TooManySignatures` | nothing | Fewer agreeing machines than `requiredSignatures`, or more than 32 | **page** if sustained — machines down, or answers to another keeper's request mixed in (the keeper's record shows who was left out) |
| `ObservedAtMismatch` | count in range | The keeper sent a mixed batch | **page** — keeper bug; it groups by `observedAt` before submitting |
| `DecimalsSpreadTooBig` | count in range, one `observedAt` | Contributors' `decimals` differ by more than 8 | **page** — every build signs at 8 decimals, so a contributor runs something else |
| `SpreadTooBig` | batch well formed | The values bracketing the median disagree by more than the policy's bound | **page** — the machines disagree about the price; compare their `prices` and `sourceHealth` |
| `NotNewer` | batch agrees on a value | Another submission landed first | see cause 5: `race_lost`, never a page |
| `TooFarAhead` | batch newer than the store | `observedAt` is not strictly before `block.timestamp` | **page** — unreachable for an honest update, whose `observedAt` is the requesting block's timestamp; an update stamped at or after the accepting block was not produced by a machine answering a mined request |
| `NoEndpointsPublished`, `NoAdminsPublished` | shape, spread and ratchet pass | No `setEndpoints` / `setAdmins` ever published for the feed | **page** — provisioning incomplete |
| `ExtensionEmergencyPaused` | commitments published | The extension's emergency pause (FlareTeeManager) is on: new submissions are blocked and the last value frozen | expected during an incident; page only if unplanned |
| `ECDSAInvalidSignature` (and friends) | commitments published | Malformed signature | **page** — encoding bug in the relay path |
| `InvalidTeeMachineExtensionId` | signature recovered | A contributor is not registered to our extension | **page** — registry drift |
| `TeeMachineNotAvailable` | machine is on the extension | A contributor's status is not `PRODUCTION` | **page** — machine suspended, paused, banned or not yet promoted |
| `DuplicateTeeId` | signer valid | The same machine appears twice in the batch | **page** — keeper bug; it keeps one update per signer |
| `WrongExtensionId`, `WrongFeedId` | signer valid and distinct | An update names a different extension/feed than this store | **page** — version/wiring mismatch, wrong deploy |
| `StaleEndpoints` | machine and feed valid | A contributor is running config that is not the feed's latest `setEndpoints` | **page** — provisioning drift; the keeper's screening names the machine |
| `StaleAdmins` | endpoints current | A contributor is running an admin set that is not the feed's latest `setAdmins` | **page** — provisioning drift |

How each reaches the record. The keeper screens every answer and the batch before the
rehearsal, so `WrongExtensionId`, `WrongFeedId`, `StaleEndpoints`, `StaleAdmins`,
`DuplicateTeeId` and `ObservedAtMismatch` normally appear in `dropped` against one machine while
the rest of the batch goes up, and `NoEndpointsPublished`, `NoAdminsPublished`,
`NotEnoughSignatures`, `TooManySignatures` and `NotNewer` as the cycle's `submitOutcome` before
any gas is spent. The rest come from the rehearsal (an `eth_call` of `submitFeedUpdates`), gas
estimation, or a mined revert's replay, decoded against the store's ABI. `ExtensionEmergencyPaused`,
`InvalidTeeMachineExtensionId`, `TeeMachineNotAvailable` and the ECDSA errors are raised inside
`Fdc2Verification`, are not in the store's ABI, and so arrive as guard **`Reverted`** with the
revert data in hex in `detail`: `0x1b344be4` `ExtensionEmergencyPaused(uint256)`, `0xfec67cd1`
`InvalidTeeMachineExtensionId`, `0xea552ba8` `TeeMachineNotAvailable`, `0xf645eedf`
`ECDSAInvalidSignature`, `0xfce698f7` `ECDSAInvalidSignatureLength`, `0xd78bce0c`
`ECDSAInvalidSignatureS`. Route `Reverted` by that selector.

The store enforces no price band and no maximum age. The enclave bounds the price to [0, 1] by
construction — `min(1, B/S)` over non-negative inputs — and applies no lower bound:
`PriceImplausible` is declared and never returned. **A real depeg is published**: the sub-par
value arrives as a status-1 result and an accepted submission, with no failure class and no
revert. Detect it on the published value — the record's `prices` and the store's
`getCurrentFeed()` — not on a failure. Staleness is the consumer's check: `getCurrentFeed()`
returns the last value with its true timestamp, and reverts only `NoValuePublished`, before the
first update.

---

## Deduplication across keepers

Two or more keepers observing one outage must produce one page.

- **Dedupe key:** `(cause, feed)` — not `(cause, keeper)`. The keeper identity belongs in the
  alert body, never in the key.
- **Suppression window:** at least `2 × T` (~20 min at the default 10-minute interval), so a
  sustained outage pages once rather than once per cycle per keeper.
- **Correlation is itself a signal.** *All* keepers failing at cause 2 or 3 points at the TEE,
  the proxy or the chain. *One* keeper failing points at that keeper's own environment. The
  alert should state which, because it changes who gets woken.
- Do not build keeper-to-keeper coordination to achieve this. The keepers are deliberately
  independent and stateless; deduplication belongs in whatever receives the alerts.

---

## What the keeper records per cycle

Enough to answer "why did nothing land?" without reading logs. Every field is already on the
wire — no extension change is required. The full schema and the outcome vocabulary are in
[usdx-oracle-keeper.md](usdx-oracle-keeper.md#monitoring-hooks) (`keeper.Record`,
`tools/pkg/keeper/record.go`); the fields this doc routes on:

| Field | Source |
| --- | --- |
| `keeperId`, `cycleStartedAt`, `cycleEndedAt`, `durationMs` | keeper |
| `outcome` | keeper — the cause, per the mapping below |
| `instructionId`, `requestTxStatus` | `requestFeedUpdate` receipt |
| `resultStatus` (`1` / `0` / `timeout`) | `ActionResult.status`, or the poll timing out |
| `errorClass` | the first status-0 `ActionResult.Log`, verbatim: `error: <reason>` |
| `sourceHealth` | each machine's signed `GET_READINESS` report, from the keeper's readiness preflight, keyed by teeId; within it `rpc_<chain>` for each chain, `rpc_<chain>_<i>` for each of its providers, `hex_bonds`, `hex_cash` |
| `submitOutcome` | `accepted`, the guard that refused the batch, `not_mined`, `send_failed`, `dry_run` or `not_attempted` |
| `teeIds`, `prices`, `dropped` | keeper — who made the batch, what each signed, and who was left out under which guard |
| `storeObservedAtAfter` | `TeeOracleFeedStore.observedAt()` at the end of the cycle |

| `outcome` | Cause |
| --- | --- |
| `accepted` | — the cycle landed |
| `race_lost` | 5 |
| `request_failed` | 2 |
| `no_result` | 3 |
| `extension_failed`, `request_skipped` | 4 |
| `rejected`, `not_mined` | 6 |
| `dry_run` | — a rehearsal by hand |
| `internal` | the keeper itself — page its owner |

`storeObservedAtAfter` is the per-cycle sample of the one signal that cannot lie, taken from the
chain rather than from any machine.

---

## Build phases

Ordered so that each phase is useful on its own. Phase 1 alone removes the situation where a
total outage is silent.

| Phase | Deliverable | Why this order | Status |
| --- | --- | --- | --- |
| **1** | **Chain-age watcher.** External process reads `observedAt()`, pages on `age > 2 × T`. No keeper involvement. | Catches all seven causes at once, including a total keeper outage. The only phase that is load-bearing on its own. | **Not built.** `cycles-web` shows feed age from the records, but it runs beside the keeper, in its failure domain, and pages nobody. |
| **2** | **Keeper cycle heartbeat.** Each keeper emits a per-cycle record (above); watcher pages if a keeper stops emitting. | Distinguishes "one keeper died" from "the feed is stale", which phase 1 cannot. | **Partially built.** The keeper emits `keeper.Record` every cycle; nothing pages when the records stop. |
| **3** | **Revert and status classification.** Map causes 4–6 to the routing tables above; suppress `NotNewer`. | Turns a page into a diagnosis. Depends on phase 2's records existing. | **Partially built.** Each record carries its `outcome`, the guard in `submitOutcome` and `dropped`, and `errorClass`; `NotNewer` is `race_lost`. Nothing routes on them. |
| **4** | **Dedup and correlation.** Suppression window, cross-keeper correlation, cross-machine correlation for cause 4. | Only meaningful once multiple keepers are emitting classified records. | **Not built.** |
| **5** | **Dashboard and on-call routing.** Failure rate by class, `sourceHealth` over time, cycle latency. | Trend and post-incident work, not paging. | **Partially built.** `cycles-web` (outcomes, price, feed age, balance, per-cycle `sourceHealth` and duration), `oracle-watch` (one machine's readiness report every 2 min, appended as samples, changes printed), `fleet-status` (registry status, configuration on-chain and live, per machine). No on-call routing. |

Phases 1 and 2 are prerequisites for the mainnet milestone. 3–5 can follow.

---

## Deliberately out of scope

- **Anything inside the extension.** The reasoning is in `usdx-oracle-availability.md`: a
  webhook host would become one more endpoint the attested code reaches, alerting credentials
  would become enclave secrets, and `ActionResult.Log` is public so the extension is constrained
  in what it can say. None of that buys anything the keeper cannot do better.
- **On-chain failure reporting.** The store has no failure entrypoint and should not gain one:
  an unsigned failure claim is unattributable, a signed one needs a second report type and a
  monotonicity story, and publishing "our reserve source is down right now" permanently and
  indexably is a disclosure at the worst possible moment.
- **Retry policy.** Whether a failed cycle retries before the next scheduled one is a keeper
  design question, not a monitoring one. Note only that retries change what "N consecutive
  failures" means, so the two must be specified together.

---

## To decide

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 1 | What is `T` in production? Freshness thresholds are all expressed as multiples of it. The keeper's default is 10 min, matching the source refresh. | Engineering | Phase 1 |
| 2 | Where does the chain-age watcher run, given it must not share a failure domain with the keepers? | Infra | Phase 1 |
| 3 | `N` for "N consecutive failures" — and does a retry inside one cycle count as one failure or several? | Engineering | Phase 3 |
| 4 | Which published value pages as a depeg, and who is woken? There is no price band anywhere, so a depeg is detected only on the value the feed publishes. | _TBD — same risk owner as spec §6.2_ | Phase 3 |
| 5 | Alert transport and on-call rotation — is there an existing path, or is one being created? | Infra | Phase 5 |

Question 4 shares an owner with the deviation guard (spec §6.2) and the fail-closed question in
`usdx-oracle-availability.md`. All three trade detection latency against false-signal risk and
are worth resolving in one sitting.
