# USDX Oracle — codebase walkthrough

A map of the repo for someone seeing it for the first time: what runs where, which
package owns what, and which functions each flow actually goes through.

Companion docs: [`usdx-oracle-flows.md`](usdx-oracle-flows.md) (same flows, more
operational detail) · [`usdx-oracle-rationale.md`](usdx-oracle-rationale.md) (*why*
each design choice) · [`usdx-oracle-findings.md`](usdx-oracle-findings.md) (security
issues found while building).

---

## 1. What the thing does

USDX has no working market venue, so there is no market price. The next-best truth is
collateralisation:

```
supply  = USDX totalSupply() on Flare + Ethereum + Songbird
backing = bond reserves (Hex public API) + cash reserves (Hex private API)
price   = min(1, backing / supply)      capped at par, 8 decimals, rounded half-up
```

That computation runs **inside a TEE**, is signed by the enclave's identity key, and is
relayed on-chain where a store contract verifies the signature against Flare's TEE
machine registry. Nobody in the delivery path can alter the number — a keeper relaying
it holds no authority at all.

---

## 2. What runs where

Three containers. The important non-obvious fact: **tee-node is a client of our
extension** — it calls in over HTTP, it does not link us as a library.

```
ext-proxy (tee-proxy)
  :6663 internal   TEE machines poll here for queued actions
  :6664 external   PUBLIC — /info, /action/result/<id>, /direct

extension-tee ──── one process, built from cmd/docker
  ├ tee-node       :5501 config · :9090 POST /sign, POST /decrypt
  │                polls proxy for actions → POSTs them to :8080/action
  │                polls GET :8080/state → folds it into attested state
  ├ extension      :8080 POST /action, GET /state
  └ poller         background goroutine, one observation every 2 min

redis              the proxy's action queue
```

---

## 3. Repo layout

| Path | What it is |
| --- | --- |
| `cmd/`, `internal/`, `pkg/` | the extension implementation (the Go module at the repository root) |
| `contracts/` | Foundry project that compiles the upstream `ITeeOracle*` interfaces straight out of a sibling flare-smart-contracts-v2 checkout — nothing is deployed from this repo |
| `tools/` | deployment, registration and the keeper — **language-neutral**, talks wire format only |
| `deploy/scripts/` | setup, start/stop, the three `oracle-*.sh` operator commands, and the two `upload-*.sh` credential uploads |
| `contracts/test/` | Foundry wire-vector tests (the ABI encodings both Go codecs assert against) |
| `docs/` | spec, rationale, findings, flows, deployment |

The split that matters: the root module is one possible implementation, `tools/` and `contracts/`
are the spine. `tools/pkg/feedupdate` deliberately re-declares the wire layout instead
of importing `internal/wire/feedupdate`, so the tooling stays honest about the contract, and
both are pinned to the same Solidity-generated fixtures in `deploy/testdata/wire`.

---

## 4. The Go extension — package map

Roughly bottom-up. "Flow" refers to §6.

