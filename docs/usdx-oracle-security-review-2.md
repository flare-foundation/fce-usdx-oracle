# USDX Oracle — Security Review II: forcing, blocking and stopping the feed

**Status:** review report · **Date:** 2026-09-15 · **Reviewed at:** commit `98188f6` (branch `tilen/improvements`), plus one fix made during the review (T-08, uncommitted at the time of writing)

A second full review of the repository, one week after
[usdx-oracle-security-review.md](usdx-oracle-security-review.md), asked from the attacker's
side: can anyone **force a wrong value** onto the feed, **block or delay a value**, or **stop
the oracle** by exhausting memory or otherwise denying service? Every finding was verified
against the code at the commit above; the upstream contracts were read at
`flare-smart-contracts-v2` `585e1276` (branch `tee-oracle-extension`) and tee-node at the
pinned module `v0.0.27-0.20260914073453-9c6bc46a5ee6`. Where a claim rests on something not
verified here, the finding says so.

## Contents

- [Summary](#summary)
- [Scope and method](#scope-and-method)
- [Actors and what they hold](#actors-and-what-they-hold)
- [Findings table](#findings-table)
- [A. Wrong value](#a-wrong-value)
- [B. Blocked or delayed value](#b-blocked-or-delayed-value)
- [C. Stopping the oracle: memory and denial of service](#c-stopping-the-oracle-memory-and-denial-of-service)
- [D. Operator tooling, deployment and configuration](#d-operator-tooling-deployment-and-configuration)
- [E. Informational](#e-informational)
- [Status of the previous review's findings](#status-of-the-previous-reviews-findings)
- [Verified clean](#verified-clean)
- [Not inspected, and assumptions](#not-inspected-and-assumptions)
- [Suggested order of fixes](#suggested-order-of-fixes)

## Summary

**Wrong value.** Nobody outside the data sources can make the machine sign a wrong price. The
proxy, the keeper, the VM operator, a holder of the direct-channel key and any on-chain caller
are all held off by the TEE signature, the chain-bound digest, the configuration commitments
and the channel separation, all re-verified in this pass. The data sources can. Two providers
colluding in any chain group, or the single provider of the Songbird group, which every shipped
configuration runs at a threshold of one, set the supply term at will, and with it the price
anywhere in `[0, 1]` (T-01). A compromised Hex bonds endpoint sets the price anywhere below par
(T-02). Nothing bounds the price from below, nothing compares an observation with the previous
one, and the outvoted honest provider is the one marked `disagree`. With the store's submission
policy at one required signature, whoever submits chooses which machine's value lands, so a
fleet of several machines does not yet corroborate anything (T-05).

**Blocked value.** This is the cheap attack. One provider that stalls, or that claims an
inflated head, fails every observation of an otherwise healthy group (T-04, T-10); after ten
minutes of that the machine refuses to sign, and the on-chain feed freezes with an ageing
timestamp, never a wrong value. Any registered machine's proxy can hang the keeper, because it
polls with no timeout and no body limit (T-07); any machine owner who pauses a machine on the
keeper's static target list stops the keeper's requests (T-11); one backing admin can wedge
every future configuration publication (T-15). A quorum that agrees on a future header
timestamp makes one observation signable forever (T-09).

**Stopping the oracle.** One malicious RPC provider can exhaust the enclave's memory with an
oversized response before the observation deadline cancels the read (T-03); the enclave keeps
its provisioned secrets in memory, so a crash costs a full re-provisioning, not just a restart.
The keeper can be hung or exhausted by any proxy it polls (T-07), and its fee, gas price and
gas limit come from its RPC with no ceiling (T-06). The VM's one exposed port carries
tee-node's unauthenticated configuration server, through which the proxy can be repointed, and
the README's advice to omit the governance variables leaves the governance endpoint open (T-16).
The extension's own HTTP surface behind tee-node is bounded and was not found exploitable.

**Found and fixed during the review.** The pipeline commit of this morning had dropped the
compose-file list from `start-services.sh`, which broke every local flow (T-08). The list is
restored in the working tree.

## Scope and method

Five area passes, each reading the code rather than the documentation: the observation
pipeline (`internal/observation/**`, `internal/config`), the extension surface and provisioning
(`internal/extension`, `internal/provisioning`, `internal/wire`, `internal/teenode`,
`pkg/server`, `cmd/docker`, `cmd/start-tee`), the operator tooling (`tools/**`,
`cmd/usdxprov`), deployment and CI (`Dockerfile*`, `deploy/**`, `.gitlab-ci.yml`), and the
on-chain protocol (the upstream store, sender, verification and manager contracts, and how the
enclave and keeper use them). The highest-impact claims were then re-read independently. The
three questions in the title were asked of every actor in the next section.

## Actors and what they hold

| Actor | Holds | Can reach |
| --- | --- | --- |
| RPC provider | one URL in a governance-published group | the enclave's supply reads, quorum 2 of 3 (Songbird 1 of 1) |
| Hex Trust endpoints | the bonds figure; the cash figure behind a provisioned key | the enclave's reserve reads |
| Proxy operator | `DIRECT_API_KEY`; sees every action and result | the direct channel; result substitution and withholding |
| VM operator | the launch environment listed in the image label; port 5500 | tee-node's configuration server |
| Machine owner (any allowlisted) | a registered machine and its proxy URL | the keeper's poll and readiness targets; pausing their machine |
| Backing admin / provider admins | admin keys at the published thresholds | `SET_SECRET` |
| Governance | the timelocked publication keys | endpoints, admin sets, submission policy |
| Keeper operator | a funded hot wallet, an RPC endpoint | requests and submissions |
| Anyone | gas | `requestFeedUpdate`, `pushEndpoints`, `submitFeedUpdates` |

Data providers (who cosign instructions), Confidential Space attestation and the timelocked
governance are trust assumptions of the platform and are not treated as attackers, though the
consequences of their failure are noted where relevant.

## Findings table

Severity reflects impact on the feed under the actor model above. `[wrong]`, `[block]`,
`[stop]` and `[ops]` mark which question a finding answers.

| ID | Sev | Tags | Title | Prior |
| --- | --- | --- | --- | --- |
| T-01 | High | wrong | One chain group's quorum sets the price anywhere in [0, 1]; Songbird runs at 1 of 1 | SR-08, SR-27 |
| T-02 | High | wrong | A compromised Hex bonds endpoint sets the price anywhere below par; metric never checked | SR-27, SR-28 |
| T-03 | High | stop | RPC response bodies are unbounded; one provider can exhaust the enclave's memory | SR-11 |
| T-04 | Medium | block | One stalled or slow provider fails every observation | SR-10 |
| T-05 | Medium | wrong | `requiredSignatures = 1`: the submitter, not the fleet, chooses the stored value | — |
| T-06 | High | ops | Keeper fee, gas price and gas limit come from the RPC with no ceiling | SR-19 |
| T-07 | High | block, stop | Keeper polls proxies with no timeout and no body limit; one proxy stalls the fleet | SR-17 |
| T-08 | High | ops | `start-services.sh` lost its compose-file list (regression, fixed) | — |
| T-09 | Medium | block | A quorum-agreed future header timestamp makes an observation signable forever | SR-53 |
| T-10 | Medium | block | An inflated head claim from one provider forces `SupplyNoQuorum` | SR-24 |
| T-11 | Medium | block | The keeper's static target list stalls requests when one machine leaves PRODUCTION | — |
| T-12 | Medium | block | Unbounded request-receipt wait; nonce gaps recorded but never acted on | SR-17 |
| T-13 | Medium | block, ops | Readiness report signer is never compared with the machine asked | SR-16 |
| T-14 | Medium | ops | The keeper's one direct-channel key is posted to every URL the registry lists | — |
| T-15 | Medium | block | One backing admin can wedge every future `SET_ENDPOINTS`; cash-URL rotation deadlocks | SR-12, SR-29 |
| T-16 | Medium | stop, ops | Unauthenticated configuration server on the exposed port; governance left open when omitted | SR-07 |
| T-17 | Medium | ops | `use-chain.sh` writes long-lived keys into world-readable `.env` files | — |
| T-18 | Medium | ops | `.env` values are written double-quoted and sourced: command injection via a credential | — |
| T-19 | Medium | ops | Proxy internal API on all interfaces by default; host mode cannot bind loopback | SR-01 |
| T-20 | Medium | ops | Reproducibility: kaniko builds without timestamp rewriting, against the documented recipe | — |
| T-21 | Low | wrong | No chain-id verification; Flare and Songbird share the token address | SR-09 |
| T-22 | Low | block | Supply and backing run sequentially under one deadline; window check dead | SR-26 |
| T-23 | Low | ops | Anyone can front-run the keeper's submission; the keeper pays gas for the revert | — |
| T-24 | Low | ops | Permissionless pushes rewrite the dispatched record and hide stragglers | — |
| T-25 | Low | wrong | The digest does not bind the store address; a fresh store accepts an old public update | — |
| T-26 | Low | ops | Direct-channel refusals write to the attested `/state` counters | SR-20 |
| T-27 | Low | ops | `SecretRef` is unrestricted and reaches two public surfaces | SR-05 |
| T-28 | Low | stop | Extension `http.Server` has no timeouts and no body cap | SR-06 |
| T-29 | Low | ops | `EXTENSION_ID=0` accepted at startup, refused only at signing | — |
| T-30 | Low | ops | Readiness correlator may be zero | SR-21 |
| T-31 | Low | ops | Handlers run on a background context past tee-node's budget | SR-22 |
| T-32 | Low | ops | Envelope replay window after a restart | SR-30 |
| T-33 | Low | block | `resolveURL` is a prefix check; `Open()` is all-or-nothing per group | SR-31, SR-32 |
| T-34 | Low | ops | Keeper `-feedId` is used for the paid request but screening is pinned to USDX | SR-40 |
| T-35 | Low | wrong | A result is not bound to its instruction; a proxy can substitute an earlier update | — |
| T-36 | Low | ops | Private keys on command lines, now on live-chain paths | SR-36 |
| T-37 | Low | ops | Direct API key passed on argv where the tools already read it from the environment | — |
| T-38 | Low | ops | Proxy-supplied `Log` strings reach logs and records unquoted and unbounded | — |
| T-39 | Low | ops | Predictable `/tmp` build and hand-off paths | SR-37 |
| T-40 | Low | ops | `usdxprov -tee` optional; scheme unchecked | SR-33 |
| T-41 | Low | ops | `allow-tee-version` still allows the self-reported hash by default | SR-15 |
| T-42 | Low | ops | Sibling development build copies the whole checkout, `.env` included | SR-38 |
| T-43 | Low | ops | Secrets in compose `environment:` visible through `docker inspect` | — |
| T-44 | Low | ops | Hardhat key remains the tools' silent fallback | SR-14 |
| T-45 | Low | ops | Indexer database root/root on the stack network; it is the proxy's policy source | — |
| T-46 | Low | ops | `upload-rpc-keys.sh --all` is a no-op against the shipped config schema | — |
| T-47 | Low | ops | Machine-chosen refs used unsanitised as variable names and file paths | SR-05, SR-35 |
| T-48 | Low | ops | Mutable image tags and CI references around the attested image | SR-18 |
| T-49 | Low | ops | `MODE` and `SIMULATED_TEE` are set separately and never cross-checked | — |
| T-50 | Info | — | `CHAIN_URL` in the launch policy is read by nothing | — |
| T-51 | Info | — | Smaller carry-overs and documentation gaps | various |

## A. Wrong value

### T-01 · High · One chain group's quorum sets the price anywhere in [0, 1]; Songbird runs at 1 of 1

**Where.** `internal/observation/supply/supply.go:308-324, 382-440` (quorum vote on
`totalSupply` and `decimals`), `internal/observation/price/price.go:41-54` (no floor),
`internal/wire/feedupdate/feedupdate.go:218-227` (`ValueFromBig(0)` accepted),
`internal/observation/poller/poller.go:112-131` (`record` overwrites without comparison),
`internal/provisioning/store/store.go:708` (threshold floor is 1);
`deploy/config/endpoints/public-only.json` and `coston2.json`: the `songbird` group has one
endpoint and threshold 1, and so does the set published on Coston today.

**Scenario.** The Songbird supply term is read from one host, `songbird-api.flare.network`.
Whoever controls that host's answers, or two of the three providers in the Flare or Ethereum
group, reports a fresh header timestamp (defeating `MaxBlockAge`), `decimals = 0` and any
`totalSupply`. The quorum is met, `Total` sums the figure in, and the price is signed anywhere
from `0.00000000` (overstated supply) up to par (understated supply). The outvoted honest
provider is marked `disagree` while the liars stay `ok`. Supply is a plain sum, so the weakest
group bounds the whole feed. Nothing compares the observation with the previous one.

**Fix.** Enforce a chain-group threshold of at least 2 in `store.validate` and `supply.Validate`
and publish at least three Songbird providers; pin the expected `decimals` per chain in
`config.Chain` and reject a quorum answer that differs; add a deviation guard against the
previous accepted observation, or an absolute floor, routed to `PriceImplausible`, which today
is raised nowhere.

### T-02 · High · A compromised Hex bonds endpoint sets the price anywhere below par; metric never checked

**Where.** `internal/observation/backing/backing.go:129, 209-252` (`metric_name` decoded and
never compared; `value` accepted as parsed), `internal/observation/decimal/decimal.go:18, 33-35`
(64-character cap is the only magnitude bound), `price.go:41-54, 60-69`.

**Scenario.** The bonds endpoint answers `{"value":"0","last_update":"<now>"}`. With cash
configured, `Compute` publishes `cash/S`; with cash also at zero it publishes 0. Any understated
value, say 0.98 of the real reserve, is signed and cannot be told from a real depeg. Any metric
the same host serves with a `value` field is accepted as reserves. The cash-less path is
stricter and fails closed below par.

**Fix.** Compare `metric_name` with a per-source expectation; add a plausibility band, either a
maximum ratio change per observation or a floor on backing versus supply, routed to
`PriceImplausible`.

### T-05 · Medium · `requiredSignatures = 1`: the submitter, not the fleet, chooses the stored value

**Where.** `TeeOracleFeedStore.sol:582` (`count >= requiredSignatures` is the only lower bound),
`:233-235`, `:503-554`; `ITeeOracleFeedStore.sol:363-367`; the shipped policy is
`requiredSignatures 1, maxSpreadBIPS 100, maxSpreadAbsolute 0` in every chain-config;
`TeeOracleInstructionsSender.sol:157-183` (anyone may address a single machine); the signed
results are world-readable on the proxies.

**Scenario.** Fleet of three, one faulty. The attacker requests from the faulty machine alone,
or reads all three answers off the proxies, and submits a one-element batch before the keeper.
The store accepts it, the spread and outlier checks being vacuous at one element, and the
keeper's batch reverts `NotNewer`. Repeatable every round for one machine fee plus gas. Even
with every machine honest the attacker can always publish the extreme answer instead of the
median. At two required signatures a bad machine shifts the truncated average by up to half the
allowed spread or stalls the pair; at three the median stays inside the honest range but a bad
extreme can stall the batch.

**Fix.** Operational: raise `requiredSignatures` to the fleet size while the fleet is small, and
to `2f+1` once it is not, as soon as that many PRODUCTION machines run the latest generation.
The contract documents exactly this. This is a policy setting, not a code defect; the keeper is
correct under any value of it.

### T-21 · Low · No chain-id verification; Flare and Songbird share the token address

**Where.** `supply.go:45-48` (`Caller` exposes only `HeaderByNumber` and `CallContract`);
`internal/config/config.go:232, 234` (identical `Token`). A group misconfigured, or steered by
a colluding quorum, at the other chain's RPCs counts one supply twice and drops the other.
**Fix.** Add `ChainID` to `config.Chain`, call `eth_chainId` per provider in `Read`, mark
mismatches `down`.

### T-25 · Low · The digest does not bind the store address

**Where.** `SignedPayload.sol:36-44`, `TeeOracleFeedStore.sol:536, 549-552`,
`internal/wire/feedupdate/feedupdate.go:296-313`. A redeployed or second store for the same
chain, extension and feed starts at `observedAt = 0` and accepts the oldest public update
carrying the current configuration hashes until the first honest batch lands.
**Fix.** Operational: land a fresh batch in the same deployment sequence. Contract-side, binding
`address(this)` into the digest closes it at the cost of a wire-format change.

### T-35 · Low · A result is not bound to its instruction; a proxy can substitute an earlier update

**Where.** `tools/pkg/feedupdate/feedupdate.go:46-58` (no instruction id in the signed struct),
`tools/pkg/keeper/cycle.go:145, 170` (whatever is filed under the id is taken); contrast
`readiness.go:82`, where the binding exists. With one machine, the proxy can file any earlier
update whose `observedAt` is still above the store's stamp and choose which recent honest price
gets published. With two or more honest machines the newest-group rule reduces this to
withholding.
**Fix.** Keeper-side, reject an `observedAt` older than the request transaction's block
timestamp; the receipt is at hand. Enclave-side, carrying the instruction id in the signed
update would close it fully but changes the wire format.

## B. Blocked or delayed value

### T-04 · Medium · One stalled or slow provider fails every observation

**Where.** `supply.go:192-211, 259-272, 362-376` (every round waits for all providers, then
checks `ctx.Err()`), go-ethereum `rpc/http.go:162-164` (`new(http.Client)`, no timeout; only
the 20-second observation context bounds it), `rpc/http.go:184-187, 195` (the body is drained
to EOF before returning).

**Scenario.** A provider accepts the connection and never answers, or answers each call in five
seconds: four sequential rounds per chain exceed `ObservationDeadline` and every tick ends
`SupplyTimeout`, although the other two providers met quorum at once. A well-formed reply
followed by an endless trickle of whitespace has the same effect through the drain. After five
ticks `GET_FEED` publishes nothing.

**Fix.** A per-provider `context.WithTimeout` of a few seconds inside each goroutine, or a
per-call `http.Client.Timeout` through `rpc.WithHTTPClient`; return as soon as `Quorum`
identical answers are in.

### T-07 · High · Keeper polls proxies with no timeout and no body limit; one proxy stalls the fleet

**Where.** `tools/pkg/keeper/cycle.go:546` (`http.Get`, default client), `:558`
(`json.NewDecoder(resp.Body)` unbounded), `:477-492` (proxies polled sequentially; the deadline
is checked only between rounds), `:503-507` (the context is observed only while sleeping);
`tools/pkg/keeper/readiness.go:151-159` (`http.DefaultClient.Do` on the keeper's signal-only
context); `tools/pkg/fccutils/tee_calls.go:23, 30, 89, 105` (the same pattern in `register-tee`,
`allow-tee-version`, `set-governance`, `verify-tee`, `retire-tee`); `cmd/usdxprov/main.go:506,
563, 586, 604`. Contrast `tools/pkg/fccutils/attested_state.go:42, 60`, which does it right.

**Scenario.** The poll targets are the URLs the registry publishes, so any allowlisted machine
owner chooses one. A proxy that accepts the connection and never answers blocks `fetchResult`
indefinitely; because proxies are polled in sequence, one such proxy stalls the whole round,
`PollTimeout` is never reached and SIGTERM does not interrupt it. A proxy streaming an endless
JSON body grows the keeper's memory without bound. The readiness POST can hang before any fee is
paid.

**Fix.** One `http.Client{Timeout}` and `io.LimitReader(resp.Body, 1<<20)` for every proxy read;
`NewRequestWithContext` with a per-request deadline; poll proxies concurrently with per-proxy
timeouts.

### T-09 · Medium · A quorum-agreed future header timestamp makes an observation signable forever

**Where.** `supply.go:153` (`time.Since(blockTime) > MaxBlockAge` is false for a future stamp;
no upper bound), `internal/observation/observation.go:255-273` (`homeBlockTime` rejects only
non-positive), `observation.go:123-125` and `poller.go:146` (a negative age is never stale),
`poller.go:192` (`ClockBehindChain` does not fire), `poller.go:234-237` (`WouldSign` true).
Contrast `backing.go:248`, which rejects a reserve stamp more than a minute ahead.

**Scenario.** A colluding Flare quorum serves one observation whose pinned header is ten years
ahead, with any supply figure, then goes dark. Every later observation fails, `record` keeps the
last one, both staleness checks pass indefinitely, and the machine signs that frozen price for
every request.

**Fix.** Reject a `blockTime` more than about a minute ahead of the wall clock in `Read`
(`SupplyMalformed`); treat a negative age beyond a tolerance as a failure in `Latest`.

### T-10 · Medium · An inflated head claim from one provider forces `SupplyNoQuorum`

**Where.** `supply.go:230-231` (the pin is the Quorum-th highest head), `:240-244` (the honest
lower node is marked `lagging`), `:409-417, 435-437` (a tie fails). Honest heads `H` and `H-1`,
one liar at `H+1000`: the pin is `H`, the `H-1` node cannot serve it, the vote at `H` is one
honest against one forged, and the round fails. On chains with blocks every second or two,
honest public providers are frequently one block apart, so a single provider turns a healthy
group into a flapping one and the health map blames the honest node.
**Fix.** On a failed vote at the pinned head, retry at the next lower distinct head; or consider
only heads corroborated by at least two providers.

### T-11 · Medium · The keeper's static target list stalls requests when one machine leaves PRODUCTION

**Where.** `tools/cmd/keeper/main.go:131-137` (`-tee` fixed for the process),
`tools/pkg/keeper/keeper.go:113-120` and `fleet.go:82-99` (the per-tick refresh updates
polling URLs only), `tools/pkg/utils/instructions.go:118` (the whole list is sent);
`Instructions.sol:88-100` (the diamond reverts for the whole batch if any target is not
PRODUCTION); `TeeOracleInstructionsSender.sol:177-178` (any lagging target reverts).

**Scenario.** Any machine owner pauses their own machine, or a third party suspends one whose
availability check lapsed. Every later `requestFeedUpdate` fails gas estimation, the keeper
records `request_failed` and the feed stops until an operator edits `-tee`, although the store
would accept updates from the remaining machines.

**Fix.** Each cycle, intersect the target list with `getActiveTeeMachines` and
`isTeeIdConfigured`, request from the survivors, record the dropped, and skip when fewer than
`requiredSignatures` remain.

### T-12 · Medium · Unbounded request-receipt wait; nonce gaps recorded but never acted on

**Where.** `tools/pkg/utils/instructions.go:417-420` (`bind.WaitMined(context.Background(), …)`),
called from `cycle.go:133`; `opts.Nonce` nil everywhere; `cycle.go:114` and `keeper.go:80-85`
(the gap is only stored). An underpriced or swallowed request transaction hangs the loop keeper
forever with no record emitted; in cron mode each tick broadcasts one more request at the next
nonce, and when the gap clears every queued request mines and pays its fee in a burst.
**Fix.** Wait with the cycle context plus a timeout, as the submit path already does; record
`not_mined`; skip requesting while `PendingNonceGap > 0`.

### T-13 · Medium · Readiness report signer is never compared with the machine asked

**Where.** `tools/pkg/keeper/readiness.go:82-86` (only the instruction id is checked),
`:99-124` (`wouldRefuse` iterates URLs with no machine id); `report.Signer` is read nowhere
except `oracle-watch`, which records it; `tools/cmd/send-endpoints/main.go:306-315` and the
admins twin trust the reported hash from any signer.

**Scenario.** A compromised proxy answers `GET_READINESS` with a report signed by any key. With
one machine, or under `PROXY_OVERRIDE`, `wouldSign:false` makes the keeper skip every paid
request; with `send-endpoints -wait` a forged hash prints "Confirmed" for a configuration the
enclave never applied. The feed's correctness is unaffected; its liveness and the operator's
signals are.

**Fix.** Carry the machine id with each proxy URL, which `fleet.Machine` already does, and treat
`report.Signer != TeeID` as "unavailable, request anyway"; require an expected id under
`PROXY_OVERRIDE`.

### T-15 · Medium · One backing admin can wedge every future `SET_ENDPOINTS`; cash-URL rotation deadlocks

**Where.** `internal/provisioning/store/store.go:190-197` (`ApplyEndpoints` rejects the
publication when any stored secret does not fit it), `:428-435` (`ApplySecret` skips
`verifySecret` while no endpoints are installed), `:447-448, :502-506`;
`internal/config/config.go:186` (`MinBackingThreshold = 1`).

**Scenario.** After a restart, one backing admin sends a malformed `hex_api_key` or a garbage
`hex_cash_url` blob; it is stored unchecked. Every `SET_ENDPOINTS` governance then publishes is
refused as `ProvisionRejected` until that role overwrites the value. Independently, rotating the
cash-URL commitment is refused because the stored blob no longer matches, and the new blob is
refused against the old publication; the machine signs with a stale hash until a restart and a
full re-provisioning. Configuration denial of service, not price control.

**Fix.** Run the blob-shape and credential checks unconditionally in `ApplySecret`; in
`ApplyEndpoints`, evict secrets that no longer fit and list them as missing instead of refusing
the publication.

### T-22 · Low · Supply and backing run sequentially under one deadline; window check dead

**Where.** `observation.go:136` then `:142`; `backing.go:31, 142` and `config.go:136` (two
attempts of five seconds); `config.go:132-134`. A slow but successful supply phase leaves a
healthy Hex endpoint too little time.
**Fix.** Run the two phases concurrently, which also makes `checkWindow` meaningful.

### T-33 · Low · `resolveURL` is a prefix check; `Open()` is all-or-nothing per group

**Where.** `store.go:803, 812-818` (no `net/url` parse; loopback, link-local and userinfo hosts
pass), `store.go:609-618` (one unresolved provider fails the whole observation even when the
threshold is met without it).
**Fix.** Parse with `net/url`, require a host and no userinfo; skip unresolved providers and
fail only below `Threshold`.

## C. Stopping the oracle: memory and denial of service

### T-03 · High · RPC response bodies are unbounded; one provider can exhaust the enclave's memory

**Where.** `internal/provisioning/store/store.go:615` (`ethclient.Dial(url)` with no client
option), go-ethereum v1.17.4 `rpc/http.go:162-164` (`new(http.Client)`), `rpc/http.go:199`
(`json.NewDecoder(respBody).Decode(&resp)` into a `json.RawMessage`), `rpc/http.go:253`
(`buf.ReadFrom(resp.Body)` on a non-2xx status, also unbounded). Contrast `backing.go:27, 193`
(64 KiB limit) and `teenode.go:33, 145` (1 MiB).

**Scenario.** One provider answers `eth_call` with a multi-gigabyte `"result":"0x…"`. The whole
token is buffered, then hex-decoded, before `supply.go:368` checks that the answer is one word.
Up to nine such calls run concurrently per round, and the observation deadline cancels the read
only after twenty seconds. Neither the Dockerfile nor the compose files set a memory limit or
`GOMEMLIMIT`. A crash loses the provisioned secrets, which live in memory, so recovery is a
restart plus a full `SET_SECRET` round, and on Confidential Space a workload exit may end the
VM.

**Fix.** Dial with `rpc.DialOptions(ctx, url, rpc.WithHTTPClient(client))`, where the client
has a timeout and a transport that wraps every response body in `http.MaxBytesReader` at one
mebibyte; headers and single-word call results are a few hundred bytes.

### T-16 · Medium · Unauthenticated configuration server on the exposed port; governance left open when omitted

**Where.** `Dockerfile:96` (`EXPOSE 5500`; Confidential Space opens the ports the image
declares, subject to the VPC firewall); tee-node `internal/settings/config.go:71` (binds `:5500`
on all interfaces), `:81-85` (`POST /proxy`, `/initial-owner`, `/extension-id`, `/chain-id`,
`/governance`, no authentication), `:189-217` (`/proxy` has no one-shot guard),
`internal/node/node.go:361-373` (when neither `GOVERNANCE_SIGNERS` nor `GOVERNANCE_THRESHOLD`
is set, governance stays unset and `/governance` stays open); `deploy/README.md:221` tells the
VM operator to omit them "for the owner alone, threshold 1", a default that exists only in the
compose file, while `set-governance` registers owner/1 on chain.

**Scenario.** Anyone who reaches port 5500 repoints the node at a proxy they run: no forged
price or secret, since instructions and envelopes still need their signatures, but the feed is
denied and the replay window is theirs. With the README's launch environment the first
`POST /governance` wins, the machine attests an attacker's governance hash, and registration
fails; even without an attacker, registration fails on the hash mismatch.

**Fix.** Always pass `GOVERNANCE_SIGNERS` and `GOVERNANCE_THRESHOLD` and correct the README;
restrict 5500 in the VPC firewall to the proxy host, or to nothing, since production sets
`PROXY_URL` in the environment. Upstream, `/proxy` should be one-shot when it came from the
environment. The port and the server are inherited from tee-node's image.

### T-28 · Low · Extension `http.Server` has no timeouts and no body cap

**Where.** `internal/extension/extension.go` (`NewExtension`, `actionHandler`). Only tee-node
reaches the loopback listener and bounds actions at 10 MB, so the residual is another process on
the same host in the `start-tee` stack.
**Fix.** `ReadHeaderTimeout`, `ReadTimeout`, `IdleTimeout`, and `http.MaxBytesReader` just above
tee-node's action size.

## D. Operator tooling, deployment and configuration

### T-06 · High · Keeper fee, gas price and gas limit come from the RPC with no ceiling

**Where.** `tools/pkg/utils/instructions.go:62-86, 108-118`, `tools/pkg/keeper/cycle.go:400-406`,
`instructions.go:270-277`. `operationFee` attaches whatever `GetOperationFee` or
`GetDefaultFee` returns, times the target count; every `TransactOpts` leaves gas limit, fee cap
and tip to `eth_estimateGas` and `eth_maxPriorityFeePerGas` from the same RPC. A lying RPC, or
a mis-set fee row, has one transaction per cycle signed at an arbitrary value, up to the wallet
balance; the single-flight loop bounds the burn rate by `-interval` only.
**Fix.** A `-maxFeeWei` flag that records `request_skipped` when the quote exceeds it, and
explicit `GasFeeCap`, `GasTipCap` and `GasLimit` ceilings on both transactors.

### T-08 · High · `start-services.sh` lost its compose-file list (regression, fixed)

**Where.** `deploy/scripts/start-services.sh:313, 339, 379` used `"${COMPOSE_FILES[@]}"`, and
commit `b6a91d6` had removed the block that assigned it. Under macOS bash 3.2 with `set -u` the
script aborted with "unbound variable"; under newer bash it ran `docker compose up` with no `-f`.
Every documented flow goes through it. CI's `scripts` job runs only `bash -n`, which cannot see
an unassigned array; the component's shellcheck job can, and its run on this commit is what would
have caught it in CI.
**Status.** Restored in the working tree: base file, sibling overlay, live overlay, mirroring
`stop-services.sh:106-117`.

### T-14 · Medium · The keeper's one direct-channel key is posted to every URL the registry lists

**Where.** `tools/pkg/keeper/readiness.go:101` (targets are the configured proxy plus every
registry URL), `:157` (`X-API-Key`), `tools/pkg/keeper/fleet.go:52-66` (URL taken verbatim,
scheme unchecked). Any allowlisted owner registering a machine with an `http://` collector
address receives the keeper's key in clear on the next refresh, and per-proxy keys become one
shared secret.
**Fix.** Send readiness only to the configured proxy, or hold per-machine keys; refuse non-https
URLs for non-loopback hosts.

### T-17 · Medium · `use-chain.sh` writes long-lived keys into world-readable `.env` files

**Where.** `deploy/scripts/use-chain.sh:119, 181, 195-198, 230, 250-254`,
`publish-local.sh:87-88`, `deploy-oracle.sh:303-311`; no `umask` or `chmod` anywhere in
`deploy/scripts`; on disk `.env` and `.env.coston` are `-rw-r--r--`. `.env.example:114-116` and
`usdx-oracle-production-setup.md:350-351` say never to put real credentials in `.env`, and
`use-chain.sh flare` does the same on a mainnet. The deployer key is also written as both the
backing admin and the first provider admin.
**Fix.** `umask 077` in every writer, or `chmod 600` after each write; refuse to write
`USDXPROV_*` and `HEX_*` when `chain_is_mainnet`; reconcile the documentation.

### T-18 · Medium · `.env` values are written double-quoted and sourced: command injection via a credential

**Where.** `use-chain.sh:145, 161` (`quoted="\"$value\""`), `:250` (`printf '%s="%s"\n'` for
the Hex values); the file is sourced at `:227` in the same run and by `lib/env.sh:17`,
`start-services.sh`, `full-setup.sh` and `import-upstream-addresses.sh`. Verified: a value
containing `$(…)` executes as the operator when the file is sourced; the single-quoted form does
not.
**Fix.** Write single-quoted with `'\''` escaping, or validate the character set before writing.

### T-19 · Medium · Proxy internal API on all interfaces by default; host mode cannot bind loopback

**Where.** `deploy/docker-compose.yaml:28` (`${EXT_PROXY_INTERNAL_BIND:-0.0.0.0:6673}`), no
override in the live overlay; tee-proxy v0.0.24 `internal/server/internal.go:75, 131-132`
(`POST /result`, `POST /queue/{id}`, unauthenticated); host mode renders 6683/6684 and the proxy
binds all interfaces; the host TEE's configuration server binds `:5521`. The template `.env`
pins the internal bind to loopback, so a fresh `.env.<chain>` is safe; a file predating the
variable, a bare `docker compose up`, or host mode is exposed.
**Fix.** Default to `127.0.0.1` in the compose file; in host mode bind the internal API and the
node's configuration port to loopback or firewall them.

### T-20 · Medium · Reproducibility: kaniko builds without timestamp rewriting, against the documented recipe

**Where.** `.gitlab-ci.yml` `.kaniko` (`--snapshot-mode=redo`, no `--reproducible`,
`--no-push`); publication is delegated to `component@2`, a floating major of a private
component; `REPRODUCIBILITY.md` says CI uses BuildKit's `rewrite-timestamp` and the verification
recipe uses buildx with it; `Dockerfile:73` creates `/app` in the final stage at build time.
An auditor following the document may not reproduce the registered image id, and the id may
drift between CI runs.
**Fix.** Confirm what the component passes and normalises, pin its version, and publish the
expected digest per tag; keep one recipe and state it once.

### T-23 · Low · Anyone can front-run the keeper's submission; the keeper pays gas for the revert

**Where.** `TeeOracleFeedStore.sol:197-202, 234`; `cycle.go:406-409, 421-426` (a
post-broadcast revert is reported as unexplained rather than classified `race_lost`). The feed
still advances; the keeper loses the submit gas and logs a misleading line each cycle. With
T-05 the front-runner also chooses the subset.
**Fix.** After a post-broadcast revert, re-read `observedAt` and classify `race_lost` when the
store is at or past the batch's stamp.

### T-24 · Low · Permissionless pushes rewrite the dispatched record and hide stragglers

**Where.** `TeeOracleInstructionsSender.sol:210-221, 627-639, 812-835`;
`store.go:182-185, 233-236` (the enclave refuses a same-version re-push). A push to an offline
machine removes it from `get*PushTargets`, `requestFeedUpdate` accepts it as a target, and its
answers carry the old hash. Each refused re-push bumps the public failure counter.
**Fix.** Reconcile with the signed readiness hashes rather than the view.

### T-26 · Low · Direct-channel refusals write to the attested `/state` counters

**Where.** `internal/extension/result.go:38-45`; contradicted by the comment at
`readiness.go:14-16`. A key holder posting malformed `GET_READINESS` increments `failureCount`
and sets `lastErrorClass` in the state tee-node folds into `/info`.
**Fix.** Count only `GET_FEED` outcomes, or keep a separate direct-refusal counter.

**Status.** Moot: `GET /state` now carries the empty state and the extension keeps no
counters a request can move (2026-09-16). The signed readiness report is the one surface.

### T-27 · Low · `SecretRef` is unrestricted and reaches two public surfaces

**Where.** `store.go:761-794` (no charset or length check), `:556-561, 525-531`,
`internal/extension/extension.go` (`actionHandler`), `poller.go:221-222`; the envelope caps a ref at 64 bytes
only on the `SET_SECRET` side. A governance publication naming a public endpoint with a very
long `secretRef` publishes the string verbatim in the attested state and every signed readiness
report.
**Fix.** Restrict `SecretRef` to `^[a-z0-9_]{1,64}$` in `validateEndpoint`.

### T-29 · Low · `EXTENSION_ID=0` accepted at startup, refused only at signing

**Where.** `internal/config/config.go:317-321` (negatives rejected only),
`cmd/docker/main.go:39-46`; refusal at `feedupdate.go:234`. The machine boots, observes,
publishes state and refuses every request as `internal error`.
**Fix.** Reject `Sign() <= 0` in `extensionIDFrom`.

### T-30 · Low · Readiness correlator may be zero

**Where.** `internal/extension/readiness.go:21-25`, `internal/wire/readiness/readiness.go:85-93`.
**Fix.** Refuse a zero correlator in both places.

### T-31 · Low · Handlers run on a background context past tee-node's budget

**Where.** `feed.go:39`, `readiness.go:35`, `provisioning.go:52`; `pkg/server/server.go:38`
(1-second sign timeout inside tee-node's 2 seconds). A `SET_SECRET` whose verify and decrypt
straddle the deadline takes effect after the operator was told it timed out.
**Fix.** Derive handler contexts from the request and check them before installing.

### T-32 · Low · Envelope replay window after a restart

**Where.** `envelope.go:35, 37, 378-383` (one hour plus two minutes of skew is the only bar that
survives a restart), `:226`, `store.go:122, 136`. The proxy sees every envelope and can replay a
recent one after a restart and a re-published admin set; harmful only if the credential was
rotated in between.
**Fix.** Refuse `issuedAt` earlier than process start minus the skew.

### T-34 · Low · Keeper `-feedId` is used for the paid request but screening is pinned to USDX

**Where.** `tools/cmd/keeper/main.go:58, 158`, `cycle.go:133`, `preflight.go:43, 120, 128,
187`. A keeper started for any other feed pays the fee every cycle and drops every answer.
**Fix.** Thread the configured feed through `Screen` and `readCommitments`, or remove the flag.

### T-36 · Low · Private keys on command lines, now on live-chain paths

**Where.** `oracle-status.sh:64`; `use-chain.sh:194, 263` (live chains); `publish-local.sh:116,
137, 189, 235`; `deploy-oracle.sh:91, 118, 227, 260, 276`. `cast wallet address --private-key`
and `cast send --private-key` expose the funded key to `ps` for the call's duration.
**Fix.** Derive addresses in Go or from `INITIAL_OWNER`; sign through a keystore or account
rather than argv.

### T-37 · Low · Direct API key passed on argv where the tools already read it from the environment

**Where.** `publish-local.sh:157`, `oracle-cycle.sh:93`; the tools default the flag from
`$DIRECT_API_KEY`.
**Fix.** Drop the flag; export `DIRECT_API_KEY=local` for the simulation only.

### T-38 · Low · Proxy-supplied `Log` strings reach logs and records unquoted and unbounded

**Where.** `cycle.go:163-166` (raw `Log` logged and stored as `errorClass`, documented as a
closed vocabulary), `readiness.go:71-72`, `cycle.go:569-581`; the console logger writes messages
verbatim, so embedded newlines forge log lines.
**Fix.** Accept `errorClass` only from the known vocabulary, log with `%q`, cap the string.

### T-39 · Low · Predictable `/tmp` build and hand-off paths

**Where.** `oracle-cycle.sh:83-84, 97, 100, 103`, `oracle-watch.sh:50-53`,
`upload-rpc-keys.sh:231`. On a shared host another user can swap the binary between build and
exec, and it runs with the hot key in its environment.
**Fix.** `mktemp -d`, or build under the project directory.

### T-40 · Low · `usdxprov -tee` optional; scheme unchecked

**Where.** `cmd/usdxprov/main.go:207, 497-506, 586`. Without `-tee` the tool encrypts to
whatever `/info` served, over any scheme.
**Fix.** Require `-tee` unless an explicit unpinned flag is given; refuse `http://` off loopback.

### T-41 · Low · `allow-tee-version` still allows the self-reported hash by default

**Where.** `tools/cmd/allow-tee-version/main.go:71-78, 84-86, 96`; `post-build.sh:111-116`
passes no hash. Pinned only when `EXPECTED_CODE_HASH` is given, against the JSON field rather
than the attestation.
**Fix.** Require the expected hash on non-simulated runs and cross-check through
`GetCodeHashAndPlatform`.

### T-42 · Low · Sibling development build copies the whole checkout, `.env` included

**Where.** `Dockerfile.siblings.dev:25` from the context `../../..`; no `.dockerignore` exists at
that context.
**Fix.** A `.dockerignore` at that context, or a COPY narrowed to the module's sources.

### T-43 · Low · Secrets in compose `environment:` visible through `docker inspect`

**Where.** `docker-compose.yaml:24`, `docker-compose.live.yaml:25, 28`.
**Fix.** Compose `secrets:` or an `env_file` with 600 permissions.

### T-44 · Low · Hardhat key remains the tools' silent fallback

**Where.** `tools/pkg/support/support.go:104-109` (warning only, no chain guard),
`configs.go:119`; `docker-compose.yaml:24` and `start-services.sh:199` keep it for the
simulation.
**Fix.** Refuse the fallback unless the chain id is 31337.

### T-45 · Low · Indexer database root/root on the stack network; it is the proxy's policy source

**Where.** `docker-compose.coston-indexer.yaml:21, 32`, `coston2-indexer.yaml:12, 22`,
`extension_proxy.live.toml:19-25`, `indexer-config.toml.template:87-94`. The host publish is
loopback by default; every container on the network can write the database the proxy reads its
signing policy from. How far a fabricated row propagates into the node's own policy chain was
not traced.
**Fix.** A non-root user with a generated password, which `start-services.sh` can already
substitute; keep the database off the proxy's external network.

### T-46 · Low · `upload-rpc-keys.sh --all` is a no-op against the shipped config schema

**Where.** `upload-rpc-keys.sh:172` reads `.[].providers[]?.secret`; every endpoints file uses
`endpoints`. The documented rotation path prints "Nothing to upload" and exits 0.
**Fix.** `.[].endpoints[]?.secret`.

### T-47 · Low · Machine-chosen refs used unsanitised as variable names and file paths

**Where.** `upload-rpc-keys.sh:128-135, 293, 321-325, 336, 364`. Refs come from an
unauthenticated `/info` read; a ref named `path` or `home` uploads `$PATH` or `$HOME` as the
credential, and `../` writes outside the output directory. The value is sealed to the attested
machine, so a network attacker cannot read it.
**Fix.** Validate refs against `^[a-z0-9_]{1,64}$` and namespace the variables.

### T-48 · Low · Mutable image tags and CI references around the attested image

**Where.** `mysql` untagged and the indexer at `:main` in the indexer compose files,
`redis:7-alpine`, `tee-proxy:v0.0.24` by tag, CI images by tag, `component@2`, an unpinned
`go get` in the test job. None reaches the attested image's bytes; the proxy and indexer that
gate what the node sees can change under a tag.

### T-49 · Low · `MODE` and `SIMULATED_TEE` are set separately and never cross-checked

**Where.** `docker-compose.live.yaml:39` (`MODE=1` by default on every live chain),
`full-setup.sh:89-90` (refuses `SIMULATED_TEE=true` on mainnets, never checks `MODE`);
`SIMULATED_TEE` is passed into a container in which nothing reads it. Fail-closed at the
registry, but only `.env.example` says to set `MODE=0` too.
**Fix.** Derive `MODE` from `SIMULATED_TEE` in `start-services.sh` and die on a mismatch.

## E. Informational

### T-50 · `CHAIN_URL` in the launch policy is read by nothing

`Dockerfile:93-94` lists it, `docker-compose.live.yaml:32` requires it for the node container,
`deploy/README.md:218` and `deployment-steps.md:197` tell operators to set it, and no code in
`internal/`, `cmd/`, `pkg/` or the pinned tee-node reads it (tee-node derives its RPC from
`CHAIN_ID`). Overriding it changes nothing; the entry is part of the attested configuration and
the comment beside it is wrong. Drop it before registering a hash, or document it as unused.

### T-51 · Smaller carry-overs and documentation gaps

- `deployment-steps.md:197` omits the mandatory `CHAIN_ID` and the governance variables from the
  launch environment; `Dockerfile.siblings.dev` claims the same label but lacks the two
  Safe-governance variables (development only).
- Port parsing: `config.go:101-115` keeps defaults on a malformed port while tee-node panics
  first, so the process still fails closed; the extension's defaults differ from tee-node's, so
  tee-node's own-port guard in `ValidateProxyURL` watches the wrong loopback ports.
- A replayed or late-delivered `GET_FEED` stamps a current reading with an old block time; the
  store accepts it only above its ratchet, and the error is towards staleness (B-11/E-8 of the
  area passes).
- Ratchet poisoning is bounded by the data-provider quorum and the enclave's ten-minute clock
  check; a freeze of at most ten minutes even against a colluding quorum, assuming honest RPC
  block times (see T-01 and T-09).
- Who can stop the feed without governance keys, by design: the extension owner and delegated
  pausers (emergency pause), each machine's owner, and anyone once an availability proof lapses.
- `send-admins` prints that the backing role governs `hex_api_key` only; the enclave's
  `BackingRefs` is `{hex_api_key, hex_cash_url}`. The impact is bounded because a provisioned
  cash URL must match the published commitment, so a backing admin can supply or withhold it,
  not redirect it.
- Dead checks: `price.go:29-34` and `supply.go:321-323` can never fire; `PriceImplausible` is
  raised nowhere; `feedupdate.ValueFromBig` accepts any int32 including negatives, harmless today
  because `Compute` cannot produce one.
- Decoder text reaches `Log` through the 400 bodies at `router.go:28, 45`; two critical sections
  in `feed.go`; `result.go:65` reports version `0.1.0` where tee-node's results say `1.0.0`;
  `envelope.go:398` burns the per-ref version before decrypt; admins sign a hash they cannot read.
- `cmd/start-tee` falls back to a well-known Hardhat key for `INITIAL_OWNER` (development
  stack); `lib/env.sh` exports every `.env` value to every child process; `use-chain.sh` reuses
  the deployer key as the first admin key; `sed -i ''` is BSD-only; unescaped `.env` values enter
  `sed` scripts in `start-services.sh:176-179` and `import-upstream-addresses.sh:115`.

## Status of the previous review's findings

| SR | Status | Evidence |
| --- | --- | --- |
| SR-01 | partial | default bind unchanged (T-19); template `.env` pins loopback |
| SR-02 | fixed | `extension.go:97` binds `127.0.0.1`; `EXPOSE 5500` only |
| SR-03 | fixed | `preflight.go:164-205`, `cycle.go:275-311` |
| SR-04 | fixed | `oracle-cycle.sh` passes `-p` only under `PROXY_OVERRIDE` |
| SR-05 | partial | store half open (T-27); script half open (T-47) |
| SR-06 | open | T-28 |
| SR-07 | partial | label complete; `deployment-steps.md` incomplete; README advice wrong (T-16) |
| SR-08 | open | T-01 |
| SR-09 | open | T-21 |
| SR-10 | open | T-04 |
| SR-11 | open | T-03 |
| SR-12 | open | T-15 |
| SR-13 | fixed | `usdxprov build -ref hex_cash_url` derives the blob; `upload-hex-key.sh` uploads both refs |
| SR-14 | partial | live chains require `PROXY_PRIVATE_KEY`; tools' fallback unchanged (T-44) |
| SR-15 | partial | T-41 |
| SR-16 | open | T-13 |
| SR-17 | partial | submit wait, state reads and readiness body bounded; request wait and result polls not (T-07, T-12) |
| SR-18 | partial | both bases pinned by digest; proxy image by tag (T-48) |
| SR-19 | open | T-06 |
| SR-20 | open | T-26 |
| SR-21 | open | T-30 |
| SR-22 | partial | 1-second sign timeout; contexts still background (T-31) |
| SR-23 | partial | tee-node's own parsing fails closed first |
| SR-24 | open | T-10 |
| SR-25 | open | `supply.go:204`, no wrong price results |
| SR-26 | open | T-22 |
| SR-27 | open | T-01, T-02 |
| SR-28 | open | T-02 |
| SR-29 | open | T-15 |
| SR-30 | open | T-32 |
| SR-31 | open | T-33 |
| SR-32 | open | T-33 |
| SR-33 | open | T-40 |
| SR-34 | open | upload scripts still `export` the secrets |
| SR-35 | open | T-47 |
| SR-36 | open, wider | T-36 |
| SR-37 | open | T-39 |
| SR-38 | open | T-42 |
| SR-39 | open | `set-governance` uses `HexToAddress` unchecked |
| SR-40 | open | T-34 |
| SR-41 | open | `registration.go` fixed fee and receipt logs by index |
| SR-42 | open | `start-services.sh:353-358` |
| SR-43 | partial | dead branch gone; `.env` overwrite remains |
| SR-44 | open | `validate/checks.go:134-135, 572-574` |
| SR-45 | fixed | `oracle-status.sh` reads `/info` |
| SR-46 | open | `modcache-proxy.sh:34` |
| SR-47 | open (info) | `router.go:28, 45` |
| SR-48 | open (info) | `feed.go:43-45, 51-57` |
| SR-49 | open (info) | `result.go:65` |
| SR-50 | open, accepted | identical to tee-node's Dockerfile |
| SR-51 | open by design | admins sign a hash |
| SR-52 | fixed | the signed `observedAt` is the instruction's block timestamp; residual moved to T-09 |
| SR-53 | open | T-09 |
| SR-54 | open (info) | `envelope.go:398, 499` |
| SR-55 | fixed | `MODE` not overridable; `INITIAL_OWNER` overridable by tee-node parity |
| SR-56 | fixed | pinned tee-node folds the state into `/info` |

## Verified clean

Re-verified in this pass, with the same conclusion as a week ago:

- **The signature and its domain.** The enclave signs
  `abi.encode(bytes32("TEE_ORACLE_FEED"), chainId, keccak(abi.encode(FeedUpdate)))` wrapped
  EIP-191, refusing a zero chain id, extension id, feed id, `observedAt` or hash; the store
  checks extension and feed ids inside the body; the keeper recomputes the digest independently.
  A testnet update, another extension's, or a sibling feed's cannot land.
- **Channel separation.** `SET_ENDPOINTS` and `SET_ADMINS` only over the cosigned instruction
  channel, `GET_FEED` never over `/direct`; the action id must equal the cosigned instruction id,
  checked by tee-node and again by the extension.
- **Configuration integrity.** Feed id checked on both payloads, versions strictly increasing,
  every pinned chain and the cash group required, HTTPS enforced, hashes taken over the raw
  dispatched bytes and captured with each observation, so an update after a configuration change
  carries the superseded hash and is rejected on chain rather than accepted with a wrong value.
  A provisioned cash URL must match the published salted commitment, so no admin can point the
  cash source elsewhere.
- **The direct channel.** `SET_SECRET` needs an installed admin set, the threshold count, chain
  id, machine id, domain, payload hash, freshness and per-ref version, then that many distinct
  low-s signatures; every rejection reads the same. Backing refs are pinned in the build and
  cannot be named by chain endpoints. Nothing is written to any map before verification passes.
- **Input bounds inside the enclave.** Instruction bodies are capped by tee-node, provisioning
  payloads at 256 KiB before decode, the feed request at exactly 32 bytes, the readiness message
  at one byte, signatures at 16, admins at 32; ABI decoding returns errors rather than panicking;
  the sign client has a 1-second timeout and a 1 MiB body cap; the Hex client 64 KiB.
- **Price arithmetic.** Quorum-agreed decimals capped at 36, exact `big.Rat` scaling, par cap,
  half-up rounding, fail-closed on zero supply and on int32 overflow; a single dissenting provider
  in a 2-of-3 group can move nothing.
- **No leaks.** `Log` and `/state` carry constant vocabulary, counters, hashes, ref names and
  positional health keys; the decrypted plaintext is never logged or wrapped; upload scripts pass
  secrets by environment or hidden prompt and verify the machine before encrypting.
- **Fleet handling in the keeper.** Answers from machines not asked, wrong extension or feed,
  or a stale generation are dropped per answer; one answer per signer; the largest group by
  `observedAt`; the store's own `eth_call` as the final check; a batch that already fails costs
  no gas.
- **The image.** Both bases pinned by digest, `go mod verify`, an allowlist `.dockerignore`, both
  in-process listeners on loopback, `EXPOSE 5500` only, `MODE` not overridable, the epoch
  required.

## Not inspected, and assumptions

- tee-proxy's handlers beyond route registration; the indexer's own code.
- Confidential Space behaviour beyond Google's published documentation (inbound blocked by
  default, `EXPOSE` opens ports, restart policy).
- Flare's consensus finality (assumed: no reorgs relevant to the keeper).
- How a fabricated row in the indexer database propagates into the node's signing-policy view
  (T-45).
- The memory available to the workload VM, which decides whether T-03 is a crash or a slowdown.

## Suggested order of fixes

1. **T-03, T-04** bound every RPC response and give each provider its own timeout: one custom
   HTTP client through `rpc.WithHTTPClient`. Closes the only remote crash of the enclave found.
2. **T-01, T-02** a chain-group threshold floor of 2 with three Songbird providers, pinned
   `decimals`, a `metric_name` check, and a deviation or floor guard that finally raises
   `PriceImplausible`. These decide whether a data-source compromise prints a depeg.
3. **T-05** raise `requiredSignatures` as soon as the fleet has two or more machines at the
   latest generation. Until then, several machines are redundancy only.
4. **T-07, T-12, T-06, T-11, T-13, T-14** the keeper: timeouts and body limits on every read,
   a bounded request wait, fee and gas ceilings, a live target list, the readiness signer check,
   and the key sent only to the configured proxy.
5. **T-09, T-10** the future-timestamp and inflated-head cases in the quorum logic.
6. **T-16, T-17, T-18, T-19** the deployment edges: correct the governance advice and firewall
   5500, `umask 077` and single-quoting in `use-chain.sh`, loopback defaults for the proxy's
   internal API.
7. **T-15** unconditional secret checks and eviction instead of refusal in the store.
8. **T-20** one reproducibility recipe, pinned and published per tag.
9. The Low and Info items, most of them one-line, as the files they touch are next edited.
