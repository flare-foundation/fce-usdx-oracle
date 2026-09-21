# Testing

Tests split into two groups: those covering **your extension**, and those covering the **deployment tooling**. Cheapest first — only the last needs a chain.

## Your extension

| Layer | What it tests | How to run | Chain? |
|-------|--------------|------------|--------|
| **Unit** | Your handlers, dispatch, encoding — in your chosen language | `go test ./...` in the root, then in `tools/` | no |
| **Conformance** | The wire contract, against golden fixtures | `go test ./internal/extension/ -run TestConformance` | no |
| **End-to-end** | The full feed cycle (request → observe → sign → relay) | `./deploy/scripts/oracle-cycle.sh -tee <machine>` | yes |


### Conformance testing

This is the layer that pins the wire contract: the exact envelope, status codes and `GET /state` body the extension serves, independent of any source or chain.

The conformance replay starts **only the extension process** — no tee-node, no proxy, no chain, no Docker — replays the 16 fixtures in `deploy/testdata/conformance/`, and diffs every response field. The response payload is compared **byte-for-byte**, because tee-node hashes it and signs the result.

```bash
go test ./internal/extension/ -run TestConformance
```

It runs in seconds, which is what makes writing a new language port tractable: a correctness signal without a full deploy cycle.

The fixtures are **order-dependent** and share one process — counters accumulate across cases and the final fixture asserts the resulting state. `index.json` fixes the order.

If you change a request or response shape, edit the fixture and let `TestConformance` confirm the extension produces it. The two fixtures that carry a signed payload (`01`, `02`) are produced by the test's own fixed observation, so a payload-layout change is regenerated from Go, not hand-assembled.

> When this suite disagrees with your implementation, check [extension-contract.md](extension-contract.md) before changing the fixture. The contract is normative; the fixtures encode it.

## The deployment tooling

`tools/` is its own Go module. It imports the extension's wire packages rather than restating them, and adds the operator's half on top. It has its own tests.

| Layer | What it tests | How to run |
|-------|--------------|------------|
| **Unit tests** | Revert decoding, feed-update decoding, payload encoding, state file I/O, env parsing, validation, report formatting, the keeper cycle | `cd tools && go test ./...` |

There are no longer any contract-deploying integration tests: nothing is deployed locally — the
sender and the store are upstream (flare-smart-contracts-v2) and verified by Flare. The
on-chain path is exercised end to end instead, by `oracle-cycle.sh` / the `keeper` against the
running stack (see below).

### Tooling unit tests

These require no external services. They cover:

- **Revert reason decoding** (`tools/pkg/fccutils/revert_test.go`) — Verifies `decodeRevertHex` and `DecodeRevertReason` correctly decode ABI-encoded `Error(string)` reverts, including the legacy revert strings of the old locally-deployed sender. Also tests edge cases: nil errors, wrapped errors, custom error selectors, invalid hex, short data.
- **Support revert decoding** (`tools/pkg/support/support_test.go`) — Tests `decodeRevertFromError` which extracts revert reasons from go-ethereum JSON-RPC error types.
- **State file I/O** (`tools/pkg/fccutils/registration_test.go`) — Tests `loadState`/`saveState` for the TEE machine registration resume flow: missing files, valid/invalid JSON, overwrite behavior, roundtrip consistency, read-only directories.
- **Validation checks** (`tools/pkg/validate/checks_test.go`) — Extension env format validation, deployer key source detection, service/registration/TEE check functions with various config states.
- **Report formatting** (`tools/pkg/validate/report_test.go`) — Report summary, JSON output, colored terminal output, empty reports, unknown statuses.
- **Validation primitives** (`tools/pkg/validate/validate_test.go`) — `AddressNotZero`, `AddressHasCode`, `KeyHasFunds`, `IsUsingDevKey` with nil clients, zero addresses, edge cases.

```bash
cd tools && go test ./... -v
```

### Pre-flight validation