| Package | Owns | Key symbols | Used in |
| --- | --- | --- | --- |
| `internal/config` | **pinned facts**: op-type names, chain set + token addresses, Hex URLs, all deadlines, price decimals | `Chains`, `LookupChain`, `ObservationInterval`, `MaxObservationAge`, `BondsSource`/`CashSource` | everywhere |
| `internal/failure` | the *closed* public error vocabulary | `Failure` (unexported fields, no constructor), `Class*`, `All` | everywhere |
| `internal/observation/decimal` | exact money parsing (`"25,452,544.52"` → `*big.Rat`), validates comma grouping instead of stripping it | `ParseAmount` | C |
| `internal/observation/price` | pure arithmetic, no I/O | `Compute`, `ComputeWithoutCash`, `Scale` | C |
| `internal/observation/supply` | per-chain `totalSupply()` over N RPC providers with a quorum procedure | `Read` `:130`, `agreedBlock` `:183`, `agreedBlockTime` `:253`, `vote` `:374`, `Total` `:437` | C |
| `internal/observation/backing` | Hex Trust HTTP reserve reads | `Source` iface, `New` `:82`, `Fetch` `:130` | C |
| `internal/wire/feedupdate` | the signed **feed update** payload's ABI layout, and the `GET_FEED` request envelope | `EncodeFeedRequest`, `DecodeFeedRequest`, `EncodeUpdate`, `SigningMessage`, `EncodeData`, `DecodeData`, `Prefix` | D |
| `internal/wire/readiness` | the signed **readiness** payload's ABI layout — a separate domain, so an answer can never be reinterpreted as a feed update | `EncodeBody`, `EncodeData`, `DecodeData`, `Domain` | D2 |
| `internal/teenode` | HTTP client for tee-node's local `/sign` + `/decrypt` | `Sign` `:82`, `Decrypt` `:106`, `TeeID` `:184`, `NormalizeV` `:158` | B, D, D2 |
| `internal/provisioning/envelope` | verifies M-of-N admin-signed provisioning envelopes | `Verifier.Verify` `:291`, `EncodeBody`, `AdminSet` | B |
| `internal/provisioning/store` | the only mutable state: provider URLs, admin sets + credentials → `oracle.Config`; also the wire codec for the ABI-encoded configuration payloads | `ApplyEndpoints` `:166`, `ApplyAdmins` `:223`, `ApplySecret` `:406`, `Readiness` `:541`, `Resolve` `:588`, `validate` `:706` | A, A2, B, C |
| `internal/observation` | orchestrates **one** observation: ordering, deadlines, health map | `Observe` `:124`, `Sign` `:219`, `readAllChains` `:276`, `readBacking` `:319`, `checkWindow` `:371` | C, D |
| `internal/observation/poller` | runs `Observe` on a timer, caches the latest, enforces staleness, and answers readiness | `Poller.Run` `:76`, `Latest` `:133`, `Report` `:159`, `ReadinessReport` `:207` | C, D, D2 |
| `internal/extension` | HTTP surface + action routing | `processAction` `:185`, `processTeeOracle` `:206`, `processGetFeed` `:268`, `processGetReadiness` `:318`, `processSetEndpoints`/`processSetAdmins`/`processSetSecret` (provisioning.go), `stateHandler` `:125` | A, B, D, D2 |
| `pkg/types` | the **public** `/state` and readiness-payload shapes | `State`, `Readiness`, `ReadinessReport`, `StateResponse` | D, D2 |
| `pkg/server` | wires everything together | `StartExtension` `:25` | boot |

### Binaries (`cmd/`)

| Binary | For |
| --- | --- |
| `cmd/docker` | **production** — what `Dockerfile` builds (tee-node + extension in one process) |
| `cmd` | conformance harness only — stub mode, unsigned $1.00, no sources |
| `cmd/devserver` | local dev: real provisioning + observation, no chain |
| `cmd/mocknode` | fake tee-node `/sign` `/decrypt` `/info` for local dev |
| `cmd/start-tee` | host-process runner behind `start-services.sh --local` |
| `cmd/usdxprov` | ops CLI: `build` / `sign` / `submit` a `SET_SECRET` envelope |

---

## 5. Boot & wiring

`pkg/server/server.go:25` is the whole dependency graph in 10 lines:

```
teenode.New(signPort)                     ── talks to tee-node on localhost
   └─► envelope.NewVerifier(chainID)     ── admin signature check
         └─► store.New(verifier, node)   ── the config Store
               ├─► poller.New(store, node)   ── Poller: Resolver = store, Signer = node
               └─► extension.NewWithProvisioner(ports, poller, store)
go poller.Run(ctx)                        ── the engine starts immediately
e.Server.ListenAndServe()
```

The extension boots **unprovisioned** and that is normal — endpoints, the admin set and
credentials are all memory-only, so every restart returns it to `not_configured` until
`SET_ENDPOINTS`, `SET_ADMINS` and `SET_SECRET` land again. `SET_ADMINS` must precede
`SET_SECRET`: with no admin set installed nothing can authorise a credential, and the
verifier rejects every envelope until one is.

