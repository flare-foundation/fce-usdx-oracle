# USDX Oracle — Implementation Findings

**Status:** living document · **Last updated:** 2026-08-04

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
| 12 | ~~The production entrypoint lands in stub mode silently~~ **RESOLVED** | **High** | `TestStubMode_CannotBeEnteredAccidentally` |
| 15 | The contract owner key can set any RPC host and any threshold — accepted | **High** | `TestAnyHostIsAccepted` |
| 16 | On-chain config recorded what was *sent*, not what a machine *runs* | **High** | `test_RejectsReportFromStaleConfig` |
| 17 | Full audit of every logging and error surface found no sensitive disclosure | Info | §17 audit + the finding-1 tests |

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
   stub.
2. **`server.StartExtension` loads the embedded config and refuses to start
   without it.** There is no fallback to stub on error — falling back would
   reintroduce the exact failure, just later.
3. **A nil observer is now unconstructible.** An interim fix logged a six-line
   banner at `ERROR` level, but inside a TEE the logs cannot be read from
   outside, so a banner is not a defence. The production constructors
   (`NewWithObserver`, `NewWithProvisioner`) now **panic** on a nil observer,
   and the only stub path is `extension.NewStub`, called at exactly one site:
   `./cmd`, the binary used by `test-conformance.sh`, which has no credentials
   by design. `./cmd/docker` is production and is what `go/Dockerfile` builds.
   `Extension.StubMode()` remains exposed for health checks.

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

The original concern: while the signed report bound the store address, the
store had to be pinned in the extension's embedded config — which appeared to
require the store before the image, while the store's constructor needs
`EXTENSION_ID`, which appeared to require a registered extension and therefore
a running TEE. That reasoning was wrong even then, and the binding has since
been removed (pre-launch), so the ordering constraint no longer exists at all:
the store address is not part of the extension image.

**Registering an extension does not require a running TEE machine.** Only
registering a *machine* does. The scaffold's lifecycle already separates them:

    pre-build.sh        deploy InstructionSender, register extension -> EXTENSION_ID
    extension-setup.sh  deploy UsdxPriceStore + UsdxCustomFeed -> record addresses
    start-services.sh   build image -> code hash
    post-build.sh       register the TEE machine

`extension-setup.sh` is a hook the scaffold already provides, and it runs exactly
between the two. One build pass, no CREATE2, no provisional image. The earlier
two-phase-build proposal solved a problem that does not exist.

`scripts/extension-setup.sh` deploys both contracts; `deploy-store` records the
addresses in the deployed-addresses file. It skips deployment if one is already
recorded there, since redeploying resets the stored price and the `observedAt`
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

- **The host allowlist** — *superseded, see §15*. Baked at this point because,
  without it, on-chain endpoint configuration is itself a substitution channel:
  whoever can set endpoints can point a chain at a server they control and
  dictate the price. It was later removed so providers can be rotated without a
  rebuild, and the resulting owner-key trust is accepted explicitly in §15.
- **The admin signer set** (`internal/provision.Admins`), the root of trust for
  provisioning.
- **The observation window**, since widening it weakens the TOCTOU bound.

(The signed report's target binding — chain id, store, feed id — was removed
pre-launch; acceptance is anchored by the store's registered-machine check.
See §16.)

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

## 15. The owner key is price-controlling, by decision

**Severity: High. Accepted deliberately — this is a recorded trade, not a defect.**

`SET_ENDPOINTS` carries RPC provider URLs and a per-chain agreement threshold,
both unconstrained. An earlier build bounded URLs with a baked host allowlist
and pinned the threshold; both were removed so that adding a provider (Alchemy,
say) or moving from 2-of-3 to 2-of-4 needs no rebuild and no re-attestation.

**The consequence.** `setEndpoints` is `onlyOwner`, so whoever holds that key
can send `{"threshold": 1, "providers": [{"url": "https://their-server/"}]}`,
have that server return any `totalSupply()`, and the attested code will
faithfully compute and sign the resulting price. A higher threshold does not
help, because the same party chooses every provider in the list.

