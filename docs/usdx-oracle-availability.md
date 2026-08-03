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

## Background 1 — why supply is read twice

Asked during review; recording the answer because the reasoning is not obvious from the
code alone.

Supply reads and reserve reads are not atomic, and the resulting error has a
**direction**. `oracle.Observe` brackets the reserve read between two supply reads:

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

That gap is **not** in the window check. It is bounded only by `MaxAge`, which for a
source refreshing on a ~10 minute tick has to be at least 10 minutes. So the double-read
tightens a 30-second bracket sitting inside a staleness gap roughly twenty times larger
— and it cannot help there: if `B` is ten minutes stale and a burn landed five minutes
ago, both `S₁` and `S₂` are post-burn while `B` is pre-burn. That is the bad direction,
and `max()` cannot see it.

The double-read is still correct and worth keeping. It simply protects a smaller
exposure than the design language implies.

**OPEN:** fold `AsOf` into `checkWindow`, or state explicitly that `MaxAge` is the real
TOCTOU bound and the consistency window only covers read skew. Right now the two
documents disagree with the code.

---

## Background 2 — what actually happens when a source is down

Tolerance is layered, and it is asymmetric. This is the state of the implementation as
of 2026-08-03.

| Layer | Behaviour | Verdict |
| --- | --- | --- |
| **RPC providers** (within a chain) | `agreedBlock` skips providers that error; fails only when surviving providers < `Quorum`. `readTotalSupply` then requires `Quorum` **identical** answers. | Tolerant. 3 providers at quorum 2 → one dead provider costs nothing. |
| **Chains** (Flare / Ethereum / Songbird) | `readAllChains` fails the whole observation if any chain fails. | Intolerant — and correctly so. Dropping Songbird's 1.16M would manufacture a 4.5% depeg. |
| **Backing APIs** (bonds / cash) | `readBacking` aborts on either error. Two attempts, 250 ms apart, transient faults only — 5xx and 429 retry, 4xx and malformed bodies do not. | Intolerant, **and with no redundancy** — a single URL per source. |

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

Today `R_bonds ≡ S` (the §3.0 blocker), so dropping cash changes nothing: both paths
give exactly 1.0 after the par cap. It looks free.

But in the world this oracle is **designed for** — where `R_bonds` is a genuine asset
measure that can fall below supply — cash is precisely the buffer that decides whether
the price is below par:

| Scenario | With cash | Without cash |
| --- | --- | --- |
| Bonds = 1.00 · S | 1.000 (capped) | 1.000 |
| Bonds = 0.99 · S | 0.998 | 0.990 |

On a depeg feed with alerting attached, 0.8 percentage points **is** the signal.
Degradation would manufacture depegs exactly when the feed matters most.

### Therefore

**Whether "drop cash and continue" is safe depends entirely on the unresolved §3.0
question.** That is a good reason not to build it yet, and a good reason the decision
should wait on Hex's answer rather than be taken now.

---

## Recommendation

The fix for the availability concern is **redundancy, not degradation**.

1. **Give the backing sources fallbacks, the way supply already has them.** One URL per
   source is the single point of failure for the entire feed. For the public bond figure
   a second source may be feasible. For the private cash endpoint it is Hex's one
   endpoint, so it becomes an uptime/SLA conversation — which is why the secondary
   question above matters more than its current position on the list suggests.

2. **Resolve the `AsOf` / `RetrievedAt` inconsistency** in `checkWindow`, or document that
   `MaxAge` is the real bound.

3. **Do not implement graceful degradation until §3.0 is answered.** If Hex's answer makes
   `R_bonds` a real asset measure, degradation is actively harmful. If it does not, the
   whole formula changes anyway.

Neither (1) nor (2) is a correctness bug in the current code. They are gaps between what
the design claims and what the implementation guarantees.

---

## Deferred — source-unreachable alerting, in the keeper

**Status: to think about later. Not designed, not scheduled.** Recorded here so the
placement decision is not relitigated.

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
ClassSourceInvalid  BackingMalformed, BackingStale, BackingCurrency
ClassNotConfigured  NotConfigured        (secrets lost to a restart — memory-only by design)
```

`ClassSourceDown` and `ClassQuorumFailed` are the reachability alerts. `ClassSourceInvalid`
is a different kind of problem — the source answered but said something we refuse to use —
and should page differently, because a shape change from Hex needs a human reading the
response, not a retry.

### Design points to settle when this is picked up

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
| 3 | Is a second source for the bond figure obtainable? | Hex / us | No |
| 4 | Should `checkWindow` bound `AsOf` as well as `RetrievedAt`? | Engineering | No |

Question 1 shares a shape with the deviation-guard decision in spec §6.2 — both trade
detection latency against false-signal risk, and both need the same named risk owner.
Worth resolving together.
