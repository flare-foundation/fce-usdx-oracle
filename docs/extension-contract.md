# Extension Container Contract

**This document is normative.** It specifies what the USDX oracle extension serves and calls
inside its container: the HTTP surface tee-node drives, the wire formats on it, how commands are
dispatched, and what the image must carry. The Go implementation in this repository
(`internal/extension`, built by the root `Dockerfile`) is the only implementation. The golden
fixtures in `deploy/testdata/conformance/` encode §2, §4 and §5, and `TestConformance` replays
them in process (§8).

The validation rules of the provisioning payloads are specified with their flows in
[usdx-oracle-codebase.md](usdx-oracle-codebase.md) §6, and the fields of the readiness report in
[usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md).

---

## 1. Process topology

One process, one static binary. `cmd/extension-tee/main.go` starts tee-node in extension mode
with `teeServer.StartServerExtension(configPort, signPort, extensionPort)` in a goroutine, then
the extension's HTTP server and its observation loop (`pkg/server.StartExtension`). tee-node is
linked as a Go module, not run beside the extension, so the two share the process and both
servers bind loopback. The image is that binary on distroless (`Dockerfile`).

Start-up:

- `EXTENSION_ID` and `CHAIN_ID` are read first. Either one missing or unparseable stops the
  process (§6).
- The extension learns its own TEE identity by having tee-node sign a fixed probe message and
  recovering the address, because tee-node offers no call that names it. It retries every second
  for up to a minute (`pkg/server`: `teeIDRetry`, `teeIDTimeout`) and then exits: a machine
  without its identity can verify no provisioning envelope.
- The extension starts unprovisioned. The observation loop observes at once and then every
  2 minutes. Every attempt fails with `NotConfigured` until `SET_ENDPOINTS` has applied and every
  credential its provider URLs name has arrived through `SET_SECRET`. The first observation is
  the first tick after that, and `GET_FEED` signs it only once `SET_ADMINS` has applied too.

If the extension's server fails, the process exits.

---

## 2. HTTP surface the extension serves

`127.0.0.1:$EXTENSION_PORT` (default 7702), loopback only. **This is a security control.**
tee-node is the one legitimate caller: it shares the process and posts to
`http://localhost:$EXTENSION_PORT/action`. The instruction router treats arrival on the
instruction channel as authorisation (§5), so a listener reachable from the network would let any
peer install its own endpoints and admin sets. The port is not `EXPOSE`d (§6).

### `POST /action`

The body is an `Action` (§4.1). The response is an `ActionResult` (§4.4) with HTTP 200 whenever
the action reached a handler, **including when the handler refuses**: a refusal is
`ActionResult.status` 0 (§4.6), not an HTTP error. Every other outcome is a non-200 with a
plain-text body.

| Condition | HTTP | Body |
| --- | --- | --- |
| Routed to a handler, success or refusal | 200 | `ActionResult` JSON |
| A command that is valid only on the other channel (§5) | 200 | `ActionResult` with `status` 0 and `log` `error: command arrived over the wrong action type` |
| The body does not decode as an `Action`: not JSON, or a field of the wrong type. A `data.message` that is not valid `0x`-prefixed hex fails here | 400 | `decoding action: …` |
| `data.type` is neither `instruction` nor `direct` | 400 | `unknown action type "…"` |
| `data.message` does not decode as a `DataFixed` (instruction), or exceeds 100 KiB | 400 | `decoding fixed data: …` |
| `data.message` does not decode as a `DirectInstruction` (direct), or exceeds 10 MiB | 400 | `decoding direct instruction: …` |
| `opType` is not `TEE_ORACLE` | 501 | `unsupported op type: received <hex>, expected <hex> (TEE_ORACLE)` |
| An instruction whose `data.id` differs from its `DataFixed.instructionId` | 400 | `action id does not match the instruction id` |
| `opCommand` is none of the five commands | 501 | `unsupported op command: received <hex>, expected one of […]`, naming every command this build serves |
| The result could not be encoded (a programming error) | 500 | `encoding the action result` |