`extension.NewWithProvisioner` panics on a nil observer. `NewStub` is the *only* way to
get a stub, and it is named at its single call site (`cmd/main.go`) — the scaffold's
old `New()` once silently shipped a hard-coded price to production.

---

## 6. The flows

Everything entering the extension goes through one dispatch, `processAction`
(`extension.go:190`) → `POST /action`:

```
POST /action  (utils.go actionHandler)
  └─ processAction
       └─ TEE_ORACLE → processTeeOracle
            ├─ GET_FEED        → processGetFeed        [Flow D]  requires type = instruction
            ├─ GET_READINESS   → processGetReadiness   [Flow D2] requires type = direct
            ├─ SET_ENDPOINTS   → ApplyEndpoints        [Flow A]  requires type = instruction
            ├─ SET_ADMINS      → ApplyAdmins           [Flow A2] requires type = instruction
            ├─ SET_SECRET      → ApplySecret           [Flow B]  requires type = direct
            └─ anything else   → HTTP 501
```

Every command shares the one `TEE_ORACLE` op type — the old op-type split is gone — so
the channel check is a security control, not bookkeeping: each command states its own
guard rather than inheriting one. `SET_ENDPOINTS` or `SET_ADMINS` over `direct` would
skip the sender's `onlyGovernance` check — for `SET_ADMINS` that means anyone reaching
the proxy could install their own admin set and provision through it; `SET_SECRET` over
`instruction` would put a credential ciphertext into permanent public storage.

`GET_FEED` is **instruction-only**. It grants nothing — it signs an observation the
enclave already made on its own timer, and the store verifies that signature however
the request arrived — so serving it over `/direct` would be safe in the narrow sense.
It is refused there anyway, because the on-chain path is the *permissionless* one:
anyone can pay the instruction fee and get an update published, and a path that is
only exercised when the cheap one is unavailable is a path nobody notices has broken.
One channel, used every cycle. `GET_READINESS` is direct-only for the mirror-image
reason: an operational status report whose answer is stale within minutes is not worth
a fee and a permanent calldata record.

### Flow A — `SET_ENDPOINTS` (which RPC sources we read)

```
governance → TeeOracleInstructionsSender.setEndpoints(feedId, teeIds, groups, claimBack)
          (upstream contract; onlyGovernance — tools/cmd/send-endpoints encodes the
           call and either sends it directly, while the caller is still governance,
           or hands the calldata to the governance executor)
          latestEndpointsHash[feedId] = keccak256(payload)            ← recorded BEFORE dispatch
          registry.sendInstructions(...), one instruction per target machine
            ↓ cosigned → proxy → tee-node → POST /action
        extension.processSetEndpoints                       provisioning.go:27
        store.ApplyEndpoints                           store.go:166
          ABI-decode Endpoints{version, feedId, groups}; feedId must be ours
          validate()  store.go:706
            · chain name must be one of config.Chains, and ALL of them must be present
            · 1 ≤ threshold ≤ provider count
            · "hex_api_key" may NOT be used as a provider secret
            · https only; {secret} may not appear in scheme or host
          endpointsHash = keccak256(payload AS RECEIVED)     ← never recomputed from the struct
```

That hash rides in every signed update, and the store compares it against
`latestEndpointsHash(feedId)` on the sender. Without it, the chain would record
what was *sent*, never what a machine is *running*.

### Flow A2 — `SET_ADMINS` (who may provision this machine)

