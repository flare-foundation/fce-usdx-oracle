# USDX Oracle — Implementation Findings

**Status:** living document · **Last updated:** 2026-08-03

Findings discovered while implementing the extension, each with the evidence
that established it and the test that now pins it. Design intent lives in
`usdx-oracle-spec.md`; this file records what turned out to be true in practice,
especially where it differs from, or sharpens, the spec.

Every finding below is enforced by a test. If a finding is ever wrong, or
tee-node changes underneath us, a named test fails rather than a price being
quietly mispublished.

| # | Finding | Severity | Pinned by |
| --- | --- | --- | --- |
| 1 | `ActionResult.Log` is public and raw Go errors leak secrets into it | **High** | `internal/failure` (whole package) |
| 2 | A redirect can hand the Hex API key to a third party | **High** | `TestFetch_DoesNotFollowRedirects` |
| 3 | Blind comma-stripping overstates reserves 100× | **High** | `TestParseAmount_LocaleCommaIsNotSilentlyStripped` |
| 4 | tee-node returns `v` as 0/1; OpenZeppelin requires 27/28 | **High** | `TestClaim1_TeeNodeReturnsRecoveryIDNotV` |
| 5 | tee-node applies EIP-191; verifying the bare hash silently misrecovers | **High** | `TestClaim2_MustRecoverAgainstPrefixedHash` |
| 6 | Rounding instead of truncating reports full backing for an under-backed token | **Medium** | `TestCompute_TruncatesNeverRounds` |
| 7 | The sign wire is base64, not hex | **Medium** | `TestClaim3_WireIsBase64NotHex` |
| 8 | An empty `sourceHealth` map reads as healthy | **Low** | `config.HealthUnknown` |
| 9 | The TEE machine status enum's numeric values are not published | **High** | `REQUIRED_TEE_STATUS` (parameterised, unresolved) |
| 10 | secp256k1 malleability gives one report two valid signatures | **Medium** | `test_RejectsMalleableSignature` |
| 11 | A `TeeExtensionStateVerifier` is NOT required — the spec is wrong | Info | `register(address(0), …)` in the scaffold |
| 12 | ~~The production entrypoint lands in stub mode silently~~ **RESOLVED** | **High** | `TestStubMode_IsAnnouncedAndDistinguishable` |

---

## 1. `ActionResult.Log` is a public surface, and raw errors leak into it

**Severity: High.** Publishes credentials and reserve figures.

Spec §4.1 establishes that `GET /state` is world-readable. `ActionResult.Log` is
a *second* surface with the same exposure and is not called out: it is part of
the wire contract (`extension-contract.md` §4.6, `"error: <message>"` for
status 0), returned to the TEE proxy, and served from its action-result
endpoint.

Go error strings routinely carry exactly what must not appear there. Both of
these were reproduced, not theorised:

```
1. http    -> Get "https://api.htdigitalassets.com/...?apikey=SUPER_SECRET_KEY": dial tcp: ...
2. decimal -> parsing reserves "25,452,544.52": not a decimal
```

The first is `*url.Error`, which embeds the full request URL including any
query-string credential. The second is the sharper one: it embeds **the reserve
figure**, which §4.1 excludes from every public surface. And because Hex's
public endpoint really does return comma-separated values, a naive
`fmt.Errorf("parsing %q: %w", raw, err)` would publish that number on a
predictable, frequently-taken path — not a rare edge case.

**Resolution.** `internal/failure` owns the complete public error vocabulary.
`Failure` has unexported fields and no exported constructor, so a `Failure` can
only be one of the package-level values declared in that one file. Putting a
runtime value into a public error string is a compile error, not a review
finding. `Extension.fail()` takes a `Failure`, never an `error`.

Tests reject any digit, quote, URL, hostname, or credential-word in the
vocabulary, so a careless addition fails CI.

**Consequence for logging.** The detail still matters for debugging, but §4.1
leaves *where Confidential Space logs land and who can read them* unresolved.
Until that is answered, internal logs carry the error class, the source
identifier, and HTTP status only — never the upstream error, the response body,
or any parsed figure. This matches the spec's own instruction to "log the
derived price and error classes only".

