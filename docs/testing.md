# Testing

Tests split into two groups: those covering **your extension**, and those covering the **deployment tooling**. Cheapest first — only the last needs a chain.

## Your extension

| Layer | What it tests | How to run | Chain? |
|-------|--------------|------------|--------|
| **Unit** | Your handlers, dispatch, encoding — in your chosen language | `./scripts/test-unit.sh` | no |
| **Conformance** | The wire contract, against golden fixtures | `./scripts/test-conformance.sh` | no |
| **End-to-end** | Full instruction lifecycle (deploy → send → process → verify) | `./scripts/test.sh` | yes |

Both script-driven layers accept a language or `--all`:

```bash
./scripts/test-unit.sh python
```

```bash
./scripts/test-conformance.sh --all
```

They dispatch through `LANGUAGE_TEST_CMD` in each `<lang>/language.env`, so they work for any language you add without modification.

### Conformance testing

This is the layer that keeps multiple implementations honest, and the acceptance test for a new language.

`scripts/test-conformance.sh` starts **only the extension process** — no tee-node, no proxy, no chain, no Docker — replays the 16 fixtures in `testdata/conformance/`, and diffs every response field. The response payload is compared **byte-for-byte**, because tee-node hashes it and signs the result.

```bash
./scripts/test-conformance.sh --all
```

It runs in seconds, which is what makes writing a new language port tractable: a correctness signal without a full deploy cycle.

The fixtures are **order-dependent** and share one process — counters accumulate across cases and the final fixture asserts the resulting state. `index.json` fixes the order.

If you change a request or response shape, regenerate rather than hand-editing:

```bash
./python/.venv/bin/python testdata/conformance/gen_fixtures.py
```

Fixtures are generated from `gen_fixtures.py` so the hex encodings are derived rather than hand-assembled. Adding a case is a code edit there.

> When this suite disagrees with your implementation, check [extension-contract.md](extension-contract.md) before changing the fixture. The contract is normative; the fixtures encode it.

## The deployment tooling

`tools/` is a standalone Go module with no dependency on any language implementation — which is what lets one deployment and test path serve Go, Python and TypeScript alike. It has its own tests.

| Layer | What it tests | How to run |
|-------|--------------|------------|
| **Unit tests** | Revert decoding, state file I/O, env parsing, validation, report formatting | `cd tools && go test ./...` |
| **Integration tests** | On-chain constructor validation, revert reasons, idempotent registration, pre-flight checks | `cd tools && go test -tags integration ./integration/ -v` |

### Tooling unit tests

These require no external services. They cover:

- **Revert reason decoding** (`tools/pkg/fccutils/revert_test.go`) — Verifies `decodeRevertHex` and `DecodeRevertReason` correctly decode ABI-encoded `Error(string)` reverts, including all 7 revert messages from `InstructionSender.sol`. Also tests edge cases: nil errors, wrapped errors, custom error selectors, invalid hex, short data.
- **Support revert decoding** (`tools/pkg/support/support_test.go`) — Tests `decodeRevertFromError` which extracts revert reasons from go-ethereum JSON-RPC error types.
- **State file I/O** (`tools/pkg/fccutils/registration_test.go`) — Tests `loadState`/`saveState` for the TEE machine registration resume flow: missing files, valid/invalid JSON, overwrite behavior, roundtrip consistency, read-only directories.
- **Validation checks** (`tools/pkg/validate/checks_test.go`) — Extension env format validation, deployer key source detection, service/registration/TEE check functions with various config states.
- **Report formatting** (`tools/pkg/validate/report_test.go`) — Report summary, JSON output, colored terminal output, empty reports, unknown statuses.
- **Validation primitives** (`tools/pkg/validate/validate_test.go`) — `AddressNotZero`, `AddressHasCode`, `KeyHasFunds`, `IsUsingDevKey` with nil clients, zero addresses, edge cases.

```bash
cd tools && go test ./... -v
```

### Tooling integration tests

Integration tests run against a live Ethereum node (Hardhat, Anvil, or Coston2). They are excluded from `go test ./...` via the `integration` build tag.

**What they test:**

