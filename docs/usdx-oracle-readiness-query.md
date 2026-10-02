# USDX Oracle — `GET_READINESS`

A read-only action that asks a machine what it is running, how its sources are behaving, and
whether it would sign a price right now — answered from cached state, signed by the enclave, and
delivered over the proxy's direct channel.

Companion docs: [usdx-oracle-flows.md](usdx-oracle-flows.md) (what each instruction does inside
the enclave) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the consumer) ·
[usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (the operator's runbook).

Code: `internal/extension/readiness.go` (the handler), `internal/observation/poller`
(`Poller.ReadinessReport`, which assembles and signs it), `pkg/types/types.go` (the JSON shape),
`pkg/wire/readiness` (the signed body), `tools/pkg/readinessreport` (verification) and
`tools/pkg/keeper/readiness.go` (`QueryReadiness`, which every tool calls).

---

## Why

This is the one way to ask a machine about itself. The extension's `GET /state`, which tee-node
reads on every TEE info response and folds into the attested `/info`, carries the empty state.
A copy of these fields there would be unsigned, written by whatever served the request rather
than by the observation loop, and readable by anyone. `GET_READINESS` describes the engine
itself, signs the answer, and binds it to the request that asked.

Everything that reads a machine goes through it: `fleet-status`, `send-endpoints` and
`send-admins` for delivery confirmation, `oracle-watch`, the keeper's preflight, and the operator
scripts through `tools/cmd/readiness` (`read_machine_readiness` in `deploy/scripts/lib/env.sh`).
All of them need the proxy's direct-channel key, `DIRECT_API_KEY`, and refuse to ask without one
(the simulation's proxy accepts any value).

### Scope

Read-only, and it changes nothing. It is not a price source — `GET_FEED` remains the only signed
feed update, under its own domain. It is not a substitute for the on-chain proof that a machine
runs the published configuration; see [What it does not replace](#what-it-does-not-replace). It
has no on-chain counterpart and needs no contract.

---

## Shape

| | |
| --- | --- |
| OP type | `TEE_ORACLE` — the one op type every command shares |
| OP command | `GET_READINESS` |
| Channel | **direct only**: `POST <proxy>/direct` with the proxy's `X-API-Key` |
| Request payload | exactly one byte, `0x01` (`protocol.ReadinessRequestV1`) |
| Result | `GET <proxy>/action/result/<id>?submissionTag=submit`, where `<id>` is the action id the proxy assigned |
| Response `data` | `abi.encode(bytes body, bytes signature, bytes payload)` (`readiness.EncodeData`) |

The request carries a version byte because the registry rejects an empty message, and a single
checkable constant is the smallest payload that satisfies it while letting the extension reject
anything it does not recognise. (`GET_FEED`'s request is different — an ABI-encoded
`FeedUpdateRequest` naming the feed — but readiness is direct-only with no on-chain counterpart,
so nothing upstream defines its shape and it keeps the one-byte envelope.)

**Direct only, unlike `GET_FEED`.** A feed update must be requestable permissionlessly, because
liveness of the feed is a public good. Readiness is the opposite: it is operational detail its
operator needs, the answer is stale within minutes, and putting it on-chain would spend a fee per
poll to write ephemeral state into permanent public calldata. The direct channel is gated by
`DIRECT_API_KEY`, which scopes it to whoever runs the machine — the right audience. Raised as an
instruction, it is refused with `WrongActionType`.

---

## What it answers

The payload is JSON, `types.ReadinessReport`. Every field is a boolean, a hash, a name from a
closed vocabulary, or a number — never a credential, a URL, a reserve or supply figure, or an
upstream's own words. The provisioning fields are nested under `"readiness"`; everything else is
top-level:

```json
{
  "readiness": {
    "endpointsSet": true,
    "endpointsHash": "0x5c1e…",
    "adminsSet": true,
    "adminsHash": "0x9a07…",
    "ready": false,
    "missingSecretRefs": ["hex_api_key", "hex_cash_url"]
  },
  "lastObservedAt": 1759390802,
  "observationAge": 47,
  "wouldSign": true,
  "observations": 41,
  "failures": 2,
  "lastErrorClass": "",
  "sourceHealth": {
    "rpc_flare": "ok", "rpc_flare_1": "ok", "rpc_flare_2": "lagging", "rpc_flare_3": "ok",
    "rpc_ethereum": "ok", "rpc_ethereum_1": "ok", "rpc_ethereum_2": "ok", "rpc_ethereum_3": "disagree",
    "rpc_songbird": "ok", "rpc_songbird_1": "ok", "rpc_songbird_2": "ok", "rpc_songbird_3": "down",
    "hex_bonds": "ok",
    "hex_cash": "not_configured"
  },
  "chainId": 16
}
```

A script reads the nested fields with the prefix: `jq '.readiness.missingSecretRefs[]'`.
`tools/cmd/readiness` prints this object with two fields added at the top level, `signer` and
`instructionId`, from the verification below.

### Provisioning — `"readiness"`, from `store.Store.Readiness`

| Field | Type | Notes |
| --- | --- | --- |
| `endpointsSet` | bool | a `SET_ENDPOINTS` has been applied |
| `endpointsHash` | hash | keccak256 of the applied payload as received; what `send-endpoints` and `fleet-status` compare against `latestEndpointsHash(feedId)`. The zero hash until one is applied |
| `adminsSet` | bool | a `SET_ADMINS` has been applied |
| `adminsHash` | hash | the same for the admin sets, against `latestAdminsHash(feedId)` |
| `missingSecretRefs` | []string | **ref names only.** `hex_api_key` always, plus every secret ref the applied endpoint configuration names (`hex_cash_url` among them), less those installed |
| `ready` | bool | endpoints and admin sets applied, and nothing missing |

### Observation — from `internal/observation/poller`, not the action handler

| Field | Type | Notes |
| --- | --- | --- |
| `lastObservedAt` | uint64 | unix seconds when the newest successful observation's inputs were read: the Flare pinned block's header timestamp. Zero before the first |
| `observationAge` | uint64 | seconds since `lastObservedAt`, by the enclave's clock at answering; the number an operator alerts on. Reads 0 if that clock is behind the block's time |
| `wouldSign` | bool | see [`wouldSign`](#wouldsign) |
| `observations` | int | successful observations since boot |
| `failures` | int | failed observations since boot. An unprovisioned machine counts one every tick |
| `lastErrorClass` | string | the `failure.Class` of the newest observation if it failed, empty if it succeeded — see below |

**These come from the poller.** They describe the engine, not the last request served: the
poller observes every 2 minutes on its own timer whether or not anyone asks, and a request
records nothing (`internal/extension/result.go`). A counter written by the request handler would
read like a dead poller for a machine nobody has asked, and like a failing one when a key holder
posts refused requests.

So `lastErrorClass` covers only what an observation can fail with: `not_configured` (no
endpoints yet, or a provider URL whose credential has not arrived), `source_unavailable`,
`source_invalid`, `supply_quorum_failed`, `implausible_value` and `internal`. A refusal on the
request path — the clock check, a wrong request payload, a signing fault — reaches only that
request's own result.

### `wouldSign`

`wouldSign` is true when the poller holds an observation no older than `MaxObservationAge`
(10 minutes, by the enclave's clock) **and** that observation was taken with an admin set
installed, so the update it would sign commits to one — the checks `Poller.Report` makes that do
not depend on the request. An admin set installed after the newest observation counts from the
next one, at most 2 minutes later. A keeper can skip a cycle it would otherwise pay for when no
machine would sign.

It is a forecast, not a guarantee. It cannot foresee the per-request clock check, because that
check needs the request's timestamp: `GET_FEED` still refuses with `ClockBehindChain` when the
requesting block is more than 10 minutes after the reading (only a slow enclave clock lets such a
reading pass the local bound), and with `MissingRequestTime` when the instruction carries no
block timestamp. Nor can it see the signing call itself, or anything on-chain:
`requestFeedUpdate` refusing an unconfigured target, and every store guard, come after.

### Sources — `sourceHealth`, the health map of the newest observation

`map[string]string`, positional names against the closed health vocabulary:

| Name | Meaning | Statuses it takes |
| --- | --- | --- |
| `rpc_<chain>_<n>` | one provider, numbered from 1 by its position in the published group (`types.ProviderHealthKey`) | `ok`, `down`, `disagree`, `lagging`, `unknown` |
| `rpc_<chain>` | the chain as a whole — did its supply read succeed (`types.ChainHealthKey`) | `ok`, `down`, `unknown` |
| `hex_bonds` | the public reserves endpoint | `ok`, `down`, `unknown` |
| `hex_cash` | the restricted cash endpoint | `ok`, `down`, `not_configured`, `unknown` |

Statuses are the `types.Health*` constants (`pkg/types/health.go`) and nothing else: `ok`, `down`,
`disagree`, `lagging`, `not_configured`, `unknown`. They are not interchangeable, and the
distinction is the diagnosis — `lagging` means a provider's head is below the pinned block, so it
is expected to fail there; `down` means the call returned nothing usable; `disagree` means it
answered with a different value from the winning one; `not_configured` on `hex_cash` means the
machine lacks the cash URL or the API key and is running cash-less; `unknown` is a source the
observation never reached. Collapsing them sends an operator hunting an outage that is not there.

The map is replaced by every observation that gets as far as reading sources, failed or not, and
is `null` until the first one: a machine without endpoints has nothing to read.

Names are **positional, never operator-supplied**, so nothing an endpoint author writes reaches
this surface.

### `chainId`

The chain the machine signs for (`CHAIN_ID`), so a report cannot be mistaken for another
deployment's. It is in the payload, covered by the payload hash, not in the signed body. There is
no `teeId` field: the machine is the recovered signer.

---

## Signing

The body is four static ABI words, 128 bytes, signed by the enclave (`readiness.EncodeBody`):

```
body = abi.encode(
    bytes32 domain,         keccak256("USDX_ORACLE_READINESS_V1")
    bytes32 instructionId,  the proxy's action id for this request
    uint64  timestamp,      the enclave's clock when it answered, unix seconds
    bytes32 payloadHash     keccak256 of the JSON payload, as emitted
)
```

tee-node signs it the way it signs everything: `POST /sign` keccaks the body and signs the
EIP-191 personal-message hash of that, and the extension returns `v` as 27/28. The JSON travels
beside the body, not inside it; the hash is what binds it.

A **distinct domain** from the price, whose first signed word is the ASCII prefix
`bytes32("TEE_ORACLE_FEED")`, and from the provisioning envelope's: a readiness answer can never be
read as a price or vice versa, though the same key signs both. Bound to `instructionId` and
`timestamp`, so a captured answer cannot be presented as the answer to another request, or later
as a current one — the field an operator trusts least is the one that ages.

Signing is the entire reason this exists rather than an exposed port: the extension listens on
loopback only, and an unsigned answer relayed by the proxy would be a claim by whatever answered.
Signed, it is the enclave's own claim, the same standard the price is held to.

### Verifying an answer

`tools/pkg/readinessreport.Decode` and `keeper.QueryReadiness` do this; anything else reading a
machine must do all of it:

1. ABI-decode `data` as `(bytes body, bytes signature, bytes payload)`. The body must be exactly
   128 bytes, the signature 65, the payload non-empty.
2. ABI-decode the body as `(bytes32, bytes32, uint64, bytes32)` and require the first word to be
   `keccak256("USDX_ORACLE_READINESS_V1")`. A price signature fails here.
3. Require `keccak256(payload) == payloadHash`, over the bytes as received. Never re-serialise the
   JSON: the signature covers those exact bytes.
4. Recover the signer: `ecrecover(keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(body)),
   signature)`, with `v` 27/28 (0/1 accepted) and the high-`s` form rejected (`pkg/wire/eip191`).
5. Require `instructionId` to equal the action id the proxy returned for this `POST /direct`.
6. Only then parse the payload as JSON.

The recovered signer is the machine that answered. Compare it with the teeId you meant to ask
(`verify-tee -printTeeId`, or the registry): the repo's tools record and print the signer, and the
keeper keys its per-machine health by it, but none of them compares it with an expected id.
Check `timestamp` against your own clock if freshness matters to you; the tools do not.

---

## What it must never contain

- **Credential values.** Ref *names* only. `missingSecretRefs` says `hex_api_key`, never anything
  about the key itself.
- **Raw upstream error strings.** Only `failure.Class` values from `internal/failure`:
  `source_unavailable`, `source_invalid`, `supply_quorum_failed`, `implausible_value`,
  `sign_failed`, `internal`, `not_configured`, `clock_skew`. The poller converts any error that is
  not a declared `Failure` to `internal` before it can reach the report. `ActionResult.Log` is
  world-readable and this report goes to every key holder, so the discipline has to be uniform to
  be auditable.
- **Provider URLs.** The public ones are already in the on-chain configuration, and the cash URL
  is published only as a salted commitment; repeating either here would widen what one response
  discloses.
- **Supply or reserve figures, and the price.** The price is what `GET_FEED` signs; the
  components may be commercially sensitive to Hex (spec §4.1).

---

## Budget

It answers **from cached state, with one round trip of I/O**: the signing call to tee-node over
loopback, bounded at 1 s (`pkg/server`'s `signTimeout`). tee-node allows an extension two seconds
per action, and an observation takes longer than that — which is exactly why observation lives on
a timer and a `GET_FEED` costs only one signature. Readiness does not re-observe, re-open
configuration or dial an upstream. It copies what the poller holds under its lock, encodes, signs
once, and returns.

---

## Failure behaviour

**It answers whenever it can.** Unlike `GET_FEED`, it does not fail closed: an unprovisioned
machine returns a report saying `endpointsSet: false, ready: false, wouldSign: false`, because
"I have nothing configured" is the answer, not a failure to produce one.

What does fail, as a status-0 result whose `Log` is `error: <reason>`:

| Cause | Reason (`internal/failure`) | Class |
| --- | --- | --- |
| raised as an on-chain instruction | `WrongActionType` | `source_invalid` |
| request payload other than the single byte `0x01` | `UnexpectedPayload` | `source_invalid` |
| an extension built without a provisioner (test harnesses only; `pkg/server` always wires one) | `NotConfigured` | `not_configured` |
| tee-node's sign server unreachable, or refusing | `SignUnavailable`, `SignRejected` | `sign_failed` |
| an encoding fault | `Internal` | `internal` |

Before the command is routed, the router answers a malformed action itself, as an HTTP error that
tee-node wraps into a status-0 result: 400 for an undecodable `DirectInstruction`, 501 for an op
type other than `TEE_ORACLE`. The tools treat any status-0 answer as the machine refusing the
request, distinct from a machine that cannot be reached.

---

## Consumers

| Who | Uses it for |
| --- | --- |
| **keeper** | the preflight before every cycle, when it has `DIRECT_API_KEY`: it asks `-p` and every machine the registry lists for the store's extension, concurrently, 15 s each. It skips the request (`request_skipped`, no fee) only when **every** one answers and **none** reports `wouldSign`; an unreadable proxy or a missing key means it requests anyway. Each machine's `sourceHealth`, keyed by recovered signer, goes into the cycle record |
| **`fleet-status`** | the `live` (endpoints hash) and `ready` columns per registered machine, the drift warning when the live hash differs from the feed's latest, and `liveAdminsHash` and `sourceHealth` in `-json`. Asked at each machine's registered URL; without the key those columns stay empty |
| **`send-endpoints` / `send-admins`** | the delivery check after a publication: they ask the machine at `-p` until its `endpointsHash` / `adminsHash` names the hash just published (`-wait`, 90 s). Without the key the check is skipped |
| **`oracle-watch`** | follows one machine over time: polls every 2 minutes (`-interval`), appends each verified sample to a records file, and prints every change in source health, `lastErrorClass`, `ready`, `wouldSign` and the failure count |
| **`tools/cmd/readiness`** | prints one verified report as JSON; `read_machine_readiness` in `deploy/scripts/lib/env.sh` wraps it for the scripts |
| **`upload-rpc-keys.sh`** | which refs to upload: `.readiness.missingSecretRefs` |
| **`upload-hex-key.sh`** | confirming the backing credentials are no longer missing |
| **`publish-local.sh`, `oracle-status.sh`** | the simulation's delivery check (`.readiness.endpointsHash` / `adminsHash`) and the status page |

---

## What it does not replace

**Commissioning remains the proof.** The store compares every update's `endpointsHash` and
`adminsHash` with the sender's `latestEndpointsHash(feedId)` and `latestAdminsHash(feedId)` before
it accepts it, so a machine whose update has landed has *proven* on-chain what it runs.
`GET_READINESS` is signed, but it is still the machine describing itself: it says what the machine
believes, where a landed update says what the chain accepted. Use it to find problems early and to
decide whether a cycle is worth paying for — not as the authority on whether a machine's prices
will be taken.

It also cannot see past its own boundary. The extension holds RPC clients for its supply reads,
opened afresh for each observation, but nothing that reads the registry or the sender. It cannot
tell you whether it is registered, whether its status is `PRODUCTION`, whether the sender has
dispatched it the feed's latest configuration (`isTeeIdConfigured`, which `requestFeedUpdate`
enforces per target), whether its proxy is reachable from outside, or whether its tunnel is up.
Those come from `fleet-status` reading the registry and the sender, and they are the failures
that most often matter.

---

## Decisions

- **No contract.** The command is direct-only, with no counterpart in
  `TeeOracleInstructionsSender`; nothing on-chain reads or stores it.
- **The keeper asks before every cycle**, not only after a failure. A query costs no fee and no
  gas, so the keeper asks every machine each cycle, both to avoid paying for a request no machine
  would sign and to record which machine saw which source fail. `oracle-watch` covers the time
  between cycles.
- **No rate limit in the extension.** Anyone holding `DIRECT_API_KEY` can ask as often as they
  like; each answer costs the machine one signing round trip and a copy of cached state. The key
  is the gate.
- **No `lastPrice`.** The price is obtainable through `GET_FEED`, and a second copy would be a
  second source of truth for the same number, outside the store's checks.

### Changing the report

A new field is an extension rebuild, and a rebuild is a new machine:

- Under real attestation (`MODE=0`) the code hash changes, so the new build must be allowed
  (`allow-tee-version`) before it can register. Under simulated attestation (`MODE=1`) tee-node
  reports its fixed test code hash and the allowed set does not change.
- tee-node generates the machine's identity key in memory at boot, so the restarted container has
  a new teeId. It must be registered, brought to the feed's latest configuration (`pushEndpoints`,
  `pushAdmins`) and re-provisioned (`SET_SECRET`, memory-only), and the old registration retired.
- The report is JSON beside a fixed body, so adding a field changes neither the domain nor the
  body layout. A change to the body itself must change `readiness.DomainName`.
