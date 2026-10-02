# Developing the extension

The repository root is the Go module. It embeds tee-node as a **library**, so the image runs a
single static binary on a distroless base, bit-for-bit reproducible across machines
(see [../REPRODUCIBILITY.md](../REPRODUCIBILITY.md)).

## Layout

```
cmd/
├── extension-tee/  tee-node + extension in one process — the image entry point
└── dev/            everything that is not shipped in the image
    ├── devserver/  real provisioning and observation, no chain
    ├── mocknode/   stand-in for tee-node's /sign and /decrypt, and for the proxy's /info
    └── start-tee/  host-process runner behind deploy/scripts/start-services.sh --local
internal/
├── config/         the extension's settings: ports, deadlines and size bounds, the bonds URL and
│                   reserve metric names, the ids read from the environment
├── failure/        the closed public error vocabulary
├── extension/      the HTTP surface: extension.go (server), router.go (both routers), result.go,
│                   feed.go (GET_FEED), readiness.go (GET_READINESS), provisioning.go (the three SET_ commands)
├── observation/    Sources, Binding, Observe, Sign — one observation start to finish
│   ├── poller/     the timer, the cache, the staleness bound and the clock check
│   ├── supply/     totalSupply over N RPC providers, pinned to one block, Quorum-agreed
│   ├── backing/    Hex Trust's two reserve endpoints
│   ├── price/      min(1, backing/supply), pure arithmetic
│   └── decimal/    exact money parsing
├── provisioning/
│   ├── envelope/   the M-of-N Verifier over the SET_SECRET envelope
│   └── store/      endpoints, admin sets and credentials, and Open, which turns them into sources
└── teenode/        client for tee-node's local /sign and /decrypt
pkg/                what the tooling imports
├── protocol/       the feed id, op tags, credential refs, admin roles and policy, the chain set
├── wire/
│   ├── feedupdate/    the signed FeedUpdate layout the store verifies, and the GET_FEED request
│   ├── readiness/     the signed readiness report
│   ├── configpayload/ the Endpoints and Admins payloads, their published hashes, the PRIVATE-URL commitment
│   ├── envelope/      the SET_SECRET envelope: body, signatures, ciphertext
│   └── eip191/        personal-sign recovery, shared by the envelope verifier and the tooling
├── server/         StartExtension: resolves the TEE identity, then starts the poller and the server
├── types/          the readiness report's shapes and source-health vocabulary, and the empty GET /state envelope
└── contracts/      abigen bindings for the upstream TeeOracleStructs ABI
```

The tooling module (`tools/`) imports `pkg/` and nothing under `internal/`. That is a
convention, not a compiler rule: `tools/`'s module path, `fce-usdx-oracle/tools`, sits under the
root's, so Go's `internal` restriction does not stop it. Keep anything the tooling needs in
`pkg/`.

## Develop

```bash
GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...
```

Both modules at once, from the repo root:

```bash
GOWORK=off go test ./... && (cd tools && GOWORK=off go test ./...)
```

`GOWORK=off` matters when a `go.work` is present. `start-services.sh` writes one (git-ignored)
for `USE_LOCAL_SIBLINGS=1`, and it points host builds at the sibling tee-node and tee-proxy
checkouts instead of the versions `go.mod` pins, silently.

The highest-value local check is a real observation against live sources, with no chain and no
TEE: `./deploy/scripts/oracle-observe.sh` starts `cmd/dev/mocknode` and `cmd/dev/devserver`, provisions
them, asks for a price and tears both down.

## Two constraints worth knowing before you edit a handler

Both were found on-chain and are written up in
[usdx-oracle-findings.md](usdx-oracle-findings.md) §22–23:

- **An instruction's message may not be empty.** The registry rejects it with `MessageEmpty()`.
  `GET_FEED` carries an ABI-encoded `FeedUpdateRequest` naming the feed, and `GET_READINESS`
  carries the one-byte envelope `protocol.ReadinessRequestV1`; the handlers require exactly those
  shapes and echo nothing else back.
- **A handler has 2 seconds to answer.** tee-node's client timeout is a const in its internal
  package. Anything slower must happen off the request path — which is what
  `internal/observation/poller` is: it observes on a timer, and the handler only signs. Each
  call to tee-node's signing service is bounded at 1 s (`signTimeout` in `pkg/server`) to stay
  inside that budget.

## Add a command

1. Add its bytes32 to `pkg/protocol/protocol.go`, beside the existing `OPCommand*` values
   (`teeutils.ToHash("NAME")`). A command that travels on chain must match the upstream sender's
   constant exactly, or the instruction is priced on the wrong fee row and rejected.