---

## 2. A redirect can hand the Hex API key to a third party

**Severity: High.** Discloses the credential that gates the private endpoint.

Go's `http.Client` strips `Authorization` on a cross-host redirect. It does
**not** strip custom headers. WP1 records that the private endpoint's auth
scheme is unconfirmed — it returns 404 rather than 401 without credentials, so
bearer vs `X-API-Key` cannot be distinguished until a key is issued. If it turns
out to be `X-API-Key`, then any upstream able to return a `302` — a compromised
Hex, a hijacked DNS entry, a misconfiguration — receives the key.

**Resolution.** `backing.New()` sets `CheckRedirect` to
`http.ErrUseLastResponse`, disabling redirect-following outright. An API client
has no legitimate reason to follow one. `TestFetch_DoesNotFollowRedirects`
stands up a sink server behind a redirect and asserts the key never arrives.

Related hardening in the same constructor, each with a test:

- **https is required.** Plaintext would expose the key on the wire and let
  anyone on path dictate the reserve figure, and therefore the published price.
- **Source names may not look like URLs.** Names reach `GET /state`, which is
  public; a URL-shaped name would disclose the endpoint and any embedded key.
- **Response bodies are size-capped** at 64 KiB via `io.LimitReader`.

---

## 3. Blind comma-stripping overstates reserves one hundredfold

**Severity: High.** Would mask the depeg the oracle exists to detect.

Spec §WP1 flags the separator difference and warns against blind stripping. The
magnitude is worth stating explicitly, because it determines how the parser must
be written rather than merely how carefully.

Measured 31 Jul 2026, the two endpoints disagree on format:

```
public:  {"value":"25,452,544.52"}   commas, no currency field
private: {"value":"205304.47"}       no commas, currency: "USD"
```

The obvious implementation — delete every comma, then parse — is wrong.
Comma-as-decimal-separator is a real convention: much of Europe writes `1,50`
for one and a half. Stripping turns that into `150`. A hundredfold
overstatement of reserves publishes $1.00 straight through a genuine depeg.

**Resolution.** `internal/decimal` validates grouping structure rather than
deleting characters: a comma is accepted only where a thousands separator can
legally appear (leading group of 1–3 digits, every subsequent group exactly 3).
`1,50` is rejected. Fail closed; never guess what the operator meant.

Also rejected, because `big.Rat.SetString` accepts them and none is a valid
amount: `1/2` (fraction syntax), `1e5` (exponent), signs, whitespace, and
anything over 64 characters.

Amounts parse to `*big.Rat` — exact. Deliberately **not** `shopspring/decimal`:
for an extension whose value proposition is a reproducible, auditable code hash,
a third-party dependency carrying its own rounding semantics is one more thing
an auditor must read and one more thing that can shift under a version bump.
`big.Rat` is covered by the Go compatibility promise. 5.9M fuzz executions, no
failures.

---

## 4. tee-node returns `v` as 0/1; OpenZeppelin requires 27/28

**Severity: High.** Every on-chain verification fails, and the failure misleads.

Verified against `tee-node v0.0.21-0.20260619120252-31fc839ae6d2`, the version
pinned in `go.mod` — not against the spec's summary:

```go
// internal/extension/server/server.go — signWithTeeHandler
msgHash := crypto.Keccak256(signRequest.Message)
signature, err := s.node.Sign(msgHash)

// pkg/utils/crypto.go — Sign
sig, err := crypto.Sign(accounts.TextHash(msgHash), privKey)
```

`crypto.Sign` returns a 65-byte signature whose final byte is a **recovery id of
0 or 1**, per go-ethereum convention. `ecrecover` and OpenZeppelin's
`ECDSA.recover` both require **27 or 28**, and OZ rejects anything else with
`ECDSAInvalidSignature`.

Exactly one side must add 27. If both do, or neither does, every signature
fails.