- **Constructor validation** — Deploys `InstructionSender` with zero addresses, EOA addresses, and valid addresses. Verifies revert messages are decoded correctly (not binary garbage).
- **setExtensionId errors** — Calls `setExtensionId` before registration ("Extension ID not found.") and after it's already set ("Extension ID already set."). Verifies the full revert decoding chain works: `DecodeRevertReason` → `SimulateAndDecodeRevert` fallback.
- **CheckTx revert reasons** — Submits transactions that revert on-chain (with manual gas limit to bypass estimation), then verifies `CheckTx` replays the call and returns human-readable revert reasons.
- **Idempotent registration** — Runs `SetupExtension` twice with the same instruction sender address. Verifies the second run detects the existing registration and returns the same extension ID without submitting duplicate transactions.
- **Pre-flight validation** — Tests `AddressHasCode` against deployed registry contracts and random EOAs. Tests `KeyHasFunds` against the funded deployer and unfunded random keys.

**Running against a local node:**

```bash
cd tools && go test -tags integration ./integration/ -v -count=1
```

Defaults: `CHAIN_URL=http://127.0.0.1:8545`, addresses file at `config/coston2/deployed-addresses.json`.

**Running against Coston2:**

```bash
cd tools && CHAIN_URL=https://coston2-api.flare.network/ext/C/rpc \
  DEPLOYMENT_PRIVATE_KEY=<your-funded-key> \
  go test -tags integration ./integration/ -v -count=1
```

**Environment variables:**

| Variable | Default | Description |
|----------|---------|-------------|
| `CHAIN_URL` | `http://127.0.0.1:8545` | RPC endpoint |
| `ADDRESSES_FILE` | `../../config/coston2/deployed-addresses.json` | Path to deployed registry addresses |
| `DEPLOYMENT_PRIVATE_KEY` | Hardhat dev key | Funded private key for deployments and transactions |

**Note:** Integration tests deploy fresh contracts on each run. On Coston2, this costs gas. On a local node, it's free.

## End-to-End Tests

After post-build completes, you can send instructions to your extension and verify the results:

```bash
./scripts/test.sh
```

Or run everything in one shot:

```bash
./scripts/full-setup.sh --test
```

> ⚠ `full-setup.sh --chain <name>` **overwrites `.env`** with `.env.<name>`. The individual
> scripts below only read it. Prefer them if your `.env` holds anything you care about.

---

# Running the USDX oracle end to end yourself

The whole chain is: **request → TEE observes → signed report → store → feed.** This section
walks it from a clean tree to a price on Coston2, with what to check at each step and what the
failures actually look like.

## 0. What must be true before you start

| | Check |
| --- | --- |
| Docker stack up | `docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml ps` — `extension-tee`, `ext-proxy`, `redis` |
| Proxy reachable **from the internet** | `curl -s $EXT_PROXY_URL/info \| jq .machineData.extensionId` returns your extension id, not an ngrok error page |
| Deployer key funded | Coston2 C2FLR on the address in `.env` |
| `config/extension.env` current | Holds the `EXTENSION_ID` / `INSTRUCTION_SENDER` you intend to use |

### Which proxy port

The tools default to **`http://localhost:6674`**, which is what Docker Compose publishes
(`6674 -> 6664`; the container itself listens on 6664). Under the default deployment they run
flag-free.

Two cases still need `-p` or `EXT_PROXY_URL`:

- **`start-services.sh --local`** — the proxy binds **6664** on the host directly, with no
  Docker mapping. Use `-p http://localhost:6664`.
- **Reaching the machine over its public URL** — pass the tunnel address.

`go run` does not read `.env`, so an `EXT_PROXY_URL` set there is not in scope for a
hand-run tool; export it in the shell instead:

```bash
export EXT_PROXY_URL=http://localhost:6674
```

A wrong port shows up as:

```
deriving TEE identity from http://localhost:6664/info: connect: connection refused
```

`post-build.sh` and `test.sh` detect the running mode and set this themselves, so they are
unaffected. Registration is the one step that genuinely needs the **public** URL, because data
providers verify the availability proof from outside.

