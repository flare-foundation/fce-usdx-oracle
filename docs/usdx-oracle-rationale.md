# USDX oracle — design rationale

Why the code is shaped the way it is. This was originally carried as long
package-doc comments in `internal/*` and `tools/pkg/*`; it was moved here so
the code stays readable and the reasoning stays available. Where the reasoning
behind a package is long, its comments point to a section here: §6 from
`internal/observation/supply` and `pkg/protocol`, §9 from
`internal/provisioning/envelope` and `internal/provisioning/store`. Elsewhere the
code's own comments carry the short form.

Companion documents: [spec](usdx-oracle-spec.md) · [flows](usdx-oracle-flows.md) ·
[findings](usdx-oracle-findings.md) · [keeper](usdx-oracle-keeper.md) ·
[monitoring](usdx-oracle-monitoring.md) · [availability](usdx-oracle-availability.md)

---

## 1. The public error vocabulary (`internal/failure`)

Two of the extension's outputs are world-readable:

- `ActionResult.Log` — returned to the TEE proxy and served from its
  action-result endpoint (`docs/extension-contract.md` §4.6). A refusal carries
  `error: <reason>` from the vocabulary. The router's 400 and 501 bodies land
  there too, because tee-node turns a non-200 answer into an error and records it
  in `Log`.
- the `GET_READINESS` report — the machine's signed account of itself, served to
  anyone holding the proxy's direct key (spec §4.1). `GET /state`, which tee-node
  folds into the attested `/info`, carries the empty state.

Go error strings routinely carry exactly what must not appear on either. Both of
these are real, and both are among the likeliest failures in this extension:

```
// *url.Error embeds the full request URL, credentials included:
Get "https://api.htdigitalassets.com/...?apikey=SECRET": dial tcp: ...

// A decimal parse error embeds the value it could not parse — and the public
// endpoint really does return commas, so this path WILL be taken:
parsing reserves "25,452,544.52": not a decimal
```

The second is sharper: spec §4.1 excludes reserve figures from every public
surface, and a naive `fmt.Errorf("parsing %q: %w", raw, err)` would publish the
exact number the design exists to withhold.

**Enforcement.** A `Failure` carries unexported fields and there is no exported
constructor, so a `Failure` can only be one of the package-level values. A
handler refuses with `refuse(failure.X)`, never with an `error`, so there is no
code path that puts a runtime value into a refusal — not by convention or review,
but because the compiler rejects it. Adding a failure mode means adding a value
to that one reviewable file. The router's 400 and 501 bodies are the one place a
runtime value is formatted, and they carry only the caller's own input (a decode
error, the op type or command it sent) and constants of this build, never
anything about the machine's state.

Detail for debugging does not belong in a `Failure`. Log it separately, and keep
to values known to be safe — source name, HTTP status, duration. Never the
upstream error, its response body, or any parsed figure. Spec §4.1 leaves where
Confidential Space logs land, and who can read them, unresolved; until that is
answered, treat logs as disclosing too.

**Wording rule.** Name the subsystem and the nature of the fault, never the
value, the endpoint, or the threshold that was crossed. "backing source returned
an implausible value" is publishable; "backing 25452544.52 exceeds cap 30000000"
is not — it discloses both a reserve figure and a bound worth knowing to anyone
probing the oracle.

**One reason for provisioning.** `ProvisionRejected` is the only failure a
provisioning command may report, for every cause — bad signature, too few
distinct signers, unknown signer, stale `issuedAt`, replayed version, wrong
`teeId`, wrong chain, wrong domain, undecryptable ciphertext, a credential that
would alter its URL, a cash URL that does not match its commitment. A caller
learns whether provisioning succeeded, never which check failed; distinguishing
them would turn the endpoint into an oracle for probing the admin set and the
freshness window.

---

## 2. One supply read, one reserve read (`internal/observation`)

Supply reads and reserve reads are not atomic, and a redemption landing between
them changes both sides of the ratio. Read reserves first (high) and supply after
the burn (low) and the price is over-stated — the direction that matters, because
an over-stated price hides a shortfall.

This is not hypothetical at the observed scale. Spec §6.1 records a single
1,000,000 USDX burn on Flare at 07:10:58Z on 31 Jul 2026: 4.8% of supply in one
event, far larger than any plausible deviation threshold. A stress event is
exactly when redemptions cluster and the price matters most.

The observation used to bracket the reserve read between two supply reads and
take the **larger** supply, so that any skew biased the price downward. That
bracket has been dropped:

```
S ← supply         B ← reserves
price = min(1, B / S)
```

Neither side has an internal race worth bracketing. The quorum procedure (§6)
pins every provider to one agreed block, so a supply figure is not "supply at
some point during the read" but *supply as of block N*. Hex republishes a reserve
figure only within ~10 minutes of an on-chain event, so it is effectively
constant across a 20s observation.
A second supply pass therefore changed the answer only when a burn or mint landed
in the few seconds between two pinned blocks — while doubling the supply-side
fail-closed surface, since either pass failing failed the whole observation.

`ObservationWindow` (30s) bounds the read skew between the earliest and latest
input. It sits above `ObservationDeadline` (20s), so the deadline binds first and
the window cannot fire with today's values; it is pinned as a backstop, so that a
longer deadline cannot silently widen the skew the two sides may drift apart by.

### Fail closed, always

Any source down, any value implausible, any window exceeded: publish nothing and
return a failure. Never guess, never interpolate, never substitute an earlier
figure for a missing input. The poller serves the newest *complete* observation
until it is `MaxObservationAge` old, and nothing after that (§3). A frozen feed
with an ageing timestamp is the designed failure mode (spec §7.15) and consumers
are required to check the timestamp; a confidently wrong price is not
recoverable downstream.

### `observedAt` is the request's block timestamp, not an input timestamp

