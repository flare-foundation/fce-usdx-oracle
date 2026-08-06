# Go implementation

The default implementation. Selected with `LANGUAGE=go` in `.env` (also the fallback when `LANGUAGE` is unset).

Go is the only language that can embed tee-node as a **library**, so this image runs a single static binary on a distroless base: ~22 MB, and bit-for-bit reproducible across machines. Every other language runs tee-node as a separate process. See [../docs/languages.md](../docs/languages.md) for the trade-offs.

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
  GET_PRICE carries a one-byte envelope, `config.GetPriceRequestV1`, and the handler requires
  exactly that.
- **A handler has 2 seconds to answer.** tee-node's client timeout is a const in its internal
  package. Anything slower must happen off the request path — which is what `internal/observe`
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
4. Mirror the `bytes32` constants and a send function in `../contracts/InstructionSender.sol`

Full walkthrough: [../docs/extension-guide.md](../docs/extension-guide.md). Or run the `/create-extension` skill, which does the language dispatch for you.

## Things specific to this implementation

**You must lock.** `Extension` holds mutable state guarded by `e.mu`. Unlike the Python and TypeScript ports, the framework does not serialize handler calls for you — `actionHandler` may run concurrently.

**`buildResult` owns the envelope.** Return through it rather than constructing an `ActionResult` by hand; it sets the `log` values the wire contract requires.

**ABI payloads** use `structs.DecodeTo` with an `abi.Argument` declared in `pkg/types/types.go` — see `SayGoodbyeMessageArg`.

## Verify

```bash
./scripts/test-conformance.sh go
```

Replays the golden wire fixtures against this implementation. Run `--all` before committing a handler change, so Go, Python and TypeScript stay byte-identical.
