# USDX Price Oracle — FCE Extension Spec (DRAFT v0.1)

**Status:** draft for review
**Owner:** _TBD_
**Last updated:** 2026-07-31

> This is a first pass meant to be edited. Anything marked **OPEN** is a real
> unresolved question, not a placeholder for prose. Anything marked **ASSUMPTION**
> is something we guessed and should confirm before building on it.

> **Implementation findings live in `usdx-oracle-findings.md`.** Where this spec
> and that document disagree, the findings doc wins: it records what was verified
> against source and pinned by a test, including the signing-path details in §WP3
> (confirmed against tee-node `v0.0.21-0.20260619120252-31fc839ae6d2`) and two
> credential-disclosure paths this spec does not mention.

### Decisions log

| Date | Decision | Rationale |
| --- | --- | --- |
| 2026-07-31 | **Keeper relays the signed result** rather than the TEE broadcasting its own transaction via PMW | Keeps gas, nonce and stuck-tx handling out of the enclave. The store verifies the signature, so the keeper is a liveness convenience, not a trusted party. |
| 2026-07-31 | **On-chain payload carries the price only**, not the supply/backing components | Reserve figures may be commercially sensitive to Hex. See §4.1 — this constrains `GET /state` as well, which is *not* private. |
| 2026-07-31 | Deviation-guard policy (§6.2) deferred — **needs a named risk owner** | Trades depeg-detection latency against manipulation resistance; not an engineering call. |
| 2026-07-31 | **`getCurrentFeed()` never reverts on staleness** — returns the last price with an ageing timestamp | `FtsoV2` forwards the call, so a revert becomes a liveness outage for every downstream consumer at once. See WP4. |
| 2026-07-31 | **`calculateFee()` returns 0** | Unlike the sFLR/stXRP feeds we have no underlying FTSO read to pay for. |
| 2026-08-03 | **§3.0 premise resolved** — `current_reserves_amount` is the bank-provider balance of the reserve account | Provenance from the team. The observed supply-tracking is 1:1 treasury operation, not derivation from chain data; the figure falls below supply on any unmirrored outflow. Written confirmation from Hex remains as diligence. |
| 2026-08-05 | **Round half-up instead of truncating** (§3.5) | Hex quotes reserves to the cent; truncation turned a $0.004 quantisation artifact into a permanent sub-par print. Half a ULP is $0.127 at current supply — 12× the worst-case quantisation, 5e-9 of the float — so the concealable shortfall stays bounded and no material depeg is affected. Findings §21. |
| 2026-08-05 | **A missing cash figure no longer fails the observation outright** | Published only where the missing figure provably cannot change the answer: cash is non-negative and the price caps at par, so a bonds-only price already at par *is* the full result. Everything below par still fails closed, so the availability doc's objection to degradation is untouched. Findings §21. |
| 2026-08-05 | **GET_PRICE carries a one-byte request envelope**, `0x01`, not an empty message | `sendInstructions` reverts `MessageEmpty()`, so `requestPriceUpdate()` could never dispatch. The extension requires exactly that byte, preserving the property the empty message was chosen for. Findings §22. |
| 2026-08-05 | **Observation moved onto a timer; GET_PRICE only signs** | tee-node allows an extension 2 s per action (`settings.ProxyTimeout`, a const in its internal package); a complete observation measures 2.0–2.9 s, so every request timed out. `internal/observe` observes every 2 min and `MaxObservationAge` (10 min) bounds the resulting staleness. Findings §23. |

---

## 1. Problem

USDX is a stablecoin issued by Hex Trust (HT Digital Assets). Its only meaningful
market venue was BitMart, which is now down, so there is no usable market price
to feed into FTSO. Existing consumers on Flare need a price for USDX.

Without a market price, the next-best truth is **collateralisation**: USDX is
backed by a reserve portfolio held by Hex. If reserves ≥ circulating supply, the
token is fully backed and a price of $1.00 is defensible. If reserves fall short,
the defensible price is the backing ratio.

## 2. Goal

Build a Flare Compute Extension (FCE) that runs inside a TEE and:

1. reads USDX circulating supply on Flare, Ethereum and Songbird via RPC,
2. reads Hex's reserve figures from two HTTP APIs (one public, one authenticated),
3. computes a price from the backing ratio,
4. produces a TEE-signed result that is submitted on-chain and exposed as an
   **FTSO custom feed**.

The TEE is what makes this trustworthy: the API key for the private endpoint and
the RPC credentials live only inside the enclave, and the code that consumes them
is pinned by a registered code hash, so consumers can verify *what* computed the
price, not just trust an operator.

### Non-goals (v1)

- Any market-price component (order books, OTC quotes, TWAP).
- Redemption, minting, or any write path against Hex.
- Supporting chains beyond Flare / Ethereum / Songbird.
- Historical backfill of the feed.

---

## 3. Price definition

### 3.0 Premise check — resolved (was blocking)

**Resolved 3 Aug 2026.** `current_reserves_amount` is sourced from Hex's banking
provider: it reports the actual balance of the reserve account, not a metric
computed from chain data. The evidence below is retained as the record of why
this was in question — and of what disciplined 1:1 treasury operation looks like
from outside.

`R_bonds` equals total on-chain supply **to the cent**, and tracks it through
changes.

| Evidence (31 Jul 2026) | |
| --- | --- |
| Supply, 3 live contracts | `25,452,544.524018` |
| API `current_reserves_amount` | `"25,452,544.52"` |
| Flare burn at 07:10:58Z, block 66310829 | `21,620,459.685024 → 20,620,459.685024` (−1,000,000 exactly) |
| API value after that burn | matches the **post-burn** sum |

