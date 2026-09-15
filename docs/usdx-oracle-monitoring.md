# USDX Oracle — Keeper Monitoring: Missed Updates

**Status:** 📋 **PLAN — not built** · **Raised:** 2026-08-04

_Companion docs: the fail-closed decision and its consequences in `usdx-oracle-availability.md`,
design intent in `usdx-oracle-spec.md`, implementation findings in `usdx-oracle-findings.md`. **The keeper that emits these signals is specified in
`usdx-oracle-keeper.md`** — this doc is its alerting half._

---

## Why this doc exists

Fail-closed is only a safe default if somebody finds out. Today an unreachable source produces
a failed observation, a `sourceHealth` entry and a status-0 `ActionResult` — and pages nobody.
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
survives every keeper being down; and it cannot be fabricated, because advancing it requires a
valid TEE signature. Two rules fall out:

1. **The page comes from `age`, evaluated by something that is not the keeper.** A separate
   watcher process, an external uptime check, or a cron on different infrastructure — the
   requirement is only that it does not share a failure domain with the keepers it is watching.
2. **Everything else is diagnosis.** The causes below explain an alert that already fired and
   route it to the right owner. Most of them should not page on their own.

There are exceptions to rule 2 — a few causes are *silent but serious*, meaning they will not
move `age` for a long time and must page early. They are marked in the table.

### Corollary: never alert on the absence of a bad signal

`sourceHealth` is written only when an observation runs (`extension.go` — `e.sourceHealth =
obs.Health`). If the keeper stops requesting, the last values persist and continue to read
`ok` indefinitely. A monitor that pages when `sourceHealth` goes `down` will stay perfectly
quiet through a total keeper outage. This is the same failure shape as finding 8, one level up.

---

## What "a missed update" actually means

Ordered by where they occur in one keeper cycle.

| # | Cause | Detected by | Signal | On its own |
| --- | --- | --- | --- | --- |
| **1** | Keeper never fired — process dead, scheduler wedged, container not restarted | External watcher only | Cycle heartbeat absent | **page** — silent but serious |
| **2** | `requestFeedUpdate()` never landed — wallet empty, fee wrong, gas, RPC down, target not configured | Keeper tx receipt | Revert or no receipt | warn → page if sustained |
| **3** | No `ActionResult` returned — TEE down, proxy down, instruction dropped, machine offline | Keeper poll timeout | Timeout, no result | warn → page if sustained |
| **4** | Extension failed the observation — a source is down or answered badly | `ActionResult.status == 0` | `failure.Class` in `Log` | route by class (below) |
| **5** | `submitFeedUpdates` reverted `NotNewer` — another keeper won the race | Tx revert reason | `NotNewer()` | **never alerts — normal operation** |
| **6** | `submitFeedUpdates` reverted for any other reason | Tx revert reason | See revert table | varies — several are page |
| **7** | Everything above succeeded and the feed is still stale | On-chain `age` | `age > 2 × T` | **page** — the consumer-facing truth |

Causes 1 and 7 are the two that must exist before the others are worth building. 1 catches the
keeper dying; 7 catches everything, including causes nobody anticipated.

---

## Cause 4 — routing a status-0 result

`ActionResult.Log` carries a constant from the closed vocabulary in `internal/failure`, and
`GET /state` carries the coarser `lastErrorClass`. The class is the alert key.

| Class | Failures | Meaning | Route |
| --- | --- | --- | --- |
| `source_unavailable` | `BackingUnavailable`, `BackingTimeout`, `SupplyUnavailable`, `SupplyTimeout` | An upstream is unreachable | warn; page on N consecutive |
| `supply_quorum_failed` | `SupplyNoQuorum` | Providers disagreed or too few answered | page — should be near-impossible at a fixed block |
| `source_invalid` | `BackingMalformed`, `BackingCurrency`, `BackingTimestamp`, `MissingRequestTime` | The source answered and we refused it | **page differently** — a human must read the response |
| `clock_skew` | `ClockBehindChain` | This machine's clock disagrees with chain time | page the **host**, not the upstreams — the fix is NTP |
| `implausible_value` | `BackingImplausible`, `SupplyImplausible`, `PriceImplausible`, `SupplyZero` | A value failed a sanity bound | page — either a real event or a bad bound |
| `not_configured` | `NotConfigured` | Secrets lost to a restart — memory-only by design | page — needs re-provisioning, not a retry |
| `sign_failed` | `SignUnavailable`, `SignRejected` | tee-node signing path broken | page |
| `internal` | `Internal` | Bug | page |

`clock_skew` is the odd one out: nothing upstream is wrong and nothing in the code is wrong, so
routing it with the source classes sends someone to read a Hex response that is perfectly fine.
It fires when the requesting instruction's block timestamp says the cached observation has aged
out while the machine's own clock still says it is fresh — see
`docs/usdx-oracle-failure-modes.md`. Expect it to arrive on one machine at a time.

