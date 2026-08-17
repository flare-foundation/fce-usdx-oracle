# USDX Oracle — Failure Modes: What Breaks the Feed, and How It Shows

What happens when something upstream goes wrong, which layer catches it, and what an operator
actually sees. The companion to [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md), which
covers alerting policy; this one covers mechanism.

The recurring theme: **almost every failure makes the feed go silent, not wrong.** That is the
design working, but it means "no price" is the normal symptom of nearly everything, and the
interesting question is always *which* silence you are looking at.

---

## The price model, and what can move it

```
price = min(1, (R_bonds + R_cash) / S)
```

Capped at par by `price.Compute`. Three consequences worth internalising:

- The feed **can never print above 1.00.** Over-collateralisation is invisible by construction.
- The only thing that moves it below par is **supply exceeding backing**.
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

When the cash source is unavailable the extension does **not** substitute zero and publish a
degraded price — that would manufacture a depeg exactly when the feed matters most (the reasoning
is [usdx-oracle-availability.md](usdx-oracle-availability.md) §2). Instead `ComputeWithoutCash`
publishes only where the missing figure provably cannot change the answer:

```go
// Below par the missing figure is load-bearing, so this errors rather than degrade.
if value.Cmp(Scale(decimals)) < 0 {
    return nil, failure.BackingUnavailable
}
```

Bonds-only reaching par proves the complete observation would also be par, because cash is
non-negative and the price is capped. Below par it proves nothing, so it refuses.

**The operational consequence.** Because `R_bonds ≈ S`, the cash-less path sits within half a ULP
of refusing. While cash is unavailable, if bonds fall behind supply by more than ~0.13 USD at
current scale, the extension stops publishing entirely — `BackingUnavailable`, status 0, no price —
and the feed ages out past the store's `maxAge` of 3600s.

It does **not** print 0.99. In cash-less mode a sub-par price is unobservable by construction.

So a stale cash figure is not cosmetic. It is the difference between a 12.6% buffer and none.

---

## `hex_cash` staleness — resolved, and the check is gone

**Was:** the cash endpoint returned `200` with valid JSON and the extension marked it `down`
anyway, because `backing.parse` rejected it on freshness against a `MaxAge` bound. Observed cash
timestamps sat at `:29:08` roughly **seven hours** apart with the value unchanged at
`3205304.47`, while `hex_bonds` on the *same host* stayed seconds fresh — which ruled out
credentials, egress, DNS and TLS and pointed at the metric's publishing cadence. A `curl` always
looked healthy, because curl has no opinion about staleness.

**Resolved by Hex:** both endpoints — public and restricted alike — are **event-driven**. They
republish within about ten minutes of an on-chain event, a mint or a redemption, and not
otherwise. There is no periodic refresh, and none was ever promised.

So `last_update` is a *change* timestamp, and the first of the two readings above was the right
one: **any** wall-clock freshness bound flaps, on **either** source. The bound has been removed
rather than widened. `BondsMaxAge` / `CashMaxAge` and `failure.BackingStale` no longer exist.

Three things make that safe:

- An old figure is normally the **correct** figure. If nothing minted or redeemed, the reserves
  did not change, and the stamp is old for exactly that reason.
- A genuinely frozen upstream still shows. `R` stops tracking a growing `S`, and
  `price = min(1, R/S)` prints **below** par — the direction that reveals a shortfall rather than
  hiding one.
- The bound never covered the dangerous case anyway. Reserves lost *without* an on-chain event
  do not move `last_update`, so no age check would have caught that.

What survives is narrower and true: `last_update` is still parsed (unparseable → `BackingMalformed`)
and a stamp in the **future** is still refused as `BackingTimestamp` — clock skew or a broken
upstream, where nothing else in the response is trustworthy either. Staleness is now bounded only
where it can be measured honestly: `MaxObservationAge` (10 min) on our own observation, and the
store's `maxAge` (3600s) on the published feed.

**What this changes operationally.** `hex_cash` now reads `ok` whenever it answers, so the health
map no longer distinguishes "answered with a figure from this morning" from "answered with a fresh
one" — and it should not, because that distinction was never evidence of a fault. Alert on the
**on-chain feed's age** instead; a genuinely dead cash endpoint still surfaces as `down` through
`BackingUnavailable` / `BackingTimeout`.

The 12.6% buffer this was costing is now available in practice, not only on paper — the cash-less
path in the next section goes back to being the exceptional case it was written as.

---

## What each failure does