Of the two readings we could construct — (1) a metric derived from chain supply,
(2) genuine 1:1 par-backed operation — the bank-balance provenance selects (2):
a redemption burns tokens and wires the same amount out of the account, so the
figure tracks supply exactly while both sides move honestly.

The consequence for the design is the opposite of what we feared: a real account
balance **can** fall below supply, on any outflow with no matching burn (theft,
account freeze, operational loss), and that is exactly the depeg event the feed
exists to detect.

**Remaining (ordinary diligence, not blocking):** written confirmation from Hex
of the provenance and semantics — see the consolidated questions (§7).

*Limitation of our evidence: the API exposes no history, so tracking was
inferred from one observed supply change rather than a time series.*

### 3.1 Inputs

| Symbol | Source | Notes |
| --- | --- | --- |
| `S_flare` | `USDX.totalSupply()` on Flare | ERC-20, **6 decimals** |
| `S_eth` | `USDX.totalSupply()` on Ethereum | ERC-20, **18 decimals** |
| `S_sgb` | `USDX.totalSupply()` on Songbird | ERC-20, **6 decimals** |
| `E` | excluded balances | see 3.3 |
| `R_bonds` | Hex public API `current_reserves_amount` | see §3.0 |
| `R_cash` | Hex private API `current_cash_reserves_amount` | cash held for redemptions |

**Verified contract addresses** (on-chain, 31 Jul 2026):

| Chain | Address | Decimals |
| --- | --- | --- |
| Flare | `0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B` | 6 |
| Songbird | `0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B` | 6 |
| Ethereum (current) | `0xf8750b54d86BE7aE9e32b4A0C826811198D63313` | **18** |
| Ethereum (old, pre-migration) | `0x7A486F809c952a6f8dEc8cb0Ff68173F2B8ED56c` | 6 |

Decimals differ per chain — normal for a LayerZero OFT, and a live footgun.
`decimals()` must be read per chain, not assumed.

### 3.2 Formula

```
S = (S_flare + S_eth + S_sgb) - E          # circulating supply, USD-denominated (1 USDX = 1 unit)
B = R_bonds + R_cash                        # total backing, USD

price = 1.0                if B >= S
price = B / S              if B <  S
```

i.e. `price = min(1, B / S)`, with a hard guard for `S == 0` (→ no update, error
status).

We deliberately cap at 1: over-collateralisation does not make USDX worth more
than a dollar, since it is redeemable at par.

### 3.3 Supply accounting — mostly resolved

Naive summing is only correct if USDX is natively minted per chain. **Evidence
says it is.** USDX is a LayerZero OFT, and on both Flare and the current Ethereum
contract `token()` returns the contract's own address — the native burn-and-mint
OFT pattern, not a lock-and-mint adapter (an adapter returns a *different*
underlying ERC-20). Bridging therefore burns on the source chain and mints on the
destination, and summing does not double-count.

Corroborating: the public reserves figure matches the three-chain sum exactly
(§3.0), which it could not do if a locked balance were being counted twice.

Still to confirm with Hex:

- No lock-and-mint adapter on *any* chain, including ones we don't read.
- **The old Ethereum contract** (`0x7A48…D56c`, 150,000 USDX at 6 decimals) is
  excluded from Hex's reserves figure. Is it a live claim? If we count it and Hex
  doesn't, we publish a spurious ~0.6% depeg. Today the cash buffer masks this;
  that is luck, not design.
- **Songbird counts.** Its 1.16M is inside Hex's reserves figure, which is
  counter-intuitive for a canary network — worth an explicit confirmation.
- Any addresses whose balance is not circulating (treasury, unissued inventory,
  burn address).
- Any other chains with USDX supply that we are not reading.

Until answered, `E` is a **configurable list of (chain, address) exclusions** read
from extension config, so we can adjust without a code-hash change. **OPEN:** or
should exclusions be code-hash-pinned for auditability? Trade-off: config is
operationally flexible but weakens the "the code decides" guarantee.

### 3.4 Reserve accounting — **OPEN**

- Does `current_reserves_amount` (public) already **include** cash reserves? If
  yes, `B = R_bonds` and adding cash double-counts.
- Are bonds valued at market or at par? Mark-to-market matters for the ratio.
- Should bonds get a haircut for liquidity/duration risk (e.g. 0–2% for T-bills)?
  A haircut factor `h` would make `B = R_cash + (1 - h) * R_bonds`, with `h`
  governance-set. **Proposal: support `h`, default 0.**
- Currency: both endpoints report `"currency": "USD"`. Do we hard-fail if
  `currency != "USD"`? **Proposal: yes, fail closed.**

### 3.5 Output encoding

FTSO custom feeds return `(uint256 value, int8 decimals, uint64 timestamp)`.

- **decimals:** proposed `8` — gives 1e-8 resolution around $1.00. The interface
  returns `int8` and imposes no constraint, so this is purely our choice.
- **value:** `round(price * 10^decimals)`. `1.0` → `100_000_000`.
- **rounding:** round **half-up**. _Changed 5 Aug 2026; was truncation, "so we never
  over-report backing"._ Truncation is the safe direction, but the error it was
  protecting against measured **$0.004**: Hex quotes reserves to the cent while supply
  carries 6 decimals on Flare/Songbird and 18 on Ethereum, so the bond figure is supply
  with up to $0.01 of quantisation removed — and truncating turned that into a permanent
  0.99999999 print. Half a ULP is **$0.127** at current supply (5e-9 of the float),
  twelve times the worst-case quantisation, so rounding absorbs the artifact while the
  shortfall it can conceal stays bounded by construction. A 1% depeg is unaffected.
  Findings §21.
