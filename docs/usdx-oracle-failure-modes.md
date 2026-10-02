# USDX Oracle — Failure Modes: What Breaks the Feed, and How It Shows

What happens when something upstream goes wrong, which layer catches it, and what an operator
actually sees. The companion to [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md), which
covers alerting policy; this one covers mechanism.

The recurring theme: **almost every failure makes the feed go silent, not wrong.** That is the
design working, but it means "no price" is the normal symptom of nearly everything, and the
interesting question is always *which* silence you are looking at. The one thing that is not
silent is a real shortfall: a sub-par price is a result, and it is published.

---

## The price model, and what can move it

```
price = min(1, (R_bonds + R_cash) / S)
```

Capped at par by `price.Compute`. Three consequences worth internalising:

- The feed **can never print above 1.00.** Over-collateralisation is invisible by construction.
- The only thing that moves it below par is **supply exceeding backing**. There is no lower
  bound: the inputs are non-negative, so the price lies in [0, 1] by construction, and any value
  in that range is published as it is.
- Rounding is half-up and exact in `big.Rat`, so a shortfall smaller than half a ULP legitimately
  prints par. At 8 decimals and ~25M supply that is about **0.13 USD** — deliberate, and guarded
  by `TestCompute_RoundingCannotHideAMaterialShortfall`.

### Where the live feed actually sits

Measured 16 Aug 2026:

| | |
| --- | --- |
| supply (flare 6dp + ethereum 18dp + songbird 6dp) | 25,452,544.524 |
| `R_bonds` | 25,452,544.52 |
| `R_cash` | 3,205,304.47 |

`R_bonds` tracks supply essentially 1:1 — the 0.004 difference is Hex reporting bonds to the cent.
**Bonds alone are not a buffer.** The entire over-collateralisation is the cash figure, at 12.6%.

Note the decimals differ per chain (`6, 18, 6`) and `supply` normalises each by its own
`decimals()`. Assuming a uniform 18 produces a number wrong by six orders of magnitude, which is an
easy and very convincing mistake to make.

---

## Cash-less operation, and the tight spot it creates