So the published price is only as trustworthy as the **contract owner key**, not
as the code hash alone. The attestation still proves which code ran and that the
credentials stayed in the enclave; it no longer proves the inputs came from
anywhere in particular. Anyone relying on this feed has to trust the owner as
well as the TEE.

**What this rules out.** The original claim in spec §2 — that consumers can
verify *what* computed the price rather than trusting an operator — is now only
half true. Worth restating in consumer documentation rather than leaving the
spec's stronger wording in place.

**What is still enforced**, and is unrelated to which host is named:

- https only, so a credential is never put on the wire in plaintext and nobody
  on path can dictate a supply figure
- `{secret}` may not appear in a URL's scheme or host, so the credential holder
  cannot change the destination even though the URL author can
- a credential containing `/ ? #` or whitespace is rejected, so substitution
  cannot restructure a URL
- threshold must be between 1 and the provider count, so a chain cannot be
  configured to fail on every observation
- every pinned chain must be present, since a missing one undercounts supply and
  therefore *overstates* the price
- token addresses, finality tag and margin, decimals, the Hex endpoints and the
  observation bounds remain pinned and are not expressible in a payload

**Mitigations available if the trade is later reconsidered:** put the owner
behind a multisig or timelock; or pin a per-chain *minimum* threshold so the
runtime value can only be raised; or reinstate the host allowlist.
`TestAnyHostIsAccepted` exists so that reintroducing a constraint means changing
a named test rather than silently altering behaviour.

---

## 16. The report's target binding was removed pre-launch

**Severity: informational. A deliberate simplification, recorded so the absence
is a documented decision rather than an oversight.**

The signed report originally carried `chainId`, `store` and `feedId`, and the
store verified each against itself. All three were removed before launch. The
payload is now `(domain, price, decimals, observedAt, instructionId)`.

**Why they were weaker than they looked.** The store's acceptance gate is the
registered-machine check: a signature is only ever accepted from a TEE machine
currently registered to the store's extension. That check proves the price was
computed by attested code from the configured sources regardless of what the
payload binds. The binding fields never protected the price itself — only
*where a correctly-signed price could be delivered*:

- `feedId` was redundant outright: one store serves one feed, `FEED_ID` is
  immutable, and the store binding already determined the destination.
- `store` blocked replay into other deployments under the same extension id —
  including deployments governance did not intend — but such a replay still
  delivers a genuine TEE price, never a forged one.
- `chainId` blocked cross-chain replay, which additionally requires a machine
  key to be registered in two networks' registries — a platform invariant the
  deployment model (one chain per machine) already makes unlikely.

**What is accepted in exchange.** Two operational invariants, carried by
governance rather than enforced by the contract: one intended store deployment
per extension (a second deployment under the same extension id accepts the
same genuine reports until paused), and machine keys staying on one network.
Nobody can publish a wrong price either way.

**What changed with it.** The store address left the extension's embedded
config (schema v5 drops `target`; the chain id stays, because provisioning
envelopes still bind to it), `extension-setup.sh` no longer writes a deployed
address into the config (`deploy-store` records it in the deployed-addresses
file instead), and the image no longer needs the store deployed before it is
built. `FEED_ID` remains on the store as the feed's FTSO identity.

---

## 17. Full audit of logging and error surfaces (2026-08-04)

**Severity: informational. No sensitive disclosure found. Records what was
checked so the next reviewer starts from a known baseline rather than
re-auditing from scratch.**

Finding 1 built the mechanism. This audit walked every site that emits
anything — every `logger` call, every error return, every HTTP response body,
every contract event — to confirm the mechanism is complete and nothing beside
it leaks.

**Scope and verdict, surface by surface.**

