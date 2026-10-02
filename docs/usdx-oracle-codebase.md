# USDX Oracle — codebase walkthrough

A map of the repo for someone seeing it for the first time: what runs where, which
package owns what, and which functions each flow actually goes through. Code is named
as `path:Symbol`; line numbers are left out because they move.

Companion docs: [`usdx-oracle-flows.md`](usdx-oracle-flows.md) (same flows, more
operational detail) · [`usdx-oracle-rationale.md`](usdx-oracle-rationale.md) (*why*
each design choice) · [`usdx-oracle-findings.md`](usdx-oracle-findings.md) (security
issues found while building) · [`development.md`](development.md) (layout, adding a
command, CI).

---

## 1. What the thing does

USDX has no working market venue, so there is no market price. The next-best truth is
collateralisation:

```
supply  = USDX totalSupply() on Flare + Ethereum + Songbird
backing = bond reserves (Hex public API) + cash reserves (Hex restricted API)
price   = min(1, backing / supply)      capped at par, 8 decimals, rounded half-up
```

That computation runs **inside a TEE**, is signed by the enclave's identity key, and is
relayed on-chain where a store contract verifies the signature against Flare's TEE
machine registry. Nobody in the delivery path can alter the number — a keeper relaying
it holds no authority at all.

---

## 2. What runs where

Three containers. tee-node and the extension are **one process**: `cmd/extension-tee`
links tee-node as a library and starts both. tee-node is nevertheless a *client* of
the extension: it calls in over loopback HTTP.

```
ext-proxy (tee-proxy)
  :6663 internal   TEE machines poll here for queued actions      (host 6673 in deploy/docker-compose.yaml)
  :6664 external   PUBLIC — /info, /action/result/<id>, /direct    (host 6674)

extension-tee ──── one process, built from cmd/extension-tee
  ├ tee-node       :5500 config · :7701 (SIGN_PORT) POST /sign, POST /decrypt
  │                polls the proxy for actions → POSTs them to :7702/action
  │                reads GET :7702/state whenever it produces its TEE info and folds
  │                it into the attested state (the extension answers the empty state)
  ├ extension      :7702 (EXTENSION_PORT) POST /action, GET /state — 127.0.0.1 only
  └ poller         background goroutine, one observation every 2 min

redis              the proxy's action queue
```

**The loopback-only listener is a security control.** `internal/extension/extension.go:NewExtension`
binds `127.0.0.1`, not all interfaces. tee-node is the one legitimate caller, and the
instruction router treats arrival on the instruction channel as authorisation for
`SET_ENDPOINTS` and `SET_ADMINS`; a listener reachable off-host would let a network
peer install its own endpoints and admin set. tee-node's sign server is bound the same
way, and `internal/teenode` calls it on `localhost` without authentication for that reason.

---

## 3. Repo layout

| Path | What it is |
| --- | --- |
| `cmd/`, `internal/`, `pkg/` | the extension implementation (the Go module at the repository root) |
| `contracts/` | Foundry project with no contracts of its own: it compiles upstream's interfaces and deploy script out of a sibling flare-smart-contracts-v2 checkout — `src/Bindings.sol` (what `generate-bindings.sh` binds), `script/DeployTeeOracle.s.sol` (the simulation and testnet deploy behind `deploy-oracle.sh`), `test/` (the wire vectors) |
| `tools/` | registration, configuration, provisioning, monitoring and the keeper — its own module (`fce-usdx-oracle/tools`), importing the extension's `pkg/protocol`, `pkg/types` and `pkg/wire/*` |
| `deploy/scripts/` | setup, start/stop, the four `oracle-*.sh` operator commands (observe, cycle, status, watch), the two `upload-*.sh` credential uploads, publishing, image builds |
| `deploy/testdata/` | `wire/`, Solidity's own encodings of the wire formats, and `conformance/`, the fixtures `TestConformance` replays |
| `docs/` | spec, rationale, findings, flows, operator and developer guides |