The checks run in the order of the table: the envelope, the op type, the action id (instructions
only), then the command. The size limits are tee-node's (`processorutils.Parse`), which the
extension decodes with.

The non-200 bodies are diagnostics rather than fixed strings: the status code is contractual, and
the fixtures assert only the `unsupported op type` and `unsupported op command` prefixes. They are
also **public**. tee-node turns any non-200 into a status-0 result whose `log` reads
`extension error: unexpected status code: <code>, response: <body>`, and results are
world-readable. So they describe only the caller's own input and this build's constants.

### `GET /state`

No request body. Returns a `StateResponse` (§4.5) with status 200: the **empty state**, the zero
`stateVersion` and no bytes. tee-node reads the route on every TEE info request and folds what it
carries into the attested TEE info the proxy republishes. The extension is registered without a
state verifier, so the registry accepts the empty state as it is, but it refuses the all-ones
`StateUnavailableVersion` a tee-node reports when it cannot reach the route: the route must
answer. The extension carries nothing here on purpose: its account of itself is the signed
`GET_READINESS` report
([usdx-oracle-readiness-query.md](usdx-oracle-readiness-query.md)), which the machine signs and
binds to the request that asked for it.

### Routing rules

| Request | HTTP status |
| --- | --- |
| `GET /action` | 405 |
| `POST /state` | 405 |
| Any other path | 404 |

---

## 3. HTTP surface the extension calls

tee-node's sign server, on `127.0.0.1:$SIGN_PORT` (default 7701). It is unauthenticated, which is
why tee-node binds it to loopback (`settings.SignHost`). The extension calls two of its routes
through `internal/teenode`, at `http://localhost:$SIGN_PORT`, each call bounded by 1 s
(`pkg/server`: `signTimeout`), inside the 2 s tee-node allows the whole action.

Go marshals `[]byte` as base64 in JSON, so every byte field on this API is **base64, not hex**.
tee-node decodes the request bodies with unknown fields disallowed.

### `POST /sign`

```json
{ "message": "<base64>" }
```

```json
{ "message": "<base64>", "signature": "<base64>" }
```

tee-node signs `keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(message))` with the
machine's key. It hashes and applies EIP-191 itself, so the extension passes the preimage, never a
hash. The signature is 65 bytes, `r ‖ s ‖ v`, with `v` as 0 or 1; the extension normalises `v` to
27 or 28 before publishing, because the store's OpenZeppelin ECDSA requires it. An empty message
is a 400.

The extension signs with it on every `GET_FEED` (the `SignedPayload` preimage, §4.7), on every
`GET_READINESS` (the report body) and once at start-up (the identity probe, §1).

### `POST /decrypt`

```json
{ "encryptedMessage": "<base64>" }
```

```json
{ "decryptedMessage": "<base64>" }
```

ECIES, to the machine's key. `SET_SECRET` uses it for the ciphertext inside the envelope.

tee-node's sign server also serves `/result`, `/sign/{walletID}/{keyID}`,
`/decrypt/{walletID}/{keyID}` and `/key-info/{walletID}/{keyID}`. The extension uses none of them.

---

## 4. Wire format

Every field below is derived from the Go types tee-node serialises. The Go type is given because
it decides the JSON encoding, and getting the encoding wrong is silent: the node accepts the
request and the result fails verification later.

| Go type | JSON encoding |
| --- | --- |
| `common.Hash` | `"0x"` + 64 hex chars (32 bytes, always full width) |
| `common.Address` | `"0x"` + 40 hex chars (20 bytes) |
| `hexutil.Bytes` | `"0x"` + hex, variable length; empty or nil encodes as `"0x"`, **not** `null`, **not** `""` |
| `uint8` / `uint32` / `uint64` | JSON number |
| `string` | JSON string |

**bytes32 identifiers** (`opType`, `opCommand`) are the name's ASCII, right-padded with zero bytes
to 32 bytes. `TEE_ORACLE` is the ten bytes `0x5445455f4f5241434c45` followed by 22 zero bytes:
`0x5445455f4f5241434c4500000000000000000000000000000000000000000000`. `pkg/protocol` builds every
one of them with `teeutils.ToHash`.

