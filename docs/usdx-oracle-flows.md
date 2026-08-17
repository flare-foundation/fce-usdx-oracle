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

extension-tee ── one process, built from go/cmd/docker
  tee-node       :5501 config, :9090 POST /sign + POST /decrypt
                 polls the proxy for actions → POSTs them to :8080
                 polls GET :8080/state → folds it into the attested state
  extension      :8080 POST /action, GET /state
  poller         background goroutine, observes every 2 minutes

redis            the proxy's action queue
```

`pkg/server/server.go:35` wires it: `teenode.Client` → `provision.Verifier` →
`runtimecfg.Store` → `observe.Poller` → `extension.Extension`.

---

## Entrypoints

### Binaries

| Entrypoint | For | Notes |
| --- | --- | --- |
| `go/cmd/docker` | **production** | what `Dockerfile` builds |
| `go/cmd` | conformance only | stub: unsigned $1.00, no sources |
| `go/cmd/devserver` | local dev | real provisioning + observation, no chain |
| `go/cmd/mocknode` | local dev | fake tee-node `/sign` `/decrypt` `/info` |
| `go/cmd/start-tee` | `start-services.sh --local` | no Docker |
| `go/cmd/usdxprov` | ops | build / sign / submit a `SET_SECRET` envelope |
| `tools/cmd/keeper` | ops | the relay timer |
| `tools/cmd/verify-tee` | ops | gate before a credential: is this proxy fronting an accepted enclave? |
| `tools/cmd/*` | deploy & one-shot ops | deploy, register, verify, submit-price… |

### Scripts

| Script | For |
| --- | --- |
| `scripts/upload-hex-key.sh` | install `hex_api_key` — backing set, ≥1 signature |
| `scripts/upload-rpc-keys.sh` | install the RPC credentials — providers set, ≥2 signatures |
| `scripts/oracle-*.sh` | observe, cycle, status — the operator commands |

### HTTP — exactly two ways into the extension

```
POST /action    internal/extension/utils.go:16       every instruction & direct action
GET  /state     internal/extension/extension.go:138  polled by tee-node → ends up public
```

### On-chain

```
InstructionSender.requestPriceUpdate()          payable, ANYONE
InstructionSender.setEndpoints(teeId, config)   payable, owner only
UsdxPriceStore.submitPrice(body, signature)     ANYONE
UsdxCustomFeed.getCurrentFeed()                 FtsoV2 / consumers
```

---

## The action router

Everything entering the extension goes through one dispatch:

```
POST /action
  └─ processAction                        extension.go:174
       ├─ USDX_ORACLE / GET_PRICE     → processGetPrice        [Flow D]
       ├─ USDX_CONFIG / SET_ENDPOINTS → ApplyEndpoints         [Flow A]
       │     requires action type = instruction
       ├─ USDX_CONFIG / SET_ADMINS    → ApplyAdmins            [Flow A2]
       │     requires action type = instruction
       ├─ USDX_CONFIG / SET_SECRET    → ApplySecret            [Flow B]
       │     requires action type = direct
       └─ anything else               → HTTP 501
```

The channel check matters: `SET_ENDPOINTS` or `SET_ADMINS` over `direct` would
skip the on-chain owner check, and `SET_SECRET` over `instruction` would put a
credential ciphertext in permanent public storage.

---

## Flow A — SET_ENDPOINTS (which sources we read)

```
owner → InstructionSender.setEndpoints(teeId, cfg)
          expectedConfigHash[teeId] = keccak256(cfg)   ← recorded before dispatch
          registry.sendInstructions(...)
             ↓  cosigned → proxy → tee-node → POST /action
        runtimecfg.ApplyEndpoints                      runtimecfg.go:176
          JSON decode, unknown fields rejected
          validate():
            · chain name must be a pinned chain
            · 1 ≤ threshold ≤ provider count
            · "hex_api_key" may NOT be a provider secret
            · https only; {secret} may not appear in scheme or host
            · EVERY pinned chain must be present
          endpointsHash = keccak256(payload)           ← hashed as received
```

That hash goes into every signed report, and the store compares it against
`expectedConfigHash[teeId]`. Without it, on-chain config would record what was
*sent*, never what a machine is *running*.

---

## Flow A2 — SET_ADMINS (who may provision this machine)

```
owner → InstructionSender.setAdmins(teeId, cfg)
          expectedAdminsHash[teeId] = keccak256(cfg)  ← recorded before dispatch
          registry.sendInstructions(...)
             ↓  cosigned → proxy → tee-node → POST /action
        runtimecfg.ApplyAdmins
          JSON decode, unknown fields rejected
          validateAdmins():
            · version non-zero AND strictly greater than the last applied
            · backing:   1 ≤ threshold ≤ admin count ≤ 32
            · providers: 2 ≤ threshold ≤ admin count ≤ 32
            · every entry parsable, non-zero and distinct within its set
          verifier.SetAdmins(backing, providers)      ← both, or neither
          adminsHash = keccak256(payload)             ← hashed as received
```

The payload carries TWO signer sets and the envelope's ref picks one:

```
ref == hex_api_key   → backing set     (1-of-N allowed)
everything else      → providers set   (2-of-N minimum)
```

The mapping is pinned in the build, never read from the payload — otherwise the
weaker set could claim the stronger set's credentials. Signers from the other set
do not count toward a threshold, and an unrecognised ref falls under the
*stronger* set.

Same enforcement as Flow A: the hash goes into every signed report and the store
compares it against `expectedAdminsHash[teeId]`.

The verifier starts with **no** admin sets and rejects every envelope until both
are installed, so Flow A2 must precede Flow B — on first provisioning and again
after every restart, since they are memory-only.

---

## Flow B — SET_SECRET (the credentials)

Three steps, three different people, and the chain is never touched.

Run twice, by different parties, because the ref decides which admin set
authorises the envelope and the two sets are held by different people:

```
scripts/upload-hex-key.sh    hex_api_key      backing set,   ≥1 signature
scripts/upload-rpc-keys.sh   every other ref  providers set, ≥2 signatures
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
  usdxprov submit   POST proxy/direct     ← must be within provision.Freshness
                                            (1 hour) of issuedAt
     ↓  tee-node polls the direct queue → POST /action (type = direct)
enclave
  runtimecfg.ApplySecret                          runtimecfg.go:211
    applyMu held for the whole call               ← the replay defence needs this
    teenode.TeeID()  signs a fixed probe, recovers its own address
    provision.Verify()                            provision.go:209
       admin sets must be installed at all — else rejected outright
       set = backing if ref == hex_api_key else providers
       chainId · teeId · payload hash · freshness · version > last
       · ≥ set.threshold DISTINCT signatures FROM THAT SET
       every failure returns the SAME error — no probing oracle
    node.Decrypt()   POST localhost:9090/decrypt  ← in-enclave
    reject a value containing  space \t \r \n / ? # &
    secrets[ref] = value                          ← memory only, lost on restart
```

---

## Flow C — the observation loop (the engine)

Runs whether or not anyone asks for a price. Nothing else touches the network.

```
observe.Poller.Run — every 2 minutes                observe.go:114
  │
  ├─ runtimecfg.Resolve()                           runtimecfg.go:415
  │     re-run every tick, never cached
  │     builds RPC clients, bonds source, cash source (nil if no secret)
  │     provider names are positional ("rpc_flare_1") so nothing an operator
  │     writes reaches the public /state
  │
  └─ oracle.Observe — 20s deadline                  oracle.go:104
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

## Flow D — GET_PRICE (must finish in 2 seconds)

```
anyone → InstructionSender.requestPriceUpdate()
           picks ONE random registered TEE machine
           message = 0x01 (fixed request envelope)
     ↓  proxy → tee-node → POST /action
  processGetPrice                                   extension.go:225
    require message == exactly 0x01
    poller.Report(instructionID)                    observe.go:200
      Latest()  — fails closed if the observation is older than 10 min
      oracle.Sign()
        body = abi(DOMAIN, price, decimals, observedAt, instructionId,
                   endpointsHash, adminsHash)
        POST localhost:9090/sign  — tee-node applies EIP-191; wire is base64
        normalise v from 0/1 → 27/28
        data = abi(body, signature)
    ActionResult{status: 1, data}
     ↓  tee-node → proxy → served at /action/result/<id>
```

**Why the split from Flow C:** an observation measures 2.0–2.9s and tee-node
allows an extension 2 seconds per action. Observing inside the handler timed out
on every request. A request now costs one signing round trip.

---

## Flow E — the keeper relay

```
tools/cmd/keeper → RunCycle, sequential, never overlapping     cycle.go:109
  at start    sweep the fleet: one direct request per machine, submitted
              oldest observation first, so every machine lands one price
  0. check for stuck transactions (a nonce gap blocks everything behind it)
  1. TRIGGER   POST proxy/direct → action id, next machine in the rotation;
               every 2h instead: requestPriceUpdate() → instructionId
  2. COLLECT   GET proxy/action/result/<id>, backoff 1s → 16s
                 on-chain: asked of every machine's proxy, one of them has it
                 404 = pending, not an error
                 status 0 → STOP, record the error class, invent nothing
  3. RELAY     decode → preflight (simulate, map the revert to a named guard)
                 → submitPrice(body, signature)   ← body relayed byte for byte
                 → wait for the receipt; a timeout is not chased, the next
                   cycle supersedes it
```

The keeper holds **no authority**. Every guard is on-chain, so the worst a wrong
invocation does is waste gas.

---

## Flow F — on-chain acceptance

`UsdxPriceStore.submitPrice`, in execution order:

```
 1  not paused
 2  signature: 65 bytes · v ∈ {27,28} · low-s · EIP-191 digest · ecrecover
 3  signer is registered to THIS extension AND status = PRODUCTION
 4  decode the body
 5  domain matches
 6  decimals match
 7  configSource.expectedConfigHash(teeId) is published AND matches   ← closes Flow A
 8  observedAt strictly greater than stored     ← replay + out-of-order in one rule
 9  not more than 2 minutes in the future
10  not older than maxAge
11  price within [minPrice, maxPrice]           ← rejected, never clamped
    ────────────────────────────────────────
    store price, observedAt, instructionId · emit PriceUpdated
```

---

## Flow G — consumer read

```
FtsoV2 → UsdxCustomFeed.getCurrentFeed() → UsdxPriceStore.latest()
           never reverts
           invalidated → (price, decimals, 0)      ← 0 means "treat as empty"
           otherwise   → (price, decimals, min(observedAt, block.timestamp))
```

`paused` and `invalidated` are different on purpose: pause stops **writes** and
lets the timestamp age; invalidate withdraws the **value** without zeroing the
price, since a zero price reads as worthless rather than absent.

---

## The failure path

```
any fault → failure.Failure (closed vocabulary, compile-time constant text)
   ├─ recorded by the poller
   ├─ next GET_PRICE → ActionResult{status: 0, log: "error: <constant text>"}
   │     → proxy /action/result/<id> → the keeper records it as an error class
   └─ GET /state → tee-node → attested → proxy /info      ← WORLD READABLE
```

`Failure` has unexported fields and no exported constructor, so no runtime value
(an API key inside a URL error, a reserve figure inside a parse error) can reach
either public surface. That is a compile error, not a review item.

---

## Trust model

| Can move the price | Constrained by |
| --- | --- |
| the attested code | registered code hash, reproducible build |
| **sender owner key** | sets RPC hosts and quorum with **no allowlist** — accepted trade (finding 15) |
| **admin set (2-of-3)** | holds the Hex credential; cannot redirect it into a provider URL |
| Hex Trust | sole reserve source, no redundancy |
| keeper | nothing — worst case is wasted gas |
| store governance | pause · invalidate · bounds · repoint extension |

---

## Which command answers which question

| Question | Command |
| --- | --- |
| Do the real sources yield a sane price? | `./scripts/oracle-observe.sh` |
| Does the wire contract hold? | `./scripts/test-conformance.sh` |
| Does provisioning work? | `./scripts/set-endpoints.sh` then `usdxprov` |
| Does the whole chain work? | `./scripts/oracle-cycle.sh` |
| What is it publishing right now? | `./scripts/oracle-status.sh` |

---

## Open items for the audit

1. **The admin set must be published before a live feed.** Nothing is baked any
   more: a machine starts with no admins and refuses every `SET_SECRET` until
   `SET_ADMINS` installs one. The pre-production requirement is that the set
   published is a real one — `testdata/admins/dev-wellknown-keys.json` names
   private keys 1, 2 and 3 and is for local and testnet work only.
2. **`/state` describes requests served, not the engine.** Its counters and
   `sourceHealth` are written only on the GET_PRICE path, so a machine nobody
   queries reports an empty health map while its poller fails every 2 minutes.
   `GET_READINESS` is the surface that reports the poller itself — use it, or
   alert on on-chain feed *age*. (`Poller.Stats()` remains unused outside tests;
   `ReadinessReport` reads the counters directly.)
3. **`checkWindow` measures retrieval time, not data age.** The 30s window bounds
   when we *fetched*; the reserve figure itself may be up to 30 min (bonds) or
   90 min (cash) old — those are bounded separately, in package `backing`. Note
   the 20s observation deadline is already tighter than the 30s window, so the
   window cannot fire in production; the deadline is the real bound on read skew.
4. **No target binding in the report** (finding 18, accepted) — a genuinely
   signed report is replayable into any other store registered under the same
   extension.
5. **Global `observedAt` ratchet** across all machines: one enclave with a fast
   clock suppresses the others until chain time catches up.
6. **Rounding reversed 5 Aug.** `price` rounds half-up, not down; the bound that
   truncation bought is now held by `TestCompute_RoundingCannotHideAMaterialShortfall`.
