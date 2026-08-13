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

extension-tee ──── one process, built from go/cmd/docker
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
| `go/` | the extension implementation (the only language checked in) |
| `contracts/` | `InstructionSender`, `UsdxPriceStore`, `UsdxCustomFeed` + interfaces |
| `tools/` | deployment, registration and the keeper — **language-neutral**, talks wire format only |
| `scripts/` | setup, start/stop, and the four `oracle-*.sh` operator commands |
| `test/` | Foundry tests for the store |
| `docs/` | spec, rationale, findings, flows, deployment |
| `testing/` | agent-driven chaos / smoke / edge-case harness |

The split that matters: `go/` is one possible implementation, `tools/` and `contracts/`
are the spine. `tools/pkg/pricereport` deliberately re-declares the wire layout instead
of importing `go/internal/report`, so the tooling stays honest about the contract.

---

## 4. The Go extension — package map

Roughly bottom-up. "Flow" refers to §6.

| Package | Owns | Key symbols | Used in |
| --- | --- | --- | --- |
| `internal/config` | **pinned facts**: op-type names, chain set + token addresses, Hex URLs, all deadlines, price decimals | `Chains`, `LookupChain`, `ObservationInterval`, `MaxObservationAge`, `BondsSource`/`CashSource` | everywhere |
| `internal/failure` | the *closed* public error vocabulary | `Failure` (unexported fields, no constructor), `Class*`, `All` | everywhere |
| `internal/decimal` | exact money parsing (`"25,452,544.52"` → `*big.Rat`), validates comma grouping instead of stripping it | `ParseAmount` | C |
| `internal/price` | pure arithmetic, no I/O | `Compute`, `ComputeWithoutCash`, `Scale` | C |
| `internal/supply` | per-chain `totalSupply()` over N RPC providers with a quorum procedure | `Read` `:116`, `agreedBlock` `:160`, `vote` `:291`, `Total` `:323` | C |
| `internal/backing` | Hex Trust HTTP reserve reads | `Source` iface, `New` `:77`, `Fetch` `:125` | C |
| `internal/report` | the signed payload's ABI layout | `EncodeBody`, `EncodeData`, `DecodeData`, `Domain` | D |
| `internal/teenode` | HTTP client for tee-node's local `/sign` + `/decrypt` | `Sign` `:82`, `Decrypt` `:106`, `TeeID` `:184`, `NormalizeV` `:158` | B, D |
| `internal/provision` | verifies M-of-N admin-signed provisioning envelopes | `Verifier.Verify` `:152`, `EncodeBody`, `Admins` | B |
| `internal/runtimecfg` | the only mutable state: provider URLs + credentials → `oracle.Config` | `ApplyEndpoints` `:118`, `ApplySecret` `:150`, `Resolve` `:227`, `Readiness` `:188` | A, B, C |
| `internal/oracle` | orchestrates **one** observation: ordering, deadlines, health map | `Observe` `:96`, `Sign` `:158`, `readAllChains` `:194`, `readBacking` `:237`, `checkWindow` `:285` | C, D |
| `internal/observe` | runs `Observe` on a timer, caches the latest, enforces staleness | `Poller.Run` `:65`, `Latest` `:122`, `Report` `:141` | C, D |
| `internal/extension` | HTTP surface + action routing | `processAction` `:149`, `processGetPrice` `:189`, `processUsdxConfig` (config_handler.go`:26`), `stateHandler` `:113` | A, B, D |
| `pkg/types` | the **public** `/state` shape | `State`, `Readiness`, `StateResponse` | D |
| `pkg/server` | wires everything together | `StartExtension` `:25` | boot |

### Binaries (`go/cmd/`)

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

`go/pkg/server/server.go:25` is the whole dependency graph in 10 lines:

```
teenode.New(signPort)                     ── talks to tee-node on localhost
   └─► provision.NewVerifier(chainID)     ── admin signature check
         └─► runtimecfg.New(verifier, node)   ── the config Store
               ├─► observe.New(store, node)   ── Poller: Resolver = store, Signer = node
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
(`extension.go:149`) → `POST /action`:

```
POST /action  (utils.go:16 actionHandler)
  └─ processAction
       ├─ USDX_ORACLE / GET_PRICE     → processGetPrice        [Flow D]
       ├─ USDX_CONFIG / SET_ENDPOINTS → ApplyEndpoints         [Flow A]  requires type = instruction
       ├─ USDX_CONFIG / SET_ADMINS    → ApplyAdmins            [Flow A2] requires type = instruction
       ├─ USDX_CONFIG / SET_SECRET    → ApplySecret            [Flow B]  requires type = direct
       └─ anything else               → HTTP 501
