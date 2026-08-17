# USDX Oracle — Source Availability: Open Question

**Status:** ⚠ **OPEN QUESTION — needs a decision owner** · **Raised:** 2026-08-03

_Companion docs: design intent in `usdx-oracle-spec.md`, implementation findings in
`usdx-oracle-findings.md`, plan in `usdx-oracle-plan.md`.
Google-Doc-ready: `usdx-oracle-availability.html`._

---

## ⚠ The question

**Today, one unavailable backing API blocks the price entirely. Is that the behaviour we
want, or should the oracle publish a degraded price computed from the sources that
did respond?**

The code fails closed: if either Hex endpoint is down, no price is published and the
feed ages. That is a deliberate design decision (spec §7.15), but it was made before
the implementation revealed that the two backing sources have **no redundancy at all**
— one URL each, no fallback — while the supply reads have three-deep provider
redundancy per chain. The asymmetry was not an explicit choice, and it makes the whole
feed's availability equal to the availability of Hex's two endpoints.

**The question that settles it:** *is an hour of stale price worse than an hour of price
computed from an incomplete formula?* Our reading is no — see the analysis below — but
the answer commits someone to accepting either staleness risk or misreporting risk, and
that is not an engineering call.

**Secondary question, same owner:** what uptime do we actually get from Hex? This is
question 8 on the existing Hex list ("uptime expectations and an escalation contact")
and it should be weighted higher than it currently is, because with no fallback source
their uptime *is* our uptime.

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
**direction**. `oracle.Observe` used to bracket the reserve read between two supply
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

`oracle.checkWindow` computes the consistency spread over `Reading.RetrievedAt` — when
*we* received the response. But `Reading.AsOf` is Hex's own `last_update`, and its doc
comment states that "the gap between this and the supply reads is the TOCTOU exposure in
spec §6.1."

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
mint understates `R/S`, and the price is capped at par. See failure-modes,
"`hex_cash` staleness — resolved".

---

## Background 2 — what actually happens when a source is down

Tolerance is layered, and it is asymmetric. This is the state of the implementation as
of 2026-08-03.

| Layer | Behaviour | Verdict |
| --- | --- | --- |
| **RPC providers** (within a chain) | `agreedBlock` skips providers that error; fails only when surviving providers < `Quorum`. `readTotalSupply` then requires `Quorum` **identical** answers. | Tolerant. 3 providers at quorum 2 → one dead provider costs nothing. |
| **Chains** (Flare / Ethereum / Songbird) | `readAllChains` fails the whole observation if any chain fails. | Intolerant — and correctly so. Dropping Songbird's 1.16M would manufacture a 4.5% depeg. |
| **Backing API** (bonds) | `readBacking` aborts on error. Two attempts, 250 ms apart, transient faults only — 5xx and 429 retry, 4xx and malformed bodies do not. | Intolerant, **and with no redundancy** — a single URL. |
| **Backing API** (cash) | _Changed 5 Aug 2026._ Absent or failing, the observation continues and `price.ComputeWithoutCash` publishes only where the figure provably cannot matter. Same retry policy when present. | Tolerant **only at par**; below par it fails closed. See "What was implemented instead" below. |

Identical results are required rather than a median because a read at a fixed block is
deterministic: two honest providers cannot disagree, so disagreement means one is wrong
or lying and there is no safe way to average that.

**The gap is the third row.** Supply has three-deep redundancy; backing has none.

---

## What "fail closed" actually does

Worth stating precisely, because the phrase sounds worse than the behaviour:

- No new price is published.
- `UsdxPriceStore` keeps the last accepted price and its `observedAt`.
- `UsdxCustomFeed.getCurrentFeed()` **never reverts** — it returns the last price with an
  ageing timestamp. Even a pause freezes rather than throws, specifically because
  `FtsoV2` forwards the call and a revert would become a simultaneous liveness outage for
  every consumer at once.

