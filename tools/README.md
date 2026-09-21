# tools/ — operating the USDX oracle

The oracle does nothing on its own. A machine has to be registered and taken to production, its
configuration has to be published, its credentials have to be installed, and every price it signs
has to be requested and relayed to the store. Each of those is a transaction or a signed message
that somebody sends, and this module is what sends them.

It is a Go module of its own, `fce-usdx-oracle/tools`, so an operator builds it without building
the enclave. It depends on the extension in exactly one place: `usdxprov` imports the provisioning
envelope package, so the body an admin signs is produced by the same encoder the enclave verifies
with. Everything else here speaks the published wire format and re-declares it, so a bug on one
side cannot hide a bug on the other, and both sides are pinned to the same Solidity-generated
fixtures in `deploy/testdata/wire`.

## The commands

Grouped by the stage of a deployment they serve. `go run ./cmd/<name>` from this directory;
every command reads `../.env` for the chain, the addresses file and the keys.

**Wiring the extension**, once, as its owner

| Command | Does |
| --- | --- |
| `wire-extension` | publishes the instructions sender and allowlists the machines' owner on the registry |
| `allow-tee-version` | allows an image's code hash and platform for the extension |
| `set-governance` | publishes the TEE governance signer set the machines were launched with |

**Registering a machine**, once per machine, as its owner

| Command | Does |
| --- | --- |
| `register-tee` | registers the machine, proves its availability and takes it to production |
| `verify-tee` | checks a machine's attestation against an expected code hash and prints its id |
| `retire-tee` | pauses a machine, which is the one-way retirement |

**Configuring the feed**, as the sender's governance

| Command | Does |
| --- | --- |
| `send-endpoints` | publishes the RPC provider groups and their quorums |
| `send-admins` | publishes the admin signer sets that may install credentials |
| `usdxprov` | builds, signs and submits a `SET_SECRET` envelope carrying one credential |

**Running the feed**, with a funded wallet and the proxies' direct-channel key

| Command | Does |
| --- | --- |
| `keeper` | one cycle or a loop of them: request, collect every machine's signed update, preflight, submit |
| `readiness` | one machine's signed account of itself: configuration, secrets, observation age, source health |
| `fleet-status` | every machine of the extension against what the chain says it should be running |
| `oracle-watch` | the readiness report on a timer, with each change printed |

**Checking a deployment**

| Command | Does |
| --- | --- |
| `verify-deploy` | a checklist of a deployment, step by step: the contracts, the registration, the version allowance, the machine, the services |
| `audit-deploy` | the addresses file against the chain: code at every address, the platforms and code hashes the registry supports, and a failing call replayed to decode its error |
| `local-stack` | the proxy and the extension as host processes on the local simulation |

## Keys

Commands sign with the role's key, taken from `.env`:

| Variable | Role |
| --- | --- |
| `EXTENSION_OWNER_KEY` | the extension owner, and the default for everything that names no other role |
| `MACHINE_OWNER_KEY` | the machines' owner, for registration, production and pausing, where it is not the extension owner |
| `DIRECT_API_KEY` | not a signing key: the proxy's gate for the direct channel, which readiness queries and `SET_SECRET` use |

## Where to go from here

- [deploy/](../deploy/README.md) sequences these commands into the flows an operator runs, on the
  simulation, on a testnet and against a machine a system administrator operates.
- [Tooling reference](../docs/usdx-oracle-tooling.md) is the per-command index: flags,
  what each touches on chain, and how to read what it prints.
- [Keeper specification](../docs/usdx-oracle-keeper.md) covers the cycle and its reliability
  semantics.

`go test ./...` runs this module's own tests. `../deploy/scripts/check-versions.sh` fails when
this module and the extension pin a shared dependency at different versions, which they must not.