The store requires strict monotonicity, and an input timestamp can stall
indefinitely: `last_update` moves only when a mint or a redemption changes the
figure, so on a quiet day every observation for hours would share one, and all
but the first would be rejected as a replay. The signed `observedAt` is instead
the block timestamp of the instruction that requested the update, as the store
defines it (§3). The reading's own time — the home chain's block header when
the inputs were **read** — is kept on the observation, and `MaxObservationAge`
(10 min) bounds how far behind the stamp it may lie. There is deliberately no
bound on how old the reserve figure itself is — see [§ *No freshness bound on
the reserve figures*](#no-freshness-bound-on-the-reserve-figures) below.

### No freshness bound on the reserve figures

Hex's endpoints are **event-driven**: both republish within about ten minutes of
an on-chain event — a mint or a redemption — and not otherwise. `last_update` is
a *change* timestamp, not a heartbeat, so an old stamp usually means nothing
happened, not that anything is wrong.

The extension used to reject a reading past a wall-clock age, and it was wrong
twice over. Empirically it rejected good data at a rate set by USDX activity: a
shared 30-minute bound had `hex_cash` reporting `down` for most of every hour on
every machine, and widening it to 90 minutes only moved the flapping — observed
stamps sat ~7 hours apart with the value unchanged. Each time the machine
fetched a correct figure and then discarded it, which cost the feed its entire
12.6% cash buffer for most of the day (availability §"Measured again").

It also never covered the direction that matters. If the upstream freezes, `R`
stops tracking a growing `S` and `min(1, R/S)` prints **below** par — the
direction that reveals a shortfall rather than hides one. And reserves lost
*without* an on-chain event never move `last_update` at all, so no bound would
have caught that either.

What survives is narrower and accurate: `last_update` is still parsed (an
unparseable one is `BackingMalformed`) and a stamp in the **future** is still
refused as `BackingTimestamp`, because that means clock skew or a broken
upstream rather than an aged figure. Staleness is bounded only where it can be
measured honestly — on the age of our own observation, and on the age of the
on-chain feed.

---

## 3. Observing on a timer, signing on demand (`internal/observation/poller`)

`Observe` is slow — five upstream sources, two of them multi-provider — and
tee-node gives an extension **2 seconds** to answer an action
(`settings.ProxyTimeout`, a const in its internal package, so not configurable
from here). Measured against live sources on 5 Aug 2026 an observation takes
2.0–2.9s, so running one inside the request handler timed out every time, and the
extension's answer never reached the chain at all.

So the two halves are separate calls. `internal/observation/poller` runs `Observe`
on a timer and keeps the latest result; a `GET_FEED` calls `Poller.Report`, which
signs that result under the requesting instruction's block timestamp in one
signing round trip. The request's timestamp is why signing cannot happen in
advance, and the upstream latency is why observing cannot happen on demand.

What is signed is always the observation's own figures — its price, its scale,
its `EndpointsHash` and `AdminsHash`. Nothing is resampled at signing time. The
`observedAt` in the signed update is the block timestamp of the instruction that
requested it: the store defines the field as the timestamp of the on-chain event
that triggered the observation, and its batch rule requires every machine's
update to carry the same value, which only a shared on-chain instant can give.
The inputs may have been read up to `config.MaxObservationAge` (10 minutes)
before that stamp, and that age is accepted; the reading's own time stays on the
observation and is reported in the readiness report as `lastObservedAt`.

### Two clocks

The age bound is measured twice, against two clocks. `Latest` measures the
reading's age against the machine's own clock and refuses with
`ObservationStale` past `MaxObservationAge`; that is what notices upstreams that
have stopped, but a machine whose clock runs slow passes it however old the
reading is. `Report` then measures the same age against the requesting
instruction's block timestamp, which the data-provider quorum cosigned and the
host cannot move (`checkClock`): a reading more than `MaxObservationAge` behind it
is refused as `ClockBehindChain`, and an instruction that carries no timestamp as
`MissingRequestTime`. A reading taken after the request — a late delivery — is
accepted; the store's own rule that `observedAt` lies strictly below
`block.timestamp` bounds that side.

### Observing needs the endpoints; signing needs the admin sets too

`Store.Open` needs only the endpoints, so a machine shows its price and source
health in the readiness report as soon as a configuration is published. Signing a
feed update needs the whole binding. The store rejects an update whose
`adminsHash` is not the feed's published one, and a zero hash can never match, so
`Report` refuses with `NotConfigured` before any signature is produced, rather
than letting the encoder's zero-hash check fail as an internal error
(`TestReport_RefusesToSignWithoutAnAdminSet`). The readiness report's `wouldSign`
applies the same two conditions — an observation inside `MaxObservationAge`, taken
with an admin set installed — but cannot foresee the clock check, which depends on
the request.

### Cadence and bounds

`ObservationInterval` is 2 minutes against Hex's ~10 minute refresh —
deliberately faster than the source. It keeps the supply side current and, more
importantly, leaves room for several consecutive failures inside
`MaxObservationAge` (10 minutes, so four consecutive failed observations before
the feed stops updating). The poller runs on its own timer whether or not anyone
requests, and a failed round keeps the last good observation, which then ages out.
Provisioning does not trigger a round: after `SET_ENDPOINTS` lands, the first
price is at the next tick, up to `ObservationInterval` later.

Nothing on chain bounds the same staleness from the other end. The store accepts
any update newer than the last and strictly in the past, and `getCurrentFeed`
returns the value with its timestamp and never reverts on age. How old a value
may be before it is unusable is the consumer's check, as for every FTSO feed.

Each call to tee-node's `/sign` and `/decrypt` is bounded at 1 second
(`signTimeout` in `pkg/server`). Both are in-memory operations over loopback, so
a call that has not returned by then means tee-node is not answering, and an
answer produced after tee-node's 2 seconds have passed is one nobody reads — for
`SET_SECRET`, a credential that would take effect after the operator was told it
failed.

---

## 4. Observing without cash

The cash figure is the one input with a single unredundant source — Hex's
restricted endpoint, one URL, gated by one credential. `Sources.Cash` is
therefore allowed to be nil (the URL or the credential not provisioned), and a
fetch failure is not automatically fatal.

What is **not** permitted is publishing a different number when it is missing.
[usdx-oracle-availability.md](usdx-oracle-availability.md) settles that: dropping
cash from the numerator would suppress exactly the buffer that decides whether a
depeg prints, and nothing in the signed payload could say the value was computed
that way.

So the observation takes the one case where the missing figure cannot matter —
cash is non-negative and the price is capped at par, so a bonds-only price that
already reaches par is the answer — and fails closed everywhere else.
`price.ComputeWithoutCash` is that rule; the equality it relies on is pinned by
`TestComputeWithoutCash_MatchesTheFullComputation`.

Health still reports the source as down or unconfigured, so an operator can tell
a par-from-bonds observation from a complete one even though the published price
is identical by construction. `Readiness.MissingRefs` keeps naming the ref for
the same reason: *ready* means "fully provisioned", not "able to answer".

---

## 5. Exact arithmetic (`internal/observation/price`, `internal/observation/decimal`)

### Why not float64

Every amount feeds a price published on-chain as a fixed-point integer. `float64`
has 53 bits of mantissa; a reserve figure like 25,452,544.52 is representable
only approximately, and the error compounds through the division. Amounts are
parsed into `*big.Rat`, which represents any decimal input exactly and divides
without loss.

### Why not shopspring/decimal

`big.Rat` is standard library. For an extension whose whole value proposition is
a reproducible, auditable code hash, a dependency carrying its own rounding
semantics is a liability: one more thing an auditor must read, and one more thing
that can change under a version bump. `big.Rat`'s semantics are fixed by the Go
compatibility promise.

### Rounding half-up

Hex quotes reserves to the cent while supply carries 6 or 18 decimals.
Truncating that quantisation artifact prints a depeg that has not happened —
measured 5 Aug 2026, B − S = −$0.004 on a $25.4M float. Half a ULP is $0.127 at
that supply, so rounding absorbs the artifact and hides nothing material. The
`+½` stays in `big.Rat`, so it is exact; a float implementation would misplace
values sitting on the boundary.

### No price band

There is no plausibility band on the price. `min(1, B/S)` over non-negative
inputs lies in [0, 1] by construction, so a band could only ever refuse a real
sub-par price — the depeg the feed exists to publish. A depeg is detected on the
published value. `failure.PriceImplausible` is declared and never returned; the
input checks that do exist are structural (negative backing or supply, zero
supply, an out-of-range scale).

### The separator trap

Hex's two endpoints disagree on formatting. Measured 31 Jul 2026:

```
public:  {"value":"25,452,544.52"}   commas, no currency field
private: {"value":"205304.47"}       no commas, currency: "USD"
```

A parser must accept both. The obvious implementation — delete every comma then
parse — is wrong and dangerously so: comma-as-decimal-separator is a real
convention (much of Europe writes `1,50` for one and a half), and blind stripping
silently turns `1,50` into `150`. A hundredfold overstatement of reserves would
publish $1.00 straight through a genuine depeg, which is precisely the event this
oracle exists to detect.

`internal/observation/decimal` therefore validates grouping structure rather than stripping:
a comma is accepted only where a thousands separator can legally appear, and
anything else is rejected. Fail closed; never guess what the operator meant.

### The bond haircut

The haircut floated in spec §3.4 is deliberately not implemented: it would always
have been zero, and applying one changes the published price and so needs a new
code hash regardless.

---

## 6. The supply quorum procedure (`internal/observation/supply`)

Reading `totalSupply()` from a single provider makes that provider able to
dictate the published price. Reading it from three at `"latest"` does not fix it:
providers disagree about what "latest" is, so three honest providers routinely
return three different numbers and the disagreement is indistinguishable from an
attack.

Measured 31 Jul 2026, the same is true of `"finalized"`: every live Ethereum
endpoint supports it and sits ~83 blocks behind head, but 1rpc.io resolved it 32
blocks earlier than the others. **A tag is not a shared truth.**

So the read is pinned to an explicit block, uniformly across chains:

1. ask every provider for its head as a block **number**
2. take the **Quorum-th highest** claimed head as the first candidate, and every
   distinct claimed head below it as the next ones
3. at a candidate, require `Quorum` identical header timestamps for that block,
   and refuse it if the timestamp is older than `MaxBlockAge`
4. query every provider at that exact number, for `decimals()` and `totalSupply()`
5. require `Quorum` **identical** results, from a unique most-common answer
6. if a candidate falls short of corroboration, step down to the next one; any
   other fault fails the whole observation

Step 2 was the **minimum** until findings §24. The minimum is the block every
responding provider can serve, which is a liveness argument and correct as far as
it goes — but it assumed honest heads. One provider reporting a head from two
weeks ago dragged the minimum back with it, every honest provider then answered
the supply query at that stale block identically and correctly, quorum passed,
and the enclave signed a stale supply figure. The provider never lies about the
supply; it lies about *which question to ask*, and the honest majority
corroborates the answer. Rewinding to when supply was lower understates supply,
which **overstates** the price.

The Quorum-th highest keeps what the minimum was reaching for — at least `Quorum`
providers claim the pinned block — while making the block choice obey the same
rule as the values, so `Quorum-1` liars cannot move it. A provider claiming a low
head is simply left behind, and recorded as `lagging`. `MaxBlockAge` covers what
the selection rule cannot: a chain with a single provider, a chain where `Quorum`
equals the provider count, and a chain that has simply stopped.

**The timestamp is voted on.** Taking it from whichever provider proposed the
block would let a liar pair an ancient block number with a fresh stamp and defeat
`MaxBlockAge`. A header at a fixed height is deterministic, so `Quorum` identical
timestamps are required, exactly as for the values.

**Stepping down.** A head is a claim, not proof: nothing checks that a provider
holds the block it names. One provider overclaiming its head shifts the Quorum-th
highest onto a block only one honest provider can serve, and the vote there is
one honest answer against the liar's — a tie, which is no agreement. Without a
step down a single provider could deny every round that way. So the read walks
down through the distinct claimed heads until `Quorum` providers serve one. Only a
shortfall of corroboration steps (`SupplyUnavailable`, `SupplyNoQuorum`): a block
that is too old, a malformed answer or an exhausted deadline is about the block or
the round, and a lower block answers none of them while spending what is left of
the deadline. Each candidate is scored on its own copy of the providers' health,
so a provider is not blamed for failing at a block the round abandoned. Any block
the walk settles on is still held to `MaxBlockAge`.

**Identical, not median.** A read at a fixed block is deterministic: two honest
providers cannot disagree, so disagreement means one is wrong or lying and there
is no safe way to average that. The winner must also be unique: a tie counts as
no agreement, or the order the providers are listed in would pick the supply
figure.

### Constraints worth knowing

- `eth.merkle.io` cannot serve explicit-block queries at all and is unusable here
  despite being reachable.
- Only `eth_call` and block lookups are used — never `eth_getLogs`. Worth
  recording because Flare's public RPC caps log ranges at 30 blocks, which looks
  like a blocker until you notice this package does not need it.
- `decimals()` is read every time rather than hard-coded: USDX is 6 on Flare and
  Songbird but 18 on Ethereum, which is normal for a LayerZero OFT and a live
  footgun. Assuming a value would silently misprice a chain by 10¹².
- **Every provider call is bounded.** `ProviderTimeout` (3s) bounds one
  provider's answer within a round, so a provider that accepts the connection and
  never replies is demoted like an unreachable one instead of holding the round
  until the 20s `ObservationDeadline`. Quorum tolerates a provider that fails; this
  is what makes it tolerate one that merely never answers. `MaxRPCResponseBytes`
  (1 MiB) caps every RPC response body at the transport (`dialRPC`), beneath
  go-ethereum's unbounded buffering of both the success and the error path: the
  enclave holds its provisioned secrets in memory, so exhausting it costs a full
  re-provisioning round, not only a restart.
- **The quorum is governance's**, per chain group, between 1 and the provider
  count. `Quorum: 1` is expressible deliberately — an explicit acceptance of
  single-provider risk for that chain's share of supply, not an oversight (spec
  §2.8) — and `deploy/config/endpoints/coston2.json` uses it for Songbird. The
  coston, songbird and public-only configurations run Songbird at 2-of-3.
  `MaxBlockAge` is the only freshness check that still works at quorum 1.