**Confirmed empirically, not by reading.** `TestClaim1_TeeNodeReturnsRecoveryIDNotV`
reproduces tee-node's exact call chain over 200 messages and asserts the final
byte is always 0 or 1, and that **both** values occur — a single sample could be
0 or 1 by luck and would prove nothing.

**Decision: normalise on the extension side.** `teenode.NormalizeV` adds 27
before the signature leaves the enclave.

Rationale: it keeps `UsdxPriceStore` using stock, unmodified OpenZeppelin —
the version an auditor already knows how to read — rather than a local variant
with a `+27` an auditor must reason about. The cost is that the published
signature is not byte-identical to what `POST /sign` returned, which is recorded
in the payload documentation. `NormalizeV` is idempotent (27/28 passes through
unchanged), so double-application is safe, and it does not mutate its input.

**Status against the current extension:** consistent. The extension does not
call `/sign` yet — `processGetPrice` still returns the stubbed $1.00 — so
nothing was wrong and nothing needed correcting. The normalisation is in place
*before* the signing path is wired, which is the point: this trap is cheap to
prevent now and expensive to diagnose later, because it surfaces on-chain as a
rejected signature rather than as an encoding bug.

---

## 5. tee-node applies EIP-191; verifying the bare hash misrecovers silently

**Severity: High.** Fails in a way that sends an investigator to the wrong place.

From the same source chain: `accounts.TextHash(msgHash)` prepends
`"\x19Ethereum Signed Message:\n32"` before hashing. The digest actually signed
is therefore:

```
keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message))
```

So `UsdxPriceStore` must recover against
`toEthSignedMessageHash(keccak256(body))`, **not** against `keccak256(body)`.

The danger is the failure mode, not the mistake. Recovering against the bare
hash **does not revert** — it returns a well-formed but different address. On
chain that presents as "this TEE is not registered", sending whoever
investigates to `TeeMachineRegistry` and the registration flow rather than to
the hashing. `TestClaim2_MustRecoverAgainstPrefixedHash` asserts both that the
prefixed recovery yields the right address and that the unprefixed one does not.

`TestSignedReportRecoversAfterNormalisation` covers findings 4 and 5 together:
sign the way tee-node does, normalise, then recover the way the contract will.

---

## 6. Truncation is load-bearing, not a style choice

**Severity: Medium.** Reports full backing for a token that is not fully backed.

Spec §3.5 specifies rounding down. The reason deserves recording: at 8 decimals,
a backing ratio of `0.999999995` **rounds** to exactly `1.00000000`. That
publishes "fully backed" for a token that is not, which is precisely the
condition the feed exists to surface.

Truncation makes every representable error conservative — the published price is
understated by at most one unit in the last place, never overstated.
`TestCompute_TruncatesNeverRounds` uses that exact value.

The par cap is the mirror image: USDX redeems at $1.00, so over-collateralisation
does not make it worth more, and publishing 1.02 would be wrong in a way that
costs someone money. Both properties are fuzzed as invariants over arbitrary
inputs (5.2M executions): the result is never negative and never exceeds par.

`internal/price` is kept **pure** — no I/O, no clock, no configuration, no
policy — which is what makes that fuzzing meaningful and lets an auditor verify
the formula by reading alone. Plausibility bounds and the deviation guard live
in the caller and on-chain respectively; encoding either here would hide a
governance parameter inside arithmetic.

---

## 7. The sign wire is base64, not hex

**Severity: Medium.** Silent misencoding.

`tee-node`'s `SignRequest.Message`, `SignResponse.Signature`,
`DecryptRequest.EncryptedMessage` and `DecryptResponse.DecryptedMessage` are all
Go `[]byte`, and `encoding/json` renders `[]byte` as **base64**. Every other
byte string in FCC is hex, so this is a genuine asymmetry rather than a
convention one can infer.

`TestClaim3_WireIsBase64NotHex` asserts the encoding, and
`TestWireFormatMatchesTeeNode` marshals our locally declared structs alongside
tee-node's and requires byte-identical JSON — so a change on their side breaks a
test rather than the feed.