```

The channel check is a security control, not bookkeeping: `SET_ENDPOINTS` or
`SET_ADMINS` over `direct` would skip the on-chain owner check — for `SET_ADMINS` that
means anyone reaching the proxy could install their own admin set and provision through
it; `SET_SECRET` over `instruction` would put a credential ciphertext into permanent
public storage.

### Flow A — `SET_ENDPOINTS` (which RPC sources we read)

```
owner → InstructionSender.setEndpoints(teeId, cfg)          InstructionSender.sol:104
          expectedConfigHash[teeId] = keccak256(cfg)        ← recorded BEFORE dispatch
          registry.sendInstructions(...)
            ↓ cosigned → proxy → tee-node → POST /action
        extension.processUsdxConfig                         config_handler.go:26
        runtimecfg.ApplyEndpoints                           runtimecfg.go:118
          JSON decode with DisallowUnknownFields
          validate()  runtimecfg.go:304
            · chain name must be one of config.Chains, and ALL of them must be present
            · 1 ≤ threshold ≤ provider count
            · "hex_api_key" may NOT be used as a provider secret
            · https only; {secret} may not appear in scheme or host
          endpointsHash = keccak256(payload AS RECEIVED)     ← never recomputed from the struct
```

That hash rides in every signed report, and the store compares it against
`expectedConfigHash[teeId]`. Without it, the chain would record what was *sent*, never
what a machine is *running*.

### Flow A2 — `SET_ADMINS` (who may provision this machine)

```
owner → InstructionSender.setAdmins(teeId, cfg)             InstructionSender.sol
          expectedAdminsHash[teeId] = keccak256(cfg)        ← recorded BEFORE dispatch
          registry.sendInstructions(...)
            ↓ cosigned → proxy → tee-node → POST /action
        extension.processUsdxConfig                         config_handler.go
        runtimecfg.ApplyAdmins                              runtimecfg.go
          JSON decode with DisallowUnknownFields
          validateAdmins()
            · version must be non-zero AND strictly greater than the last applied
            · backing:   MinBackingThreshold (1)  ≤ threshold ≤ count ≤ 32
            · providers: MinProviderThreshold (2) ≤ threshold ≤ count ≤ 32
            · every entry a parsable, non-zero hex address, distinct within its set
          provision.Verifier.SetAdmins(backing, providers)   ← both, or neither
          adminsHash = keccak256(payload AS RECEIVED)        ← never recomputed from the struct
```

The payload carries **two** signer sets. `config.HexAPIKeyRef` is authorised by the
`backing` set (1-of-N allowed, because it authenticates a pinned endpoint by header);
every other ref by the `providers` set (2-of-N minimum, because those credentials are
substituted into URLs `SET_ENDPOINTS` chooses). The ref-to-set mapping is pinned at
`NewVerifier` and never read from the payload, and a signer from one set does not count
toward the other's threshold — see rationale §9.

Same shape as Flow A otherwise, and the same enforcement: the hash rides in every signed
report and the store compares it against `expectedAdminsHash[teeId]`. The version counter
is the one addition — the admin sets are the root of trust for every credential that
follows, so a captured `SET_ADMINS` must not be able to reinstate a rotated-out signer.

Rotation does **not** reset the per-ref envelope versions in the verifier: those are
anti-rollback state independent of who signs, and clearing them would let an envelope
the outgoing admins signed be replayed against the incoming set.

### Flow B — `SET_SECRET` (the Hex credential)

Three steps, three different people, chain never touched.

```
admin laptop (go/cmd/usdxprov)
  usdxprov build    GET proxy/info → TEE public key; ciphertext = ECIES(pubkey, secret)
                    body = abi(domain, chainId, teeId, version, issuedAt, ref, payloadHash)
  usdxprov sign     admin #1, then admin #2   (refuses a duplicate signer)
  usdxprov submit   POST proxy/direct         (must be within 15 min of issuedAt)
    ↓ tee-node polls the direct queue → POST /action (type = direct)
enclave
  runtimecfg.ApplySecret                       runtimecfg.go:150
    applyMu held for the whole call            ← the replay defence needs this
    teenode.TeeID()   signs a fixed probe, recovers its own address
    provision.Verify()                         provision.go:152
      chainId · teeId · payloadHash · freshness · version strictly > last
      · ≥2 DISTINCT admins, low-s only
      EVERY failure returns the same error — no probing oracle
    teenode.Decrypt() POST localhost:9090/decrypt   ← in-enclave
    reject a value containing  space \t \r \n / ? # &
    secrets[ref] = value                       ← memory only, lost on restart
