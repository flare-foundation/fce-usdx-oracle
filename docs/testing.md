# Testing

Tests split into two groups: those covering **the extension**, and those covering the
**deployment tooling**. Cheapest first — only the last layer needs a chain.

## The extension

| Layer | What it tests | How to run | Chain? |
|-------|--------------|------------|--------|
| **Unit** | Routing, the handlers, the observation path, provisioning, the wire codecs | `GOWORK=off go test ./...` at the root | no |
| **Conformance** | The wire contract, against 13 golden fixtures | `GOWORK=off go test ./internal/extension -run TestConformance` | no |
| **Wire vectors** | Solidity's own encoding of the payloads that cross the extension boundary | `cd contracts && forge test` (prerequisites below) | no |
| **End-to-end** | The full feed cycle (request → observe → sign → relay) | `./deploy/scripts/oracle-cycle.sh -tee <machine>` | yes |

### Before you run them

- **`GOWORK=off`.** `start-services.sh` with `USE_LOCAL_SIBLINGS=1` writes a git-ignored
  `go.work` at the root that points at the sibling `tee-node` and `tee-proxy` checkouts. While it
  exists, a plain `go test` builds against those checkouts rather than the versions `go.mod` pins,
  and says nothing about it. Run every `go` command in this document with `GOWORK=off`, or delete
  the file (`start-services.sh` without `USE_LOCAL_SIBLINGS` removes it). CI never has one.
- **An exported `EXTENSION_ID` breaks `TestConformance`.** The extension refuses to run without
  `CHAIN_ID` and `EXTENSION_ID`, so the packages that need them have a `TestMain`
  (`env_test.go` in `internal/extension`, `internal/observation`, `internal/observation/poller`
  and `internal/provisioning/store`) that sets `CHAIN_ID=31337` and `EXTENSION_ID=1`, **only when
  they are unset**. Fixtures 01 and 02 embed extension id 1 in the payload they pin, so a shell
  that exported another id (`set -a; source .env.coston_local` exports 65652) fails them. Unset
  it (`env -u EXTENSION_ID GOWORK=off go test ./...`) or use a clean shell.
- **The tooling reads `.env`.** `tools/pkg/configs` loads the root `.env` and then
  `deploy/config/stack.env` when a tool or a tooling test starts, so the tools' defaults follow
  the active chain.

### Conformance testing

This is the layer that pins the wire contract: routing, status codes, the `ActionResult` field
set and its encoding, and the `GET /state` body, independent of any source or chain.

```bash
GOWORK=off go test ./internal/extension -run TestConformance
```

`TestConformance` is an **in-process Go test**. It builds the extension with a fixed observer and
no provisioner, then replays the 13 fixtures in `deploy/testdata/conformance/` through the
server's handler: no tee-node, no proxy, no chain, no Docker and no listening socket. It runs in
about a second.

Every fixture asserts the HTTP status. Beyond that, each asserts what its `expect` block names:
the exact JSON body (01, 12, 13), a subset of fields (02, 03), a substring of a plain-text body
(04, 05), or the prefix of `log` (03). Fixtures 06 to 11 assert the status alone. Where the body is
compared exactly, `data` is compared byte for byte, because tee-node hashes it into what it signs.
[extension-contract.md](extension-contract.md) §8 lists what each fixture pins and what the
fixtures leave to the unit tests.

The fixtures share one extension, and `index.json` fixes their order. None of the current set
depends on what an earlier one did: the extension keeps no state a request can move.

The payload in fixtures 01 and 02 is `abi.encode(FeedUpdate, Signature)` over the test's fixed
observation and constant signature (`conformance_test.go`); nothing generates it. If you change a
request or response shape, change the fixture by hand: run the test, check the `got` it prints
against [extension-contract.md](extension-contract.md), and copy it in.

> When this suite disagrees with the implementation, check
> [extension-contract.md](extension-contract.md) before changing the fixture. The contract is
> normative; the fixtures encode it.

The replay is a test of this Go package, not a client of a running container, so it cannot be
pointed at an implementation in another language. Extension-contract §8 says what a port would
need.

### Fuzz targets

| Target | Package | Fuzzes |
| --- | --- | --- |
| `FuzzVerify` | `internal/provisioning/envelope` | the provisioning envelope verifier, on arbitrary messages |
| `FuzzParseAmount` | `internal/observation/decimal` | the parser for Hex's reserve amounts: it must never panic |
| `FuzzCompute` | `internal/observation/price` | the price computation: never negative, never above par |

`go test` runs each target's seed corpus as an ordinary test. To fuzz, name one target and its
package:

```bash
GOWORK=off go test ./internal/provisioning/envelope -run '^$' -fuzz '^FuzzVerify$' -fuzztime 60s
```

Nothing fuzzes in CI.

### The extension's unit tests