`source_unavailable` and `supply_quorum_failed` are reachability. `source_invalid` is a
different kind of problem — the source answered but said something we refuse to use — and a
shape change from Hex needs a human reading the payload, not a retry. Route them to different
places.

**One failure may be one machine.** The keeper names its target machines (`-tee`), so a
single status-0 does not distinguish "Hex is down" from "one TEE machine has lost its egress
route". Correlating results across machines does; a single result cannot. Until that
correlation exists, treat a single status-0 as a warn regardless of class.

---

## Cause 5 — what must *not* page

`TeeOracleFeedStore`'s design is explicit on this point:

> _a rejected submission is normal operation, not an error — a slow relayer racing a fast one is
> expected._

The plan calls for **≥2 idempotent keepers**, deliberately, so that no single keeper is a
liveness dependency. The direct consequence is that on a healthy cycle **exactly one submission
succeeds and the others revert `NotNewer`**. That is the system working.

A `NotNewer` revert must therefore be recorded as a *successful cycle* by the keeper that lost
the race — the price it was carrying did land, submitted by someone else. Concretely:

```
if revert == NotNewer:
    verify store.observedAt >= our report's observedAt   # someone else's landed
    record cycle as success
    do NOT alert
```

The verification matters. `NotNewer` also fires if our own report was genuinely stale — an old
result resubmitted after a long delay — and that is a latency problem worth seeing. Comparing
against `store.observedAt` separates "someone beat me by a second" from "I am submitting
minutes-old reports".

---

## Cause 6 — classifying a `submitFeedUpdates` revert

The guard order in `submitFeedUpdates` is diagnostic: each revert tells you everything that
already passed. The batch is judged first — its shape, the spread of its values and the
`observedAt` ratchet — and only then are the contributors verified one by one, so by the time
`StaleEndpoints` fires the batch was well formed, in agreement and newer than the store.

| Revert | What it already proves | Meaning | Route |
| --- | --- | --- | --- |
| `NotEnoughSignatures`, `TooManySignatures` | nothing | Fewer agreeing machines than `requiredSignatures`, or more than 32 | **page** if sustained — machines down, or answers to another keeper's request mixed in (the keeper's record shows who was left out) |
| `ObservedAtMismatch`, `DecimalsSpreadTooBig` | count in range | The keeper sent a mixed batch | **page** — keeper bug; it groups by `observedAt` before submitting |
| `SpreadTooBig` | batch well formed | The values bracketing the median disagree by more than the policy's bound | **page** — the machines disagree about the price; compare their `prices` and `sourceHealth` |
| `NotNewer` | batch agrees on a value | Lost a race, or submitting stale updates | see cause 5 |
| `TooFarAhead` | batch newer than the store | `observedAt` is not strictly before `block.timestamp` | **page** — TEE clock ahead of chain time; check NTP on the machine's host |
| `NoEndpointsPublished`, `NoAdminsPublished` | shape, spread and ratchet pass | No `setEndpoints` / `setAdmins` ever published for the feed | **page** — provisioning incomplete |
| `ExtensionEmergencyPaused` | commitments published | Governance froze the extension | expected during an incident; page only if unplanned |
| `ECDSAInvalidSignature` (and friends) | commitments published | Malformed signature | **page** — encoding bug in the relay path |
| `InvalidTeeMachineExtensionId` | signature recovered | A contributor is not registered to our extension | **page** — registry drift |
| `TeeMachineNotProduction` | machine is on the extension | A contributor's status is not `PRODUCTION` | **page** — machine suspended/paused/not yet promoted |
| `DuplicateTeeId` | signer valid | The same machine appears twice in the batch | **page** — keeper bug; it keeps one update per signer |
| `WrongExtensionId`, `WrongFeedId` | signer valid and distinct | An update names a different extension/feed than this store | **page** — version/wiring mismatch, wrong deploy |
| `StaleEndpoints` | machine and feed valid | A contributor is running config that is not the feed's latest `setEndpoints` | **page** — provisioning drift; the keeper's preflight names the machine |
| `StaleAdmins` | endpoints current | A contributor is running an admin set that is not the feed's latest `setAdmins` | **page** — provisioning drift |

The store no longer enforces an `[minPrice, maxPrice]` band or a `maxAge` window —
`OutOfBounds` and `TooOld` have no on-chain counterpart any more. The price band is now
enforced **in the enclave before signing**, and staleness is the consumer's check. So the
"valid, current, fresh, TEE-signed number the store refused" failure has moved: it surfaces as
a status-0 `ActionResult` (a `failure.Class` from cause 4) rather than as an on-chain revert.
A real depeg that the enclave's bound suppresses now shows up as a status-0 with an
`implausible_value` class, not as an `OutOfBounds` revert — route it the same way, and page
with the value the observation produced, because that figure is the finding.

