# USDX Oracle — Architecture & Flows

A short map of how the pieces fit together: what runs where, what the entrypoints
are, and which code each flow goes through.

Companion docs: `usdx-oracle-spec.md` (design intent), `usdx-oracle-findings.md`
(what turned out to be true), `usdx-oracle-keeper.md` (the relay).

---

## What it does

Publishes a **USDX price** — `min(1, backing / supply)` — as an FTSO custom feed.
The price is computed inside a TEE from live off-chain data, signed by the
enclave's identity key, and relayed on-chain where a store contract verifies the
signature against the TEE machine registry.

Nobody in the delivery path can alter the number.

---

## Processes

One Compose stack, three containers. Note that **tee-node is a client of our
extension** — it calls in over HTTP, it does not link us as a library.

```
ext-proxy (tee-proxy)
  :6663 internal → host 6673    TEE machines poll here for work
  :6664 external → host 6674    PUBLIC: /info, /action/result/<id>, /direct

extension-tee ── one process, built from cmd/docker
  tee-node       :5500 config, :7701 POST /sign + POST /decrypt
                 polls the proxy for actions → POSTs them to :7702
                 polls GET :7702/state → folds it into the attested state
  extension      :7702 POST /action, GET /state
  poller         background goroutine, observes every 2 minutes

redis            the proxy's action queue
```

`pkg/server/server.go:35` wires it: `teenode.Client` → `provision.Verifier` →
`store.Store` → `observe.Poller` → `extension.Extension`.

---

## Entrypoints

### Binaries

| Entrypoint | For | Notes |
| --- | --- | --- |
| `cmd/docker` | **production** | what `Dockerfile` builds |
| `cmd/` | `docker` in production; `devserver` + `mocknode` locally | no stub build exists; conformance is `TestConformance` in `internal/extension` |
| `cmd/devserver` | local dev | real provisioning + observation, no chain |
| `cmd/mocknode` | local dev | fake tee-node `/sign` `/decrypt` `/info` |
| `cmd/start-tee` | `start-services.sh --local` | no Docker |
| `cmd/usdxprov` | ops | build / sign / submit a `SET_SECRET` envelope |
| `tools/cmd/keeper` | ops | the relay timer (`-tee` names the target machines) |
| `tools/cmd/send-endpoints` | ops | publish `SET_ENDPOINTS` to the extension's active machines (`-feedId`, `-config`) |
| `tools/cmd/send-admins` | ops | publish `SET_ADMINS` to the fleet |
| `tools/cmd/fleet-status` | ops | reconcile on-chain vs live config per machine |
| `tools/cmd/verify-tee` | ops | gate before a credential: is this proxy fronting an accepted enclave? |
| `tools/cmd/*` | one-shot ops | register-tee, set-governance, allow-tee-version… (the contracts themselves deploy upstream) |

### Scripts

| Script | For |
| --- | --- |
| `deploy/scripts/upload-hex-key.sh` | install `hex_api_key` — backing set, ≥1 signature |
| `deploy/scripts/upload-rpc-keys.sh` | install the RPC credentials — providers set, ≥2 signatures |
| `deploy/scripts/oracle-*.sh` | observe, cycle, status — the operator commands |

### HTTP — exactly two ways into the extension

```
POST /action    internal/extension/utils.go:16       every instruction & direct action
GET  /state     internal/extension/extension.go:138  polled by tee-node → ends up public
```

### On-chain

The sender and store are upstream's generic TEE oracle contracts
(`TeeOracleInstructionsSender` + `TeeOracleFeedStore`); this repo deploys
nothing.

```
TeeOracleInstructionsSender.requestFeedUpdate(feedId, teeIds)        payable, ANYONE
TeeOracleInstructionsSender.setEndpoints(feedId, teeIds, groups, cb) payable, governance
TeeOracleInstructionsSender.setAdmins(feedId, teeIds, roles, cb)     payable, governance
TeeOracleFeedStore.submitFeedUpdates(SignedFeedUpdate[])            ANYONE
TeeOracleFeedStore.getCurrentFeed()                                  FtsoV2 / consumers (payable)
```

---

## The action router

Everything entering the extension goes through one dispatch. Every command now
sits under a single op type, `TEE_ORACLE`:

```
POST /action
  └─ processAction                        extension.go
       ├─ TEE_ORACLE / GET_FEED       → processGetFeed         [Flow D]
       │     requires action type = instruction
       ├─ TEE_ORACLE / GET_READINESS  → processGetReadiness
       │     requires action type = direct
       ├─ TEE_ORACLE / SET_ENDPOINTS  → ApplyEndpoints         [Flow A]
       │     requires action type = instruction
       ├─ TEE_ORACLE / SET_ADMINS     → ApplyAdmins            [Flow A2]
       │     requires action type = instruction
       ├─ TEE_ORACLE / SET_SECRET     → ApplySecret            [Flow B]
       │     requires action type = direct
       └─ anything else               → HTTP 501
```

With one op type for everything, the per-command **action-type guard is the only
fence** between a permissionless read and a governance-published write — there is
no longer a second, coarser op-type fence. The channel check matters:
`SET_ENDPOINTS` or `SET_ADMINS` over `direct` would skip the on-chain governance
check, and `SET_SECRET` over `instruction` would put a credential ciphertext in
permanent public storage.

---

## Flow A — SET_ENDPOINTS (which sources we read)

```
governance → TeeOracleInstructionsSender.setEndpoints(feedId, teeIds, groups, cb)
          version = ++endpointsVersion                ← contract-assigned, one per publication
          message = abi.encode(Endpoints{version, feedId, groups})
          latestEndpointsHash[feedId] = keccak256(message)   ← per feed, recorded before dispatch
          endpointsVersion[feedId]    = version               (isTeeIdConfigured tracks which
                                                               machines were sent it)
          flareTeeManager.sendInstructions(...)
             ↓  cosigned → proxy → tee-node → POST /action
        store.ApplyEndpoints
          ABI decode into Endpoints{version, feedId, groups}
          the feedId must name THIS machine's feed, else rejected
          validate():
            · group tag must be a known group (a pinned chain, or hex_cash)
            · 1 ≤ threshold ≤ endpoint count
            · "hex_api_key" may NOT be a provider secret
            · https only; {secret} may not appear in scheme or host
            · EVERY pinned chain group must be present
            · hex_cash must be exactly one PRIVATE endpoint under hex_cash_url
            · version strictly greater than the last applied
          endpointsHash = keccak256(payload)           ← hashed as received
```

Groups are the unit now — a tagged (`bytes32`) list of endpoints, each endpoint
PUBLIC (`url`) or PRIVATE (a salted `urlHash` commitment plus a `secretRef` that
delivers the URL off-chain). The chain RPC groups are public; the restricted
`hex_cash` reserve endpoint is private, so its URL never lands in calldata.

That hash goes into every signed update, and the store compares it against
`latestEndpointsHash[feedId]`. Without it, on-chain config would record
what was *sent*, never what a machine is *running*.

---

## Flow A2 — SET_ADMINS (who may provision this machine)

```
governance → TeeOracleInstructionsSender.setAdmins(feedId, teeIds, roles, cb)
          version = ++adminsVersion                   ← contract-assigned, one per publication
          message = abi.encode(Admins{version, feedId, roles})
          latestAdminsHash[feedId] = keccak256(message)      ← per feed, recorded before dispatch
          adminsVersion[feedId]    = version                  (isTeeIdConfigured tracks which
                                                               machines were sent it)
          flareTeeManager.sendInstructions(...)
             ↓  cosigned → proxy → tee-node → POST /action
        store.ApplyAdmins
          ABI decode into Admins{version, feedId, roles}
          the feedId must name THIS machine's feed, else rejected
          convertAdmins():
            · version non-zero AND strictly greater than the last applied
            · role tags are "backing" and "providers"; both required; unknown rejected
            · backing:   1 ≤ threshold ≤ min(admin count, 16), count ≤ 32
            · providers: 2 ≤ threshold ≤ min(admin count, 16), count ≤ 32
              (16 is the signature cap Verify enforces: a higher threshold
               installs cleanly and can then never be met)
            · every entry parsable, non-zero and distinct within its role
          verifier.SetAdmins(roles)                   ← all roles, or none
          adminsHash = keccak256(payload)             ← hashed as received
```

The operator no longer picks the version — the contract assigns it, so a
publication cannot be replayed at a stale one. The payload carries role-TAGGED
signer sets and the envelope's ref picks one:

```
ref == hex_api_key   → backing set     (1-of-N allowed)
everything else      → providers set   (2-of-N minimum)
```

The ref→role mapping is pinned in the build, never read from the payload —
otherwise the weaker set could claim the stronger set's credentials. Signers from
the other set do not count toward a threshold, and an unrecognised ref falls under
the *stronger* set.

Same enforcement as Flow A: the hash goes into every signed update and the store
compares it against `latestAdminsHash[feedId]`.

The verifier starts with **no** admin sets and rejects every envelope until both
are installed, so Flow A2 must precede Flow B — on first provisioning and again
after every restart, since they are memory-only.

---

## Flow B — SET_SECRET (the credentials)

Three steps, three different people, and the chain is never touched.

Run twice, by different parties, because the ref decides which admin set
authorises the envelope and the two sets are held by different people:

```
deploy/scripts/upload-hex-key.sh    hex_api_key      backing set,   ≥1 signature
deploy/scripts/upload-rpc-keys.sh   every other ref  providers set, ≥2 signatures
```

Both gate on `tools/cmd/verify-tee` before anything is encrypted, and take every
secret from the environment rather than argv. The RPC script uploads whatever
the machine reports as `missingSecretRefs`, one envelope per ref, and hands the
envelope off to a second admin when it cannot meet the threshold itself. Under
the scripts it is the same three commands:

```
admin laptop
  usdxprov build    GET proxy/info → public key → teeId derived from it
                    ciphertext = ECIES.Encrypt(pubkey, secret)
                    body = abi(domain, chainId, teeId, version, issuedAt, ref, hash)
  usdxprov sign     admin #1   (refuses a duplicate signer)
  usdxprov sign     admin #2   (providers set only; backing can be 1-of-N)
  usdxprov submit   POST proxy/direct     ← must be within envelope.Freshness
                                            (1 hour) of issuedAt
     ↓  tee-node polls the direct queue → POST /action (type = direct)
enclave
  store.ApplySecret                          store.go:211
    applyMu held for the whole call               ← the replay defence needs this
    teenode.TeeID()  signs a fixed probe, recovers its own address
    envelope.Verify()                            envelope.go:209
       admin sets must be installed at all — else rejected outright
       set = backing if ref == hex_api_key else providers
       chainId · teeId · payload hash · freshness · version > last
       · ≥ set.threshold DISTINCT signatures FROM THAT SET
       every failure returns the SAME error — no probing oracle
    node.Decrypt()   POST localhost:7701/decrypt  ← in-enclave
    reject a value containing  space \t \r \n / ? # &
    secrets[ref] = value                          ← memory only, lost on restart
```

---

## Flow C — the observation loop (the engine)

Runs whether or not anyone asks for a price. Nothing else touches the network.

```
poller.Poller.Run — every 2 minutes                poller.go:114
  │
  ├─ store.Open()
  │     re-run every tick, never cached
  │     builds RPC clients, bonds source, cash source (nil if no secret)
  │     provider names are positional ("rpc_flare_1") so nothing an operator
  │     writes reaches the public readiness report
  │
  └─ observation.Observe — 20s deadline                  observation.go:104
        S   supply, all chains concurrently
              ask every provider for its head → take the Quorum-th HIGHEST
              refuse it if older than MaxBlockAge → query all at that EXACT block
              require N IDENTICAL results (never a median) and a UNIQUE
                winner — a tie fails closed rather than letting the provider
                order decide the supply figure
              amount = raw / 10^decimals, exact
        B   backing, both endpoints concurrently
              bonds failing is fatal; cash failing is not
              no redirects · unknown JSON fields rejected
              comma grouping validated, not stripped
              NO bound on how old last_update is — both endpoints republish on
                an on-chain event, not on a schedule, so an old stamp means
                nothing changed. A FUTURE stamp is still refused.
        checkWindow(30s) across all read timestamps
        price = min(1, B/S) × 10⁸, rounded half-up
              no cash? publish only if the answer provably cannot depend on it
        observedAt = now
  │
  └─ success → cached as "latest"
     failure → error recorded, the previous observation is LEFT IN PLACE
               and ages out after 10 minutes
```