The proxy really does need to be publicly reachable: machine registration ends with an
availability proof that data providers verify, and they cannot reach `localhost`. If your
tunnel is down, registration stalls at status 1 and nothing downstream works.

## 1. The fast checks — no chain, no Docker

Run these first. They catch most breakage in seconds.

```bash
cd go && go test ./... && cd ..
```

```bash
./scripts/test-conformance.sh go
```

```bash
forge test
```

## 2. Prove a real observation locally

This is the highest-value local test: the real observation path against **live** Flare,
Ethereum, Songbird and Hex — no chain, no TEE, no credentials.

Three terminals, or background the first two:

```bash
cd go && SIGN_PORT=9099 go run ./cmd/mocknode
```

```bash
cd go && EXTENSION_PORT=8099 SIGN_PORT=9099 go run ./cmd/devserver
```

```bash
./scripts/set-endpoints.sh testdata/endpoints/public-only.json 8099
```

Then ask for a price. `originalMessage` **must** be `0x01` — the request envelope; an empty
message is rejected (findings §22):

```bash
python3 -c "import json;b32=lambda s:'0x'+s.encode().ljust(32,b'\x00').hex();df={'instructionId':'0x'+'c1'*32,'teeId':'0x'+'22'*20,'timestamp':1785945000,'rewardEpochId':0,'opType':b32('USDX_ORACLE'),'opCommand':b32('GET_PRICE'),'cosigners':[],'cosignersThreshold':0,'originalMessage':'0x01','additionalFixedMessage':'0x'};print(json.dumps({'data':{'id':'0x'+'c1'*32,'type':'instruction','submissionTag':'submit','message':'0x'+json.dumps(df).encode().hex()},'additionalVariableMessages':[],'timestamps':[],'additionalActionData':'0x','signatures':[]}))" > /tmp/getprice.json
```

```bash
curl -sS -X POST -H 'Content-Type: application/json' --data-binary @/tmp/getprice.json http://127.0.0.1:8099/action | jq '{status, log}'
```

```bash
curl -sS http://127.0.0.1:8099/state | jq '.state | {lastPrice, lastObservedAt, sourceHealth, readiness}'
```

**What good looks like** — `status: 1`, and:

```json
{ "lastPrice": "100000000", "sourceHealth": { "hex_bonds": "ok", "hex_cash": "not_configured",
  "rpc_flare": "ok", "rpc_ethereum": "ok", "rpc_songbird": "ok" },
  "readiness": { "ready": false, "missingSecretRefs": ["hex_api_key"] } }
```

`ready: false` with only `hex_api_key` missing is **expected** until Hex issues the key. The
feed still publishes at par, because a bonds-only price that already reaches par is provably
what the full computation would give (findings §21). It will *not* publish anything below par.

## 3. Deploy and register on Coston2

Only needed the first time, or after changing a contract (steps a–b) or extension code (c–d).

**a. Contracts** — deploys a fresh sender and registers a new extension id into
`config/extension.env`:

```bash
./scripts/pre-build.sh
```

**b. Store and feed** — omit `FORCE_REDEPLOY_STORE` to reuse an existing store; setting it
deploys a fresh one and **resets the stored price**:

```bash
FORCE_REDEPLOY_STORE=1 ./scripts/extension-setup.sh
```

**c. Build and start the TEE** — a code change means a new image, and a restart means a **new
TEE identity**, because the enclave key does not survive one:

```bash
./scripts/stop-services.sh --chain coston2 && ./scripts/start-services.sh --chain coston2
```

Use `stop` then `start`, not `start` alone. `stop-services.sh` runs `docker compose down`,
which removes redis — where tee-node persists its identity and machine data. Without that, a
restart silently keeps the **old** extension id and identity even though the container env
says otherwise.

**d. Register the machine** — allow the code hash, set governance, register, wait for the
availability proof:

```bash
./scripts/post-build.sh
```

**e. If the extension id changed in (a), repoint the store** rather than redeploying it. This
is what `setExtension` exists for, and it preserves the stored price and the feed registration:

```bash
cd tools && go run ./cmd/set-store-extension -store <STORE> -extensionId <NEW_ID> -instructionSender <NEW_SENDER>
```

