# USDX Oracle — Source Availability

**Status:** **DECIDED in code** — fail closed; publish without cash only at par · **Still open:**
risk-owner sign-off, Hex's uptime commitment, a second source for the bond figure ·
**Raised:** 2026-08-03

_Companion docs: design intent in `usdx-oracle-spec.md`, implementation findings in
`usdx-oracle-findings.md`, the observation design in `usdx-oracle-rationale.md` §2 and §4._

---

## The decision, and what is still open

**The question was:** when a backing API is unavailable, should the oracle stop publishing, or
publish a degraded price computed from the sources that did respond?

**The code fails closed** (spec §7, question 15: freeze and let the timestamp age), with one
narrow exception for cash:

- **The bond figure** (`hex_bonds`, the pinned public endpoint) failing fails the observation.
  Nothing replaces it.
- **The cash figure** (`hex_cash`, the restricted endpoint) failing, or not provisioned, does not
  fail the observation on its own. `price.ComputeWithoutCash` publishes a cash-less price **only
  when bonds alone already reach par**, where the missing figure provably cannot change the
  answer; below par it refuses with `BackingUnavailable` (`internal/observation/observation.go`,
  `readBacking`).
- **Any chain** failing its supply read fails the observation.

A failed observation does not stop the feed at once: the machine keeps signing its last good
observation until that is `MaxObservationAge` (10 minutes) old, and then refuses (see "What
'fail closed' actually does").

The decision was made before the implementation showed how asymmetric the redundancy is. Each
backing source is **one URL with no fallback**, while each chain's supply is read from as many RPC
providers as governance publishes for it, at a governance-set quorum. So the feed's availability
is the bond endpoint's availability, and below par the cash endpoint's too. The asymmetry was
never an explicit choice.

**Still open:**

1. **Sign-off by a risk owner.** *Is an hour of stale price worse than an hour of price computed
   from an incomplete formula?* Our reading is no, the analysis below says why, and the code
   implements it. But the answer commits someone to accepting staleness risk over misreporting
   risk, and that is not an engineering call.
2. **Hex's uptime and escalation path** for both endpoints (spec §7, question 8). With no
   fallback source, their uptime *is* our uptime.
3. **A second source for the bond figure**, the one whose failure stops the feed outright.

---

## Background 1 — why supply was read twice, and why it no longer is

> **Resolved 12 Aug 2026 — supply is now read once.** The caveat below is what
> settled it: the double-read tightened a 30-second bracket sitting inside a
> staleness gap roughly twenty times larger. Since each supply read is already
> pinned to one agreed block, the second pass changed the answer only when a burn
> or mint landed in the few seconds between two pinned blocks, while doubling the
> supply-side fail-closed surface. See rationale §2. The reasoning is kept because
> it is why the bracket existed and why removing it is safe.

Asked during review; recording the answer because the reasoning is not obvious from the
code alone.

Supply reads and reserve reads are not atomic, and the resulting error has a
**direction**. `observation.Observe` used to bracket the reserve read between two supply
reads:

```
S₁ ← supply        B ← reserves        S₂ ← supply
S  = max(S₁, S₂)
price = min(1, B / S)
```

The dangerous interleaving is reading reserves first (pre-redemption, high `B`) and
supply after a burn lands (post-redemption, low `S`). `B/S` then comes out
**overstated** — and an overstated price hides a shortfall, which is the one failure
this oracle exists to prevent.

Taking `max(S₁, S₂)` means the larger denominator, so any timing skew pushes the price
*down*. The error is always in the direction that fails safe.

Not theoretical at this scale: the 1,000,000 USDX Flare burn on 31 Jul 2026 was **4.8%
of supply in a single event**, far larger than any deviation threshold we would set. And
redemptions cluster precisely during the stress when the price matters most. The cost is
one extra RPC round per observation.

### Caveat: the double-read covers less than the framing suggests

`checkWindow` (`internal/observation`) computes the consistency spread over
`Reading.RetrievedAt` — when *we* received the response. But `Reading.AsOf` is Hex's own
`last_update`, and at the time its doc comment called the gap between it and the supply
reads the TOCTOU exposure in spec §6.1.

That gap is **not** in the window check. At the time it was bounded only by `MaxAge`,
which for a source believed to tick every ~10 minutes had to be at least 10 minutes. So
the double-read tightened a 30-second bracket sitting inside a staleness gap roughly
twenty times larger — and it could not help there: if `B` is ten minutes stale and a burn
landed five minutes ago, both `S₁` and `S₂` are post-burn while `B` is pre-burn. That is
the bad direction, and `max()` cannot see it.

The double-read was correct; it simply protected a much smaller exposure than the
design language implied. That is what led to dropping it — the exposure it covered
is now closed at a different layer, by pinning each supply read to one agreed block.

**RESOLVED, and in the other direction.** `MaxAge` was not the real TOCTOU bound either;
it has since been removed entirely. Hex confirmed both endpoints are event-driven — they
republish within ~10 minutes of a mint or a redemption and not otherwise — so `AsOf` is a
*change* timestamp with no periodic refresh to bound, and folding it into `checkWindow`
would have failed every observation on a quiet day. `checkWindow` covers read skew and
says so. The reserve lag itself is unbounded and fails safe: a reserve figure trailing a
mint understates `R/S`, and the price is capped at par. See failure-modes, "Backing figures
carry no freshness bound".

---

## Background 2 — what actually happens when a source is down

Tolerance is layered, and it is asymmetric.

| Layer | Behaviour | Verdict |
| --- | --- | --- |
| **RPC providers** (within a chain) | `providerHeads` demotes providers that do not answer; `pinCandidateBlocks` takes the Quorum-th highest claimed head and steps down through the distinct heads below it until `Quorum` providers serve one; `readTotalSupply` then requires `Quorum` **identical** answers with a unique winner. Too few providers answering fails the chain with `SupplyUnavailable`, and no agreement with `SupplyNoQuorum`. | Tolerant of N − `Quorum` dead providers, where N and `Quorum` are what governance published for that chain. The shipped `coston`, `songbird` and `public-only` configurations run every chain at 2-of-3, so one dead provider costs nothing; `coston2.json` runs Songbird at 1-of-1 and Ethereum at 2-of-2, where it costs the chain. |
| **Chains** (Flare / Ethereum / Songbird) | `readAllChains` fails the whole observation if any chain fails. | Intolerant — and correctly so. Dropping Songbird's 1.16M (4.5% of supply) would overstate the price by about that much and hide a shortfall of that size. |
| **Backing API** (bonds) | `readBacking` fails the observation on error. Two attempts, 250 ms apart, transient faults only — 5xx and 429 retry, 4xx and malformed bodies do not. | Intolerant, **and with no redundancy** — a single pinned URL. |
| **Backing API** (cash) | Absent, unprovisioned or failing, the observation continues and `price.ComputeWithoutCash` publishes only where the figure provably cannot matter. Same retry policy when present. | Tolerant **only at par**; below par it fails closed. See "What was implemented instead" below. |

Identical results are required rather than a median because a read at a fixed block is
deterministic: two honest providers cannot disagree, so disagreement means one is wrong
or lying and there is no safe way to average that.

**The gap is the third row.** Supply's redundancy is whatever governance publishes per chain;
the bond figure has one URL.

---

## What "fail closed" actually does

Worth stating precisely, because the phrase sounds worse than the behaviour:

- The poller keeps its last good observation when a later one fails, and `GET_FEED` keeps
  signing it until it is `MaxObservationAge` (10 minutes) old. The age is measured against the
  machine's clock and again against the requesting instruction's block timestamp
  (`internal/observation/poller`). A one-tick outage therefore publishes nothing different.
- Past that, `GET_FEED` refuses with `error: no recent observation` (`ObservationStale`), and no
  new feed update can be published. Which source failed is in the signed readiness report
  (`sourceHealth`, `lastErrorClass`), not in the refusal.
- `TeeOracleFeedStore` keeps the last accepted value and its `observedAt`.
- `TeeOracleFeedStore.getCurrentFeed()` returns that value with its ageing timestamp. It reverts
  `NoValuePublished` before the first update has landed, and on a fee error, but never on
  staleness, specifically because `FtsoV2` forwards the call and a revert would become a
  simultaneous liveness outage for every consumer at once.

The failure mode is **stale and visibly so**, not broken. Consumers are required to check
the timestamp, and that requirement is already on the consumer-docs TODO.

---

## The analysis: should a cash outage block the price?

Cash reserves are ~205,304 against ~25,452,544 of bonds — **0.8% of backing**. Omitting
cash understates backing, which is the conservative direction. The tempting conclusion is
that dropping it is harmless.

Two reasons we recommend against it.

### 1. Structural — a degraded price is indistinguishable from a full one

The signed update, `feedupdate.FeedUpdate`, is `{ExtensionID, FeedID, Value, Decimals,
ObservedAt, EndpointsHash, AdminsHash}`. There is no field for "computed without cash", and the
on-chain payload is price-only **by decision** (spec §4.1, Hex confidentiality). The readiness
report carries no reserve figure either, for the same reason; its `sourceHealth` says whether the
cash source was read, but that reaches the machine's operators, not the feed's consumers.

So a degraded price would reach consumers and auditors as an ordinary one. We would be
silently changing what the published number means, with no channel to say so. The
signature's entire purpose is that the published price is the one the attested code
computed to the published definition.

### 2. Numeric — and this is the decisive one

Under par operations `R_bonds ≈ S` (observed 31 Jul), so dropping cash changes nothing:
both paths give exactly 1.0 after the par cap. It looks free.

But `R_bonds` is a genuine asset measure that can fall below supply (spec §3.0, resolved: it
is a bank-reported balance) — and when it does, cash is precisely the buffer that decides
whether the price is below par:

| Scenario | With cash | Without cash |
| --- | --- | --- |
| Bonds = 1.00 · S | 1.000 (capped) | 1.000 |
| Bonds = 0.99 · S | 0.998 | 0.990 |

On a depeg feed with alerting attached, 0.8 percentage points **is** the signal.
Degradation would manufacture depegs exactly when the feed matters most.

### Therefore

**With spec §3.0 resolved — `R_bonds` is a real balance that can fall below supply — the
numeric argument applies today.** Dropping cash would suppress exactly the buffer that
decides whether a depeg prints, which is a strong reason not to degrade.

---

## Recommendation

The fix for the availability concern is **redundancy, not degradation**.

1. **Give the backing sources fallbacks, the way supply already has them.** One URL per
   source is the single point of failure for the entire feed. For the public bond figure
   a second source may be feasible. For the private cash endpoint it is Hex's one
   endpoint, so it becomes an uptime/SLA conversation — which is why Hex's uptime is one of
   the open items at the top.

2. **Resolve the `AsOf` / `RetrievedAt` inconsistency** in `checkWindow`, or document that
   `MaxAge` is the real bound. **Done, 17 Aug 2026 — neither option.** `MaxAge` was removed:
   Hex republishes on an on-chain event, not on a schedule, so no wall-clock bound on `AsOf`
   is meaningful. `checkWindow` covers read skew and says only that.

3. **Do not implement graceful degradation.** Spec §3.0 is resolved: `R_bonds` is a real
   asset measure, so degradation is actively harmful — it would drop the cash buffer
   that decides whether a depeg prints.

### What was implemented instead, 5 Aug 2026

Recommendation 3 stands and is enforced in code. What shipped is narrower than
degradation and does not weaken it: `price.ComputeWithoutCash` publishes a
cash-less observation **only where the missing figure provably cannot change the
answer**, and fails closed everywhere else.

Cash is non-negative and the price is capped at par. So if the bonds-only price
already reaches par, the complete computation reaches par too, and the number
published is the one a full observation would have produced — not a substitute
for it. The decisive case in the table above is untouched: at `R_bonds = 0.99·S`
this publishes nothing, where degradation would have printed 0.990 against a true
0.998.

| | With cash | Degradation | Implemented rule |
| --- | --- | --- | --- |
| Bonds = 1.00 · S | 1.000 | 1.000 | 1.000 |
| Bonds = 0.99 · S | 0.998 | 0.990 ← the harm | **nothing published** |

Two things make this more than a technicality. `internal/observation/price` rounds
half-up rather than truncating (findings §21), which is what lets the measured
figures — bonds $0.004 below supply, purely because Hex quotes to the cent —
reach par at all. And the readiness report's health map reports the cash source as
`not_configured` (never provisioned) or `down` (failed) rather than dropping it, so a
par-from-bonds observation is distinguishable from a complete one to the machine's
operators, even though the published price is identical by construction.

Recommendation 1 is unaffected: this covers the case where cash is *absent*, not
the case where the bond figure is. That source still has no redundancy, and it is
the one whose failure stops the feed outright.

### Measured again, 16 Aug 2026 — and cash absence turns out to be the normal state

The figures above held: supply 25,452,544.524 against bonds 25,452,544.52, still
$0.004 apart for the same cent-quoting reason. The 1:1 tracking is stable rather
than a one-off of 31 Jul.

What changed is the standing of the cash-less branch. It was written as the
exceptional path; on a three-machine fleet it had become the **routine** one,
because `hex_cash` spent most of its time outside any plausible freshness bound.
Observed `last_update` values sat at `:29:08` roughly **seven hours** apart with
the value unchanged, while `hex_bonds` on the same host stayed seconds fresh —
which rules out credentials, egress and TLS and points at the metric's publishing
cadence. `BackingMaxAge` was split into `BondsMaxAge` (30 min) and `CashMaxAge`
(90 min); on this evidence 90 minutes was still not enough, and the real question
was whether Hex republishes cash on a schedule at all or only on change.

That mattered more than it first reads, because of what §2 established. Bonds are
not a buffer — they track supply. **Cash is the entire 12.6% of
over-collateralisation.** So for most of any given day the feed was running with
no numerical margin at all: the cash-less path publishes par only while bonds
stay within half a ULP of supply, about $0.13 at current scale. Cross that and
the extension publishes *nothing* — `BackingUnavailable` — and the feed ages out.

### Answered, 17 Aug 2026 — the sources are event-driven, and the bound is gone

Hex's answer: **on change, not on a schedule.** Both endpoints, public and
restricted alike, republish within about ten minutes of an on-chain event — a
mint or a redemption — and never otherwise. There is no periodic refresh, and
none was ever offered.

That settles it against the freshness bound entirely, on both sources. The
readings above were not a stalled upstream; they were a quiet seven hours,
reported accurately. `BondsMaxAge`, `CashMaxAge` and `failure.BackingStale` have
been removed rather than widened — a bound whose false-positive rate is set by
USDX trading activity is not a bound, and every rejection it produced discarded a
figure that was correct. Only a `last_update` in the *future* is still refused
(`BackingTimestamp`), because that is clock skew or a broken upstream rather than
an old figure.

The cash-less branch goes back to being exceptional, and the 12.6% buffer is
available in practice rather than only on paper.

This does not change recommendation 3. Degradation would still be harmful, and
failing closed is still right. It **narrows** recommendation 1 without retiring
it: what looked like an availability problem was a measurement problem, so the
cash endpoint is not in fact down most of the day. But it remains a single URL
with no fallback, and the bond figure — the one whose failure stops the feed
outright — still has none either. Mechanism and symptoms are in
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md).