The `tools/pkg/validate` checks are covered by the unit tests above: `AddressHasCode` against
deployed registry contracts and random EOAs, `KeyHasFunds` against funded and unfunded keys, and
the deployer-key source detection the registration flow relies on.

## End-to-End Tests

For the whole thing on a local Hardhat simulation — oracle contracts included — see
[local-e2e.md](local-e2e.md). Against an existing deployment, after `full-setup.sh` completes
(services up, machine registered), drive one full cycle against the running stack:

```bash
./deploy/scripts/oracle-cycle.sh -tee <machine>
```

Add `--dry-run` to stop after the preflight (send nothing), or `--loop 10m` to keep cycling on a
timer — the same code path the unattended keeper runs.

> ⚠ `full-setup.sh --chain <name>` **overwrites `.env`** with `.env.<name>`. The individual
> scripts only read it. Prefer them if your `.env` holds anything you care about.

---

# Running the USDX oracle end to end yourself

The whole chain is: **request → TEE observes → signed report → store → feed.** This section
walks it from a clean tree to a price on Coston2, with what to check at each step and what the
failures actually look like.

## 0. What must be true before you start

| | Check |
| --- | --- |
| Docker stack up | `docker compose -f deploy/docker-compose.yaml -f deploy/docker-compose.live.yaml ps` — `extension-tee`, `ext-proxy`, `redis` |
| Proxy reachable **from the internet** | `curl -s $EXT_PROXY_URL/info \| jq .machineData.extensionId` returns your extension id (the reserved id 1), not an ngrok error page |
| Operator key funded | Coston2 C2FLR on the address in `.env` |
| `deploy/config/extension.env` current | Holds the pinned `EXTENSION_ID` (the reserved id 1). Contract addresses come from `deploy/config/coston2/deployed-addresses.json` — the `TeeOracleFeedStore` entry must be filled in from the flare-smart-contracts-v2 deployment; the sender is then derived from the store's `instructionsSender()` |

### Which proxy port

The tools default to **`http://localhost:6674`**, which is what Docker Compose publishes
(`6674 -> 6664`; the container itself listens on 6664). Under the default deployment they run
flag-free.

On the simulation the scripts and tools address whichever stack `start-services.sh` brought up
last, compose or host processes, through `deploy/config/stack.env`; nothing needs `-p`. One case
still does:

- **Reaching the machine over its public URL** — pass the tunnel address.

`go run` does not read `.env`, so an `EXT_PROXY_URL` set there is not in scope for a
hand-run tool; export it in the shell instead:

```bash
export EXT_PROXY_URL=http://localhost:6674
```

A wrong port shows up as:

```
deriving TEE identity from http://localhost:6684/info: connect: connection refused
```

`post-build.sh` reads the recorded stack (Docker's 6674 vs the host stack's 6684) and sets this itself,
so it is unaffected. Registration is the one step that genuinely needs the **public** URL,
because data providers verify the availability proof from outside.

The proxy really does need to be publicly reachable: machine registration ends with an
availability proof that data providers verify, and they cannot reach `localhost`. If your
tunnel is down, registration stalls at status 1 and nothing downstream works.

## 1. The fast checks — no chain, no Docker

Run these first. They catch most breakage in seconds.

```bash
go test ./... && (cd tools && go test ./...)
```

```bash
go test ./internal/extension/ -run TestConformance
```

```bash
cd contracts && forge test
```

## 2. Prove a real observation locally

This is the highest-value local test: the real observation path against **live** Flare,
Ethereum, Songbird and Hex — no chain, no TEE, no credentials. One script runs the whole thing:

```bash
./deploy/scripts/oracle-observe.sh
```

It starts a mock signing node and the dev server on high ports, provisions them with an endpoint
configuration and the dev-only admin set, requests a feed update, waits out the first
observation, and tears both down.

To run it by hand instead — three terminals, or background the first two:

```bash
SIGN_PORT=9099 go run ./cmd/dev/mocknode
```

```bash
EXTENSION_PORT=8099 SIGN_PORT=9099 go run ./cmd/dev/devserver
```