```
governance → TeeOracleInstructionsSender.setAdmins(feedId, teeIds, roles, claimBack)
          latestAdminsHash[feedId] = keccak256(payload)               ← recorded BEFORE dispatch
          registry.sendInstructions(...), one instruction per target machine
            ↓ cosigned → proxy → tee-node → POST /action
        extension.processSetAdmins                          provisioning.go:44
        store.ApplyAdmins                              store.go:223
          ABI-decode Admins{version, feedId, roles}; feedId must be ours
          validateAdmins()
            · version must be non-zero AND strictly greater than the last applied
            · backing:   MinBackingThreshold (1)  ≤ threshold ≤ min(count, 16)
            · providers: MinProviderThreshold (2) ≤ threshold ≤ min(count, 16)
            · count ≤ MaxAdmins (32); the threshold ceiling is MaxThreshold (16),
              the same bound Verify puts on the signatures an envelope may carry —
              a higher threshold would install and then be unsatisfiable forever
            · every entry a parsable, non-zero hex address, distinct within its set
          envelope.Verifier.SetAdmins(backing, providers)   ← both, or neither
          adminsHash = keccak256(payload AS RECEIVED)        ← never recomputed from the struct
```

The payload carries **two** signer sets. `config.HexAPIKeyRef` is authorised by the
`backing` set (1-of-N allowed, because it authenticates a pinned endpoint by header);
every other ref by the `providers` set (2-of-N minimum, because those credentials are
substituted into URLs `SET_ENDPOINTS` chooses). The ref-to-set mapping is pinned at
`NewVerifier` and never read from the payload, and a signer from one set does not count
toward the other's threshold — see rationale §9.

Same shape as Flow A otherwise, and the same enforcement: the hash rides in every signed
update and the store compares it against `latestAdminsHash(feedId)` on the
sender. The version counter is the one addition — the admin sets are the root of trust
for every credential that follows, so a captured `SET_ADMINS` must not be able to
reinstate a rotated-out signer. The sender assigns versions per feed
(`adminsVersion(feedId)`), and the machine refuses anything not strictly newer than the
last applied.

Rotation does **not** reset the per-ref envelope versions in the verifier: those are
anti-rollback state independent of who signs, and clearing them would let an envelope
the outgoing admins signed be replayed against the incoming set.

### Flow B — `SET_SECRET` (the credentials)

Three steps, three different people, chain never touched. Two uploads, because
the ref selects the admin set and the two sets are held by different parties:
`deploy/scripts/upload-hex-key.sh` for `hex_api_key` (backing, ≥1 signature) and
`deploy/scripts/upload-rpc-keys.sh` for every RPC credential (providers, ≥2). Both gate
on `tools/cmd/verify-tee` first, and read every secret from the environment
rather than argv, where `ps` would expose it.

```
admin laptop (cmd/usdxprov)
  usdxprov build    GET proxy/info → TEE public key; ciphertext = ECIES(pubkey, secret)
                    body = abi(domain, chainId, teeId, version, issuedAt, ref, payloadHash)
  usdxprov sign     admin #1, then admin #2   (refuses a duplicate signer)
  usdxprov submit   POST proxy/direct         (must be within envelope.Freshness —
                                               1 hour — of issuedAt)
    ↓ tee-node polls the direct queue → POST /action (type = direct)
enclave
  store.ApplySecret                       store.go:322
    applyMu held for the whole call            ← the replay defence needs this
    teenode.TeeID()   signs a fixed probe, recovers its own address
    envelope.Verify()                         envelope.go:232
      chainId · teeId · payloadHash · freshness · version strictly > last
      · DISTINCT signatures up to the threshold of the set the ref selects
        (backing ≥1, providers ≥2), low-s only
      EVERY failure returns the same error — no probing oracle
    teenode.Decrypt() POST localhost:9090/decrypt   ← in-enclave
    reject a value containing  space \t \r \n / ? # &
    secrets[ref] = value                       ← memory only, lost on restart
```

### Flow C — the observation loop (the engine)

Runs on a timer whether or not anyone asks for a price. **This is the only code that
touches the network for data.**