**Verify before going on.** The store requires status **2**; anything less is rejected:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc 0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE 'getTeeMachineStatus(address)(uint8)' <TEE_ID>
```

Your `<TEE_ID>` is derived from the attested public key — `send-endpoints` prints it, or:

```bash
curl -s $EXT_PROXY_URL/info | jq -r '"\(.teeInfo.publicKey.x)\(.teeInfo.publicKey.y)"'
```

## 4. Publish the endpoint configuration

Costs gas and sets `expectedConfigHash`, so validate the payload locally first (step 2) —
`send-endpoints` only checks that it is valid JSON, and a payload the extension rejects will
leave every later `submitPrice` reverting `StaleConfig`:

```bash
cd tools && go run ./cmd/send-endpoints -instructionSender <SENDER> -config ../testdata/endpoints/public-only.json
```

Confirm the machine actually applied it — this is the difference between what was *sent* and
what is *running*:

```bash
docker compose -f docker-compose.yaml -f docker-compose.coston2.yaml logs extension-tee | grep SET_ENDPOINTS
```

Expect `SET_ENDPOINTS applied`. The `configHash` that `send-endpoints` printed must equal the
`endpointsHash` in `/state`.

## 5. Request a price and put it on-chain

`run-test` validates the wire format without submitting — the cheap check:

```bash
cd tools && go run ./cmd/run-test -instructionSender <SENDER>
```

Then the real thing. `submit-price` requests, polls with a bounded deadline, decodes, preflights
every store guard, and submits:

```bash
cd tools && go run ./cmd/submit-price -instructionSender <SENDER>
```

Add `-dry-run` to stop after the preflight, or `-instructionId 0x…` to relay a result you
already have instead of paying for a new request.

## 6. Verify what a consumer sees

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <STORE> 'latest()(uint256,int8,uint64)'
```

`(100000000, 8, <recent unix ts>)` is a healthy feed at par. The feed contract reads straight
through, so `UsdxCustomFeed.getCurrentFeed()` returns the same triple. Reading it via
`FtsoV2.getFeedById` additionally requires the feed to be registered by governance
(`addCustomFeeds`) — a timelocked proposal, not a deploy step.

## 7. Reading the result

Four places tell you different things. Start at the bottom of the stack and work back only if
something looks wrong.

**The consumer surface** — what anyone reading the feed sees:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <FEED> 'getCurrentFeed()(uint256,int8,uint64)'
```

```
100000000 [1e8]   8   1786004691 [1.786e9]
└─ value          │   └─ observedAt, unix seconds
                  └─ decimals
```

Read it as `value / 10^decimals` → **$1.00000000**. Nothing else. In particular the value is
*not* a float and must never be parsed as one — at 8 decimals a `float64` round-trip is already
lossy, which is why the whole path carries it as a uint256.

| What you see | What it means |
| --- | --- |
| `100000000` | Par. Backing ≥ supply, so the price is capped at $1.00 |
| `99000000` | 0.99 — a **1% shortfall**, real and worth paging on |
| `99999999` | One unit below par. After the 5 Aug rounding change this can no longer be caused by Hex's cent quantisation, so it means backing genuinely fell just short |
| timestamp `0` | Governance called `setInvalidated` — the value is withdrawn. Treat as no data |
| timestamp not advancing | The feed froze. **This is the designed failure mode**, not an outage: check the age, do not assume the last price is current |

**Always check the age.** The feed cannot revert — `FtsoV2` forwards the call, so a revert would
take down every consumer at once — which means a stale price is returned looking exactly like a
fresh one. The timestamp is the only signal:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <STORE> 'observedAt()(uint64)'
```

**The store's own view**, for operators — `price()`, `observedAt()`, `paused()`,
`invalidated()`, and `observedAtInstructionID()`, which is the instruction that produced the
stored price. That last one is the thread back to the TEE.

**The signed report behind it.** Feed the instruction id to a dry run and you get the decoded
report without spending gas:

```bash
cd tools && go run ./cmd/submit-price -dry-run -instructionId <ID>
```