The failure mode is **stale and visibly so**, not broken. Consumers are required to check
the timestamp, and that requirement is already on the consumer-docs TODO.

---

## The analysis: should a cash outage block the price?

Cash reserves are ~205,304 against ~25,452,544 of bonds — **0.8% of backing**. Omitting
cash understates backing, which is the conservative direction. The tempting conclusion is
that dropping it is harmless.

Two reasons we recommend against it.

### 1. Structural — a degraded price is indistinguishable from a full one

`report.PriceReport` is `{ChainID, Store, FeedID, Price, Decimals, ObservedAt,
InstructionID}`. There is no field for "computed without cash", and the on-chain payload
is price-only **by decision** (spec §4.1, Hex confidentiality). `GET /state` cannot carry
it either, for the same reason.

So a degraded price would reach consumers and auditors as an ordinary one. We would be
silently changing what the published number means, with no channel to say so. The
signature's entire purpose is that the published price is the one the attested code
computed to the published definition.

### 2. Numeric — and this is the decisive one

Under par operations `R_bonds ≈ S` (observed 31 Jul), so dropping cash changes nothing:
both paths give exactly 1.0 after the par cap. It looks free.

But `R_bonds` is a genuine asset measure that can fall below supply (§3.0, resolved: it
is a bank-reported balance) — and when it does, cash is precisely the buffer that decides
whether the price is below par:

| Scenario | With cash | Without cash |
| --- | --- | --- |
| Bonds = 1.00 · S | 1.000 (capped) | 1.000 |
| Bonds = 0.99 · S | 0.998 | 0.990 |

On a depeg feed with alerting attached, 0.8 percentage points **is** the signal.
Degradation would manufacture depegs exactly when the feed matters most.

### Therefore

**With §3.0 resolved — `R_bonds` is a real balance that can fall below supply — the
numeric argument applies today.** Dropping cash would suppress exactly the buffer that
decides whether a depeg prints, which is a strong reason not to degrade.

---

## Recommendation

The fix for the availability concern is **redundancy, not degradation**.

1. **Give the backing sources fallbacks, the way supply already has them.** One URL per
   source is the single point of failure for the entire feed. For the public bond figure
   a second source may be feasible. For the private cash endpoint it is Hex's one
   endpoint, so it becomes an uptime/SLA conversation — which is why the secondary
   question above matters more than its current position on the list suggests.

2. **Resolve the `AsOf` / `RetrievedAt` inconsistency** in `checkWindow`, or document that
   `MaxAge` is the real bound. **Done, 17 Aug 2026 — neither option.** `MaxAge` was removed:
   Hex republishes on an on-chain event, not on a schedule, so no wall-clock bound on `AsOf`
   is meaningful. `checkWindow` covers read skew and now says only that.

3. **Do not implement graceful degradation.** §3.0 is resolved: `R_bonds` is a real
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

Two things make this more than a technicality. `internal/price` now rounds
half-up rather than truncating (findings §21), which is what lets the measured
figures — bonds $0.004 below supply, purely because Hex quotes to the cent —
reach par at all. And `GET /state` reports the cash source as `not_configured`
rather than dropping it, so a par-from-bonds observation is distinguishable from
a complete one even though the published price is identical by construction.

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

**Status: designed — the build plan is `usdx-oracle-monitoring.md`.** _Was "to think about
later, not designed, not scheduled" until 4 Aug 2026._

What remains here is the **placement** reasoning — why this belongs in the keeper and not the
extension — recorded so it is not relitigated. The missed-update taxonomy, the alert routing
tables and the phased build now live in the monitoring doc, which also carries every design
point listed at the end of this section.

Fail-closed is only a safe default if somebody finds out. Today an unreachable source
produces a failed observation and a `sourceHealth` entry, and nothing pages anyone. The
`Price < 1` alert is already on the plan as a business event; **source-unreachable is the
missing operational twin** — and it is the one that fires first, because a source outage
precedes the staleness a consumer would eventually notice.

### This belongs in the keeper, not the extension