---

## 8. An empty `sourceHealth` map reads as healthy

**Severity: Low.** Hides a source that silently stopped being polled.

`GET /state` reports `sourceHealth` as a map of source identifier to status.
With no entries it serialises as `{}`, which is indistinguishable from "all
sources fine" to anyone eyeballing it — and once sources exist, an *absent* key
would read the same way.

**Resolution.** `config.HealthUnknown` exists as an explicit value, and every
known source is to be seeded with it at startup so absence is never mistaken for
health. Names are identifiers (`hex_bonds`, `rpc_flare_1`), never URLs, for the
disclosure reason in finding 2.

**Still open:** granularity. The natural key is per-chain, but the design reads
three RPC providers per chain with 2-of-3 agreement, so `rpc_flare: "ok"` hides
one-of-three being down — exactly the state worth seeing *before* the second one
goes and quorum fails. Per-provider keys disclose how many providers are
configured, though not which. WP7.4 wants per-provider health for monitoring.
Not yet decided.

---

## 9. The TEE machine status enum's values are not published

**Severity: High.** Blocks correct mainnet deployment. **Unresolved — needs an answer.**

`UsdxPriceStore` must reject a signer that is registered to this extension but
has since been revoked. `MachineManager` exposes
`getTeeMachineStatus(address) -> uint8`, but the ABI types the return as a bare
`uint8`: the enum's member names and numeric values do not survive into the
published Go bindings, and nothing in this repo or `go-flare-common` records
them.

Guessing is worse than either outcome it produces. Guess too low and revoked
machines keep publishing prices indefinitely. Guess too high and every
submission reverts, which is a total feed outage that would only be discovered
after deployment.

**Resolution (partial).** The expected value is a constructor parameter,
`REQUIRED_TEE_STATUS`, not a hard-coded constant. The contract is correct for
whatever the right value turns out to be, tests cover both the matching and
non-matching cases, and the unknown is visible at the deployment boundary rather
than buried in a comparison.

**Still required before mainnet:** confirm the enum against
`flare-smart-contracts-v2` and set the parameter accordingly. Also confirm
whether `getExtensionId` reverts or returns 0 for an unknown machine — the store
handles both (`try/catch` plus a value check), but the behaviour should be
recorded rather than merely defended against.

---

## 10. secp256k1 malleability gives one report two valid signatures

**Severity: Medium.** Breaks "one report, one signature" for anything keying off
signature bytes.

For any valid `(r, s, v)` there is an equally valid `(r, n − s, v ^ 1)` that
recovers the same signer. An observer who sees a submitted report can therefore
mint a second, byte-distinct signature for it without any key material.

Strict `observedAt` monotonicity already makes a replay harmless *in this
contract* — the second submission is rejected as not-newer regardless. But the
property is worth enforcing rather than relying on: a keeper deduplicating by
signature hash, or any future consumer that treats the signature as an
identifier, would be wrong in a way that is hard to see.

**Resolution.** `_recover` rejects the high-`s` form outright, the same rule
OpenZeppelin applies. One accepted representation means one code path to audit.

---

## 11. A `TeeExtensionStateVerifier` is not required

**Severity: informational.** Removes a planned work item.

Spec §WP4.3 lists "`TeeExtensionStateVerifier` — required by `register()`;
scaffold has none" as a task. Observed behaviour contradicts it:
`tools/cmd/register-extension/main.go` passes `common.Address{}` as the state
verifier and registration succeeds — the scaffold's whole end-to-end flow runs
that way.

A state verifier exists to validate the extension's attested `GET /state`
on-chain. This design does not need one: the price reaches the chain through the
signed-report path into `UsdxPriceStore`, and `/state` carries only operational
health that nothing on-chain consumes.

**No contract was written.** If a future design wants attested state on-chain,
this decision should be revisited — but building one now would be unused code in
an audited codebase.

---