| Package | Test files | Covers |
| --- | --- | --- |
| `internal/extension` | `extension_test.go`, `provisioning_test.go`, `readiness_test.go`, `conformance_test.go` | routing and its 400 and 501 cases, the channel rule for each command, the `GET_FEED` payload rules, `GET_READINESS`, the conformance replay |
| `internal/observation` | `observation_test.go` | one observation end to end: fail closed, the cash-less rule, health seeded for every source |
| `internal/observation/poller` | `poller_test.go`, `readiness_test.go`, `report_admins_test.go` | the cached observation and its age bound, the clock check against the request, signing, the readiness report and its `wouldSign`, no signature without an admin set |
| `internal/observation/supply` | `supply_test.go` | the quorum procedure: head pinning, the step-down, agreement, stale blocks, per-provider health |
| `internal/observation/backing` | `backing_test.go` | both Hex response shapes, the metric and currency checks, timestamps, error classes, no redirects, the body cap |
| `internal/observation/price`, `decimal` | `price_test.go`, `decimal_test.go` | half-up rounding in exact arithmetic, the cash-less rule; amount parsing |
| `internal/provisioning/envelope` | `envelope_test.go`, `construct_test.go`, `policy_test.go` | thresholds, distinct signers, the binding, freshness, replay and rollback, the role policy |
| `internal/provisioning/store` | `store_test.go`, `abi_test.go`, `dial_test.go` | the `SET_ENDPOINTS` validation rules, the cash-URL commitment, admin sets, secrets, decoding Solidity's payloads, the RPC response cap |
| `internal/teenode` | `teenode_test.go` | what tee-node's `/sign` actually returns: the recovery id, EIP-191, base64 |
| `internal/failure`, `internal/config`, `pkg/protocol` | `failure_test.go`, `extensionid_test.go`, `protocol_test.go` | the public vocabulary, the mandatory ids, the op hashes |
| `pkg/wire/feedupdate`, `pkg/wire/configpayload`, `pkg/wire/envelope` | `feedupdate_test.go`, `configpayload_test.go`, `envelope_test.go` | the codecs; the first two against Solidity's bytes in `deploy/testdata/wire` |

The provisioning store's concurrency and size bounds are pinned by name.
`TestApplySecret_ConcurrentReplayAdmitsExactlyOne` submits one envelope eight times at once and
requires exactly one to be accepted; `TestApplySecret_IsSafeUnderConcurrency` sends eight
different envelopes at once; `TestApplySecret_RejectsOversizedMessage` pads a valid envelope past
the size cap and requires a refusal, then shows the same version still applies at normal size.

### Wire vectors (`forge test`)

`contracts/` is a Foundry project with no contracts of its own: its tests print Solidity's
encoding of the boundary payloads, which `deploy/scripts/regen-wire-fixtures.sh` writes into
`deploy/testdata/wire/` and the Go codecs are tested against
([its README](../deploy/testdata/wire/README.md)). Prerequisites:

- `forge` on the `PATH`;
- the sibling checkout of `flare-smart-contracts-v2` (branch `tee-oracle-extension`) at
  `../../../fsp/flare-smart-contracts-v2` from the repository root, where the remappings in
  `contracts/foundry.toml` look, with its Soldeer dependencies installed: `forge soldeer install`
  in that checkout (`deploy-oracle.sh` also installs them when missing);
- forge-std in the git-ignored `contracts/lib/`:
  `cd contracts && forge install --no-git foundry-rs/forge-std`.

```bash
cd contracts && forge test
```

CI does not run it: CI has no access to the sibling checkout, and the vectors reach it as the
fixture files the Go tests read.

## The deployment tooling

`tools/` is its own Go module (Go 1.25.8). It imports the extension's `pkg/` packages rather than
restating them, and adds the operator's half on top. Its tests need no external services:

```bash
cd tools && GOWORK=off go test ./...
```

| Test file | Covers |
| --- | --- |
| `cmd/keeper/main_test.go` | parsing the `-minBalance` amount |
| `cmd/send-endpoints/endpoints_test.go` | the shipped endpoint configurations validate; the validation rules; the summary names refs, never URLs |
| `cmd/send-admins/admins_test.go` | admin-set validation: both roles, known roles, the file schema |
| `cmd/send-secret/main_test.go` | deriving the teeId from the served key; refusing a key that is not the attested machine's; the cash-URL preimage; counting only distinct signers of the governing set |
| `pkg/configs/configs_test.go` | resolving the chain from `FLARE_CHAIN` and `CHAIN_ID`, `coston_local` included; every chain the scripts know is known here |
| `pkg/direct/direct_test.go` | the direct-channel request shape, proxy refusals, the result-body cap, waiting for the machine's verdict |
| `pkg/feedupdate/feedupdate_test.go` | decoding the extension's update; the digest against Solidity's; signer recovery |
| `pkg/keeper/batch_test.go` | screening answers and choosing the batch |
| `pkg/keeper/fleet_test.go` | the machine list and polling every proxy |
| `pkg/keeper/keeper_test.go` | outcome classification, the record emitter, timeouts |
| `pkg/keeper/relay_test.go` | the relay's outcomes: a landed submission, `race_lost` and `rejected` for each way `NotNewer` arrives, a mined revert, no receipt |
| `pkg/keeper/pollresult_test.go`, `pkg/keeper/readiness_test.go` | polling for a result; the readiness preflight that may skip a request |
| `pkg/fccutils/revert_test.go`, `pkg/support/support_test.go` | decoding `Error(string)` and custom-error reverts from raw hex and from go-ethereum's JSON-RPC errors |
| `pkg/fccutils/registration_test.go` | the `register-tee` state file |
| `pkg/fccutils/status_test.go` | machine status names follow upstream's `TeeStatus` |
| `pkg/fccutils/policy_consistency_test.go` | the proxy's signing-policy epoch against the chain's |
| `pkg/support/key_test.go`, `pkg/support/readonly_test.go` | refusing the dev key on a live chain; read-only tools need no key |
| `pkg/utils/instructions_test.go` | publication calldata against the sender's ABI |
| `pkg/validate/checks_test.go`, `report_test.go`, `validate_test.go` | the extension-env format, deployer-key detection, the check registration, report formatting, and the validation primitives |

