# USDX Oracle — Keeper Monitoring: Missed Updates

**Status:** 📋 **PLAN — not built** · **Raised:** 2026-08-04

_Companion docs: the fail-closed decision and its consequences in `usdx-oracle-availability.md`,
design intent in `usdx-oracle-spec.md`, implementation findings in `usdx-oracle-findings.md`,
working plan in `usdx-oracle-plan.md`. **The keeper that emits these signals is specified in
`usdx-oracle-keeper.md`** — this doc is its alerting half. Signal/severity table in
`usdx-oracle-operations.html` §3._

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
(price, decimals, timestamp) = UsdxPriceStore.latest()
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
| **2** | `requestPriceUpdate()` never landed — wallet empty, fee wrong, gas, RPC down | Keeper tx receipt | Revert or no receipt | warn → page if sustained |
| **3** | No `ActionResult` returned — TEE down, proxy down, instruction dropped, machine offline | Keeper poll timeout | Timeout, no result | warn → page if sustained |
| **4** | Extension failed the observation — a source is down or answered badly | `ActionResult.status == 0` | `failure.Class` in `Log` | route by class (below) |
| **5** | `submitPrice` reverted `NotNewer` — another keeper won the race | Tx revert reason | `NotNewer()` | **never alerts — normal operation** |
| **6** | `submitPrice` reverted for any other reason | Tx revert reason | See revert table | varies — several are page |
| **7** | Everything above succeeded and the feed is still stale | On-chain `age` | `age > 2 × T` | **page** — the consumer-facing truth |

Causes 1 and 7 are the two that must exist before the others are worth building. 1 catches the
keeper dying; 7 catches everything, including causes nobody anticipated.

---

## Cause 4 — routing a status-0 result

`ActionResult.Log` carries a constant from the closed vocabulary in `go/internal/failure`, and
`GET /state` carries the coarser `lastErrorClass`. The class is the alert key.

| Class | Failures | Meaning | Route |
| --- | --- | --- | --- |
| `source_unavailable` | `BackingUnavailable`, `BackingTimeout`, `SupplyUnavailable`, `SupplyTimeout` | An upstream is unreachable | warn; page on N consecutive |
| `supply_quorum_failed` | `SupplyNoQuorum` | Providers disagreed or too few answered | page — should be near-impossible at a fixed block |
| `source_invalid` | `BackingMalformed`, `BackingCurrency`, `BackingTimestamp` | The source answered and we refused it | **page differently** — a human must read the response |
| `implausible_value` | `BackingImplausible`, `SupplyImplausible`, `PriceImplausible`, `SupplyZero` | A value failed a sanity bound | page — either a real event or a bad bound |
| `not_configured` | `NotConfigured` | Secrets lost to a restart — memory-only by design | page — needs re-provisioning, not a retry |
| `sign_failed` | `SignUnavailable`, `SignRejected` | tee-node signing path broken | page |
| `internal` | `Internal` | Bug | page |

`source_unavailable` and `supply_quorum_failed` are reachability. `source_invalid` is a
different kind of problem — the source answered but said something we refuse to use — and a
shape change from Hex needs a human reading the payload, not a retry. Route them to different
places.

**One failure may be one machine.** `getRandomTeeIds` picks one machine per instruction, so a
single status-0 does not distinguish "Hex is down" from "one TEE machine has lost its egress
route". Correlating results across machines does; a single result cannot. Until that
correlation exists, treat a single status-0 as a warn regardless of class.

---

## Cause 5 — what must *not* page

`UsdxPriceStore`'s own contract comment is explicit:

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

## Cause 6 — classifying a `submitPrice` revert

The guard order in `submitPrice` is diagnostic: each revert tells you everything that already
passed. By the time `OutOfBounds` fires, the signature has been verified, the machine confirmed
registered for this extension, the config hash confirmed current, and the report confirmed
newer than the stored one.

| Revert | What it already proves | Meaning | Route |
| --- | --- | --- | --- |
| `IsPaused` | nothing | Governance froze the store | expected during an incident; page only if unplanned |
| `BadSignatureLength`, `BadRecoveryId`, `MalleableSignature` | nothing | Malformed signature | **page** — encoding bug in the relay path |
| `UnknownSigner` | signature recovered | Machine not registered to our extension, or its status is not `PRODUCTION` | **page** — registry drift, or a machine suspended/paused/banned |
| `WrongDomain`, `WrongDecimals` | machine is registered | Report layout disagrees with the deployed store | **page** — version mismatch, wrong deploy |
| `NoConfigPublished` | machine is registered | No `setEndpoints` recorded for this machine | **page** — provisioning incomplete |
| `StaleConfig` | machine is registered | Machine is running config that is not what `setEndpoints` published | **page** — provisioning drift |
| `NotNewer` | config is current | Lost a race, or submitting stale reports | see cause 5 |
| `TooOld` | report is newer | Report aged past `maxAge` before landing | **page** — round-trip latency exceeds the window |
| `TooFarAhead` | report is newer | `observedAt` is more than `MAX_FUTURE_SKEW` (2 min) ahead of chain time | **page** — TEE clock drift beyond what the window absorbs; check NTP on the machine's host |
| `OutOfBounds` | *everything else* | A valid, current, fresh, TEE-signed price the store refused | **page — highest priority** |

### `OutOfBounds` deserves its own paragraph

Every other revert means something is broken in the plumbing. `OutOfBounds` means the plumbing
worked perfectly and the store rejected the number.

That is either a bound set too tightly, or **a real depeg being suppressed** — the exact event
this feed exists to detect, arriving and being dropped on the floor. It must page immediately,
and it must page with the price that was rejected, because that figure is the finding.

Note the interaction with the `Price < 1.00` business alert already on the plan: if `minPrice`
is set at or near par, a genuine depeg fires `OutOfBounds` *instead of* `Price < 1.00`, and the
business alert never fires at all. Whoever sets the bounds must set them below the lowest price
worth publishing, not at the lowest price considered plausible. Worth checking against the
deviation-guard decision (spec §6.2), which is unresolved and shares an owner.

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
| `instructionId` | `requestPriceUpdate` receipt |
| `requestTxStatus` | receipt |
| `resultStatus` (`1` / `0` / `timeout`) | `ActionResult.status`, or the poll timing out |
| `errorClass` | `ActionResult.Log` — the closed vocabulary |
| `sourceHealth` | proxy `GET /info` → `TEEState.state` |
| `submitOutcome` | `accepted` / revert name / not attempted |
| `storeObservedAtAfter` | `UsdxPriceStore.latest()` |
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
| 4 | Are `minPrice` bounds set below the lowest price worth publishing, or at the lowest plausible one? Determines whether a real depeg pages as `OutOfBounds` or as `Price < 1.00`. | _TBD — same risk owner as spec §6.2_ | Phase 3 |
| 5 | Alert transport and on-call rotation — is there an existing path, or is one being created? | Infra | Phase 5 |
| 6 | Should `sourceHealth` report per-provider rather than per-chain? `rpc_flare: "ok"` hides one-of-three providers being down — the state worth seeing *before* the second goes and quorum fails. Open since finding 8; the only item here that needs an extension change. | Engineering | Phase 5 |

Question 4 shares an owner with the deviation guard (spec §6.2) and the fail-closed question in
`usdx-oracle-availability.md`. All three trade detection latency against false-signal risk and
are worth resolving in one sitting.