```
price:         100000000 (1.00000000 at 8 decimals)
observedAt:    1786004691 (2026-08-06T08:24:51Z)
instructionId: 0x7040b32d…cb17
configHash:    0xf9f4c2be…4a8d
body:          192 bytes, signature: 65 bytes
```

Check `configHash` equals what `send-endpoints` printed — that is the proof the machine computed
this price under the configuration you published, not a stale one.

Re-running a dry run against an already-stored report ends in
`NotNewer: the store already holds observedAt=…`. **That is the correct answer**, not a
failure: the store takes strictly increasing timestamps, which is what makes replay and
out-of-order delivery harmless.

**The machine's own health** is in the extension's `GET /state` — last price, counters, error
class and per-source health. Under Docker the extension port is container-internal, so read it
from the logs or via the proxy's `/info` (`teeInfo.state.state`, hex-encoded JSON) once tee-node
has polled it.

### Verifying the contracts on the explorer

Until this is done the explorer shows bytecode, so nobody can check the acceptance rules they
are relying on — which undercuts the whole point of the design.

```bash
./scripts/verify-contracts.sh
```

Already-verified contracts are skipped; `--force` re-submits, `--only <Name>` narrows, and
`--chain coston` switches network. Once it passes, `<explorer>/address/<addr>#code` shows the
source, and **Read Contract** lets you call `latest()`, `price()` and `getCurrentFeed()` from a
browser with no tooling at all — the easiest way to hand someone a link that proves the feed
is real.

Constructor arguments are **recovered from the deployment**, never hand-written, because for
this store they cannot be read back: `setExtension` overwrites two of the values the
constructor took, so today's storage would produce arguments the contract was never deployed
with. The script pulls the creation bytecode from the explorer and subtracts the locally
compiled initcode; what remains is exactly the encoded arguments. That subtraction doubles as
a reproducibility check — if your local build does not reproduce the deployed initcode byte for
byte, it stops rather than submitting something that cannot verify.

Verified on Coston2, 6 Aug 2026, against the deployment in the plan doc.

### What a passing run does and does not prove

| Proven | Not proven |
| --- | --- |
| The whole path: instruction → observation → signature → store → feed | The **below-par** path. Everything so far prints par, so the arithmetic that matters during a depeg has been exercised only in unit tests |
| The signature verifies against a registered PRODUCTION machine | The **cash source**. It has never been reachable; par is computed cash-less |
| The config hash binds the price to the published endpoints | **Sustained operation.** One submission is not a keeper on a timer |
| Consumers can read a price from `UsdxCustomFeed` | Reading via `FtsoV2.getFeedById`, which needs the `addCustomFeeds` governance proposal |

## When it goes wrong

Everything here was hit for real while bringing this up.

| Symptom | Cause | Fix |
| --- | --- | --- |
| `execution reverted: Extension ID is not set.` | A freshly deployed sender has never had `setExtensionId()` called. Only `run-test` calls it. | Run `run-test` once against the new sender. |
| Revert `0x3b06a1be` (`MessageEmpty()`) | The instruction carried an empty message. | The sender must send `GET_PRICE_REQUEST_V1`; findings §22. |
| Result `status 3`, log `context deadline exceeded` | The handler took longer than tee-node's **2 s** action budget. | Observation must stay off the request path — that is what `internal/observe` is; findings §23. |
| Result `status 0`, log `no recent observation` | The background loop has not produced anything inside `MaxObservationAge` (10 min). | Check `/state` `sourceHealth`; an upstream is failing. Working as designed — it fails closed. |
| Result `status 0`, log `instruction carries an unexpected payload` | `originalMessage` was not exactly `0x01`. | Check the sender's constant matches `config.GetPriceRequestV1`. |
| `submitPrice` reverts `StaleConfig` | The machine is running a different config than `expectedConfigHash`. | Compare `/state` `endpointsHash` against the hash `send-endpoints` printed; re-send if they differ. |
| `submitPrice` reverts `UnknownSigner` | Machine not at status 2, **or the store still points at a previous extension generation**. Re-running `pre-build.sh` mints a new id and sender, and the store does not follow automatically. | Compare `store.extensionId()` with `getExtensionId(<TEE_ID>)`. If they differ, run `set-store-extension` (step 3e). Otherwise check `getTeeMachineStatus` and re-run `post-build.sh`. |
| `connect: connection refused` on `localhost:6664` | Running under `--local`, where the proxy binds 6664 directly. (Before 6 Aug 2026 this was the default and Docker users hit it instead.) | `-p http://localhost:6664`, or export `EXT_PROXY_URL`. See "Which proxy port" above. |
| `submitPrice` reverts `NotNewer` | A newer price already landed. | **Not a fault** — with ≥2 keepers exactly one submission wins per cycle. |
| `submitPrice` reverts `NoConfigPublished` | No `setEndpoints` has ever been sent to this machine. | Step 4. |
| Registration stalls at status 1 | The availability proof needs the proxy reachable from outside. | Bring the public tunnel up and re-run `post-build.sh`. |
| Tools get `invalid character 'T'` or HTML back | `EXT_PROXY_URL` points at a dead tunnel. | Restart it. Note `post-build.sh` does `source .env`, so an inline `EXT_PROXY_URL=…` override is discarded — edit `.env` or call the tools directly with `-p`. |
| Restart kept the old extension id | redis persisted tee-node's machine data. | `stop-services.sh` (which runs `down`) before starting. |