- *Internal logging.* The runtime extension imports `logger` in exactly three
  files: `extension.go`, `config_handler.go`, `utils.go`. The packages that
  actually touch secrets and reserve figures — `backing`, `supply`, `price`,
  `decimal`, `report`, `provision`, `runtimecfg`, `teenode` — import no logger
  at all, so the sensitive values have no log path to reach. What the three
  files log is: action IDs, statuses, the vocabulary `Log`, port numbers, the
  stub-mode banner, `SET_ENDPOINTS` validation detail (chain names, thresholds
  and secret *ref names* — all public on-chain or names rather than values),
  and the fixed string `SET_SECRET rejected` with deliberately no cause.
- *ActionResult.Log.* Every path reaches it through `Extension.fail()`, whose
  parameter type is `failure.Failure`, not `error` — a raw error cannot be
  passed in even by accident. `asFailure()` sanitises at the extension and
  oracle boundaries. Pinned by `TestProcessGetPrice_ForeignErrorIsNotPublished`
  (an injected `dial tcp …?apikey=SECRET` error surfaces as `internal error`)
  and `TestVocabularyIsPublishable` (rejects digits, URLs, hostnames, quotes
  and credential-words in the vocabulary).
- *GET /state.* Carries last price, timestamps, counters, the coarse error
  class, readiness (secret ref *names*, never values) and per-source health
  keyed by non-URL identifiers (`backing.New` rejects URL-shaped names).
- *Signing/decrypt round trips.* `teenode.post` returns vocabulary values
  only; the failed-decrypt response body is dropped because it could carry key
  material. `backing.fetchOnce` drops the `*url.Error` that embeds the request
  URL with any query-string credential. `decimal` errors never quote their
  input, and the input is dropped anyway, because the input is the reserve
  figure.
- *Contract events and reverts.* Events carry the price (the intended public
  output), timestamps, addresses, and `keccak256(config)` — the hash, not the
  endpoint config. Revert strings are constants. The endpoint payload itself
  is public by construction (it arrives by on-chain instruction) and carries
  URLs with `{secret}` placeholders and ref names, never credential values.
- *Operator tooling.* `deploy-store`, `set-endpoints.sh` and `usdxprov` print
  addresses, refs, versions and expiry — nothing secret. `usdxprov build`
  prints metadata about the envelope but never the credential, which exists in
  plaintext only inside the target TEE.

**What remains, recorded rather than leaked.**

1. *HTTP-level error bodies are not vocabulary-controlled.* `decoding action:
   %v`, `decoding fixed data: %v`, the 501 unsupported-op messages and
   `sending response: %v` are formatted with the underlying error. They are
   visible only to the direct HTTP peer (tee-node, the proxy, or a direct
   submitter), never on the two public surfaces, and their content is either
   the caller's own input or public type names. Not a disclosure; recorded so
   nobody assumes the closed-vocabulary property extends to raw HTTP bodies.
2. *`usdxprov` takes the credential (`-value`) and the admin key (`-key`) as
   argv flags*, which are visible in `ps` output and shell history on the
   operator's own machine. Operational exposure rather than a logging one; a
   stdin/env mode would close it.

One inaccuracy was found and corrected during the audit: the `SET_ENDPOINTS`
rejection comment in `config_handler.go` claimed the error detail names hosts;
the `runtimecfg` errors actually carry chain names, thresholds and secret ref
names. No host ever appears, which is the safer direction.

---

## 16. On-chain configuration was advisory, not binding

**Severity: High. Resolved.**

`setEndpoints` published a configuration and emitted `EndpointsSet(teeId,
keccak256(config))`. That recorded what was **sent**. Nothing recorded, or
required, what a machine was actually **running**.

Three ways they diverge, none of which raised anything:

1. **The extension rejected the payload.** `ApplyEndpoints` validates before
   storing, so a malformed config leaves the *previous* one in force — correct
   behaviour, but the chain still shows the new one.
2. **The instruction was never delivered.** FCC is fire-and-forget: no delivery
   guarantee.