## 12. The production entrypoint still serves a hard-coded price

**Severity: High.** **Resolved 2026-08-03.**

Found by auditing the call graph rather than by a failing test, which is why it
is worth recording: every check was green while this was true.

    cmd/docker/main.go
      -> extserver.StartExtension(port, signPort)
      -> extension.New(extensionPort, signPort)
      -> NewWithObserver(..., nil)          // observer == nil
      -> stub mode

Nothing anywhere constructs an `oracle.Runner`. The observation path is complete
and tested, but no code assembles it, because that needs configuration which
does not exist yet: RPC endpoints per chain, Hex endpoint URLs, injected
credentials, the store address, the feed id. There is no config loader.

The reason this is High rather than a to-do: **a hard-coded par price is the one
failure mode that raises no alarm.** No error, no failure counter, no stale
timestamp, no unhealthy source. A feed frozen at exactly $1.00 is
indistinguishable from a healthy, fully-backed one — precisely the state the
monitoring in WP7 is designed to detect, and precisely the state it cannot see.

**Resolution.** Three changes, in order of how much they matter:

1. **`extension.New()` was deleted.** That constructor was the actual defect: it
   read as the ordinary way to build an extension while silently producing a
   stub. Reaching stub mode now requires writing `nil` at the call site.
2. **`server.StartExtension` loads the embedded config and refuses to start
   without it.** There is no fallback to stub on error — falling back would
   reintroduce the exact failure, just later.
3. **The two entrypoints are separate binaries.** `./cmd` is the stub, used only
   by `test-conformance.sh` which has no credentials by design; `./cmd/docker` is
   production and is what `go/Dockerfile` builds. Stub mode also still logs a
   six-line banner at `ERROR` level, and `Extension.StubMode()` is exposed for
   health checks.

**Still true:** the two modes emit different wire formats, so conformance
fixtures pin the stub's JSON rather than the production ABI payload. Regenerating
them against the real path needs a running signer, and `observedAt` becomes
wall-clock — the byte-exact `data` assertions must become structural at that
point. `TestStubMode_PayloadDiffersFromObserverPayload` keeps the divergence
visible until then.

**Related: the two modes emit different wire formats.** Stub mode returns JSON
(`GetPriceResponse`); observer mode returns `abi.encode(body, signature)`. The
conformance fixtures were generated in stub mode, so **they pin the stub's format
and not the production payload** — conformance currently gives less assurance
than its green result suggests.
`TestStubMode_PayloadDiffersFromObserverPayload` asserts the divergence rather
than letting it be discovered later. Resolving finding 12 means regenerating the
fixtures against the real path, at which point `observedAt` becomes wall-clock
and the byte-exact `data` assertions must become structural.

**Remaining:** regenerate conformance fixtures against the real payload → E2E on
Coston2.

---

## 13. The deployment ordering has no circularity

**Severity: informational.** Corrects an error in an earlier draft of this
document.

The signed report binds the store address, so the store must be pinned in the
extension's embedded config — which appeared to require the store before the
image, while the store's constructor needs `EXTENSION_ID`, which appeared to
require a registered extension and therefore a running TEE. That reasoning was
wrong.

**Registering an extension does not require a running TEE machine.** Only
registering a *machine* does. The scaffold's lifecycle already separates them:

    pre-build.sh        deploy InstructionSender, register extension -> EXTENSION_ID
    extension-setup.sh  deploy UsdxPriceStore + UsdxCustomFeed -> STORE
    start-services.sh   build image with STORE pinned -> code hash
    post-build.sh       register the TEE machine

`extension-setup.sh` is a hook the scaffold already provides, and it runs exactly
between the two. One build pass, no CREATE2, no provisional image. The earlier
two-phase-build proposal solved a problem that does not exist.

`scripts/extension-setup.sh` now deploys both contracts and writes the store
address into the embedded config with `jq`. It skips deployment if a real address
is already pinned, since redeploying resets the stored price and the `observedAt`
high-water mark — `FORCE_REDEPLOY_STORE=1` overrides.