```
poller.Poller.Run — every 2 min                poller.go:69
  ├─ store.Open()
  │     re-run every tick (never cached), so a rotated credential needs no restart
  │     builds RPC clients, bonds source, cash source (nil if no secret yet)
  │     provider names are POSITIONAL — "rpc_flare_1" — so nothing an operator
  │     writes can reach the public /state
  │
  └─ observation.Observe — 20s deadline              observation.go:104
        S  readAllChains, all chains concurrently      observation.go:204
             supply.Read per chain                     supply.go:163
               agreedBlock: ask every provider for its head, sort DESCENDING and
                 pin the Quorum-th highest — the highest block at least Quorum of
                 them can serve. NOT the minimum: one provider answering with a
                 two-week-old head dragged the minimum back with it, and every
                 honest provider then corroborated the stale supply at that block
                 perfectly. Findings §24.
               agreedBlockTime: that block's own header timestamp, also agreed by
                 Quorum, then refused if older than the chain's pinned MaxBlockAge
                 (2 min Flare/Songbird, 3 min Ethereum) — the only check that works
                 at Quorum 1, and the only one that notices a stalled chain
               readDecimals + readTotalSupply at that EXACT block number
               require `Quorum` IDENTICAL answers, and a UNIQUE winner (never a
                 median — a read at a fixed block is deterministic, so
                 disagreement means someone is wrong; and a tie fails closed,
                 because otherwise the provider ORDER in the payload would pick
                 the supply figure)
               amount = raw / 10^decimals, exact in big.Rat
             any chain failing fails the whole observation (a partial sum is a
             wrong supply, not a smaller one)
        B  readBacking, both endpoints concurrently    observation.go:247
             backing.Fetch → 2 attempts, no redirects ever, unknown JSON fields
             rejected, comma grouping validated not stripped, last_update parsed
             and refused only if it is in the FUTURE — there is no bound on how
             old it is, because both endpoints republish on an on-chain event
             rather than on a schedule (rationale §2)
             bonds failing is FATAL; cash failing is not
        checkWindow(30s) across every read timestamp   observation.go:295
             read skew only; in practice the 20s deadline is the tighter bound
        price.Compute → min(1, B/S) × 10⁸, rounded half-up
             no cash? price.ComputeWithoutCash — publishes only if the answer
             provably cannot depend on the missing figure (i.e. already at par)
        observedAt = wall-clock now             ← NOT the oldest input; the store
                                                  requires strict monotonicity
  └─ success → cached as `last`
     failure → recorded; the PREVIOUS observation stays and ages out after 10 min
```

### Flow D — `GET_FEED` (must finish in 2 seconds)

tee-node allows an extension **2 seconds** per action, and a full observation measures
2.0–2.9s. That is why C and D are separate: a request costs exactly one signing round
trip.

```
anyone → TeeOracleInstructionsSender.requestFeedUpdate(feedId, teeIds)   (upstream)
           dispatches to the named machines; the instruction message is the
           ABI-encoded FeedUpdateRequest{feedId}
    ↓ proxy → tee-node → POST /action
  extension.processGetFeed                                extension.go:268
    decode the FeedUpdateRequest; the feed must be ours, else UnexpectedPayload
    poller.Poller.Report(requestedAt)                    poller.go:159
      Latest()  ─ fails closed if the observation is older than 10 min
      observation.Sign()                                       observation.go:219
        update = FeedUpdate(extensionId, feedId, value, decimals, observedAt,
                            endpointsHash, adminsHash)
        messageHash = keccak256(abi(TEE_ORACLE_FEED, chainId, keccak256(abi(update))))
        teenode.Sign(messageHash preimage) → POST localhost:9090/sign
                                   (tee-node applies EIP-191; wire is base64, not hex)
        NormalizeV: 0/1 → 27/28, so the store can use stock OpenZeppelin
        feedupdate.EncodeData → abi(update, signature)
    ActionResult{status: 1, data}
    ↓ tee-node → proxy → served at GET /action/result/<id>
```

### Flow D2 — `GET_READINESS` (the machine's signed account of itself)

Direct only, no fee, no transaction. Same 2-second budget as `GET_FEED`, and met the
same way: one signing round trip and no I/O beyond it — nothing here observes, resolves
configuration or dials an upstream.