---

## Deduplication across keepers

Two or more keepers observing one outage must produce one page.

- **Dedupe key:** `(cause, feed)` — not `(cause, keeper)`. The keeper identity belongs in the
  alert body, never in the key.
- **Suppression window:** at least `2 × T` (~20 min at the planned 10-minute cadence), so a
  sustained outage pages once rather than once per cycle per keeper.
- **Correlation is itself a signal.** *All* keepers failing at cause 2 or 3 points at the TEE,
  the proxy or the chain. *One* keeper failing points at that keeper's own environment. The
  alert should state which, because it changes who gets woken.
- Do not build keeper-to-keeper coordination to achieve this. The keepers are deliberately
  independent and stateless; deduplication belongs in whatever receives the alerts.

---

## What the keeper should record per cycle

Enough to answer "why did nothing land?" without reading logs. Every field here is already on
the wire — no extension change is required.

| Field | Source |
| --- | --- |
| `cycleStartedAt`, `cycleEndedAt` | keeper |
| `instructionId` | `requestFeedUpdate` receipt |
| `requestTxStatus` | receipt |
| `resultStatus` (`1` / `0` / `timeout`) | `ActionResult.status`, or the poll timing out |
| `errorClass` | `ActionResult.Log` — the closed vocabulary |
| `sourceHealth` | proxy `GET /info` → `TEEState.state` |
| `submitOutcome` | `accepted` / revert name / not attempted |
| `dropped` | keeper — the answers left out of the batch, each with the store guard it would have failed |
| `storeObservedAtAfter` | `TeeOracleFeedStore.observedAt()` |
| `keeperId` | keeper |

`storeObservedAtAfter` is what makes a lost race distinguishable from a real failure, and it is
also the per-cycle sample of the one signal that cannot lie.

---

## Build phases

Ordered so that each phase is useful on its own. Phase 1 alone removes the current situation
where a total outage is silent.

| Phase | Deliverable | Why this order |
| --- | --- | --- |
| **1** | **Chain-age watcher.** External process reads `latest()`, pages on `age > 2 × T`. No keeper involvement. | Catches all seven causes at once, including a total keeper outage. The only phase that is load-bearing on its own. |
| **2** | **Keeper cycle heartbeat.** Each keeper emits a per-cycle record (above); watcher pages if a keeper stops emitting. | Distinguishes "one keeper died" from "the feed is stale", which phase 1 cannot. |
| **3** | **Revert and status classification.** Map causes 4–6 to the routing tables above; suppress `NotNewer`. | Turns a page into a diagnosis. Depends on phase 2's records existing. |
| **4** | **Dedup and correlation.** Suppression window, cross-keeper correlation, cross-machine correlation for cause 4. | Only meaningful once multiple keepers are emitting classified records. |
| **5** | **Dashboard and on-call routing.** Failure rate by class, `sourceHealth` over time, cycle latency. | Trend and post-incident work, not paging. |

Phases 1 and 2 are prerequisites for the mainnet milestone. 3–5 can follow.

---

## Deliberately out of scope

- **Anything inside the extension.** The reasoning is in `usdx-oracle-availability.md`: a
  webhook host would have to enter the code-hash-pinned `allowedHosts` allowlist, alerting
  credentials would become enclave secrets, and `ActionResult.Log` is public so the extension
  is constrained in what it can say. None of that buys anything the keeper cannot do better.
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
| 1 | What is `T`? Freshness thresholds are all expressed as multiples of it. Plan says match the ~10-min source refresh. | Engineering | Phase 1 |
| 2 | Where does the chain-age watcher run, given it must not share a failure domain with the keepers? | Infra | Phase 1 |
| 3 | `N` for "N consecutive failures" — and does a retry inside one cycle count as one failure or several? | Engineering | Phase 3 |
| 4 | Where is the enclave's price bound set relative to the lowest price worth publishing? The band moved on-chain→enclave, so a real depeg now pages as a status-0 `implausible_value` class (cause 4) rather than an `OutOfBounds` revert. | _TBD — same risk owner as spec §6.2_ | Phase 3 |
| 5 | Alert transport and on-call rotation — is there an existing path, or is one being created? | Infra | Phase 5 |
| 6 | Should `sourceHealth` report per-provider rather than per-chain? `rpc_flare: "ok"` hides one-of-three providers being down — the state worth seeing *before* the second goes and quorum fails. Open since finding 8; the only item here that needs an extension change. | Engineering | Phase 5 |

Question 4 shares an owner with the deviation guard (spec §6.2) and the fail-closed question in
`usdx-oracle-availability.md`. All three trade detection latency against false-signal risk and
are worth resolving in one sitting.
