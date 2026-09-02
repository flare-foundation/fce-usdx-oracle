# Go implementation

The default implementation. Selected with `LANGUAGE=go` in `.env` (also the fallback when `LANGUAGE` is unset).

This implementation embeds tee-node as a **library**, so the image runs a single static binary on a distroless base: ~22 MB, and bit-for-bit reproducible across machines (see [../REPRODUCIBILITY.md](../REPRODUCIBILITY.md)).

## Layout

```
cmd/
├── main.go             Standalone extension server (local dev)
├── docker/main.go      Combined tee-node + extension — the image entry point
├── devserver/main.go   Real provisioning, no chain — exercise SET_ENDPOINTS / SET_SECRET
├── mocknode/main.go    Stand-in for tee-node's /sign, /decrypt and /info
├── usdxprov/           Build, sign and submit provisioning envelopes
└── start-tee/main.go   Host-process runner backing `start-services.sh --local`
internal/
├── config/config.go    ★ Constants: op types, pinned chains and endpoints, timing bounds
├── extension/
│   ├── extension.go    ★ MAIN CUSTOMIZATION POINT — routing and handlers
│   └── utils.go        Infrastructure: actionHandler, buildResult
├── observe/            The observation timer and the staleness bound
├── oracle/             One observation: ordering, deadlines, the window — then Sign
├── price/              The price rule. Pure: no I/O, no clock, no config
├── supply/, backing/   The five upstream sources
├── decimal/            Exact parsing; never float64
├── runtimecfg/         What arrives after startup: RPC URLs and credentials
├── provision/          M-of-N envelope verification for SET_SECRET
├── report/             The signed payload's encoding
├── teenode/            Client for tee-node's /sign and /decrypt
└── failure/            The closed public error vocabulary
pkg/
├── server/server.go    Infrastructure: StartExtension (starts the observation loop too)
└── types/types.go      ★ Request, response and state types
```

★ = yours to change. Everything else is infrastructure — see the `DO NOT MODIFY` comments.

**Two constraints worth knowing before you edit a handler**, both found on-chain and both
written up in [../docs/usdx-oracle-findings.md](../docs/usdx-oracle-findings.md) §22–23:

- **An instruction's message may not be empty.** The registry rejects it with `MessageEmpty()`.
  `GET_FEED` carries an ABI-encoded `FeedUpdateRequest` naming the feed, and `GET_READINESS`
  carries the one-byte envelope `config.ReadinessRequestV1`; the handler requires exactly those
  shapes and rejects anything else.
- **A handler has 2 seconds to answer.** tee-node's client timeout is a const in its internal
  package. Anything slower must happen off the request path — which is what `internal/observation/poller`
  is: it observes on a timer, and the handler only signs.

## Develop

```bash
cd go && go build ./... && go test ./...
```

Or through the language-neutral entry point, from the repo root:

```bash
./scripts/test-unit.sh go
```

Run the extension alone (no tee-node, no proxy) on a port of your choosing:

```bash
EXTENSION_PORT=8080 go run ./cmd
```

## Add an operation

1. Add the constants to `internal/config/config.go`
2. Add request/response structs to `pkg/types/types.go`
3. Add a `case` to `processAction` and write the handler in `internal/extension/extension.go`
4. The on-chain entry point is the upstream `TeeOracleInstructionsSender`; its interface is
   vendored under `../contracts/interfaces/` — nothing is deployed from this repo

## Things specific to this implementation

**You must lock.** `Extension` holds mutable state guarded by `e.mu`; the framework does not serialize handler calls for you — `actionHandler` may run concurrently.

**`buildResult` owns the envelope.** Return through it rather than constructing an `ActionResult` by hand; it sets the `log` values the wire contract requires.

**ABI payloads** are decoded with the `abi.Arguments` declared in `internal/provisioning/store/abi.go`
(endpoints/admins) and the feed-update request decoder in `internal/wire/feedupdate`.

## Verify

```bash
go test ./internal/extension/ -run TestConformance
```

Replays the golden wire fixtures against this implementation. Run it before committing a handler change.