```
operator/keeper → POST proxy/direct { GET_READINESS, 0x01 }
    ↓ tee-node → POST /action (type = direct)
  extension.processGetReadiness                           extension.go:306
    require message == exactly 0x01, else UnexpectedPayload
    both halves must exist (observer AND provisioner), else NotConfigured
    poller.Poller.ReadinessReport(id, store.Readiness())  poller.go:163
      assembled behind the poller's lock, where the observation state and the
        signer both live; the caller supplies the provisioning half it owns
      payload = JSON types.ReadinessReport — readiness, lastObservedAt,
        observationAge, wouldSign, observations/failures, lastErrorClass,
        sourceHealth, chainId
      body    = abi(READINESS_DOMAIN, instructionId, timestamp, keccak256(payload))
      teenode.Sign(body) → readiness.EncodeData(body, sig, payload)
```

Three properties are the whole point:

- **It never fails closed.** An unprovisioned or stale machine answers with a report
  *saying so*. "Nothing is configured" is the answer, not a failure to produce one —
  only an encoding or signing fault is an error here.
- **Its domain differs from the price report's.** The identity key signs both, so a
  readiness answer can never be reinterpreted as a price however the bytes are routed.
- **It does not touch the `/state` counters.** Those describe prices *served*; counting
  status polls would make monitoring look like production traffic.

`wouldSign` is the field that earns its keep: `tools/pkg/keeper.wouldRefuse` reads it
before paying for an on-chain request, so a machine whose newest observation has aged
out costs a free direct action instead of a registry fee. It is the one thing the
keeper still sends over `/direct`. It fails **open** — an
unreachable machine or a build without the command means "not known", never "do not
try". Consumers: `keeper`, `fleet-status`, `send-endpoints`, `send-admins`.

The report is verified but still a *self*-report; see
[`usdx-oracle-readiness-query.md`](usdx-oracle-readiness-query.md) for what that does
and does not settle.

### Flow E — the keeper relay (`tools/`)

```
tools/cmd/keeper → keeper.Run (timer, single-flight) → keeper.RunCycle   cycle.go
  0. pendingNonceGap  — a stuck tx blocks everything behind its nonce
     wouldRefuse      — ask every machine over GET_READINESS first;         readiness.go
                        all refusing → request_skipped, no fee spent
  1. TRIGGER   instrutils.RequestFeedUpdate(feedId, teeIds) → instructionId
                 the keeper names the target machines (-tee); fee = opFee × count
  2. COLLECT   pollResults: GET proxy/action/result/<id>, backoff 1s → 16s
                 every named machine's proxy — each files its own result there —
                 until all have answered or the poll timeout passes
                 404 = pending, not an error
                 status 0 → that machine is skipped and its error class recorded;
                 inventing a price is the one thing the keeper must never do
  3. RELAY     feedupdate.Decode each → chooseBatch (one per signer; the largest
                 group agreeing on observedAt) → Preflight (feed-level commitments,
                 requiredSignatures, the ratchet, then an eth_call rehearsal of the
                 submission that names the failing guard)
                 → store.submitFeedUpdates(SignedFeedUpdate[])
                 → wait for the receipt; a timeout is not chased, the next cycle
                   supersedes it
  → Record with an Outcome: accepted · race_lost · stale_submission · rejected …
```

`race_lost` is a **success** — with ≥2 keepers exactly one submission wins per cycle,
so it must never alert. That taxonomy lives in `tools/pkg/keeper/record.go`.

### Flow F — on-chain acceptance, then the consumer read

`TeeOracleFeedStore.submitFeedUpdates(SignedFeedUpdate[])`, in execution order:

```
 1  batch shape — count ≥ requiredSignatures (NotEnoughSignatures) and ≤ 32
    (TooManySignatures); one common observedAt (ObservedAtMismatch); the
    contributors' decimals within one spread (DecimalsSpreadTooBig)
 2  the two values bracketing the median lie within the policy's bound —
    maxSpreadBIPS of |median| plus maxSpreadAbsolute (SpreadTooBig)
 3  observedAt strictly greater than stored (NotNewer) and < block.timestamp
    (TooFarAhead) — checked once, since the whole batch shares one stamp
 4  sender.latestEndpointsHash(feedId) and latestAdminsHash(feedId) published
    (NoEndpointsPublished / NoAdminsPublished)
 5  per contributor, in submission order: verifyTeeSignature(extensionId, sig,
    messageHash) recovers the signer over the EIP-191 digest — on THIS extension
    AND PRODUCTION, extension not emergency paused; not seen earlier in the batch
    (DuplicateTeeId); update.extensionId == store's (WrongExtensionId);
    update.feedId == store's (WrongFeedId); endpointsHash == latest
    (StaleEndpoints) ← closes Flow A; adminsHash == latest (StaleAdmins) ← closes Flow A2
    ────────► store the median at the finest int32 scale it fits · emit
              FeedUpdated(value, decimals, observedAt, teeIds[]) · emit FeedOutliers
              naming the machines whose value lay beyond the bound
```

The store keeps **no per-machine bookkeeping** — the old `lastReport[teeId]` /
`seenTees` / `isProvenConfigured` are gone. What was "commissioning legible
on-chain" is now assembled from the sender's per-feed commitments
(`latestEndpointsHash` / `latestAdminsHash` / versions, plus `isTeeIdConfigured` per
machine), the machine's own `GET_READINESS`, and the `FeedUpdated` event that names every
contributing machine. See `fleet-status`, which reconciles those three.

```
FtsoV2 → TeeOracleFeedStore.getCurrentFeed()      payable
           reverts NoValuePublished before the first accepted update
           otherwise → (value int256, decimals, observedAt clamped to block.timestamp)
           read fee resolves through FeeCalculator; msg.value → feeDestination
```

The store implements `IICustomFeed` and registers with FtsoV2 directly — no
separate adapter. The old `paused` / `invalidated` split is gone; withdrawing a bad
value is now the extension-level emergency pause, which blocks submissions but not
reads.

---

## 7. Contracts

Both contracts are upstream's generic TEE oracle contracts — this repo deploys
nothing; the Go bindings are generated from the interfaces in a sibling
flare-smart-contracts-v2 checkout (see `contracts/`).

| Contract | Entry points | Notes |
| --- | --- | --- |
| `TeeOracleInstructionsSender` | `requestFeedUpdate(feedId, teeIds)` payable, **anyone** · `setEndpoints(feedId, teeIds, groups, cb)` and `setAdmins(feedId, teeIds, roles, cb)` payable, **governance** · reads: `latestEndpointsHash`/`latestAdminsHash`/`…Version` (per feed), `getFeedIds()`, `isTeeIdConfigured(feedId, teeId)` | the extension's registered instructions sender; assigns the config version and records the commitments before dispatch |
| `TeeOracleFeedStore` | `submitFeedUpdates(SignedFeedUpdate[])` **anyone** · `getCurrentFeed()` payable (also `observedAt()`, `getSubmissionPolicy()`, `calculateFee()`) · governance: `setSubmissionPolicy`, `setFeeDestination` | implements `IICustomFeed` and registers with FtsoV2 directly; verifies every signature through `Fdc2Verification`, takes the median of a batch and bounds its spread |

---

## 8. `tools/` map

| Path | Role |
| --- | --- |
| `pkg/support` | chain client, key, ABI/revert plumbing shared by every tool |
| `pkg/configs` | env-var defaults so tools run flag-free from a sourced `.env` |
| `pkg/feedupdate` | decodes the signed `FeedUpdate` and recovers the signer — the wire contract, re-declared independently |
| `pkg/oraclepayload` | builds/predicts the ABI-encoded `Endpoints`/`Admins` payloads and their hashes |
| `pkg/keeper` | `Run` (timer) · `RunCycle` · `PollResult` · `Preflight` · `Record`/`Outcome` |
| `pkg/utils/instructions.go` | `RequestFeedUpdate`, `EncodeSetEndpoints`/`EncodeSetAdmins` (+ `Publication`), fee reads (fees are **read**, never hard-coded) |
| `pkg/fccutils` | FCC registration, governance, policy-consistency, revert decoding |
| `pkg/contracts/*` | generated bindings from the upstream interfaces (`deploy/scripts/generate-bindings.sh`) |
| `pkg/validate` | the deploy/verify check suite behind `verify-deploy` |