### No exclusions

Supply is the plain sum of `totalSupply()` over the configured contracts. There
is no exclusion list and no `balanceOf` call: the contract list **is** the
policy, and it is pinned in `pkg/protocol.Chains`, covered by the code hash. The
set must match the basis on which Hex reports reserves, or the ratio compares two
different populations.

The pre-migration Ethereum contract (`0x7A48…D56c`, 150,000 USDX) is deliberately
absent: Hex excludes it from their reserves figure, and counting it here would
publish a spurious ~0.6% depeg. See spec §3.1.

A plain sum is correct only because USDX is a native burn-and-mint LayerZero OFT
on every chain read: `token()` returns the contract's own address on both Flare
and current Ethereum, which is the native pattern rather than a lock-and-mint
adapter. Bridging therefore burns on the source and mints on the destination, and
no balance is counted twice. **If a lock-and-mint adapter exists on any chain,
this sum double-counts and the price is overstated.**

---

## 7. The signed payload (`pkg/wire/feedupdate`)

```solidity
struct FeedUpdate {             // ITeeOracleFeedStore, in declaration order
    uint256 extensionId;
    bytes21 feedId;             // 0x21 ‖ "USDX/USD", zero-padded (protocol.FeedID)
    int32   value;              // scaled by 10^decimals
    int8    decimals;           // 8 (config.PriceDecimals)
    uint64  observedAt;         // block timestamp of the requesting instruction
    bytes32 endpointsHash;      // keccak256 of the active SET_ENDPOINTS payload, as received
    bytes32 adminsHash;         // keccak256 of the active SET_ADMINS payload, as received
}

dataHash    = keccak256(abi.encode(update))
messageHash = keccak256(abi.encode(bytes32("TEE_ORACLE_FEED"), chainId, dataHash))
digest      = keccak256("\x19Ethereum Signed Message:\n32" ‖ messageHash)
data        = abi.encode(update, Signature{v, r, s})
```