---

## 14. Configuration arrives at runtime; only the un-substitutable parts stay baked

**Severity: informational.** Records a design change and the reasoning, since it
reverses an earlier implementation.

An earlier build embedded RPC endpoints in the image and read credentials from
`SECRETS_CIPHERTEXT` at startup. Both now arrive after boot:

| | Was | Now |
| --- | --- | --- |
| Endpoints | Embedded JSON, code-hash pinned | On-chain `USDX_CONFIG`/`SET_ENDPOINTS` |
| Credentials | `SECRETS_CIPHERTEXT` env at startup | Direct `USDX_CONFIG`/`SET_SECRET`, M-of-N signed envelope |
| Lifetime | Immutable after startup | Memory-only, lost on restart |
| Rotation | Rebuild or restart | Instant, no gas, no restart |

**Why endpoints go on-chain.** Publishing which RPC providers are read is the
point — it is what makes the data sources verifiable rather than merely
asserted.

**Why credentials go by direct instruction.** No gas, no permanent public record
of a ciphertext, instant rotation, and no secret-handling path in Solidity.

**What still had to stay baked**, because moving it would have made the
attestation meaningless:

- **The host allowlist.** Without it, on-chain endpoint configuration is itself a
  substitution channel: whoever can set endpoints can point a chain at a server
  they control and dictate the price. The allowlist means configured endpoints
  can only ever be hosts that were attested.
- **The admin signer set** (`internal/provision.Admins`), the root of trust for
  provisioning.
- **The target binding** — chain id, store, feed id, decimals — which the signed
  report commits to.
- **The observation window**, since widening it weakens the TOCTOU bound.

### Properties worth knowing

**Each command is valid over exactly one channel, enforced not assumed.**
`SET_ENDPOINTS` is instruction-only: its authorisation is the owner-only check in
the on-chain sender, which a direct action would bypass entirely. `SET_SECRET` is
direct-only: a credential ciphertext has no business in permanent public storage.
The handler checks `action.Data.Type` — which the scaffold's dispatch ignored
completely.

**One rejection reason for everything.** Bad signature, too few distinct signers,
unknown signer, stale `issuedAt`, replayed version, wrong `teeId`, wrong chain,
wrong domain, undecryptable ciphertext — all return
`failure.ProvisionRejected`. Distinguishing them would turn provisioning into an
oracle for probing the admin set, the freshness window, and which machine is
which. `TestVerify_AllRejectionsAreIndistinguishable` holds that line.

**`version` and `issuedAt` are complementary, not redundant.** `version` is
in-memory and blocks rolling a secret back while the process lives. `issuedAt`
(15 minutes) survives restarts, where the version counter resets to zero and
would otherwise let a captured envelope be replayed indefinitely.

**Distinct signers.** One compromised key signing M times must not satisfy an
M-of-N threshold; recovered addresses are deduplicated.
`TestVerify_RejectsDuplicateSignerReachingThreshold`.

**The allowlist matches the parsed hostname, never a substring.**
`https://evil.com/?x=flare-api.flare.network` must not pass, and exact matching
avoids the `evil-flare-api.flare.network` class of near-miss.

**A rejected update leaves the working configuration in place.** A bad
`SET_ENDPOINTS` must not take a live feed down.

### Design bug caught while building

`secretRef` was initially one field serving two purposes: substitution into a
`{secret}` URL placeholder, and the value of an auth header. Those are different
cases — the Hex private endpoint authenticates by header with no placeholder,
while a keyed RPC URL has a placeholder and no header — and one field could not
express both without validation rejecting one of the two real configurations.
Split into `URLSecretRef` and `AuthSecretRef`.

### Open

The admin set is **still placeholder addresses**
(`0x1111…`, `0x2222…`, `0x3333…`) with `Threshold = 2`.
`TestAdminSetIsNotPlaceholder` skips with a warning today and must become a hard
failure once real keys are baked. Nothing can be provisioned against a live
deployment until they are.

---

## Design decisions recorded while implementing