- **feed id:** see §3.6 — **verified** against `flare-smart-contracts-v2`.

### 3.6 Feed ID encoding — verified

Source: `contracts/ftso/implementation/FtsoFeedIdConverter.sol`.

```solidity
function getFeedId(uint8 _category, string memory _name) external pure returns (bytes21) {
    bytes memory nameBytes = bytes(_name);
    require(nameBytes.length <= 20, "name too long");
    return bytes21(bytes.concat(bytes1(_category), nameBytes));
}
```

A feed ID is `bytes21` = **1 category byte + 20 bytes ASCII name**, right-padded
with zeros by the `bytes21` cast.

**Category is a range, not a fixed value.** `FtsoV2._isCustomFeedId`:

```solidity
function _isCustomFeedId(bytes21 _feedId) internal pure returns (bool) {
    uint8 category = uint8(bytes1(_feedId[0]));
    return 32 <= category && category < 64;
}
```

| Range | Use |
| --- | --- |
| `0`–`31` (`0x00`–`0x1F`) | reserved system / standard feeds |
| **`32`–`63` (`0x20`–`0x3F`)** | **custom feeds** — routed via the `customFeeds` registry |
| `64`–`255` | unused |

So any byte in `0x20`–`0x3F` is valid; the specific value is our choice, subject
only to the resulting full ID not colliding with an already-registered feed.
(An earlier draft of this doc named `0x21` — that happens to be inside the valid
range, but it was a guess, not a requirement.)

Taking category `0x20` and name `"USDX/USD"`:

```
0x20  555344582f555344  000000000000000000000000
 └cat  └ "USDX/USD"       └ 12 zero bytes of padding

feedId = 0x20555344582f555344000000000000000000000000
```

**OPEN:** confirm the category byte and the exact feed name with Flare — the name
is what integrators will hard-code, so it should be agreed before registration,
not after.

---

## 4. Architecture

```
                 ┌──────────────────────── keeper / relayer (off-chain) ────────────────────────┐
                 │                                                                              │
                 │  (1) requestPriceUpdate()                    (4) submitPrice(result, sigs)   │
                 ▼                                                                              ▼
   ┌─────────────────────────────┐                                       ┌──────────────────────────────┐
   │ UsdxInstructionSender.sol   │                                       │ UsdxPriceStore.sol           │
   │  → TeeExtensionRegistry     │                                       │  verify TEE sig + registry   │
   │    .sendInstructions()      │                                       │  monotonic ts, sanity bounds │
   └──────────────┬──────────────┘                                       └──────────────┬───────────────┘
                  │ TeeInstructionsSent event                                           │
                  ▼                                                                     ▼
        relay client (data providers)                                    ┌──────────────────────────────┐
                  │ cosigned instruction                                 │ UsdxCustomFeed.sol           │
                  ▼                                                      │  IICustomFeed → FtsoV2       │
   ┌──────────────────────────────────────────────┐                      └──────────────────────────────┘
   │ TEE proxy  ──►  TEE machine (Confidential Space)                     
   │                  ├── tee-node   (attestation, keys, /sign, /decrypt)
   │                  └── usdx-oracle extension  (POST /action, GET /state)
   │                        ├─► RPC ×3 providers × 3 chains  → totalSupply
   │                        ├─► Hex public API   → bond reserves
   │                        └─► Hex private API  → cash reserves   (API key, IP-whitelisted)
   └──────────────────────────────────────────────┘
                  │ (3) signed ActionResult
                  ▼
            TEE proxy result store  ──► GET /action/result/<actionId>
```

Flow:

1. Keeper calls `requestPriceUpdate()` on our InstructionSender (payable — the
   registry charges a per-instruction fee).
2. Registry emits an instruction event; data providers' relay clients cosign and
   deliver it to the selected TEE machine(s).
3. Extension fetches all inputs, computes the price, and returns an
   `ActionResult` whose `data` is the ABI-encoded price payload **plus a TEE
   signature** obtained from `POST /sign` on `$SIGN_PORT`. This makes the result
   self-contained and independently verifiable on-chain.
4. Keeper reads the result from the proxy (`/action/result/<actionId>`) and
   submits it to `UsdxPriceStore`, which verifies the signature against
   `TeeMachineRegistry` before accepting.

**No cosigners:** both instructions are sent with an empty `cosigners` list and
`cosignersThreshold` 0, so a machine executes on delivery without waiting for
external co-signatures. Adding cosigners later changes instruction dispatch and
delivery only — see the findings doc.

**DECIDED:** the keeper relays the signed result (as drawn). The TEE does not
broadcast its own transaction — no PMW, no gas or nonce handling inside the
enclave.

### 4.1 What is public, and what is not

This matters more than it looks, because **the extension's `GET /state` is not
private**. tee-node polls it and folds the response into `TEEState.state`, which
is attested and then published by the TEE proxy at `GET /info`. Anything the
extension puts in `/state` is world-readable with a signature attached.

Consequence of the price-only payload decision: if Hex considers the reserve
components confidential, they can go in **neither** the on-chain payload **nor**
`/state`. The surfaces are:

| Surface | Visibility | May carry reserve components? |
| --- | --- | --- |
| `ActionResult.data` → `UsdxPriceStore` | public, on-chain, permanent | no (decided) |
| `GET /state` → `TEEState.state` → proxy `/info` | public, attested | **no** — same exposure |
| Extension logs | **OPEN** — see below | probably, if egress is controlled |