`messageHash` is upstream's `SignedPayload.messageHash(TEE_ORACLE_FEED, dataHash)`,
built here from go-flare-common's `signing.Payload` rather than a local copy. The
enclave hands tee-node the preimage `abi.encode(prefix, chainId, dataHash)`, not
its hash: tee-node keccaks what it is given and applies EIP-191 itself (§8),
which lands exactly on the digest `Fdc2Verification` recovers over. Passing the
hash would sign the wrong bytes. `data` is the `ActionResult` payload, the two
halves of the `SignedFeedUpdate` that `submitFeedUpdates` takes, so the keeper
forwards it without reshaping anything.

FCC is explicitly "fire and forget": no ordering guarantee, no delivery
guarantee, no replay protection beyond what a payload arranges for itself.

- **The prefix** separates this signature from anything else the TEE identity key
  signs; the provisioning envelope and the readiness report carry their own
  domains. It is `bytes32("TEE_ORACLE_FEED")` — the ASCII, right-padded, not a
  hash — and shared by every TEE oracle feed store, so per-feed separation is the
  next two bullets.
- **chainId** is inside the signed envelope (`block.chainid` on the store's
  side), so an update signed for one network is not valid on another. The enclave
  signs for `CHAIN_ID`, the value tee-node reads (§10).
- **extensionId and feedId** name the exact feed. The store refuses any other
  pair (`WrongExtensionId`, `WrongFeedId`), so one machine's signature cannot be
  replayed onto a second feed the same extension serves, or into another
  extension's store. The enclave refuses a `GET_FEED` for any feed but its own
  before observing (§10), rather than spend an observation on an update the store
  would reject only after the fee was paid and the answer written to a public log.
- **observedAt** is the store's monotonicity key, so it MUST be inside the signed
  body. Outside, a relayer could rewrite it and either replay an old price as
  fresh or push a real price out of order. It is the requesting instruction's
  block timestamp (§3), so every machine answering one request signs the same
  value, which a multi-machine batch requires.
- **endpointsHash** commits to the configuration this price was computed under.
  The store compares it with the feed's `latestEndpointsHash(feedId)`, so a
  machine running a stale or divergent configuration cannot contribute at all.
  Without it the on-chain configuration is advisory: it records what was *sent*,
  never what a machine is actually *running* (findings §16).
- **adminsHash** commits to the admin signer sets in force when the price was
  computed, on the same terms, against `latestAdminsHash(feedId)`. A price is only
  as trustworthy as the party that could install the credentials behind it, so a
  machine still honouring rotated-out signers cannot contribute either.

### Bound to chain, extension and feed; not to the request

The update carries no instruction id. Correlation is tee-node's action id,
outside the signature, and replay is covered by the strictly increasing
`observedAt` and the feed binding, both enforced by the store. A signed update can
therefore be submitted by anyone who holds it, which is the intent: submission is
open, and the keeper is a relay with no authority (§12).

### Static fields, so re-encoding is safe

Every `FeedUpdate` field is a static ABI type, so `abi.encode(FeedUpdate)` is a
fixed seven words with exactly one encoding, and `data` is a flat ten. The store
recomputes `dataHash` from the struct it decodes, and the keeper decodes each
update and re-encodes it into a batch, without either depending on a particular
encoder reproducing the enclave's bytes. Lengths are checked before decoding
(`DataLength`, `RequestLength`), because ABI unpacking accepts a longer buffer and
reads only the words it expects, so a peer on a later layout is rejected rather
than silently truncated.

The configuration payloads are the opposite case. `SET_ENDPOINTS` and
`SET_ADMINS` are dynamic, and re-encoding a decoded one is not guaranteed to
reproduce the bytes the sender hashed, so their hashes are taken over the message
**as received**, never over a re-serialisation (`internal/provisioning/store`).

`value` is `int32` and `decimals` `int8`, the store's shape. At 8 decimals the
ceiling is about 21.47, so `feedupdate.ValueFromBig` refuses rather than
truncates a value out of range: overflow means the arithmetic went wrong, and a
wrapped number would be published as fact.

### What the store does with it

`TeeOracleFeedStore.submitFeedUpdates` takes a batch of signed updates, each a
different machine's own observation, and stores their median. Upstream's rules
decide whether a batch lands:

- every signature recovers, through `Fdc2Verification`, to a machine in
  PRODUCTION on this extension, and the extension is not emergency-paused;
- the batch carries at least `requiredSignatures` updates, at most 32, from
  distinct machines (`NotEnoughSignatures`, `TooManySignatures`,
  `DuplicateTeeId`);
- every update names this extension and feed and the feed's latest endpoints and
  admins hashes (`NoEndpointsPublished`, `NoAdminsPublished`, `StaleEndpoints`,
  `StaleAdmins`), read once per batch, so a batch that mixes configuration
  generations fails on the element that lags;
- all share one `observedAt` (`ObservedAtMismatch`), strictly greater than the
  stored one (`NotNewer`) and strictly below `block.timestamp` (`TooFarAhead`);
- their decimals differ by at most 8 (`DecimalsSpreadTooBig`), and the two values
  bracketing the median agree within `maxSpreadBIPS` of the median plus
  `maxSpreadAbsolute` (`SpreadTooBig`). A single value outside that bound is still
  accepted, and named in `FeedOutliers`.

`requiredSignatures`, `maxSpreadBIPS` and `maxSpreadAbsolute` are governance's,
through `setSubmissionPolicy`. The enclave never sees a batch: it signs one
machine's update, and agreement across machines is the store's.

`getCurrentFeed` reverts with `NoValuePublished` before the first update, and with
`FeeTooLow` or `FeeTransferFailed` on a fee fault. It never reverts on staleness:
it returns the value with its timestamp. The emergency pause is the extension's,
in `FlareTeeManager`: it stops new submissions and freezes the last value; it
cannot withdraw one.

### Price only

Supply and backing components are excluded: they may be commercially sensitive to
Hex (spec §4.1), and this payload is public and permanent. The consequence,
recorded in the spec, is that consumers cannot recompute `min(1, B/S)` from
on-chain data.

---

## 8. What tee-node's `POST /sign` actually does (`internal/teenode`)