```

### Flow C — the observation loop (the engine)

Runs on a timer whether or not anyone asks for a price. **This is the only code that
touches the network for data.**

```
observe.Poller.Run — every 2 min                observe.go:65
  ├─ runtimecfg.Resolve()                       runtimecfg.go:227
  │     re-run every tick (never cached), so a rotated credential needs no restart
  │     builds RPC clients, bonds source, cash source (nil if no secret yet)
  │     provider names are POSITIONAL — "rpc_flare_1" — so nothing an operator
  │     writes can reach the public /state
  │
  └─ oracle.Observe — 20s deadline              oracle.go:96
        S  readAllChains, all chains concurrently      oracle.go:194
             supply.Read per chain                     supply.go:116
               agreedBlock: ask every provider for its head, take the MINIMUM
               readDecimals + readTotalSupply at that EXACT block number
               require `Quorum` IDENTICAL answers (never a median — a read at a
               fixed block is deterministic, so disagreement means someone is wrong)
               amount = raw / 10^decimals, exact in big.Rat
             any chain failing fails the whole observation (a partial sum is a
             wrong supply, not a smaller one)
        B  readBacking, both endpoints concurrently    oracle.go:237
             backing.Fetch → 2 attempts, no redirects ever, unknown JSON fields
             rejected, comma grouping validated not stripped, last_update ≤ 30 min
             bonds failing is FATAL; cash failing is not
        checkWindow(30s) across every read timestamp   oracle.go:285
        price.Compute → min(1, B/S) × 10⁸, rounded half-up
             no cash? price.ComputeWithoutCash — publishes only if the answer
             provably cannot depend on the missing figure (i.e. already at par)
        observedAt = wall-clock now             ← NOT the oldest input; the store
                                                  requires strict monotonicity
  └─ success → cached as `last`
     failure → recorded; the PREVIOUS observation stays and ages out after 10 min
```

### Flow D — `GET_PRICE` (must finish in 2 seconds)

tee-node allows an extension **2 seconds** per action, and a full observation measures
2.0–2.9s. That is why C and D are separate: a request costs exactly one signing round
trip.

```
anyone → InstructionSender.requestPriceUpdate()           InstructionSender.sol:153
           picks ONE random registered TEE machine; message = 0x01 (fixed envelope)
    ↓ proxy → tee-node → POST /action
  extension.processGetPrice                               extension.go:189
    require message == exactly 0x01, else UnexpectedPayload
    observe.Poller.Report(instructionID)                  observe.go:141
      Latest()  ─ fails closed if the observation is older than 10 min
      oracle.Sign()                                       oracle.go:158
        report.EncodeBody → abi(DOMAIN, price, decimals, observedAt,
                                instructionId, endpointsHash, adminsHash)
        teenode.Sign → POST localhost:9090/sign   (tee-node applies EIP-191;
                                                   wire is base64, not hex)
        NormalizeV: 0/1 → 27/28, so the store can use stock OpenZeppelin
        report.EncodeData → abi(body, signature)
    ActionResult{status: 1, data}
    ↓ tee-node → proxy → served at GET /action/result/<id>
```

### Flow E — the keeper relay (`tools/`)

```
tools/cmd/keeper → keeper.Run (timer, single-flight) → keeper.RunCycle   cycle.go:109
  at start:  keeper.Sweep — one direct request per registered machine,     sweep.go:30
             submitted in ascending observedAt order so they all land
  0. pendingNonceGap  — a stuck tx blocks everything behind its nonce
  1. TRIGGER   direct by default: POST proxy/direct → action id (no fee, no tx),
                 rotating over the fleet, one machine per cycle;
                 every -onChainEvery (2h) instead:
                 instrutils.RequestPriceUpdate() → instructionId
  2. COLLECT   PollResultAny: GET proxy/action/result/<id>, backoff 1s → 16s
                 on-chain: every machine's proxy, since the registry chose one
                 404 = pending, not an error
                 status 0 → STOP and record the error class; inventing a price
                 is the one thing the keeper must never do
  3. RELAY     pricereport.Decode → Preflight (simulate every store guard and map
                 the revert to a NAMED guard) → store.submitPrice(body, signature)
                 → wait for the receipt; a timeout is not chased, the next cycle
                   supersedes it
  → Record with an Outcome: accepted · race_lost · stale_submission · rejected …
```

`race_lost` is a **success** — with ≥2 keepers exactly one submission wins per cycle,
so it must never alert. That taxonomy lives in `tools/pkg/keeper/record.go`.

### Flow F — on-chain acceptance, then the consumer read

`UsdxPriceStore.submitPrice` (`UsdxPriceStore.sol:124`), in execution order:

```
 1 not paused                              6 configSource.expectedConfigHash(teeId)
 2 sig: 65 bytes, v∈{27,28}, low-s,          published AND matching   ← closes Flow A
   EIP-191 digest, ecrecover                7 observedAt strictly greater than stored
 3 signer registered to THIS extension        (replay + out-of-order in one rule)
   AND status = PRODUCTION                  8 not > 2 min in the future
 4 domain matches                           9 not older than maxAge
 5 decimals match                          10 price within [minPrice, maxPrice] — rejected, never clamped
   ────────► store price/observedAt/instructionId · emit PriceUpdated