The residual exposure is now stated honestly rather than bounded falsely: a
reserve figure may lag chain state by up to Hex's ~10-minute event latency, and
that lag fails safe — reserves trailing a mint understate `R/S`, and the price is
capped at par. What no longer has *any* guard, at this or any previous
`MaxAge`, is reserves changing without an on-chain event; that is a disclosure
question for Hex, not a freshness one.

Neither (1) nor (2) is a correctness bug in the current code. They are gaps between what
the design claims and what the implementation guarantees.

---

## Source-unreachable alerting, in the keeper

**Status: partly built.** The keeper's cycle records carry each machine's refusal text and the
source health its readiness preflight read (`tools/pkg/keeper`); `oracle-watch` samples every
machine's signed readiness report on the poller's cadence; `cycles-web` serves the records.
Paging, deduplication across keepers and a watcher outside the keeper are not built. The plan is
`usdx-oracle-monitoring.md`.

What remains here is the **placement** reasoning — why this belongs in the keeper and not the
extension — recorded so it is not relitigated. The missed-update taxonomy, the alert routing
tables and the phased build live in the monitoring doc, which also carries every design point
listed at the end of this section.

Fail-closed is only a safe default if somebody finds out. An unreachable source produces a
failed observation and a `sourceHealth` entry, and nothing pages anyone. The `Price < 1` alert
is already on the plan as a business event; **source-unreachable is the missing operational
twin** — and it is the one that fires first, because a source outage precedes the staleness a
consumer would eventually notice.