So `/state` carries only non-sensitive operational data: last published price,
last update timestamp, success/failure counters, last error class, per-source
health. No raw supply or reserve figures.

**OPEN:** where do extension logs actually go from a Confidential Space VM, and
who can read them? If they land in Cloud Logging under an account Flare controls,
that is a disclosure surface Hex needs to agree to. Resolve before logging any
reserve figure. Until then: log the *derived price* and error classes only.

**Audit status (2026-08-04).** Every log statement and error surface was
audited against this rule (findings doc §17). Nothing logs a credential, a
reserve or supply figure, a response body, or an endpoint URL: internal detail
is confined to error classes, source identifiers, and values already public
on-chain. The OPEN above is therefore about *whether* logs leave the machine,
not about what they carry.

**OPEN:** consumers cannot audit `min(1, B/S)` without `B` and `S`. If Hex is
willing to publish the components after some delay, a lagged or periodic
disclosure would restore auditability without revealing live positions. Worth
proposing to them.

### 4.2 Why RPC and not FDC

We considered attesting the supply reads through FDC. Rejected for v1: it adds a
round trip and considerable complexity for data we can read directly, and the TEE
is already the trust anchor. Instead we get robustness from **redundancy**: three
independent RPC providers per chain (Flare public, Ankr, QuickNode — all
configurable), so no single provider is a point of failure or a manipulation
vector.

---

## 5. Work packages

Each row is a candidate ticket. Owner/estimate to be filled in.

### WP1 — Data sources: Hex APIs

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 1.1 | Obtain API key for the private endpoint | Hex | in progress |
| 1.2 | Get TEE egress IPs whitelisted by Hex (see WP6.4) | | blocked on 1.1 |
| 1.3 | Client for public `current_reserves_amount` | | |
| 1.4 | Client for private `current_cash_reserves_amount` (auth header) | | |
| 1.5 | Response validation: schema, `currency == USD`, `last_update` freshness, value parseable as decimal, non-negative, within plausible bounds | | |
| 1.6 | Timeouts, bounded retries, no unbounded response body | | |
| 1.7 | Agree an SLA / uptime expectation with Hex; what happens on their maintenance windows | | |

Endpoints:

- Public: `GET https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount`
- Private: `GET https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount`

Private response shape (confirmed by Hex):

```json
{
  "metric_name": "current_cash_reserves_amount",
  "value": "205304.47",
  "currency": "USD",
  "last_update": "2026-07-31T09:07:21Z"
}
```

**The two response shapes are NOT identical** — verified by calling the public
endpoint directly (31 Jul 2026):

```json
{"last_update":"2026-07-31T12:27:21Z","metric_name":"current_reserves_amount","value":"25,452,544.52"}
```

