# USDX Oracle — Mint / Redeem / Bridge Dynamics: Where Supply and Reserves Diverge

**Status:** 📋 **ANALYSIS — feeds spec §3.3, §3.4, §6.1 and the Hex question list** · **Raised:** 2026-08-05

_Companion docs: design intent in `usdx-oracle-spec.md`, implementation findings in
`usdx-oracle-findings.md`, timing/TOCTOU analysis in spec §6.1 and
`usdx-oracle-availability.md`, alert taxonomy in `usdx-oracle-monitoring.md`._

---

## Why this doc exists

`price = min(1, B/S)` is correct only while `B` and `S` measure the same population
at the same instant. Neither holds unconditionally. USDX is a burn-and-mint LayerZero
OFT whose supply moves across three chains, while its backing is a bank-reported
balance that updates on its own clock — and the issuer's own mint and redemption
flows move the two sides **at different times by design**.

Spec §6.1 analysed one such case: a redemption burn landing between two reads. This
doc enumerates every mechanism we know of that moves one side without the other —
mint, redeem, bridge, reserve-account flows, and population mismatches — with for
each: the dynamics, the direction of the resulting price error, what we should be
prepared for, and how to account for it.

**Evidence base (5 Aug 2026).** Live `totalSupply()` reads on all four contracts,
the public reserves endpoint, the issuer's site and whitepaper v1.0, and probes of
the issuer's API surface. Details in the appendix. Headline: the three-chain supply
matches the reserves figure to **$0.004 dust** today, and no chain's supply has moved
since 31 Jul — so every case below is about states that appear *when activity
resumes*, not about a divergence observable right now. Supply fell 54% in six months,
so activity will resume.

---

## Case 1 — Bridge transfer: burn on source, mint on destination

