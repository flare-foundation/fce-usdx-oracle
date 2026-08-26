# USDX Price Oracle — Plan & TODO

> **Migration note (2026-08).** The on-chain half migrated to the upstream generic
> TEE oracle contracts (`TeeOracleInstructionsSender` + `TeeOracleFeedStore`, op type
> `TEE_ORACLE`, `FeedUpdate` payload). Items below that reference the old contracts,
> op types (`USDX_ORACLE`/`USDX_CONFIG`/`GET_PRICE`), or `UsdxPriceStore` are a record
> of the plan as it stood; the current model is in `usdx-oracle-flows.md` and
> `docs/deployment-steps.md`.

_Working plan. Evidence and reference data: `usdx-oracle-spec.md` (deep technical)._

_**Implementation findings — including security issues found while building — are in
`usdx-oracle-findings.md`.** Read it before touching the extension: it records eight
findings, each pinned by a named test, and four that would publish a credential, a
reserve figure, or a wrong price if reintroduced._

## What & why

USDX's only real market (BitMart) is down, so there's no price for FTSO. We publish a
**backing-based price** instead: a TEE extension reads on-chain supply, asks Hex for
reserves, publishes the ratio as an FTSO custom feed. The TEE holds the API keys and is
pinned by a registered code hash, so consumers verify *what* computed the price rather than
trusting an operator.

```
supply  = USDX totalSupply on Flare + Ethereum + Songbird  (minus exclusions)
backing = bond reserves (public API) + cash reserves (private API)
price   = min(1, backing / supply)      capped at 1: USDX redeems at par
```

## Resolved: the reserves figure is a bank-reported balance

_Resolved 3 Aug 2026. Was: "⚠ Blocker: the reserves figure may be pinned to supply"._

`current_reserves_amount` is sourced from Hex's banking provider — it reports the actual
balance of the reserve account, not a metric computed from chain data. That provenance
resolves the premise question: the figure equals on-chain supply to the cent because Hex
operates 1:1 — a redemption burns tokens and wires the same amount out of the account —
which is exactly what the observed behaviour (to-the-cent match; tracking the 1M burn
within hours) looks like from outside.

The detection capability is therefore real: a bank balance **can** fall below supply — any
outflow with no matching burn (theft, account freeze, operational loss) — and that is
precisely the depeg event this feed exists to catch.