```bash
./deploy/scripts/set-config.sh endpoints deploy/config/endpoints/public-only.json 8099
```

Observation needs only the endpoints, but a signed feed update also needs an admin set: the
update commits to one, and the store rejects an update that names none. The dev-only set is
built from well-known keys and must never be published to a live feed:

```bash
./deploy/scripts/set-config.sh admins deploy/config/admins/dev-wellknown-keys.json 8099
```

Then ask for a feed update. `originalMessage` is the ABI-encoded `FeedUpdateRequest` — the
21-byte feed id in a single 32-byte word; a wrong envelope is rejected (findings §22):

```bash
python3 -c "import json;b32=lambda s:'0x'+s.encode().ljust(32,b'\x00').hex();df={'instructionId':'0x'+'c1'*32,'teeId':'0x'+'22'*20,'timestamp':1785945000,'rewardEpochId':0,'opType':b32('TEE_ORACLE'),'opCommand':b32('GET_FEED'),'cosigners':[],'cosignersThreshold':0,'originalMessage':'0x21555344582f5553440000000000000000000000000000000000000000000000','additionalFixedMessage':'0x'};print(json.dumps({'data':{'id':'0x'+'c1'*32,'type':'instruction','submissionTag':'submit','message':'0x'+json.dumps(df).encode().hex()},'additionalVariableMessages':[],'timestamps':[],'additionalActionData':'0x','signatures':[]}))" > /tmp/getfeed.json
```

```bash
curl -sS -X POST -H 'Content-Type: application/json' --data-binary @/tmp/getfeed.json http://127.0.0.1:8099/action | jq '{status, log}'
```

```bash
# the machine's account of itself: its signed readiness report, asked of the dev server directly
(cd tools && go run ./cmd/readiness -extension http://127.0.0.1:8099) \
  | jq '{observationAge, wouldSign, sourceHealth, readiness}'
```

The first request is served from whatever the boot-time observation produced; the price appears
once the poller ticks again. Observation runs on a background timer, never on the request path —
a `GET_FEED` costs one signature and nothing more, which is what keeps it inside tee-node's 2 s
action budget.

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
key and the cash URL is provisioned through `SET_SECRET`. The
feed still publishes at par, because a bonds-only price that already reaches par is provably
what the full computation would give (findings §21). It will *not* publish anything below par.

## 3. Bring up the stack on Coston2

Nothing here is deployed: the sender and the store are upstream (flare-smart-contracts-v2) and
already on-chain, and the extension id is the reserved 1 — a build fact pinned in
`internal/config`, registered by Flare governance, never minted or discovered at deploy
time. This step is only needed the first time, or after changing extension code.

**a. Full setup** — pins `EXTENSION_ID=1` into `deploy/config/extension.env`, starts the TEE + proxy,
and runs post-build registration (`allow-tee-version` → `set-governance` → `register-tee`),
waiting for the availability proof:

```bash
./deploy/scripts/full-setup.sh --chain coston2
```

**b. After a code change** — a new image means a restart, and a restart means a **new TEE
identity**, because the enclave key does not survive one:

```bash
./deploy/scripts/stop-services.sh --chain coston2 && ./deploy/scripts/start-services.sh --chain coston2
```

Use `stop` then `start`, not `start` alone. `stop-services.sh` runs `docker compose down`,
which removes redis — where tee-node persists its identity and machine data. Without that, a
restart silently keeps the **old** identity even though the container env says otherwise.

Then re-run registration for the new identity:

```bash
./deploy/scripts/post-build.sh
```

