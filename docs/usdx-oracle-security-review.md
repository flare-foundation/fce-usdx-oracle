# USDX Oracle — Security and Robustness Review

**Status:** review report · **Date:** 2026-09-08 · **Reviewed at:** commit `5a17d7e` (branch `tilen/improvements`)

> A second review, asked from the attacker's side and recording the status of every finding
> below, is [usdx-oracle-security-review-2.md](usdx-oracle-security-review-2.md) (2026-09-15).

A full read-through of the component for exploitable weaknesses and for robustness
(availability, fail-closed behaviour, operability) problems. Every finding below was
verified against the code at the commit named above; where a claim rests on something
outside this repository that was not verified, the finding says so.

This report complements [usdx-oracle-findings.md](usdx-oracle-findings.md), which records
the findings made *during* implementation and the tests that pin them. Nothing here
duplicates those; where a finding sharpens one of them, it says which.

## Contents

- [Summary](#summary)
- [Scope and method](#scope-and-method)
- [Trust model used](#trust-model-used)
- [Findings table](#findings-table)
- [A. Extension process](#a-extension-process)
- [B. Provisioning](#b-provisioning)
- [C. Observation and price](#c-observation-and-price)
- [D. Keeper and publication tools](#d-keeper-and-publication-tools)
- [E. Deployment, images and supply chain](#e-deployment-images-and-supply-chain)
- [F. Dependency scan](#f-dependency-scan)
- [Verified clean](#verified-clean)
- [Not inspected, and assumptions](#not-inspected-and-assumptions)
- [Suggested order of fixes](#suggested-order-of-fixes)

## Summary

The enclave-side core holds up well. The price arithmetic is exact and fails closed, the
signing domains are separated and chain-bound, the envelope verification handles
malleability and thresholds correctly, no secret reaches a log or a public surface, and
the action router keeps the privileged instruction channel and the direct channel apart.
No path was found by which an attacker can make the machine sign a wrong price.

The weaknesses are at the edges: how the process is exposed on the network, what the
operator tooling trusts, and how the system behaves when one of its many inputs
misbehaves. Four findings are rated high:

1. **The proxy's unauthenticated internal API is published on all interfaces by default**
   (`deploy/docker-compose.yaml`), on every chain. Anyone reaching the host can post
   forged action results.
2. **The extension's `/action` port binds all interfaces and trusts arrival on the
   instruction channel as authorisation**; the image also `EXPOSE`s it. Anything that can
   reach the port can install its own admin set and endpoints.
3. **The keeper never filters an individual update by signer**, so one forged or stale
   update from any proxy rejects the whole batch and stops the feed.
4. **The `oracle-cycle.sh` wrapper always passes `-p`**, which the keeper treats as an
   override of every machine's registered proxy. With two or more machines the fleet
   collapses onto one proxy and the batch never reaches the signature threshold.

Fourteen findings are rated medium. The ones an operator would hit first: a production
deploy following the documentation cannot pass `CHAIN_ID` through the launch policy and
panics at start (fail-closed, but undeployable as written); the `hex_cash_url` credential
cannot be produced by any tool in the repository, so the cash source can never be
enabled; rotating that URL deadlocks the store until a restart; and the upload script
maps machine-chosen secret names onto arbitrary environment variables, including the
one holding the operator's admin private key.

The dependency scan found no third-party module vulnerability reachable from this code.
It did show that the production builder image pins Go 1.25.1, a year of standard-library
fixes behind the current release.

## Scope and method

In scope: the extension module (`cmd/`, `internal/`, `pkg/`), the tools module
(`tools/`), the deployment scripts and compose files (`deploy/`), the Dockerfiles, and the
Foundry deploy script. The pinned tee-node and tee-proxy checkouts were read only where the
extension's behaviour depends on theirs. Upstream contracts were not reviewed.

Method: four independent read-throughs by area (extension HTTP surface and routing;
provisioning and envelopes; observation and price; tools, keeper and deployment), each
producing candidate findings with file and line references, followed by a verification
pass in which every candidate was re-read in the source before inclusion. Mechanical
sweeps covered binds, timeouts, body limits, secret defaults, `set -x`/`eval`/`curl -k`,
strict mode in scripts, Dockerfile pins, HTTP clients without timeouts, private key
sources, and `panic` sites. `govulncheck` was run on both modules.

Severity: **High** means exploitable or feed-stopping under the stated trust model with
no unusual precondition; **Medium** means it needs a second failure or a misconfiguration,
or it is a robustness problem that stops the feed under realistic conditions; **Low** is
defence in depth or a degraded mode; **Info** is a note for the maintainers. Type **S** is
a security finding, **R** a robustness finding.

## Trust model used

Trusted: the tee-node co-process (same process, same enclave), the on-chain governance
that publishes SET_ENDPOINTS and SET_ADMINS, Hex as the source of reserve figures, the
admin quorum for credentials, and up to `threshold - 1` colluding RPC providers per group.

Attacker positions considered: a network peer of the machine or of an operator host; a
holder of the proxy's direct-channel API key; a compromised proxy; a compromised or
misconfigured RPC provider; a compromised publication tool run; another local user on an
operator host; and a supply-chain position over the images and modules.

## Findings table

| # | Sev | Type | Area | Finding |
| --- | --- | --- | --- | --- |
| SR-01 | High | S | E | Proxy internal API (`POST /result`, `/queue`) published on `0.0.0.0` by default |
| SR-02 | High | S | A | Extension `/action` binds all interfaces; instruction channel trusted by arrival; port `EXPOSE`d |
| SR-03 | High | R | D | Keeper does not filter updates by signer; one bad update rejects the batch |
| SR-04 | High | R | D | `oracle-cycle.sh` always passes `-p`; fleet collapses onto one proxy |
| SR-05 | Medium | S | B | Upload script maps machine-chosen refs onto arbitrary env vars; `SecretRef` unvalidated |
| SR-06 | Low | R | A | Extension server has no body limit and no timeouts (bounded upstream by tee-node) |
| SR-07 | Medium | R | E | Launch policy cannot supply `CHAIN_ID`/`GOVERNANCE_*`; documented deploy panics at start |
| SR-08 | Medium | S | C | Chain-group threshold may be 1: one provider decides supply and block time |
| SR-09 | Medium | S | C | No chain-id check on providers; Flare and Songbird share the token address |
| SR-10 | Medium | R | C | No per-provider timeout; one stalled provider fails the observation |
| SR-11 | Medium | R | C | RPC response bodies unbounded; OOM loses memory-only secrets |
| SR-12 | Medium | R | B | Rotating a PRIVATE URL deadlocks between `ApplyEndpoints` and `ApplySecret` |
| SR-13 | Medium | R | B | No tool can produce a `hex_cash_url` envelope; cash source cannot be enabled |
| SR-14 | Medium | S | D/E | Well-known keys as silent defaults for `DEPLOYMENT_PRIVATE_KEY` and `PROXY_PRIVATE_KEY` |
| SR-15 | Medium | S | D | `allow-tee-version` allowlists the code hash the proxy reports, unverified |
| SR-16 | Medium | R | D | Readiness report signer is never bound to a machine |
| SR-17 | Medium | R | D | Keeper and tools wait without bound: receipts, proxy HTTP, body sizes |
| SR-18 | Medium | S | E | Base image untagged (contradicting `REPRODUCIBILITY.md`); builder Go a year old; proxy image on mutable tag |
| SR-19 | Medium | R | D | Request fee paid blind with no ceiling |
| SR-20 | Low | R | A | Direct-channel refusals poison the public `/state` counters |
| SR-21 | Low | S | A | Readiness correlator documented non-zero but not enforced |
| SR-22 | Low | R | A | Handler work outlives tee-node's 2 s budget; reported result can diverge from applied state |
| SR-23 | Low | R | A | Malformed port env vars silently replaced by defaults |
| SR-24 | Low | R | C | Inflated head claim forces `SupplyNoQuorum` when honest heads differ by one |
| SR-25 | Low | R | C | Header number truncated to 64 bits |
| SR-26 | Low | R | C | Supply then backing read sequentially under one deadline; window check is dead |
| SR-27 | Low | R | C | Dead implausibility checks; `PriceImplausible` never raised; no magnitude bound |
| SR-28 | Low | R | C | Backing parse accepts trailing data and ignores `metric_name` |
| SR-29 | Low | S | B | Secret provisioned before endpoints is stored unvalidated; one backing admin can block SET_ENDPOINTS |
| SR-30 | Low | S | B | Replay window after restart: versions are in-memory; the 1 h freshness is the only bar |
| SR-31 | Low | S | B | `resolveURL` is a prefix check: loopback, link-local and userinfo hosts pass; denylist charset |
| SR-32 | Low | R | B | `Open()` is all-or-nothing per group even when quorum is met without the missing provider |
| SR-33 | Low | S | B | `usdxprov -tee` optional; unpinned runs trust a plain-HTTP `/info` for the encryption key |
| SR-34 | Low | S | B | Upload scripts export every secret into all child processes; here-string key splitting |
| SR-35 | Low | R | B | `envVarFor` collisions; only `hex_api_key` skipped, so `hex_cash_url` is prompted for |
| SR-36 | Low | S | E | `oracle-status.sh` puts the deployment key on a command line |
| SR-37 | Low | S | E | `oracle-cycle.sh` builds into a predictable `/tmp` path, then executes it |
| SR-38 | Low | S | E | Sibling image copies the whole checkout, `.env` included, with no `.dockerignore` at that context |
| SR-39 | Low | R | D | `set-governance` accepts malformed addresses silently |
| SR-40 | Low | R | D | Keeper `-feedId` accepted but preflight pins a constant |
| SR-41 | Low | R | D | Registration hardcodes a 1 gwei fee and parses receipts by log index |
| SR-42 | Low | R | E | `start-services.sh` validates `EXTENSION_ID` by substring |
| SR-43 | Low | R | E | `full-setup.sh` dead `CHAIN` branch; silent `.env` overwrite |
| SR-44 | Low | R | D | `verify-deploy` treats unset `LOCAL_MODE` as local |
| SR-45 | Low | S | E | `oracle-status.sh` runs `curlimages/curl:latest` on the stack network |
| SR-46 | Low | S | E | Module-cache proxy listens on all interfaces during sibling builds |
| SR-47 | Info | — | A | Decode-error text reaches the public `Log` |
| SR-48 | Info | — | A | Contract §5 serialisation is per-field, not per-handler |
| SR-49 | Info | — | A | `ActionResult.Version` differs from tee-node's |
| SR-50 | Info | — | E | apt snapshot bootstrap disables TLS peer verification |
| SR-51 | Info | — | B | Admins blind-sign a 32-byte hash under `personal_sign` |
| SR-52 | Info | — | C | `observedAt` is provider-supplied; a colluding quorum can advance the ratchet |
| SR-53 | Info | — | C | Freshness bounds rest on the enclave wall clock |
| SR-54 | Info | — | B | Envelope version is burnt before install; `Ref` trim is dead leniency |
| SR-55 | Info | — | E | `MODE` and `INITIAL_OWNER` are launch-overridable without a code-hash change |
| SR-56 | Info | — | A | tee-node publishes an empty extension state; every `/info`-based read of `/state` is inert today |

## A. Extension process

### SR-02 · High · S — `/action` binds all interfaces and the instruction channel is trusted by arrival

**Where.** `internal/extension/extension.go:92`, `internal/extension/router.go:77-81`,
`Dockerfile:83,92`, `Dockerfile.siblings:71,80`.

```go
e.Server = &http.Server{Addr: fmt.Sprintf(":%d", extensionPort), Handler: mux}
```

**What.** The extension listens on every interface. `processInstruction` documents that
"arrival on this channel is itself the authorisation": tee-node has verified the
cosigners before forwarding, so the extension checks nothing about an instruction's
provenance. tee-node itself posts to `http://localhost:<port>/action`
(tee-node `internal/processors/instructions/default.go:54`), so loopback is all the
extension needs. Both Dockerfiles declare `EXPOSE 5501 7701 7702`. Confidential Space
opens declared `EXPOSE` ports in the VM firewall when the operator's VPC rules permit
them (Google documentation, image 230600+; the operator's actual VPC rules were not
inspected). By contrast tee-node's sign server binds `127.0.0.1` on purpose
(tee-node `internal/settings/settings.go:40`).

**Scenario.** A peer that can reach port 7702 posts a hand-built action with
`type: "instruction"`, op type `TEE_ORACLE`, command `SET_ADMINS` with its own keys and
a high version, then `SET_ENDPOINTS` naming its own providers and its own cash-URL
commitment, then `SET_SECRET` signed by its own admins. The on-chain store rejects
updates whose `endpointsHash`/`adminsHash` differ from governance's, so the price cannot
be corrupted, but the feed freezes until a restart and re-provisioning. If the real Hex
API key was already held, the attacker's cash URL is then opened with it in
`X-API-Key`, over TLS to a host the attacker controls. The compose stack does not publish
7702, so on the local simulation the port is reachable from other containers on the
compose network only (`oracle-status.sh:129` relies on exactly that).

**Fix.** Bind `127.0.0.1:%d`. Reject non-loopback `RemoteAddr` in `actionHandler` as a
second line. Drop `7702` (and `7701`) from `EXPOSE`, keeping only what must be reachable.
Update `docs/extension-contract.md:75`, which claims the sign port "is never exposed
outside the container": the tee-node bind enforces that, this repository's `EXPOSE`
undermines it.

**Status.** Fixed after the review: the extension binds `127.0.0.1`, both Dockerfiles
declare `EXPOSE 5501` only, and every consumer reads the state out of the proxy's `/info`,
where tee-node now carries it (SR-56). The `RemoteAddr` check was not added; the loopback
bind makes it redundant.

### SR-06 · Low · R — no request body limit and no server timeouts

**Where.** `internal/extension/extension.go` (`actionHandler`, `NewExtension`).

```go
err := json.NewDecoder(r.Body).Decode(&action)
```

**What.** The server itself has no `MaxBytesReader`, no `ReadHeaderTimeout`, `ReadTimeout`
or `IdleTimeout`. What keeps this low is that every action reaches it through tee-node,
which bounds the input before dispatch (tee-node `internal/settings/settings.go:74-79`,
`internal/router/queue/queue.go:27`, `internal/router/router.go:282`):

| Bound | Value |
| --- | --- |
| Fetched action response (`io.LimitReader`) | 10 MB |
| Action size | 10 MB |
| Per-signer entries (signatures, timestamps, messages) | 200 |
| Total variable-message bytes | 1 MiB |

With the SR-02 fix the listener is loopback-only, so tee-node is the only party that can
reach it in a container or a Confidential Space VM. The residual exposure is a process on
the same host in the host-process development stack, and the standard-library default of
no `ReadHeaderTimeout` (advisory GO-2026-6089, see §F).

**Fix.** Hygiene rather than urgency: `ReadHeaderTimeout` and `IdleTimeout` on the
server, and `http.MaxBytesReader` sized just above tee-node's action limit so the two
halves state the same bound.

**Status.** Downgraded from Medium after review discussion: tee-node's limits were
confirmed in its source, and the loopback bind removes every caller but tee-node.

### SR-20 · Low · R — direct-channel refusals poison the `/state` counters

**Status.** Moot since 2026-09-16: `GET /state` carries the empty state and the extension
keeps no counters. See review II, T-26.

**Where.** `internal/extension/result.go:38-44`; contradicts `internal/extension/readiness.go:14-16`.

`result()` increments `failureCount` and sets `lastErrorClass` for every refused outcome,
including `GET_READINESS` with a malformed body and `GET_FEED` sent over the direct channel.
A holder of the direct API key can drive the world-readable `/state` (republished at the
proxy's `/info`) to look faulty; only a successful on-chain `GET_FEED` clears it. The
readiness comment says the command "leaves the /state counters untouched", which is only
true on success.

**Fix.** Count only `GET_FEED` outcomes, or keep a separate counter for direct-channel refusals.

### SR-21 · Low · S — readiness correlator is documented non-zero but not enforced

**Where.** `internal/extension/readiness.go:19-24`, `internal/wire/readiness/readiness.go:85-93`.

`EncodeBody` refuses a zero timestamp and a zero payload hash but not a zero
`InstructionID`. A direct action with id `0x00…` yields a validly signed report bound to
the zero correlator. Whether the proxy ever assigns such an id was not verified.

**Fix.** Refuse `correlator == (common.Hash{})` in both places with `UnexpectedPayload`.

### SR-22 · Low · R — handler work outlives tee-node's budget

**Where.** `pkg/server/server.go:36` (`teenode.New(signPort, 10*time.Second)`),
`internal/extension/feed.go:39`, `readiness.go:35`, `provisioning.go:52`
(`context.Background()`); tee-node `internal/settings/settings.go:46` (`ProxyTimeout = 2s`).

tee-node abandons the `/action` call after 2 s and reports the action invalid, but the
handlers run on background contexts and the signing client waits up to 10 s. A slow
`/decrypt` makes `SET_SECRET` *succeed* in the enclave after the operator was told it
timed out; for `GET_FEED` a signature is produced and discarded. Goroutines are bounded,
so this is not a leak.

**Fix.** Derive handler contexts from `r.Context()` and keep the teenode client timeout
under tee-node's budget, or make that budget the single source.

**Status.** The signing client timeout is now `signTimeout = 1s` (`pkg/server/server.go`),
inside tee-node's 2 s, with the reasoning in a comment; the value had arrived as an
unexplained literal. Handler contexts still come from `context.Background()`; the
`r.Context()` plumbing remains open.

### SR-23 · Low · R — malformed port variables silently replaced by defaults

**Where.** `internal/config/config.go:98-107`.

A typo in `EXTENSION_PORT` or `SIGN_PORT` falls back to the default without a log line.
In `cmd/extension-tee` both tee-node and the extension parse the same value, so they stay
consistent; in `cmd/dev/devserver` the extension may listen where nothing forwards.
`CHAIN_ID` and `EXTENSION_ID` already panic when unset; the ports should fail the same way.

### SR-47 · Info — decode-error text reaches the public `Log`

`internal/extension/router.go:28,45` return `encoding/json`'s message in a 400 body,
which tee-node places in `ActionResult.Log`. The text names Go field names and one
offending character, never payload content. Acceptable; noted because the `failure`
package's rule is "constant text only" and these are the exceptions.

### SR-48 · Info — contract §5 serialisation is per-field

`processGetFeed` updates `sourceHealth` and the counters in two separate critical
sections (`feed.go:43-45,51-57`). tee-node abandons but does not cancel a slow handler,
so two handlers can overlap and a concurrent `/state` can mix health from one call with
counters from another. No data race: the state handler copies under `RLock` and the poller
replaces rather than mutates its map. Either hold the lock across the update or amend §5.

### SR-56 · Info — tee-node publishes an empty extension state

Both tee-node entrypoints initialise the node with `node.ZeroState{}` (tee-node
`cmd/main.go:17`, `cmd/extension/main.go:17`), whose `State()` returns an empty
`TeeState.State`, and nothing in tee-node performs an HTTP GET against the extension. So
`GET /state` is never folded into the attested machine info, in the pinned version or in
develop (`v0.0.25-20`). The design documents assume it eventually will be
(`docs/usdx-oracle-flows.md:35`, `docs/usdx-oracle-readiness-query.md:21` says as much),
and the tools already fall back cleanly: `fccutils.ErrNoAttestedState`, `upload-rpc-keys.sh`
requiring `--refs` or `--all`, `send-endpoints -wait` reporting at INFO. Two consequences
worth stating plainly: today `/state` is readable only from inside the machine's own
network namespace, and the one working remote read of the same facts is the signed
`GET_READINESS` report over the proxy's direct channel. The world-readable treatment of
`/state` in the spec remains the right assumption, since the fold-in can arrive with a
tee-node upgrade and no change here.

**Status.** tee-node `4761481` (develop) folds the state in: it decodes the extension's
`GET /state` body as its `TeeState` on every info request. That decoder expects `state` as
hex bytes, so the extension's `StateResponse.State` changed from a JSON object to the JSON
encoding of `State` as bytes, and every consumer (the tools via
`fccutils.ReadExtensionState`, the scripts via `read_machine_state`, the harness via a
mock node that folds the same way) reads the state out of `/info` and nowhere else. An
extension tee-node cannot reach shows as the all-ones `StateUnavailableVersion`, reported
distinctly from an empty field. The pinned tee-node still publishes the field empty until
the pin moves.

A consequence surfaced on the first live run: the registry's availability check accepts a
non-empty state only through a registered `TeeExtensionStateVerifier`, so with the fold-in
`toProduction` reverted with `InvalidResponseData()`. A minimal verifier was written and
published with `setExtensionContracts` to get past it.

**Status, 2026-09-16.** Moot, and the verifier is removed. The extension answers `GET /state`
with the empty state (see review II, T-26), which the availability check accepts with no
verifier registered while still refusing tee-node's unavailable sentinel. `wire-extension`
publishes the zero address.

### SR-49 · Info — `ActionResult.Version` differs from tee-node's

Extension results carry `config.Version` (`0.1.0`); tee-node's own results carry
`EncodingVersion = "1.0.0"`. No reader of the field was found in tee-node. Confirm what the
proxy and keeper expect and document the asymmetry if it is intentional.

## B. Provisioning

### SR-05 · Medium · S — the upload script maps machine-chosen refs onto arbitrary env vars

**Where.** `deploy/scripts/upload-rpc-keys.sh:113-121` (`ask_secret`), `:126-135`
(`envVarFor`), `:300-305`, `:325`; `internal/provisioning/store/store.go:769-790`
(`validateEndpoint`, no charset or length check on `SecretRef`); `:557` (refs into `/state`).

```bash
name="$(printf '%s' "$1" | tr 'a-z' 'A-Z' | tr -c 'A-Z0-9_' '_')"
...
if [ -n "${!var:-}" ]; then return; fi        # ask_secret: already set → use it
...
USDXPROV_VALUE="${!var}" "$BIN_DIR/usdxprov" build ...
```

**What.** The list of refs to upload is the machine's `missingSecretRefs`, read from
`/state` via the proxy's `/info`. Each ref is upper-cased into an environment variable
name and, if that variable is already set, its value is used as the credential without a
prompt. Nothing restricts a `SecretRef` in SET_ENDPOINTS to a safe alphabet, so `usdxprov_key`
maps to `USDXPROV_KEY`, the variable holding the operator's admin private key;
`usdxprov_api_key`, `usdxprov_keys`, `aws_secret_access_key` and `path` work the same way.

**Scenario.** A SET_ENDPOINTS carrying a PUBLIC endpoint `https://attacker.example/{secret}`
with `secret: "usdxprov_key"` makes the script encrypt the admin key to the enclave, sign
it with the operator's own admin keys and submit it; unknown refs are accepted by the
store, and on the next `Open()` the value is substituted into the attacker's URL and sent
over TLS. The full chain needs a malicious publisher (governance is trusted), so this is
rated medium rather than high. A compromised proxy alone, fabricating `missingSecretRefs`,
already makes the operator ship arbitrary environment variables into the enclave; if the
run is not pinned with `-tee` (SR-33), the proxy can also supply the encryption key and
read them.

**Fix.** Read credentials only from a dedicated namespace such as `USDX_SECRET_<REF>` and
never from an arbitrary variable. In the store, restrict `SecretRef` to `^[a-z0-9_]{1,64}$`
in `validateEndpoint` (the envelope already caps refs at 64 bytes).

**Note.** With every current tee-node the machine-chosen half of this chain is inert: the
extension's `missingSecretRefs` never reach the proxy's `/info` (SR-56), so the script
refuses to run without `--refs` or `--all`, both operator-supplied. The mapping is still
applied to those inputs, and the exposure returns the day tee-node folds the state in, so
the fix stands.

### SR-12 · Medium · R — rotating a PRIVATE URL deadlocks the store

**Where.** `internal/provisioning/store/store.go:186-196` and `:430-434`.

```go
for ref, value := range s.secrets {                    // ApplyEndpoints
    if err := verifySecret(&e, ref, value); err != nil { return err }
}
...
if endpoints != nil {                                  // ApplySecret
    if err := verifySecret(endpoints, env.Ref, plaintext); err != nil {
        return failure.ProvisionRejected
    }
}
```

**What.** A new SET_ENDPOINTS is rejected when a stored secret does not fit it, and a new
secret is rejected when it does not fit the current SET_ENDPOINTS. Governance publishes E2
with a new cash-URL commitment: `ApplyEndpoints(E2)` finds the stored E1 blob does not
match and refuses E2; sending the E2 blob first fails against E1. The ref cannot be
renamed, since `validateCashGroup` pins `hex_cash_url`. The same holds for any ref that
flips PUBLIC/PRIVATE. Because the sender records the new hash before dispatch, the
machine's updates carry a stale `endpointsHash` from that moment until it is restarted and
fully re-provisioned. `TestPrivateURL_MismatchIsRejectedOnEndpoints` pins the rejecting
behaviour rather than a rotation path.

**Fix.** In `ApplyEndpoints`, evict stored secrets that no longer fit and list them as
missing, instead of refusing the publication. Keep the `ApplySecret` direction as is.

### SR-13 · Medium · R — no tool can produce a `hex_cash_url` envelope

**Where.** `tools/cmd/usdxprov/main.go:104,145`, `tools/cmd/usdxprov/flags.go:31-46`,
`internal/provisioning/store/abi.go:88-95,112`, `tools/pkg/oraclepayload/oraclepayload.go:237`.

The private URL's plaintext is `abi.encode(bytes32 salt, string url)`, which always
contains zero bytes. `usdxprov build` takes the value as a string from an environment
variable or a flag, neither of which can carry NUL. `EncodePrivateURL` exists in both
modules but no command calls it; `upload-rpc-keys.sh` and `upload-hex-key.sh` do not
handle the ref; `docs/usdx-oracle-production-setup.md` never mentions it. The machine
therefore always lists `hex_cash_url` as missing, never reports `ready: true`, and runs
cash-less permanently (correct but under-reporting backing, so the price can only be lower
than it should be).

**Fix.** Add a `-value-file` or `-value-hex` input to `usdxprov build`, or a
`-private-url <url> -salt <hex>` mode that calls `EncodePrivateURL`; have the hex-key
script (backing role) upload it; document the step.

**Status.** Fixed after the review. `usdxprov build -ref hex_cash_url` derives the blob
from `HEX_CASH_URL` and `HEX_CASH_SALT` and refuses a string value; `usdxprov commit`
prints the matching `urlHash`. `publish-local.sh endpoints` commits the `hex_cash` group
to the URL from `.env`, generating and recording the salt, and `upload-hex-key.sh` uploads
both refs from the same variables.

### SR-29 · Low · S — a secret provisioned before endpoints is stored unvalidated

**Where.** `internal/provisioning/store/store.go:427-434`, `:445-448`.

With `endpoints == nil` (after a restart), `ApplySecret` checks nothing but the envelope;
`verifySecret` runs `checkCredential` for `hex_api_key` only when endpoints are present.
One compromised backing admin (1-of-N by policy) installs `hex_api_key = "x\r\n"` or a
garbage `hex_cash_url` blob, and every later SET_ENDPOINTS fails the pre-check in SR-12
until that same role overwrites the value. Configuration denial of service, not price control.

**Fix.** Run the credential and blob-shape checks unconditionally in `ApplySecret`, and
adopt the evict-rather-than-reject rule from SR-12.

### SR-30 · Low · S — replay window after restart

**Where.** `internal/provisioning/envelope/envelope.go:34,36,387-399`;
`internal/provisioning/store/store.go:121,135,179,201,232`.

Per-ref envelope versions and the SET_ENDPOINTS/SET_ADMINS version ratchets are in-memory.
After a restart, a captured envelope younger than `Freshness` (1 h, plus 2 min skew) can be
replayed to reinstate a superseded credential, and a superseded SET_ADMINS naming a
rotated-out signer would be accepted if it can be re-delivered (whether tee-node or the
relay dedups re-delivery was not inspected). The code acknowledges the envelope half.

**Fix.** Reject `issuedAt` earlier than process start minus `ClockSkew`; bootstrap the two
version ratchets from the sender contract's current versions at start.

### SR-31 · Low · S — `resolveURL` is a prefix check

**Where.** `internal/provisioning/store/store.go:802-822`.

The check is `HasPrefix(raw, "https://")` plus placeholder position; nothing parses the
URL. `https://localhost:9090/`, `https://169.254.169.254/` and `https://user:pw@host/`
pass. The credential charset is a denylist (`" \t\r\n/?#&"`), so a value containing `=`,
`;`, `@`, `%` or `\` can rewrite the query parameter it lands in, though not the host.
Server-side request forgery to tee-node or the metadata service is blocked in practice only
because those endpoints do not speak TLS.

**Fix.** Parse with `net/url`; require `https`, a non-empty host, no userinfo; refuse
loopback, link-local and private literal hosts; allowlist the credential charset.

### SR-32 · Low · R — `Open()` is all-or-nothing per group

**Where.** `internal/provisioning/store/store.go:609-616`.

One provider whose secret is missing makes `Open` return `NotConfigured` for the whole
observation, even when the group's other providers meet the threshold. With `flare` at 2-of-3
and one key outstanding, no price is produced.

**Fix.** Skip unresolved providers and fail only when fewer than `Threshold` remain.

### SR-33 · Low · S — `usdxprov -tee` is optional

**Where.** `tools/cmd/usdxprov/main.go:126-142`, `:422-423`.

Without `-tee`, the tool encrypts to whatever public key `/info` returns, fetched over plain
HTTP by default. The scripts pass the attested id when `verify-tee` produced one; a run by
hand does not. The "always pass -tee" rule in the docs is advisory.

**Fix.** Make `-tee` mandatory; require an explicit `-insecure-unpinned` flag to omit it.

### SR-34 · Low · S — upload scripts export every secret

**Where.** `deploy/scripts/upload-rpc-keys.sh:110,121,198`, `deploy/scripts/upload-hex-key.sh:71,82`.

`ask_secret` exports each value, so `go build`, `curl`, `jq`, `xxd` and `verify-tee`
inherit admin keys, the API key and every provider credential. Admin keys are split with a
here-string; bash before 5.1 backs `<<<` with a temporary file, so the joined keys may
briefly touch disk on such hosts.

**Fix.** Do not export; pass each secret only to the one command that needs it. Split with
a bash array literal or `tr` into a pipe.

### SR-35 · Low · R — `envVarFor` collisions and the missing backing skip

**Where.** `deploy/scripts/upload-rpc-keys.sh:129`, `:53`, `:281`; `internal/config/config.go:159`.

`ankr-key`, `ankr_key` and `ANKR_KEY` all map to `ANKR_KEY`, so a second ref silently
receives the first ref's credential. The script skips only `hex_api_key`, while the machine
always lists `hex_cash_url` as missing, so a default run prompts for a "credential for
hex_cash_url", signs it with provider keys and is refused.

**Fix.** Refuse refs outside `[a-z0-9_]` and detect collisions before prompting; skip every
ref in `config.BackingRefs()`.

### SR-51 · Info — admins blind-sign a hash

`tools/cmd/usdxprov/main.go:223` signs `TextHash(keccak256(body))`. A hardware wallet shows an
opaque hash, so the signer cannot see ref, version or teeId. EIP-712 typed data would.

### SR-54 · Info — version burnt before install; dead trim

`envelope.go:399` records the version before decrypt and `verifySecret` succeed, so a
correctly signed envelope that fails later burns its version (harmless with epoch-second
versions). `envelope.go:505` trims `Ref` after decode, but the signature is checked over
the re-encoded body, so a padded ref can never verify; reject rather than trim.

## C. Observation and price

### SR-08 · Medium · S — a chain-group threshold of 1 is accepted

**Where.** `internal/provisioning/store/store.go:707`, `internal/observation/supply/supply.go:115`;
`internal/config/config.go:185` (`MinProviderThreshold = 2` governs admin signatures, not this).

At threshold 1 the provider reporting the highest head is pinned and alone answers the
block time, decimals and total supply; it can return a fresh header timestamp (defeating
`MaxBlockAge`) and any supply. Understating supply publishes up to par. More generally the
design tolerates `Quorum - 1` colluders: `Quorum` colluders can claim a head only they hold
(honest providers then fail at that block) and answer identically. The floor is a
configuration matter, but the code should not accept the degenerate value on a chain group.

**Fix.** Reject `Threshold < 2` for chain groups in `store.validate` and
`ChainSource.Validate`; document that the tolerated colluder count is `Quorum - 1`.

**Status.** The code floor is unchanged. The simulation configuration
(`deploy/config/endpoints/public-only.json`) moved `flare` and `ethereum` from 2-of-2 to
2-of-3, after two incidents in which one unreachable public provider (a VPN route, then a
resolver that would not resolve `ethereum-rpc.publicnode.com`) stopped every observation;
`songbird` stays 1-of-1 because no second public provider answers the pinned-block calls.

### SR-09 · Medium · S — no chain-id verification; Flare and Songbird share the token address

**Where.** `internal/config/config.go:230-232`, `internal/observation/supply/supply.go:45-48`.

```go
{Name: "flare",    Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", ...},
{Name: "songbird", Token: "0x4A771Cc1a39FDd8AA08B8EA51F7Fd412e73B3d2B", ...},
```

Nothing queries `eth_chainId`. If the `flare` group's providers serve Songbird state, by
misconfiguration or by a colluding quorum, every read succeeds and agrees, the token exists
at the same address, and the total counts Songbird's supply twice while dropping Flare's.
If Flare's supply exceeds Songbird's, the total shrinks and the price is overstated. On
Ethereum the address differs, so a wrong chain fails closed; on the two Flare networks it
does not.

**Fix.** Add the expected chain id to `config.Chain`, read `eth_chainId` per provider in
`Read`, and mark any provider that answers differently `down`.

### SR-10 · Medium · R — no per-provider timeout

**Where.** `internal/observation/supply/supply.go:191-212` (same shape at `260-276`,
`365-380`); `internal/provisioning/store/store.go:614` (`ethclient.Dial`);
go-ethereum v1.17.4 `rpc/http.go:133-134` (`new(http.Client)`, no timeout).

Every round waits for all providers, and the client has no HTTP timeout, so the only bound
is the 20 s observation deadline. A provider that accepts the connection and never answers
makes the round take the full 20 s; `ctx.Err()` then fires and the observation fails with
`SupplyTimeout` even though the other providers met quorum at once. Repeated each tick,
the feed goes stale after `MaxObservationAge`, defeating the redundancy the extra provider
was meant to buy.

**Fix.** Give each provider call its own `context.WithTimeout` inside the goroutine, or dial
with `rpc.WithHTTPClient(&http.Client{Timeout: …})`.

### SR-11 · Medium · R — RPC response bodies are unbounded

**Where.** go-ethereum v1.17.4 `rpc/http.go:199,263` (reached from `supply.go:197,265,371`);
compare `internal/observation/backing/backing.go:193` (64 KiB cap).

The RPC client hands `resp.Body` straight to `json.NewDecoder(...).Decode`. A malicious
provider can stream gigabytes of a single JSON token inside the deadline and exhaust
memory. Because credentials are memory-only, a crash means re-provisioning, not just a
restart.

**Fix.** Supply an `http.Client` whose transport wraps the body in `http.MaxBytesReader`
(1 MiB is ample), via `rpc.DialOptions(ctx, url, rpc.WithHTTPClient(...))`.

### SR-24 · Low · R — an inflated head claim forces `SupplyNoQuorum`

**Where.** `internal/observation/supply/supply.go:232-233`, `:441-443`.

With 2-of-3 and honest heads `H` and `H-1`, a provider reporting `H+1000` makes the pinned
head `H`; the honest provider at `H-1` cannot serve it, leaving one honest answer against
one forged one, a tie, so `agreed = 0`. Honest nodes are routinely a block apart, so this
turns a healthy 2-of-3 into a flapping group. Ties fail closed, so no wrong price; but the
health map blames the honest node (`lagging`) rather than the liar.

**Fix.** On failure at the pinned head, retry at the next lower distinct head before giving
up, or consider only heads corroborated by at least two providers within a small tolerance.

### SR-25 · Low · R — header number truncated to 64 bits

`supply.go:206` uses `h.Number.Uint64()` on a `*big.Int` that decodes up to 256 bits; a
provider returning `2^64 + k` is read as head `k`. `MaxBlockAge` then rejects the ancient
block, so the outcome is a failed read, not a wrong price. Check `IsUint64()` first.

### SR-26 · Low · R — sequential reads under one deadline; dead window check

`internal/observation/observation.go:128,134` read supply then backing under the one 20 s
context. Backing may legitimately need ~10 s (two 5 s attempts), so a slow supply phase
makes a healthy backing source time out. `config.go:125-127` says of `ObservationWindow`:
"the deadline binds first and this never fires". Run the two phases concurrently, which
also makes the window check meaningful, or give each its own sub-deadline.

### SR-27 · Low · R — dead implausibility checks and no magnitude bound

`price.go:29-34` refuses negative bonds, cash or supply, but `ParseAmount` rejects any sign
and `big.Int.SetBytes` is never negative, so neither branch can fire. `PriceImplausible`
(`failure.go:133`) is raised nowhere. The only bounds are the 64-byte decimal cap and the
par clamp, so a backing figure of 10^63 publishes exactly par. Consistent with trusting
Hex, but the vocabulary advertises a check that does not exist. Either drop the dead
entries or add a real bound (backing greater than k times supply, or a maximum
per-observation change) routed to `PriceImplausible`.

### SR-28 · Low · R — backing parse accepts trailing data and ignores `metric_name`

`backing.go:211-213` decodes one JSON value; `{...}garbage` passes and duplicate keys take
the last value. `MetricName` is decoded and never compared, so a response for a different
Hex metric is accepted as reserves. After `Decode`, require the next `Decode` to return
`io.EOF`, and compare `metric_name` with a per-source expectation.

### SR-52 · Info — `observedAt` is provider-supplied

`observation.go:177` stamps the update with the home chain's header time. A colluding Flare
quorum can push it forward up to the store's future-skew bound, advancing the strictly
increasing ratchet so honest later updates are rejected until chain time catches up.
Requires quorum compromise; denial of service only.

### SR-53 · Info — freshness bounds rest on the enclave wall clock

`supply.go:153` and `backing.go:248` compare against `time.Now()`. A clock behind by `D`
admits blocks up to `MaxBlockAge + D` old until `checkClock` catches the drift; a clock
more than 60 s behind Hex rejects fresh republishes with `BackingTimestamp`. Within the
design's tolerances; noted for completeness.

## D. Keeper and publication tools

### SR-03 · High · R — the keeper does not filter individual updates by signer

**Where.** `tools/pkg/keeper/cycle.go:262-272`; `tools/pkg/keeper/preflight.go` (batch-level only).

```go
for _, u := range updates {
    signer, err := u.Signer(chainID)
    ...
    seen[signer] = true
    groups[u.ObservedAt] = append(groups[u.ObservedAt], member{u, signer})
}
```

The recovered signer is never compared with `cfg.TeeIDs` (the machines actually asked) or
the registry's active set; the only signer check is the on-chain rehearsal of the whole
batch, and the preflight returns on the first mismatching update. A compromised proxy, or
anyone posting to the internal API (SR-01), returns one update stamped with the honest
machines' `observedAt` (readable from the other proxies' public results) and a signature
recovering to an unregistered key, or a stale hash. It joins the largest group, the batch
is rejected, nothing is submitted, and the request fee was already paid. Every cycle. The
store's own verification keeps correctness; a single bad machine or proxy stops the feed.

**Fix.** Before `chooseBatch`, drop any update whose signer is not in `cfg.TeeIDs`; drop
per-update on extension, feed or hash mismatch; submit the remainder if it still meets
`requiredSignatures`.

**Status.** Fixed after the review. `keeper.Screen` judges every answer on its own before the
batch is formed — signer among the addressed machines, extension and feed ids, both
configuration commitments — and drops the failing ones by guard name into the record's
`dropped` list; `chooseBatch` does the same for a repeated signer or a mismatched
`observedAt`. `Preflight` keeps the batch-level rules. A round with nothing left is rejected
under the one guard everything failed, or `NoUsableUpdate`.

### SR-04 · High · R — `oracle-cycle.sh` always passes `-p`

**Where.** `deploy/scripts/oracle-cycle.sh:62,73`; `tools/cmd/keeper/main.go:148-150`;
`tools/pkg/keeper/fleet.go:56`.

The keeper deliberately treats an explicitly given `-p` as an override of every machine's
registered URL (its comment says why). The wrapper always passes it, and `deploy/README.md`
documents the wrapper as the way to run the keeper on the testnets. With two or more
machines, every request's results are polled on the one proxy in `EXT_PROXY_URL`; the other
machines' results, filed on their own proxies, are never collected, so the batch has one
update and fails `NotEnoughSignatures` whenever `requiredSignatures >= 2`. On the single-
machine simulation this is invisible.

**Fix.** Pass `-p` only when the operator sets an explicit override variable; let the
keeper's registry-driven URLs stand as the default.

**Status.** Fixed after the review. `oracle-cycle.sh` passes `-p` only when `PROXY_OVERRIDE`
is set; otherwise the keeper reads each machine's registered url from the registry and
`EXT_PROXY_URL` is only where results are looked for first.

### SR-15 · Medium · S — `allow-tee-version` allowlists the code hash the proxy reports

**Where.** `tools/cmd/allow-tee-version/main.go:58,63,81`; compare
`tools/cmd/register-tee/main.go:56` and `tools/cmd/verify-tee/main.go:110`, which call
`GetCodeHashAndPlatform` and cross-check the attestation.

The tool logs "Code hash is from proxy /info response — not independently verified against
attestation" and then signs `addTeeVersion` with the extension owner key using that hash.
`post-build.sh` runs it unattended. A compromised proxy gets an arbitrary code hash allowed
on the extension, which is the on-chain anchor `verify-tee` later relies on.

**Fix.** Use `GetCodeHashAndPlatform(teeInfo)` and its return values, or require an explicit
`-code-hash` obtained out of band.

### SR-16 · Medium · R — readiness report signer is never bound to a machine

**Where.** `tools/pkg/readinessreport/readinessreport.go:73-75`; the only `.Signer` reader
in `tools/` is `cycle.go:262`, which is for feed updates.

The report's signature is verified and the signer recovered, but no caller compares it with
the machine the proxy is supposed to front. Any key produces a "verified" report. A proxy
can answer `wouldSign: false` for its machine (and under SR-04 that one proxy speaks for
the whole fleet, so every cycle is skipped), or fake the `endpointsHash`/`adminsHash`
confirmation that `send-endpoints -wait` prints. The documentation's claim that this is
"the enclave's own claim rather than whatever answered a port" does not hold in the tools.

**Fix.** Compare `report.Signer` with the teeId of the machine that proxy fronts (from the
registry entry for that URL) and reject a mismatch.

### SR-17 · Medium · R — unbounded waits

**Where.** `tools/pkg/utils/instructions.go:417-418` (`bind.WaitMined(context.Background(), …)`);
`tools/pkg/keeper/cycle.go:494,518`, `tools/pkg/fccutils/tee_calls.go:23,89`,
`tools/cmd/send-endpoints/main.go:328`, `tools/cmd/send-admins/main.go:328`,
`tools/cmd/fleet-status/main.go:209` (`http.Get` with the default client, no timeout, no
body cap).

A stuck `requestFeedUpdate` (nonce gap, underpriced) blocks the cycle forever; the loop is
single-flight, so the keeper stops without exiting. A proxy that accepts the connection and
never answers hangs the poll indefinitely, so `PollTimeout` is not actually a bound.

**Fix.** Thread a context with a deadline into `RequestFeedUpdate` and `waitMined`; use one
`http.Client{Timeout: …}` plus `io.LimitReader` for every proxy read.

### SR-19 · Medium · R — request fee paid blind

**Where.** `tools/pkg/utils/instructions.go:112-116`, `:62-72`.

The keeper attaches whatever `getOperationFee` returns, every cycle, with no ceiling. A
lying RPC (in the stated threat model) or a governance mis-set inflates the fee and drains
the keeper wallet at the loop cadence with no alarm.

**Fix.** Add `-maxFeeWei`; record `request_skipped` and refuse when the quote exceeds it.

### SR-39 · Low · R — `set-governance` accepts malformed addresses

`tools/cmd/set-governance/main.go:54,82` use `common.HexToAddress`, which zero-fills or
truncates a typo, so a malformed `GOVERNANCE_SIGNERS` or `INITIAL_OWNER` registers a set
nobody holds. `send-admins` checks with `IsHexAddress`; this tool should too.

### SR-40 · Low · R — `-feedId` accepted but preflight pins a constant

`tools/pkg/keeper/preflight.go:38` hardcodes the USDX feed id; `tools/cmd/keeper/main.go:57`
exposes `-feedId`; `cycle.go:214` calls `Preflight` without it. Any other feed pays the
request fee every cycle and is then rejected locally. Pass `cfg.FeedID` or remove the flag.

### SR-41 · Low · R — registration hardcodes the fee and parses receipts by index

`tools/pkg/fccutils/registration.go:191,268,299` attach a fixed 1 gwei rather than a fee
read from `TeeOperationFees`; `:244-247` and `:281` take `receipt.Logs[1]` as the
attestation event, so an added or reordered event breaks resume with an error that hides
the cause. Read the fee as `operationFee` does; select logs by topic and address.

### SR-44 · Low · R — `verify-deploy` treats unset `LOCAL_MODE` as local

`tools/pkg/validate/checks.go:134-135` (and `:574`) pass when `LOCAL_MODE` is empty, so the
checks report success on a testnet whose `.env` omits it. Decide from the RPC chain id.

## E. Deployment, images and supply chain

### SR-01 · High · S — the proxy's internal API is published on all interfaces

**Where.** `deploy/docker-compose.yaml:25-26`; `.env.example:98-99`; neither Coston
overlay overrides `ports`. tee-proxy v0.0.18 `internal/server/internal.go:131-132`
registers `POST /result` and `POST /queue/{queueID}` through `prepareHandler`, whose third
argument only controls logging; there is no authentication on the internal server.

```yaml
- "${EXT_PROXY_INTERNAL_BIND:-0.0.0.0:6673}:6663"
- "${EXT_PROXY_EXTERNAL_BIND:-0.0.0.0:6674}:6664"
```

**Scenario.** Anyone who can reach the host on 6673 posts forged action results for any
instruction id or interferes with the node's queue. Combined with SR-03, one forged
status-1 result with a bad signature sinks every keeper cycle. Only 6664 is meant to be
public (`docs/usdx-oracle-production-setup.md:180`); 6663 exists for the TEE container on
the compose network.

**Fix.** Default `EXT_PROXY_INTERNAL_BIND` to `127.0.0.1:6673`, or drop the host publish
entirely and let the containers talk over the compose network; fix `.env.example`.

### SR-07 · Medium · R — the launch policy cannot supply `CHAIN_ID`

**Where.** `Dockerfile:90`, `Dockerfile.siblings:78`; `internal/config/config.go:255-259`;
`cmd/extension-tee/main.go:39`; `deploy/docker-compose.yaml:46,51-52`.

```dockerfile
LABEL "tee.launch_policy.allow_env_override"="LOG_LEVEL,PROXY_URL,INITIAL_OWNER,EXTENSION_ID,CHAIN_URL,MODE,CONFIG_PORT,SIGN_PORT,EXTENSION_PORT"
```

`CHAIN_ID` is now mandatory (it panics when unset, by design), but it is not in the
override list and has no baked default; `GOVERNANCE_SIGNERS` and `GOVERNANCE_THRESHOLD`,
which compose sets, are absent as well. Confidential Space rejects launch overrides not in
the label. The deployment steps do not mention `CHAIN_ID` at all. A production deploy
following the docs fails attestation on the override or panics at start: fail-closed, but
undeployable as documented. Compose hides this locally with `CHAIN_ID=${CHAIN_ID:-31337}`.

**Fix.** Add `CHAIN_ID,GOVERNANCE_SIGNERS,GOVERNANCE_THRESHOLD` to the label (or bake
`CHAIN_ID` per image) and list them in the deployment steps. Consider whether `MODE` and
`INITIAL_OWNER` should remain overridable (SR-55).

### SR-14 · Medium · S — well-known keys as silent defaults

**Where.** `tools/pkg/support/support.go:106-109` with `tools/pkg/configs/configs.go:80`;
`deploy/docker-compose.yaml:21`; `deploy/scripts/start-services.sh:121`; `.env.example:2`.

Every tool goes through `DefaultSupport`, which falls back to a hardcoded Hardhat key with a
warning when `DEPLOYMENT_PRIVATE_KEY` is unset; on a testnet with a misconfigured `.env`,
registrations and claim-back addresses end up on a key everyone holds, and anything sent to
that address is lost. `PROXY_PRIVATE_KEY` defaults to the same well-known key on every chain
in the base compose and in `start-services.sh`, and `.env.example` ships it as the live
value. The proxy's signature over `TeeInfo` is what `TeeProxyId` recovers and what
registration records as `proxyID`; with a public key anyone can forge proxy-signed `/info`.
The Coston overlays already make `DIRECT_API_KEY` mandatory with `${VAR:?}` but leave this
one defaulted. `tools/cmd/local-stack` gets it right and refuses to start without the key.

**Fix.** In `DefaultSupport`, return an error unless the RPC chain id is 31337. In the
Coston overlays use `PROXY_PRIVATE_KEY=${PROXY_PRIVATE_KEY:?…}`; blank it in `.env.example`.

### SR-18 · Medium · S — image and toolchain pins

**Where.** `Dockerfile:68`, `Dockerfile.siblings:61` (`FROM gcr.io/distroless/static`,
no tag, no digest); `REPRODUCIBILITY.md:13,28` ("digest-pinned distroless base");
`Dockerfile:10` (`golang:1.25.1-trixie`); `deploy/proxy/Dockerfile:15,22,29`
(`golang:1.25.1-alpine`, `git clone --depth 1 --branch ${TEE_PROXY_VERSION}`, `alpine:3.21`).

For a TEE whose identity is the image's code hash, a floating base changes the hash on
rebuild, and the reproducibility document states the opposite of what the Dockerfile does.
The production builder pins Go 1.25.1 (September 2025); every standard-library advisory in
§F post-dates it. The proxy image builds from a mutable tag on undigested bases. Generated
bindings are git-ignored and rebuilt from an unpinned sibling checkout, so the ABI the
keeper and publishers use depends on whatever commit that checkout is at.

**Fix.** Pin distroless by digest; bump the builder to the latest 1.25.x patch (and re-record
the code hash); pin the proxy build to a commit and digested bases; commit the generated
bindings or record the upstream commit they were generated from.

**Status.** Fixed in part after the review. The Dockerfile is now tee-node's, with
`distroless/static-debian12` pinned by digest and the same cgo static build; the proxy is
tee-proxy's published image at the tag `tools/go.mod` pins, so there is no proxy build to pin;
the generated bindings are tracked. The builder stays at Go 1.25.1, the pin tee-node builds
with.

### SR-36 · Low · S — deployment key on a command line

`deploy/scripts/oracle-status.sh:59` runs `cast wallet address --private-key "$KEY"` to
derive an address for an `eth_call` `from`. The script defaults to the Coston2 RPC, so on
testnets this exposes the funded key to `ps` for the duration of the call. The other
`--private-key` uses (`deploy-oracle-local.sh`, `publish-local.sh`) are simulation-only.
Read `INITIAL_OWNER` or derive the address in Go.

### SR-37 · Low · S — predictable build path in `/tmp`

`deploy/scripts/oracle-cycle.sh:70-72,81` build the keeper into
`${TMPDIR:-/tmp}/usdx-oracle-bin` and execute it. On a shared host another user can
pre-create the directory and swap the binary between build and exec, running code with the
operator's environment. Build into `mktemp -d` or `$PROJECT_DIR/out/bin` (which
`start-services.sh` already does).

### SR-38 · Low · S — sibling build copies the whole checkout

`Dockerfile.siblings:22` copies `extension-examples/fce-usdx-oracle/` from a context of
`../../..` (`deploy/docker-compose.siblings.yaml:15`), where no `.dockerignore` exists; the
repository's own `.dockerignore` applies only to the default build. A `.env` holding testnet
keys lands in the build cache and any pushed intermediate image. Development path only, but
`USE_LOCAL_SIBLINGS=1` is the documented way to build against a develop simulation. Add a
`.dockerignore` at that context excluding `**/.env*`, `**/out/`, `**/.git`, or narrow the
`COPY` to the module's sources.

### SR-42 · Low · R — `EXTENSION_ID` validated by substring

`deploy/scripts/start-services.sh:293` greps the proxy's `/info` for the id; with
`EXTENSION_ID=1` any `1` in the JSON matches, so a proxy configured for another extension
never triggers the warning. Compare `.machineData.extensionId` with `jq`.

### SR-43 · Low · R — dead `CHAIN` branch; silent `.env` overwrite

`deploy/scripts/full-setup.sh:39` resets `CHAIN` to empty, so the "honor env CHAIN" branch
at `:72-74` can never run; `:54-55` copy `.env.<chain>` over a hand-edited `.env` that may
hold secrets not present in the tracked file. Use `CHAIN="${CHAIN:-}"`; back up or refuse to
overwrite a differing `.env`.

### SR-45 · Low · S — unpinned third-party image on the stack network

`deploy/scripts/oracle-status.sh:129` runs `curlimages/curl:latest` attached to the compose
network, where it reaches redis and the internal proxy port. Diagnostic only; pin by digest
or `docker exec` into an existing container.

**Status.** Fixed: the throwaway container is gone. `oracle-status.sh` reads the state out
of the proxy's `/info` like every other consumer (SR-56).

### SR-46 · Low · S — module-cache proxy on all interfaces

`deploy/scripts/lib/modcache-proxy.sh:34` binds `0.0.0.0` (a loopback bind is not reachable
from the Docker VM on macOS). For the build's duration the whole `GOMODCACHE/cache/download`
is listable on the LAN, private modules included. Bind to the Docker bridge address, or
restrict with a firewall rule on hosts where the cache holds private modules.

### SR-50 · Info — apt snapshot bootstrap without TLS verification

`Dockerfile:33` fetches `ca-certificates` with `Acquire::https::Verify-Peer=false` and
`Check-Valid-Until=false`, as the upstream reproducibility recipe does before certificates
exist. Release signatures still protect integrity; a replayed old snapshot is the residual
risk. Acceptable for this one package; worth a comment.

### SR-55 · Info — `MODE` and `INITIAL_OWNER` are launch-overridable

They are in the override list, so they can be changed at launch without a code-hash change,
while `GOVERNANCE_SIGNERS`, `GOVERNANCE_THRESHOLD` and `SIMULATED_TEE` cannot (SR-07).
Decide deliberately which variables are part of the attested identity.

**Status.** `MODE` was removed from the override list and the production Dockerfile now
bakes `MODE=0`, matching the tee-node image. `INITIAL_OWNER` remains overridable, as it is
in the tee-node image.

## F. Dependency scan

`govulncheck` (current toolchain) on both modules at the reviewed commit.

**Third-party modules.** No advisory is reachable from this code. The modules that carry
advisories are imported transitively and the affected packages are not called:
`filippo.io/edwards25519` v1.1.0 (GO-2026-4503, fixed v1.1.1), `golang.org/x/crypto`
v0.54.0 (`ssh` and `openpgp` advisories GO-2026-6355, -6354, -6303, -5932) and, in the
tools module, `github.com/klauspost/compress` (GO-2026-5841). A routine `go get -u` of these
is harmless.

**Standard library.** The scan runs against the host's Go 1.26.5 and reports the
advisories fixed in 1.26.6. The ones that touch this code's actual behaviour:

| Advisory | Package | Reached from | Relevance |
| --- | --- | --- | --- |
| GO-2026-6089 | `net/http` | the extension's `ListenAndServe` | Missing `ReadHeaderTimeout` on the h2c check; the same server that SR-06 leaves without timeouts |
| GO-2026-6090 | `crypto/tls` | TLS client to Hex and providers; local-stack proxy server | Post-handshake message limit |
| GO-2026-6218 | `net/url` | backing client, tools HTTP | Quadratic `resolvePath`; URLs are governance-published, low |
| GO-2026-5026 | `net/http` | backing client, teenode client, usdxprov | Punycode label handling |
| GO-2026-5972 | `encoding/asn1` | certificate parsing paths | Recursion depth |
| GO-2026-6091 | `html/template` | tools only, via the proxy's `init.Init` | Not in the enclave binary |

The relevant point for the enclave is not the host toolchain but the image: `Dockerfile:10`
builds with Go 1.25.1, so the production binary carries every standard-library fix released
since September 2025 as missing. See SR-18.

## Verified clean

- **Routing and channel separation.** Op type checked before dispatch; the action id must
  equal the cosigned instruction id; instruction-only and direct-only commands are refused by
  name over the other channel; an unknown type is refused rather than defaulted; method and
  path are enforced by the Go 1.22 mux patterns (conformance fixtures 09–11).
- **Signing domains.** Feed updates sign `abi.encode(bytes32 "TEE_ORACLE_FEED", chainId,
  dataHash)` with a zero chain id refused; readiness signs a 128-byte body led by
  `keccak("USDX_ORACLE_READINESS_V1")`; the TEE-id probe is a distinct constant. No direct
  action lets a caller choose the signed bytes. `SplitSignature`/`NormalizeV` enforce 65
  bytes and `v ∈ {27, 28}`.
- **Envelope verification.** `ValidateSignatureValues(v, r, s, true)` enforces low-s and
  `1 ≤ r, s < N`; 0/1 and 27/28 both normalised; the same signature in either encoding counts
  once; malformed and non-admin signatures are skipped; at most 16 recoveries. Floors ≥ 1,
  `floor ≤ threshold ≤ min(len(admins), 16)`, zero and duplicate admins rejected, both roles
  required. Chain id, non-zero teeId, domain word and payload hash are all bound, and the
  signature is checked over the canonically re-encoded body. Compile-time floors and
  `BackingRefs` cannot be altered by any payload field.
- **Secrets.** Memory-only; `Readiness` names refs only; every `ApplySecret` failure
  collapses to `ProvisionRejected`; `Open` reduces dial errors to `NotConfigured`; teenode
  errors are vocabulary values; decrypt plaintext is never logged. `SET_SECRET` logs no cause.
  Every error string on the SET_ENDPOINTS and SET_ADMINS paths was read; none carries a URL
  or value. `TestState_ExposesOnlyOperationalFields` and `TestReadiness_ExposesNoSecrets` pin
  the public surfaces.
- **Price arithmetic.** `ParseAmount` is a byte-level digit check (no signs, no exponent, no
  float path, grouping validated, 64-byte cap). `Compute` uses exact `big.Rat`; half-up
  rounding is correct; the clamp is applied after rounding; zero supply fails closed;
  `ValueFromBig` fails closed on overflow. `ComputeWithoutCash` cannot raise a price. No path
  signs a partial observation: `Observe` returns only health on any error and the poller keeps
  `last` only on success.
- **Backing client.** HTTPS enforced, redirects refused (so `X-API-Key` cannot be forwarded),
  5 s timeout, 64 KiB body cap, 3xx/4xx non-retryable, default TLS verification, no error
  text carrying URLs or values.
- **Concurrency.** Store `applyMu` serialises all `Apply*`; `mu` guards the fields
  `Readiness` and `Open` read; the poller's health map is replaced, never mutated after
  publication; every goroutine is joined; per-iteration loop variables under `go 1.25`.
- **Provider URLs** must be `https://` for PUBLIC and PRIVATE alike, so `ethclient.Dial`
  performs no I/O under the store's read lock and no websocket can leak.
- **Startup.** Missing `CHAIN_ID`/`EXTENSION_ID` panic; unresolved or zero TEE id after 60 s is
  fatal; a `ListenAndServe` error is fatal; `EXTENSION_ID` is normalised before tee-node reads it.
- **Scripts.** Every script under `deploy/scripts` uses `set -euo pipefail` except
  `test-unit.sh` (`set -uo`, deliberate) and the sourced helpers. No `eval`, no `curl | sh`,
  no `-k`, no `set -x`, no chain-clock or `evm_*` RPCs. Secrets are taken from the environment
  or a hidden prompt, never argv (SR-36 excepted); hand-off files hold calldata and fees only.
  `.gitignore` excludes `.env*`, the Coston TOMLs, `deploy/config/local/` and `out/`; the only
  tracked env file holds Hardhat keys.
- **Publication encoders.** Calldata round-trips against the sender ABI
  (`TestPublicationCalldataMatchesSenderABI`); zero claim-back refused.
- **Foundry deploy script.** Simulation-only, keys via `vm.envUint`, no production-mode
  switch by design.

## Not inspected, and assumptions

- Upstream contracts (`TeeOracleInstructionsSender`, `TeeOracleFeedStore`, registry) and
  their access control. Findings that say "the store rejects" rely on their documented behaviour.
- tee-node's `/decrypt` limits and the relay's re-delivery and deduplication of instructions
  (bears on SR-30).
- The tee-proxy's `/result` and `/queue` handler bodies beyond route registration and the
  handler wrapper (SR-01 is about the absence of authentication, confirmed there).
- The operator's actual VPC firewall rules and Confidential Space image version (SR-02).
- Generated bindings (`tools/pkg/contracts/*/autogen.go`) and test files beyond targeted reads.
- Whether the Coston hosts actually expose 6673 (SR-01 is about the default).
- The four Coston `.example` proxy TOMLs still carry `initial_signing_policy_offset`, which
  the develop proxy dropped; whether the pinned v0.0.18 proxy still accepts it was not checked.

## Suggested order of fixes

1. **SR-01** bind the proxy's internal port to loopback (one-line compose change).
2. **SR-02** bind the extension to loopback and trim `EXPOSE`.
3. **SR-03, SR-04, SR-16** keeper: filter updates by signer, stop the wrapper forcing `-p`,
   bind readiness reports to a machine. These three together decide whether a multi-machine
   fleet can publish at all.
4. **SR-07** launch-policy label and deployment steps, before the first Confidential Space deploy.
5. **SR-13, SR-12, SR-29** make the cash source provisionable and rotatable.
6. **SR-05** namespace the upload script's variables and validate `SecretRef` in the store.
7. **SR-14, SR-15** remove the well-known defaults and verify the code hash before allowing it.
8. **SR-06, SR-10, SR-11, SR-17** timeouts and body limits on every HTTP path, in both modules.
9. **SR-08, SR-09** threshold floor and chain-id check on chain groups.
10. **SR-18** pins: distroless digest, builder Go, proxy build; re-record the code hash.

The remaining low and info items are individually small and can be folded into the files
they touch as those are edited.
