# Extension Container Contract

**This document is normative.** Any container that satisfies it is a valid FCC extension, regardless of implementation language, and `deploy/scripts/test-conformance.sh` enforces it mechanically against the golden fixtures in `deploy/testdata/conformance/`.

If you are adding a new language, this is the specification you are implementing. Nothing else in the repo needs to change.

> **Scope note for this repository.** The upstream scaffold ships Go, Python and
> TypeScript implementations and this document is written for all three. **Only Go is
> checked in here** (`go/`), so passages describing `python/` or `typescript/` files
> describe the scaffold, not this repo. The scaffold's `SAY_HELLO` / `SAY_GOODBYE`
> examples are likewise illustrative — this extension's operations all sit under
> one op type, `TEE_ORACLE/{GET_FEED, GET_READINESS, SET_ENDPOINTS, SET_ADMINS,
> SET_SECRET}`. Of those, only `GET_FEED`,
> the `/state` shape and the routing rules are pinned by conformance fixtures;
> the provisioning commands and `GET_READINESS` are specified in
> [usdx-oracle-codebase.md](usdx-oracle-codebase.md) §6 and
> [usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md).

---

## 1. Process topology

Two shapes are equally valid. The contract is what is observable from *outside* the container; how you get there is your choice.

**Single-process** — the extension embeds tee-node as a library and runs both in one binary. Only available to Go. This is what `Dockerfile` builds: `cmd/docker/main.go` calls `teeServer.StartServerExtension(configPort, signPort, extensionPort)` in a goroutine alongside the extension's own HTTP server. Produces the smallest, most reproducible image (distroless, static, single binary).

**Two-process** — a prebuilt tee-node `./cmd/extension` binary runs beside the extension runtime, joined so that the container dies if either child dies. This is what every non-Go language uses:

```dockerfile
CMD ["/bin/bash", "-c", "/app/server & SERVER_PID=$!; <your-runtime> & EXT_PID=$!; wait -n $SERVER_PID $EXT_PID; kill -TERM $SERVER_PID $EXT_PID 2>/dev/null; exit 1"]
```

Use `/bin/bash`, not `/bin/sh`. Debian's `/bin/sh` is dash, which does not implement `wait -n`, and without it the container will not exit when a child dies.

Neither shape is privileged. The conformance suite tests the contract, not the topology.

---

## 2. HTTP surface the extension MUST serve

On `$EXTENSION_PORT`, bound on all interfaces inside the container.

### `POST /action`

Request body is an `Action` (§4.1). Response is an `ActionResult` (§4.3) as JSON with status 200 whenever the action was routed to a handler — **including when the handler fails**. Handler failure is signalled by `ActionResult.status`, not by the HTTP status.

| Condition | HTTP status | Body |
| --- | --- | --- |
| Routed to a handler (success or handler error) | 200 | `ActionResult` JSON |
| Body is not valid JSON | 400 | error text or `{"error": ...}` |
| `data.message` is not valid hex | 400 | error text |
| `data.message` does not decode to a valid `DataFixed` | 400 | error text |
| No handler registered for `(opType, opCommand)` | 501 | plain text containing `unsupported op type` |

The 501 body is a human-readable diagnostic, not a fixed string — the status
code is what is contractual. The Go implementation additionally names the
received and expected identifiers, which is worth imitating.

### `GET /state`

No request body. Returns a `StateResponse` (§4.5) with status 200. tee-node reads it on every
TEE info request and nothing else does: the port is loopback-only, and every consumer reads the
state out of the proxy's `/info`. A tee-node that cannot reach the route reports its all-ones
`StateUnavailableVersion` there instead of failing the info response.

### Routing rules

| Request | HTTP status |
| --- | --- |
| `GET /action` | 405 |
| `POST /state` | 405 |
| Any other path | 404 |

---

## 3. HTTP surface the extension MAY call

tee-node exposes a signing/crypto API on `http://localhost:$SIGN_PORT`. The extension calls it; it is never exposed outside the container.

### `POST /decrypt`

Decrypts a payload that was encrypted to the TEE's public key. Because tee-node is Go and Go marshals `[]byte` as base64 in JSON, **the wire encoding here is base64, not hex** — this is the single most common porting mistake.

