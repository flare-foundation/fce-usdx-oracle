# USDX oracle — design rationale

> **Migration note (2026-08).** The on-chain half moved to the upstream generic TEE
> oracle contracts (`TeeOracleInstructionsSender` + `TeeOracleFeedStore`, op type
> `TEE_ORACLE`, `FeedUpdate` payload). The rationale below for the *enclave* — the
> price formula, supply quorum, reserve handling, fail-closed behaviour, credential
> split — still applies. Where it names the old contracts, op types, or store guards
> (`UsdxPriceStore`, `GET_PRICE`, `minPrice`/`maxAge`), read it against the new model
> in `usdx-oracle-flows.md`.

Why the code is shaped the way it is. This was originally carried as long
package-doc comments in `internal/*` and `tools/pkg/*`; it was moved here so
the code stays readable and the reasoning stays available. The code keeps a
one-line pointer wherever the reasoning is load-bearing.

Companion documents: [spec](usdx-oracle-spec.md) ·
[findings](usdx-oracle-findings.md) · [keeper](usdx-oracle-keeper.md) ·
[monitoring](usdx-oracle-monitoring.md) · [availability](usdx-oracle-availability.md)

---

## 1. The public error vocabulary (`internal/failure`)

Two of the extension's outputs are world-readable:

- `ActionResult.Log` — returned to the TEE proxy and served from its
  action-result endpoint (`docs/extension-contract.md` §4.6).
- `GET /state` — polled by tee-node, folded into `TEEState.state`, attested, and
  republished by the proxy at `GET /info` (spec §4.1).

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
constructor, so a `Failure` can only be one of the package-level values. There is
no code path that puts a runtime value into a public string — not by convention
or review, but because the compiler rejects it. Adding a failure mode means
adding a value to that one reviewable file.

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
`teeId`, wrong chain, wrong domain, undecryptable ciphertext, disallowed host. A
caller learns whether provisioning succeeded, never which check failed;
distinguishing them would turn the endpoint into an oracle for probing the admin
set and the freshness window.

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

`ObservationWindow` (30s) bounds the spread between the earliest and latest
input read. It is a constant rather than a knob because widening it silently
weakens the bound on how far the two sides can drift apart.

### Fail closed, always

Any source down, any value implausible, any window exceeded: publish nothing and
return a failure. Never guess, never interpolate, never fall back to a cached
figure. A frozen feed with an ageing timestamp is the designed failure mode
(spec §7.15) and consumers are required to check the timestamp; a confidently
wrong price is not recoverable downstream.

### `observedAt` is wall-clock, not the oldest input