**Status: mechanism confirmed** (spec §3.3 evidence: `token()` returns self on Flare
and current Ethereum; issuer site: *"Moving USDX across chains burns on the source
and mints on the destination — no wrapped tokens."*)

**Dynamics.** A cross-chain send is three steps: `_debit()` burns on the source chain
(atomic, one tx), a LayerZero message is verified by DVNs and delivered by an
executor, `_credit()` mints on the destination (atomic, one tx). Between the burn and
the mint, the bridged amount exists on no chain: `Σ totalSupply()` across all chains
is lower than the true circulating amount by exactly the transfer size. No partial
state within any single chain — the inconsistency is purely cross-chain.

Duration is normally seconds to minutes (confirmation requirements + executor
scheduling). It becomes unbounded if destination execution keeps failing: the message
waits for retry, and supply stays reduced until a mint finally lands. OFT also
converts between local decimals (6 on Flare/Songbird, 18 on Ethereum) via a shared
minimum, so bridging truncates sub-micro amounts — negligible, but a permanent dust
loss from global supply per transfer.

**Direction.** `S` low, `B` flat → `B/S` biased **up**, toward $1. Masked by the par
cap while `B ≥ S`; when the token is genuinely under-backed, a bridge in flight makes
the published price *closer to $1 than the truth*. Wrong direction, exactly when it
matters.

**What to be prepared for.**

- Supply is read once, pinned to one agreed block per chain, so there is no
  intra-observation supply race left to cover: the figure is supply *as of block N*.
  The earlier conservative double-read (`max(S₁, S₂)`) covered a burn or mint landing
  between two supply reads, and was dropped along with them — see rationale §2.
- Neither scheme covers the case that actually matters here: a gap containing the
  whole observation — burn before the read, mint after. Our read window is seconds;
  the gap is tens of seconds to minutes, so an entire update can sit inside one gap.
- A large transfer moves the ratio proportionally: 1M USDX ≈ 4% of current supply.
  Bridges cluster in exactly the market conditions where the price matters.
- Failed-then-retried delivery stretches the gap from minutes to hours. Nothing on our
  side sees the retry; the supply just stays low until it lands.

**How to account for it.**

1. *Accept and document* (minimum). The exposure is bounded by transfer size, and the
   error direction is only dangerous when already under-backed.
2. *Cross-update supply hold* (recommended — see Preparedness P2; now the only
   supply-timing defence). If supply drops between observations while reserves are
   unchanged, publish against the **higher** recent supply. The mint restores supply
   within minutes, so the hold self-corrects; while active it can only understate the
   price, never overstate it. Note this signature (`S` down, `B` flat) is shared with
   a redemption burn whose reserve tick has not landed yet (Case 3) — the same
   treatment is correct there too, for the same conservative reason.

---

## Case 2 — Mint: fiat settles before tokens exist

**Status: mechanism confirmed** (whitepaper: *"Tokens can only be minted after
confirmed fiat settlement... Minting against unconfirmed or pending transactions is
prohibited."* Site: *"No pre-minting. No synthetic supply. Ever."*)

**Dynamics.** Mint order: user wires fiat → treasury detects settlement → compliance
checks → `mint(user, amount)` on-chain. Reserves rise at settlement; supply rises
later, when the controller mints. The window spans compliance processing — likely
hours to days.

**Direction.** `B` high, `S` lagging → `B/S > 1` → **capped at par**. The error is
invisible by construction: over-collateralisation never publishes above $1.00.

**What to be prepared for.** Nothing harmful. The only observable effect is that after
a large mint, supply appears to "catch up" to reserves some time after the reserve
figure moves. A deviation guard comparing consecutive updates must not treat
reserve-led movement as suspicious (Case 6 shares this shape).

**How to account for it.** No action. Monitor-only; the par cap already does the work.

---

## Case 3 — Redemption: escrow, then wire, then burn — the sharp case

**Status: mechanism documented by the issuer; our docs did not previously cover it.**

Whitepaper order, verbatim:

> 3. USDX tokens are transferred to the redemption contract — **held in on-chain
>    escrow until fiat delivery is confirmed**
> 4. Treasury system initiates fiat release to the user's bank account
> 5. `burn()` executes **after fiat delivery is confirmed** — tokens are permanently
>    destroyed

The site states the same order more tersely: *Token Escrow → Treasury Release → Token
Burn "after fiat delivery confirmed".*

**Dynamics.** Three phases, and the two sides of our ratio move in *different*
phases:

| Phase | Supply (`totalSupply`) | Reserves (bank balance) |
| --- | --- | --- |
| Escrow | unchanged — escrowed tokens are a contract balance, still minted | unchanged |
| Fiat release | unchanged — burn has not happened | **falls** when the wire debits |
| After delivery confirmation | **falls** — `burn()` executes | already low |

Between fiat release and burn, `B < S` **during perfectly normal operation**. The
window is bank settlement time — SWIFT/wire delivery plus confirmation — with
published redemption SLAs we have not yet retrieved; expect hours to days, not
seconds.

Two sub-readings, not yet distinguishable from outside:

- **(a) Literal.** The reserve-account balance drops when the wire leaves, and the
  burn follows delivery confirmation days later. The oracle prints below $1 for the
  whole window of every redemption.
- **(b) Benign.** "Total reserves" in the issuer's invariant still counts the
  segregated redemption-payment account (their `R_cash`, "cash held for redemptions"),
  so `B` only falls when money leaves their banking system entirely, with the burn
  following promptly. The window shrinks to the confirmation lag (minutes to hours).

Our 31 Jul observation (reserves tracking supply to the cent through the 1M burn)
**cannot distinguish (a) from (b)** — we sampled only after the burn had landed.

There is a direct contradiction worth putting to Hex: the site asserts *"reserves ≥
totalSupply at all times — not a target, a structural constraint enforced at the
protocol level,"* yet their own redemption ordering violates it between phases 4 and 5
under reading (a). One of the two statements is imprecise; which one determines our
accounting.

**Direction.** Price **under-stated** — conservative, never a hidden shortfall. But it
is a *false depeg signal*: consumers see below-$1 prints during routine redemptions,
and a deviation guard (§6.2) trips precisely during normal operation. Redemptions also
cluster in stress events, so "routine" windows will land exactly when confidence in
the feed matters most.

**What to be prepared for.**

- A below-$1 print with no actual shortfall. Distinguish it from a real one before
  anyone pages: the escrow balance is **public**. If the drop in `B` is matched by a
  rise in the redemption contract's `balanceOf`, it is a redemption in progress. A
  drop in `B` with no escrow rise is an outflow with no matching burn — the event this
  feed exists to detect (theft, freeze, operational loss; same signature, no benign
  explanation). This discriminator needs the redemption contract addresses from Hex,
  and its resolution is coarse because reserves tick only every ~10 minutes.
- Escrowed tokens are counted in our `S` today (they are inside `totalSupply`). Any
  change to that is a population decision — see accounting below.

**How to account for it.**

The choice follows the population principle from findings §3.0: our `S` must be
measured on the same basis Hex measures reserves against.

1. **Default — keep escrowed tokens in `S`.** The published price is the honest
   backing ratio on the issuer's stated populations, biased conservatively during
   redemption windows. Cost: periodic false-low prints that monitoring must learn to
   classify (use the escrow discriminator above).
2. **Alternative — exclude the escrow balance via `E`** (spec §3.3 already made the
   exclusion list configurable for exactly this class of problem). `S` then drops at
   escrow time — *before* the wire leaves — and `B/S` stays ≈ 1 through ordinary
   redemptions. Adopt only if Hex confirms both that reading (a) is real (multi-day
   sub-$1 windows) **and** that their own global-supply reconciliation excludes
   escrow, so we stay on their basis. Trade-off: a failed redemption that returns
   escrowed tokens makes `S` jump back up; and exclusion diverges from plain
   `totalSupply`, which any auditor of the feed must be told about.

Decision owner: the same party that owns the Hex question list. Do not adopt (2)
unilaterally — it changes what the feed means.

---

## Case 4 — The old Ethereum contract: a persistent population question

**Status: verified live 5 Aug 2026** — `0x7A48…D56c` still carries
**150,000.000000 USDX** (6 decimals), unchanged since spec §3.1 recorded it.

**Dynamics.** Not a timing case — a standing mismatch. The contract is excluded from
our sum and, per the working assumption, from Hex's reserves basis. If those tokens
are live claims that reserves do not cover, true backing is persistently
150,000 / 25,452,544.52 ≈ **0.59% below** what we publish, every day, in every
market condition. If they are dead (locked, unmigratable, or reserved against
elsewhere), the exclusion is correct and the case closes.

**Direction if wrong.** Price over-stated, persistently — the bad direction, hidden
inside what otherwise looks like a healthy par print.

**What to be prepared for.** This is not observable from chain data alone; no amount
of reading resolves it. It is a question for Hex, and until answered it is a standing
0.59% basis risk on the feed.

**How to account for it.** Keep the exclusion until Hex answers (adding it today would
publish a spurious 0.59% depeg on day one if reserves do cover it — the cash buffer
currently masks either error, which is luck, not design). When answered: one-line
config change either way. Recorded as Q3 below.

---

## Case 5 — Domains we do not read: set completeness

**Status: open, sharpened 5 Aug 2026.** The issuer's site claims *"a native
burn-and-mint architecture maintains supply integrity across **11 EVM networks**."*
Our oracle pins three chains.

**Dynamics.** If USDX supply exists on a chain we do not read, and Hex's reserves
figure covers the global population (it reconciles globally — *"Total supply across
all chains is tracked and enforced against reserves at the global level"*), then our
`S` is permanently under-counted and every price we publish is biased up. The config
rule "every pinned chain must be present" (findings §15) protects the configured set;
it cannot protect against the set being incomplete.

Their new transparency surface exposes exactly the right data — `GET /domains`
(canonical domain registry) and `GET /supply` (per-domain breakout) — but it is not
live: the routes return 404 on the known host and the transparency hub renders
"Waiting for registry" (appendix, probe results). The registry is currently only in
their hands.

**Corroborating datapoint.** Songbird's 1.16M is empirically inside the reserves
figure — today's cent-exact match includes it. That settles the spec's open "does
Songbird count" question in practice, though written confirmation is still worth
having.

**Direction if wrong.** Price over-stated, persistently, magnitude = uncounted supply
/ total supply.

**What to be prepared for.** New domains going live silently. The 11-network claim may
be a roadmap figure ("phase 1" language suggests not all are live), but we have no
independent view of which are.

**How to account for it.** Obtain the domain registry from Hex (Q4) and pin every
domain with non-zero supply into config. When their `/domains` route goes live, have
the keeper poll it and alert on any delta against the pinned set — that converts a
standing blind spot into a monitored one. Revisit the config every time Hex announces
a new domain.

---

## Case 6 — Reserve-account flows: T-bill purchases, fees, interest

**Status: semantics open (spec §3.4), risk sharpened by the §3.0 resolution.**

**Dynamics.** `current_reserves_amount` is the bank-provider balance of the reserve
account. Anything that moves money through that account without a matching burn or
mint moves `B` alone:

- **T-bill purchases — the milestone event.** Reserve policy targets ~60% T-bills;
  today's exact 1:1 supply match says the reserves are effectively all cash right now
  (the site itself notes the allocation ranges are *"the target allocation model,
  while live publication status belongs in the transparency surface"*). The day Hex
  buys bills from the reserve account, the bank balance falls **with no burn** —
  a printed depeg despite full backing — unless the reported figure includes the
  securities position. This is the largest foreseeable false-depeg event, and it will
  arrive as an ordinary treasury operation with no announcement we can rely on.
- **Fees, custodian moves, wire charges** — small `B` outflows, no burn. Same shape,
  smaller magnitude.
- **Interest accrual** — `B` rises alone → capped at par. Harmless.

**Direction.** Outflows: price under-stated (conservative but false). The danger is
not the direction; it is alert fatigue and misattribution — the same signature as a
benign T-bill purchase is also the signature of reserve theft.

**What to be prepared for.** The first T-bill purchase. It is an operational milestone
for the feed even though it is routine treasury management for the issuer.

**How to account for it.**

1. Get the figure's semantics in writing (Q5): does it include securities positions,
   and at what valuation (par vs market)?
2. Watch for the issuer's `USDXReserveOracle` / daily reserve-report surface to go
   live. It reports `totalReserves` and `liquidReserves` with freshness flags and
   report hashes — a richer `B` source than the bank balance, and the one that will
   keep meaning "full backing" once bills enter the portfolio. Trust model differs —
   those are issuer-committed figures anchored by hash, not a bank balance — so any
   source swap is a spec-level decision, not an implementation detail.
3. Until then, treat any unexplained step-down in `B` with `S` flat as: redemption in
   progress (check escrow, Case 3) → T-bill purchase (check with Hex) → genuine
   outflow. In that order of base rates, but with the last one being the one that
   pages.

---

## Case 7 — Non-circulating balances inside `totalSupply`

**Status: believed empty, unverified.**

**Dynamics.** Any balance that is counted in `totalSupply` but is not a redeemable
claim — burn address, controller contracts, unissued inventory — overstates `S` and
understates the price. The issuer states *"The issuer cannot expand supply at will,
cannot create tokens for internal use, and cannot hold USDX in treasury circulation,"*
which would make this case empty by construction.

**Direction.** Price under-stated — conservative. The risk is a persistent quiet
discount nobody investigates.

**How to account for it.** The exclusion list `E` handles it if it ever exists.
Verification is one `balanceOf` sweep once Hex provides the controller/burn addresses
(folded into Q4/Q6). No action until then.

---

## Signature summary

| # | Case | Supply | Reserves | Price bias | Duration | Covered by current design? |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Bridge in flight | ↓ | flat | toward $1 (up) | sec–min; unbounded if delivery fails | **no** — gap-inside-window was never covered, and the double-read is gone |
| 2 | Mint | ↑ (lagged) | ↑ first | capped at 1 | hours–days | yes — par cap |
| 3 | Redemption escrow→wire→burn | flat, then ↓ | ↓ first | below $1 (down), false | hours–days | **no** — reserves side; S reads are "correct" |
| 4 | Old ETH contract | understated | n/a | up, persistent | forever | no — population question |
| 5 | Domains not read | understated | n/a | up, persistent | forever | no — set completeness |
| 6 | T-bill purchase / outflows | flat | ↓ | below $1 (down), false | persistent until semantics fixed | no — reserves side |
| 7 | Non-circulating balances | overstated | n/a | down, persistent | forever | yes if in `E`; conservative either way |

**What the current design already absorbs:** the par cap (Cases 2, 7-safe), truncation
(always biased down), the single block-pinned supply read (no intra-observation supply
race — the figure is supply as of one block), the configurable exclusion list `E`
(3-alt, 7), and "every pinned chain must be present" (integrity of the configured set).

**What it does not absorb:** any reserves-side movement (Cases 3, 6 — the supply reads
are correct there; nothing to bracket), cross-chain gaps fully inside the observation
window (Case 1 residual), and population completeness (Cases 4, 5 — no read can see a
contract or chain it was not given).

---

## Preparedness — what to do

### P1. Questions for Hex (consolidated, quotable)

Numbered to extend the spec §7 list:

- **Q1 (Case 3).** Confirm the redemption ordering and timing: is fiat released before
  `burn()`, and how long does delivery confirmation take in practice? Does
  `current_reserves_amount` fall when the wire leaves, or only at burn? Are
  redemptions paid from the reserve account or from the segregated cash account?
- **Q2 (Case 3).** Addresses of the redemption/escrow contracts on each chain. These
  are public state we can verify against, and the discriminator between benign
  outflows and genuine reserve loss.
- **Q3 (Case 4).** Is the old Ethereum contract (`0x7A48…D56c`, 150,000 USDX) a live
  claim, and is it covered by reserves?
- **Q4 (Cases 5, 7).** The full domain registry — every chain with USDX supply, with
  contract addresses — plus the controller and any non-circulating addresses. When
  does `GET /domains` go live?
- **Q5 (Case 6).** Semantics of `current_reserves_amount` once T-bill purchases begin:
  does it include securities positions, valued at par or market? Will we get notice
  before the first purchase?
- **Q6 (Case 3 accounting).** Does your global-supply reconciliation count escrowed
  (pending-redemption) tokens as supply? We must measure the same population you
  reconcile against.

### P2. Cross-update supply hold (guard for Cases 1 and 3)

If `S` falls between consecutive observations while `B` is unchanged, publish against
`max(S_now, S_prev)` instead of `S_now`. Rationale: the two causes of that signature
are a bridge in flight (Case 1) and a redemption burn whose reserve tick has not
landed (Case 3 within one 10-minute tick). In both, `B` is stale-*high* in the same
direction `S` would correct, so holding the larger supply aligns the two figures and
the ratio stays ≈ correct; in neither case can the hold overstate the price — it only
ever pushes it down. The hold self-clears when the mint lands or the tick arrives.

Trade-offs, stated honestly: after a genuine redemption it can print slightly low for
up to one reserve tick, which may trip the deviation guard (§6.2) — consistent with
the design-wide rule that every error fails down; and the comparison state is
per-process, so the guard is inactive on the first observation after a restart.
Implementation location (extension observation loop vs keeper) is open; the extension
is the only place that sees both figures.

### P3. Monitoring hooks (feeds `usdx-oracle-monitoring.md` taxonomy)

- **Escrow watch (needs Q2):** alert on redemption-contract balance rising, so a
  subsequent reserves drop is pre-classified as redemption-in-progress before anyone
  has to guess.
- **Domain delta (needs Q4 / live `/domains`):** alert on any domain not in our pinned
  set appearing with non-zero supply.
- **First-divergence watch:** any update where `|B − S|` exceeds a threshold in either
  direction should carry its case classification (S-moved vs B-moved) in the keeper's
  diagnosis — the signature table above is the lookup.

### P4. Milestones that change this doc

1. First T-bill purchase (Case 6) — re-verify `B` source semantics; consider the
   reserve-oracle source swap.
2. `/domains` + `/supply` transparency routes going live (Case 5) — wire the keeper
   delta alert.
3. Hex answers Q1/Q6 (Case 3) — decide default vs escrow-exclusion accounting.
4. Any new domain announcement (Case 5) — config update before the domain carries
   material supply.

---

## Appendix — evidence collected 5 Aug 2026

**Live reads (all `eth_call totalSupply()` at `latest`; reserves from the public
endpoint, same day).**

| Source | Value |
| --- | --- |
| Flare `0x4A77…d2B` (6 dp) | 20,620,459.685024 |
| Songbird `0x4A77…d2B` (6 dp) | 1,159,750.000000 |
| Ethereum current `0xf875…313` (18 dp) | 3,672,334.838994 |
| **Three-chain sum** | **25,452,544.524018** |
| `current_reserves_amount` (last_update 2026-08-05T10:27:21Z) | "25,452,544.52" |
| Residual | 0.004018 — sub-cent, consistent with 2-decimal reporting granularity |
| Ethereum old `0x7A48…56c` (6 dp, excluded) | 150,000.000000 |

All three live supplies are byte-identical to the 31 Jul figures in spec §3.1 — no
bridge, mint, or redemption has landed on any pinned chain in the intervening five
days.

**Issuer documentation (usdxstable.com, retrieved 5 Aug 2026).**

- Mint flow: fiat transfer → settlement confirmation → compliance → *"Mint/Burn
  Controller authorizes on-chain mint"*; *"mint(user_address, amount) is executed
  on-chain"* is step 5 of 6, after settlement. *"Every USDX token is minted only after
  verified fiat settlement... No pre-minting. No synthetic supply. Ever."*
- Redemption flow: as quoted in Case 3 (site order: Token Escrow → Treasury Release →
  Token Burn after fiat delivery confirmed; whitepaper steps 1–6 with `burn()` after
  delivery confirmation).
- Bridging: *"Moving USDX across chains burns on the source and mints on the
  destination — no wrapped tokens, no custody risk."* *"A Global Supply Controller
  reconciles per-domain supply with total reserves."* *"A 24/7 backend worker polls
  all networks at 30-second intervals"* — the issuer's own supply view is also
  eventually consistent, not atomic.
- Invariant claim: *"reserves ≥ totalSupply always... not a target — a structural
  constraint enforced at the protocol level."* (Contradicted between redemption
  phases 4–5 under the literal reading — Case 3.)
- Network claim: *"11 EVM networks"*; transparency routes `/domains`, `/supply`,
  `/reserves`, `/proof`, `/status` documented but launch-gated ("Production
  publication requires a real `--public-registry`").

**Probes.**

- `GET https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount`
  → 200, value as above (tick cadence ~10 min, per spec).
- `GET /domains`, `/supply`, `/reserves`, `/status` on the same host → **404** — the
  new transparency surface is not served from the known host.
- Transparency hub page renders "Waiting for registry / Polling /status" — no live
  per-domain data published yet.

**Limitations.** No history endpoint exists for the reserves figure, so the Case 3
sub-readings cannot be separated by time series; the SLA figure for redemption
delivery is published somewhere on the site we did not retrieve; and everything about
issuer intent beyond the quoted text is inference.
