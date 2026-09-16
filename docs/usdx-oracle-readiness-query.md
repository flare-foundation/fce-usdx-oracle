# USDX Oracle — `GET_READINESS`

A read-only action that asks a machine what it is running, how its sources are behaving, and
whether it would sign a price right now — answered from cached state, signed by the enclave, and
delivered over the channel that already works.

Companion docs: [usdx-oracle-flows.md](usdx-oracle-flows.md) (what each instruction does inside
the enclave) · [usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the consumer) ·
[usdx-oracle-production-setup.md](usdx-oracle-production-setup.md) (the operator's runbook).

---

## Why

This is the one way to ask a machine about itself. The extension's `GET /state`, which tee-node
folds into the attested `/info`, carries the empty state: it once carried a second, unsigned copy
of the fields below, written by the `GET_FEED` handler and therefore up to a keeper cycle behind,
with counters that any holder of the direct-channel key could move. `GET_READINESS` describes
the engine itself, signs the answer, and binds it to the request that asked.

Everything that reads a machine goes through it: `fleet-status`, `send-endpoints` and
`send-admins` for delivery confirmation, `oracle-watch` and the keeper's preflight, and the
operator scripts through `tools/cmd/readiness` (`read_machine_readiness` in `lib/env.sh`). All of
them need the proxy's direct-channel key.

### Scope

Read-only, and it changes nothing. It is not a price source — `GET_FEED` remains the only signed
feed update, under its own domain. It is not a substitute for the on-chain proof that a machine
runs the published configuration; see [What it does not replace](#what-it-does-not-replace). It
needs no contract change.

---

## Shape

| | |
| --- | --- |
| OP type | `TEE_ORACLE` — a read of the oracle's own state, alongside `GET_FEED` |
| OP command | `GET_READINESS` |
| Channel | **direct only** |
| Request payload | `READINESS_REQUEST_V1 = 0x01`, one version byte |
| Response | `abi.encode(body, signature, payload)`, where the body commits to the JSON payload by hash |

The request carries a version byte because the registry rejects an empty message, and a single
checkable constant is the smallest payload that satisfies it while letting the extension reject
anything it does not recognise. (`GET_FEED`'s request is different — an ABI-encoded
`FeedUpdateRequest` naming the feed — but readiness is direct-only with no on-chain counterpart,
so nothing upstream defines its shape and it keeps the one-byte envelope.)

**Direct only, unlike `GET_FEED`.** A feed update must be requestable permissionlessly, because
liveness of the feed is a public good. Readiness is the opposite: it is operational detail its
operator needs, the answer is stale within minutes, and putting it on-chain would spend a fee per
poll to write ephemeral state into permanent public calldata. The direct channel is gated by
`DIRECT_API_KEY`, which scopes it to whoever runs the machine — the right audience.

---

## What it answers

Four groups. Every field is either a boolean, a hash, a name from a closed vocabulary, or a
number — never a credential, a URL, or an upstream's own words.

### Provisioning — from `store.Store`

| Field | Type | Notes |
| --- | --- | --- |
| `endpointsSet` | bool | |
| `endpointsHash` | hash | what `send-endpoints` compares against `latestEndpointsHash(feedId)` |
| `adminsSet` | bool | |
| `adminsHash` | hash | what `send-admins` compares against `latestAdminsHash(feedId)` |
| `missingSecretRefs` | []string | **ref names only.** Never a value, never a hint about one |
| `ready` | bool | every credential the current configuration names is installed |

### Observation — from `observe.Poller`, not the action handler

| Field | Type | Notes |
| --- | --- | --- |
| `lastObservedAt` | uint64 | unix seconds of the newest observation |
| `observationAge` | uint64 | seconds; the number an operator actually alerts on |
| `wouldSign` | bool | age ≤ `MaxObservationAge`, i.e. would a `GET_FEED` succeed right now |
| `observations` | int | cumulative successes, `Poller.Stats()` |
| `failures` | int | cumulative failures, `Poller.Stats()` |
| `lastErrorClass` | string | `internal/failure` vocabulary only — see below |

**These come from the poller.** They describe the engine, not the last request served. A
counter written by the request handler reads like a dead poller for a machine nobody has asked,
and reads like a failing one when a key holder posts refused requests — both happened before the
state was emptied, and both are why the report is produced by the poller and nothing else.

### Sources — the health map of the newest observation

`map[string]string`, positional names against the closed health vocabulary:

| Name | Meaning |
| --- | --- |
| `rpc_<chain>_<n>` | one provider, numbered by its position in the published configuration |
| `rpc_<chain>` | the chain's aggregate — did it reach quorum |
| `hex_bonds`, `hex_cash` | the two backing sources |

Statuses are `config.Health*` and nothing else: `ok`, `down`, `degraded`, `disagree`, `lagging`,
`not_configured`, `unknown`. They are not interchangeable and the distinction is the diagnosis —
`lagging` means a provider does not hold the pinned block and is expected to fail there, `down`
means the call returned nothing, `disagree` means it answered with a different value from the
majority. Collapsing them sends an operator hunting an outage that is not there.

Names are **positional, never operator-supplied**, so nothing an endpoint author writes reaches
this surface.

### Identity

| Field | Type | Notes |
| --- | --- | --- |
| `teeId` | address | binds the answer to one machine |
| `chainId` | uint64 | binds it to one network |

---

## Signing

The body is ABI-encoded and signed by the enclave, in the same shape as the price report:

```
domain      keccak256("USDX_ORACLE_READINESS_V1")
teeId       address
instructionId  bytes32   the action this answers
timestamp   uint64       when the enclave answered
payloadHash bytes32      keccak256 of the readiness body
```

A **distinct domain** from the price report, so a readiness answer can never be replayed as a
price or vice versa. Bound to `instructionId` and `timestamp` so a captured answer cannot be
presented later as a current one — the field an operator trusts least is the one that ages.

Signing is the entire reason to build this rather than publish port `7702` and read `/state` over
the network. Unsigned, it is a claim by whatever answered the port; signed, it is the enclave's
own claim, which is the same standard the price is held to.

---

## What it must never contain

- **Credential values.** Ref *names* only. `missingSecretRefs` says `hex_api_key`, never anything
  about the key itself.
- **Raw upstream error strings.** Only `internal/failure` classes: `not_configured`,
  `source_unavailable`, `source_invalid`, `supply_quorum_failed`, `implausible_value`,
  `sign_failed`, `internal`. Raw errors have leaked API keys and reserve figures into
  `ActionResult.Log` and `/state` before; the closed vocabulary exists because that surface is
  world-readable and the discipline has to be uniform to be auditable.
- **Provider URLs.** They are already public in the on-chain configuration; repeating them here
  adds nothing and widens what a single response discloses.

---

## Budget

It must answer **from cached state, with no I/O**. tee-node allows an extension two seconds per
action, and an observation takes longer than that — which is exactly why observation lives on a
timer and a `GET_FEED` costs only one signature. Readiness must not re-observe, re-resolve
configuration, or dial anything. It reads what the poller already holds, encodes, signs once, and
returns.

---

## Failure behaviour

**It always answers.** Unlike `GET_FEED`, it never fails closed: an unprovisioned machine returns
a body saying `endpointsSet: false, ready: false` rather than an error, because "I have nothing
configured" is the answer, not a failure to produce one. The only path that returns a failure is
an internal encoding or signing fault, which is `sign_failed` or `internal` and means the machine
is broken in a way readiness cannot describe.

---

## Consumers

| Who | Uses it for |
| --- | --- |
| **keeper** | skip a cycle when `wouldSign` is false rather than paying a fee for a result it can predict; alert on `observationAge` and per-source health from outside the machine |
| **`fleet-status`** | the `live` line, `config proven`, and the per-source notes it already renders but cannot currently populate |
| **`upload-rpc-keys.sh`** | "which refs is this machine missing" — without `--refs` or `ENDPOINTS_FILE` |
| **`send-endpoints` / `send-admins`** | the delivery check, which today cannot distinguish a machine that disagrees from one that says nothing |

---

## What it does not replace

**Commissioning remains the proof.** The store verifies `configHash` and `adminsHash` against the
sender before it accepts a price, so a machine that has landed one has *proven* on-chain what it
runs. `GET_READINESS` is signed, but it is still the machine describing itself: it says what the
machine believes, where a landed price says what the chain accepted. Use it to find problems
early and to decide whether a cycle is worth paying for — not as the authority on whether a
machine's prices will be taken.

It also cannot see past its own boundary. The extension holds no chain client and no view of the
network, so it cannot tell you whether it is registered, whether its status is `PRODUCTION`,
whether its proxy is reachable from outside, or whether its tunnel is up. Those come from
`fleet-status` reading the registry, and they are the failures that most often matter.

---

## Cost of adding

- **No contract change.** Nothing on-chain moves.
- An extension rebuild, which means a container restart, and therefore a **new enclave identity
  and re-registration** — redis is `tmpfs`, so the key does not survive. That is the real cost,
  and it also adds a permanently-registered dead machine to the extension unless the retirement
  gap is closed first.
- Under `SIMULATED_TEE` the code hash is a constant, so `allow-tee-version` is unaffected. Under
  real attestation the hash changes and the new build must be whitelisted before registration.

---

## To decide

- **Should the keeper poll it every cycle, or only on failure?** Every cycle costs one direct
  action and makes `race_lost` cheaper to distinguish from a genuine fault; only-on-failure is
  quieter but blind to a machine degrading while still signing.
- **Rate limiting.** It is cheap but not free, and it is reachable by anyone holding
  `DIRECT_API_KEY`. A per-machine floor may be worth having before the first operator scripts a
  one-second poll.
- **Whether to include `lastPrice`.** It is already obtainable via `GET_FEED`, and duplicating it
  invites two sources of truth for the same number.
