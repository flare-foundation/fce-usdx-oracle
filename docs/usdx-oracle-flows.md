# USDX Oracle — Architecture & Flows

A short map of how the pieces fit together: what runs where, what the entrypoints
are, and which code each flow goes through.

Companion docs: [usdx-oracle-spec.md](usdx-oracle-spec.md) (design intent),
[usdx-oracle-findings.md](usdx-oracle-findings.md) (what turned out to be true),
[usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the relay),
[usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md) (a machine's account of itself).

---

## What it does

Publishes a **USDX price** — `min(1, backing / supply)` — as an FTSO custom feed.
The price is computed inside a TEE from live off-chain data, signed by the
enclave's identity key, and relayed on-chain where a store contract verifies the
signature against the TEE machine registry.

Nobody in the delivery path can alter the number.

---

## Processes

One machine is one Compose stack (`deploy/docker-compose.yaml`), three containers.
tee-node is not a container of its own: it is a Go library, linked into the
extension's binary, and the two halves talk HTTP over loopback inside one process.

```
ext-proxy (tee-proxy image, the tag tools/go.mod pins)
  :6663 internal → host 6673    tee-node polls here for work and posts its results
  :6664 external → host 6674    PUBLIC: /info, /action/result/<id>, /direct
                                (/direct needs DIRECT_API_KEY on a live network)

extension-tee ── ONE process, cmd/extension-tee, what Dockerfile builds
  tee-node    (teeServer.StartServerExtension, in-process)
                 :5500 config server
                 :7701 sign server, loopback: POST /sign, POST /decrypt
                 polls the proxy for actions → POST 127.0.0.1:7702/action
                 reads GET 127.0.0.1:7702/state on every TEE info response
  extension   (pkg/server.StartExtension)
                 :7702 loopback only: POST /action, GET /state
  poller      goroutine, observes every 2 minutes

redis            the proxy's action queue (tmpfs, nothing persisted)
```

The other compose files add no container to the machine:

| File | Role |
| --- | --- |
| `deploy/docker-compose.live.yaml` | overlay for a live network: the same services, with `CHAIN_ID`, `DIRECT_API_KEY` and `PROXY_PRIVATE_KEY` required (and `CHAIN_URL`, which nothing in the image reads), on the chain's indexer network |
| `deploy/docker-compose.coston-indexer.yaml`, `deploy/docker-compose.coston2-indexer.yaml` | a separate stack: the chain's C-chain indexer and its MySQL database, which the proxy reads signing policies from |
| `deploy/docker-compose.siblings.yaml` | overlay (`USE_LOCAL_SIBLINGS=1`) that builds `extension-tee` against the sibling tee-node checkout instead of the pinned module |

`cmd/dev/start-tee` runs the same tee-node + extension pairing as a host process,
for `start-services.sh --local`.

`pkg/server.StartExtension` wires the extension: `teenode.Client` → `resolveTeeID`
(retries for up to a minute while tee-node's sign server comes up, then the process
stops) → `envelope.NewVerifier(chainID, teeID, protocol.AdminPolicy())` →
`store.New` → `poller.New` → `extension.NewExtension`.

---

## Entrypoints

### Binaries

| Entrypoint | For | Notes |
| --- | --- | --- |
| `cmd/extension-tee` | **production** | tee-node and the extension in one process; what `Dockerfile` builds |
| `cmd/dev/start-tee` | `start-services.sh --local` | the same pairing as a host process, no Docker |
| `cmd/dev/devserver` | local dev | the extension alone: real provisioning and observation, actions POSTed at `/action`, no chain |
| `cmd/dev/mocknode` | local dev | fake tee-node `/sign` `/decrypt` `/info`, for `devserver` |
| `tools/cmd/send-secret` | ops | build / sign / submit a `SET_SECRET` envelope; `commit` prints the cash URL's commitment, `signers` who has signed |
| `tools/cmd/keeper` | ops | the relay timer; `-tee` names the target machines, a static list |
| `tools/cmd/send-endpoints` | ops | publish `SET_ENDPOINTS` for the feed (`-feedId`, `-config`); confirms delivery against `-p` |
| `tools/cmd/send-admins` | ops | publish `SET_ADMINS` the same way |
| `tools/cmd/fleet-status` | ops | per machine: registry status, whether it holds the feed's latest configuration, and what it reports running |
| `tools/cmd/verify-tee` | ops | gate before a credential: the machine behind a proxy is `PRODUCTION` on our extension, on an allowed code hash; `-printTeeId` for `send-secret build -tee` |
| `tools/cmd/*` | ops | one-shot: register-tee, set-governance, allow-tee-version, retire-tee, wire-extension, readiness; long-running: oracle-watch, cycles-web… |

There is no stub build of the extension. Conformance is `TestConformance` in
`internal/extension`, an in-process Go test.

### Scripts

| Script | For |
| --- | --- |
| `deploy/scripts/upload-hex-key.sh` | install `hex_api_key` and `hex_cash_url` — backing set |
| `deploy/scripts/upload-rpc-keys.sh` | install the RPC credentials — providers set |
| `deploy/scripts/oracle-*.sh` | observe, cycle, status, watch — the operator commands |

### HTTP — exactly two ways into the extension

Both on `127.0.0.1:7702`, deliberately loopback only: arrival on the instruction
channel is itself the authorisation, so a listener reachable from the network would
let a peer install its own admins and endpoints.

```
POST /action    internal/extension/extension.go:Extension.actionHandler   every instruction & direct action
GET  /state     internal/extension/extension.go:Extension.stateHandler    read by tee-node → the empty state
```

### On-chain

The sender and store are upstream's generic TEE oracle contracts
(`TeeOracleInstructionsSender` + `TeeOracleFeedStore`, flare-smart-contracts-v2).
`coston`, `songbird` and `flare` use the upstream deployment (`oracle=upstream` in
`deploy/scripts/lib/chains.sh`). This repo deploys the same contracts itself only
for the local simulation and the test deployments `coston_local` and `coston2`
(`deploy/scripts/deploy-oracle.sh`), where they stay governed by the deployer, so
"Flare governance" below is the deployer's key and publications execute at once.

```
TeeOracleInstructionsSender
  requestFeedUpdate(feedId, teeIds)               payable, ANYONE → instructionId
  setEndpoints(feedId, groups, claimBack)         payable, Flare governance (timelocked)
  setAdmins(feedId, roles, claimBack)             payable, Flare governance (timelocked)
  pushEndpoints(feedId, teeIds, groups)           payable, ANYONE
  pushAdmins(feedId, teeIds, roles)               payable, ANYONE
  getEndpointsPushTargets(feedId) · getAdminsPushTargets(feedId)      view
  latestEndpointsHash(feedId) · latestAdminsHash(feedId) · isTeeIdConfigured(feedId, teeId)
TeeOracleFeedStore
  submitFeedUpdates(SignedFeedUpdate[])           ANYONE
  setSubmissionPolicy(policy) · setFeeDestination(addr)               Flare governance (timelocked)
  getCurrentFeed()                                payable, FtsoV2 / consumers
```

No tool in this repo wraps `pushEndpoints` / `pushAdmins`.

---

## The action router

Everything entering the extension goes through one dispatch. Every command sits
under a single op type, `TEE_ORACLE`, and each command is valid over exactly one
channel:

```
POST /action                                    Extension.actionHandler
  │  body is not an Action                           → HTTP 400
  └─ processAction                              internal/extension/router.go
       ├─ Data.Type = instruction → DataFixed        (undecodable → HTTP 400)
       │    opType ≠ TEE_ORACLE                      → HTTP 501
       │    action id ≠ instruction id               → HTTP 400
       │    processInstruction
       │      ├─ GET_FEED                → processGetFeed          [Flow D]
       │      ├─ SET_ENDPOINTS           → processSetEndpoints     [Flow A]
       │      ├─ SET_ADMINS              → processSetAdmins        [Flow A2]
       │      ├─ GET_READINESS, SET_SECRET → status 0, WrongActionType
       │      └─ anything else           → HTTP 501
       ├─ Data.Type = direct → DirectInstruction     (undecodable → HTTP 400)
       │    opType ≠ TEE_ORACLE                      → HTTP 501
       │    processDirect
       │      ├─ GET_READINESS           → processGetReadiness     [readiness doc]
       │      ├─ SET_SECRET              → processSetSecret        [Flow B]
       │      ├─ GET_FEED, SET_ENDPOINTS, SET_ADMINS → status 0, WrongActionType
       │      └─ anything else           → HTTP 501
       └─ any other Data.Type                        → HTTP 400
```

A handled command always answers HTTP 200 with an `ActionResult`: status 1 with its
data, or status 0 with `log: "error: <reason>"`. tee-node turns a non-200 into a
status-0 result itself, carrying the response body as the log.

With one op type for everything, the two routers are the **only fence** between a
permissionless read and a governance-published write, which is why neither has a
shared default arm. The channel check matters: `SET_ENDPOINTS` or `SET_ADMINS` over
`direct` would skip the sender's `onlyGovernance` check, `SET_SECRET` over
`instruction` would put a credential ciphertext in permanent public storage, and
`GET_FEED` over `direct` would hand a signed update to anyone holding the proxy key.

---

## Flow A — SET_ENDPOINTS (which sources we read)

```
Flare governance → TeeOracleInstructionsSender.setEndpoints(feedId, groups, claimBack)
          shape checks only (non-zero unique tags, 1 ≤ threshold ≤ count, each endpoint a
            url or a commitment); what a tag means is the enclave's to check
          version = the feed's endpointsVersion + 1      ← assigned when the call executes
          message = abi.encode(Endpoints{version, feedId, groups})
          latestEndpointsHash(feedId) = keccak256(message)   ← the feed-level commitment
          emit EndpointsPublished(feedId, version, hash, groups)   ← the payload a push reads back
          extension emergency paused, or no active machine → published, NOT dispatched
          otherwise ONE instruction to the extension's whole active set, each target's
            version recorded (EndpointsSet)
anyone → pushEndpoints(feedId, teeIds, groups)            ← a machine that joined later, or a retry
          re-encoded with the stored version; must hash to latestEndpointsHash (WrongConfigPayload)
          one instruction to the named machines; getEndpointsPushTargets(feedId) lists who is behind
             ↓  cosigned → proxy → tee-node → POST /action (type = instruction)
        processSetEndpoints → store.ApplyEndpoints         internal/provisioning/store
          payload non-empty and ≤ 256 KiB
          ABI decode into Endpoints{version, feedId, groups}
          validate():
            · version non-zero
            · feedId must be this build's (protocol.FeedID), else rejected
            · every group tag known: a pinned chain (protocol.Chains) or hex_cash
            · every group non-empty, 1 ≤ threshold ≤ endpoint count
            · PUBLIC: a url only, https, {secret} present iff a secret ref is,
              never in the scheme or host
            · PRIVATE: a urlHash commitment and a secret ref, no url
            · a chain group may not name a backing ref (hex_api_key, hex_cash_url)
            · EVERY pinned chain group present
            · hex_cash present: exactly one PRIVATE endpoint, threshold 1, secret ref hex_cash_url
          version strictly greater than the last applied
          every credential already installed must fit the new configuration
          endpointsHash = keccak256(payload)           ← hashed as received
          any failure → ProvisionRejected; the detail goes to the machine's log only
```

Groups are the unit — a tagged (`bytes32`) list of endpoints, each endpoint PUBLIC
(`url`) or PRIVATE (a salted `urlHash` commitment plus a `secretRef` naming the
off-chain entry that delivers the URL). The chain RPC groups are public; the
restricted `hex_cash` reserve endpoint is private, so its URL never lands in
calldata. The bonds URL is not configuration at all: it is pinned in the build.

That hash goes into every signed update, and the store compares it with
`latestEndpointsHash(feedId)`. A publication therefore invalidates every machine
still on the previous generation until it applies the new one, and
`requestFeedUpdate` refuses to address such a machine (`TeeIdNotConfigured`).
Without the hash, on-chain config would record what was *sent*, never what a
machine is *running*.

---

## Flow A2 — SET_ADMINS (who may provision this machine)

```
Flare governance → TeeOracleInstructionsSender.setAdmins(feedId, roles, claimBack)
          shape checks only (non-zero unique roles and admins, 1 ≤ threshold ≤ count)
          version = the feed's adminsVersion + 1          ← assigned when the call executes
          message = abi.encode(Admins{version, feedId, roles})
          latestAdminsHash(feedId) = keccak256(message)
          emit AdminsPublished(feedId, version, hash, roles)
          dispatch exactly as setEndpoints: the whole active set, or nothing while paused / empty
anyone → pushAdmins(feedId, teeIds, roles)  ·  getAdminsPushTargets(feedId)
             ↓  cosigned → proxy → tee-node → POST /action (type = instruction)
        processSetAdmins → store.ApplyAdmins
          payload non-empty and ≤ 256 KiB
          ABI decode into Admins{version, feedId, roles}
          convertAdmins():
            · version non-zero
            · feedId must be this build's, else rejected
            · role tags "backing" and "providers"; both required; unknown or duplicate rejected
            · per role: 1 ≤ admin count ≤ 32, floor ≤ threshold ≤ min(admin count, 16)
              floors pinned in protocol.AdminPolicy: backing 1, providers 2
              (16 is the signature cap Verify enforces: a higher threshold
               would install cleanly and could then never be met)
            · every admin non-zero and distinct within its role
          version strictly greater than the last applied
          verifier.SetAdmins(roles)                   ← all roles, or none
          adminsHash = keccak256(payload)             ← hashed as received
```

The contract assigns the version when the publication executes, so a publication
cannot be replayed at a stale one, and the enclave refuses any version not newer
than the one it holds. The payload carries role-TAGGED signer sets and the
envelope's ref picks one:

```
hex_api_key, hex_cash_url   → backing set     (protocol.BackingRefs; floor 1)
every other ref             → providers set   (floor 2)
```

The ref→role mapping is pinned in the build, never read from the payload —
otherwise the weaker set could claim the stronger set's credentials. Signers from
the other set do not count toward a threshold, and an unrecognised ref falls under
the *stronger* set. The floors are the least a publication may set; the threshold
that applies is the published one (Songbird's backing set is 2-of-3).

Same enforcement as Flow A: the hash goes into every signed update and the store
compares it with `latestAdminsHash(feedId)`.

The verifier starts with **no** admin sets and rejects every envelope until both
are installed, so Flow A2 must precede Flow B. Nothing survives a restart, and a
restarted machine has a new identity: it is re-registered and brought up to date
with `pushAdmins` and `pushEndpoints` before it can be provisioned again.

---

## Flow B — SET_SECRET (the credentials)

Three steps, possibly three different people, and the chain is never written.

Run by two parties, because the ref decides which admin set authorises the
envelope and the two sets may be held by different people:

```
deploy/scripts/upload-hex-key.sh    hex_api_key, hex_cash_url   backing set
deploy/scripts/upload-rpc-keys.sh   every other ref             providers set
```

Both run `verify-tee -printTeeId` first and build every envelope with
`send-secret build -tee <that teeId>`, so nothing is encrypted to a key the registry
has not attested. Every secret comes from the environment, never argv. Each signs
with the admin keys held locally (`ADMIN_KEYS`, or the single `ADMIN_KEY`):
`upload-rpc-keys.sh` writes out any envelope still short of the threshold for
another admin to sign, and `upload-hex-key.sh` refuses before building when its
keys cannot meet the backing threshold. With `--build-only` either writes the
envelopes unsigned and reads no admin key. `upload-rpc-keys.sh` uploads whatever the
machine reports as `readiness.missingSecretRefs`, one envelope per ref. Under the
scripts it is the same three commands:

```
admin laptop
  send-secret build    GET proxy/info → public key; refused unless it derives the -tee id
                       ciphertext = ECIES.Encrypt(pubkey, secret)
                         (hex_cash_url: the secret is abi.encode(salt, url))
                       body = abi.encode(domain, chainId, teeId, version, issuedAt, ref,
                                         keccak256(ciphertext))
  send-secret sign     admin #1   (refuses a duplicate signer)
  send-secret sign     admin #2…  (until the set's threshold)
  send-secret submit   POST proxy/direct     ← within envelope.Freshness (1 hour) of issuedAt
     ↓  tee-node polls the direct queue → POST /action (type = direct)
enclave
  processSetSecret → store.ApplySecret                  internal/provisioning/store
    message non-empty and ≤ 256 KiB, checked before the lock
    applyMu held for the whole call                     ← the replay defence needs this
    envelope.Verifier.Verify                            internal/provisioning/envelope
       admin sets must be installed at all — else rejected outright
       ≤ 16 signatures (envelope.MaxSignatures)
       set = backing for hex_api_key / hex_cash_url, providers otherwise
       chainId == CHAIN_ID · teeId == this machine's (resolved once at boot)
       ref non-empty, ≤ 64 bytes
       issuedAt non-zero, ≤ 1 hour old, ≤ 2 minutes ahead (Freshness, ClockSkew)
       version > the last applied for this ref
       ≥ set.threshold DISTINCT signers FROM THAT SET (EIP-191 over the body)
    node.Decrypt()   POST 127.0.0.1:7701/decrypt        ← in-enclave
    with endpoints installed, the value must fit them (else Flow A re-checks it):
       a credential (hex_api_key, or one filling a PUBLIC url's {secret}):
         no space \t \r \n / ? # &
       a PRIVATE endpoint's entry (hex_cash_url, or a private RPC url): decodes as
         (salt, url), salt non-zero, commits to the published urlHash, a complete
         https url
    secrets[ref] = value                                ← memory only, lost on restart
    every failure returns the SAME ProvisionRejected    ← no probing oracle
```

---

## Flow C — the observation loop (the engine)

Runs every 2 minutes whether or not anyone asks for a price. Nothing else reads an
upstream.

```
poller.Poller.Run — at boot, then every 2 minutes      internal/observation/poller
  │
  ├─ store.Open()
  │     re-run every tick, never cached
  │     no endpoints, or a provider url whose credential is missing →
  │       NotConfigured (recorded; nothing is read)
  │     builds RPC clients, the bonds source (pinned URL), and the cash source only when
  │       both the committed URL and hex_api_key are installed — else nil, "running cash-less"
  │     provider names are positional ("rpc_flare_1") so nothing an operator
  │     writes reaches the readiness report
  │
  └─ observation.Observe — 20s deadline                 internal/observation
        S   supply, all chains concurrently              internal/observation/supply
              ask every provider for its head → take the Quorum-th HIGHEST, stepping
                down through the distinct lower heads until Quorum providers serve one
              refuse it if its header is older than MaxBlockAge (2 min, Ethereum 3)
              query every provider at that EXACT block
              require Quorum IDENTICAL results (never a median) and a UNIQUE most
                common answer — a tie fails closed rather than letting provider order
                decide the supply figure
              amount = raw / 10^decimals, decimals read at the same block, exact
              supply = the plain sum over protocol.Chains — no exclusion list
        B   backing, both endpoints concurrently         internal/observation/backing
              bonds failing is fatal; cash failing is not
              no redirects · one JSON value · unknown fields rejected
              metric_name must be the configured one
              currency: the cash figure must say USD; the bonds figure may omit it
              comma grouping validated, not stripped
              NO bound on how old last_update is — both endpoints republish on
                an on-chain event, not on a schedule, so an old stamp means
                nothing changed. A FUTURE stamp is still refused.
        checkWindow(30s) across all read timestamps
        price = min(1, B/S) × 10⁸, rounded half-up in exact big.Rat, as int32
              no cash? publish only if bonds alone reach par — the answer then
              provably cannot depend on it
        the reading's time = the Flare pinned block's header timestamp
  │
  └─ success → cached as "latest"
     failure → class recorded, the previous observation is LEFT IN PLACE
               and ages out after 10 minutes
```

**One pass per side:** each supply figure is pinned to one agreed block, and a
reserve figure changes only within ~10 minutes of an on-chain event, so neither
side has an internal race worth bracketing. `checkWindow` bounds the skew between
the two. A double-read (S₁ … B … S₂, taking the larger supply) was considered and
dropped for this reason — see rationale §2.

---

## Flow D — GET_FEED (must finish in 2 seconds)

```
anyone → TeeOracleInstructionsSender.requestFeedUpdate(feedId, teeIds)   payable
           FeedNotConfigured   until endpoints AND admins are both published for the feed
           TeeIdNotConfigured  unless every named machine was dispatched both latest versions
           ONE instruction, addressed to every named machine; the fee scales with the count
           message = abi.encode(FeedUpdateRequest{feedId})   ← one 32-byte word
     ↓  cosigned → proxy → tee-node → POST /action
  processGetFeed                                      internal/extension/feed.go
    feedupdate.DecodeFeedRequest(message) — exactly that ABI envelope, naming this
      machine's feed; else UnexpectedPayload
    Poller.Report(requestedAt = the instruction's block timestamp, from DataFixed)
      Latest()      before the first success: the latest failure, or NoObservation;
                    ObservationStale if the reading is older than 10 min by the
                    enclave's clock
      checkClock    requestedAt == 0 → MissingRequestTime
                    requestedAt more than 10 min after the reading → ClockBehindChain
                    (the local bound passed, so the enclave's clock is slow)
      the cached binding names no admin set → NotConfigured
      observation.Sign(…, requestedAt)
        update = FeedUpdate{extensionId, feedId, value, decimals, observedAt = requestedAt,
                            endpointsHash, adminsHash}
        dataHash = keccak256(abi.encode(update))
        message  = abi.encode(TEE_ORACLE_FEED, chainId, dataHash)   ← SignedPayload
        POST 127.0.0.1:7701/sign (message) — tee-node keccaks it and applies EIP-191;
        base64 on the wire; v normalised from 0/1 to 27/28 (teenode.NormalizeV)
        data = abi.encode(update, Signature{v, r, s})
    ActionResult{status: 1, data}       a refusal: status 0, log "error: <reason>"
     ↓  tee-node → proxy → served at /action/result/<id>
```

**observedAt is the request's time, not the reading's.** The signed `observedAt` is
the requesting instruction's block timestamp, so every machine answering one
request signs the same value — which `submitFeedUpdates` requires of a batch — and
it is always a past block's time, so `TooFarAhead` is unreachable for an honest
update. The inputs behind it were read at the Flare pinned block's header time
(`lastObservedAt` in the readiness report), at most `MaxObservationAge` (10 min)
before it — or after it, when the instruction was delivered late — bounded against
both the enclave's own clock (`Latest`) and the request time (`checkClock`).

The signed payload is a `FeedUpdate`: it names the `extensionId` and `feedId`
(cross-feed replay protection), carries `value` as an `int32` with its `decimals`,
and commits to both configuration hashes. It carries no instruction id. The chain id
is a field of the signed payload, between the `TEE_ORACLE_FEED` prefix and the data
hash (upstream `SignedPayload.messageHash`), not part of the prefix.

**Why the split from Flow C:** an observation measures 2.0–2.9s and tee-node
allows an extension 2 seconds per action. Observing inside the handler timed out
on every request. A request costs one signing round trip.

---

## Flow E — the keeper relay

```
tools/cmd/keeper → keeper.Run: a cycle every -interval (10 min), never overlapping
                                                      tools/pkg/keeper
  0. record the pending-nonce gap and the balance
     preflight, with DIRECT_API_KEY: GET_READINESS to -p and to every machine the
       registry lists for the store's extension; EVERY one answers and NONE would
       sign → request_skipped, no fee spent; anything unreadable → request anyway
  1. TRIGGER   requestFeedUpdate(feedId, teeIds) → instructionId
                 teeIds is the static -tee list
  2. COLLECT   GET <proxy>/action/result/<id>, on -p and every listed machine's
                 proxy, backoff 1s → 16s, until every target has answered or the
                 poll timeout passes
                 404 = pending, not an error
                 status 0 → that machine contributes nothing; the first such log
                 is recorded verbatim as errorClass, nothing invented
  3. RELAY     decode each FeedUpdate → keep one per signer, the largest group
                 agreeing on observedAt → preflight (each answer's signer, feed
                 binding and commitments; then requiredSignatures, the ratchet, and an
                 eth_call rehearsal that names the failing guard)
                 → submitFeedUpdates(SignedFeedUpdate[])
                 ← every update relayed as decoded, signature split into {v, r, s}
                 → wait for the receipt; a timeout is not chased, the next
                   cycle supersedes it
     NotNewer, from the preflight, the rehearsal or a mined revert: the store's
       observedAt ≥ ours → race_lost (another submission landed first; never alert);
       lower → rejected with guard NotNewer
     not_mined means only that no receipt arrived in time
```

The keeper holds **no authority**. Every guard is on-chain, so the worst a wrong
invocation does is waste gas.

---

## Flow F — on-chain acceptance

`TeeOracleFeedStore.submitFeedUpdates(SignedFeedUpdate[])`, in execution order:

```
 1  batch shape: count ≥ requiredSignatures and ≤ 32          (NotEnoughSignatures / TooManySignatures)
    one common observedAt across the batch                     (ObservedAtMismatch)
    the contributors' decimals within 8 of each other          (DecimalsSpreadTooBig)
 2  the two values bracketing the median lie within the bound  (SpreadTooBig)
    — maxSpreadBIPS of |median| plus maxSpreadAbsolute
 3  observedAt strictly greater than stored                    ← replay + out-of-order (NotNewer)
    observedAt < block.timestamp                               ← never in the future (TooFarAhead)
    checked once: the whole batch shares one stamp
 4  sender.latestEndpointsHash(feedId) and latestAdminsHash(feedId) are published
                                                               (NoEndpointsPublished / NoAdminsPublished)
 5  per contributor, in submission order:
    verifyTeeSignature(extensionId, sig, messageHash)          ← Fdc2Verification: on THIS extension,
                                                                 PRODUCTION, extension not paused
    signer not seen earlier in the batch                       (DuplicateTeeId)
    update.extensionId == the store's                          (WrongExtensionId)
    update.feedId == the store's                               (WrongFeedId)
    update.endpointsHash == latestEndpointsHash                ← closes Flow A (StaleEndpoints)
    update.adminsHash == latestAdminsHash                      ← closes Flow A2 (StaleAdmins)
    ────────────────────────────────────────
    store the median at the finest int32 scale it fits, and observedAt
    emit FeedUpdated(value, decimals, observedAt, teeIds[])
    emit FeedOutliers naming the machines whose value lay beyond the bound
```

`requiredSignatures`, `maxSpreadBIPS` and `maxSpreadAbsolute` are the store's
submission policy, set at deployment and changed by Flare governance with
`setSubmissionPolicy` (upstream's deployment configuration for Coston and Songbird:
2 signatures, 100 BIPS, absolute 0). Only the two
values bracketing the median are judged; an outlier beyond them is accepted and
named in `FeedOutliers`.

There is no price band, no pause flag and no age window on the store. The only
time rules are the strictly increasing ratchet and "never in the future". The
enclave bounds the price to [0, 1] by construction — `min(1, B/S)` over
non-negative inputs — and nothing tighter, so a real depeg publishes the sub-par
price, and is detected on the published value. Staleness is the consumer's check.

---

## Flow G — consumer read

```
FtsoV2 → TeeOracleFeedStore.getCurrentFeed()      payable
           reverts NoValuePublished before the first accepted update
           otherwise → (value int256, decimals int8, observedAt)
             observedAt is the accepted batch's, below block.timestamp by the submission rule
           fee = calculateFee() = FeeCalculator.calculateFeeByIds([feedId]); short → FeeTooLow
           FtsoV2 forwards exactly calculateFee() and burns the caller's excess; the store
           forwards the whole msg.value it receives to the governance-set feeDestination
```

The store implements `IICustomFeed` itself and registers with FtsoV2 directly —
there is no separate adapter. It keeps no per-machine bookkeeping: one global
`observedAt`, one latest value. It never reverts on staleness.

The emergency pause is extension-level, on `FlareTeeManager`
(`emergencyPauseExtension`, by the extension owner or an address on its pauser
list). While it is set, no instruction for the extension is dispatched and
`Fdc2Verification` rejects its signatures, so the feed cannot be requested or
advanced. Reads continue: the pause freezes the last value with its ageing
timestamp, and cannot withdraw it.

---

## The failure path

```
any fault → failure.Failure (closed vocabulary, compile-time constant text)
   ├─ an observation's: recorded by the poller as its last error class
   ├─ next GET_FEED → ActionResult{status: 0, log: "error: <constant text>"}
   │     → proxy /action/result/<id> → the keeper records the log verbatim (errorClass)
   └─ GET_READINESS → signed report (lastErrorClass, sourceHealth) ← PUBLIC TO KEY HOLDERS
```

`Failure` has unexported fields and no exported constructor, so a handler's
refusal is always one of the declared values: no runtime value (an API key inside a
URL error, a reserve figure inside a parse error) can reach a status-0 log or the
readiness report. That is a compile error, not a review item.

The router's own HTTP errors are the exception, and they are public too: tee-node
wraps a non-200 body into the result's `ActionResult.Log`. The 400 bodies quote the
envelope decoder's error and the received action type, the 501 bodies the received
op type or command beside this build's constants. All of it is the caller's own
input or a constant; none of it is the machine's state.

---

## Trust model

| Who | Can | Constrained by |
| --- | --- | --- |
| the attested code | compute and sign the price | registered code hash, reproducible build |
| **Flare governance (timelocked)** | `setEndpoints`: the RPC hosts and each chain's quorum, with **no allowlist** — an accepted trade (findings §15). The bonds URL is pinned in the build; the cash URL is a PRIVATE salted commitment whose preimage only a backing admin provisions | the enclave's `validate()` rules (Flow A) |
| | `setAdmins`: who may provision credentials | the pinned floors: backing ≥ 1, providers ≥ 2 |
| | `setSubmissionPolicy`: `requiredSignatures` and the spread bound, which decide which batch is accepted and which median is stored; `setFeeDestination` | the store's own limits (≤ 32 signatures, ≤ 10000 BIPS) |
| **backing admin set** | install `hex_api_key` and `hex_cash_url` | its published threshold; the key cannot be named by a chain group, and the URL must match its published commitment |
| **providers admin set** | install every RPC credential | its published threshold (≥ 2); a credential cannot alter the URL it lands in |
| Hex Trust | supply the reserve figures | nothing: sole reserve source, no redundancy |
| keeper, or anyone | request and relay | nothing — worst case is wasted gas |
| extension owner / its pausers | emergency pause on `FlareTeeManager` | it freezes the last value; it cannot change or withdraw it |

---

## Which command answers which question

| Question | Command |
| --- | --- |
| Do the real sources yield a sane price? | `./deploy/scripts/oracle-observe.sh` |
| Does the wire contract hold? | `go test ./internal/extension/ -run TestConformance` |
| Does provisioning work? | against a dev server: `./deploy/scripts/set-config.sh admins`, `./deploy/scripts/set-config.sh endpoints`, then `send-secret` (no envelope is accepted before the admins) |
| Does the whole chain work? | `./deploy/scripts/oracle-cycle.sh` |
| What is it publishing right now? | `./deploy/scripts/oracle-status.sh` |

---

## Open items for the audit

1. **The admin set must be published before a live feed.** Nothing is baked: a
   machine starts with no admins and refuses every `SET_SECRET` until `SET_ADMINS`
   installs one. The pre-production requirement is that the set published is a
   real one — `deploy/config/admins/dev-wellknown-keys.json` names private keys 1,
   2 and 3 and is for local and testnet work only.
2. **`GET /state` carries the empty state.** `GET_READINESS` is the one surface
   that reports the poller itself, so use it, and alert on on-chain feed *age* as
   well, which needs no key and no answering machine.
3. **`checkWindow` measures retrieval time, not data age.** The 30s window bounds
   when we *fetched*; the reserve figure itself carries **no** freshness bound —
   both Hex endpoints republish on an on-chain event, not a schedule, so an old
   stamp means nothing changed (see `config.BondsSource`). The 20s observation
   deadline is tighter than the 30s window, so the window cannot fire in
   production; the deadline is the real bound on read skew.
4. **Feed binding in the update.** The `FeedUpdate` names its `extensionId` and
   `feedId`, and the store rejects a mismatch (`WrongExtensionId`/`WrongFeedId`),
   so an update cannot be replayed onto another feed the same extension serves.
   A second store registered for the *same* feed under the same extension would
   still accept it.
5. **One `observedAt` ratchet per feed**, across every machine serving it. Every
   machine signs the requesting block's timestamp, so no machine's clock can push
   the ratchet ahead: a slow clock makes its machine refuse (`ClockBehindChain`),
   a fast one only ages its readings out sooner (`ObservationStale`). Two batches
   carrying the same block's timestamp — answers to two requests mined in one
   block — cannot both land; the second reverts `NotNewer`.
6. **Rounding is half-up**, not down; the bound that truncation bought is held by
   `TestCompute_RoundingCannotHideAMaterialShortfall`.