**Verify before going on.** The store requires status **2**; anything less is rejected:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc 0x1a9C4A0f9D76c0b1D91d22E24E573a9b377618aE 'getTeeMachineStatus(address)(uint8)' <TEE_ID>
```

Your `<TEE_ID>` is derived from the attested public key — `register-tee` prints it at
registration, or:

```bash
curl -s $EXT_PROXY_URL/info | jq -r '"\(.teeInfo.publicKey.x)\(.teeInfo.publicKey.y)"'
```

## 4. Publish the endpoint configuration

Costs gas and sets the sender's `latestEndpointsHash` for the feed, so validate
the payload locally first (step 2) — `send-endpoints` only checks that it is valid JSON, and a
payload the extension rejects will leave every later `submitFeedUpdates` reverting
`StaleEndpoints`:

```bash
cd tools && go run ./cmd/send-endpoints -instructionSender <SENDER> -config ../deploy/config/endpoints/public-only.json
```

The tool reads `endpointsVersion(feedId)`, predicts the version the sender will assign
(current + 1), and prints the predicted `endpointsHash` — the value the machine must report
before its updates will be accepted. Direct submission works only while the caller is still
governance (before the switch to production mode); on a production deployment the same
calldata goes through the Flare governance timelock instead, and the tool's encoded call is
the artefact to hand over. Add `-dry-run` to validate and print the prediction without
sending, and `-claimBack` to name the refund address for the instruction fee.

Then publish the admin set, which decides who may install credentials in that machine. The
store requires both hashes, so a machine with endpoints but no admin set still cannot land an
update — it reverts `NoAdminsPublished`:

```bash
cd tools && go run ./cmd/send-admins -instructionSender <SENDER> -config ../deploy/config/admins/dev-wellknown-keys.json
```

### Handing the Hex credential to the machine

The credential holder does not need this repo's deployment context, only the proxy URL
and a few secrets. One script covers the whole flow:

```bash
./deploy/scripts/upload-hex-key.sh
```

It reads `.env` first: `HEX_API_KEY` is the credential, and with `HEX_CASH_URL` and
`HEX_CASH_SALT` set it also uploads `hex_cash_url`, the commitment preimage of the
restricted cash endpoint, which the machine needs alongside the key before the cash
figure counts. On the recorded local stack the proxy URL, chain id, API key and the dev
backing admin key default as well, so the run is unattended; elsewhere it prompts for
whatever is unset, hidden for secrets (`PROXY_URL`, `CHAIN_ID`, `EXPECTED_CODE_HASH`,
`USDXPROV_KEY`, `USDXPROV_API_KEY`).

Three steps: verify the machine, which also yields the attested key everything is
encrypted to; then, per credential, encrypt, sign once and submit over the direct channel,
waiting for the machine's own verdict on each (`usdxprov submit -wait`, so "applied" means
the enclave holds it, not that the proxy took it); then confirm from the machine's
published state. Verification gates the rest — if it fails, nothing is sent. The URL goes
before the key, because the machine accepts it only against the published commitment:
`publish-local.sh endpoints` makes that commitment from the same two variables, and
`usdxprov commit` prints it for a hand-written configuration.

**Why the verification step is not optional.** The next step encrypts a credential to a
public key the proxy served. A proxy that lied about that key would receive the plaintext.
Reading a code hash from the same proxy proves nothing on its own, so `verify-tee` closes
the loop against the chain instead:

```bash
cd tools && go run ./cmd/verify-tee -p <PROXY_URL> -code-hash 0x<EXPECTED>
```

It parses the code hash out of the **attestation** (not the self-reported JSON), derives
the teeId from the **attested public key**, then checks on-chain that the derived teeId is
registered at PRODUCTION and that its extension accepts that code hash on that platform.
Substitute the key and the derived teeId stops matching a registered machine; substitute
the attestation and the code hash stops matching what governance accepts. Pass
`-code-hash` obtained **out of band** — without it the tool only proves the machine is
*some* accepted machine, not that it runs the build you reviewed, and it says so.

**Secrets never go in argv.** `usdxprov` reads its three credentials from `USDXPROV_VALUE`,
`USDXPROV_KEY` and `USDXPROV_API_KEY`, and warns if you use the equivalent flag — a value
in argv is readable by any other local process through `ps` and lands in shell history.

Only one signature is needed here: `hex_api_key` falls under the `backing` set, which is
1-of-N. A provider credential would need two distinct signers from the `providers` set.

### Handing the RPC credentials to the machine

The RPC keys are a separate upload, by a separate party, under a separate admin set:

```bash
./deploy/scripts/upload-rpc-keys.sh
```

Same five steps, three differences that follow from the split:

- **Several credentials.** Every `{secret}` named in the published endpoint configuration is
  its own ref, its own envelope and its own version. Which refs get uploaded comes from the
  **machine** — it publishes `missingSecretRefs` — so the script cannot disagree with the
  configuration actually applied. Each credential is read from an environment variable named
  after its ref: `ankr_flare_key` → `ANKR_FLARE_KEY`.
- **Two signatures.** The `providers` set is at least 2-of-N, so one laptop usually cannot
  finish. The script signs with every key in `USDXPROV_KEYS` (comma-separated), submits if it
  has enough, and otherwise **hands off**: the envelope goes to `OUT_DIR` for the next admin
  to sign and submit. It holds ciphertext and signatures only, so it is safe to send over any
  channel — but it expires one hour after it was built.
- **Rotation is explicit.** The default uploads only what the machine says is missing, which
  makes a re-run after a partial failure safe. `--all` re-uploads every ref named in
  `ENDPOINTS_FILE` — that is the rotation path, and the only one that needs a local copy of
  the configuration, because the readiness report names what is *missing* and never the full list.

```bash
./deploy/scripts/upload-rpc-keys.sh --refs ankr_eth_key      # one ref
./deploy/scripts/upload-rpc-keys.sh --all                    # rotate everything
```

For a local mocknode, which has no attestation and no on-chain registration, both scripts
take `--no-verify`. Never use it against a machine that will hold a real credential: it turns
off the only check that the key being encrypted to belongs to an enclave at all.

> Everything in this section uses throwaway keys anyone can sign with. For a live feed, follow
> [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md): same commands, but with
> the keys genuinely separated across parties and the multi-admin handoff spelled out.

`dev-wellknown-keys.json` names the addresses of private keys 1, 2 and 3, so anyone can sign
as those admins. It is for local and testnet work only. It publishes two sets: `backing`
(1-of-1, governing `hex_api_key`) and `providers` (2-of-3, governing the RPC credentials).
`send-admins` prints both, with the credentials each governs — check that before spending gas. Both payloads are memory-only in the
extension, so both must be republished after every restart — and `SET_ADMINS` must land
before any `SET_SECRET`, since with no admin set installed nothing can authorise a credential.

Confirm the machine actually applied it — this is the difference between what was *sent* and
what is *running*:

```bash
docker compose -f deploy/docker-compose.yaml -f deploy/docker-compose.live.yaml logs extension-tee | grep SET_ENDPOINTS
```

Expect `SET_ENDPOINTS applied`, and `SET_ADMINS applied` for the admin set. The `endpointsHash` that `send-endpoints` printed must equal the
`endpointsHash` in the machine's readiness report.

## 5. Request a feed update and put it on-chain

The cheap check first — preflight every store guard against the addressed machines, sending
nothing:

```bash
./deploy/scripts/oracle-cycle.sh -tee <MACHINE> --dry-run
```

Then the real thing. The keeper requests, polls with a bounded deadline, decodes, preflights
every store guard, and submits:

```bash
./deploy/scripts/oracle-cycle.sh -tee <MACHINE>
```

(or the binary directly: `cd tools && go run ./cmd/keeper -tee <MACHINE> -once`)

Add `--loop 10m` to stay up and cycle on a timer — the same code path the unattended keeper
runs — and `--records <file>` to append the JSON cycle records somewhere other than stdout.

## 6. Verify what a consumer sees

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <STORE> 'getCurrentFeed()(uint256,int8,uint64)'
```