### This belongs in the keeper, not the extension

The extension is the wrong place, for reasons that are structural rather than stylistic:

- **The extension calls only what the build pins or governance publishes.** Its destinations
  are the pinned bonds URL, the RPC providers governance publishes, and the cash URL committed
  in the same publication. A webhook or paging endpoint would need either a new code hash —
  re-attestation and a version registration — or a governance publication, for a purely
  operational concern.
- **Alerting credentials would become enclave secrets.** A PagerDuty key or Slack webhook
  would join the secret inventory, the provisioning path, and the disclosure surface, for
  no security benefit.
- **`ActionResult.Log` is public**, so the extension is deliberately constrained in what it
  can say. The keeper is not, and it never sees reserve figures anyway — the payload is
  price-only.
- **The extension cannot report its own absence.** If the TEE is down, no `ActionResult`
  comes back at all. Only the caller can detect that, and the caller is the keeper.

The keeper already schedules updates, polls the proxy for results, and submits to the
store. It is the only component that sees the whole round trip.

### Signals already available to it

No extension change is needed; the data is already on the wire.

| Source | Signal |
| --- | --- |
| `ActionResult.Log` | `error: <reason>`, where the reason is one of a closed set of fixed texts (`internal/failure`), each with a class. Route on the reason, through a reason → class table built from `failure.All`. Once the machine has had one good observation, a source outage reaches the log only as `no recent observation`, ten minutes later |
| `GET_READINESS` report | `sourceHealth`, per source and per RPC provider (`rpc_<chain>` and `rpc_<chain>_<n>`): `ok` / `down` / `lagging` / `disagree` / `unknown` / `not_configured` |
| Same | `failures`, `observations`, `lastErrorClass`, `lastObservedAt`, `observationAge`, `wouldSign`, `readiness` |
| Keeper's own timeout | No result at all → TEE or proxy unreachable, distinct from a source outage |