The split that matters: the root module is the extension, `tools/` operates it, and
`contracts/` pins the wire formats. Every format has one implementation, in the extension's
`pkg/wire` packages, and every protocol fact one definition, in `pkg/protocol`; the tooling
imports those and, by convention, nothing under `internal/` (Go does not enforce it, since
`fce-usdx-oracle/tools` sits under the root's module path). What the tooling adds on top is
recovery of who signed and the transactions that carry the payloads. Solidity's own encoding
in `deploy/testdata/wire` is the reference both are tested against.

---

## 4. The Go extension — package map

Roughly bottom-up. "Flow" refers to §6.

| Package | Owns | Key symbols | Used in |
| --- | --- | --- | --- |
| `pkg/protocol` | **the protocol's facts**: the op type and commands, feed id, credential refs and which the backing set governs, admin roles and their floors, the chain set with token addresses and `MaxBlockAge` | `OPTypeTeeOracle`, `OPCommand*`, `OpName`, `FeedID`, `HexAPIKeyRef`, `HexCashURLRef`, `BackingRefs`, `AdminPolicy`, `Chains`, `LookupChain`, `HomeChain` | everywhere, and `tools/` |
| `internal/config` | **the extension's settings**: ports, deadlines and size bounds, price decimals, the bonds URL, both metric names, the cash header and group tag, and the ids read from the environment | `ExtensionPort`/`SignPort`, `ChainID`, `ExtensionID`, `ObservationInterval`, `MaxObservationAge`, `ProviderTimeout`, `MaxRPCResponseBytes`, `HTTPTimeout`, `BondsSource`/`CashSource` | everywhere |
| `internal/failure` | the *closed* public error vocabulary | `Failure` (unexported fields, no constructor), `Class*`, `All`, `Coerce` | everywhere |
| `internal/observation/decimal` | exact money parsing (`"25,452,544.52"` → `*big.Rat`), validates comma grouping instead of stripping it, refuses a sign | `ParseAmount` | C |
| `internal/observation/price` | pure arithmetic, no I/O | `Compute`, `ComputeWithoutCash`, `Backing`, `Scale` | C |
| `internal/observation/supply` | per-chain `totalSupply()` over N RPC providers with a quorum procedure | `ChainSource`, `Read`, `providerHeads`, `pinCandidateBlocks`, `readAtBlock`, `agreedBlockTime`, `vote`, `Total` | C |
| `internal/observation/backing` | Hex Trust HTTP reserve reads | `Source`, `New`, `hexSource.Fetch` | C |
| `pkg/wire/feedupdate` | the signed **feed update** payload's ABI layout, and the `GET_FEED` request envelope | `FeedUpdate`, `EncodeFeedRequest`, `DecodeFeedRequest`, `ValueFromBig`, `EncodeUpdate`, `SigningMessage`, `Digest`, `EncodeData`, `DecodeData`, `Prefix` | D |
| `pkg/wire/readiness` | the signed **readiness** payload's ABI layout — a separate domain, so an answer can never be reinterpreted as a feed update | `Report`, `EncodeBody`, `DecodeBody`, `EncodeData`, `DecodeData`, `Domain` | D2 |
| `pkg/wire/envelope` | the **provisioning envelope**'s ABI layout: signed body, signatures, ciphertext, under its own domain | `Envelope`, `EncodeBody`, `Encode`, `Decode`, `Freshness`, `ClockSkew`, `MaxSignatures` | B, `send-secret` |
| `pkg/wire/eip191` | personal-sign recovery, low-s only, v as 27/28 or 0/1 | `Recover` | B, `tools/` |
| `pkg/wire/configpayload` | the **Endpoints** and **Admins** payloads, the hashes the sender publishes, and the PRIVATE-URL commitment | `DecodeEndpoints`, `DecodeAdmins`, `EncodeEndpoints`, `EncodeAdmins`, `EndpointsHash`, `AdminsHash`, `CommitPrivateURL`, `DecodePrivateURL` | A, A2, B, `tools/` |
| `pkg/contracts/teeoraclestructs` | abigen binding of upstream's `TeeOracleStructs` ABI: the struct layouts `configpayload` packs and unpacks | `TeeOracleStructsMetaData` | A, A2 |
| `internal/teenode` | HTTP client for tee-node's local `/sign` + `/decrypt` | `New`, `Client.Sign`, `Client.Decrypt`, `Client.TeeID`, `NormalizeV` | boot, B, D, D2 |
| `internal/provisioning/envelope` | verifies M-of-N admin-signed provisioning envelopes against the installed sets | `NewVerifier`, `Verifier.Verify`, `Verifier.SetAdmins`, `Verifier.HasAdmins`, `AdminSet` | A2, B |
| `internal/provisioning/store` | the provisioning state: endpoints, admin-set hash and versions, credentials; opens the sources for each observation | `New`, `Store.ApplyEndpoints`, `Store.ApplyAdmins`, `Store.ApplySecret`, `Store.Readiness`, `Store.Open`, `validate`, `validateCashGroup`, `verifySecret` | A, A2, B, C, D2 |
| `internal/observation` | orchestrates **one** observation: ordering, deadlines, health map | `Sources`, `Binding`, `Observation`, `Observe`, `Sign`, `readAllChains`, `readBacking`, `checkWindow`, `homeBlockTime` | C, D |
| `internal/observation/poller` | runs `Observe` on a timer, caches the latest, enforces staleness against both clocks, and answers readiness | `New`, `Poller.Run`, `Poller.Latest`, `Poller.Report`, `checkClock`, `Poller.ReadinessReport` | C, D, D2 |
| `internal/extension` | HTTP surface + action routing | `extension.go:NewExtension`/`actionHandler`/`stateHandler`; `router.go:processAction`/`checkOPType`/`processInstruction`/`processDirect`/`unsupportedCommand`; `feed.go:processGetFeed`; `readiness.go:processGetReadiness`; `provisioning.go:processSetEndpoints`/`processSetAdmins`/`processSetSecret`; `result.go:outcome`/`result` | A, A2, B, D, D2 |
| `pkg/types` | the readiness report's shapes and its source-health vocabulary, and the empty `/state` envelope | `Readiness`, `ReadinessReport`, `Health*`, `ChainHealthKey`, `ProviderHealthKey`, `StateResponse` | C, D2, `tools/` |
| `pkg/server` | wires everything together | `StartExtension`, `resolveTeeID` | boot |

### Binaries (`cmd/`)

| Binary | For |
| --- | --- |
| `cmd/extension-tee` | **production** — what `Dockerfile` builds (tee-node + extension in one process) |
| `cmd/dev/devserver` | local dev: real provisioning + observation, no chain |
| `cmd/dev/mocknode` | fake tee-node `/sign` and `/decrypt`, and the proxy's `/info`, for local dev |
| `cmd/dev/start-tee` | host-process runner behind `start-services.sh --local` |
| `tools/cmd/send-secret` | ops CLI: `build` / `sign` / `submit` a `SET_SECRET` envelope, `signers` (who has signed, against the threshold), `commit` (the cash URL's commitment) |

---

## 5. Boot & wiring

`cmd/extension-tee/main.go` and `pkg/server/server.go:StartExtension` are the whole
dependency graph:

```
main
  config.ExtensionIDEnv(EXTENSION_ID)       ── normalised for tee-node; EXTENSION_ID and CHAIN_ID
                                               are mandatory, with no default
  go teeServer.StartServerExtension(5500, signPort, extensionPort)   ── tee-node, in-process
  server.StartExtension(extensionPort, signPort)
    teenode.New(signPort, signTimeout)      ── tee-node's sign server on localhost, 1 s per call
    resolveTeeID(node)                      ── node.TeeID() every 1 s for up to 1 min; past that
                                               StartExtension returns the error and main exits
    envelope.NewVerifier(chainID, teeID, protocol.AdminPolicy())   ── teeId and policy pinned
    store.New(verifier, node)               ── the provisioning Store
    poller.New(store, node)                 ── Opener = store, Signer = node
    extension.NewExtension(extensionPort, signPort, poller, store)  ── 127.0.0.1 only
    go poller.Run(ctx)                      ── observes at once, then every 2 min
    go e.Server.ListenAndServe()
```

`TeeID` learns the machine's own address by signing a fixed probe and recovering the
signer, since tee-node exposes no "who am I". It is resolved once, before the verifier
exists, so every envelope is checked against the address pinned at boot.

The extension boots **unprovisioned** and that is normal — endpoints, the admin sets and
credentials are all memory-only, and tee-node generates the identity key afresh on every
start, so a restarted machine comes back under a new teeId with nothing installed. It must
be registered again, brought up to date with `pushEndpoints` / `pushAdmins` (Flow A) or a
new publication, and re-provisioned. `SET_ADMINS` must precede `SET_SECRET`: with no admin
set installed nothing can authorise a credential, and the verifier rejects every envelope
until one is.

The poller's first observation runs at boot, before any configuration has arrived, and
records `NotConfigured`. Once `SET_ENDPOINTS` lands, the first price exists only after the
**next tick**, up to two minutes later; a `GET_FEED` before then is refused.

`extension.NewExtension` panics on a nil observer, and no stub constructor or stub binary
exists: an extension that cannot observe cannot start. A fixed price is indistinguishable
from a healthy feed on the wire (findings §12). The conformance replay is
`TestConformance` in `internal/extension`, in-process.

---

## 6. The flows

Everything entering the extension goes through one route, `POST /action`, and one
dispatch, `internal/extension/router.go:processAction`, which picks the envelope by
`Data.Type` and hands it to that channel's router:

```
POST /action                         extension.go:actionHandler
  body is not an Action              → HTTP 400
  └─ processAction                   router.go
       type = instruction  parse DataFixed               (HTTP 400 if it does not parse)
                           checkOPType: opType ≠ TEE_ORACLE → HTTP 501
                           action id ≠ instruction id   → HTTP 400
                           └─ processInstruction
                                GET_FEED      → processGetFeed       [Flow D]   feed.go
                                SET_ENDPOINTS → processSetEndpoints  [Flow A]   provisioning.go
                                SET_ADMINS    → processSetAdmins     [Flow A2]  provisioning.go
                                GET_READINESS, SET_SECRET → status 0, WrongActionType
                                anything else → HTTP 501, unsupportedCommand
       type = direct       parse DirectInstruction       (HTTP 400 if it does not parse)
                           checkOPType                   → HTTP 501
                           └─ processDirect
                                GET_READINESS → processGetReadiness  [Flow D2]  readiness.go
                                SET_SECRET    → processSetSecret     [Flow B]   provisioning.go
                                GET_FEED, SET_ENDPOINTS, SET_ADMINS → status 0, WrongActionType
                                anything else → HTTP 501, unsupportedCommand
       any other type      → HTTP 400
```

A wrong-channel refusal is an ordinary HTTP 200 whose `ActionResult` has status 0 and
`log` `error: command arrived over the wrong action type`. The three `SET_` commands answer
`NotConfigured` when the extension was built without a provisioner.

Every command shares the one `TEE_ORACLE` op type, so the channel check is a security
control, not bookkeeping: each command states its own guard rather than inheriting one,
and neither router may grow a shared default arm. `SET_ENDPOINTS` or `SET_ADMINS` over
`direct` would skip the sender's `onlyGovernance` check — for `SET_ADMINS` that means
anyone reaching the proxy could install their own admin set and provision through it;
`SET_SECRET` over `instruction` would put a credential ciphertext into permanent public
storage.

`GET_FEED` is **instruction-only**. It grants nothing — it signs an observation the
enclave already made on its own timer, and the store verifies that signature however
the request arrived — so serving it over `/direct` would be safe in the narrow sense.
It is refused there anyway, because the on-chain path is the *permissionless* one:
anyone can pay the instruction fee and get an update published, and a path that is
only exercised when the cheap one is unavailable is a path nobody notices has broken.
One channel, used every cycle. `GET_READINESS` is direct-only for the mirror-image
reason: an operational status report whose answer is stale within minutes is not worth
a fee and a permanent calldata record.

### Flow A — `SET_ENDPOINTS` (which RPC sources we read, and the cash URL's commitment)

```
governance → TeeOracleInstructionsSender.setEndpoints(feedId, groups, claimBack)
          payable, onlyGovernance (Flare's timelock in production). tools/cmd/send-endpoints
          encodes the call and sends it while the caller is still governance, or writes
          the calldata and fee for the governance executor
          validates the shape; version = endpointsVersion(feedId) + 1
          latestEndpointsHash(feedId) = keccak256(abi.encode(Endpoints{version, feedId, groups}))
                                                          ← recorded BEFORE dispatch
          emits EndpointsPublished(…, groups)
          ONE instruction to the extension's whole active set (its PRODUCTION machines);
            skipped — "published, push later" — while the extension is paused or the set is empty
            ↓ cosigned → proxy → tee-node → POST /action
        processInstruction → processSetEndpoints       internal/extension/provisioning.go
        store.Store.ApplyEndpoints                     internal/provisioning/store/store.go
          applyMu held; 0 < payload ≤ 256 KiB
          configpayload.DecodeEndpoints, then convertEndpoints: printable, distinct group tags
          validate()
            · version non-zero; feedId = protocol.FeedID
            · every tag is a protocol.Chains name or hex_cash; every chain AND hex_cash present
            · every group: at least one endpoint, 1 ≤ threshold ≤ endpoint count
            · hex_cash (validateCashGroup): exactly one endpoint, threshold 1, PRIVATE,
              secret ref hex_cash_url
            · PRIVATE endpoint: a commitment and no url, and a secret ref
            · PUBLIC endpoint: a url and no commitment; https only; {secret} and a secret ref
              together or not at all, never in the scheme or host
            · a chain group may not name a backing ref (hex_api_key, hex_cash_url) as its secret
          version strictly greater than the last applied, checked before anything is installed
          every credential already held must fit the arriving configuration (verifySecret)
          endpointsHash = keccak256(payload AS RECEIVED)     ← never recomputed from the struct
```

That hash rides in every signed update, and the store compares it against
`latestEndpointsHash(feedId)` on the sender. Without it, the chain would record
what was *sent*, never what a machine is *running*. The version bar means a captured
`SET_ENDPOINTS` cannot reinstate a superseded provider set.

**Catch-up.** A publication reaches only the machines active when it executes. A machine
registered later, or out of PRODUCTION at the time, is brought up to date by
`pushEndpoints(feedId, teeIds, groups)` — permissionless and payable, with `groups` taken
from the `EndpointsPublished` event; the sender re-encodes them against the stored version
and requires the result to hash to `latestEndpointsHash`. `getEndpointsPushTargets(feedId)`
lists the machines that are behind. No tool in this repo wraps the push. Until the latest
publication of both kinds has been dispatched to a machine, a `requestFeedUpdate` naming it
reverts `TeeIdNotConfigured`, or `FeedNotConfigured` when nothing has been published for
the feed at all. The sender records dispatch, not adoption: a machine whose instruction
never reached its enclave still reads as configured, and only its own `GET_READINESS` or a
rejected update shows otherwise.

### Flow A2 — `SET_ADMINS` (who may provision this machine)

```
governance → TeeOracleInstructionsSender.setAdmins(feedId, roles, claimBack)
          same shape as setEndpoints: version = adminsVersion(feedId) + 1,
          latestAdminsHash(feedId) recorded BEFORE dispatch, AdminsPublished(…, roles),
          one instruction to the active set; catch-up is pushAdmins(feedId, teeIds, roles)
          with getAdminsPushTargets(feedId)
            ↓ cosigned → proxy → tee-node → POST /action
        processInstruction → processSetAdmins          internal/extension/provisioning.go
        store.Store.ApplyAdmins                        internal/provisioning/store/store.go
          applyMu held; 0 < payload ≤ 256 KiB
          configpayload.DecodeAdmins, then convertAdmins + parseAdminSet
            · version non-zero; feedId = protocol.FeedID
            · role tags printable, distinct and known (backing, providers); both present
            · each set: 1 ≤ count ≤ MaxAdmins (32);
              floor ≤ threshold ≤ min(count, MaxThreshold 16), where the floor is
              MinBackingThreshold (1) or MinProviderThreshold (2) — MaxThreshold is the cap
              Verify puts on an envelope's signatures, so a higher threshold would install
              and then be unsatisfiable forever
            · every address non-zero and distinct within its set
          version strictly greater than the last applied
          envelope.Verifier.SetAdmins(sets)            ← both, or neither
          adminsHash = keccak256(payload AS RECEIVED)  ← never recomputed from the struct
```

The payload carries **two** signer sets. `protocol.BackingRefs` — `hex_api_key` and
`hex_cash_url` — are authorised by the `backing` set (1-of-N allowed: the bonds URL is
pinned and the cash URL must match governance's commitment, so a backing credential can
degrade the feed but not choose where a reserve figure comes from); every other ref by the
`providers` set (2-of-N minimum, because those credentials are substituted into URLs
`SET_ENDPOINTS` chooses). The ref-to-set mapping is `protocol.AdminPolicy`, pinned at
`NewVerifier` and never read from the payload, and a signer from one set does not count
toward the other's threshold — see rationale §9.

Same enforcement as Flow A: the hash rides in every signed update and the store compares it
against `latestAdminsHash(feedId)` on the sender. The admin sets are the root of trust for
every credential that follows, so a captured `SET_ADMINS` must not be able to reinstate a
rotated-out signer; the sender assigns versions per feed and the machine refuses anything
not strictly newer than the last applied.

Rotation does **not** reset the per-ref envelope versions in the verifier: those are
anti-rollback state independent of who signs, and clearing them would let an envelope
the outgoing admins signed be replayed against the incoming set.

### Flow B — `SET_SECRET` (the credentials)

Three steps, possibly three different people, chain never written. Two uploads, because
the ref selects the admin set and the two sets are held by different parties:
`deploy/scripts/upload-hex-key.sh` for `hex_api_key` and `hex_cash_url` (the backing set)
and `deploy/scripts/upload-rpc-keys.sh` for every RPC credential (the providers set). Both
sign with every key in `ADMIN_KEYS` (or the single `ADMIN_KEY`), and `--build-only` writes
unsigned envelopes for the signers to sign and a holder of the proxy key to submit. With
fewer local keys than the set's threshold, `upload-rpc-keys.sh` hands its partly signed
envelopes on, and `upload-hex-key.sh` refuses before building and points to
`--build-only`. Both gate on `tools/cmd/verify-tee -printTeeId` first and pass that teeId
to `send-secret build -tee`, and read every secret from the environment rather than argv,
where `ps` would expose it.

```
admin side (tools/cmd/send-secret)
  build    GET proxy/info → the machine's public key; with -tee <id>, a key for any other
           machine is refused
           payload = ECIES(pubkey, value)   value = SECRET_VALUE, or abi(salt, url) built from
                                            HEX_CASH_URL and HEX_CASH_SALT for hex_cash_url
           body = abi(domain, chainId, teeId, version, issuedAt, ref, keccak256(payload))
  sign     one admin signs the body under EIP-191 (ADMIN_KEY); refuses a duplicate signer
  signers  who has signed; with -admins, whether that meets the set's threshold
  submit   POST proxy/direct (DIRECT_API_KEY), wait for the machine's verdict
           (within 1 h of issuedAt)
    ↓ tee-node polls the direct queue → POST /action (type = direct)
enclave
  processDirect → processSetSecret              internal/extension/provisioning.go
  store.Store.ApplySecret                       internal/provisioning/store/store.go
    0 < message ≤ 256 KiB, checked BEFORE the lock
    applyMu held for the whole call            ← the replay defence needs this
    envelope.Verifier.Verify                    internal/provisioning/envelope/envelope.go
      both admin sets installed
      wire.Decode: the envelope domain, and keccak256(payload) = the signed payloadHash
      ≤ 16 signatures, and at least the threshold of the set the ref selects
      chainId and teeId (pinned at boot) match; 0 < len(ref) ≤ 64
      issuedAt non-zero, at most 1 h old (Freshness), at most 2 min ahead (ClockSkew)
      version strictly greater than the last for this ref (in memory)
      at least threshold DISTINCT members of that set recover (eip191.Recover, low-s only)
      EVERY failure returns failure.ProvisionRejected — no probing oracle
    teenode.Decrypt → POST localhost:<SIGN_PORT>/decrypt   ← in-enclave
    with endpoints installed, verifySecret:
      hex_api_key: no space \t \r \n / ? # &
      a ref a PUBLIC endpoint substitutes: the same, and the URL it lands in still resolves
      hex_cash_url, and any PRIVATE endpoint's ref: abi(salt, url) with a non-zero salt
        whose keccak256 equals the published commitment, an https URL with no placeholder
    secrets[ref] = value                       ← memory only, lost on restart
```

The cash source is opened only once both `hex_cash_url` and `hex_api_key` are held; until
then the machine runs cash-less (Flow C). A ref the configuration does not yet mention is
accepted as-is, and `ApplyEndpoints` re-checks every stored secret when a configuration
arrives.

### Flow C — the observation loop (the engine)

Runs on a timer whether or not anyone asks for a price. **This is the only code that
touches the network for data.**

```
poller.Poller.Run — at once, then every 2 min (ObservationInterval)        poller.go
  └─ observeOnce
       store.Store.Open()                                                  store.go
         re-run every tick (never cached), so a rotated credential needs no restart
         NotConfigured until SET_ENDPOINTS has landed (admins are not needed to observe)
         the bonds source (pinned URL), the cash source (nil unless both hex_cash_url and
         hex_api_key are held), and one supply.ChainSource per protocol.Chains entry with
         its RPC clients: each response ≤ 1 MiB (MaxRPCResponseBytes), each call ≤ 3 s
         (ProviderTimeout)
         provider names are POSITIONAL — "rpc_flare_1" — so nothing an operator
         writes can reach the public readiness report
         the Binding: extensionId, feedId, endpointsHash, adminsHash as of this tick
       observation.Observe — 20 s deadline (ObservationDeadline)           observation.go
         S  readAllChains, all chains concurrently
              supply.Read per chain                                        supply.go
                providerHeads: ask every provider for its head
                pinCandidateBlocks: the Quorum-th highest head, then each distinct head
                  below it. NOT the minimum: one provider answering with a two-week-old
                  head dragged the minimum back with it, and every honest provider then
                  corroborated the stale supply at that block perfectly. Findings §24.
                readAtBlock at the first candidate; a corroboration shortfall
                  (SupplyUnavailable, SupplyNoQuorum) steps down to the next, any other
                  fault ends the read
                  agreedBlockTime: that block's own header timestamp, agreed by Quorum,
                    refused if older than the chain's pinned MaxBlockAge (2 min
                    Flare/Songbird, 3 min Ethereum) — SupplyBlockStale. The only check that
                    works at Quorum 1, and the only one that notices a stalled chain
                  readDecimals + readTotalSupply at that EXACT block number
                  vote: `Quorum` IDENTICAL answers and a UNIQUE winner (never a median — a
                    read at a fixed block is deterministic, so disagreement means someone is
                    wrong; and a tie fails closed, because otherwise the provider ORDER in
                    the payload would pick the supply figure)
                markLagging: providers whose head is below the pin are recorded `lagging`
                amount = raw / 10^decimals, exact in big.Rat
              any chain failing fails the whole observation (a partial sum is a wrong
              supply, not a smaller one)
         B  readBacking, both endpoints concurrently
              backing.Fetch → 2 attempts of 5 s each (HTTPTimeout), no redirects ever,
              body ≤ 64 KiB, unknown JSON fields rejected, metric_name must be the pinned
              one, currency USD (required on cash; on bonds, if present), comma grouping
              validated not stripped, last_update refused only if it is in the FUTURE —
              there is no bound on how old it is, because both endpoints republish on an
              on-chain event rather than on a schedule (rationale §2)
              bonds failing is FATAL; cash failing is not
         checkWindow(30 s) across every read timestamp
              read skew only; in practice the 20 s deadline is the tighter bound
         price.Compute → min(1, B/S) × 10⁸, rounded half-up
              no cash? price.ComputeWithoutCash — publishes only if the answer
              provably cannot depend on the missing figure (i.e. already at par)
         feedupdate.ValueFromBig → int32, failing closed out of range
         ObservedAt = homeBlockTime: the header timestamp of Flare's pinned block,
              an instant the host cannot move. It dates the reading; it is not the
              observedAt the update is later signed with (Flow D)
  └─ success → cached as `last`
     failure → recorded (lastErr, counters, source health); the PREVIOUS observation
               stays and ages out after 10 min
```

### Flow D — `GET_FEED` (must finish in 2 seconds)

tee-node allows an extension **2 seconds** per action, and a full observation measures
2.0–2.9 s. That is why C and D are separate: a request costs exactly one signing round
trip, bounded at 1 s.

```
anyone → TeeOracleInstructionsSender.requestFeedUpdate(feedId, teeIds)   payable (upstream)
           FeedNotConfigured if the feed has nothing published; TeeIdNotConfigured if a
           named machine is not at the feed's latest endpoints and admins generation
           dispatches to the named machines; the instruction message is the
           ABI-encoded FeedUpdateRequest{feedId}
    ↓ proxy → tee-node → POST /action
  processInstruction → processGetFeed                        internal/extension/feed.go
    feedupdate.DecodeFeedRequest; anything but exactly our feed → UnexpectedPayload
    poller.Poller.Report(requestedAt = DataFixed.Timestamp)   poller.go
      Latest()       no observation yet → the last failure, or NoObservation;
                     older than 10 min by the machine's clock → ObservationStale
      checkClock     requestedAt = 0 → MissingRequestTime; the observation older than
                     10 min against the request's block timestamp → ClockBehindChain
      no admin set in the observation's binding → NotConfigured
      observation.Sign(signer, obs, requestedAt)              observation.go
        update = FeedUpdate(extensionId, feedId, value, decimals, observedAt = requestedAt,
                            endpointsHash, adminsHash)
        messageHash = keccak256(abi(TEE_ORACLE_FEED, chainId, keccak256(abi(update))))
        teenode.Sign(messageHash preimage) → POST localhost:<SIGN_PORT>/sign
                                   (tee-node applies EIP-191; wire is base64, not hex)
        NormalizeV: 0/1 → 27/28, so the store can use stock OpenZeppelin
        feedupdate.EncodeData → abi(update, signature)
    ActionResult{status: 1, data}
    ↓ tee-node → proxy → served at GET /action/result/<id>
```

The signed `observedAt` is the block timestamp of the requesting instruction, cosigned by
the data providers in `DataFixed`, so every machine answering one request signs the same
value, which the store requires of a batch. The inputs behind it were read at the
observation's own `ObservedAt`, at most 10 minutes earlier, and the reading is checked
against both clocks: the machine's own, which catches upstreams that have stopped, and the
request's, which a slow machine clock cannot pass. A reading taken after the request block
(an instruction delivered late) is accepted; the store's `observedAt < block.timestamp`
rule holds regardless, because the round trip cannot finish within one block, so
`TooFarAhead` is unreachable for honest updates.

### Flow D2 — `GET_READINESS` (the machine's signed account of itself)

Direct only, no fee, no transaction. Same 2-second budget as `GET_FEED`, and met the
same way: one signing round trip and no I/O beyond it — nothing here observes, resolves
configuration or dials an upstream.

```
operator/keeper → POST proxy/direct { GET_READINESS, 0x01 }
    ↓ tee-node → POST /action (type = direct)
  processDirect → processGetReadiness(di, action id)     internal/extension/readiness.go
    require message == exactly 0x01, else UnexpectedPayload
    no provisioner → NotConfigured
    poller.Poller.ReadinessReport(id, store.Readiness())   poller.go
      assembled behind the poller's lock, where the observation state and the
        signer both live; the caller supplies the provisioning half it owns
      payload = JSON types.ReadinessReport — readiness {endpointsSet, endpointsHash,
        adminsSet, adminsHash, ready, missingSecretRefs}, lastObservedAt,
        observationAge, wouldSign, observations, failures, lastErrorClass,
        sourceHealth, chainId
      body    = abi(readiness.Domain(), id, now, keccak256(payload))
                (Domain = keccak256("USDX_ORACLE_READINESS_V1"); now = the enclave's clock)
      teenode.Sign(body) → readiness.EncodeData(body, sig, payload)
```

The body names no teeId: a reader recovers the signer from the EIP-191 signature over
`body`, checks `keccak256(payload)` against the body's hash, and checks the body's id
against the action it sent. `tools/pkg/readinessreport.Decode` does the first two (and the
domain); `keeper.QueryReadiness` and `tools/cmd/readiness` add the third.

Three properties are the whole point:

- **It never fails closed.** An unprovisioned or stale machine answers with a report
  *saying so*. "Nothing is configured" is the answer, not a failure to produce one —
  only an encoding or signing fault is an error here.
- **Its domain differs from the feed update's.** The identity key signs both, so a
  readiness answer can never be reinterpreted as a price however the bytes are routed.
- **It is the machine's only account of itself.** `GET /state` carries the empty state, so
  nothing a request does is counted anywhere a reader could mistake for the poller.

`wouldSign` is the field that earns its keep. It is true only when the cached observation
is within `MaxObservationAge` and was taken with an admin set installed (a set installed
after it counts from the next observation, at most 2 minutes later); it cannot foresee the
per-request clock check (`ClockBehindChain`, `MissingRequestTime`), which depends on the
request's own timestamp. `tools/pkg/keeper.wouldRefuse` reads it before paying for an
on-chain request, so a machine whose newest observation has aged out costs a free direct
action instead of a registry fee. It fails **open**: the keeper skips only when every
proxy it asks answers and none would sign, and an unreachable machine or a build without
the command means "not known", never "do not try". Consumers: `keeper`, `fleet-status`,
`oracle-watch`, `readiness` (behind the scripts' readiness reads), `send-endpoints`,
`send-admins`.

The report is verified but still a *self*-report; see
[`usdx-oracle-readiness-query.md`](usdx-oracle-readiness-query.md) for what that does
and does not settle.

### Flow E — the keeper relay (`tools/`)

```
tools/cmd/keeper → keeper.Run (timer, single-flight; RunOnce for -once) → keeper.RunCycle
  fleet.refresh — the registry's active machines, for their proxy URLs ONLY;  fleet.go
                  the targets are the static -tee list
  Collect                                                          cycle.go
    pendingNonceGap, keeperBalance — diagnostics on the record (a stuck tx blocks
                     everything behind its nonce)
    wouldRefuse    — GET_READINESS to -p and every fleet proxy (needs   readiness.go
                     DIRECT_API_KEY); request_skipped, no fee spent, only if all answer
                     and none would sign
    1. TRIGGER   instrutils.RequestFeedUpdate(sender, feedId, teeIds) → instructionId
                   fee = the registry's GET_FEED fee × target count, read, never hard-coded
    2. COLLECT   pollResults: GET <proxy>/action/result/<id> on -p and every fleet proxy,
                   backoff 1 s doubling to 16 s, until as many proxies as there are targets
                   have answered or -pollTimeout passes
                   404 = pending, not an error
                   status 0 → that machine is skipped and its log recorded as errorClass;
                   inventing a price is the one thing the keeper must never do
                 feedupdate.Decode each result
  Relay
    3. RELAY     Screen — per answer: signer recovered, one this cycle asked, the store's
                   extensionId, our feedId, the feed's latest endpointsHash and adminsHash;
                   a failing answer is dropped and named, the rest go on    preflight.go
                 chooseBatch — one per signer, the largest group agreeing on observedAt
                 Preflight — requiredSignatures, ≤ 32, the observedAt ratchet, then an
                   eth_call rehearsal of submitFeedUpdates that names the failing guard
                 → store.submitFeedUpdates(SignedFeedUpdate[])
                 → wait for the receipt; a timeout is not chased, the next cycle
                   supersedes it
  → Record with an Outcome: accepted · race_lost · rejected · not_mined · no_result ·
    extension_failed · request_failed · request_skipped · dry_run · internal
```

Every `NotNewer` — from the preflight, the rehearsal, or a submission that is mined and
reverts — is classified by re-reading the store's `observedAt`. Stored ≥ ours is
**`race_lost`**: another submission landed first, almost always another keeper's later
request, and the feed advanced, so it must never alert. Stored lower is `rejected` with
guard `NotNewer`. A mined revert is `race_lost` by that rule, or `rejected` with the
decoded guard, else `Reverted`; `not_mined` means only that no receipt arrived within the
deadline. That taxonomy lives in `tools/pkg/keeper/record.go`.

### Flow F — on-chain acceptance, then the consumer read

`TeeOracleFeedStore.submitFeedUpdates(SignedFeedUpdate[])`, in execution order:

```
 1  batch shape — count ≥ requiredSignatures (NotEnoughSignatures) and ≤ 32
    (TooManySignatures); one common observedAt (ObservedAtMismatch); the
    contributors' decimals within 8 of each other (DecimalsSpreadTooBig)
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

`requiredSignatures`, `maxSpreadBIPS` and `maxSpreadAbsolute` are the governance-set
submission policy (`setSubmissionPolicy`). The store keeps **no per-machine
bookkeeping**. What a machine is running is assembled from the sender's per-feed
commitments (`latestEndpointsHash` / `latestAdminsHash` / versions, plus
`isTeeIdConfigured` per machine), the machine's own `GET_READINESS`, and the `FeedUpdated`
event that names every contributing machine. See `fleet-status`, which reconciles those
three.

```
FtsoV2 → TeeOracleFeedStore.getCurrentFeed()      payable
           reverts NoValuePublished before the first accepted update
           otherwise → (value int256, decimals, observedAt), never reverting on staleness;
           observedAt is always below block.timestamp, enforced at submission
           read fee resolves through FeeCalculator; msg.value → feeDestination
           (FeeTooLow / FeeTransferFailed)
```

The store implements `IICustomFeed` and registers with FtsoV2 directly — no
separate adapter. The emergency pause is extension-level (FlareTeeManager): it blocks new
submissions and freezes the last value, but it cannot withdraw a value already stored.

---

## 7. Contracts

Both contracts are upstream's generic TEE oracle contracts. The Go bindings are generated
from the interfaces in a sibling flare-smart-contracts-v2 checkout (see `contracts/`), and
`contracts/script/DeployTeeOracle.s.sol` behind `deploy/scripts/deploy-oracle.sh` deploys
them, deployer-governed, for the local simulation and testnets; production is upstream's
own deploy plus Flare governance.

| Contract | Entry points | Notes |
| --- | --- | --- |
| `TeeOracleInstructionsSender` | `requestFeedUpdate(feedId, teeIds)` payable, **anyone** · `setEndpoints(feedId, groups, claimBack)` and `setAdmins(feedId, roles, claimBack)` payable, **governance**, dispatched to the whole active set · `pushEndpoints(feedId, teeIds, groups)` and `pushAdmins(feedId, teeIds, roles)` payable, **anyone**, the catch-up for machines that missed a publication · reads: `latestEndpointsHash`/`latestAdminsHash`/`endpointsVersion`/`adminsVersion` (per feed), `expectedEndpointsVersion`/`expectedAdminsVersion` and `isTeeIdConfigured` (per machine), `getEndpointsPushTargets`/`getAdminsPushTargets`, `getEndpointsPublicationFee`/`getAdminsPublicationFee`, `getFeedIds()` | the extension's registered instructions sender; assigns the config version and records the commitments before dispatch |
| `TeeOracleFeedStore` | `submitFeedUpdates(SignedFeedUpdate[])` **anyone** · `getCurrentFeed()` payable (also `observedAt()`, `getSubmissionPolicy()`, `requiredSignatures()`, `calculateFee()`) · governance: `setSubmissionPolicy`, `setFeeDestination` | implements `IICustomFeed` and registers with FtsoV2 directly; verifies every signature through `Fdc2Verification`, takes the median of a batch and bounds its spread |

---

## 8. `tools/` map

| Path | Role |
| --- | --- |
| `pkg/support` | chain client, key, ABI/revert plumbing shared by every tool |
| `pkg/configs` | env-var defaults so tools run flag-free from a sourced `.env`, and contract addresses by name from `deploy/config/<chain>/deployed-addresses.json` |
| `pkg/direct` | the proxy's direct channel: `Post`, `Result`, `Await`, and `PostToExtension` for the dev harness |
| `pkg/feedupdate` | decodes the signed `FeedUpdate` through the extension's wire package and recovers the signer |
| `pkg/readinessreport` | decodes a `GET_READINESS` answer, checks the payload hash and recovers the signer |
| (payloads) | the `Endpoints`/`Admins` payloads and their hashes come from the extension's `pkg/wire/configpayload` |
| `pkg/keeper` | `Run` (timer) · `RunOnce` · `RunCycle` = `Collect` + `Relay` · `Screen` · `Preflight` · `PollResult` · `QueryReadiness` · `ActiveMachines` · `Record`/`Outcome` |
| `pkg/utils/instructions.go` | `RequestFeedUpdate`, `FeedUpdateFee`, `EncodeSetEndpoints`/`EncodeSetAdmins` (+ `Publication`, `Handoff`), fee reads (fees are **read**, never hard-coded) |
| `pkg/fccutils` | FCC registration, governance, policy-consistency, the registry's machine records, revert decoding |
| `pkg/contracts/teeoraclesender`, `pkg/contracts/teeoraclestore` | generated bindings from the upstream interfaces (`deploy/scripts/generate-bindings.sh`) |
| `pkg/validate` | the deploy/verify check suite behind `verify-deploy` |

---

## 9. Five cross-cutting rules

**1. The failure vocabulary is closed.** `failure.Failure` has unexported fields and no
exported constructor, so a value can only be one of the 28 declared in
`internal/failure` (`failure.All`). `ActionResult.Log` and the readiness report are both
world-readable, and an ordinary Go error carries exactly what must not appear there — an
API key inside a `*url.Error`, a reserve figure inside a parse error. This is enforced by
the compiler, not by review.

**2. Fail closed, always.** A source down, a malformed or impossible value, an observation
older than 10 minutes: publish nothing and let the on-chain timestamp age. A frozen feed is
the designed failure mode; a confidently wrong price is not recoverable downstream. There
is no price band: the price is bounded to [0, 1] by construction, and a real depeg
publishes the sub-par price. **Consumers must check the timestamp.**

**3. Pinned vs runtime.** Pinned in the build, and so covered by the registered code
hash: everything in `pkg/protocol` and `internal/config` — the price definition, the chain
set and token addresses, the feed id, the bonds URL, both metric names, every deadline and
bound. Runtime and governed: the RPC provider URLs and quorums (Flow A); the cash URL,
which governance publishes only as a salted commitment in the PRIVATE `hex_cash` entry and
a backing admin provisions as the preimage (Flows A and B); the admin sets (Flow A2); the
credentials (Flow B). `EXTENSION_ID` and `CHAIN_ID` come from the environment, which
tee-node reads too, and have no default.

**4. Nothing operator-supplied reaches a public surface.** Provider names are
positional (`rpc_flare_1`), backing source names are identifiers not URLs (rejected at
construction if they look like one), and readiness names secret *refs* never values.

**5. Every wait and every read is bounded.**

| Bound | Value | Where |
| --- | --- | --- |
| tee-node's answer budget per action | 2 s | tee-node, not configurable |
| one call to tee-node's `/sign` or `/decrypt` | 1 s | `pkg/server:signTimeout` |
| startup wait for the machine's own address | 1 min, retried every 1 s, then exit | `pkg/server:teeIDTimeout`, `teeIDRetry` |
| one observation | 20 s | `config.ObservationDeadline` |
| one RPC provider's answer, per round | 3 s | `config.ProviderTimeout` |
| one RPC response body | 1 MiB | `config.MaxRPCResponseBytes` |
| one Hex attempt, two attempts | 5 s each | `config.HTTPTimeout`, `backing.attempts` |
| one Hex response body | 64 KiB | `backing.maxBodyBytes` |
| read skew within an observation | 30 s | `config.ObservationWindow` |
| age of an observation that may be signed | 10 min | `config.MaxObservationAge` |
| a `SET_ENDPOINTS`, `SET_ADMINS` or `SET_SECRET` message | 256 KiB | `store.maxPayloadBytes` |
| an envelope's signatures / issuedAt age / issuedAt ahead | 16 / 1 h / 2 min | `pkg/wire/envelope` |
| an admin set / its threshold | 32 / 16 | `protocol.MaxAdmins`, `protocol.MaxThreshold` |

---

## 10. Where to start reading

```
pkg/server/server.go                      the whole dependency graph
internal/extension/router.go              the two routers and the channel rule
internal/observation/observation.go       one observation, start to finish
internal/observation/poller/poller.go     why observing and signing are separate
pkg/wire/feedupdate/feedupdate.go         the FeedUpdate encoder + SignedPayload digest
internal/provisioning/store/store.go      what a configuration may say
tools/pkg/keeper/cycle.go                 the relay, end to end
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
| How is the machine doing over time? | `./deploy/scripts/oracle-watch.sh` |

---

## 11. Gotchas worth knowing before you touch it

- **The admin sets are published, not baked, and are memory-only.** A machine starts with
  none and rejects every `SET_SECRET` until `SET_ADMINS` lands, so they must reach every new
  or restarted machine (by publication or `pushAdmins`) *before* any credential. There are
  TWO sets and the envelope's ref picks one: `hex_api_key` and `hex_cash_url` → `backing`
  (1-of-N allowed), everything else → `providers` (2-of-N minimum). The checked-in
  `deploy/config/admins/dev-wellknown-keys.json` names private keys 1, 2 and 3 — local and
  testnet only.
- **A machine that joins after a publication cannot be requested.** Publications go to
  the active set when they execute; a later machine needs `pushEndpoints` and `pushAdmins`,
  which no repo tool wraps, and until then `requestFeedUpdate` naming it reverts
  `TeeIdNotConfigured`.
- **`GET /state` carries the empty state.** The machine's counters and health are the
  poller's, read through `GET_READINESS`, which needs the proxy's key and a machine that
  answers. Alert on on-chain feed *age* as well, which needs neither.
- **The update binds no store and no request.** It binds the chain, the extension and the
  feed, so it is valid in any store of the same extension serving the same feed on the same
  chain; the store's `observedAt` ratchet is the replay bar. Findings §18.
- **`observedAt` is the request's block timestamp.** At most one update per distinct block
  timestamp can ever be accepted: two keepers requesting in the same block each pay a fee,
  and only one submission carrying that timestamp lands. An enclave's clock cannot move
  the stamp; a clock that runs slow is caught by `checkClock` instead.