`(100000000, 8, <recent unix ts>)` is a healthy feed at par. The store implements the FTSO
custom-feed interface itself — there is no separate feed adapter — so this is exactly what
consumers read, and what `FtsoV2.getFeedById` returns once the feed is registered by
governance (`addCustomFeeds`), a timelocked proposal, not a deploy step. Reading the value is
a paid read like every other FTSO feed: `calculateFee` resolves through the central
`FeeCalculator`, FtsoV2 forwards exactly that fee, and `getCurrentFeed` forwards it to the
governance-set `feeDestination`.

## 7. Reading the result

Four places tell you different things. Start at the bottom of the stack and work back only if
something looks wrong. `./deploy/scripts/oracle-status.sh` reads all four without sending anything.

**The consumer surface** — what anyone reading the feed sees:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <STORE> 'getCurrentFeed()(uint256,int8,uint64)'
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
| timestamp `0` | No feed update has ever landed — the store holds no data yet. Treat as no data |
| timestamp not advancing | The feed froze. **This is the designed failure mode**, not an outage: check the age, do not assume the last price is current |

**Always check the age.** The feed cannot revert — `FtsoV2` forwards the call, so a revert would
take down every consumer at once — which means a stale price is returned looking exactly like a
fresh one. The timestamp is the only signal:

```bash
cast call --rpc-url https://coston2-api.flare.network/ext/C/rpc <STORE> 'observedAt()(uint64)'
```

**The store's own view**, for operators — `observedAt()`, `extensionId()`,
`instructionsSender()` and `feeDestination()`. The value and decimals themselves have no free
getter — reading them is the paid `getCurrentFeed` read above. The thread back to the TEE is
the `teeIds` list in the store's `FeedUpdated` events, one entry per contributing machine.

**The signed update behind it.** A dry run requests, polls and decodes exactly like the real
thing, then stops at the preflight instead of submitting, and emits its cycle record:

```bash
./deploy/scripts/oracle-cycle.sh -tee <MACHINE> --dry-run
```

The record names each contributor's price, the batch's `observedAt`, the instruction id, and the
`teeIds` recovered from the updates' signatures. The preflight checks both configuration
commitments and rehearses the submission with an `eth_call` on the way, so a cycle that reaches
`submitOutcome: dry_run` proves the machines computed their updates under the endpoints and admin
sets the sender currently publishes, and that the store would accept the batch.

Relaying an update the store already holds ends in
`NotNewer: the store already holds observedAt=…`. **That is the correct answer**, not a
failure: the store takes strictly increasing timestamps, which is what makes replay and
out-of-order delivery harmless.

**The machine's own health** is its signed readiness report (`GET_READINESS`): observation age,
counters, error class and per-source health, which `oracle-status.sh` prints. `GET /state`, the
route tee-node folds into `/info`, carries the empty state.

### Verifying the contracts on the explorer

The sender and the store are upstream (flare-smart-contracts-v2), and verifying their source
in the explorer is Flare's job as part of that deployment — nothing in this repo is deployed
locally, so there is nothing here to verify. Before relying on a deployment, check that the
`TeeOracleFeedStore` and `TeeOracleInstructionsSender` addresses in
`deploy/config/<chain>/deployed-addresses.json` show verified source, and read them back: **Read
Contract** (or `cast call`) should show `extensionId()` returning the reserved id 1 and
`instructionsSender()` pointing at the sender. Once the feed is registered with FtsoV2,
`getCurrentFeed()` can be called from a browser with no tooling at all — the easiest way to
hand someone a link that proves the feed is real.

### What a passing run does and does not prove

| Proven | Not proven |
| --- | --- |
| The whole path: instruction → observation → signature → store → feed | The **below-par** path. Everything so far prints par, so the arithmetic that matters during a depeg has been exercised only in unit tests |
| The signature verifies against a registered PRODUCTION machine | The **cash source**. It has never been reachable; par is computed cash-less |
| The configuration hashes bind the price to the published endpoints and admin set | **Sustained operation.** One submission is not a keeper on a timer |
| Consumers can read a price from the store's `getCurrentFeed()` | Reading via `FtsoV2.getFeedById`, which needs the `addCustomFeeds` governance proposal |

## When it goes wrong

Everything here was hit for real while bringing this up.

