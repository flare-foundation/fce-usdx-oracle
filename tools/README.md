# tools/ — operating the USDX oracle

The oracle does nothing on its own. A machine has to be registered and taken to production, its
configuration has to be published, its credentials have to be installed, and every price it signs
has to be requested and relayed to the store. Each of those is a transaction or a signed message
that somebody sends, and this module is what sends them.

It is a Go module of its own, `fce-usdx-oracle/tools`, and it depends on the extension's `pkg/`
for what the two must agree on: the wire formats (the signed feed update, the readiness report,
the configuration payloads, the provisioning envelope), the protocol's facts (the feed id, the op
tags, the credential refs, the admin roles, the chain set) and the binding of the payload
structs. Each has one implementation, so the tooling follows a format change by construction.
Nothing here imports the extension's `internal/`; that is a rule kept rather than one the
compiler enforces, since this module's path sits under the extension's.
What this module adds is the operator's half: recovering who signed, predicting the hash a
publication will record, and every transaction, through bindings of its own in `pkg/contracts`.
The payloads that cross the contract boundary are pinned to Solidity's own bytes in
`deploy/testdata/wire`.

## The commands

Grouped by the stage of a deployment they serve. `go run ./cmd/<name>` from this directory
(with `GOWORK=off` where a `go.work` points at sibling checkouts).

The commands read the repository's `.env` for the chain, the addresses file and the keys — a
variable already set in the environment wins — and then `deploy/config/stack.env`, which
`start-services.sh` writes for the simulation's stack and which wins over both. The chain is
`FLARE_CHAIN` (`local`, `coston`, `coston_local`, `coston2`, `songbird` or `flare`), else the
one `CHAIN_ID` names (16 is `coston`), else the simulation. `-a`, `-c` and `-p` override the
addresses file, the chain node and the proxy. `audit-deploy` reads none of this: it takes its
own flags, with Coston defaults.

**Wiring the extension**, once, as its owner

| Command | Does |
| --- | --- |
| `wire-extension` | publishes the instructions sender and allowlists the machines' owner on the registry |
| `allow-tee-version` | allows an image's code hash and platform for the extension; pin the hash with `EXPECTED_CODE_HASH` |
| `set-governance` | publishes the TEE governance signer set the machines were launched with |

**Registering and retiring a machine**, once per machine, as its owner

| Command | Does |
| --- | --- |
| `register-tee` | registers the machine, proves its availability and takes it to production |
| `retire-tee` | pauses a machine; for a stopped one, whose key is gone, that is final |

**Publishing the configuration**, as the sender's governance

| Command | Does |
| --- | --- |
| `send-endpoints` | publishes the RPC provider groups and their quorums, or writes the governance hand-off (`-out`) |
| `send-admins` | publishes the admin signer sets that may install credentials, or writes the hand-off |

**Installing credentials**, as the admins

| Command | Does |
| --- | --- |
| `verify-tee` | checks, against the registry, that the key a proxy serves is a PRODUCTION machine of the extension running an accepted build, and prints its id for `send-secret build -tee` |
| `send-secret` | `build`, `sign` and `submit` a `SET_SECRET` envelope carrying one credential, taken from `SECRET_VALUE`; `signers` lists who has signed one and, given the admins file, whether they meet its set's threshold; `commit` prints the `urlHash` that publishes the cash URL |

**Running the feed**, the keeper with a funded key

| Command | Does |
| --- | --- |
| `keeper` | one cycle or a loop of them: request, collect every machine's signed update, preflight, submit. Runs unattended — see [running it unattended](../docs/usdx-oracle-keeper.md#running-it-unattended) |
| `cycles-web` | serves a keeper's records file and a page that reads it; no key, no chain |

**Reading the machines**, with the proxies' direct-channel key and no signing key

| Command | Does |
| --- | --- |
| `readiness` | one machine's signed account of itself: configuration, secrets, observation age, source health |
| `oracle-watch` | the readiness report on a timer, with each change printed |
| `fleet-status` | every active machine of the extension against what the chain says it should be running |

**Checking a deployment**

| Command | Does |
| --- | --- |
| `verify-deploy` | a checklist of a deployment, step by step: the contracts, the registration, the version allowance, the machine, the services |
| `audit-deploy` | an addresses file against a chain: code at the TEE system contracts, the platforms and code hashes the registry supports, and a failing `addTeeVersion` replayed to decode its error |

**On the local simulation**

| Command | Does |
| --- | --- |
| `local-stack` | the proxy and the extension as host processes |

## Keys

Commands sign with the role's key, taken from `.env`:

| Variable | Role |
| --- | --- |
| `EXTENSION_OWNER_KEY` | the extension owner, and the default for everything that names no other role |
| `MACHINE_OWNER_KEY` | the machines' owner, for registration, production and pausing, where it is not the extension owner |
| `ADMIN_KEY` | one admin of a signer set: what `send-secret sign` signs an envelope with. The upload scripts also take several, comma-separated, as `ADMIN_KEYS` |
| `KEEPER_KEY` | the keeper's gas, and nothing else: its two transactions are permissionless, so this is a funded throwaway rather than a role. Falls back to `EXTENSION_OWNER_KEY` with a warning |
| `DIRECT_API_KEY` | not a signing key: the proxy's gate for the direct channel, which readiness queries and `SET_SECRET` use |

Every command that signs, the keeper with `KEEPER_KEY` set excepted, reads `EXTENSION_OWNER_KEY`
when it starts, and on any chain but the simulation refuses to run without it — even
`register-tee` and `retire-tee` when `MACHINE_OWNER_KEY` is what signs, and `send-endpoints` and
`send-admins` for a `-dry-run` or an `-out` hand-off, which sign nothing. On the simulation it
falls back to a dev key funded only there. `verify-tee`, `fleet-status`, `audit-deploy`,
`readiness`, `oracle-watch` and `cycles-web` need no key at all, and `send-secret` needs only
`ADMIN_KEY`, for `sign`. The full matrix is in the [tooling
reference](../docs/usdx-oracle-tooling.md#which-key-each-command-needs).

## Where to go from here

- [deploy/](../deploy/README.md) sequences these commands into the flows an operator runs, on the
  simulation, on a testnet and against a machine a system administrator operates.
- [Tooling reference](../docs/usdx-oracle-tooling.md) is the per-command index: flags,
  what each touches on chain, and how to read what it prints.
- [Keeper specification](../docs/usdx-oracle-keeper.md) covers the cycle and its reliability
  semantics.

`go test ./...` runs this module's own tests. `../deploy/scripts/check-versions.sh` fails when
this module and the extension pin a shared dependency at different versions, which they must not.