The extension is the wrong place, for reasons that are structural rather than stylistic:

- **The host allowlist is baked into the image.** `allowedHosts` in the embedded config is
  code-hash-pinned precisely so configured endpoints can only ever be attested hosts.
  Adding a webhook or paging endpoint would mean a new code hash — re-attestation and a
  version registration — for a purely operational concern.
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
| `ActionResult.Log` | A `failure.Failure` from the closed vocabulary — the error *class* is the alert key |
| Proxy `GET /info` → `TEEState.state` | `sourceHealth` map, per source: `ok` / `degraded` / `down` / `unknown` |
| Same | `failureCount`, `lastErrorClass`, `updateCount`, `lastObservedAt`, `readiness` |
| Keeper's own timeout | No result at all → TEE or proxy unreachable, distinct from a source outage |

The classes that matter for this alert:

```
ClassSourceDown     BackingUnavailable, BackingTimeout, SupplyUnavailable, SupplyTimeout
ClassQuorumFailed   SupplyNoQuorum
ClassSourceInvalid  BackingMalformed, BackingCurrency, BackingTimestamp
ClassNotConfigured  NotConfigured        (secrets lost to a restart — memory-only by design)
```

`ClassSourceDown` and `ClassQuorumFailed` are the reachability alerts. `ClassSourceInvalid`
is a different kind of problem — the source answered but said something we refuse to use —
and should page differently, because a shape change from Hex needs a human reading the
response, not a retry.

### Design points to settle when this is picked up

_Each of these is carried into `usdx-oracle-monitoring.md` — kept here as the record of what
was raised. Update that doc, not this list, if the thinking changes._

- **Alert on sustained failure, not single events.** FCC is fire-and-forget with no
  delivery guarantee, and `UsdxPriceStore` treats a rejected submission as normal
  operation. A single failed observation is noise. N consecutive failures, or a time
  threshold, is signal.
- **Distinguish "a machine" from "the sources".** `getRandomTeeIds` picks one machine per
  instruction, so one failure may be one bad machine. Alerting on a single result cannot
  tell the difference; correlating across machines can.
- **Deduplicate across keeper instances.** The plan calls for ≥2 idempotent keepers. Both
  will observe the same outage and must not both page.
- **`sourceHealth` is a snapshot, not a probe.** It is written only when an observation
  runs. If the keeper stops requesting, the last values persist and continue to read `ok`
  — the failure mode finding 8 was written about, one level up. Freshness of the health
  map needs its own check.
- **Staleness is the consumer-facing alert and is separate.** Source-down is a cause; feed
  age past 2× the update interval is the effect consumers actually feel. Both are wanted;
  only the second is a true page.
- **Per-source or per-chain granularity** is still open (finding 8): `rpc_flare: "ok"`
  hides one-of-three providers being down, which is exactly the state worth seeing before
  the second one goes and quorum fails.

### Related

This is the mitigation half of the open question at the top of this document. If the
answer there is "keep failing closed", then alerting is what makes that acceptable, and it
should be scheduled alongside — not after — the decision.

---

## To decide

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 1 | Is stale-but-honest preferable to fresh-but-incomplete? | _TBD — needs a risk owner_ | Design |
| 2 | What uptime and escalation path does Hex offer on both endpoints? | Hex | Yes, for sizing (1) |
| 2a | ~~Does Hex republish reserves on a schedule or on change?~~ **On change**, within ~10 min of an on-chain event — answered 17 Aug 2026 | Hex | Closed |
| 3 | Is a second source for the bond figure obtainable? | Hex / us | No |
| 4 | ~~Should `checkWindow` bound `AsOf` as well as `RetrievedAt`?~~ **No** — resolved 17 Aug 2026, the sources are event-driven and `MaxAge` was removed | Engineering | Closed |

Question 1 shares a shape with the deviation-guard decision in spec §6.2 — both trade
detection latency against false-signal risk, and both need the same named risk owner.
Worth resolving together.
