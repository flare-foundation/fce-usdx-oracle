# USDX Price Oracle — Plan & TODO

_Working plan. Evidence and reference data: `usdx-oracle-reference.html` (Google-Doc-ready)
or `usdx-oracle-spec.md` (deep technical). Google-Doc-ready plan: `usdx-oracle-plan.html`._

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

## ⚠ Blocker: the reserves figure may be pinned to supply

Hex's `current_reserves_amount` equals total on-chain supply **to the cent**, and tracks it
through changes — when Flare burned exactly 1M USDX this morning, the figure followed within
hours. Whether that's because the metric is derived from chain data, or because Hex is
genuinely 1:1 par-backed, the result is the same: `backing/supply` = 1.000000 by construction
and the feed prints $1.00 forever, unable to detect the depeg it exists to detect.

**The question that settles it:** *under what circumstances would `current_reserves_amount`
ever be less than total on-chain supply?* If the answer is "never" or "only on a manual
write-down", we need a different data source.

**Nothing else on this plan is worth building until that's answered.** Full evidence in the
findings doc §1 / `usdx-oracle-spec.md` §3.0.

## Decisions

| Decision | Why |
| --- | --- |
| Keeper relays the signed result; TEE sends no transaction | Keeps gas/nonces/stuck-tx out of the enclave. The store verifies the signature, so the keeper is liveness, not trust — anyone can submit. |
| RPC, not FDC, for supply reads | FDC adds a round trip and complexity; the TEE is already the trust anchor. Redundancy instead: 3 providers/chain, 2-of-3 or all-3 — configurable. |
| Query an explicit block number, never a tag | Providers disagree on `latest` — and, measured, on `finalized` too. Resolve the tag to a number per provider, take the minimum, query everyone there. |
| Read `decimals()` per chain | They differ: 18 on Ethereum, 6 on Flare/Songbird. |
| On-chain payload carries price only | Hex may consider reserve figures sensitive. Note `GET /state` is public too, so it can't hold them either. |
| Fail closed | Source down or value implausible → publish nothing, let the timestamp age. Never guess a price. |
| Feed never reverts on stale data | `FtsoV2` forwards calls through, so a revert becomes a liveness outage for every consumer. Return last price + ageing timestamp; pause freezes rather than throws. |

## TODO

### Blocked on Hex 🔴

- [ ] **Resolve the reserves-tracks-supply blocker** (above)
- [ ] API key for the private endpoint · [ ] whitelist TEE egress IPs (one static IP per
      machine — decide machine count first)
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

- [ ] Schedule updates · [ ] poll proxy for results · [ ] submit to store (gas, nonce, resubmission)
- [ ] ≥2 instances (idempotent) · [ ] wallet funding + low-balance alerts
- [ ] Secret inventory (Hex + RPC keys) · [ ] inject encrypted to TEE pubkey, decrypt via `POST /decrypt`
- [ ] Rotation without a new code hash · [ ] static egress IP per machine (Cloud NAT) ·
      [ ] reproducible build + code hash registration
- [ ] Write up what a third party can verify from the attestation

### Monitoring

- [ ] Feed freshness (> 2× interval) · [ ] Hex API health + `last_update` staleness ·
      [ ] per-RPC health and disagreement rate
- [ ] **Price < 1 alert** — a business event, not a metric. Pages someone, and needs an agreed
      comms path to Hex.
- [ ] **Source-unreachable alert — build it in the keeper, not the extension.** Fail-closed is
      only safe if someone finds out. Signals are already on the wire (`lastErrorClass`,
      `sourceHealth`, keeper timeout); the extension can't do it without putting a webhook host
      in the code-hash-pinned allowlist. Deferred — see `usdx-oracle-availability.md`.
- [ ] TEE liveness, keeper balance, failed-tx rate · [ ] dashboard + on-call routing

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

1. **Under what circumstances would `current_reserves_amount` ever be below total on-chain supply?** Blocking.
2. Does that number already include cash? *(We think not — but it already equals supply on its
   own, which sharpens this rather than mooting it.)*
3. Status of the old Ethereum contract (`0x7A48…D56c`, 150,000 USDX) — a live claim on
   reserves? Currently excluded from your figure.
4. Bonds at market or par? Liquidity haircut?
5. Auth scheme and rate limits on the private endpoint? Public has no `currency` field and uses
   comma separators while your private sample has neither — deliberate and stable?
6. How often are figures recomputed, and does `last_update` move on every redemption? *(We see a 10-min tick.)*
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
| **0** | **Premise confirmed** | Hex explains the reserves figure and we agree it can actually fall below supply |
| **1** | Access secured | API key issued, IPs whitelisted |
| **2** | Local prototype | Price computes end-to-end against real APIs; unit + conformance green |
| **3** | Contracts on Coston2 | Sender, store, feed deployed and tested |
| **4** | E2E on Coston2 | Instruction → TEE → store → `getCurrentFeed()` returns a price |
| **5** | Hardening | Monitoring live, runbook written, drills passed, security review done |
| **6** | Mainnet | Feed registered with governance, keepers running, consumers notified |

Milestone 0 gates the design, not just the schedule. Milestones 1–2 can proceed against mocks meanwhile.