The classes, from `failure.All`:

```
ClassSourceDown     BackingUnavailable, BackingTimeout, SupplyUnavailable, SupplyTimeout,
                    Deadline, NoObservation, ObservationStale
ClassQuorumFailed   SupplyNoQuorum
ClassSourceInvalid  BackingMalformed, BackingCurrency, BackingMetric, BackingTimestamp,
                    SupplyMalformed, SupplyBlockStale, WindowExceeded,
                    UnexpectedPayload, WrongActionType, ProvisionRejected, MissingRequestTime
ClassNotConfigured  NotConfigured     (unprovisioned: credentials are memory-only, so after every restart)
ClassClockSkew      ClockBehindChain
ClassImplausible    BackingImplausible, SupplyImplausible, SupplyZero, PriceImplausible (declared, never returned)
ClassSignFailed     SignUnavailable, SignRejected
ClassInternal       Internal
```

`ClassSourceDown` and `ClassQuorumFailed` are the reachability alerts. The source faults in
`ClassSourceInvalid` (the `Backing*` and `Supply*` reasons and `WindowExceeded`) are a different
kind of problem — the source answered but said something we refuse to use — and should page
differently, because a shape change from Hex needs a human reading the response, not a retry. The
rest of `ClassSourceInvalid` names a malformed request rather than a source, which is why routing
is on the reason and not on the class alone.