## What the test does

The test runner (`tools/cmd/run-test/main.go`) executes this lifecycle:

```
1. SetExtensionId()         ← Generic: tells the contract its extension ID (idempotent)
2. Send instruction          ← YOUR CODE: call your contract function with your payload
3. Wait for TEE processing   ← Generic: time.Sleep(5s)
4. Poll for result            ← Generic: utils.ActionResult() polls proxy (15 retries, 2s apart)
5. Validate response          ← YOUR CODE: unmarshal Data into your type, check your fields
```

Steps 1, 3, and 4 are the same for every extension. Steps 2 and 5 are what you customize.

## How the scaffold test works

The scaffold's test sends instructions via `SendSayHello` and `SendSayGoodbye` and verifies the responses. Here's how each part works — when you build your own extension, you'll replace these with your own types, payloads, and assertions.

### 1. Define your message and response types

The scaffold defines `sayHelloResponse` and `sayGoodbyeResponse` at the top of the test file. They **mirror** your extension's response shapes rather than importing them — `tools/` is deliberately independent of every language implementation, which is what lets this one test run unchanged against Go, Python and TypeScript:

```go
type sayHelloResponse struct {
    Greeting       string `json:"greeting"`
    GreetingNumber int    `json:"greetingNumber"`
}

type sayGoodbyeResponse struct {
    Farewell       string `json:"farewell"`
    FarewellNumber int    `json:"farewellNumber"`
}
```

Replace these with structs matching your extension's response shapes.

### 2. Send your instructions

The scaffold builds JSON payloads and sends them through the contract. The scaffold includes two test cases:

```go
// SAY_HELLO test case
payload, _ := json.Marshal(map[string]interface{}{
    "name": "World",
})
instructionId, _, err := instrutils.SendSayHello(s, addr, payload)

// SAY_GOODBYE test case
payload, _ = json.Marshal(map[string]interface{}{
    "name": "World",
})
instructionId, _, err = instrutils.SendSayGoodbye(s, addr, payload)
```

Replace the payloads with whatever your contract functions expect. If your Solidity contract has multiple send functions, you'll need to add corresponding Go helpers in `tools/pkg/utils/instructions.go` and call them here.

### 3. Validate your responses

The `verifyHelloResult` and `verifyGoodbyeResult` functions each receive the raw response from the proxy. The response envelope is always the same:

```json
{
  "result": {
    "id": "0x...",
    "status": 1,
    "log": "",
    "opType": "0x...",
    "opCommand": "0x...",
    "data": "<your extension's JSON response>"
  }
}
```

- `status`: `0` = failed, `1` = success, `2` = pending
- `log`: error message when `status == 0`
- `data`: your extension's response bytes — this is whatever your `processAction` handler returned via `buildResult`