```

```
FtsoV2 → UsdxCustomFeed.getCurrentFeed() → UsdxPriceStore.latest()
           NEVER reverts (a revert would take down every consumer at once)
           invalidated → (price, decimals, 0)     ← 0 means "treat as empty"
           otherwise   → (price, decimals, min(observedAt, block.timestamp))
```

`paused` ≠ `invalidated`, on purpose: pause stops **writes** and lets the timestamp age;
invalidate withdraws the **value** without zeroing the price, since a zero price reads
as worthless rather than absent.

---

## 7. Contracts

| Contract | Entry points | Notes |
| --- | --- | --- |
| `UsdxOracleInstructionSender` | `requestPriceUpdate()` payable, **anyone** · `setEndpoints(teeId, cfg)` payable, **owner** · `setExtensionId()` once | also the `IUsdxConfigSource` the store reads `expectedConfigHash` from |
| `UsdxPriceStore` | `submitPrice(body, sig)` **anyone** · `latest()` · governance: `setPaused`, `setInvalidated`, `setBounds`, `setExtension` | all authority lives here |
| `UsdxCustomFeed` | `getCurrentFeed()`, `feedId()`, `calculateFee()` | thin `IICustomFeed` adapter for FtsoV2 |

---

## 8. `tools/` map

| Path | Role |
| --- | --- |
| `pkg/support` | chain client, key, ABI/revert plumbing shared by every tool |
| `pkg/configs` | env-var defaults so tools run flag-free from a sourced `.env` |
| `pkg/pricereport` | decodes the signed payload — the wire contract, re-declared independently |
| `pkg/keeper` | `Run` (timer) · `RunCycle` · `PollResult` · `Preflight` · `Record`/`Outcome` |
| `pkg/utils/instructions.go` | `RequestPriceUpdate`, `SetEndpoints`, fee reads (fees are **read**, never hard-coded) |
| `pkg/fccutils` | FCC registration, governance, policy-consistency, revert decoding |
| `pkg/contracts/*` | generated bindings (`go generate ./...` after editing Solidity) |
| `pkg/validate` | the deploy/verify check suite behind `verify-deploy` |
| `cmd/*` | one binary per operation: `deploy-store`, `register-extension`, `register-tee`, `send-endpoints`, `send-admins`, `set-governance`, `set-store-extension`, `submit-price`, `keeper`, `verify-deploy`, `audit-deploy`, `query-tee` … |

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
go/pkg/server/server.go            the whole dependency graph, 10 lines
go/internal/extension/extension.go the HTTP surface and the router
go/internal/oracle/oracle.go       one observation, start to finish
go/internal/observe/observe.go     why observing and signing are separate
contracts/UsdxPriceStore.sol       every guard that protects the feed
tools/pkg/keeper/cycle.go          the relay, end to end
```

Every package's doc comment carries the reasoning; `docs/usdx-oracle-rationale.md`
carries the long form.

| Question | Command |
| --- | --- |
| Do the real sources yield a sane price? | `./scripts/oracle-observe.sh` |
| Does the wire contract hold? | `./scripts/test-conformance.sh` |
| Does the whole chain work? | `./scripts/oracle-cycle.sh` |
| What is it publishing right now? | `./scripts/oracle-status.sh` |

---

## 11. Gotchas worth knowing before you touch it

- **The admin sets are published, not baked, and are memory-only.** A machine starts with
  none and rejects every `SET_SECRET` until `SET_ADMINS` lands, so they must be republished
  after every restart and *before* any credential. There are TWO sets and the envelope's
  ref picks one: `hex_api_key` → `backing` (1-of-N allowed), everything else → `providers`
  (2-of-N minimum). The checked-in `testdata/admins/dev-wellknown-keys.json` names private
  keys 1, 2 and 3 — local and testnet only.
- **`/state` counters count served `GET_PRICE` actions, not the poller.**
  `observe.Poller.Stats()` exists but is never called — a machine nobody queries reports
  an empty health map while its poller may be failing every 2 minutes. Alert on on-chain
  feed *age*, not on these counters.
- **Stub mode is indistinguishable from a healthy fully-backed feed** on the wire — both
  print `$1.00000000`. `Extension.StubMode()` is what tells them apart.
- **The report has no target binding.** A genuinely signed report is replayable into any
  other store registered under the same extension. Accepted trade-off, findings §18.
- **`observedAt` is a global ratchet** across all machines: one enclave with a fast clock
  suppresses the others until chain time catches up.