Verified against tee-node `v0.0.27-0.20260914073453-9c6bc46a5ee6`, the version
pinned in `go.mod`. Reading the chain end to end:

```go
// internal/extension/server/server.go — signWithTeeHandler
msgHash := crypto.Keccak256(signRequest.Message)
signature, err := s.node.Sign(msgHash)

// pkg/utils/crypto.go — Sign
sig, err := crypto.Sign(accounts.TextHash(msgHash), privKey)
```

So the signed digest is:

```
keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message))
```

Three consequences, each of which breaks verification silently if missed. All
three are pinned by tests in that package.

1. **EIP-191 prefixing is applied by tee-node, not by us.** A verifier must
   recover against `toEthSignedMessageHash(keccak256(message))`, not against the
   bare hash; for a feed update `keccak256(message)` is the `messageHash` of §7.
   Recovering against the bare hash yields a well-formed but *wrong* address, so
   the failure looks like an unregistered TEE rather than a hashing mismatch.
2. **`crypto.Sign` returns `v` as 0 or 1.** `ecrecover` and OpenZeppelin's
   `ECDSA.recover` require 27 or 28; OZ rejects anything else outright. Exactly
   one side must add 27, and the verifying side is upstream's `Fdc2Verification`,
   which uses OpenZeppelin unmodified and is not ours to change. So `NormalizeV`
   adds it on the extension side, and `feedupdate.SplitSignature` refuses a raw
   recovery id rather than paper over one. The cost is that the published
   signature is not byte-identical to what `POST /sign` returned.
3. **The wire is base64, not hex.** tee-node's `SignRequest.Message` and
   `SignResponse.Signature` are Go `[]byte`, and `encoding/json` renders `[]byte`
   as base64. Everything else in FCC is hex, so this is a genuine asymmetry.

OpenZeppelin's `ECDSA.recover` also rejects the high-`s` form of a signature, so
one update has exactly one accepted signature (findings §10); go-ethereum's
`crypto.Sign` produces the low-`s` form.

`teeIDProbe` exists because tee-node exposes no "who am I" endpoint: signing a
fixed, domain-separated constant and recovering the signer is the only way to
learn the identity key's address without adding plumbing outside the enclave.
Being domain-separated, that signature can never be a valid feed update,
provisioning envelope or readiness report. The address is resolved at startup,
retrying for up to a minute while tee-node's sign server comes up, and the process
exits if it never arrives (`pkg/server`): the provisioning verifier is bound to
it, and a machine without an identity could accept no credential.

---

## 9. Provisioning (`pkg/wire/envelope`, `internal/provisioning/envelope`, `internal/provisioning/store`)

Three things the extension needs are deliberately not compiled into the image:

- **RPC endpoints**, which arrive by on-chain instruction so that *which
  providers we read* is publicly verifiable — that visibility is the point. The
  restricted cash endpoint arrives the same way, as a salted commitment to its URL
  (§10).
- **The admin signer sets**, which arrive the same way, for the same reason:
  *who may provision this machine* should be as verifiable as which sources it
  reads.
- **Credentials**, and the cash URL the commitment names, which arrive by direct
  instruction: no gas, no permanent on-chain record of a ciphertext, instant
  rotation, and no secret-handling path in Solidity.

### Why the admin set is published rather than baked

An earlier design compiled the admin set into the image, on the reasoning that
the root of trust must not be substitutable. That reasoning holds only if the
substitution channel is weaker than the thing it replaces, and here it is not:
`SET_ADMINS` is published by the sender's governance — on a production deployment,
Flare governance's timelocked `setAdmins` — and that party can already choose
every price source (findings §15). Baking the set bought nothing governance did
not already have, and cost a rebuild and re-attestation for every rotation.

What the on-chain route adds is a public record. The payload is calldata,
`latestAdminsHash` records it per feed, and the feed store refuses an update
whose `adminsHash` is not the published one — so "who could have installed the
credentials behind this price" is answerable from the chain alone, for any
historical report. A baked set answered that question only for whoever compared
code hashes.

The bootstrap works because there is no default. A verifier starts with **no**
admin sets and rejects every envelope until they are installed, so there is no
guessable set and no first-come window to race for. They are memory-only like
everything else here, which means a restart returns the machine to that state:
`SET_ADMINS` must precede `SET_SECRET` every time. That ordering is the cost of
the design, and it is the safe direction — a machine that has forgotten who its
admins are refuses to be provisioned rather than trusting whoever asks first.

### Two sets, because the two credential classes differ

`SET_ADMINS` publishes **two** signer sets, and the envelope's `ref` decides
which one authorises it:

| set | governs | floor |
|---|---|---|
| `backing` | `hex_api_key` and `hex_cash_url` (`protocol.BackingRefs`) | 1 signer |
| `providers` | every other ref | 2 distinct signers |

The split follows the exposure, which is genuinely asymmetric:

- A **provider** credential is substituted into a URL that `SET_ENDPOINTS`
  chooses. Since there is no host allowlist (findings §15), the publisher can
  already name `https://attacker.example/{secret}` and collect that credential on
  the next observation. It is also the class an operator rotates routinely, and a
  rogue value denies service on every chain naming it.
- The **backing** refs cannot be routed anywhere. `hex_api_key` is only ever the
  `X-API-Key` header of the cash request, and `hex_cash_url` is the preimage of a
  commitment governance published; `validate` refuses either as a chain
  provider's secret. The key therefore goes only to the URL that matches the
  published commitment, and a URL that does not match is refused. A backing signer
  acting alone can deny the cash read — a wrong key, or a URL that matches nothing
  — but not move the price: without cash the feed publishes only where bonds alone
  reach par (§4). Moving the cash figure would take governance's commitment and a
  backing admin's preimage together, and governance can already move the price
  through the RPC providers.

A single signer is accepted for `backing` on that impact argument: the blast
radius of a wrong value is denial, not a forged reserve figure. It is worth being
explicit that this is the *narrower* of the two protections applied to the
*better-protected* credentials — the reasoning is impact, not exposure. If the
bonds URL stops being pinned, or the cash URL stops being bound by a governance
commitment, the floor must go back up.

Two properties make the split mean anything:

- **The ref-to-set mapping is pinned in the build, never carried in the payload.**
  If the published document could name its own refs, whoever held the 1-of-1
  backing key could claim the provider credentials and the split would authorise
  nothing.
- **A signer from the other set does not count.** Each envelope is verified
  against exactly one set, so the backing key cannot help satisfy a provider
  threshold or the reverse. A ref this build has never heard of falls under the
  *stronger* set, so a new credential name cannot be slipped in on one signature.

The sets may overlap — the same person can hold both roles — since that changes
nothing about which set authorises which envelope.

### The envelope

```
body = abi.encode(
    bytes32 domain,       keccak256("USDX_ORACLE_PROVISION_V1")
    uint256 chainId,      binds to one network
    address teeId,        binds to ONE machine
    uint64  version,      strictly increasing, blocks in-session rollback
    uint64  issuedAt,     freshness, bounds replay without any state
    string  ref,          which secret or config slot this sets
    bytes32 payloadHash   keccak256 of the ciphertext or config blob
)
```