These are not defects, but they were judgement calls an auditor will want the
reasoning for.

**No exclusion list.** Supply is the plain sum of `totalSupply()` over the
configured contracts, with no `balanceOf` calls and no exclusions. The contract
list *is* the policy. It is pinned in configuration covered by the code hash,
and it must match the basis on which Hex reports reserves — currently the three
live contracts, excluding the pre-migration Ethereum contract (`0x7A48…D56c`,
150,000 USDX). Including it would compare two different populations and publish
a spurious ~0.6% depeg on day one. Adding it later is a one-line config change.

**Minimum head, not maximum or median.** Each provider resolves its own finality
reference to a block *number*; the minimum is taken, less a per-chain margin,
and every provider is queried at that explicit number. The minimum is the only
choice guaranteed to name a block all providers can serve — a higher one would
make the laggard fail every round, silently shrinking the effective provider
count to whichever endpoints happen to be ahead.

**Identical results, not median.** A read at a fixed block is deterministic, so
two honest providers cannot disagree. Disagreement means one is wrong or lying,
and there is no safe way to average that. Quorum failure aborts the whole
observation.

**Quorum 1 is expressible, deliberately.** Songbird has exactly one working
public RPC (spec §2.8), so a three-provider rule cannot be met there for free.
`Quorum: 1` is a documented acceptance of single-provider risk for that chain's
~4.5% of supply, not an oversight.

**`GET_PRICE` takes no payload.** The report binds `chainId`, `store` and
`feedId`, all pinned in extension config rather than supplied by the caller. If
they were caller-supplied, anyone could obtain a TEE signature over a report
bound to a store they control. A non-empty payload is rejected rather than
ignored, so nobody builds on a field the extension drops — and the rejection
does not echo the payload or its length back out, since `Log` is public.

**Unknown JSON fields are rejected.** A shape change from Hex is a signal to
stop and look, not to keep publishing prices derived from a response we no
longer fully understand.

**The conservative double-read brackets reserves between two supply reads** and
takes `max(S₁, S₂)`. A larger denominator is a lower price, so any timing skew
biases the result downward. Spec §6.1 proposed this; the implementation confirms
the cost is one extra RPC round. `TestObserve_DoubleReadBiasesPriceDown` uses the
real 1,000,000 USDX burn from §6.1 and asserts the price comes out below par
where a naive single read would have printed exactly $1.00.

**`observedAt` is wall-clock completion time, not the oldest input timestamp.**
The store requires strict monotonicity, and the public reserves endpoint ticks
only every ~10 minutes — two observations minutes apart could share an input
timestamp, and the second would be rejected as a replay. Data freshness is
guarded earlier and separately: a stale reserve figure fails the observation
outright in package `backing`, so a published price is never built on data older
than `MaxAge`.

**Out-of-band prices are rejected, not clamped.** Clamping would publish a number
the TEE never signed, silently. The signature's entire purpose is that the
published price is the one the attested code computed.

**The feed never reverts, including when paused.** `FtsoV2.getFeedById` forwards
the call, so a revert in `getCurrentFeed()` becomes a simultaneous outage for
every consumer. A pause freezes the value; it does not blind the feed.

---

## Open questions this work did not resolve

- **The §3.0 blocker stands.** `current_reserves_amount` has been observed equal
  to total on-chain supply to the cent, and tracking it through a 1,000,000 USDX
  burn. `internal/backing` is an interface precisely so a source swap does not
  invalidate anything else. `TestCompute` encodes the observed figures as a case
  asserting the feed prints exactly par — if Hex's answer changes the formula,
  that test fails and says why.
- **Deviation guard (§6.2)** — still needs a named risk owner. To be implemented
  on-chain, where it can change without a new code hash.
- **Where Confidential Space logs land**, and who can read them (§4.1). Gates how
  much detail internal logging may carry.
- **Feed name and category byte** — to be agreed with Flare before registration.
- **`sourceHealth` granularity** — per-chain vs per-provider, finding 8.