### 4.1 `Action`: the request body of `POST /action`

Source: `tee-node/pkg/types/actions.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `data` | `ActionData` | object | §4.2 |
| `additionalVariableMessages` | `[]hexutil.Bytes` | array of hex strings | tee-node's; ignored by the extension |
| `timestamps` | `[]uint64` | array of numbers | tee-node's; ignored |
| `additionalActionData` | `hexutil.Bytes` | hex string | ignored |
| `signatures` | `[]hexutil.Bytes` | array of hex strings | tee-node's; ignored |

### 4.2 `ActionData`: the `data` field

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `id` | `common.Hash` | 32-byte hex | the action id, echoed in the result. On an instruction it must equal `instructionId` (§2) |
| `type` | `ActionType` (string) | `"instruction"` or `"direct"` | selects the envelope in `message` and the router (§5) |
| `submissionTag` | `SubmissionTag` (string) | `"threshold"`, `"end"` or `"submit"` | echoed in the result. tee-node forwards an instruction to the extension only for `threshold` and answers `end` itself |
| `message` | `hexutil.Bytes` | hex string | **hex-encoded UTF-8 JSON** of the envelope `type` names: a `DataFixed` or a `DirectInstruction` (§4.3) |

Note the double encoding on `message`: hex-decode it, then parse the bytes as JSON. The envelope
is chosen by `type`, never guessed. Decoding one envelope as the other does not fail, because
`encoding/json` drops unknown fields: it yields an empty payload.

### 4.3 The two envelopes

**`DataFixed`**, on the instruction channel. Source:
`go-flare-common/pkg/tee/instruction/instruction.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `instructionId` | `common.Hash` | 32-byte hex | must equal `ActionData.id` |
| `teeId` | `common.Address` | 20-byte hex | |
| `timestamp` | `uint64` | number | block timestamp of the instruction event. `GET_FEED` signs it as the update's `observedAt` (the store's definition of the field) and checks the reading's age against it |
| `rewardEpochId` | `uint32` | number | |
| `opType` | `common.Hash` | 32-byte hex | bytes32 of the op-type name |
| `opCommand` | `common.Hash` | 32-byte hex | bytes32 of the command name |
| `cosigners` | `[]common.Address` | array of 20-byte hex | |
| `cosignersThreshold` | `uint64` | number | |
| `originalMessage` | `hexutil.Bytes` | hex string | **the command's payload**: the bytes the sender passed to `sendInstructions` (§4.7) |
| `additionalFixedMessage` | `hexutil.Bytes` | hex string | |