The validation primitives `AddressHasCode` and `KeyHasFunds` are tested only for a nil client and
zero addresses: nothing exercises them against a chain.

## CI

`.gitlab-ci.yml` runs on every pipeline:

| Job | Runs |
| --- | --- |
| `build` | `go build ./...` in both modules |
| `lint` | `gofmt -l` over the tracked Go files, which must print nothing; `go vet ./...` in both modules |
| `golangci` | `golangci-lint run` with `.golangci.yml`, in both modules |
| `test` | `go test -race ./...` at the root, `TestConformance` among them, with coverage (`-covermode atomic`); `go test -race ./...` in `tools/`. The race detector is what checks the locking properties: the provisioning store's replay protection and the poller's cache |
| `scripts` | `bash -n` on every script in `deploy/scripts/` and `deploy/scripts/lib/`; `deploy/scripts/check-versions.sh` |

The shared CI component the pipeline includes adds its own lint, hadolint and shellcheck jobs.
Not in CI: `forge test`, fuzzing beyond the seed corpora, and anything that needs a chain. The
same gates, locally:

```bash
test -z "$(gofmt -l $(git ls-files '*.go'))"
GOWORK=off go vet ./... && (cd tools && GOWORK=off go vet ./...)
golangci-lint run ./... && (cd tools && golangci-lint run ./...)
GOWORK=off go test -race ./... && (cd tools && GOWORK=off go test -race ./...)
for f in deploy/scripts/*.sh deploy/scripts/lib/*.sh; do bash -n "$f"; done
./deploy/scripts/check-versions.sh
```

## End-to-End Tests

No test deploys contracts. The on-chain path is exercised against a running stack instead:

- on the local Hardhat simulation, where `deploy-oracle.sh --chain local` deploys upstream's
  contracts from the sibling checkout ([local-e2e.md](local-e2e.md));