When the cash figure is unavailable — the cash source fails in any way, or no cash URL or API key
is provisioned — the extension does **not** substitute zero and publish a degraded price: that
would manufacture a depeg exactly when the feed matters most (the reasoning is
[the analysis in usdx-oracle-availability.md](usdx-oracle-availability.md#the-analysis-should-a-cash-outage-block-the-price)).
Instead `ComputeWithoutCash` publishes only where the missing figure provably cannot change the
answer:

```go
// Below par the missing figure is load-bearing, so this errors rather than degrade.
if value.Cmp(Scale(decimals)) < 0 {
    return nil, failure.BackingUnavailable
}
```

Bonds-only reaching par proves the complete observation would also be par, because cash is
non-negative and the price is capped. Below par it proves nothing, so it refuses.

Whatever went wrong with cash, the refusal is `BackingUnavailable`; the cash source's own fault
shows only in the health map, as `hex_cash: down` (or `not_configured` when it was never
provisioned).

**The operational consequence.** Because `R_bonds ≈ S`, the cash-less path sits within half a ULP
of refusing. While cash is unavailable, if bonds fall behind supply by more than ~0.13 USD at
current scale, the extension stops publishing entirely — `BackingUnavailable`, status 0, no price —
and the on-chain feed's timestamp ages. The store enforces no maximum age; staleness is the
consumer's check.

It does **not** print 0.99. In cash-less mode a sub-par price is unobservable by construction.

So a missing cash figure is not cosmetic. It is the difference between a 12.6% buffer and none.

---

## Backing figures carry no freshness bound

Hex's endpoints — public and restricted alike — are **event-driven**: they republish within about
ten minutes of an on-chain event, a mint or a redemption, and not otherwise. There is no
periodic refresh. `last_update` is therefore a *change* timestamp, and any wall-clock freshness
bound on it would flap, on **either** source. The evidence: cash timestamps observed at `:29:08`
roughly **seven hours** apart with the value unchanged at `3205304.47`, while `hex_bonds` on the
*same host* stayed seconds fresh — which ruled out credentials, egress, DNS and TLS and pointed at
the metric's publishing cadence. A `curl` always looked healthy, because curl has no opinion about
staleness.

So neither backing figure has a maximum age, and no failure in the vocabulary means "backing data
is old". Three things make that safe:

- An old figure is normally the **correct** figure. If nothing minted or redeemed, the reserves
  did not change, and the stamp is old for exactly that reason.
- A genuinely frozen upstream still shows. `R` stops tracking a growing `S`, and
  `price = min(1, R/S)` prints **below** par — the direction that reveals a shortfall rather than
  hiding one.
- An age bound would not cover the dangerous case anyway. Reserves lost *without* an on-chain
  event do not move `last_update`, so no age check would catch that.

What is checked is narrower and true: `last_update` must parse (unparseable → `BackingMalformed`),
and a stamp in the **future** is refused as `BackingTimestamp` — clock skew or a broken upstream,
where nothing else in the response is trustworthy either. Staleness is bounded only where it can be
measured honestly: `MaxObservationAge` (10 min) on our own observation. The store enforces no
`maxAge` on the published feed — staleness there is the consumer's check.

**What this means operationally.** `hex_cash` reads `ok` whenever it answers, so the health map
does not distinguish "answered with a figure from this morning" from "answered with a fresh
one" — and it should not, because that distinction is no evidence of a fault. Alert on the
**on-chain feed's age** instead; a genuinely dead cash endpoint still surfaces as `down`.

---

## What each failure does

| Condition | Failure | Result |
| --- | --- | --- |
| never provisioned: no endpoint configuration applied, or a credential an endpoint needs missing | `NotConfigured` | no price; `readiness.ready=false` |
| endpoints applied but no admin set installed | `NotConfigured` | observes and reports health, refuses to sign |
| machine just started, first observation not finished | `NoObservation` | no price until the poller's first observation completes |
| bonds source unreachable, 5xx or 429 | `BackingUnavailable` | no price — bonds have no substitute |
| bonds source timed out | `BackingTimeout` | no price |
| cash down **and** bonds-only below par | `BackingUnavailable` | no price (see above) |
| cash down **and** bonds-only at par | — | price publishes, cash-less |
| backing figure old | — | **accepted**; `last_update` moves on an on-chain event, not on a clock |
| backing timestamp in the future | `BackingTimestamp` | no price — clock skew or a broken upstream |
| backing shape changed, other 4xx (401/403 included), unparseable value or time | `BackingMalformed` | no price — unknown JSON fields are rejected, not ignored |
| backing response names another metric than the pinned `current_reserves_amount` / `current_cash_reserves_amount` | `BackingMetric` | no price |
| non-USD currency | `BackingCurrency` | no price |
| too few supply providers answered (none, for the supply read itself) | `SupplyUnavailable` | no price |
| supply providers answered but fewer than the group's threshold agree, at every candidate head | `SupplyNoQuorum` | no price |
| supply provider round ran out of time | `SupplyTimeout` | no price |
| the agreed block timestamp or `decimals()` is out of range | `SupplyMalformed` | no price |
| the pinned block's header is older than the chain's `MaxBlockAge` (2 min Flare and Songbird, 3 min Ethereum) | `SupplyBlockStale` | no price — the providers agree, on a block too old to use |
| supply reads zero | `SupplyZero` | no price — an undefined ratio, not an infinite one |
| inputs read more than 30 s apart | `WindowExceeded` | cannot fire: the 20 s observation deadline binds first |
| the 20 s observation deadline runs out | `SupplyTimeout` / `BackingTimeout` | no price; `Deadline` is declared and never returned |
| newest successful observation older than 10 min | `ObservationStale` | refuses to sign |
| observation older than 10 min *by the requesting block's clock* | `ClockBehindChain` | refuses to sign — the machine's clock is behind chain time |
| GET_FEED instruction with no block timestamp | `MissingRequestTime` | refuses to sign — a zero would disable the bound above |
| GET_FEED request that is not one `FeedUpdateRequest` for this feed | `UnexpectedPayload` | refuses, without observing |
| price below par | — | **published** — a sub-par price is a result, not a failure |

Everything above that refuses is **fail-closed**. The feed stops; it does not guess.
`PriceImplausible` is declared and never returned: there is no plausibility band on the price.
The signing failures (`SignUnavailable`, `SignRejected`) and the rest of the vocabulary, with
each reason's class, are in
[usdx-oracle-monitoring.md](usdx-oracle-monitoring.md#cause-4--routing-a-status-0-result).

### The 10-minute bound is measured twice

`MaxObservationAge` is checked against two different clocks, and the pair is the point. An
observation's inputs are timed by the Flare pinned block's header timestamp, a clock the host
cannot move. `Poller.Latest()` measures the age with the machine's own clock — which is what
catches upstreams that have stopped, but a machine whose clock runs behind understates that age
and passes its own check however old the reading is. `Poller.Report()` then re-checks the same age
against the **requesting instruction's block timestamp**, which the data-provider quorum cosigned
and the host cannot move either. `ObservationStale` means the upstreams stopped;
`ClockBehindChain` means this machine's clock did.

The bound runs in one direction only. An observation *newer* than the requesting block is
ordinary, not suspicious: delivery is fire-and-forget, so an instruction can sit in a queue
while the poller keeps observing. It needs no check, because the observation's own time is never
what gets signed.

The requesting timestamp is what gets signed as `observedAt`. The store defines the field as
the timestamp of the on-chain event that triggered the observation, and a batch from several
machines must share it, so every machine answering one request signs the same value. The
inputs behind it were read at most `MaxObservationAge` earlier — that is what the second
check enforces — and the reading's own time stays visible as `lastObservedAt` in the readiness
report. The store's `TooFarAhead` (`observedAt` must be strictly before the accepting block)
guards the ratchet against a future-dated update; an honest update, stamped with a block that is
already mined, can never trip it.

### `down` is six different things

`healthOf(err)` collapses every backing error to `down`, which reads like "cannot reach it" but
also covers a timeout (`BackingTimeout`), a `401`/`403` or a changed shape (`BackingMalformed`),
a wrong currency (`BackingCurrency`), a wrong metric (`BackingMetric`) and a future timestamp
(`BackingTimestamp`). When diagnosing, go to the failure reason rather than the health word — the
vocabulary is precise even though the health map is not.

The RPC side is finer-grained, and the distinctions are load-bearing. Each chain has a verdict
key, `rpc_<chain>`, and one key per provider, `rpc_<chain>_<i>`, numbered from 1 in the
published order:

| Health | Meaning |
| --- | --- |
| `ok` | answered and agreed |
| `down` | did not answer (for a chain key: the chain's read failed) |
| `disagree` | answered with a **wrong** value and lost the vote — a different problem from silence |
| `lagging` | head below the pinned block; tolerated, but also the shape of a rewind attempt |
| `not_configured` | never provisioned — send a `SET_SECRET`, do not page an outage |
| `unknown` | never polled; the zero value, so an unpolled source never reads as healthy |

---

## The store's own guards

Even correctly signed updates can be refused on-chain. `TeeOracleFeedStore.submitFeedUpdates`
judges the batch as a whole, in this order:

| Guard | Behaviour |
| --- | --- |
| batch shape | at least `requiredSignatures` updates and at most 32, all stamped with one `observedAt`, otherwise `NotEnoughSignatures` / `TooManySignatures` / `ObservedAtMismatch`; the contributors' `decimals` may differ by at most 8, otherwise `DecimalsSpreadTooBig` |
| spread | the two values bracketing the median must lie within the governance-set bound (`maxSpreadBIPS` of the median plus `maxSpreadAbsolute`), otherwise `SpreadTooBig`; machines beyond the bound are named in `FeedOutliers` rather than rejected |
| `observedAt` | strictly increasing (`NotNewer`) and strictly before `block.timestamp` (`TooFarAhead`, unreachable for an honest update) |
| endpoints / admins published | the feed must have both published at all, otherwise `NoEndpointsPublished` / `NoAdminsPublished` |
| signatures | each verified through `Fdc2Verification`: the extension not emergency paused (`ExtensionEmergencyPaused`), a well-formed signature (OpenZeppelin's `ECDSA*` errors), a signer on the store's extension (`InvalidTeeMachineExtensionId`) and `PRODUCTION` (`TeeMachineNotAvailable`); every signer distinct (`DuplicateTeeId`) |
| `extensionId` / `feedId` | must match the store's, otherwise `WrongExtensionId` / `WrongFeedId` |
| endpoints / admins hash | every update must carry the feed's latest published generation (`latestEndpointsHash` / `latestAdminsHash`), otherwise `StaleEndpoints` / `StaleAdmins` |

The store enforces no price band and no maximum age, and neither does the enclave: the price is
in [0, 1] by construction, with the par cap at 1.00 and no lower bound. A genuine collapse is
therefore **published**, as a status-1 result and an accepted submission, and is detected on the
published value — see the cause-6 note in
[usdx-oracle-monitoring.md](usdx-oracle-monitoring.md#cause-6--classifying-a-submitfeedupdates-revert).
Staleness is the consumer's check: `getCurrentFeed()` returns the last value with its ageing
timestamp, and reverts only `NoValuePublished`, before the first update.

`NotNewer` is **not** a fault when the store is already at or past the update: the store accepts
at most one update per request timestamp, and a keeper whose update was overtaken records
`race_lost`.

---

## Testing the unhappy paths

### You cannot fake the bonds figure

`HexBondsURL` is a pinned constant, not configuration (`internal/config`):

> *The bonds endpoint is pinned: redirecting it would hand whoever publishes configuration the
> numerator of the price.*

There is no `SET_ENDPOINTS` path to it, by design. A test that needs a different bonds figure has
to change the constant and rebuild.

### The cash figure is governed, not pinned

The cash URL is provisioned at runtime. Governance publishes it as a PRIVATE salted commitment
in the `hex_cash` endpoint group — exactly one entry, threshold 1, secret ref `hex_cash_url` — and
a backing admin provisions the URL itself over `SET_SECRET`; the enclave accepts it only if it
matches the commitment. The response must still name the pinned metric,
`current_cash_reserves_amount`, and carry the shape the parser accepts. So a mock cash source needs
both a published commitment to its URL and the backing set's signatures. On the development
server (`cmd/dev/devserver`) both are in the tester's hands; on a live deployment that is a
governance publication and the backing admins, never a test lever.

### You can fake the supply

Supply providers *are* governed configuration, which is the lever. A mock changes the total only
if it can carry the chain group's vote: in `deploy/config/endpoints/coston2.json` Songbird is
1-of-1, so a single mock provider decides it; in `coston.json`, `songbird.json` and
`public-only.json` Songbird is 2-of-3, so one mock is outvoted (it reads `disagree`) and changing
the total takes two agreeing mocks. A mock needs two JSON-RPC methods:

- `eth_getBlockByNumber` — the head, and the pinned block's header for the quorum and the age
  check; the header's timestamp must be within the chain's `MaxBlockAge` (2 min for Songbird), or
  the read fails `SupplyBlockStale`
- `eth_call` for `totalSupply()` (`0x18160ddd`), returning an inflated `uint256`, and
  `decimals()` (`0x313ce567`)

From there:

| To observe | Requires |
| --- | --- |
| **silent feed** (`BackingUnavailable`) | cash unavailable, supply pushed a little above bonds |
| **sub-par price** | cash **available**, supply pushed above `bonds + cash` |

The second needs the cash figure present and answering: on a machine running cash-less — no cash
URL or API key provisioned, or the cash source failing — over-supply produces silence rather than
a sub-par price.

### Where to run it

Locally, `cmd/dev/devserver` runs the real extension with real provisioning and no chain, so a mock
supply endpoint exercises the whole path without touching the feed.

On a live deployment it cannot be confined to one machine. The endpoint configuration is one
governance publication per feed, delivered to every machine, so a mock provider changes what the
whole fleet reads, and its value is published — or, when the machines' values differ beyond the
spread bound, the batch fails `SpreadTooBig` and the feed stalls. It leaves a false observation in
the feed's history either way, so it belongs on a test deployment of your own, never on a feed
someone consumes.

---

_Companion docs: [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md) (what to alert on) ·
[usdx-oracle-availability.md](usdx-oracle-availability.md) (why cash is not degraded away) ·
[usdx-oracle-mint-redeem-dynamics.md](usdx-oracle-mint-redeem-dynamics.md) (why backing moves) ·
[usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md) (asking a machine how it is) ·
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md) (registration and upgrades)._