The generic status checks are already in `verifyHelloResult` and `verifyGoodbyeResult`. The scaffold validates the SAY_HELLO response like this:

```go
// verifyHelloResult
var resp sayHelloResponse
err = json.Unmarshal(actionResult.Data, &resp)
if err != nil {
    return errors.Errorf("failed to unmarshal response: %s", err)
}

if resp.Greeting == "" {
    return errors.New("expected non-empty Greeting")
}
if resp.GreetingNumber < 1 {
    return errors.Errorf("expected GreetingNumber >= 1, got %d", resp.GreetingNumber)
}
```

And the SAY_GOODBYE response like this:

```go
// verifyGoodbyeResult
var resp sayGoodbyeResponse
err = json.Unmarshal(actionResult.Data, &resp)
if err != nil {
    return errors.Errorf("failed to unmarshal response: %s", err)
}

if resp.Farewell == "" {
    return errors.New("expected non-empty Farewell")
}
if resp.FarewellNumber < 1 {
    return errors.Errorf("expected FarewellNumber >= 1, got %d", resp.FarewellNumber)
}
```

Replace the response types, unmarshal targets, and field assertions with your own.

### 4. Add more test cases

The scaffold shows two send+verify pairs (SAY_HELLO and SAY_GOODBYE). For a real extension, add multiple test cases covering:

- Each op type your extension supports
- Success cases with valid inputs
- Edge cases (empty fields, boundary values)
- Error cases (invalid payloads that should return `status == 0`)

### Matching op types between Solidity and your extension

Your Solidity contract defines op types and op commands as `bytes32` constants:

```solidity
bytes32 constant OP_TYPE_GREETING  = bytes32("GREETING");
bytes32 constant OP_COMMAND_SAY_HELLO   = bytes32("SAY_HELLO");
bytes32 constant OP_COMMAND_SAY_GOODBYE = bytes32("SAY_GOODBYE");
```

Your extension routes on the same values. Go matches them explicitly in `processAction`:

```go
case dataFixed.OPType == teeutils.ToHash(config.OPTypeGreeting) &&
    dataFixed.OPCommand == teeutils.ToHash(config.OPCommandSayHello):
    return e.processSayHello(action, dataFixed)
```

Python and TypeScript register the pair up front and let the framework dispatch:

```python
framework.handle(OP_TYPE_GREETING, OP_COMMAND_SAY_HELLO, handle_say_hello)
```

```typescript
framework.handle(OP_TYPE_GREETING, OP_COMMAND_SAY_HELLO, handleSayHello);
```

All three compare the same bytes32 encoding — the UTF-8 string right-padded with zeros to 32 bytes. **The strings must match Solidity exactly**; a mismatch is not a compile error, it silently falls through to "unsupported op type" (HTTP 501) at runtime.

The test sends instructions through the contract functions (`sendSayHello`, `sendSayGoodbye`), which set the OPType to `GREETING` and the corresponding OPCommand, then verifies the response matches whatever your handler returned. Because the test asserts on the wire format rather than on language types, it is identical for every implementation.

## What you need to change (summary)

| Step | What to change | Where |
|------|---------------|-------|
| Response types | Define structs for your extension's responses | `tools/cmd/run-test/main.go` (top of file) |
| Message payloads | Create the JSON your contract function expects | `main()` in `run-test/main.go` |
| Send instructions | Call your contract's specific function(s) (e.g. `SendSayHello`, `SendSayGoodbye`) | `main()` in `run-test/main.go` |
| Validate responses | Unmarshal `Data` and assert your fields | `verifyHelloResult()` / `verifyGoodbyeResult()` in `run-test/main.go` |
| Op type + command routing | Match the constants across Solidity and your extension | `go/internal/config/config.go`, `python/app/config.py`, or `typescript/src/app/config.ts` |
| Add test scenarios | Add more send+verify pairs for each op command | `main()` in `run-test/main.go` |
| Conformance fixtures | Regenerate after any request/response shape change | `testdata/conformance/gen_fixtures.py` |

None of the `run-test` changes are language-specific: the tool asserts on the wire format, so one set of edits covers every implementation you maintain.