signed by at least the published threshold of **distinct** admins from the set
that governs `ref`, using the
standard EIP-191 personal-sign convention so a hardware wallet can produce them.
Distinctness is the whole point of M-of-N: without deduplication, one compromised
key could supply the same signature M times and satisfy the threshold alone.

`version` and `issuedAt` are complementary, not redundant. `version` is held in
memory and blocks replaying an older envelope while the process lives. `issuedAt`
bounds replay without any state, so it holds wherever the version counter has
reset and the identity has not. In this build the two reset together — tee-node
and the extension share one process, and tee-node generates its identity key at
every start — so the `teeId` binding already refuses an envelope captured before
a restart, and `issuedAt` keeps that from being the only bar.

`Freshness` is one hour: long enough to collect M signatures from separate people
in separate timezones, short enough that a captured envelope expires quickly. The
envelope is safe to pass around in the clear, so assume it leaks: this window is
how long a leaked one stays usable against the machine it names. Widening it
further is a real trade, not a free knob; the way to buy more room is to make the
per-ref version survive restarts, not to raise this. The blast radius is bounded
meanwhile: only `SET_SECRET` travels this path, so the worst a replay could
achieve is rolling one ref back to a credential that was already provisioned once.
An `issuedAt` more than `ClockSkew` (2 minutes) in the future is refused as a
broken clock rather than a fresh envelope.

Re-provisioning is manual, and a restart always needs it. The restarted machine
has a new identity, so it is registered again, brought up to the feed's
publications with the sender's permissionless `pushEndpoints` and `pushAdmins`
(no repository tool wraps them), and then sent freshly signed envelopes for its new
`teeId` (`upload-hex-key.sh`, `upload-rpc-keys.sh`). Nothing holds envelopes to
re-submit; an old one names a machine that no longer exists.

The high-`s` signature form is rejected. secp256k1 is malleable, so accepting
both forms would let one admin signature be presented as two distinct-looking
ones — which does not defeat the distinct-signer check, since both recover to the
same address, but rejecting keeps one signature meaning one thing. Recovery work
is bounded before any cryptography runs: at most `MaxSignatures` (16) signatures
are considered, and a `ref` is at most 64 bytes.

### One command per channel

Each command is valid over exactly one action type, enforced rather than assumed:

- `SET_ENDPOINTS` is **instruction-only**. Its authorisation is the sender's
  `onlyGovernance` check, which a direct action would bypass entirely — anyone
  able to reach the proxy could repoint the data sources.
- `SET_ADMINS` is **instruction-only**, on the same grounds and with more at
  stake: a direct action would let anyone who can reach the proxy install their
  own admin set and then provision the machine through it.
- `SET_SECRET` is **direct-only**. A credential has no business on-chain, where
  the ciphertext would be permanent and public.
- `GET_FEED` is **instruction-only**: over the direct channel it would hand a
  signed update to anyone holding the proxy key, outside the fee. `GET_READINESS`
  is **direct-only**: a status report is stale within minutes and not worth a fee.

A command sent over the wrong channel is refused as `WrongActionType`. Every
command shares one op type, so the two routers are the only fence, and neither has
a shared default arm.

`SET_ENDPOINTS` and `SET_ADMINS` are separate commands rather than two fields of
one payload, so either can be rotated without republishing the other, and because
they answer different questions: one decides which sources a price comes from,
the other decides who may put credentials in the machine that reads them.

Both carry their own monotonic `version`, assigned by the sender per feed, and the
store refuses one that is not newer than the last it applied. The instruction
channel has the registry's nonce, but these payloads are the root of trust for
every credential and every price that follows, so they do not lean on that alone:
a captured `SET_ADMINS` must not be able to reinstate a rotated-out signer, nor a
captured `SET_ENDPOINTS` a superseded provider set. Each also names its feed, and
a payload for another feed of the same extension is refused rather than installed
and then rejected by the store on every update. Rotation deliberately does **not**
reset the per-ref envelope versions — those are anti-rollback state independent of
who signs, and clearing them would let an envelope the outgoing admins signed be
replayed against the incoming set.

### Arrival is authorisation, so the listener is loopback-only

`processInstruction` treats arrival on the instruction channel as authorisation:
the sender's governance check has run and the data providers have cosigned the
instruction. That holds only if nothing but tee-node can deliver an action, so the
extension listens on `127.0.0.1` alone (`extension.NewExtension`). tee-node shares
the process and posts to localhost; a listener reachable from anywhere else would
let a network peer install its own admin sets and endpoints. tee-node binds its
sign server the same way, which is why that service can be unauthenticated.

Two more checks keep an instruction from being forged inside an action:

- The action's type picks the envelope it is parsed as, never a guess. Parsing one
  as the other does not fail — `encoding/json` drops unknown fields and yields an
  empty payload — and the instruction router is the privileged one, so an unknown
  type is refused with 400 rather than treated as an instruction.
- The action id must equal the instruction id inside the cosigned `DataFixed`.
  The two name the same instruction by construction, and nothing legitimate
  produces an envelope and contents that disagree, so a mismatch is refused with
  400 rather than reconciled.

### The provider list is unconstrained, by decision

Provider URLs and the per-chain threshold are set entirely at runtime, with **no
host allowlist**. Whoever can publish `SET_ENDPOINTS` can name any host and any
threshold, and the attested code will faithfully sign whatever those sources
return — so they can choose the published price. The published price is therefore
only as trustworthy as the sender's governance as well as the TEE. The trade — no
rebuild to add a provider or change 2-of-3 to 2-of-4 — was made deliberately; see
findings §15.

What is still enforced is unrelated to which host is named: **https only**, and a
credential that cannot restructure the URL it is substituted into.

### The URL-ref / auth-ref split

`urlUnsafe` lists characters a credential may not contain, because each one can
restructure the URL it lands in: `/` changes the path, `?` opens a query, `&`
splits or appends parameters, `#` truncates the rest to a fragment, and
whitespace terminates the request line. This matters because the URL author
(governance, via `SET_ENDPOINTS`) and the credential holder (the admins, via
`SET_SECRET`) are different parties. Neither should be able to change what the
other authorised. The set is declared once and used at both checkpoints —
`ApplySecret` on arrival and `resolveURL` at substitution — because two copies of
a security-relevant character set drift, and the one that drifts is the one that
stops matching.

The `{secret}` placeholder must sit after the authority, so a credential can
never change the host.

An endpoint's ref fills its URL, and nothing else: no endpoint names the one
header credential, `hex_api_key`. Its ref is pinned in `pkg/protocol` and the
value is attached to the cash request by `config.CashSource`. No backing ref may be used as a chain
provider's secret either. `hex_api_key` authenticates the cash endpoint and
`hex_cash_url` is that endpoint's URL; without the check a payload author could
name either as a provider secret and have it substituted into a path on a host of
their choosing, receiving it on the next observation. It matters most for these
refs because their destination is otherwise fixed by the commitment, and so is
not the publisher's to choose.

### Locking

`store.Store` holds two locks because they protect different things over
different spans:

- `applyMu` covers a whole `Apply*` call, including the verifier — which keeps
  per-ref version state in a plain map and is not safe for concurrent use.
  Nothing serialises above: `/action` is an ordinary `net/http` handler, so
  tee-node delivering two direct actions at once runs two `ApplySecret`
  goroutines, and a concurrent map write is a hard runtime throw that takes the
  process down. The replay defence depends on it too — two concurrent envelopes
  for one ref could otherwise both read the old version and both be accepted.
- `mu` covers only the data fields, in short critical sections. It is
  deliberately **not** held across the tee-node round trip in `ApplySecret`: it
  is also taken by `Readiness` and `Open`, so a hung `/decrypt` would otherwise
  freeze every observation and every readiness answer.

Order is `applyMu` then `mu`, never the reverse. The size bound on `SET_SECRET`
is checked before `applyMu` is taken, so an oversized message cannot hold the
lock while it is rejected.

`Open` runs per observation rather than being cached. Dialling an HTTP RPC
endpoint opens no connection, so it costs nothing, and it removes a whole class
of staleness bug: there is no snapshot that can disagree with the configuration
it came from.

Credentials are memory-only. Nothing is written to disk; a restart loses every
credential, and the machine's identity with it. See the envelope above for what
re-provisioning takes.

---

## 10. Pinned facts vs runtime knobs (`pkg/protocol`, `internal/config`)

Everything in the pinned blocks — the chain set, token addresses, `MaxBlockAge`,
the credential refs and admin floors and the feed id in `pkg/protocol`; the
deadlines, bounds, bonds URL and metric names in `internal/config` — is compiled
into the binary and therefore covered by the registered code hash. These are
settled facts and safety properties, not knobs: the contract addresses are
verified on-chain (spec §3.1), the per-chain `MaxBlockAge` values come from
measured block times, and the observation bounds are what keep the two sides of
the ratio close together.

Configurable at runtime, and governed: the RPC provider URLs and each chain's
quorum (`SET_ENDPOINTS`); the cash endpoint's URL, as a salted commitment in
`SET_ENDPOINTS` and its preimage by `SET_SECRET`; the admin signer sets
(`SET_ADMINS`); and the credentials (`SET_SECRET`). `EXTENSION_ID` and `CHAIN_ID`
come from the environment and have no default. Of these, only the provider list
lets one party move the price, and that is the accepted trade of findings §15.

- **The bonds URL is pinned; the cash URL is committed.** The bonds endpoint is
  public and fixed: redirecting it would hand whoever publishes configuration the
  numerator of the price. The cash endpoint is restricted, so its URL cannot be
  published in the clear, and pinning it would put it in the image. Instead the
  `hex_cash` group carries exactly one PRIVATE endpoint at threshold 1, naming
  `hex_cash_url`, whose `urlHash` is `keccak256(abi.encode(salt, url))`. A backing
  admin provisions `abi.encode(salt, url)` by `SET_SECRET`, and the enclave refuses
  it unless it reproduces the commitment, the salt is non-zero, and the URL is a
  complete https URL with no placeholder. The salt is what makes the commitment
  safe to publish: a bare hash of a low-entropy URL is brute-forceable from chain
  data. A PUBLIC entry is refused, because it would put the restricted URL on
  chain in the clear and let the publisher point the numerator anywhere. The match
  is checked in both orders of arrival: `ApplySecret` against the live
  configuration, and `ApplyEndpoints` against every stored secret.
- **Each response must name its metric.** Both Hex endpoints share one response
  shape, so `metric_name` is the only field distinguishing a reserve amount from
  another metric on the same host. The enclave compares it on every read
  (`current_reserves_amount` for bonds, `current_cash_reserves_amount` for cash)
  and refuses a mismatch as `BackingMetric`. It matters most for the cash endpoint,
  whose URL is provisioned rather than pinned: a commitment to the wrong path on
  Hex's host would otherwise feed another figure into the numerator. It is a check
  on a mistake, not on an adversary who controls the endpoint.
- **`MaxBlockAge` is pinned, not configurable.** It is the check that stops a
  hostile provider rewinding the observation, so letting the same key that
  chooses the providers also choose how stale a block may be would give the check
  away with the attack. Ethereum gets more room than Flare and Songbird because
  its head is routinely 12s+ behind wall-clock while they run ~1.8s blocks. It
  bounds the *freshness* of the chosen block, not a depth below the head.
- **`RequireCurrency` must be false for the public endpoint.** Measured 31 Jul
  2026, the two endpoints return different shapes and the public one omits
  `currency` entirely. A single shared struct that asserts `currency == "USD"`
  would fail closed against it forever.
- **`ChainID` and `ExtensionID` are tee-node's.** Both bind every signed artefact
  (provisioning envelopes, feed updates) to one network and one extension, so an
  envelope signed for Coston2 cannot be replayed against mainnet. The extension
  reads them from the same `CHAIN_ID` and `EXTENSION_ID` the node reads, so the
  two halves of one process cannot disagree, and neither has a default: a
  process without them stops at startup, because a default that happens to
  differ from the node's fails silently, with every update signed for the wrong
  chain and rejected as a stranger's. The feed id, by contrast, is a build fact
  (`protocol.FeedID`): it names what this code computes.
- **The `GET_FEED` request names the feed.** One sender serves every feed of its
  extension, so the instruction message is upstream's ABI-encoded
  `FeedUpdateRequest{feedId}`, one word. The handler requires exactly that word
  and this build's feed id (`feedupdate.DecodeFeedRequest`), and refuses anything
  else as `UnexpectedPayload` without echoing it, so no caller builds on a field
  the extension drops. An empty message was never an option: the registry rejects
  one with `MessageEmpty()` (findings §22). `GET_READINESS` takes the one-byte
  `protocol.ReadinessRequestV1` on the same terms.
- **`HexCashAuthHeader` is unresolved.** OPEN (spec WP1): the endpoint returns
  404 rather than 401 without credentials, so bearer vs `X-API-Key` cannot be
  distinguished until a key is issued.
- **Every upstream call is bounded.** `ProviderTimeout` (3s) per RPC answer and
  `MaxRPCResponseBytes` (1 MiB) per RPC body (§6); `HTTPTimeout` (5s) per Hex
  fetch attempt, two attempts, bodies capped at 64 KiB and redirects never
  followed (§11); the whole observation inside `ObservationDeadline` (20s); each
  tee-node call inside 1s (§3). Each bound is what keeps one slow or hostile
  upstream from consuming the round, so none is a knob.

### Health is seeded, never absent

`newHealth` seeds every known source as `unknown`. Absence must never read as
health: an empty map is indistinguishable from "everything fine" to anyone
reading the readiness report, and that is exactly the state where a source has
silently stopped being polled. The cash source is seeded by its pinned name even
when unconfigured, so it never simply vanishes from the report — an absent key reads as
"no such source", which is the one thing this map must never say.

---

## 11. No accidental stub (`internal/extension`)

The scaffold's `New(port, signPort)` was removed because it read as the ordinary
way to build an extension while silently producing a stub — which is how the
production entrypoint ended up serving a hard-coded price (findings §12).