| | Public | Private (Hex's sample) |
| --- | --- | --- |
| `currency` field | **absent** | present, `"USD"` |
| thousands separators in `value` | **yes, commas** | no |

Consequences for 1.5:

- A single shared response struct with a `currency == "USD"` assertion would
  **fail closed forever on the public endpoint.** Validate per endpoint.
- `"25,452,544.52"` does not parse as a decimal. Separators must be stripped —
  but validate the shape rather than blindly deleting commas, since comma-as-
  decimal-separator is a real locale convention and blind stripping turns
  `"1,50"` into `150`.
- Parse as arbitrary-precision decimal, never float64.
- `last_update` ticked 12:17:21 → 12:27:21 with an unchanged value, suggesting a
  **10-minute refresh cadence**. If real, polling faster than that gains nothing.

**OPEN:** how is the private endpoint authenticated — bearer token, `X-API-Key`,
mTLS? It currently returns **404** (not 401/403) without credentials, so we cannot
distinguish "unauthorised" from "wrong path" until the key arrives.
**OPEN:** are the formatting differences deliberate and stable, or an accident of
two different serializers? Handle both formats on both endpoints regardless.

### WP2 — On-chain supply reads

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 2.1 | ~~Confirm USDX contract addresses~~ — **done**, §3.1 | | ✅ |
| 2.2 | ~~Confirm token decimals per chain~~ — **done**: 6 / 6 / **18**. Read at runtime anyway; never hard-code | | ✅ |
| 2.2b | Decide whether the old Ethereum contract counts toward supply (§3.3) | | |
| 2.3 | Multi-provider RPC client: 3 endpoints per chain, configurable | | |
| 2.4 | Quorum rule across providers (see below) | | |
| 2.5 | Block-finality policy per chain | | |
| 2.6 | Exclusion-list handling (`balanceOf` on excluded addresses) | | |
| 2.7 | Per-provider timeout, retry, and failover behaviour | | |

**Quorum rule — proposal.** For each chain, query all 3 providers for
`totalSupply()` **at an explicit block number** (not `latest`, which differs per
provider). Procedure: ask each provider for its head, take the *minimum* head
across providers, subtract the finality depth, and query all three at that exact
block. Require **≥ 2 of 3 identical** results; if fewer, the read fails and the
whole update aborts. Identical (not median) is right here because reads at a
fixed block are deterministic — disagreement means something is wrong.

**Finality — revised after measurement.** The original proposal ("Ethereum: use
the `finalized` tag") is wrong as stated. Surveyed 31 Jul 2026: all live Ethereum
endpoints support `finalized` and sit ~83 blocks (~17 min) behind head, but
`1rpc.io` resolved it **32 blocks earlier** than the others. The tag is not a
shared truth — it has the same disagreement problem as `latest`.

**Corrected rule, uniform across chains:** ask each provider to resolve its
finality reference (`finalized` on Ethereum, `latest` on Flare/Songbird) to a
block *number*, take the **minimum** across providers, subtract the per-chain
margin, then query every provider at that explicit number.

**Measured block times:** Flare 1.11 s, Songbird 1.07 s, Ethereum 12.01 s. `N` is
per-chain, not global — 60 s of margin is ~54 blocks on Flare, ~5 on Ethereum.

**State-retention constraint.** Provider state depth ranges from *latest only* to
full archive (measured: `eth.merkle.io` latest-only, `publicnode`/`1rpc` 64 blocks,
`blockrazor` 128, `drpc`/`mevblocker` ≥100k). `N` must stay inside the shallowest
configured provider's window or that provider drops out of quorum every round.
`eth.merkle.io` cannot serve explicit-block queries at all and is unusable here
despite being reachable.

**We never need `eth_getLogs`** — only `eth_call`, `eth_blockNumber` and
`eth_getBlockByNumber`. Worth recording because Flare's public RPC caps log ranges
at 30 blocks, which looks like a blocker until you notice we don't use it.

### 2.8 Endpoint inventory

Full surveyed table lives in `usdx-oracle-plan.md`. The operationally important
result: **Songbird has exactly one working public RPC**
(`songbird-api.flare.network`), so the 3-provider rule cannot be met there with
free endpoints. Flare has four, Ethereum five usable. Production should assume
keyed/paid primaries (QuickNode, dRPC, Ankr premium) with public endpoints as the
third leg — three endpoints named in the original plan had already died by the
time of the survey.

### WP3 — Extension logic (Go)

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 3.1 | Define op type / commands (`USDX_ORACLE` / `GET_PRICE`) and `bytes32` constants | | |
| 3.2 | Decimal arithmetic for the ratio — big.Rat or shopspring/decimal, no floats | | |
| 3.3 | Orchestration: parallel fetch of 3 chains + 2 APIs, with a global deadline | | |
| 3.4 | Consistency window enforcement (see §6.1) | | |
| 3.5 | Result payload encoding + `POST /sign` for the TEE signature | | |
| 3.6 | `GET /state`: last price, last update ts, counters, last error class, per-source health — **no raw supply or reserve figures** (§4.1: `/state` is public) | | |
| 3.7 | Error taxonomy → `ActionResult.status` / `log` (fail closed, never publish a guessed price) | | |
| 3.8 | Structured logging that never logs secrets, full API responses, or reserve figures (§4.1) | | |

Result payload (`ActionResult.data`) — **decided**. Canonical definition lives in
`usdx-oracle-extension.html` §6; summarised here:

```solidity
struct PriceReport {
    bytes32 domain;         // keccak256("USDX_ORACLE_PRICE_V1")
    uint256 price;          // round(price · 10^decimals), half-up
    int8    decimals;       // 8
    uint64  observedAt;     // store's monotonicity key — must be signed
    bytes32 instructionId;
}

body = abi.encode(report)
data = abi.encode(body, signature)   // carry body as raw bytes, never re-encode
```

The payload carries **no target binding** (no chain id, store address or feed
id): where a report is accepted is settled by the store's registered-machine
check rather than by the payload. A genuinely-signed report can therefore be
submitted to any deployment wired to the same extension registration, though
nobody can forge a price. Decided pre-launch; see the findings doc.

Price only. Supply and backing components are excluded as possibly sensitive to
Hex — and for the same reason excluded from `GET /state`, which is public (§4.1).
Consumers therefore cannot recompute `min(1, B/S)` from on-chain data; see §4.1
for a possible lagged-disclosure remedy.

**Signing path — verified in tee-node source.** `POST /sign` does
`crypto.Sign(accounts.TextHash(crypto.Keccak256(message)), key)`, so:

- **EIP-191 prefixing is applied by tee-node.** The store must recover against
  `toEthSignedMessageHash(keccak256(body))`, not the bare hash.
- **`v` is returned as 0/1**, while `ecrecover` and OpenZeppelin's `ECDSA.recover`
  need 27/28. Decide which side adds 27, and test that exactly one does.
- Request/response are **base64**, not hex (Go `[]byte` marshalling).

Note this is a *second, separate* signature from the FCC result signature over
`hash(hash(data), id, hash(submissionTag), status)` — that one is for the proxy
and is explicitly not for external use.

### WP4 — Contracts

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 4.1 | `UsdxInstructionSender.sol` — from scaffold, one `requestPriceUpdate()` | | |
| 4.2 | Access control on who may request an update (keeper allowlist vs open) | | |
| 4.3 | `TeeExtensionStateVerifier` — required by `register()`; scaffold has none | | |
| 4.4 | `UsdxPriceStore.sol` — signature verification, replay/ordering guards, sanity bounds, pause | | |
| 4.5 | `UsdxCustomFeed.sol` implementing `IICustomFeed` — see below | | |
| 4.6 | Agree category byte + feed name with Flare | | |
| 4.7 | Feed registration via governance `addCustomFeeds` — **has a lead time, start early** | | |
| 4.8 | `go generate ./...` / `./scripts/generate-bindings.sh` after contract edits | | |
| 4.9 | Foundry tests incl. fuzz on the store's guards and the feed's staleness behaviour | | |

`UsdxPriceStore` acceptance rules — proposal:

- signature recovers to a `teeId` that is **registered and active** for our
  extension ID in `TeeMachineRegistry`;
- `observedAt > lastObservedAt` (rejects replays *and* out-of-order delivery —
  FCC is explicitly "fire and forget" with no ordering guarantee);
- `observedAt` within `[now - maxAge, now + MAX_FUTURE_SKEW]`, where
  `MAX_FUTURE_SKEW` is a fixed 2 minutes. The forward window absorbs ordinary
  clock drift between the TEE and the chain so a slightly-early report can be
  submitted immediately rather than held. It is bounded because `observedAt` is
  a one-way ratchet with no reset: an accepted future timestamp blocks honest
  reports until chain time catches up, so the window is also the worst-case
  feed freeze — keep it far below `maxAge`. Consumers are unaffected: `latest()`
  clamps the published timestamp to chain time, so `block.timestamp - timestamp`
  is always safe even while `observedAt` is briefly ahead;
- `price` within `[minPrice, maxPrice]` governance bounds (e.g. `[0.5, 1.0]`) —
  a value outside the band is rejected, not clamped, and raises an alert;
- optional deviation guard: reject a move larger than `maxDeviationBps` from the
  stored price **unless** it is the K-th consecutive such observation. See §6.2 —
  this is a genuine tension and needs a decision.
- governance `pause()` that freezes the feed at its last value.

Anyone may submit a valid signed result; the keeper is a liveness convenience,
not a trusted party. This is deliberate.

#### `IICustomFeed` — verified against `flare-smart-contracts-v2`

Source: `contracts/customFeeds/interface/IICustomFeed.sol`. The whole interface is
three functions:

```solidity
interface IICustomFeed {
    function getCurrentFeed() external payable returns (uint256 _value, int8 _decimals, uint64 _timestamp);
    function feedId() external view returns (bytes21 _feedId);
    function calculateFee() external view returns (uint256 _fee);
}
```

Four things about it that shape our implementation:

**1. `getCurrentFeed()` is `payable` and *not* `view`.** It cannot be marked
`view` in our implementation even though we only read storage — the mutability
must match the interface. Consumers therefore call it as a transaction, or via
`staticcall` off-chain. `FtsoV2.getFeedById` forwards `msg.value` straight
through to it.

**2. We are a *primary* feed, unlike both shipped examples.** `SFlrCustomFeed`
and `StXrpCustomFeed` are *derivative* — they read an underlying FTSO feed from
`FastUpdater` and rescale it by an exchange rate. We have no underlying feed; we
serve a value our own TEE produced. That is explicitly allowed (the registry only
cares that the ID is in range and the contract implements the interface), but it
means we cannot copy either example wholesale. The nearest thing to a template is
their structure, not their logic.

**3. `calculateFee()` should return 0.** The shipped examples defer to
`FeeCalculator.calculateFeeByIds([referenceFeedId])` because their consumers are
effectively paying for the underlying feed read. We have no underlying read to
pay for. **Proposal: return 0** — free reads. Revisit only if there's a reason to
meter access.

**4. Reverting is a consumer-facing failure.** `FtsoV2.getFeedById` forwards the
call, so if `getCurrentFeed()` reverts, the consumer's call reverts. This makes
the staleness decision from §7.15 concrete and load-bearing:

> **Decision: `getCurrentFeed()` must never revert on staleness.** Return the last
> stored price with its true (ageing) `_timestamp` and let consumers apply their
> own freshness policy. Reverting would turn a stale-data problem into a liveness
> outage for every downstream protocol at once.

The same applies to the governance pause: pausing must freeze the value, not make
the feed throw.

**Registration is a governance action.** `FtsoV2.addCustomFeeds(IICustomFeed[])`
is `onlyGovernance` and reverts with `"invalid feed category"` outside 32–63 or
`"feed already exists"` on a duplicate ID. This is a Flare governance proposal
with whatever timelock that carries — treat it as a scheduling dependency, not a
deploy step, and start it well before we need the feed live.

**We can upgrade the feed contract without changing the ID.**
`replaceCustomFeeds(IICustomFeed[])` repoints an existing feed ID at a new
contract address. Useful: integrators hard-code the feed ID, so a bug fix in
`UsdxCustomFeed` doesn't force them to re-integrate. Worth keeping the feed
contract thin (a read adapter over `UsdxPriceStore`) so that most changes land in
the store or the extension rather than requiring a governance action.

### WP5 — Keeper / relayer

_Implementation spec: `usdx-oracle-keeper.md`. It corrects two lines of the cycle sketch that
were not implementable against the deployed contracts — `requestPriceUpdate()` returns `void`
rather than the instruction ID, and `ActionResult` has no `signature` field._

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 5.1 | Scheduler: trigger `requestPriceUpdate()` every `T` | | |
| 5.2 | Poll TEE proxy for the result; handle timeouts and missing results | | |
| 5.3 | Submit result + signatures to `UsdxPriceStore` | | |
| 5.4 | Gas management, nonce handling, resubmission on stuck tx | | |
| 5.5 | Hot-wallet funding + low-balance alerting | | |
| 5.6 | Run ≥ 2 independent keeper instances (idempotent submissions) | | |
| 5.7 | Deployment: where does it run, who operates it | | |

**OPEN:** update cadence `T`. A stablecoin backing ratio moves slowly, but a
depeg needs fast detection. Straw man: **every 5 minutes**, plus an immediate
update if the computed price deviates from the stored one by more than X bps.
Cost per update = registry instruction fee + gas × 2 transactions.

### WP6 — Secrets, attestation, infrastructure

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 6.1 | Inventory of secrets: Hex private API key, RPC provider keys (Ankr, QuickNode) | | |
| 6.2 | Secret injection encrypted to the TEE public key; decrypt via `POST /decrypt` on `$SIGN_PORT` | | |
| 6.3 | Key rotation procedure without a code-hash change | | |
| 6.4 | **Static egress IP** per TEE machine for Hex IP whitelisting (GCP Cloud NAT) | | |
| 6.5 | Reproducible build + code hash registration (`addTeeVersion`) | | |
| 6.6 | Publish the attestation story: what a third party can verify and how | | |

6.4 is on the critical path and easy to miss: Confidential Space VMs need a
deliberately configured NAT with a reserved static IP, and **every TEE machine
serving this extension needs its IP whitelisted**. If we run N machines, Hex must
whitelist N addresses, and adding a machine later is a coordination step with
them, not a self-service action. Decide the machine count now.

### WP7 — Monitoring & alerting  (owner: Podlogar)

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 7.1 | Feed freshness alert (no update in > 2×T) | Podlogar | |
| 7.2 | Price deviation alert (price < 1 at all; price < threshold) | Podlogar | |
| 7.3 | Hex API health + `last_update` staleness | Podlogar | |
| 7.4 | Per-RPC-provider health and disagreement rate | Podlogar | |
| 7.5 | TEE machine liveness / attestation status | Podlogar | |
| 7.6 | Keeper wallet balance, failed-tx rate | Podlogar | |
| 7.7 | Instruction failure rate by error class | Podlogar | |
| 7.8 | Dashboard + on-call routing | Podlogar | |

A price below $1.00 is a **business event**, not just a metric — it should page
someone, and there should be an agreed comms path to Hex when it fires.

### WP8 — Testing

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 8.1 | Unit tests for the formula: over/under-collateralised, `S == 0`, rounding, extreme values | | |
| 8.2 | Mock Hex API server (incl. malformed, stale, slow, 5xx responses) | | |
| 8.3 | Mock/forked RPC for supply reads, incl. provider disagreement | | |
| 8.4 | Conformance suite (`./scripts/test-conformance.sh`) with USDX fixtures | | |
| 8.5 | Foundry tests for the store and feed contracts | | |
| 8.6 | E2E on Coston2: instruction → TEE → result → store → feed read | | |
| 8.7 | Shadow run: compute prices for N days without publishing, compare against expectation | | |
| 8.8 | Failure drills: Hex API down, one RPC down, two RPCs down, TEE down | | |

### WP9 — Deployment & operations

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 9.1 | Coston2 deployment following `docs/deployment-steps.md` | | |
| 9.2 | Mainnet deployment plan + rollback | | |
| 9.3 | Runbook: what to do when the feed goes stale / a source fails / price drops | | |
| 9.4 | Code-version upgrade path (`addTeeVersion`, machine upgrade, no feed gap) | | |
| 9.5 | Consumer documentation: feed ID, decimals, semantics, staleness contract | | |

### WP10 — Security review

| # | Task | Owner | Status |
| --- | --- | --- | --- |
| 10.1 | Threat model write-up (§6) | | |
| 10.2 | Internal review of the extension + contracts | | |
| 10.3 | External audit — **OPEN:** in scope/budget? | | |
| 10.4 | Agree the disclosure path with Hex for reserve-data issues | | |

---

## 6. Security considerations

### 6.1 Timing / TOCTOU between reserves and supply

The core correctness risk. Supply reads and reserve reads are not atomic, and a
**redemption that lands between them** changes both sides of the ratio:

- Reserves read *before* the redeem (high) + supply read *after* the burn (low)
  → price **over-stated**.
- Supply read *before* the burn (high) + reserves read *after* (low) → price
  **under-stated**.

For a single redemption the error is bounded by the redemption size relative to
supply, which is normally tiny — but it is exactly the kind of thing that gets
large precisely during a stress event, when the price matters most.

Mitigations, in rough order of preference:

1. **Tight window.** Do all reads within a short bounded window (e.g. 10s),
   record a timestamp for each, and abort the update if the spread exceeds the
   window. Cheap, and bounds the exposure.
2. **Conservative double-read.** Read supply → read reserves → re-read supply,
   and use `max(supply)` with `min(reserves)`. This makes any timing skew bias
   the price *downward*, never upward. Costs one extra round of RPC reads.
   **Implemented, then withdrawn 12 Aug 2026.** Each supply read is pinned to one
   agreed block, so the bracket only covered a burn or mint landing in the few
   seconds between two pinned blocks, while doubling the supply-side fail-closed
   surface — see rationale §2 and availability "Background 1". Mitigation 1 is
   the operative one; mitigation 3 remains the cheapest real fix.
3. **Ask Hex for a consistent snapshot** — an endpoint that returns reserves with
   an explicit `as_of` timestamp (the private one already has `last_update`), and
   ideally both figures from one atomic query. Then require
   `|as_of − block_time| < threshold`. **Worth asking for; the cheapest real fix.**
4. Reject the update if `last_update` is older than `maxReserveAge`.

**This is not hypothetical.** Observed on Flare: supply fell 45.1M → 20.6M over
240 days, and a **single 1,000,000 USDX burn landed at 07:10:58Z on 31 Jul 2026** —
4.8% of supply in one event. A redemption of that size landing mid-update moves
the naive ratio by ~4.8%, which is far larger than any plausible deviation
threshold. Mitigation 2 (conservative double-read) was judged worth its cost at
the time; see the note above for why it was later withdrawn.

**Partially answered:** the public endpoint refreshes on a ~10-minute tick
(`last_update` advanced 12:17:21 → 12:27:21 → 12:37:21 with an unchanged value).
So sub-10-minute consistency windows are theatre against that source, and we
should lean on mitigations 2 and 4. **Still OPEN:** whether `last_update` jumps
immediately on a redemption or only on the next scheduled tick.

### 6.2 Depeg detection vs manipulation resistance

A deviation guard ("reject any move > X bps") protects against a single bad data
point moving the feed. It also **delays detection of a genuine depeg**, which is
the one scenario the feed exists for. These pull in opposite directions.

**DEFERRED — needs a named risk owner.** This is not an engineering call: it
trades depeg-detection latency against manipulation resistance, and the right
answer depends on who bears the loss in each direction.

Straw man to react to, not a decision: allow *upward* moves toward 1.0 freely;
require **K consecutive observations** (K ≈ 3, ~15 min at T = 5 min) before
publishing a downward move larger than `maxDeviationBps`, and page on the first
one. Note that under this rule a real depeg reaches consumers ~15 minutes late,
which someone must consciously accept.

### 6.3 Source compromise

- **Hex API compromise** — a wrong reserve number produces a wrong price. The TEE
  cannot detect this. Mitigation is bounds + alerting + a governance pause, plus
  agreeing an out-of-band verification channel with Hex.
- **RPC compromise** — mitigated by the 2-of-3 quorum at a fixed block.
- **TEE compromise** — out of scope; the FCC trust model handles it via
  attestation and code-hash registration.

### 6.4 On-chain

- Replay and out-of-order delivery — handled by strict `observedAt` monotonicity
  (FCC gives no ordering guarantee by design).
- Griefing via spam `requestPriceUpdate()` — each call costs the registry fee, so
  it is self-limiting, but consider an allowlist or a minimum interval.
- Feed consumers reading a stale price — `getCurrentFeed()` returns a timestamp;
  document loudly that consumers **must** check it.

---

## 7. Open questions — consolidated

For Hex:

0. ~~Why does `current_reserves_amount` exactly equal on-chain supply?~~ (§3.0)
   Resolved 3 Aug — it is the bank-provider balance of the reserve account, and Hex
   operates 1:1. Remaining as diligence: written confirmation of that provenance and
   that an unmirrored outflow shows up in the figure.
1. ~~Is USDX natively issued or bridged?~~ Evidence says native burn-and-mint OFT
   (§3.3) — needs confirmation, not investigation. Which addresses are
   non-circulating, and what is the old Ethereum contract's status?
2. Does `current_reserves_amount` already include cash reserves?
3. Are bonds valued at market or par?
4. Auth scheme and rate limits for the private endpoint? Response shape of the
   public one?
5. How many IPs can we whitelist, and what is the lead time to add one?
6. How frequently are the reserve figures recomputed? Does `last_update` move on
   every redemption?
7. Are the raw reserve components confidential? We have assumed **yes** and
   excluded them from both the on-chain payload and `/state` (§4.1). If they are
   in fact publishable, we would rather include them — it makes the feed
   auditable. Also: would a *lagged* disclosure (e.g. T+24h) be acceptable?
8. Uptime expectations and a contact/escalation path.

For Flare / internal:

9. Feed **name** and the specific category byte within `0x20`–`0x3F` (§3.6).
   Encoding and valid range are now verified; only the values are open. Decimals
   (proposed 8) are entirely our call — the interface constrains nothing.
10. Update cadence `T` and the fee/gas budget that implies.
11. How many TEE machines serve this extension? (Drives the IP-whitelist count in
    WP6.4 — Hex has to whitelist one static egress IP per machine.)
12. Deviation-guard policy (§6.2) — **who owns that risk decision?** Currently
    unassigned and blocking.
13. Where do Confidential Space extension logs land and who can read them (§4.1)?
14. Is an external audit in scope?
15. Behaviour when a source is unavailable: freeze at last value (with an ageing
    timestamp) vs actively signal "no price". **Proposal: freeze and let the
    timestamp age — consumers already must check staleness.**

Resolved (see decisions log at the top): submission path, on-chain payload shape.

---

## 8. Milestones (straw man)

| # | Milestone | Exit criteria | Depends on |
| --- | --- | --- | --- |
| M0 | Access secured | API key issued, IPs whitelisted, USDX addresses confirmed | Hex |
| M1 | Local prototype | Extension computes a price locally against mocks + real APIs; unit + conformance tests green | M0 |
| M2 | Contracts on Coston2 | Sender, store, feed deployed; Foundry tests green | M1 |
| M3 | E2E on Coston2 | Instruction → TEE → signed result → store → `getCurrentFeed()` returns a price | M2 |
| M4 | Hardening | Monitoring live, runbook written, failure drills passed, security review done | M3 |
| M5 | Mainnet | Feed registered with governance, keepers running, consumers notified | M4 |

---

## 9. Appendix — repo layout

Built on the FCE extension scaffold in this repo. Relevant references:

- `docs/extension-contract.md` — normative container contract (`POST /action`,
  `GET /state`, wire format, reproducibility rules)
- `docs/instruction-sender.md` — how the on-chain entry point works
- `docs/deployment-steps.md` — Confidential Space deployment
- `docs/testing.md` — unit / conformance / e2e layers
- `go/internal/extension/extension.go` — handler dispatch to replace with USDX logic
- `contracts/InstructionSender.sol` — starting point for `UsdxInstructionSender`

The tee-node signing API available to the extension on `$SIGN_PORT`:

| Endpoint | Use here |
| --- | --- |
| `POST /sign` | sign the price result with the TEE identity key |
| `POST /sign/{walletID}/{keyID}` | not needed in v1 |
| `POST /decrypt` | decrypt injected secrets (Hex API key, RPC keys) |
| `GET /key-info/{walletID}/{keyID}` | not needed in v1 |