**One pass per side:** each supply figure is pinned to one agreed block, and a
reserve figure changes only within ~10 minutes of an on-chain event, so neither
side has an internal race worth bracketing. `checkWindow` bounds the skew between the two. The earlier
double-read (S₁ … B … S₂, taking the larger supply) was dropped for this reason —
see rationale §2.

---

## Flow D — GET_FEED (must finish in 2 seconds)

```
anyone → TeeOracleInstructionsSender.requestFeedUpdate(feedId, teeIds)
           dispatches one instruction to EACH named machine (targets are
           explicit now — the sender no longer picks at random); every target
           must already have endpoints AND admins published for the feed
           message = abi.encode(FeedUpdateRequest{feedId})   ← one 32-byte word
     ↓  proxy → tee-node → POST /action
  processGetFeed                                    extension.go
    DecodeFeedRequest(message) — must be exactly the ABI envelope, and the
    feedId must be the one this machine serves; else rejected
    poller.Report()                                 poller.go
      Latest()  — fails closed if the observation is older than 10 min
      observation.Sign()
        update = FeedUpdate{extensionId, feedId, value, decimals, observedAt,
                            endpointsHash, adminsHash}
        dataHash    = keccak256(abi.encode(update))
        messageHash = keccak256(abi.encode(TEE_ORACLE_FEED, chainId, dataHash))
        POST localhost:7701/sign over messageHash's PREIMAGE — tee-node applies
        EIP-191; wire is base64; normalise v from 0/1 → 27/28
        data = abi.encode(update, Signature{v, r, s})
    ActionResult{status: 1, data}
     ↓  tee-node → proxy → served at /action/result/<id>
```

The signed payload is a `FeedUpdate`, not the old `PriceReport`: it names the
`extensionId` and `feedId` (cross-feed replay protection), carries `value` as an
`int32` with per-update `decimals`, and drops both the versioned domain and the
`instructionId` — the chain id now lives inside a `SignedPayload` prefix instead
of a domain string.

**Why the split from Flow C:** an observation measures 2.0–2.9s and tee-node
allows an extension 2 seconds per action. Observing inside the handler timed out
on every request. A request now costs one signing round trip.

---

## Flow E — the keeper relay

```
tools/cmd/keeper → RunCycle, sequential, never overlapping     cycle.go
  0. check for stuck transactions (a nonce gap blocks everything behind it)
     ask every machine over GET_READINESS whether it would sign at all;
     all refusing → request_skipped, no fee spent
  1. TRIGGER   requestFeedUpdate(feedId, teeIds) → instructionId
                 the keeper names the target machines (-tee); the fee is
                 operationFee × len(teeIds)
  2. COLLECT   GET proxy/action/result/<id>, backoff 1s → 16s
                 asked of every named machine's proxy — each holds its own —
                 until all have answered or the poll timeout passes
                 404 = pending, not an error
                 status 0 → that machine is skipped, its error class recorded,
                 nothing invented
  3. RELAY     decode each FeedUpdate → keep one per signer, the largest group
                 agreeing on observedAt → preflight (feed-level commitments,
                 requiredSignatures, the ratchet, then an eth_call rehearsal that
                 names the failing guard) → submitFeedUpdates(SignedFeedUpdate[])
                 ← every update relayed as decoded, signature split into {v, r, s}
                 → wait for the receipt; a timeout is not chased, the next
                   cycle supersedes it
```

The keeper holds **no authority**. Every guard is on-chain, so the worst a wrong
invocation does is waste gas.

---

## Flow F — on-chain acceptance

`TeeOracleFeedStore.submitFeedUpdates(SignedFeedUpdate[])`, in execution order:

```
 1  batch shape: count ≥ requiredSignatures and ≤ 32          (NotEnoughSignatures / TooManySignatures)
    one common observedAt across the batch                     (ObservedAtMismatch)
    the contributors' decimals within one spread               (DecimalsSpreadTooBig)
 2  the two values bracketing the median lie within the bound  (SpreadTooBig)
    — maxSpreadBIPS of |median| plus maxSpreadAbsolute, governance-set
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

Gone from the old store: `paused`/`invalidated`, the `[minPrice, maxPrice]` band,
and the `maxAge`/`maxFutureSkew` acceptance window. The only time rules left are
the strictly-increasing ratchet and "never in the future" — the price band is now
enforced in the enclave before signing, and staleness is the consumer's check.

---

## Flow G — consumer read

```
FtsoV2 → TeeOracleFeedStore.getCurrentFeed()      payable
           reverts NoValuePublished before the first accepted update
           otherwise → (value int256, decimals, observedAt clamped to block.timestamp)
           the read fee resolves through FeeCalculator and the whole msg.value is
           forwarded to the governance-set feeDestination