---

## 9. Four cross-cutting rules

**1. The failure vocabulary is closed.** `failure.Failure` has unexported fields and no
exported constructor, so a value can only be one of the ~24 declared in
`internal/failure`. `ActionResult.Log` and `GET /state` are both world-readable, and an
ordinary Go error carries exactly what must not appear there — an API key inside a
`*url.Error`, a reserve figure inside a parse error. This is enforced by the compiler,
not by review.

**2. Fail closed, always.** A source down, a value implausible, an observation older
than 10 minutes: publish nothing and let the on-chain timestamp age. A frozen feed is
the designed failure mode; a confidently wrong price is not recoverable downstream.
**Consumers must check the timestamp.**

**3. Pinned vs runtime.** Everything in `internal/config` is compiled in and therefore
covered by the registered code hash — chain set, token addresses, Hex URLs, every
deadline. Only RPC provider URLs and credentials are configurable at runtime, and they
arrive through Flows A and B respectively.

**4. Nothing operator-supplied reaches a public surface.** Provider names are
positional (`rpc_flare_1`), backing source names are identifiers not URLs (rejected at
construction if they look like one), and readiness names secret *refs* never values.

---

## 10. Where to start reading

```
pkg/server/server.go            the whole dependency graph, 10 lines
internal/extension/extension.go the HTTP surface and the router
internal/observation/observation.go       one observation, start to finish
internal/observation/poller/poller.go     why observing and signing are separate
internal/wire/feedupdate/feedupdate.go       the FeedUpdate encoder + SignedPayload digest
tools/pkg/keeper/cycle.go          the relay, end to end
```

The on-chain guards live upstream (`TeeOracleFeedStore.submitFeedUpdates`); the
upstream interfaces, compiled via `contracts/`, are what the Go binds against.

Every package's doc comment carries the reasoning; `docs/usdx-oracle-rationale.md`
carries the long form.

| Question | Command |
| --- | --- |
| Do the real sources yield a sane price? | `./deploy/scripts/oracle-observe.sh` |
| Does the wire contract hold? | `go test ./internal/extension/ -run TestConformance` |
| Does the whole chain work? | `./deploy/scripts/oracle-cycle.sh` |
| What is it publishing right now? | `./deploy/scripts/oracle-status.sh` |

---

## 11. Gotchas worth knowing before you touch it

- **The admin sets are published, not baked, and are memory-only.** A machine starts with
  none and rejects every `SET_SECRET` until `SET_ADMINS` lands, so they must be republished
  after every restart and *before* any credential. There are TWO sets and the envelope's
  ref picks one: `hex_api_key` → `backing` (1-of-N allowed), everything else → `providers`
  (2-of-N minimum). The checked-in `deploy/config/admins/dev-wellknown-keys.json` names private
  keys 1, 2 and 3 — local and testnet only.
- **`/state` counters count served `GET_FEED` actions, not the poller.**
  `observe.Poller.Stats()` exists but is never called — a machine nobody queries reports
  an empty health map while its poller may be failing every 2 minutes. Alert on on-chain
  feed *age*, not on these counters.
- **Stub mode is indistinguishable from a healthy fully-backed feed** on the wire — both
  print `$1.00000000`. `Extension.StubMode()` is what tells them apart.
- **The report has no target binding.** A genuinely signed report is replayable into any
  other store registered under the same extension. Accepted trade-off, findings §18.
- **`observedAt` is a global ratchet** across all machines: one enclave with a fast clock
  suppresses the others until chain time catches up.
