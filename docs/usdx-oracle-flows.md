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
| `tools/cmd/*` | deploy & one-shot ops | deploy, register, verify, submit-price… |

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
       ├─ USDX_CONFIG / SET_SECRET    → ApplySecret            [Flow B]
       │     requires action type = direct
       └─ anything else               → HTTP 501
```

The channel check matters: `SET_ENDPOINTS` over `direct` would skip the on-chain
owner check, and `SET_SECRET` over `instruction` would put a credential
ciphertext in permanent public storage.

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

## Flow B — SET_SECRET (the Hex credential)

Three steps, three different people, and the chain is never touched.

```
admin laptop
  usdxprov build    GET proxy/info → public key → teeId derived from it
                    ciphertext = ECIES.Encrypt(pubkey, secret)
                    body = abi(domain, chainId, teeId, version, issuedAt, ref, hash)
  usdxprov sign     admin #1   (refuses a duplicate signer)
  usdxprov sign     admin #2
  usdxprov submit   POST proxy/direct     ← must be within 15 min of issuedAt
     ↓  tee-node polls the direct queue → POST /action (type = direct)
enclave
  runtimecfg.ApplySecret                          runtimecfg.go:211
    applyMu held for the whole call               ← the replay defence needs this
    teenode.TeeID()  signs a fixed probe, recovers its own address
    provision.Verify()                            provision.go:209
       chainId · teeId · payload hash · freshness · version > last
       · ≥2 DISTINCT admin signatures
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
  ├─ runtimecfg.Resolve()                           runtimecfg.go:305
  │     re-run every tick, never cached
  │     builds RPC clients, bonds source, cash source (nil if no secret)
  │     provider names are positional ("rpc_flare_1") so nothing an operator
  │     writes reaches the public /state
  │
  └─ oracle.Observe — 20s deadline                  oracle.go:179
        S   supply, all chains concurrently
              ask every provider for its head → take the Quorum-th HIGHEST
              refuse it if older than MaxBlockAge → query all at that EXACT block
              require N IDENTICAL results (never a median)
              amount = raw / 10^decimals, exact
        B   backing, both endpoints concurrently
              bonds failing is fatal; cash failing is not
              no redirects · unknown JSON fields rejected
              comma grouping validated, not stripped · last_update ≤ 30 min
        checkWindow(30s) across all read timestamps
        price = min(1, B/S) × 10⁸, rounded half-up
              no cash? publish only if the answer provably cannot depend on it
        observedAt = now
  │
  └─ success → cached as "latest"
     failure → error recorded, the previous observation is LEFT IN PLACE
               and ages out after 10 minutes
```

**One pass per side:** each supply figure is pinned to one agreed block and the
reserve figure moves on Hex's ~10 minute tick, so neither side has an internal
race worth bracketing. `checkWindow` bounds the skew between the two. The earlier
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
        body = abi(DOMAIN, price, decimals, observedAt, instructionId, endpointsHash)
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
tools/cmd/keeper → RunCycle, sequential, never overlapping     cycle.go:78
  0. check for stuck transactions (a nonce gap blocks everything behind it)
  1. TRIGGER   requestPriceUpdate() → instructionId
  2. COLLECT   GET proxy/action/result/<id>, backoff 1s → 16s
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

1. **Admin set is placeholder keys.** `provision.Admins` are private keys 1, 2
   and 3. Flagged loudly in-code; the hard pre-production blocker.
2. **`Poller.Stats()` is dead code.** `/state` counters and `sourceHealth` are
   written only on the GET_PRICE path, so a machine nobody queries reports an
   empty health map while its poller fails every 2 minutes.
3. **`checkWindow` measures retrieval time, not data age.** The 30s window bounds
   when we *fetched*; the reserve figure itself may be up to 30 minutes old.
   Worth confirming against spec §6.1.
4. **No target binding in the report** (finding 18, accepted) — a genuinely
   signed report is replayable into any other store registered under the same
   extension.
5. **Global `observedAt` ratchet** across all machines: one enclave with a fast
   clock suppresses the others until chain time catches up.
6. **Rounding reversed 5 Aug.** `price` rounds half-up, not down; the bound that
   truncation bought is now held by `TestCompute_RoundingCannotHideAMaterialShortfall`.