- on a testnet deployment of your own (`coston_local`, Coston2), where `deploy-oracle.sh` deploys
  them governed by the deployer ([deploy/README.md](../deploy/README.md), "Testing on a
  testnet");
- against an upstream deployment (`coston`, `songbird`), whose contracts Flare deployed and Flare
  governance publishes to ([usdx-oracle-production-setup.md](usdx-oracle-production-setup.md)).

With the stack up and the machine registered and provisioned, drive one full cycle:

```bash
./deploy/scripts/oracle-cycle.sh -tee <machine>
```

Add `--dry-run` to stop after the preflight, before the submission. A dry run still sends and pays
for `requestFeedUpdate`: the machines cannot be asked any other way. `--loop 10m` keeps cycling on
a timer — the same code path the unattended keeper runs.

> ⚠ `full-setup.sh --chain <name>` **replaces `.env`** with `.env.<name>` (through
> `use-chain.sh`, which carries the Hex credentials across). The individual scripts only read it.

---

# Running the USDX oracle end to end yourself

The whole chain is: **request → TEE observes → signed update → store → feed.** This section walks
it on a testnet deployment of your own — `coston_local`, contracts `deploy-oracle.sh` deployed and
the deployer governs — with what to check at each step and what the failures actually look like.
The commands that bring the deployment up are in [deploy/README.md](../deploy/README.md),
"Testing on a testnet". On an upstream deployment (`coston`) the checks are the same, but the
publications are Flare governance's timelocked calls
([usdx-oracle-production-setup.md](usdx-oracle-production-setup.md)).

## 0. What must be true before you start

| | Check |
| --- | --- |
| Stack up | `curl -s $EXT_PROXY_URL/info \| jq .machineData.extensionId` answers |
| Proxy reachable **from the internet** | The same call through the public URL returns your extension id as a 32-byte word, not an ngrok error page |
| Paying keys funded | The chain's coin on `EXTENSION_OWNER_KEY`'s address, which deploys, registers and publishes on a deployer-governed testnet, and on `KEEPER_KEY`'s, which pays for the requests and submissions (the keeper falls back to `EXTENSION_OWNER_KEY` without it) |
| `deploy/config/extension.env` current | Holds `EXTENSION_ID`, which `full-setup.sh` writes from `.env` |
| Addresses file | `deploy/config/<chain>/deployed-addresses.json` names `TeeOracleFeedStore` and `TeeOracleInstructionsSender`: `deploy-oracle.sh` writes them for a deployment of your own, `import-upstream-addresses.sh` for an upstream one. The publishers take the sender from the file; the keeper takes the store from it and the sender from the store's `instructionsSender()` |

**The extension id is not a build fact.** tee-node and the extension both read `EXTENSION_ID`
from the environment, and both refuse to start without it. It is 1, the reserved id, for the
upstream deployments (`coston` and the mainnets). For a deployment of your own it is the public
id the registry minted for the deployer, which `deploy-oracle.sh` records in `.env` and
`.env.<chain>` (`coston_local`: 65652). The **feed id** is the build fact: `protocol.FeedID`,
`0x21555344582f555344…`.

### Which proxy port

The tools default to **`http://localhost:6674`**, the port Docker Compose publishes for the
proxy's external API (`6674 -> 6664`). The container listens on 6664, which is not reachable from
the host. The host-process stack (`--local`, `full-setup-host.sh`) binds 6684 directly.

The tools read `.env` and `deploy/config/stack.env` themselves (`tools/pkg/configs`), so a hand-run
`go run` picks up `EXT_PROXY_URL` from `.env`. On the simulation `start-services.sh` records the
stack it brought up in `deploy/config/stack.env`, which outranks both `.env` and the shell, so
nothing needs `-p`. On a testnet `EXT_PROXY_URL` in `.env` is the machine's public URL, the
tunnel; `-p` overrides everything.

A wrong port shows up as:

```
deriving TEE identity from http://localhost:6684/info: connect: connection refused
```

Registration is the one step that genuinely needs the **public** URL: machine registration ends
with an availability proof that data providers verify from outside, and they cannot reach
`localhost`. If your tunnel is down, registration stalls at status 1 (INITIALIZED) and nothing
downstream works.

## 1. The fast checks — no chain, no Docker

Run these first. They catch most breakage in seconds.

```bash
GOWORK=off go test ./... && (cd tools && GOWORK=off go test ./...)
```

```bash
GOWORK=off go test ./internal/extension -run TestConformance
```

```bash
cd contracts && forge test
```

## 2. Prove a real observation locally

This is the highest-value local test: the real observation path against **live** Flare,
Ethereum, Songbird and Hex — no chain of your own, no TEE, no credentials. One script runs the
whole thing:

```bash
./deploy/scripts/oracle-observe.sh
```

It starts a mock signing node and the dev server on high ports, provisions them with an endpoint
configuration and the dev-only admin set, requests a feed update, waits out the first
observation, and tears both down.

To run it by hand instead — three terminals, or background the first two:

```bash
SIGN_PORT=9099 EXTENSION_PORT=8099 go run ./cmd/dev/mocknode
```

```bash
CHAIN_ID=31337 EXTENSION_ID=1 EXTENSION_PORT=8099 SIGN_PORT=9099 go run ./cmd/dev/devserver
```

```bash
./deploy/scripts/set-config.sh endpoints deploy/config/endpoints/public-only.json 8099
```

Observation needs only the endpoints, but a signed feed update also needs an admin set: the
update commits to one, and the machine refuses to sign without it. The dev-only set is built from
well-known keys and must never be published to a live feed:

```bash
./deploy/scripts/set-config.sh admins deploy/config/admins/dev-wellknown-keys.json 8099
```

Then ask for a feed update. `originalMessage` is the ABI-encoded `FeedUpdateRequest` — the
21-byte feed id in a single 32-byte word; any other payload is refused (findings §22). The
`timestamp` stands in for the requesting block's, which the update is signed under:

```bash
python3 -c "import json,time;b32=lambda s:'0x'+s.encode().ljust(32,b'\x00').hex();df={'instructionId':'0x'+'c1'*32,'teeId':'0x'+'22'*20,'timestamp':int(time.time()),'rewardEpochId':0,'opType':b32('TEE_ORACLE'),'opCommand':b32('GET_FEED'),'cosigners':[],'cosignersThreshold':0,'originalMessage':'0x21555344582f5553440000000000000000000000000000000000000000000000','additionalFixedMessage':'0x'};print(json.dumps({'data':{'id':'0x'+'c1'*32,'type':'instruction','submissionTag':'submit','message':'0x'+json.dumps(df).encode().hex()},'additionalVariableMessages':[],'timestamps':[],'additionalActionData':'0x','signatures':[]}))" > /tmp/getfeed.json
```

```bash
curl -sS -X POST -H 'Content-Type: application/json' --data-binary @/tmp/getfeed.json http://127.0.0.1:8099/action | jq '{status, log}'
```

```bash
# the machine's account of itself: its signed readiness report, asked of the dev server directly
(cd tools && go run ./cmd/readiness -extension http://127.0.0.1:8099) \
  | jq '{observationAge, wouldSign, sourceHealth, readiness}'
```

The extension observes once at start, when it holds no endpoints, and then every 2 minutes on its
own timer, so a request made before the next tick answers `error: extension is not provisioned`.
`oracle-observe.sh` waits that out, for up to 3 minutes. Observation never runs on the request
path — a `GET_FEED` costs one signature and nothing more, which is what keeps it inside
tee-node's 2 s action budget.

**What good looks like** — `status: 1`, and:

```json
{ "observationAge": 12, "wouldSign": true,
  "sourceHealth": { "hex_bonds": "ok", "hex_cash": "not_configured",
  "rpc_flare": "ok", "rpc_flare_1": "ok", "rpc_flare_2": "ok", "rpc_ethereum": "ok", "…": "…" },
  "readiness": { "endpointsSet": true, "adminsSet": true, "ready": false,
                 "missingSecretRefs": ["hex_api_key", "hex_cash_url"] } }
```

The price itself is in the `data` of the `GET_FEED` result, the signed update; `oracle-observe.sh`
decodes it.

`ready: false` with `hex_api_key` and `hex_cash_url` missing is **expected** until Hex issues the
key and both are provisioned through `SET_SECRET`. The feed still publishes at par, because a
bonds-only price that already reaches par is provably what the full computation would give
(findings §21). It will *not* publish anything below par.

## 3. Bring up the stack

On `coston_local` the one-time steps — the chain switch, the addresses, the indexer, the tunnel,
`deploy-oracle.sh` and `wire-extension` — are in [deploy/README.md](../deploy/README.md),
"Testing on a testnet". This step is needed the first time, and after every change to the
extension code.

**a. Full setup** — activates `.env.coston_local`, writes `EXTENSION_ID` into
`deploy/config/extension.env`, starts the TEE and the proxy, and runs the registration in
`post-build.sh` (`allow-tee-version` → `set-governance` → `register-tee`), waiting for the
availability proof:

```bash
./deploy/scripts/full-setup.sh --chain coston_local
```

**b. After a code change** — a new image means a new container, and a new container means a **new
TEE identity**: the enclave key is generated in memory when tee-node starts, and nothing persists
it.

```bash
./deploy/scripts/stop-services.sh --chain coston_local && ./deploy/scripts/start-services.sh --chain coston_local
```

Use `stop` then `start`. `stop-services.sh` first retires the running machine on chain
(`retire-tee`, the registry's pause), so the old identity does not stay in the extension's active
set, then takes the stack down. `start-services.sh` alone recreates the container on a live chain
only when its image or configuration changed; otherwise the process, and its identity, carries
on.

Then register the new identity:

```bash
./deploy/scripts/post-build.sh
```

The new machine has none of the feed's configuration. Publications go to every machine active
when they execute, so either publish again (step 4), or push the latest publication to it with
the sender's permissionless, payable `pushEndpoints` / `pushAdmins`, fed the payload of the latest
`EndpointsPublished` / `AdminsPublished` event (no tool in this repository wraps them). Its
credentials are bound to its teeId, so they are uploaded again too.

**Verify before going on.** The store accepts signatures only from machines at status **2**
(PRODUCTION). `<RPC>` here and below is the chain's RPC, `https://coston-api.flare.network/ext/C/rpc`
for `coston_local`:

```bash
cast call --rpc-url <RPC> "$(jq -r '.[] | select(.name=="FlareTeeManager") | .address' deploy/config/coston_local/deployed-addresses.json)" 'getTeeMachineStatus(address)(uint8)' <TEE_ID>
```

The statuses are 0 NONE, 1 INITIALIZED, 2 PRODUCTION, 3 SUSPENDED, 4 PAUSED, 5 BANNED.
`full-setup.sh` prints `<TEE_ID>`; it is also the attested teeId `verify-tee` derives:

```bash
(cd tools && go run ./cmd/verify-tee -printTeeId)
```

## 4. Publish the endpoint configuration

The endpoint configuration sets the sender's `latestEndpointsHash(feedId)`, which every later
feed update must carry. On a sender its deployer governs, `publish-local.sh` publishes it in one
transaction and then waits for the machine to report the new hash. Its default,
`deploy/config/endpoints/public-only.json`, names public RPC providers only, so no RPC credential
has to be uploaded before the machine can observe; a file whose provider URLs name `{secret}`
refs (`coston.json`) needs `upload-rpc-keys.sh` below first:

```bash
./deploy/scripts/publish-local.sh endpoints
```

With `HEX_CASH_URL` in `.env`, the `hex_cash` group commits to that URL instead of the file's
placeholder. Underneath it runs `send-endpoints`, which checks the payload against the rules the
machine applies before anything is sent. A payload the machine rejects anyway leaves it running
its previous configuration, and its updates are then refused `StaleEndpoints`. To check without
sending:

```bash
cd tools && go run ./cmd/send-endpoints -config ../deploy/config/endpoints/public-only.json -dry-run
```

`send-endpoints` predicts the version the sender will assign (`endpointsVersion(feedId)` + 1) and
prints the predicted `endpointsHash`, the value the machine must report before its updates are
accepted. Sending directly works only while the sender is not in production mode; on a production
deployment the publication is Flare governance's timelocked call, and `-out` writes the hand-off
(calldata, fee and predicted hash) instead. `-claimBack` names the payer of record.

Then publish the admin set, which decides who may install credentials on the machine. Off the
simulation, `publish-local.sh admins` defaults to `deploy/config/admins/<chain>.json`, which
`use-chain.sh` writes from the deployer key:

```bash
./deploy/scripts/publish-local.sh admins
```

Until both kinds are published for the feed, `requestFeedUpdate` reverts `FeedNotConfigured`.
A machine that has endpoints but no admin set observes, but refuses to sign
(`error: extension is not provisioned`).

### Handing the Hex credentials to the machine

The credential holder does not need this repo's deployment context, only the proxy URL and a
few secrets. One script covers the whole flow:

```bash
./deploy/scripts/upload-hex-key.sh
```

It reads `.env` first: `HEX_API_KEY` is the credential, and with `HEX_CASH_URL` and
`HEX_CASH_SALT` set it also uploads `hex_cash_url`, the commitment preimage of the restricted cash
endpoint, which the machine needs alongside the key before the cash figure counts. On the recorded
local stack the proxy URL, the API key and the dev backing admin key default as well, so the run
is unattended. Elsewhere it asks for what is unset: the proxy URL, and the chain id when `.env`
names no chain; hidden, the admin keys (`ADMIN_KEYS`, or `ADMIN_KEY`), `DIRECT_API_KEY` and the Hex
API key. `EXPECTED_CODE_HASH` is optional; on a machine `.env` marks `SIMULATED_TEE=true` it
defaults to tee-node's simulated-machine constant.

**Signatures.** Both refs fall under the `backing` set. The script signs with every key in
`ADMIN_KEYS` (comma-separated), or the single `ADMIN_KEY`, and needs as many distinct signers as
the set's threshold: `--threshold`, else `BACKING_THRESHOLD`, else `backing.threshold` in
`deploy/config/admins/<chain>.json`, else 1, the floor. With fewer it refuses before building
anything. `--build-only` then encrypts each envelope and writes it to `deploy/envelopes/` unsigned,
for the other admins to sign and submit (`send-secret signers` shows who has signed). The floor
of 1 is what the dev set publishes; Songbird's backing set is 2-of-3.

Three steps. First, verify the machine (`verify-tee -printTeeId`), which yields the teeId
everything is encrypted to. Then, per credential, encrypt it to that machine
(`send-secret build -tee <teeId>`, which refuses any other key), sign, and submit over the direct
channel, waiting for the machine's own verdict (`send-secret submit -wait`, so "applied" means
the enclave holds it, not that the proxy took it). Last, confirm from the machine's readiness
report. Verification gates the rest — if it fails, nothing is sent. The URL goes before the key,
because the machine accepts it only against the published commitment: `publish-local.sh
endpoints` makes that commitment from the same two variables, and `send-secret commit` prints it
for a hand-written configuration.

**Why the verification step is not optional.** The next step encrypts a credential to a public
key the proxy served. A proxy that lied about that key would receive the plaintext. Reading a code
hash from the same proxy proves nothing on its own, so `verify-tee` closes the loop against the
chain instead:

```bash
cd tools && go run ./cmd/verify-tee -p <PROXY_URL> -codeHash 0x<EXPECTED> -printTeeId
```

It derives the teeId from the attested public key the proxy serves, then reads the registry's
record of that machine: it must be PRODUCTION, on the expected extension (`-extensionId`, default
`EXTENSION_ID` from `.env`; with neither it refuses), with a code hash and platform the extension
accepts. The code hash and platform come from the chain's record, not from the token: registration
verified the attestation, so the record is authoritative, and the token's claims are only a
cross-check that fails on a mismatch. Substitute the key and the derived teeId stops matching a
registered machine. Pass `-codeHash` obtained **out of band** — without it the tool only proves
the machine is *some* accepted machine of the extension, not that it runs the build you reviewed,
and it says so. `-printTeeId` puts the teeId on stdout for `send-secret build -tee`.

**Secrets never go in argv.** `send-secret` reads its credentials from `SECRET_VALUE`,
`HEX_CASH_URL` and `HEX_CASH_SALT`, `ADMIN_KEY` and `DIRECT_API_KEY`, and has no flag form for them
— a value in argv is readable by any other local process through `ps` and lands in shell
history. The scripts hand
`DIRECT_API_KEY` to every tool through the environment as well.

### Handing the RPC credentials to the machine

The RPC keys are a separate upload, by a separate party, under a separate admin set:

```bash
./deploy/scripts/upload-rpc-keys.sh
```

Four steps — verify the machine, work out which refs to upload, build, sign and submit each, then
confirm — with three differences that follow from the split:

- **Several credentials.** Every `{secret}` named in the published endpoint configuration is its
  own ref, its own envelope and its own version. Which refs get uploaded comes from the
  **machine** — it publishes `missingSecretRefs` — so the script cannot disagree with the
  configuration actually applied. Each credential is read from an environment variable named
  after its ref: `ankr_flare_key` → `ANKR_FLARE_KEY`.
- **Two signatures.** The `providers` set is at least 2-of-N, so one laptop usually cannot
  finish. The script signs with every key in `ADMIN_KEYS` (comma-separated), submits once it has
  `PROVIDER_THRESHOLD` (default 2, the floor; `--threshold` overrides), and otherwise **hands
  off**: the envelope goes to `OUT_DIR` (default `deploy/envelopes/`) for the next admin to sign
  and submit. It holds ciphertext and signatures only, so it is safe to send over any channel —
  but it expires one hour after it was built.
- **Rotation is explicit.** The default uploads only what the machine says is missing, which
  makes a re-run after a partial failure safe. `--all` re-uploads every ref named in
  `ENDPOINTS_FILE` — that is the rotation path, and the only one that needs a local copy of the
  configuration, because the readiness report names what is *missing* and never the full list.

```bash
./deploy/scripts/upload-rpc-keys.sh --refs ankr_eth_key      # one ref
./deploy/scripts/upload-rpc-keys.sh --all                    # rotate everything
```

For a local mocknode, which has no attestation and no on-chain registration, both scripts take
`--no-verify`. Never use it against a machine that will hold a real credential: it turns off the
only check that the key being encrypted to belongs to an enclave at all.

> Everything in this section uses keys one person holds. For a live feed, follow
> [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md): the same commands, with the
> keys genuinely separated across parties and the multi-admin hand-off spelled out.

`dev-wellknown-keys.json` names the addresses of private keys 1, 2 and 3, so anyone can sign as
those admins. It is for the simulation only. It publishes two sets: `backing` (1-of-1, governing
`hex_api_key` and `hex_cash_url`) and `providers` (2-of-3, governing every other ref, the RPC
credentials). `send-admins` prints both, with the credentials each governs — check that before
spending gas. Both payloads are memory-only in the extension, and `SET_ADMINS` must land before
any `SET_SECRET`, since with no admin set installed nothing can authorise a credential.

Confirm the machine actually applied each publication — the difference between what was *sent*
and what is *running*. `publish-local.sh` waits for it; by hand, the readiness report carries both
hashes:

```bash
(cd tools && go run ./cmd/readiness) | jq '.readiness | {endpointsHash, adminsHash, ready, missingSecretRefs}'
```

The `endpointsHash` and `adminsHash` there must equal the hashes `send-endpoints` and
`send-admins` printed. `fleet-status` shows the same per machine, beside whether the sender
counts it as at the feed's latest publication.

## 5. Request a feed update and put it on-chain

The cheap check first. A dry run requests, polls, decodes and preflights every store guard, then
stops before the submission. It still sends, and pays for, the `requestFeedUpdate`:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run
```

Then the real thing. The keeper requests, polls with a bounded deadline, decodes, preflights
every store guard, and submits:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID>
```

(or the binary directly: `cd tools && go run ./cmd/keeper -tee <TEE_ID> -once`)

Add `--loop 10m` to stay up and cycle on a timer — the same code path the unattended keeper runs
— and `--records <file>` to append the JSON cycle records somewhere other than stdout.

## 6. Verify what a consumer sees

`getCurrentFeed()` is payable: it charges the feed's read fee (`calculateFee()`, resolved through
the central `FeeCalculator`) and forwards it to the governance-set `feeDestination`. An `eth_call`
spends nothing, but it must carry a value that covers the fee, from an address that holds it.
`<STORE>` is the `TeeOracleFeedStore` entry in `deploy/config/<chain>/deployed-addresses.json`:

```bash
cast call --rpc-url <RPC> --from <FUNDED_ADDRESS> --value 1ether <STORE> 'getCurrentFeed()(int256,int8,uint64)'
```

`(100000000, 8, <recent unix ts>)` is a healthy feed at par. The store implements the FTSO
custom-feed interface itself — there is no separate feed adapter — so this is exactly what
consumers read, and what `FtsoV2.getFeedById` returns once the feed is registered by governance
(`addCustomFeeds`), a timelocked proposal, not a deploy step. FtsoV2 forwards exactly the fee
`calculateFee` quotes. `oracle-status.sh` makes the same read.

## 7. Reading the result

Four places tell you different things. Start at the bottom of the stack and work back only if
something looks wrong. `./deploy/scripts/oracle-status.sh` reads all four without sending
anything.

**The consumer surface** — what anyone reading the feed sees, `getCurrentFeed()` above:

```
100000000 [1e8]   8   1786004691 [1.786e9]
└─ value          │   └─ observedAt, unix seconds
                  └─ decimals
```

Read it as `value / 10^decimals` → **$1.00000000**. Nothing else. In particular the value is
*not* a float and must never be parsed as one — at 8 decimals a `float64` round-trip is already
lossy, which is why the whole path carries it as an integer (`int32` in the signed update,
`int256` from `getCurrentFeed`).

