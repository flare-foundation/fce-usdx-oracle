# Developing the extension

The repository root is the Go module. It embeds tee-node as a **library**, so the image runs a
single static binary on a distroless base, bit-for-bit reproducible across machines
(see [../REPRODUCIBILITY.md](../REPRODUCIBILITY.md)).

## Layout

```
cmd/
├── docker/         tee-node + extension in one process — the image entry point
├── usdxprov/       build, sign and submit a SET_SECRET envelope
└── dev/            everything that is not shipped in the image
    ├── devserver/  real provisioning and observation, no chain
    ├── mocknode/   stand-in for tee-node's /sign, /decrypt and /info
    └── start-tee/  host-process runner behind deploy/scripts/start-services.sh --local
internal/
├── config/         pinned facts: op hashes, chains, endpoints, timing bounds, health keys
├── failure/        the closed public error vocabulary
├── extension/      the HTTP surface: extension.go, router.go, result.go, one file per command
├── observation/    Sources, Binding, Observe, Sign — one observation start to finish
│   ├── poller/     the timer, the cache and the staleness bound
│   ├── supply/     totalSupply over N RPC providers, pinned to one block, Quorum-agreed
│   ├── backing/    Hex Trust's two reserve endpoints
│   ├── price/      min(1, backing/supply), pure arithmetic
│   └── decimal/    exact money parsing
├── wire/
│   ├── feedupdate/ the signed FeedUpdate layout the store verifies
│   └── readiness/  the signed readiness report
├── provisioning/
│   ├── envelope/   the SET_SECRET envelope and its M-of-N Verifier
│   └── store/      endpoints, admin sets and credentials — the only mutable state
└── teenode/        client for tee-node's local /sign and /decrypt
pkg/
├── server/         StartExtension: resolves the TEE identity, then starts the poller and the server
├── types/          the readiness payload shapes and the empty GET /state envelope
└── contracts/      abigen bindings for the upstream TeeOracleStructs ABI
```

## Develop

```bash
go build ./... && go vet ./... && go test ./...
```

Both modules at once, from the repo root:

```bash
go test ./... && (cd tools && go test ./...)
```

The highest-value local check is a real observation against live sources, with no chain and no
TEE: `./deploy/scripts/oracle-observe.sh` starts `cmd/dev/mocknode` and `cmd/dev/devserver`, provisions
them, asks for a price and tears both down.

## Two constraints worth knowing before you edit a handler

Both were found on-chain and are written up in
[usdx-oracle-findings.md](usdx-oracle-findings.md) §22–23:

- **An instruction's message may not be empty.** The registry rejects it with `MessageEmpty()`.
  `GET_FEED` carries an ABI-encoded `FeedUpdateRequest` naming the feed, and `GET_READINESS`
  carries the one-byte envelope `config.ReadinessRequestV1`; the handlers require exactly those
  shapes and echo nothing else back.
- **A handler has 2 seconds to answer.** tee-node's client timeout is a const in its internal
  package. Anything slower must happen off the request path — which is what
  `internal/observation/poller` is: it observes on a timer, and the handler only signs.

## Add a command

1. Add its hash to `internal/config/config.go`, beside the existing `OPCommand*` values.
2. Route it in `internal/extension/router.go` — in `processInstruction` if it arrives on-chain,
   in `processDirect` if it arrives through the proxy. The router a command is reachable from is
   its authorisation; do not add a shared default arm.
3. Write the handler in its own file under `internal/extension/`, taking only its decoded input
   and returning an `outcome`. The router assembles the result.
4. The on-chain entry point is the upstream `TeeOracleInstructionsSender`; its interface is
   compiled from a sibling flare-smart-contracts-v2 checkout (see `contracts/`) — nothing is deployed from this repo.

## Things specific to this implementation

**You must lock.** `Extension` holds mutable state guarded by `e.mu`; `actionHandler` may run
concurrently.

**Results go through `result`.** Handlers return an `outcome`; `result` builds the `ActionResult`
and sets the `log` values the wire contract requires. A handler never sees the action envelope, so
nothing from it can be echoed by accident.

**ABI payloads** are decoded with the `abi.Arguments` derived from the upstream ABI in
`internal/provisioning/store/abi.go` (endpoints, admins) and in `internal/wire/feedupdate` (the
feed-update request).

## Verify

```bash
go test ./internal/extension/ -run TestConformance
```

Replays the golden wire fixtures in `deploy/testdata/conformance/` against the extension through its own
handler. Run it before committing a handler change.

The GitLab pipeline (`.gitlab-ci.yml`, after tee-node's) runs on every push: both Go modules build,
`gofmt` and `go vet` gate, the extension's tests run with coverage and the tooling's without, every
operator script must parse and `check-versions.sh` must find the dependency pins aligned, and
the extension image is built with kaniko without pushing. The Foundry project is not built in CI:
its sources are upstream's, from a sibling checkout CI cannot reach; run `forge test` in
`contracts/` locally, and its wire vectors reach CI as the fixtures the conformance test replays. `golangci-lint` with tee-node's linter set
(`.golangci.yml`) gates, over both modules.