```

The store implements `IICustomFeed` itself and registers with FtsoV2 directly —
there is no separate `UsdxCustomFeed` adapter any more. It keeps no per-machine
bookkeeping: one global `observedAt`, one latest value. The old `paused` /
`invalidated` split is gone; withdrawal of a bad value is now the extension-level
emergency pause (which blocks submissions, not reads).

---

## The failure path

```
any fault → failure.Failure (closed vocabulary, compile-time constant text)
   ├─ recorded by the poller
   ├─ next GET_FEED → ActionResult{status: 0, log: "error: <constant text>"}
   │     → proxy /action/result/<id> → the keeper records it as an error class
   └─ GET_READINESS → signed report (lastErrorClass, sourceHealth) ← PUBLIC TO KEY HOLDERS
```

`Failure` has unexported fields and no exported constructor, so no runtime value
(an API key inside a URL error, a reserve figure inside a parse error) can reach
either public surface. That is a compile error, not a review item.

---

## Trust model

| Can move the price | Constrained by |
| --- | --- |
| the attested code | registered code hash, reproducible build |
| **Flare governance (timelocked)** | `setEndpoints`/`setAdmins` set RPC hosts, quorum and admin sets with **no allowlist** — accepted trade (finding 15); the `hex_cash` URL is a PRIVATE commitment, and `hex_bonds` is pinned in the build |
| **admin set (2-of-3)** | holds the Hex credential; cannot redirect it into a provider URL |
| Hex Trust | sole reserve source, no redundancy |
| keeper | nothing — worst case is wasted gas |
| store governance | `setFeeDestination` · extension-level emergency pause (blocks submissions, not reads) |

---

## Which command answers which question

| Question | Command |
| --- | --- |
| Do the real sources yield a sane price? | `./deploy/scripts/oracle-observe.sh` |
| Does the wire contract hold? | `go test ./internal/extension/ -run TestConformance` |
| Does provisioning work? | `./deploy/scripts/set-config.sh endpoints` then `usdxprov` |
| Does the whole chain work? | `./deploy/scripts/oracle-cycle.sh` |
| What is it publishing right now? | `./deploy/scripts/oracle-status.sh` |

---

## Open items for the audit

1. **The admin set must be published before a live feed.** Nothing is baked any
   more: a machine starts with no admins and refuses every `SET_SECRET` until
   `SET_ADMINS` installs one. The pre-production requirement is that the set
   published is a real one — `deploy/config/admins/dev-wellknown-keys.json` names
   private keys 1, 2 and 3 and is for local and testnet work only.
2. **`GET /state` carries the empty state.** `GET_READINESS` is the surface that
   reports the poller itself — the only one — so use it, and alert on on-chain
   feed *age* as well, which needs no key and no answering machine.
3. **`checkWindow` measures retrieval time, not data age.** The 30s window bounds
   when we *fetched*; the reserve figure itself carries **no** freshness bound —
   both Hex endpoints republish on an on-chain event, not a schedule, so an old
   stamp means nothing changed (see `config.BondsSource`). Note the 20s
   observation deadline is already tighter than the 30s window, so the window
   cannot fire in production; the deadline is the real bound on read skew.
4. **Feed binding in the update** — the old report had no target binding and was
   replayable into any store under the same extension. The `FeedUpdate` now names
   its `extensionId` and `feedId`, and the store rejects a mismatch
   (`WrongExtensionId`/`WrongFeedId`), so an update can no longer be replayed
   onto another feed the same extension serves.
5. **Per-feed `observedAt` ratchet** across all machines serving that feed: one
   enclave with a fast clock suppresses the others until chain time catches up.
   The store additionally requires `observedAt < block.timestamp` strictly, so a
   machine whose clock runs fast reverts `TooFarAhead` rather than advancing the
   ratchet.
6. **Rounding reversed 5 Aug.** `price` rounds half-up, not down; the bound that
   truncation bought is now held by `TestCompute_RoundingCannotHideAMaterialShortfall`.