| What you see | What it means |
| --- | --- |
| `100000000` | Par. Backing ≥ supply, so the price is capped at $1.00 |
| `99000000` | 0.99 — a **1% shortfall**, real and worth paging on |
| `99999999` | One unit below par. The price is rounded half-up, so Hex quoting to the cent cannot cause it at current supply: backing genuinely fell just short |
| revert `NoValuePublished` | No feed update has ever landed |
| timestamp not advancing | The feed froze. **This is the designed failure mode**, not an outage: check the age, do not assume the last price is current |

**Always check the age.** `getCurrentFeed` never reverts on staleness — `FtsoV2` forwards the
call, so a revert would take down every consumer at once — which means a stale price is returned
looking exactly like a fresh one. The timestamp is the only signal, and `observedAt()` reads it
free:

```bash
cast call --rpc-url <RPC> <STORE> 'observedAt()(uint64)'
```

**The store's own view**, for operators — `observedAt()`, `extensionId()`, `instructionsSender()`,
`feeDestination()` and `getSubmissionPolicy()`. The value and decimals themselves have no free
getter — reading them is the paid `getCurrentFeed` above. The thread back to the TEE is the
`teeIds` list in the store's `FeedUpdated` events, one entry per contributing machine.

**The signed update behind it.** A dry run requests, polls and decodes exactly like the real
thing, then stops at the preflight instead of submitting, and emits its cycle record:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE_ID> --dry-run
```

The record names each contributor's price (`prices`), the batch's `observedAt`, the
`instructionId`, and the `teeIds` recovered from the updates' signatures. The preflight checks
both configuration commitments and rehearses the submission with an `eth_call` on the way, so a
cycle that reaches `submitOutcome: dry_run` proves the machines computed their updates under the
endpoints and admin sets the sender currently publishes, and that the store would accept the
batch.

Relaying an update the store already holds ends in
`NotNewer: the store already holds observedAt=… and this batch is stamped …`, recorded as
`race_lost`. **That is the correct answer**, not a failure: the store takes strictly increasing
timestamps, which is what makes replay and out-of-order delivery harmless.

**The machine's own health** is its signed readiness report (`GET_READINESS`): observation age,
whether it would sign, its observation and failure counters, the class of the last failure, and
per-source and per-provider health, which `oracle-status.sh` prints. `GET /state`, the route
tee-node folds into `/info`, carries the empty state.

### Verifying the contracts on the explorer

On an upstream deployment the sender and the store are Flare's, and verifying their source in the
explorer is part of that deployment. On a deployment of your own `deploy-oracle.sh` deployed
upstream's sources, unverified. Either way, before relying on one, read the addresses in
`deploy/config/<chain>/deployed-addresses.json` back: **Read Contract** (or `cast call`) should
show `extensionId()` returning the deployment's `EXTENSION_ID` and `instructionsSender()` pointing
at the sender. Once the feed is registered with FtsoV2, `getCurrentFeed()` can be called from a
browser with no tooling at all — the easiest way to hand someone a link that proves the feed is
real.

### What a passing run does and does not prove

| Proven | Not proven |
| --- | --- |
| The whole path: instruction → observation → signature → store → feed | The **below-par** path. Everything so far prints par, so the arithmetic that matters during a depeg has been exercised only in unit tests |
| The signature verifies against a registered PRODUCTION machine | The **cash source**, unless the run provisioned `hex_api_key` and `hex_cash_url`: without them the price is computed from bonds alone (`sourceHealth.hex_cash: not_configured`) |
| The configuration hashes bind the price to the published endpoints and admin set | **Sustained operation.** One submission is not a keeper on a timer |
| Consumers can read a price from the store's `getCurrentFeed()` | Reading via `FtsoV2.getFeedById`, which needs the `addCustomFeeds` governance proposal |

## When it goes wrong

| Symptom | Cause | Fix |
| --- | --- | --- |
| `requestFeedUpdate` reverts `FeedNotConfigured` | The feed has no endpoints or no admin set published. | Step 4. |
| `requestFeedUpdate` reverts `TeeIdNotConfigured` | A target machine was not sent the feed's latest publication of both kinds — usually because it registered after the publication. | Push it with the sender's `pushEndpoints` / `pushAdmins` (permissionless, payable; `get*PushTargets` lists the machines behind; no repo tool wraps them), or publish again. `fleet-status` shows which machines are behind. |
| Revert `0x3b06a1be` (`MessageEmpty()`) | The instruction carried an empty message. | The sender must send the ABI-encoded `FeedUpdateRequest`; findings §22. |
| Result `status 3`, log containing `context deadline exceeded` | The handler took longer than tee-node's **2 s** action budget. | Observation must stay off the request path — that is what `internal/observation/poller` is; findings §23. |
| Result `status 0`, log `error: extension is not provisioned` | No endpoints yet, a credential a provider URL names is missing, or no admin set. | Check `readiness` and `missingSecretRefs` in the readiness report. |
| Result `status 0`, log `error: no recent observation` | No successful observation inside `MaxObservationAge` (10 min). | Check `sourceHealth` in the readiness report; an upstream is failing. Working as designed — it fails closed. |
| Result `status 0`, log `error: machine clock is behind chain time` | The cached observation is more than 10 min older than the requesting block, while the machine's own clock says it is fresh. | Fix the machine's clock (NTP). |
| Result `status 0`, log `error: instruction carries an unexpected payload` | `originalMessage` was not an ABI-encoded `FeedUpdateRequest` naming this feed. | This extension serves exactly one feed and refuses requests for any other — check the sender's feed id against the build's (`protocol.FeedID`). |
| `submitFeedUpdates` reverts `StaleEndpoints` | A machine in the batch signed under a different configuration from the sender's `latestEndpointsHash(feedId)`. | Compare the readiness report's `endpointsHash` against the hash `send-endpoints` printed; a machine that rejected the publication is still on the previous one. |
| `submitFeedUpdates` reverts `StaleAdmins` | The same, for the admin set and `latestAdminsHash(feedId)`. | Compare the readiness report's `adminsHash` against the hash `send-admins` printed. |
| `submitFeedUpdates` reverts `NoEndpointsPublished` / `NoAdminsPublished` | Nothing of that kind has been published for the feed. | Step 4. |
| `submitFeedUpdates` reverts `WrongExtensionId` / `WrongFeedId` | A signed update names a different extension or feed than the store's. | Deployment mismatch — check `EXTENSION_ID` in `deploy/config/extension.env` against the store's `extensionId()`, and the build's `protocol.FeedID` against its `feedId()`. |
| `SET_SECRET` rejected right after a restart | A restart is a new machine: new key, new teeId, no configuration. | Register it, bring it up to date (step 3b), then build the envelopes again for the new teeId. |
| `submitFeedUpdates` rejects a signature | A signer is not a registered PRODUCTION machine on the store's extension, **or the extension is emergency paused** — `Fdc2Verification` rejects both. | Check `getTeeMachineStatus(<TEE_ID>)` is 2 and re-run `post-build.sh` if the machine was re-created; check the extension's pause state. |
| `connect: connection refused` on `localhost:6684` | A tool addressed the host-process stack's port while the Docker stack (6674) is the one running, or no stack is. | On the simulation, start the stack again so `deploy/config/stack.env` names it; elsewhere pass `-p http://localhost:6674` or fix `EXT_PROXY_URL`. The container's own 6664 is not published to the host. See "Which proxy port" above. |
| `submitFeedUpdates` reverts `NotNewer`, recorded `race_lost` | Another submission landed first, almost always another keeper's later request: the feed advanced. | **Not a fault**; never alert on it. A `NotNewer` while the store reads lower is recorded `rejected` instead. |
| Preflight `NotEnoughSignatures` | Fewer usable answers than the store's `requiredSignatures`. Every machine answering one request signs the same `observedAt`, so the missing ones refused, failed to decode, or never answered. | The record's `teeIds` says who made the batch, `errorClass` the first refusal, and `dropped` why an answer was left out; one that never answered cost the poll timeout. |
| `submitFeedUpdates` reverts `SpreadTooBig` | The two values bracketing the median disagree by more than the governance-set bound (`maxSpreadBIPS` / `maxSpreadAbsolute`). | The machines disagree about the price — compare their `prices` in the record and each machine's `sourceHealth` in its readiness report. |
| Registration stalls at status 1 | The availability proof needs the proxy reachable from outside. | Bring the public tunnel up and re-run `post-build.sh`. |
| Tools get `invalid character 'T'` or HTML back | `EXT_PROXY_URL` points at a dead tunnel. | Restart it. `post-build.sh` loads `.env` over the environment, so an inline `EXT_PROXY_URL=…` is discarded: use `post-build.sh --proxy <url>`, or call the tools with `-p`. |
| A restart kept the old identity | `start-services.sh` on a live chain recreated nothing, because the image and configuration were unchanged, so the process and its key carried on. | `stop-services.sh`, then `start-services.sh`. |