| Symptom | Cause | Fix |
| --- | --- | --- |
| `requestFeedUpdate` reverts `TeeIdNotConfigured` | A target machine does not have both endpoints and admins published for the feed. | Run `send-endpoints` and `send-admins` for it; `fleet-status` shows which commitment is missing. |
| Revert `0x3b06a1be` (`MessageEmpty()`) | The instruction carried an empty message. | The sender must send the ABI-encoded `FeedUpdateRequest`; findings §22. |
| Result `status 3`, log `context deadline exceeded` | The handler took longer than tee-node's **2 s** action budget. | Observation must stay off the request path — that is what `internal/observation/poller` is; findings §23. |
| Result `status 0`, log `no recent observation` | The background loop has not produced anything inside `MaxObservationAge` (10 min). | Check `sourceHealth` in the readiness report; an upstream is failing. Working as designed — it fails closed. |
| Result `status 0`, log `instruction carries an unexpected payload` | `originalMessage` was not an ABI-encoded `FeedUpdateRequest` naming this feed. | This extension serves exactly one feed and refuses requests for any other — check the sender's feed id matches the build's (`internal/config`). |
| `submitFeedUpdates` reverts `StaleEndpoints` | A machine in the batch is running a different config than the sender's `latestEndpointsHash`. | Compare the readiness report's `endpointsHash` against the hash `send-endpoints` printed; re-send if they differ. |
| `submitFeedUpdates` reverts `NoAdminsPublished` | No admin set has been published for the feed yet. | Run `send-admins` for it. |
| `submitFeedUpdates` reverts `StaleAdmins` | A machine in the batch is running a different admin set than the sender's `latestAdminsHash`. | Compare the readiness report's `adminsHash` against the hash `send-admins` printed; re-send if they differ. |
| `submitFeedUpdates` reverts `WrongExtensionId` / `WrongFeedId` | A signed update names a different extension or feed than the store's. | Deployment mismatch — check the extension id and feed id pinned in the build (`internal/config`) against the store's `extensionId()`. |
| `SET_SECRET` rejected right after a restart | The admin set is memory-only and was lost. | Re-run `send-admins`, then re-submit the envelope. |
| `submitFeedUpdates` rejects a signature | A signer is not a registered PRODUCTION machine on the store's extension, **or the extension is emergency paused** — `Fdc2Verification` rejects both. | Check `getTeeMachineStatus(<TEE_ID>)` is 2 and re-run `post-build.sh` if the machine was re-created; check the extension's pause state. |
| `connect: connection refused` on `localhost:6684` | Running under `--local`, where the proxy binds 6684 directly. (Before 6 Aug 2026 this was the default and Docker users hit it instead.) | `-p http://localhost:6664`, or export `EXT_PROXY_URL`. See "Which proxy port" above. |
| `submitFeedUpdates` reverts `NotNewer` | A newer update already landed. | **Not a fault** — with ≥2 keepers exactly one submission wins per cycle. |
| `submitFeedUpdates` reverts `NoEndpointsPublished` | No `setEndpoints` has ever been published for the feed. | Step 4. |
| Preflight `NotEnoughSignatures` | Fewer machines agreed on one `observedAt` than the store's `requiredSignatures`. | The record's `teeIds` says who made the batch; a machine that stamped a different instant is left out, and one that never answered cost the poll timeout. |
| `submitFeedUpdates` reverts `SpreadTooBig` | The values bracketing the median disagree by more than the governance-set bound. | The machines disagree about the price — compare their `prices` in the record and each machine's `/state` `sourceHealth`. |
| Registration stalls at status 1 | The availability proof needs the proxy reachable from outside. | Bring the public tunnel up and re-run `post-build.sh`. |
| Tools get `invalid character 'T'` or HTML back | `EXT_PROXY_URL` points at a dead tunnel. | Restart it. Note `post-build.sh` does `source .env`, so an inline `EXT_PROXY_URL=…` override is discarded — edit `.env` or call the tools directly with `-p`. |
| Restart kept the old identity | redis persisted tee-node's machine data. | `stop-services.sh` (which runs `down`) before starting. |