2. Route it in `internal/extension/router.go`, in exactly one router: `processInstruction` if it
   arrives on chain, `processDirect` if it arrives through the proxy. The router a command is
   reachable from is its authorisation; do not add a shared default arm.
3. Add it to the **other** router's `WrongActionType` arm, so the wrong channel answers with a
   named refusal rather than an unknown command, and to the list `unsupportedCommand` prints.
4. Write the handler in `internal/extension/`, taking only its decoded input and returning an
   `outcome`; the router assembles the result. A refusal is a `failure.Failure`: if none fits,
   declare one in `internal/failure` and add it to `failure.All`.
5. Pin the routing with tests beside `TestSetSecret_RejectedOverInstructionChannel` in
   `internal/extension/provisioning_test.go`, and update
   [extension-contract.md](extension-contract.md) if the wire contract changes.

The on-chain entry point is the upstream `TeeOracleInstructionsSender`; its interface is compiled
from a sibling flare-smart-contracts-v2 checkout (see `contracts/`). `contracts/` deploys
nothing in production: its `script/DeployTeeOracle.s.sol`, behind
`deploy/scripts/deploy-oracle.sh`, deploys upstream's contracts for the local simulation and
testnets only.

## Things specific to this implementation

**Lock in the component, not the handler.** `net/http` serves every request on its own
goroutine, so `actionHandler` runs concurrently and nothing serialises actions. `Extension` holds
no mutable state; each component locks its own:

- `store.Store` takes `applyMu` across every `Apply*` call, so one provisioning operation runs at
  a time and the verifier's unsynchronised version map is covered, and `mu` over the data fields.
  `mu` is never held across a tee-node round trip, so a hung `/decrypt` cannot freeze the
  readiness report. The order is `applyMu`, then `mu`.
- `poller.Poller` guards the cached observation, its health map and the counters with `mu`.
- `teenode.Client` guards the cached machine address with `mu`.

State added to a handler needs its own lock on the same terms.

**Results go through `result`.** Handlers return an `outcome`; `result` builds the `ActionResult`
and sets the `log` values the wire contract requires. A handler never sees the action envelope
(`GET_READINESS` receives only the action id it binds into its report), so nothing from it can be
echoed by accident.

**ABI payloads.** The `SET_ENDPOINTS` and `SET_ADMINS` payloads are decoded by
`pkg/wire/configpayload`, whose `abi.Arguments` come from the abigen binding of upstream's
`TeeOracleStructs` ABI (`pkg/contracts/teeoraclestructs`); `internal/provisioning/store/abi.go`
only maps its error onto the store's own. The feed update, the `GET_FEED` request, the readiness
body and the provisioning envelope are hand-declared argument lists in their `pkg/wire` packages.
The layouts that reach the chain are pinned to Solidity's own bytes in `deploy/testdata/wire`.

## Verify

```bash
GOWORK=off go test ./internal/extension/ -run TestConformance
```

Replays the golden wire fixtures in `deploy/testdata/conformance/` against the extension through
its own handler, in-process. Run it before committing a handler change, with `EXTENSION_ID`
unset: the tests default it to 1, and an exported value (from a sourced `.env`, for example)
changes the bytes fixtures 01 and 02 pin.

The GitLab pipeline (`.gitlab-ci.yml`, after tee-node's) runs on every branch push, merge
request and tag. A branch with an open merge request runs as the merge request's pipeline only.
Every pipeline:

- builds both Go modules;
- gates on `gofmt` and `go vet` (both modules) and on `golangci-lint` with tee-node's linter set
  (`.golangci.yml`, both modules);
- runs both modules' tests under the race detector (`-race`), the extension's with coverage and
  `TestConformance` among them;
- checks that every operator script parses (`bash -n`) and that `check-versions.sh` finds the
  dependency pins aligned.

No push pipeline builds an image, so a broken `Dockerfile` shows only in a local build or at
the next tag. A tag pipeline, after those jobs pass, builds both images with BuildKit and
publishes them to `europe-west1-docker.pkg.dev/flare-network-staging/containers` (`tee-usdx` for
the extension, `fce-usdx-keeper` for the keeper); `record-images` reads them back and exports
`TEE_IMAGE` and `EXPECTED_CODE_HASH`, the image's config digest; and the manual
`verify-reproducible` job rebuilds the extension cold and fails unless the config digests match
(see [../REPRODUCIBILITY.md](../REPRODUCIBILITY.md)). On GitHub,
`.github/workflows/repro-build.yml` does the same for the extension image on every tag: it
builds it reproducibly, publishes it to ghcr.io and fails unless a cold rebuild is identical.

The Foundry project is not built in CI: its sources are upstream's, from a sibling checkout CI
cannot reach. Run `forge test` in `contracts/` locally; its wire vectors reach CI as the fixtures
the Go tests assert against.