3. **Several machines.** `setEndpoints` names one `teeId`, so N machines means N
   calls. A machine that never received its call keeps running stale
   configuration — and `UsdxPriceStore` accepts a signed price from *any*
   registered machine for the extension, so its prices are equally valid.

The third is the sharpest: every machine reports `ready: true`, and nothing
anywhere reveals that they disagree.

**Resolution.** The signed price report now carries `endpointsHash`, and the
store enforces it:

```solidity
// UsdxOracleInstructionSender.setEndpoints — recorded BEFORE dispatch, so a
// machine that never receives the instruction cannot submit either.
expectedConfigHash[_teeId] = keccak256(_config);

// UsdxPriceStore.submitPrice
bytes32 expected = CONFIG_SOURCE.expectedConfigHash(teeId);
if (expected == bytes32(0)) revert NoConfigPublished();
if (endpointsHash != expected) revert StaleConfig();
```

The TEE already signs the report, so it attests its active configuration in the
same signature. A machine running anything other than what was published cannot
submit at all — no monitoring, no human, no off-chain comparison.

This makes a claim readable from chain alone that was previously unsupported:
*this price was computed by attested code, running this exact published
configuration.*

**Implementation detail that matters.** The hash is taken over the **raw payload
bytes as received**, never a re-serialisation of the decoded struct.
`json.Marshal` will not reproduce byte-identical input — key order, whitespace —
so a re-encode could never match `keccak256(_config)` from the transaction. Same
lesson as "carry the body, never re-encode it".

**What it does NOT do.** It does not constrain the owner, who sets both the
configuration and the expected hash and can keep them consistent while pointing
at anything. It closes divergence, not finding 15.

**Also exposed** in `GET /state` as `readiness.endpointsHash`, so an operator can
compare against the `EndpointsSet` event without waiting for a price submission.
That field is a convenience; the enforcement is on-chain.

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

**No cosigners on any instruction.** Every instruction this system sends —
`GET_PRICE` and `SET_ENDPOINTS` in `InstructionSender`, the provisioning
envelope dispatched by `usdxprov`, and the `set-endpoints.sh` payload — goes
out with an empty `cosigners` list and `cosignersThreshold` 0. The machine
executes on delivery; no external address has to co-sign first. This is the
default state, recorded so changing it later is a documented decision. If
cosigners are ever added — e.g. requiring FDC data providers to vouch for a
price-update request before it executes — the change lands in
`InstructionSender` and in those two payloads; the extension and the price
store are untouched, since cosigning gates *delivery/execution*, not the signed
report. The conformance fixtures (`testdata/conformance/gen_fixtures.py`) pin
the empty list and would need regenerating.

**`GET_PRICE` takes no payload.** The observation needs nothing from the
caller: everything it reads is configured by `SET_ENDPOINTS`/`SET_SECRET` or
pinned in the image, and where the signed report lands is settled by the
store's registered-machine check, not by the payload (§16). A non-empty payload
is rejected rather than ignored, so nobody builds on a field the extension
drops — and the rejection does not echo the payload or its length back out,
since `Log` is public.

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

- **~~The §3.0 blocker stands~~ — resolved 3 Aug 2026.** `current_reserves_amount`
  is the bank-provider balance of the reserve account; the observed supply-tracking
  is 1:1 treasury operation, not derivation from chain data, and the figure can fall
  below supply on any outflow with no matching burn. `internal/backing` stays an
  interface for source-swap containment. `TestCompute` still encodes the observed
  figures as a par case — correct under 1:1 operations. Written confirmation from
  Hex remains as ordinary diligence.
- **Deviation guard (§6.2)** — still needs a named risk owner. To be implemented
  on-chain, where it can change without a new code hash.
- **Where Confidential Space logs land**, and who can read them (§4.1). Gates how
  much detail internal logging may carry.
- **Feed name and category byte** — to be agreed with Flare before registration.
- **`sourceHealth` granularity** — per-chain vs per-provider, finding 8.