Request:

```json
{ "encryptedMessage": "<base64>" }
```

Response:

```json
{ "decryptedMessage": "<base64>" }
```

The scaffold wraps this in each language's `base/node.*` module so extension authors never hand-roll it.

---

## 4. Wire format

Every field below is derived from the Go types that tee-node actually serializes. The Go type is given because it determines the JSON encoding, and getting the encoding wrong is silent — the node accepts the request and the result fails verification later.

Encoding rules for the Go types involved:

| Go type | JSON encoding |
| --- | --- |
| `common.Hash` | `"0x"` + 64 lowercase hex chars (32 bytes, always full width) |
| `common.Address` | `"0x"` + 40 hex chars (20 bytes) |
| `hexutil.Bytes` | `"0x"` + hex, variable length; empty/nil encodes as `"0x"` — **not** `null`, **not** `""` |
| `uint8` / `uint32` / `uint64` | JSON number |
| `string` | JSON string |

**bytes32 identifiers** (`opType`, `opCommand`) are UTF-8 strings right-padded with zero bytes to 32 bytes, then hex-encoded. `"GREETING"` becomes `0x4752454554494e4700000000000000000000000000000000000000000000000000`-style padding — 8 content bytes followed by 24 zero bytes. The empty string is 32 zero bytes, and it is meaningful: see §5.

### 4.1 `Action` — request body of `POST /action`

Source: `tee-node/pkg/types/actions.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `data` | `ActionData` | object | §4.2 |
| `additionalVariableMessages` | `[]hexutil.Bytes` | array of hex strings | |
| `timestamps` | `[]uint64` | array of numbers | |
| `additionalActionData` | `hexutil.Bytes` | hex string | |
| `signatures` | `[]hexutil.Bytes` | array of hex strings | |

### 4.2 `ActionData` — the `data` field

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `id` | `common.Hash` | 32-byte hex | echoed back in the result |
| `type` | `ActionType` (string) | `"instruction"` or `"direct"` | |
| `submissionTag` | `SubmissionTag` (string) | `"threshold"`, `"end"` or `"submit"` | echoed back in the result |
| `message` | `hexutil.Bytes` | hex string | **hex-encoded UTF-8 JSON** that decodes to a `DataFixed` (§4.3) |

Note the double encoding on `message`: hex-decode it, then parse the resulting bytes as JSON.

### 4.3 `DataFixed` — decoded from `ActionData.message`

Source: `go-flare-common/pkg/tee/instruction/instruction.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `instructionId` | `common.Hash` | 32-byte hex | |
| `teeId` | `common.Address` | 20-byte hex | |
| `timestamp` | `uint64` | number | block timestamp of the instruction event. `GET_FEED` signs it as the update's `observedAt` (the store's definition of the field) and checks the reading's age against it |
| `rewardEpochId` | `uint32` | number | |
| `opType` | `common.Hash` | 32-byte hex | bytes32 of the op-type string |
| `opCommand` | `common.Hash` | 32-byte hex | bytes32 of the op-command string |
| `cosigners` | `[]common.Address` | array of 20-byte hex | |
| `cosignersThreshold` | `uint64` | number | |
| `originalMessage` | `hexutil.Bytes` | hex string | **your payload** — the bytes your contract passed to `sendInstructions` |
| `additionalFixedMessage` | `hexutil.Bytes` | hex string | |

`originalMessage` is what a handler receives. Its interpretation is entirely up to the extension — the scaffold's `SAY_HELLO` treats it as UTF-8 JSON, `SAY_GOODBYE` treats it as ABI-encoded `(string,string)`.

**It may not be empty.** The instructions facet rejects an empty `message`
with `MessageEmpty()`, so an operation that needs no input from the
caller still has to send *something*. Found on Coston2, 5 Aug 2026, when
the first request path — which passed `message: ""` — reverted before any
machine saw it (findings §22).

TEE_ORACLE/GET_FEED therefore carries an **ABI-encoded `FeedUpdateRequest`
envelope**: the 21-byte feed id in a single 32-byte word
(`abi.encode(FeedUpdateRequest{feedId})`, the field being static so it encodes
inline). The envelope names the feed to observe, because one instructions sender
serves every feed of its extension. The envelope is assembled by
`TeeOracleInstructionsSender` upstream and decoded by `report.DecodeFeedRequest`
in the extension. Every implementation must require **exactly** that word — for
the feed it serves — and fail the action otherwise:

| `originalMessage` | Expected result |
| --- | --- |
| the 32-byte `FeedUpdateRequest` for this feed | status 1, signed feed update |
| `0x` (empty) | status 0, `instruction carries an unexpected payload` |
| a request for a different feed | status 0, `instruction carries an unexpected payload` |
| anything else | status 0, `instruction carries an unexpected payload` |

Accepting-and-ignoring is not equivalent: a caller must not be able to come to
depend on bytes the extension silently drops, and a request for another feed must
not be mistakable for this one. Conformance fixtures 01 and 03 pin both halves.

### 4.4 `ActionResult` — response body of `POST /action`

Source: `tee-node/pkg/types/actions.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `id` | `common.Hash` | 32-byte hex | echo `action.data.id` |
| `submissionTag` | `SubmissionTag` (string) | string | echo `action.data.submissionTag` |
| `status` | `uint8` | number | §4.6 |
| `log` | `string` | string | §4.6 |
| `opType` | `common.Hash` | 32-byte hex | echo from `DataFixed` |
| `opCommand` | `common.Hash` | 32-byte hex | echo from `DataFixed` |
| `additionalResultStatus` | `hexutil.Bytes` | hex string | `"0x"` when unused |
| `version` | **`string`** | **plain string** | see the warning below |
| `data` | `hexutil.Bytes` | hex string | your response payload; `"0x"` when there is none |

> **Every field is always present.** The Go struct carries no `omitempty` tags,
> so `data` and `additionalResultStatus` marshal as `"0x"` rather than being
> omitted, and `log` is always a string. An implementation that drops empty
> fields produces a different JSON shape from the reference Go image.

**`data` must be byte-exact across implementations.** `ActionResult.Hash()`
computes `keccak256(data)` and that hash is signed, so serialization details
matter: emit compact JSON with no whitespace, preserving field declaration
order. Go's `encoding/json`, Python dicts and TypeScript object literals all do
this naturally; the conformance fixtures compare the resulting hex exactly.

> **`version` is a plain string, not bytes32.**
>
> The Go declaration is `Version string` (`tee-node/pkg/types/actions.go:57`). Send `"0.1.0"`, not `"0x302e312e30000..."`.
>
> This is easy to get wrong because `StateResponse.stateVersion` *is* bytes32 (§4.5) — the two are genuinely asymmetric. The `sign` repo's Python and TypeScript ports both hex-encode `ActionResult.version` and are wrong; do not copy them. `deploy/testdata/conformance/` pins the correct encoding.

### 4.5 `StateResponse` — response body of `GET /state`

| Field | Type | JSON | Notes |
| --- | --- | --- | --- |
| `stateVersion` | `common.Hash` | **32-byte hex** | bytes32 of the version string — asymmetric with `ActionResult.version` by design |
| `state` | `hexutil.Bytes` | **hex string** | the JSON encoding of the extension's `State`. Bytes, not an object: tee-node decodes this body as its own `TeeState`, takes `state` and `stateVersion`, and carries them opaquely into the attested TEE info, which the proxy republishes as `teeInfo.state.state`. `stateVersion` names the schema of those bytes |

### 4.6 `status` and `log`

| `status` | Meaning | Required `log` |
| --- | --- | --- |
| `0` | Handler failed | `"error: <message>"` |
| `1` | Handler succeeded | `"ok"` |
| anything else | In progress | `"pending"` |

`data` is only meaningful for `status == 1`.

---

## 5. Handler dispatch

An extension registers handlers against `(opType, opCommand)` pairs, both compared as bytes32.

**Lookup order:** exact `(opType, opCommand)` match first; then `(opType, <empty bytes32>)` as a wildcard. Registering a handler with an empty `opCommand` makes it the default for every command under that op-type. No match at all is a 501.

**Concurrency:** handler invocations are serialized — at most one runs at a time. `GET /state` is serialized against handlers too, so a state read never observes a half-applied mutation. Implementations may use a mutex (Go, Python) or a promise chain (TypeScript); what matters is the observable guarantee.

---

## 6. Container requirements

### Environment variables consumed

| Variable | Meaning |
| --- | --- |
| `MODE` | `0` = production attestation (the image default), `1` = simulated attestation (the local compose stack sets it); not launch-overridable |
| `SIGN_PORT` | tee-node signing/crypto endpoint the extension calls (code default `7701`; env only overrides) |
| `EXTENSION_PORT` | port the extension serves `/action` and `/state` on, loopback only (default `7702`) |
| `PROXY_URL` | extension proxy the node polls for actions |
| `CHAIN_ID` | chain the node binds signatures to; must match the proxy and the chain |
| `LOG_LEVEL` | node log level |
| `INITIAL_OWNER` | extension owner address |
| `GOVERNANCE_SIGNERS` | comma-separated governance signer addresses |
| `GOVERNANCE_THRESHOLD` | governance threshold |

An extension implementation itself only needs to read `EXTENSION_PORT` and `SIGN_PORT`; the rest are consumed by tee-node. The port pair defaults in code to the image's values, so the image sets neither; all of them must still be *settable* on the container (the launch-policy label lists them).

### Ports

```dockerfile
EXPOSE 5500
```

Only tee-node's configuration port is declared. Confidential Space opens every `EXPOSE`d port in
the VM firewall (subject to the operator's VPC rules), so the list is a security boundary, not
documentation. The sign server (`SIGN_PORT`) and the extension's `/action` and `/state`
(`EXTENSION_PORT`) bind `127.0.0.1`: tee-node reaches both over localhost inside the process, and
the instruction channel is authorised by arrival, so neither may be reachable from the network.

### Launch policy label — required

```dockerfile
LABEL "tee.launch_policy.allow_env_override"="LOG_LEVEL,PROXY_URL,INITIAL_OWNER,EXTENSION_ID,CHAIN_URL,CHAIN_ID,GOVERNANCE_SIGNERS,GOVERNANCE_THRESHOLD,GOVERNANCE_SAFE,GOVERNANCE_TEE_MANAGER,SIGN_PORT,EXTENSION_PORT"
```

Without this label, a GCP Confidential Space VM **rejects operator env overrides at attestation time** and whatever was baked into the image at build time is final. Every language image must carry an identical list — a mismatch produces a deployment that cannot be reconfigured, and the failure appears at attestation rather than at build.

### User

```dockerfile
USER 0:0
```

Matches tee-node. The TEE itself is the isolation boundary, not in-container user separation.

---

## 7. Reproducibility

The container's code hash is what gets registered on-chain, so build determinism is a security property, not a nicety.

Every language image must:

- accept a `SOURCE_DATE_EPOCH` build arg and propagate it,
- pin apt to `snapshot.debian.org` keyed on `SOURCE_DATE_EPOCH`,
- install dependencies from a committed lockfile (`go.sum`, `package-lock.json`, pinned `requirements.txt`),
- normalize mtimes as the final build step: `RUN find /app -exec touch -h -d @${SOURCE_DATE_EPOCH} {} +`

Determinism is not equal across languages. Go on distroless is bit-for-bit reproducible across machines. Python wheels and `node_modules` trees embed build-host paths and npm-version-dependent layout, so those images target *same-machine* determinism only. See `REPRODUCIBILITY.md` for the honest per-language caveats.

**tee-node version pinning.** The extension links tee-node as a Go module, so `go.mod` is the single pin; the sibling build (`Dockerfile.siblings.dev`) replaces it with the on-disk checkout for development.

`deploy/scripts/check-versions.sh` fails when the tee-node or go-flare-common pins of the two Go modules, or the tee-proxy pin and the proxy image's build argument, drift apart.

---

## 8. Adding a language

Only the Go implementation exists, and the deploy scripts build one image from the
root `Dockerfile`. A port would have to satisfy §2, §4 and §6, pass the conformance
replay (`go test ./internal/extension/ -run TestConformance` replays the fixtures in
`deploy/testdata/conformance` against a running extension; no chain, no registration, no
proxy), and bring its own image build, since nothing in `deploy/` dispatches on a language.