| Condition | Failure | Result |
| --- | --- | --- |
| never provisioned | `NotConfigured` | no price; `readiness.ready=false` |
| bonds source down | `BackingUnavailable` | no price — bonds have no substitute |
| cash down **and** bonds-only below par | `BackingUnavailable` | no price (see above) |
| cash down **and** bonds-only at par | — | price publishes, cash-less |
| backing figure old | — | **accepted**; `last_update` moves on an on-chain event, not on a clock |
| backing timestamp in the future | `BackingTimestamp` | no price — clock skew or a broken upstream |
| backing shape changed | `BackingMalformed` | source `down` — unknown JSON fields are rejected, not ignored |
| non-USD currency | `BackingCurrency` | no price |
| RPC quorum unmet | `SupplyUnavailable` | no price |
| supply reads zero | `SupplyZero` | no price — an undefined ratio, not an infinite one |
| observation older than 10 min | `ObservationStale` | refuses to sign |
| price outside plausible range | `PriceImplausible` | no price |

Everything above is **fail-closed**. The feed stops; it does not guess.

### `down` is five different things

`healthOf(err)` collapses every backing error to `down`, which reads like "cannot reach it" but
also covers a `401`/`403` (via `BackingMalformed`), a timeout, a wrong currency, and stale data.
When diagnosing, go to the failure class rather than the health word — the vocabulary is precise
even though the health map is not.

The RPC side is finer-grained, and the distinctions are load-bearing:

| Health | Meaning |
| --- | --- |
| `ok` | answered and agreed |
| `down` | did not answer |
| `disagree` | answered with a **wrong** value and lost the vote — a different problem from silence |
| `lagging` | head below the pinned block; tolerated, but also the shape of a rewind attempt |
| `not_configured` | never provisioned — send a `SET_SECRET`, do not page an outage |
| `unknown` | never polled; the zero value, so an unpolled source never reads as healthy |

---

## The store's own guards

Even a correctly signed price can be refused on-chain. Live values:

| Guard | Value | Behaviour |
| --- | --- | --- |
| `minPrice` / `maxPrice` | 0.50 / 1.00 | outside → `OutOfBounds`, the feed **freezes rather than publishing a collapse** |
| `maxAge` | 3600 s | consumers see the feed as stale; the store does not revert |
| `observedAt` | strictly increasing | replay guard; a non-newer report is `NotNewer` |
| signer status | must be `PRODUCTION` | otherwise `UnknownSigner` |
| config / admins hash | must match published | otherwise `StaleConfig` / `StaleAdmins` |

`maxPrice` of exactly 1.00 mirrors the extension's par cap, so the two halves agree by
construction. `minPrice` of 0.50 is a judgement: a genuine collapse below 50c is treated as more
likely to be a fault than a fact, and freezing is preferred to publishing it. That is a deliberate
trade — see `OutOfBounds` in [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md), which argues it
deserves its own alert precisely because it is the one guard that can hide a real event.

`NotNewer` is **not** a fault: with more than one keeper, exactly one submission wins each cycle and
the rest correctly record `race_lost`.

---

## Testing the unhappy paths

### You cannot fake the backing

`HexBondsURL` and `HexCashURL` are pinned constants, not configuration:

> *pinned: redirecting the reserve read would hand an operator the numerator of the price.*

There is no `SET_ENDPOINTS` path to them, by design. Any test that needs a different backing figure
has to change the constant and rebuild.

### You can fake the supply

Supply providers *are* operator-configurable, which is the lever. **Songbird is 1-of-1**, so a
single mock provider changes the total. A mock needs only three JSON-RPC methods:

- `eth_blockNumber` and `eth_getBlockByNumber` — for the pinned-block quorum logic
- `eth_call` for selector `0x18160ddd` (`totalSupply()`), returning an inflated `uint256`

From there:

| To observe | Requires |
| --- | --- |
| **silent feed** (`BackingUnavailable`) | cash unavailable, supply pushed a little above bonds |
| **sub-par price** | cash **healthy**, supply pushed above `bonds + cash` |

The second is the one people expect to test first and the one that is currently blocked: while
`hex_cash` is stale, over-supply produces silence rather than a sub-par price.

### Where to run it

Locally, `cmd/devserver` runs the real extension with real provisioning and no chain, so a mock
supply endpoint exercises the whole path without touching the feed.

On a live machine it is contained to that machine — the others keep publishing par and will
overwrite it, and a `SET_ENDPOINTS` back to the real config reverses it. It does leave a false
observation in the feed's history, so it belongs on a testnet only. A useful side effect: with one
machine on mock supply the others should report `disagree`, which is itself worth confirming.

---

_Companion docs: [usdx-oracle-monitoring.md](usdx-oracle-monitoring.md) (what to alert on) ·
[usdx-oracle-availability.md](usdx-oracle-availability.md) (why cash is not degraded away) ·
[usdx-oracle-mint-redeem-dynamics.md](usdx-oracle-mint-redeem-dynamics.md) (why backing moves) ·
[usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md) (asking a machine how it is) ·
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md) (registration and upgrades)._