**`DirectInstruction`**, on the direct channel. Source: `tee-node/pkg/types/direct.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `opType` | `common.Hash` | 32-byte hex | |
| `opCommand` | `common.Hash` | 32-byte hex | |
| `message` | `hexutil.Bytes` | hex string | **the command's payload** (§4.7) |

A direct action has no instruction id and no block timestamp; `ActionData.id` is the proxy's
action id.

### 4.4 `ActionResult`: the response body of `POST /action`

Source: `tee-node/pkg/types/actions.go`.

| Field | Go type | JSON | Notes |
| --- | --- | --- | --- |
| `id` | `common.Hash` | 32-byte hex | `action.data.id` |
| `submissionTag` | `SubmissionTag` (string) | string | `action.data.submissionTag` |
| `status` | `uint8` | number | §4.6 |
| `log` | `string` | string | §4.6 |
| `opType` | `common.Hash` | 32-byte hex | `TEE_ORACLE`, the received value |
| `opCommand` | `common.Hash` | 32-byte hex | the received command |
| `additionalResultStatus` | `hexutil.Bytes` | hex string | always `"0x"` |
| `version` | **`string`** | **plain string** | `"0.1.0"` (`internal/config.Version`); see below |
| `data` | `hexutil.Bytes` | hex string | the command's result (§4.7); `"0x"` when there is none |

> **Every field is always present.** The Go struct carries no `omitempty` tags, so `data` and
> `additionalResultStatus` marshal as `"0x"` rather than being omitted, and `log` is always a
> string.

**`data` is ABI-encoded bytes, and must be byte-exact.** tee-node signs
`ActionResult.Hash() = keccak256(keccak256(data) ‖ id ‖ keccak256(submissionTag) ‖ status)`, so
`data` must be exactly the bytes §4.7 defines. It is never JSON. The fixtures compare it as hex.

> **`version` is a plain string, not bytes32.** The Go declaration is `Version string`. Send
> `"0.1.0"`, not `"0x302e312e30000…"`. `StateResponse.stateVersion` *is* bytes32 (§4.5), so the
> two are asymmetric; fixture 01 pins the plain string.

### 4.5 `StateResponse`: the response body of `GET /state`

Source: `pkg/types.StateResponse`.

| Field | Type | JSON | Notes |
| --- | --- | --- | --- |
| `stateVersion` | `common.Hash` | **32-byte hex** | the zero hash: there are no bytes, so no schema to name |
| `state` | `hexutil.Bytes` | **hex string** | `"0x"`: this extension carries no state. Bytes, not an object: tee-node decodes the body as its own `TeeState`, takes `state` and `stateVersion`, and carries them opaquely into the attested TEE info, which the proxy republishes as `teeInfo.state.state` |

### 4.6 `status` and `log`

| `status` | Written by | Meaning | `log` |
| --- | --- | --- | --- |
| `0` | the extension | the handler refused | `"error: <reason>"`, where the reason is a fixed text from the closed vocabulary in `internal/failure` |
| `1` | the extension | the handler succeeded | `"ok"` |
| `0` | tee-node | the extension answered non-200 (§2), or tee-node's own checks failed before it forwarded the action | tee-node's error text |
| `3` | tee-node | the extension did not answer within tee-node's 2 s per action (`processorutils.DeadlineExceeded`) | the client's timeout error, which contains `context deadline exceeded` |

The extension writes no other status. `data` is `"0x"` on every result except a status-1 answer
to `GET_FEED` or `GET_READINESS`.

### 4.7 Command payloads and results

| Command | Channel | Payload (`originalMessage` or `message`) | `data` on status 1 |
| --- | --- | --- | --- |
| `GET_FEED` | instruction | `abi.encode(FeedUpdateRequest{feedId})`: one 32-byte word, the 21-byte feed id left-aligned | `abi.encode(FeedUpdate, Signature)`: ten static words (`pkg/wire/feedupdate`) |
| `SET_ENDPOINTS` | instruction | `abi.encode(Endpoints{version, feedId, groups})` (`pkg/wire/configpayload`) | empty |
| `SET_ADMINS` | instruction | `abi.encode(Admins{version, feedId, roles})` (`pkg/wire/configpayload`) | empty |
| `GET_READINESS` | direct | exactly one byte, `0x01` (`protocol.ReadinessRequestV1`) | `abi.encode(bytes body, bytes signature, bytes payload)` (`pkg/wire/readiness`) |
| `SET_SECRET` | direct | the provisioning envelope, `abi.encode(body, signatures, payload)` (`pkg/wire/envelope`) | empty |

**`GET_FEED` data.** The ten words are `FeedUpdate{extensionId uint256, feedId bytes21, value
int32, decimals int8, observedAt uint64, endpointsHash bytes32, adminsHash bytes32}` followed by
`Signature{v uint8, r bytes32, s bytes32}`, every field static, so the encoding is canonical. The
signature is tee-node's over the `SignedPayload` preimage
`abi.encode(bytes32("TEE_ORACLE_FEED"), chainId, keccak256(abi.encode(FeedUpdate)))`, which
`TeeOracleFeedStore` verifies. `observedAt` is `DataFixed.timestamp`, so every machine answering
one request signs the same value.

**`GET_READINESS` data.** `body` is
`abi.encode(keccak256("USDX_ORACLE_READINESS_V1"), actionId, timestamp, keccak256(payload))`, four
static words; `signature` is tee-node's over `body`; `payload` is the JSON `ReadinessReport`
(`pkg/types`), carried verbatim so a reader hashes exactly what was signed. The report carries no
teeId: the signer is recovered from the signature.

**`SET_ENDPOINTS` and `SET_ADMINS`** are hashed as received, never re-encoded: the hash is what
every later feed update commits to, and what the store compares with the sender's
`latestEndpointsHash(feedId)` and `latestAdminsHash(feedId)`. The three provisioning commands
reject every bad payload with one reason, `provisioning envelope rejected`, whatever the cause.

**A payload may not be empty.** The instructions facet rejects an empty message with
`MessageEmpty()`, so a command that needs no input still has to send something (findings §22).

**The `GET_FEED` request names the feed**, because one instructions sender serves every feed of
its extension. `TeeOracleInstructionsSender` assembles it upstream, and
`feedupdate.DecodeFeedRequest` decodes it. The extension requires **exactly** that one word, for
the feed it serves (`protocol.FeedID`), and refuses anything else without echoing it:

| Payload | Result |
| --- | --- |
| the 32-byte `FeedUpdateRequest` for this feed | status 1 and the signed update, or a status-0 refusal from the observation (no recent observation, clock checks, no admin set) |
| `0x` (empty) | status 0, `error: instruction carries an unexpected payload` |
| a request for another feed | status 0, `error: instruction carries an unexpected payload` |
| anything else, including a longer or shorter buffer | status 0, `error: instruction carries an unexpected payload` |

Accepting and ignoring is not equivalent: a caller must not come to depend on bytes the extension
drops, and a request for another feed must not be mistakable for this one. Fixture 01 pins the
accepted request and fixture 03 a payload that is not a request. The empty, other-feed, short and
long cases are pinned by `TestProcessGetFeed_RequiresTheRequestEnvelope` in
`internal/extension/extension_test.go`.

---

## 5. Handler dispatch

One op type, `TEE_ORACLE`, carries all five commands, so the channel a command arrives on is a
security boundary of its own. `internal/extension/router.go` has two routers: `processInstruction`
for a `DataFixed` and `processDirect` for a `DirectInstruction`. Each is an **exact-match switch**
on `opCommand`. A command is served by the router of its channel, named and refused with
`WrongActionType` by the other, and anything else is a 501. There is no wildcard, no fallback on
an empty command, and no default handler, and the two routers share no arm.

| Command | `instruction` (on-chain, through `TeeOracleInstructionsSender`) | `direct` (the proxy's direct channel, behind its API key) | Why |
| --- | --- | --- | --- |
| `GET_FEED` | served | `WrongActionType` | over direct, anyone holding the proxy key would get the same signed update free and unrecorded |
| `SET_ENDPOINTS` | served | `WrongActionType` | over direct it would skip the sender's `onlyGovernance` check |
| `SET_ADMINS` | served | `WrongActionType` | as `SET_ENDPOINTS` |
| `GET_READINESS` | `WrongActionType` | served | the sender has no such command: a status answer is stale within minutes and not worth a fee |
| `SET_SECRET` | `WrongActionType` | served | on-chain, the ciphertext would be permanent and public |
| anything else | 501 | 501 | |

Arrival on the instruction channel is the authorisation for its commands: the sender's
governance check has run and the data providers have cosigned the instruction. Nothing on the
direct channel is authorised by arrival: `SET_SECRET` carries its own M-of-N admin signatures,
and `GET_READINESS` grants nothing. An extension built without a provisioner (the conformance
harness, §8) answers `SET_ENDPOINTS`, `SET_ADMINS`, `SET_SECRET` and `GET_READINESS` with
`NotConfigured`.

**Concurrency.** Nothing serialises the handlers. `net/http` serves each request on its own
goroutine, so `GET_FEED`, `GET_READINESS` and the provisioning commands can run at the same time,
and nothing orders one action against another. Each component locks its own state:

- The provisioning store (`internal/provisioning/store`) holds one mutex across every whole
  `Apply*` call, so `SET_ENDPOINTS`, `SET_ADMINS` and `SET_SECRET` apply one at a time, replay
  check included. A read-write lock guards the snapshot that the observation loop opens and the
  readiness report reads.
- The poller (`internal/observation/poller`) holds a read-write lock over its cached
  observation. `GET_FEED` and `GET_READINESS` copy what they need under the read lock and sign
  outside it.

A `GET_FEED` that races a `SET_ENDPOINTS` signs the binding its cached observation was read
under: the commitments are fixed when the inputs are read, not when the request arrives.
`GET /state` touches no state.

---

## 6. Container requirements

### Environment variables

| Variable | Read by | Meaning |
| --- | --- | --- |
| `CHAIN_ID` | tee-node and the extension | **Mandatory, no default.** The chain every signature is bound to, provisioning envelopes and feed updates alike; must match the proxy and the chain. Decimal or `0x`-hex. Without it the process stops |
| `EXTENSION_ID` | tee-node and the extension | **Mandatory, no default.** The extension every update names. Decimal or `0x`-hex; the extension normalises it to the 32-byte hex word tee-node expects. Without it, or with a value it cannot parse, the process stops |
| `SIGN_PORT` | tee-node and the extension | tee-node's sign server (§3); default 7701 |
| `EXTENSION_PORT` | tee-node and the extension | the extension's `/action` and `/state` (§2); default 7702 |
| `PROXY_URL` | tee-node | the proxy's internal endpoint the node polls for actions and posts results to |
| `INITIAL_OWNER` | tee-node | the machine's initial owner |
| `GOVERNANCE_SIGNERS`, `GOVERNANCE_THRESHOLD` | tee-node | the TEE governance the node signs with; set together |
| `GOVERNANCE_SAFE`, `GOVERNANCE_TEE_MANAGER` | tee-node | Safe-backed governance; set together |
| `LOG_LEVEL` | tee-node | log level |
| `MODE` | tee-node | `0` production attestation (the image default), `1` simulated (the compose stack sets it). Not in the launch-policy label, so not overridable at launch |
| `CHAIN_URL` | nothing | in the launch-policy label, but no code in the image reads it |

The extension reads `CHAIN_ID`, `EXTENSION_ID`, `SIGN_PORT` and `EXTENSION_PORT`, and reads the
first two from the same variables as tee-node, so the two cannot disagree on the chain or the
extension. The port pair defaults in code to the image's values, so the image sets neither.

### Ports

```dockerfile
EXPOSE 5500
```

Only tee-node's configuration port is declared. Confidential Space opens every `EXPOSE`d port in
the VM firewall (subject to the operator's VPC rules), so the list is a security boundary, not
documentation. The sign server (`SIGN_PORT`) and the extension's `/action` and `/state`
(`EXTENSION_PORT`) bind `127.0.0.1`: tee-node reaches both over loopback inside the process, and
the instruction channel is authorised by arrival, so neither may be reachable from the network.

### Launch policy label

```dockerfile
LABEL "tee.launch_policy.allow_env_override"="LOG_LEVEL,PROXY_URL,INITIAL_OWNER,EXTENSION_ID,CHAIN_URL,CHAIN_ID,GOVERNANCE_SIGNERS,GOVERNANCE_THRESHOLD,GOVERNANCE_SAFE,GOVERNANCE_TEE_MANAGER,SIGN_PORT,EXTENSION_PORT"
```

Without this label, a Confidential Space VM **rejects operator env overrides at attestation
time**, and whatever the image bakes in is final. The label is part of the image config and so of
the code hash: a variable missing from it cannot be set at launch, and the failure appears at
attestation rather than at build.

### User

```dockerfile
USER 0:0
```

The Confidential Space launcher exposes its attestation-token socket owned by root with no group
or other access, and the workload cannot change that. The attested VM, not the container, is the
trust boundary.

---

## 7. Reproducibility

The image's config digest is the code hash registered on-chain, so build determinism is a
security property. The `Dockerfile`:

- pins both base images by digest;
- takes `SOURCE_DATE_EPOCH` as a build argument and propagates it;
- installs apt packages from `snapshot.debian.org` at the instant `SOURCE_DATE_EPOCH` names;
- downloads the Go modules `go.mod` pins and verifies them against `go.sum` (`go mod verify`);
- builds with `-trimpath`, an empty build id, no symbol tables and no VCS stamp, statically
  linked;
- sets file ownership and mode on every `COPY`, and normalises every mtime as the final build
  step: `RUN find /app -exec touch -h -d @${SOURCE_DATE_EPOCH} {} +`.

[REPRODUCIBILITY.md](../REPRODUCIBILITY.md) has the build and verification procedure.

**tee-node version pinning.** The extension links tee-node as a Go module, so `go.mod` is the
pin. `Dockerfile.siblings.dev` replaces it with an on-disk checkout for development
(`USE_LOCAL_SIBLINGS=1`); that image is not the attested build.

`deploy/scripts/check-versions.sh` fails when the tee-node or go-flare-common pins of the two Go
modules differ, when any other module both `go.mod` files name is at two versions, or when the
tee-proxy pin in `tools/go.mod` differs from the tag of tee-proxy's published image in
`deploy/docker-compose.yaml`.

---

## 8. Conformance, and other implementations

`TestConformance` (`internal/extension/conformance_test.go`) is the conformance replay:

```bash
GOWORK=off go test ./internal/extension -run TestConformance
```

It is an **in-process Go test**. It builds the extension with `NewExtension`, a fixed observer and
no provisioner: no tee-node, no proxy, no chain and no listening socket. It replays the 13
fixtures in the order `deploy/testdata/conformance/index.json` gives, through
`Server.Handler.ServeHTTP`. Every fixture asserts the HTTP status; a fixture may also assert the
exact JSON body, a subset of its fields, a substring of a plain-text body, or the prefix of `log`.

| Fixtures | What they pin |
| --- | --- |
| 01, 02 | `GET_FEED` success: the whole `ActionResult`, the exact `data`, and that a repeat returns the same payload |
| 03 | `GET_FEED` with a payload that is not a `FeedUpdateRequest`: status 0, no data, an `error: ` log |
| 04, 05 | 501 for an unknown op type and for an unknown command |
| 06, 07, 08 | 400 for a body that is not JSON, a message that is not hex, and a message that is not a `DataFixed` |
| 09, 10, 11 | 405 for `GET /action` and `POST /state`, 404 for any other path |
| 12 | the whole body of `GET /state` |
| 13 | `GET_FEED` over the direct channel, in a real `DirectInstruction` envelope: refused with `WrongActionType` |

The fixtures do not cover `GET_READINESS`, the provisioning commands, the action-id check, an
unknown action type, the other `GET_FEED` payload cases (§4.7), or the clock and staleness
refusals. The Go unit tests beside them do: `extension_test.go`, `provisioning_test.go` and
`readiness_test.go` in `internal/extension`, and the tests in `internal/observation/poller`.

**What this means for an implementation in another language.** There is none, and the replay
cannot be pointed at one: it is a test of this Go package, not a client of a running container,
and nothing in the repository sends the fixtures over HTTP. Fixtures 01 and 02 also pin a payload
built from the harness's stub observation (value 1.00000000 at 8 decimals, `observedAt`
1700000000, `endpointsHash` `0x…9f3c`, `adminsHash` `0x…5e21`), a constant signature (bytes 1 to
64, `v` 27) and extension id 1 from the test environment, so another implementation can match them
only by injecting the same stub. A port would need its own harness that replays the fixtures
against it with that stub; its own image build, since nothing in `deploy/` builds anything but the
root `Dockerfile`; and tee-node as a separate process beside it (tee-node's `cmd/extension`),
which this repository neither builds nor tests. It would have to satisfy §2 to §6 exactly.