Remaining, as ordinary diligence rather than a blocker: written confirmation from Hex of
the provenance and semantics (Questions for Hex, #1) — the same class of check as the OFT
burn-and-mint confirmation. Evidence and history: findings doc §1 / `usdx-oracle-spec.md` §3.0.

## Decisions

| Decision | Why |
| --- | --- |
| Keeper relays the signed result; TEE sends no transaction | Keeps gas/nonces/stuck-tx out of the enclave. The store verifies the signature, so the keeper is liveness, not trust — anyone can submit. |
| RPC, not FDC, for supply reads | FDC adds a round trip and complexity; the TEE is already the trust anchor. Redundancy instead: 3 providers/chain, 2-of-3 or all-3 — configurable. |
| Query an explicit block number, never a tag | Providers disagree on `latest` — and, measured, on `finalized` too. Resolve the tag to a number per provider, pin the **Quorum-th highest** (the minimum let one lagging or lying provider rewind the observation — findings §24), then query everyone there. |
| Read `decimals()` per chain | They differ: 18 on Ethereum, 6 on Flare/Songbird. |
| On-chain payload carries price only | Hex may consider reserve figures sensitive. Note `GET /state` is public too, so it can't hold them either. |
| Fail closed | Source down or value implausible → publish nothing, let the timestamp age. Never guess a price. |
| Feed never reverts on stale data | `FtsoV2` forwards calls through, so a revert becomes a liveness outage for every consumer. Return last price + ageing timestamp; pause freezes rather than throws. |

## TODO

### Blocked on Hex 🔴

- [x] ~~Resolve the reserves-tracks-supply blocker~~ — resolved: bank-reported balance (above).
      Written confirmation of semantics remains as Q1
- [ ] API key for the private endpoint · [ ] whitelist TEE egress IPs (one static IP per
      machine — decide machine count first)
      _No longer blocks a Coston2 end-to-end run (5 Aug 2026): the observation now runs
      cash-less and publishes where the figure provably cannot matter — findings §21. Still
      required before the feed can print anything below par._
- [ ] Status of the old Ethereum contract (150,000 USDX, excluded from Hex's figure)
- [x] ~~Confirm addresses + decimals~~ — done, see findings §2

### Data sources

- [ ] Clients for both endpoints (private takes an auth header)
- [ ] Validation **per endpoint** — they return different shapes (findings §3). Strip
      thousand separators; parse as decimal, never float64.
- [ ] Timeouts, bounded retries, capped response size

### On-chain supply reads

- [ ] Multi-provider RPC client, 3 endpoints/chain, configurable · [ ] quorum at a fixed block, abort on failure
- [ ] **Decide Songbird coverage** — only one public endpoint exists (findings §4): buy,
      self-host, or accept single-provider risk
- [ ] Per-chain finality margin *N*, kept inside the shallowest provider's state window
- [ ] Decide whether the old Ethereum contract counts toward supply · [ ] exclusion list

### Extension (Go)

- [ ] Op type / commands (`USDX_ORACLE` / `GET_PRICE`) · [ ] decimal arithmetic, no floats
- [ ] Parallel fetch of all 5 sources with a global deadline · [ ] consistency-window check
- [ ] Sign result via `POST /sign` · [ ] error taxonomy → status/log
- [ ] `GET /state`: operational only (last price, price history?, counters, error class,
      source health) — no raw reserve/supply figures
- [ ] Logging that never emits secrets, raw responses, or reserve figures

### Contracts

- [ ] `UsdxInstructionSender.sol` — one `requestPriceUpdate()` · [ ] `TeeExtensionStateVerifier` (scaffold has none)
- [ ] `UsdxPriceStore.sol` — verify sig against `TeeMachineRegistry`, strictly increasing
      `observedAt` (FCC has no ordering guarantee → covers replays *and* out-of-order),
      price bounds, pause
- [ ] `UsdxCustomFeed.sol` — `IICustomFeed`, a thin read adapter over the store (findings §5)
- [ ] Register via governance `addCustomFeeds` — **a proposal with lead time, not a deploy step. Start early.**
- [ ] Agree feed name + category byte with Flare · [ ] Foundry tests incl. fuzzing the store's
      guards · [ ] regenerate Go bindings

### Keeper · Secrets & infra

_Implementation spec: `usdx-oracle-keeper.md` — the three-step cycle, its deadline budget, and why
the keeper is deliberately stateless._

- [x] ~~Schedule updates (single-flight)~~ · [x] ~~poll proxy for results~~ ·
      [x] ~~decode `data` into `(body, signature)` before submitting~~ · [x] ~~submit to store~~
      — **built**: cycle in `tools/pkg/keeper`, timer in `tools/cmd/keeper`,
      one-shot in `tools/cmd/submit-price`. Bounded deadlines, exponential backoff,
      pending-aware, full preflight against the store's guards, graceful shutdown.
- [x] ~~Cycle records~~ — one JSON line per tick, the outcome taxonomy from the monitoring doc.
      **`NotNewer` is classified as success**, not failure: with ≥2 keepers exactly one
      submission wins per cycle and the rest lose the race.
- [x] ~~Read the instruction fee from the registry rather than hard-coding it~~
- [ ] **Replace-by-fee is deliberately not implemented** — a superseded submission is not worth
      bumping. The keeper instead reports `pendingNonceGap`, since a stuck tx blocks every later
      cycle behind its nonce. Decide whether that reporting is enough or RBF is wanted.
- [ ] ≥2 instances (idempotent) — the code is ready for it (`-keeperId`); the deployment is not ·
      [ ] wallet funding + low-balance alerts
- [ ] Secret inventory (Hex + RPC keys) · [ ] inject encrypted to TEE pubkey, decrypt via `POST /decrypt`
- [ ] Rotation without a new code hash · [ ] static egress IP per machine (Cloud NAT) ·
      [ ] reproducible build + code hash registration
- [ ] Write up what a third party can verify from the attestation

### Monitoring

_Missed-update alerting is designed in `usdx-oracle-monitoring.md` — taxonomy, routing tables
and the phased build. Placement reasoning (keeper, not extension) stays in
`usdx-oracle-availability.md`._

- [ ] **Phase 1 — chain-age watcher.** Reads `UsdxPriceStore.latest()`, pages on `age > 2× T`.
      Must run outside the keepers' failure domain: a dead keeper reports nothing, and its
      silence is indistinguishable from health. Catches every missed-update cause at once and is
      the only piece load-bearing on its own. **Prerequisite for mainnet.**
- [ ] **Phase 2 — keeper cycle heartbeat.** One record per cycle (instruction ID, result status,
      error class, submit outcome, store `observedAt` after). Separates "one keeper died" from
      "the feed is stale". **Prerequisite for mainnet.**
- [ ] Phase 3 — classify status-0 results by `failure.Class` and `submitPrice` reverts by error;
      **suppress `NotNewer`**, which is normal operation with ≥2 keepers, not a fault ·
      [ ] Phase 4 — dedup on `(cause, feed)` + cross-keeper and cross-machine correlation
- [ ] **Price < 1 alert** — a business event, not a metric. Pages someone, and needs an agreed
      comms path to Hex. **Check it against `minPrice`:** bounds set at par mean a genuine depeg
      reverts `OutOfBounds` and this alert never fires at all.
- [ ] Hex API health (reachability, **not** `last_update` age — the sources are event-driven) ·
      [ ] per-RPC health and disagreement rate
- [ ] TEE liveness, keeper balance, failed-tx rate · [ ] Phase 5 — dashboard + on-call routing

### Testing · Deployment

- [ ] Formula unit tests (over/under-collateralised, supply=0, rounding) · [ ] mock Hex API
      (malformed, stale, slow, 5xx, **comma and plain values**)
- [ ] Mock RPC incl. provider disagreement · [ ] conformance suite with USDX fixtures · [ ] E2E on Coston2
- [ ] **Shadow run** — compute prices for days without publishing before anyone depends on it
- [ ] Failure drills: Hex down, 1 RPC down, 2 RPCs down, TEE down · [ ] backup and restore
      (just the TEE key, or can we rotate?)
- [ ] Coston2 then mainnet + rollback · [ ] runbook (stale feed, source failure, price drop) ·
      [ ] upgrade path with no feed gap
- [ ] Consumer docs — feed ID, decimals, and that they **must** check the timestamp

## Questions for Hex

1. Confirm in writing: `current_reserves_amount` is the bank-provider balance of the reserve
   account, and an outflow with no matching burn (loss, freeze) shows up in the figure.
   *(Was the blocking question; resolved 3 Aug — now ordinary diligence.)*
2. Does that number already include cash? *(We think not — but it already equals supply on its
   own, which sharpens this rather than mooting it.)*
3. Status of the old Ethereum contract (`0x7A48…D56c`, 150,000 USDX) — a live claim on
   reserves? Currently excluded from your figure.
4. Bonds at market or par? Liquidity haircut?
5. Auth scheme and rate limits on the private endpoint? Public has no `currency` field and uses
   comma separators while your private sample has neither — deliberate and stable?
6. ~~How often are figures recomputed, and does `last_update` move on every redemption?~~
   **Answered:** on change only — within ~10 min of an on-chain event, never on a schedule.
7. Confirm burn-and-mint OFT on every chain, no lock-and-mint adapter anywhere.
8. Uptime expectations and an escalation contact.

## To decide internally

1. **Deviation guard — needs a risk owner.** 🔴 Blocking. Rejecting large jumps protects against
   one bad data point but delays real depeg detection. Straw man: upward moves publish freely, a
   large drop needs ~3 confirmations (~15 min) but pages on the first. Someone must accept that lag.
2. **Songbird RPC coverage** — buy, self-host, or accept one provider for ~4.5% of supply
3. Update frequency and the fee/gas budget — match the 10-min source refresh rather than going faster
4. How many TEE machines — drives the IP whitelist. Any redundancy needed?
5. Does the old Ethereum contract count toward supply? Hex informs it; the policy call is ours.
6. Any logs we need to keep?

## Sequencing

| # | Milestone | Done when |
| --- | --- | --- |
| **0** | **Premise confirmed** | ✅ Resolved 3 Aug — bank-reported balance, falls below supply on any unmirrored outflow. Written confirmation from Hex remains (Q1) |
| **1** | Access secured | API key issued, IPs whitelisted — _still open; no longer blocks 2–4, see findings §21_ |
| **2** | Local prototype | ✅ 5 Aug — price computes end-to-end against real APIs (par, cash-less); unit + conformance + contract suites green |
| **3** | Contracts on Coston2 | ✅ 5 Aug — sender, store and feed deployed, store repointed to the current extension generation |
| **4** | E2E on Coston2 | ✅ **6 Aug** — full path proven: instruction → observation → signed report → `submitPrice` → `getCurrentFeed()` returns `(100000000, 8, 1786004691)`. Below-par and cash-inclusive paths remain unexercised on-chain |
| **5** | Hardening | Monitoring live, runbook written, drills passed, security review done |
| **6** | Mainnet | Feed registered with governance, keepers running, consumers notified |

Milestone 0 is resolved; the design stands. Milestone 1 is still open with Hex but no longer
gates 2–4: the observation runs cash-less and publishes where the figure provably cannot matter.

### Coston2 deployment, as of 15 Aug 2026

Read back from the chain, not from this file's history. The generation below supersedes the
one recorded here through 6 Aug 2026 (sender `0xe14781f3…8555`, extension id `0x101a8`, store
`0x786307b2…cD60`, feed `0xEF67cD9C…2386`) — those contracts still exist and still answer, so
a stale reference does not fail loudly, it just reads a feed nobody updates any more.

| What | Address / value |
| --- | --- |
| `UsdxOracleInstructionSender` | `0x1562e092C807a823fE22dc832b6F20cCe169a8F6` |
| Extension ID | `0x102e9` (66281) |
| `UsdxPriceStore` | `0x4E2f462cfB6CC8567D6A918666B15c04E03B73aE` |
| `UsdxCustomFeed` | `0x56c5F1b72DCA580F7b2B0c07c63973C58627a12D` |
| Feed ID | `0x20555344582f555344…` (`USDX/USD`, category 32) |
| Store bounds / maxAge | `0.5 … 1.0`, 3600 s |

The store and the feed are **verified** on `coston2-explorer.flare.network` via
`./scripts/verify-contracts.sh`, so their acceptance rules are readable by anyone relying on
them, and `latest()` / `getCurrentFeed()` can be called from the explorer's Read Contract tab.
**The instruction sender is not verified**, and it is the one holding the price-controlling
owner key (finding 15) — verifying it is outstanding work, not a cosmetic gap.

The endpoints config hash is deliberately not recorded here: it changes on every
`setEndpoints`, so a copy in a document is stale by construction. Read it from the machine
(`readiness.endpointsHash`) or from `expectedConfigHash(teeId)` on the sender.

**Milestone 4 closed 6 Aug 2026.** First price on-chain: `1.00000000` at 8 decimals,
`observedAt` 1786004691, from instruction `0x7040b32d…cb17` under config hash
`0xf9f4c2be…4a8d`. Walkthrough and how to read the result: `docs/testing.md`.

Note the addresses above are the generation deployed on 5 Aug. Re-running `pre-build.sh` mints
a new extension id and sender, and the store must then be repointed with `set-store-extension`
rather than redeployed — it does not follow automatically.

Two earlier store/sender generations exist on-chain and are superseded — `0xD173d75e…61ba`
(sender) and `0xE77fc35d…e96E` (store, a pre-hardening build with `REQUIRED_TEE_STATUS = 1`).
Neither is referenced by the current deployment.