### Design points to settle when this is picked up

_Each of these is carried into `usdx-oracle-monitoring.md` — kept here as the record of what
was raised. Update that doc, not this list, if the thinking changes._

- **Alert on sustained failure, not single events.** FCC is fire-and-forget with no
  delivery guarantee, and `TeeOracleFeedStore` treats a rejected submission as normal
  operation. A single failed observation is noise. N consecutive failures, or a time
  threshold, is signal.
- **Distinguish "a machine" from "the sources".** The keeper names its target machines per
  request, so one failure may be one bad machine. Alerting on a single result cannot
  tell the difference; correlating across machines can.
- **Deduplicate across keeper instances.** The plan calls for ≥2 idempotent keepers. Both
  will observe the same outage and must not both page.
- **`sourceHealth` describes the most recent attempt.** The poller observes every 2 minutes on
  its own timer, whether or not anyone requests, and every attempt rewrites the map, so it does
  not freeze when the keeper stops. It says nothing about a machine that is down, which answers
  nothing at all: freshness of the report itself (its timestamp and `observationAge`) still
  needs its own check.
- **Staleness is the consumer-facing alert and is separate.** Source-down is a cause; feed
  age past 2× the update interval is the effect consumers actually feel. Both are wanted;
  only the second is a true page.
- **Per-provider health is reported.** `rpc_flare: "ok"` alone would hide one of three
  providers being down; the per-provider entries beside it (`rpc_flare_1` …) show that state,
  which is exactly the one worth seeing before the next provider goes and quorum fails.

### Related

This is the mitigation half of the decision at the top of this document. The code fails closed,
and alerting is what makes that acceptable, so it belongs alongside the risk owner's sign-off,
not after it.

---

## To decide

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 1 | Is stale-but-honest preferable to fresh-but-incomplete? The code answers yes (fail closed; cash-less only at par); this is the sign-off | _TBD — needs a risk owner_ | Sign-off |
| 2 | What uptime and escalation path does Hex offer on both endpoints? | Hex | Yes, for sizing (1) |
| 2a | ~~Does Hex republish reserves on a schedule or on change?~~ **On change**, within ~10 min of an on-chain event — answered 17 Aug 2026 | Hex | Closed |
| 3 | Is a second source for the bond figure obtainable? | Hex / us | No |
| 4 | ~~Should `checkWindow` bound `AsOf` as well as `RetrievedAt`?~~ **No** — resolved 17 Aug 2026, the sources are event-driven and `MaxAge` was removed | Engineering | Closed |

Question 1 shares a shape with the deviation-guard decision in spec §6.2 — both trade
detection latency against false-signal risk, and both need the same named risk owner.
Worth resolving together.