The store requires strict monotonicity, and an input timestamp can stall
indefinitely: `last_update` moves only when a mint or a redemption changes the
figure, so on a quiet day every observation for hours would share one, and all
but the first would be rejected as a replay. `observedAt` therefore stamps when
the inputs were **read**, and that is what `MaxObservationAge` (10 min) and the
store's `maxAge` (3600s) each bound. There is deliberately no bound on how old
the reserve figure itself is — see [§ *No freshness bound on the reserve
figures*](#no-freshness-bound-on-the-reserve-figures) below.

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
upstream rather than an aged figure. Staleness is now bounded only where it can
be measured honestly — on the age of our own observation, and on the age of the
on-chain feed.

---

## 3. Observing on a timer, signing on demand (`internal/observation/poller`)

`Observe` is slow — five upstream sources, two of them multi-provider — and
tee-node gives an extension **2 seconds** to answer an action
(`settings.ProxyTimeout`, a const in its internal package, so not configurable
from here). Measured against live sources on 5 Aug 2026 an observation takes
2.0–2.9s, so running one inside the request handler timed out every time, and the
extension's answer never reached the chain at all.

So the two halves are separate calls. `internal/observation/poller` runs `Observe` on a
timer and keeps the latest result; the handler calls `Sign`, which binds that
result to the requesting instruction id and returns in one signing round trip.
The instruction id is why signing cannot happen in advance, and the upstream
latency is why observing cannot happen on demand.

What is signed is always the observation's own figures — its price, its
`observedAt`, its `EndpointsHash`. Nothing is resampled at signing time, because
a fresh timestamp over stale inputs is exactly the lie the freshness guards exist
to prevent. Staleness is bounded by the caller instead: `config.MaxObservationAge`.

**Cadence.** `ObservationInterval` is 2 minutes against Hex's ~10 minute refresh
— deliberately faster than the source. It keeps the supply side current and,
more importantly, leaves room for several consecutive failures inside
`MaxObservationAge` (10 minutes, so four consecutive failed observations before
the feed stops updating). `MaxObservationAge` must stay well inside
`UsdxPriceStore.maxAge` (3600s at deployment), which bounds the same staleness
from the other end: one caps how old the inputs may be, the other caps how long
the report may take to land.

---

## 4. Observing without cash

The cash figure is the one input with a single unredundant source — Hex's
restricted endpoint, one URL, gated by one credential. `Cfg.Cash` is therefore
allowed to be nil (no credential provisioned), and a fetch failure is not
automatically fatal.

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

Every amount feeds a price published on-chain as a `uint256`. `float64` has 53
bits of mantissa; a reserve figure like 25,452,544.52 is representable only
approximately, and the error compounds through the division. Amounts are parsed
into `*big.Rat`, which represents any decimal input exactly and divides without
loss.

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
2. take the **Quorum-th highest** — the highest block at least `Quorum` of them can serve
3. refuse it if the block's own header timestamp is older than `MaxBlockAge`
4. query every provider at that exact number
5. require `Quorum` **identical** results, else fail the whole observation

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
providers can serve the pinned block — while making the block choice obey the
same rule as the values, so `Quorum-1` liars cannot move it. `MaxBlockAge` covers
what the selection rule cannot: a chain with a single provider, a chain where
`Quorum` equals the provider count, and a chain that has simply stopped.

Identical, not median. A read at a fixed block is deterministic: two honest
providers cannot disagree, so disagreement means one is wrong or lying and there
is no safe way to average that.

The minimum rather than the maximum or the median, because it is the only choice
guaranteed to name a block every provider can actually serve. A higher block
would make the laggard fail its query every round, silently reducing the
effective provider count to those that happen to be ahead.

### Constraints worth knowing

- The margin must stay inside the shallowest provider's state window or that
  provider drops out of quorum on every round. Measured retention ranges from
  latest-only to full archive (publicnode/1rpc ~64 blocks, blockrazor ~128,
  drpc/mevblocker ≥100k).
- `eth.merkle.io` cannot serve explicit-block queries at all and is unusable here
  despite being reachable.
- Only `eth_call` and block lookups are used — never `eth_getLogs`. Worth
  recording because Flare's public RPC caps log ranges at 30 blocks, which looks
  like a blocker until you notice this package does not need it.
- `decimals()` is read every time rather than hard-coded: USDX is 6 on Flare and
  Songbird but 18 on Ethereum, which is normal for a LayerZero OFT and a live
  footgun. Assuming a value would silently misprice a chain by 10¹².
- Songbird has exactly one working public RPC, so a 3-provider rule cannot be met
  there for free. `Quorum: 1` is a deliberate acceptance of single-provider risk
  for that chain's share of supply, not an oversight — see spec §2.8.

### No exclusions

Supply is the plain sum of `totalSupply()` over the configured contracts. There
is no exclusion list and no `balanceOf` call: the contract list **is** the
policy, and it is pinned in configuration covered by the code hash. The set must
match the basis on which Hex reports reserves, or the ratio compares two
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

## 7. The signed payload (`internal/wire/feedupdate`)

```solidity
struct PriceReport {
    bytes32 domain;         // keccak256("USDX_ORACLE_PRICE_V1")
    uint256 price;
    int8    decimals;
    uint64  observedAt;
    bytes32 instructionId;
    bytes32 endpointsHash;  // keccak256 of the active SET_ENDPOINTS payload
    bytes32 adminsHash;     // keccak256 of the active SET_ADMINS payload
}

body = abi.encode(report)
data = abi.encode(body, signature)
```

FCC is explicitly "fire and forget": no ordering guarantee, no delivery
guarantee, no replay protection beyond what a payload arranges for itself.

- **domain** separates this signature from any other thing the TEE identity key
  ever signs. Without it, a signature produced for some future USDX instruction
  could be replayed as a price.
- **observedAt** is the store's monotonicity key, so it MUST be inside the signed
  body. Outside, a relayer could rewrite it and either replay an old price as
  fresh or push a real price out of order.
- **instructionId** ties the report to the instruction that requested it.
- **endpointsHash** commits to the configuration this price was computed under.
  `UsdxPriceStore` compares it against what `setEndpoints` published for this
  machine, so a machine running a stale or divergent configuration cannot submit
  at all. Without it the on-chain config is advisory: it records what was *sent*,
  never what a machine is actually *running*, and with several machines a missed
  `setEndpoints` call leaves one publishing perfectly valid prices from the wrong
  configuration.
- **adminsHash** commits to the admin signer set in force when the price was
  computed, on exactly the same terms and for the same reason. A price is only as
  trustworthy as the party that could install the credentials behind it, so a
  machine running a superseded admin set cannot submit either — the rotated-out
  signers might still be the ones it honours.

`adminsHash` was appended to the body **without** bumping `domain` to
`USDX_ORACLE_PRICE_V2`, which the rule above otherwise requires. That is a
deliberate development-phase exception, taken because the extension and
`UsdxPriceStore` are redeployed together and no mixed pair is ever live. The
body-length check on both sides is what enforces it: a peer still on the
six-word layout fails the length check outright rather than having its body read
as one that merely omits the admin commitment. Solidity's `abi.decode` tolerates
trailing bytes, so without that explicit check a longer body would be silently
truncated to the fields the older store knows about. Bump the domain the moment
the two can ship independently.

### No target binding, by decision

The payload carries no chain id, store address or feed id. *Where* a report is
accepted is settled by the store's trust anchor instead: a signature is only ever
accepted from a TEE machine currently registered to the store's extension
(`UsdxPriceStore._requireRegisteredTee`). The payload binds *what* was observed;
the registry check plus governance bind where it lands.

The trade — a genuinely-signed report can be replayed into another deployment
registered under the same extension, though nobody can forge a price — was made
deliberately before launch; see the findings doc.

### Carry the body, never re-encode it

`data` carries `body` as raw bytes rather than the struct's fields. The store
verifies the signature over exactly the bytes it received, then decodes them.
Re-encoding on the way in would make verification depend on the encoder agreeing
byte-for-byte with the TEE's, and any future divergence — a field reordering, a
padding difference — would present as an invalid signature rather than as the
encoding bug it is.

The same rule applies to `endpointsHash`: the payload is hashed **as received**,
never re-serialised, because `json.Marshal` will not reproduce byte-identical
input and a re-encode could never match `keccak256(_config)` from the transaction.

### Price only

Supply and backing components are excluded: they may be commercially sensitive to
Hex (spec §4.1), and this payload is public and permanent. The consequence,
recorded in the spec, is that consumers cannot recompute `min(1, B/S)` from
on-chain data.

---

## 8. What tee-node's `POST /sign` actually does (`internal/teenode`)

Verified against tee-node `v0.0.21-0.20260619120252-31fc839ae6d2`, the version
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

1. **EIP-191 prefixing is applied by tee-node, not by us.** The verifying
   contract must recover against `toEthSignedMessageHash(keccak256(body))`, not
   against the bare `keccak256(body)`. Recovering against the bare hash yields a
   well-formed but *wrong* address, so the failure looks like an unregistered TEE
   rather than a hashing mismatch.
2. **`crypto.Sign` returns `v` as 0 or 1.** `ecrecover` and OpenZeppelin's
   `ECDSA.recover` require 27 or 28; OZ rejects anything else outright. Exactly
   one side must add 27 — if both do, or neither does, every signature fails.
   `NormalizeV` does it on the extension side so the contract can use stock
   OpenZeppelin unmodified. The cost is that the published signature is not
   byte-identical to what `POST /sign` returned.
3. **The wire is base64, not hex.** tee-node's `SignRequest.Message` and
   `SignResponse.Signature` are Go `[]byte`, and `encoding/json` renders `[]byte`
   as base64. Everything else in FCC is hex, so this is a genuine asymmetry.

`teeIDProbe` exists because tee-node exposes no "who am I" endpoint: signing a
fixed, domain-separated constant and recovering the signer is the only way to
learn the identity key's address without adding plumbing outside the enclave.
Being domain-separated, that signature can never be a valid price report or
provisioning envelope.

---

## 9. Provisioning (`internal/provisioning/envelope`, `internal/provisioning/store`)

Three things the extension needs are deliberately not compiled into the image:

- **RPC endpoints**, which arrive by on-chain instruction so that *which
  providers we read* is publicly verifiable — that visibility is the point.
- **The admin signer set**, which arrives the same way, for the same reason:
  *who may provision this machine* should be as verifiable as which sources it
  reads.
- **Credentials**, which arrive by direct instruction: no gas, no permanent
  on-chain record of a ciphertext, instant rotation, and no secret-handling path
  in Solidity.

### Why the admin set is published rather than baked

An earlier design compiled the admin set into the image, on the reasoning that
the root of trust must not be substitutable. That reasoning holds only if the
substitution channel is weaker than the thing it replaces, and here it is not:
`SET_ADMINS` is owner-only, and the owner key is a governance address that can
already choose every price source (§15). Baking the set bought nothing the owner
key did not already have, and cost a rebuild and re-attestation for every
rotation.

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
| `backing` | `config.HexAPIKeyRef` only | 1 signer |
| `providers` | every other ref | 2 distinct signers |

The split follows the exposure, which is genuinely asymmetric:

- A **provider** credential is substituted into a URL that `SET_ENDPOINTS`
  chooses. Since there is no host allowlist (§15), the owner key can already
  publish `https://attacker.example/{secret}` and collect that credential on the
  next observation. It is also the class an operator rotates routinely, and a
  rogue value denies service on every chain naming it.
- The **backing** credential cannot be routed anywhere: `validate` refuses to let
  `hex_api_key` be named as a provider secret, and it is only ever an auth header
  on a pinned URL. It is therefore the one credential the owner key cannot obtain
  by other means — and, because the endpoint is pinned, a wrong value degrades
  the feed to cash-less rather than moving the price.

A single signer is accepted for `backing` on that impact argument: the blast
radius of a wrong value is denial, not a forged reserve figure. It is worth being
explicit that this is the *narrower* of the two protections applied to the
*better-protected* credential — the reasoning is impact, not exposure, and if the
backing endpoint ever stops being pinned the floor must go back up.

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
    uint64  issuedAt,     freshness, blocks cross-session replay
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
survives restarts, where the version counter resets to zero and would otherwise
let a captured envelope be replayed indefinitely.

`Freshness` is one hour: long enough to collect M signatures from separate people
in separate timezones, short enough that a captured envelope expires quickly. It
is deliberately the *only* bar left after a restart — the envelope is safe to
pass around in the clear, so assume it leaks, and this window is exactly how long
a leaked one can re-provision a machine whose version counter has just reset.
Widening it further is a real trade, not a free knob; the way to buy more room is
to make the per-ref version survive restarts, not to raise this. The blast radius
is bounded meanwhile: only `SET_SECRET` travels this path, so the worst a replay
achieves is rolling one ref back to a credential that was already provisioned
once. The consequence is that
recovering from a restart hours later needs a freshly signed envelope — the safe
direction, and why the keeper holds ciphertext envelopes to re-submit promptly
rather than relying on old ones.

The high-`s` signature form is rejected. secp256k1 is malleable, so accepting
both forms would let one admin signature be presented as two distinct-looking
ones — which does not defeat the distinct-signer check, since both recover to the
same address, but rejecting keeps one signature meaning one thing.

### One command per channel

Each provisioning command is valid over exactly one action type, enforced rather
than assumed:

- `SET_ENDPOINTS` is **instruction-only**. Its authorisation is the owner-only
  check in the on-chain sender, which a direct action would bypass entirely —
  anyone able to reach the proxy could repoint the data sources.
- `SET_ADMINS` is **instruction-only**, on the same grounds and with more at
  stake: a direct action would let anyone who can reach the proxy install their
  own admin set and then provision the machine through it.
- `SET_SECRET` is **direct-only**. A credential has no business on-chain, where
  the ciphertext would be permanent and public.

`SET_ENDPOINTS` and `SET_ADMINS` are separate commands rather than two fields of
one payload, so either can be rotated without republishing the other, and because
they answer different questions: one decides which sources a price comes from,
the other decides who may put credentials in the machine that reads them.

`SET_ADMINS` carries its own monotonic `version`. The instruction channel has the
registry's nonce, but the admin set is the root of trust for every credential
that follows, so it does not lean on that alone: a captured `SET_ADMINS` must not
be able to reinstate a rotated-out signer. Rotation deliberately does **not**
reset the per-ref envelope versions — those are anti-rollback state independent
of who signs, and clearing them would let an envelope the outgoing admins signed
be replayed against the incoming set.

### The provider list is unconstrained, by decision

Provider URLs and the per-chain threshold are set entirely at runtime, with **no
host allowlist**. Whoever can send `SET_ENDPOINTS` can name any host and any
threshold, and the attested code will faithfully sign whatever those sources
return — so they can choose the published price. The published price is therefore
only as trustworthy as the contract owner key as well as the TEE. The trade — no
rebuild to add a provider or change 2-of-3 to 2-of-4 — was made deliberately; see
findings §15.

What is still enforced is unrelated to which host is named: **https only**, and a
credential that cannot restructure the URL it is substituted into.

### The URL-ref / auth-ref split

`urlUnsafe` lists characters a credential may not contain, because each one can
restructure the URL it lands in: `/` changes the path, `?` opens a query, `&`
splits or appends parameters, `#` truncates the rest to a fragment, and
whitespace terminates the request line. This matters because the URL author (the
owner key, via `SET_ENDPOINTS`) and the credential holder (the admins, via
`SET_SECRET`) are different parties. Neither should be able to change what the
other authorised. The set is declared once and used at both checkpoints —
`ApplySecret` on arrival and `resolveURL` at substitution — because two copies of
a security-relevant character set drift, and the one that drifts is the one that
stops matching.

The `{secret}` placeholder must sit after the authority, so a credential can
never change the host.

Separately, `config.HexAPIKeyRef` may not be used as a provider secret. That
credential authenticates the **pinned** Hex endpoint, by header. Without the
check, a payload author could name it as a provider secret and have it
substituted into a path on a host of their choosing — receiving it on the next
observation. It matters most for this ref because it is the one credential whose
destination is otherwise pinned and therefore not the owner's to choose.

### Locking

`runtimecfg.Store` holds two locks because they protect different things over
different spans:

- `applyMu` covers a whole `Apply*` call, including the verifier — which keeps
  per-ref version state in a plain map and is not safe for concurrent use.
  Nothing serialises above: `/action` is an ordinary `net/http` handler, so
  tee-node delivering two direct actions at once runs two `ApplySecret`
  goroutines, and a concurrent map write is a hard runtime throw that takes the
  process down. The replay defence depends on it too — two concurrent envelopes
  for one ref could otherwise both read the old version and both be accepted.
- `mu` covers only the data fields, in short critical sections. It is
  deliberately **not** held across the tee-node round trips in `ApplySecret`: it
  is also taken by `Readiness` and `Resolve`, so a hung `/decrypt` would
  otherwise freeze `GET /state` and every observation.

Order is `applyMu` then `mu`, never the reverse.

`Resolve` runs per observation rather than being cached. Dialling an HTTP RPC
endpoint opens no connection, so it costs nothing, and it removes a whole class
of staleness bug: there is no snapshot that can disagree with the configuration
it came from.

Credentials are memory-only. Nothing is written to disk; a restart loses every
credential and the extension returns to `not_configured` until the keeper
re-submits.

---

## 10. Pinned facts vs runtime knobs (`internal/config`)

Everything in the "pinned deployment facts" block is compiled into the binary and
therefore covered by the registered code hash. These are settled facts and safety
properties, not knobs: the contract addresses are verified on-chain (spec §3.1),
the per-chain `MaxBlockAge` values come from measured block times, and the
observation bounds are what keep the two sides of the ratio close together.

Only two things are configurable at runtime — the RPC provider URLs and the
credentials. Everything an operator could change to move the published price
lives in the pinned block instead.

- **Hex endpoints are pinned** rather than configurable: these are the one source
  that cannot be swapped for another, so allowing an operator to redirect the
  reserve read would hand them the numerator of the price.
- **`MaxBlockAge` is pinned, not configurable.** It is the check that stops a
  hostile provider rewinding the observation, so letting the same key that
  chooses the providers also choose how stale a block may be would give the check
  away with the attack. Ethereum gets more room than Flare and Songbird because
  its head is routinely 12s+ behind wall-clock while they run ~1.8s blocks. It
  replaced `FinalityMargin`, a depth *below* the head that measurement showed
  bought no availability; this bounds the *freshness* of the chosen block
  instead — a different property.
- **`RequireCurrency` must be false for the public endpoint.** Measured 31 Jul
  2026, the two endpoints return different shapes and the public one omits
  `currency` entirely. A single shared struct that asserts `currency == "USD"`
  would fail closed against it forever.
- **`ChainID` is an ldflags constant.** It binds provisioning envelopes to one
  network, so an envelope signed for Coston2 cannot be replayed against mainnet.
  A constant rather than a config file because it is a pure function of which
  network the build targets.
- **`GetPriceRequestV1`.** The observation needs nothing from the caller, so the
  natural payload is none at all — but the registry rejects an empty `message`
  with `MessageEmpty()`, found the first time this path ran on Coston2 (5 Aug
  2026). One version byte is the smallest payload that satisfies it and still
  says something: the handler requires exactly this value, so an unrecognised
  envelope is refused rather than ignored, and a future version can change the
  request shape without ambiguity about which one arrived.
- **`HexCashAuthHeader` is unresolved.** OPEN (spec WP1): the endpoint returns
  404 rather than 401 without credentials, so bearer vs `X-API-Key` cannot be
  distinguished until a key is issued.

### Health is seeded, never absent

`newHealth` seeds every known source as `unknown`. Absence must never read as
health: an empty map is indistinguishable from "everything fine" to anyone
reading `GET /state`, and that is exactly the state where a source has silently
stopped being polled. The cash source is seeded by its pinned name even when
unconfigured, so it never simply vanishes from `/state` — an absent key reads as
"no such source", which is the one thing this map must never say.

---

## 11. No accidental stub (`internal/extension`)

The scaffold's `New(port, signPort)` was removed because it read as the ordinary
way to build an extension while silently producing a stub — which is how the
production entrypoint ended up serving a hard-coded price (findings §12).

The next defence logged a banner at startup, which is useless inside a TEE: the
logs cannot be read from outside, and a feed frozen at $1.00 looks exactly like a
healthy fully-backed one.

So there is no way left to build a stub by accident. Every production constructor
panics on a nil observer, and the only stub path is `NewStub`, named at its
single call site (`deploy/scripts/test-conformance.sh`, which exercises the extension
alone — no tee-node, no chain, no credentials).

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

The keeper holds no authority. `UsdxPriceStore` verifies the TEE signature
against `TeeMachineRegistry`, so everything in the keeper is liveness: the worst a
wrong invocation can do is waste gas. Nothing there may compute, adjust or
sanity-"fix" a price, and the signed body is relayed byte for byte.

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
overruns the interval therefore delays the next tick rather than running beside
it, and the overrun is logged: a keeper persistently unable to finish inside `T`
is one whose deadlines no longer fit its cadence.

The poll and submit deadlines are spent out of the store's `maxAge` budget —
`observedAt` is stamped inside the enclave before signing, so every second the
keeper spends is one `submitPrice` counts against the report's age.

### The outcome taxonomy

"No price landed" has several causes, they route to different owners, and one of
them is not a fault at all. Collapsing them into a single "failed" is what
produces an alert nobody trusts.

`race_lost` is the important one: with ≥2 keepers exactly one submission wins per
cycle and the rest lose the race. That is the system working, and it must never
alert. But the same `NotNewer` guard also fires when our own report was stale — an
old result relayed after a long delay — and that *is* a latency problem worth
seeing. Comparing against the store's own stamp is what separates "someone beat
me by a second" from "I am submitting minutes-old reports".

`sourceHealth` is a snapshot written only when an observation runs. If requests
stop, the last values persist and keep reading "ok" — which is why the alerting
in the monitoring doc pages on on-chain feed age and not on that map. A nil map
means the proxy could not be read, and must never be read as "the sources are
fine".

`Preflight` duplicates each of the store's guards deliberately: it turns a revert
into a named, actionable reason, and it does so before a transaction is paid for.

### Fees are read, never hard-coded

The registry's instruction fee is governance-settable, and a stale constant fails
closed in the worst way: every request reverts, no price is published, and
nothing in this repo changed to explain it. The op type and command come from the
deployed contract's own constants rather than being re-derived, so the fee lookup
cannot drift from the instruction actually sent.

`calculateFeeByTeeIds` is not used: `requestPriceUpdate` picks its machine
internally via `getRandomTeeIds`, so the caller cannot know the tee id in
advance.

### Why `pricereport` lives in `tools/`

The wire layout is declared there rather than imported from `internal/wire/feedupdate`
so that `tools/` stays independent of any one implementation: it asserts on the
*wire contract*, and must run unchanged against the Go, Python and TypeScript
extensions alike (`docs/extension-contract.md`). It is a second, deliberate
declaration of the same layout — which is also why it is one package and not a
copy in every command that needs it.

---

## 13. Operational notes

- **`set-store-extension` exists** because re-registering the extension mints a
  new extension id *and* a new instruction sender, both baked into the store's
  acceptance check. Without it the store would have to be redeployed, which
  resets the stored price and, on mainnet, invalidates the `addCustomFeeds`
  registration that points governance at the feed. Both values move together: a
  re-registration produces both, so changing one without the other would leave
  the store checking a config hash published by a contract nobody is using.
  `setExtension` deliberately leaves the stored price alone — consider
  `setInvalidated` if consumers must not read it in the meantime.
- **Governance signers must match the node's env.** The node derives its
  `governanceHash` from the same `(signers, threshold)` pair via
  `GOVERNANCE_SIGNERS` / `GOVERNANCE_THRESHOLD`, and `MachineManager.register`
  only accepts a machine whose signed `governanceHash` matches what was
  registered on-chain.
- **`EXT_PROXY_URL` defaults to 6674.** The container listens on 6664 and the
  compose file maps 6674 → 6664, so the two ports are the same proxy reached from
  different sides. Under `start-services.sh --local` the proxy binds 6664 on the
  host directly, so a bare `go run` there needs
  `EXT_PROXY_URL=http://localhost:6664` or `-p`.
- **`--local` mode is Go-only.** `cmd/start-tee` links the Go extension
  in-process, so it lives in the extension module; `tools/` must stay independent
  of any one language implementation so it can deploy and test all of them.
- **`cmd/mocknode` is not secure and not for deployment.** Its key is derived
  from a fixed seed so runs are reproducible, which is the opposite of what a real
  TEE guarantees. It exists so the provisioning flow can be exercised without
  Confidential Space: `SET_SECRET` carries a ciphertext that must be decrypted
  in-enclave, so without a decrypt endpoint the path cannot be tested at all.