The next defence logged a banner at startup, which is useless inside a TEE: the
logs cannot be read from outside, and a feed frozen at $1.00 looks exactly like a
healthy fully-backed one.

So there is no way left to build a stub at all. The one constructor panics on a
nil observer, and no stub path exists: the conformance replay
(`go test ./internal/extension/ -run TestConformance`) exercises the real router
with a fixed observation — no tee-node, no chain, no credentials.

Redirects are never followed when fetching backing figures: Go strips
`Authorization` on a cross-host redirect, but it does **not** strip custom
headers — and the private endpoint may well use `X-API-Key`. A compromised or
misconfigured upstream could then 302 to a host of its choosing and be handed the
key. An API client has no legitimate need to follow redirects, so the safe
behaviour is also the simple one.

Unknown JSON fields are rejected rather than ignored: a shape change from Hex is
a signal to stop and look, not to keep publishing prices from a response we no
longer fully understand.

---

## 12. The keeper (`tools/pkg/keeper`)

The keeper holds no authority. `TeeOracleFeedStore` verifies every signature
through `Fdc2Verification` against the machine registry, so everything in the
keeper is liveness: the worst a wrong invocation can do is waste gas and fees.
Nothing there may compute, adjust or sanity-"fix" a price. It decodes each
machine's update, forwards it unchanged, and the store takes the median.

### Why there is no persistence

Every partial cycle is harmless. Crash after the trigger and the instruction is
already dispatched, the TEE still computes, and the result sits unread — one
wasted fee. Crash before the submit and a co-keeper relays it, or the next cycle
supersedes it. Nothing is two-phase, nothing needs to be exactly-once, and
`observedAt` monotonicity makes duplicate submissions safe by construction.

So the keeper keeps no state across cycles and needs no recovery logic. The
records it emits are write-only: nothing reads one back to make a decision.

### Single-flight

Cycles run sequentially in one goroutine, so one can never overlap its successor.
Two in-flight instructions from one keeper mean two nonce-consuming transactions
and a real chance of submitting reports out of order, which burns fees to
accomplish nothing since the store accepts only the newer one. A cycle that
overruns the interval starts the next one as soon as it finishes, never beside
it, and the overrun is logged: a keeper persistently unable to finish inside `T`
is one whose deadlines no longer fit its cadence.

The poll and submit deadlines are spent out of the consumer's freshness budget —
`observedAt` is the request's block timestamp, so every second the keeper spends
after requesting is one the published value is older by when it lands.

### The outcome taxonomy

"No price landed" has several causes, they route to different owners, and one of
them is not a fault at all. Collapsing them into a single "failed" is what
produces an alert nobody trusts.

`race_lost` is the important one: another submission whose `observedAt` is at
least ours landed first — almost always another keeper's, often for a later
request. The feed advanced; that is the system working, and it must never alert.
Every `NotNewer`, whether from the preflight, the rehearsal or a mined submission
that reverted, is classified by re-reading the store's `observedAt` against ours.
The ratchet only moves up, so a `NotNewer` while the store reads *lower* than ours
cannot be a lost race; it is recorded as `rejected`, naming the guard. Only one
submission per block timestamp can ever land: two requests mined in the same
block share one `observedAt`, so at most one of their batches is accepted.

`sourceHealth` on a record is each machine's own view of its sources, from the
readiness preflight. The poller observes every two minutes whether or not anyone
requests, so the map is current while the machine runs, but it is what the
machine says about its inputs, not evidence that a value landed. That is why the
alerting in the monitoring doc pages on the on-chain feed's age and not on that
map. A missing map means no machine could be asked, and must never be read as
"the sources are fine".

`Preflight` checks each update against the store's cheap guards — the feed
binding and the configuration commitments — and then rehearses
`submitFeedUpdates` by `eth_call`. It turns a revert into a named, actionable
reason, and it does so before a transaction is paid for.

### Fees are read, never hard-coded

The registry's instruction fee is governance-settable, and a stale constant fails
closed in the worst way: every request reverts, no price is published, and
nothing in this repo changed to explain it. The keeper reads the operation fee for
(`TEE_ORACLE`, `GET_FEED`) from the diamond, falling back to its default fee where
no row is set, and pays it once per target. `requestFeedUpdate(feedId, teeIds)`
takes its targets explicitly, from the keeper's static `-tee` list, and the
registry charges per machine addressed, which is the figure
`calculateFeeByTeeIds` computes. The op type and command come from
`pkg/protocol`, and must match the sender's bytes32 constants exactly: a
mismatch prices the wrong fee row, and the instruction is rejected as underpaid.

### Why `feedupdate` lives in `tools/`

The tooling decodes a signed update through the extension's own `pkg/wire/feedupdate`, so
the layout is declared once and the tooling follows a change to it by construction. What
`tools/pkg/feedupdate` adds is what the enclave never does with its own output: recovering
the machine that signed, naming the feed, and rendering the value for a human. That is why it
is one package rather than a copy in every command that needs it. The independent check on
the layout is not a second Go declaration but Solidity's own encoding in
`deploy/testdata/wire`, which the codec is tested against; `docs/extension-contract.md`
remains the wire contract any other implementation of the extension is held to, and the
tooling runs unchanged against one because it speaks that contract, not one binary's quirks.

---

## 13. Operational notes

- **The sender and the store belong to one extension.** The sender's
  `extensionId` is set at initialisation and cannot change, and the store caches
  it, so an extension registered again under a new id needs a new sender and a new
  store. `deploy-oracle.sh` reuses a recorded sender only when it was deployed for
  the current `EXTENSION_ID` and is governed by the current deployer, and deploys
  anew otherwise. A new store starts empty: `getCurrentFeed` reverts
  `NoValuePublished` until its first update, the `observedAt` ratchet restarts,
  and an FtsoV2 registration of the feed names the old store's address, so it has
  to be replaced through governance as well.
- **Governance signers must match the node's env.** The node derives its
  `governanceHash` from the same `(signers, threshold)` pair via
  `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD`, and the registry only accepts a
  machine whose signed `governanceHash` matches what `set-governance` registered
  on-chain.
- **The started stack names its proxy.** The compose stack publishes the proxy on 6674
  and a host-process stack binds 6684 directly, in a block clear of the e2e simulation's own processes; rather than every follow-up script and tool
  needing to be told which, `start-services.sh` records the URL of the stack it brought up in
  `deploy/config/stack.env`, read after `.env` and above the environment (the e2e scripts
  share that shell), so the commands after a start are the same for both. The tools' `-p`
  flag is the explicit override; on the testnets nothing is recorded and `.env` applies.
- **`--local` mode is Go-only.** `cmd/dev/start-tee` links the Go extension
  in-process, so it lives in the extension module; `tools/` must stay independent
  of any one language implementation so it can deploy and test all of them.
- **`cmd/dev/mocknode` is not secure and not for deployment.** Its key is derived
  from a fixed seed so runs are reproducible, which is the opposite of what a real
  TEE guarantees. It exists so the provisioning flow can be exercised without
  Confidential Space: `SET_SECRET` carries a ciphertext that must be decrypted
  in-enclave, so without a decrypt endpoint the path cannot be tested at all.
