# USDX Oracle — Production Setup Runbook

How to take registered TEE machines to a live feed, securely: publish the feed's configuration,
install each machine's credentials under two separate admin sets, and run the keepers.

[deployment-steps.md](deployment-steps.md) gets you a machine that is **running and registered
at `PRODUCTION`**. This runbook starts there. [testing.md](testing.md) is the same ground on a
testnet with throwaway keys.

There are two kinds of step, and they repeat on different schedules:

- **Publications (steps 1–2) are once per feed, for the whole fleet.** `setEndpoints` and
  `setAdmins` name no machine: each publication is dispatched to every machine in the extension's
  active set when it executes. A machine that joins later is brought up to date by a push, not by
  another publication ([A machine that joins after a publication](#a-machine-that-joins-after-a-publication)).
- **Credentials (steps 3–4) are per machine.** Each envelope is encrypted to one enclave's key
  and bound to its `teeId`, so one built for one machine cannot be replayed into another, and a
  three-machine fleet needs every credential three times.

Songbird is the live deployment. Its files are `deploy/config/songbird/deployed-addresses.json`,
`deploy/config/endpoints/songbird.json` and `deploy/config/admins/songbird.json` (both admin sets
2-of-3), the extension is the reserved id **1**, and its store takes a batch only from **two**
distinct machines (`requiredSignatures`, governance-set). The commands below are written for it;
another chain differs only in those files.

### Conventions

`<PROXY_URL>` is always *that* machine's proxy, the URL it registered. The `cd tools` commands
resolve `-config` and `-a` relative to `tools/`, so use absolute paths or `../`. The tools read
`.env` (activate the chain with `./deploy/scripts/use-chain.sh songbird`), which supplies the chain,
`CHAIN_URL`, `EXTENSION_ID` and the addresses file.

The raw contract calls use Foundry's `cast` with these shell variables, set from the repository
root:

```bash
A=deploy/config/songbird/deployed-addresses.json
SENDER=$(jq -r '.[] | select(.name=="TeeOracleInstructionsSender") | .address' "$A")
TM=$(jq -r '.[] | select(.name=="FlareTeeManager") | .address' "$A")
STORE=$(jq -r '.[] | select(.name=="TeeOracleFeedStore") | .address' "$A")
FEED=0x21555344582f555344000000000000000000000000     # pkg/protocol.FeedID, the tools' default
RPC=https://songbird-api.flare.network/ext/C/rpc      # CHAIN_URL, or the chain's row in deploy/scripts/lib/chains.sh
```

A secret is never typed on a command line, where `ps` shows it, nor in front of one as
`VAR=value`, where the shell history keeps it. Enter it with `read -rs NAME && export NAME` (type
it and press enter; nothing is echoed), load it from a file you control with
`set -a; . ./provisioning.env; set +a`, or answer the scripts' hidden prompts. `cast send` takes
`--interactive`, which prompts for the key, never `--private-key`.

---

## Who holds what

The split is the security model. No single party can both choose a credential and authorise it.

| Role | Holds | Used for |
| --- | --- | --- |
| **Flare governance** | the timelocked governance authority | publishing the endpoints and the admin sets (steps 1–2) with `setEndpoints`/`setAdmins`, and the store's submission policy (`setSubmissionPolicy`) |
| **Extension owner** | `EXTENSION_OWNER_KEY`, the address governance reserved the extension for | allowing and disabling builds (`allow-tee-version`, `disableCodeHashPlatforms`), `ban`/`unban`, `set-governance`, `wire-extension` |
| **Machine owner** | `MACHINE_OWNER_KEY`, the machines' `INITIAL_OWNER` | registering and promoting machines (`register-tee`, `post-build.sh`), pausing them (`retire-tee`) |
| **Backing admins** | one admin private key each, on separate machines | authorising `hex_api_key` and `hex_cash_url` (step 4) — 2-of-3 on Songbird |
| **Provider admins** | one admin private key each, on separate machines | authorising the RPC credentials (step 3) — 2-of-3 on Songbird |
| **Hex credential holder** | the Hex API key, the cash URL and its salt | building those envelopes (step 4) |
| **RPC credential holder** | the RPC provider keys | building those envelopes (step 3) |
| **Operator** | `DIRECT_API_KEY`, the proxies' direct-channel key | submitting envelopes, reading each machine's signed readiness report |
| **Keeper** | `KEEPER_KEY`, a funded hot wallet with no authority | paying `requestFeedUpdate` and `submitFeedUpdates` (step 5) |
| **Anyone funded** | any key with gas | the permissionless, payable `pushEndpoints`/`pushAdmins` |

Two properties worth stating plainly:

- **An admin never needs the credential.** Signing authorises a payload that is already
  ciphertext. The party who holds a credential is the one who builds the envelope.
- **A credential holder cannot install it alone.** They need a threshold of admins: on Songbird
  two of the three, for either set.

### Which command reads which key

| Command | Keys |
| --- | --- |
| `send-endpoints`, `send-admins` | `EXTENSION_OWNER_KEY` must be set even for `-dry-run` and `-out`: both read the chain through the tools' signing bootstrap. Neither path signs anything, and the key's address is the default `-claimBack`. |
| `verify-tee`, `fleet-status`, `send-secret build` / `signers` / `commit` | none (`fleet-status` reads `DIRECT_API_KEY` for its live columns) |
| `send-secret sign` | `ADMIN_KEY` |
| `send-secret submit`, `readiness` | `DIRECT_API_KEY` |
| `upload-rpc-keys.sh`, `upload-hex-key.sh` | `ADMIN_KEYS` (or `ADMIN_KEY`), except with `--build-only`; `DIRECT_API_KEY` to submit and to ask the machine what it is missing |
| `register-tee`, `retire-tee`, `post-build.sh` | `MACHINE_OWNER_KEY` signs; on a live chain `EXTENSION_OWNER_KEY` must be set as well, and `post-build.sh` signs `allow-tee-version` and `set-governance` with it |
| `keeper`, `oracle-cycle.sh` | `KEEPER_KEY` (falling back to `EXTENSION_OWNER_KEY`, with a warning); `DIRECT_API_KEY` for the readiness preflight |
| `oracle-status.sh` | `EXTENSION_OWNER_KEY`, only to name the caller of its reads; `DIRECT_API_KEY` for its readiness section |

### What is public, and what is not

| | Where it goes | Visibility |
| --- | --- | --- |
| Endpoint configuration | on-chain calldata and the `EndpointsPublished` log | **public forever** — URLs, the cash URL's salted commitment and secret *names*, never values |
| Admin sets | on-chain calldata and the `AdminsPublished` log | **public forever** — addresses and thresholds, by design |
| Credentials | `POST /direct`, ECIES-encrypted to the enclave | never on-chain, never on disk in plaintext |
| Readiness report | `POST /direct`, signed by the machine | whoever holds `DIRECT_API_KEY` |

`SET_SECRET` and `GET_READINESS` are the two instructions the on-chain channel refuses; both are
direct-only, and the direct channel refuses the other three. A ciphertext in calldata would be
permanent public storage of something that must be revocable by restart, and the readiness report
names what a machine is missing.

---

## Order, and why

```
1. endpoints  →  once per feed: names which refs exist at all
2. admins     →  once per feed: nothing can authorise a credential until a set is installed
   (push)     →  per machine that joined after 1–2
3. RPC keys   →  per machine, providers set
4. Hex key    →  per machine, backing set
5. run        →  the keepers request from the machines that are ready
```

Steps 3 and 4 are independent of each other and run by different people. Both need 1 and 2
delivered to the machine first. Getting the order wrong is not dangerous, only silent: an envelope
arriving before its admin set is rejected with the same opaque error as everything else.

Run everything that is not an admin's signature from **one control node**, pointing `-p` at each
machine's proxy in turn. The control node holds the operator's keys — `DIRECT_API_KEY`, and
`EXTENSION_OWNER_KEY` / `MACHINE_OWNER_KEY` for the registry calls — and **no admin key**: each
admin signs on their own machine (Rule 5). A machine needs nothing beyond its image and its own
proxy key, which matters because it publishes a port to the internet and a control node does not.

Getting a machine *registered* in the first place — and retiring, replacing or upgrading one — is
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md). A rebuild or restart mints a
new enclave key, so it produces a machine that has never been provisioned: it needs the push and
steps 3–4, its predecessor needs pausing, and every keeper's `-tee` list needs updating.

---

## Step 1 — publish the endpoint configuration

Once per feed. The file names each RPC provider and, for the private ones, the **ref** whose
credential is substituted for `{secret}` at dial time. It contains no secret values. Songbird's is
[`deploy/config/endpoints/songbird.json`](../deploy/config/endpoints/songbird.json).

Its `hex_cash` group carries the cash URL as a salted commitment, never the URL. `send-secret
commit` prints the `urlHash` from `HEX_CASH_URL` and `HEX_CASH_SALT` (32 bytes of hex,
`openssl rand -hex 32`), both read from the environment. Keep the salt with the URL: step 4
uploads the pair, and the machine accepts it only if it hashes to the published commitment.

Validate before anything is recorded — a rejected payload is silent, and leaves the chain
expecting a config the machines are not running:

```bash
cd tools && go run ./cmd/send-endpoints -dry-run -config ../deploy/config/endpoints/songbird.json
```

It checks the file against the extension's own rules, then prints the predicted version (the
current one plus one), the predicted `endpointsHash`, the fee and the active set it would go to.

Then try every provider with the credential that will be installed for it. Each ref's key is read
from the environment under its name in capitals (`aflabs_key` from `AFLABS_KEY`), and each URL must
answer with the chain its group reads — a wrong key, a wrong parameter name or a URL for another
network fails here, before it costs a round of admin signatures. Load the keys in a subshell, so
they leave with it:

```bash
( set -a; . ./provisioning.env; set +a; ./deploy/scripts/check-endpoints.sh deploy/config/endpoints/songbird.json )
```

`provisioning.env` is a file you control (mode 600; git ignores `*.env`) holding `AFLABS_KEY=…`,
`FLARE_TRACER_KEY=…` and so on, deleted once the uploads are done. The `hex_cash` group is
skipped: its URL is itself the credential.

Changing a URL after publishing costs another governance publication, so this is the cheap place
to find out.

Then build the publication — once, for the whole fleet:

```bash
cd tools && go run ./cmd/send-endpoints -config ../deploy/config/endpoints/songbird.json -claimBack <EXECUTOR_WALLET> -out /tmp/endpoints.json
```

`setEndpoints` is governance-only once the sender is in production mode, so the command does
not publish — `-out` writes the **governance hand-off** (exact calldata, the fee, the predicted
`endpointsHash`) for the Flare governance executor, who runs it through the timelock and attaches
the fee at execution. Hand the file over as written: the recorded calldata hash must match byte
for byte. The fee in it is quoted for the active set when the file was written; the executor reads
`getEndpointsPublicationFee()` in the executing block, and a short fee reverts and can be retried.
Two pending publications for one feed execute in either order and the later one wins, so a
superseded one is cancelled rather than left queued.

**Confirm the publication.** After execution the sender's `latestEndpointsHash` must equal the
hand-off's `payloadHash`:

```bash
cast call "$SENDER" "latestEndpointsHash(bytes21)(bytes32)" "$FEED" --rpc-url "$RPC"
```

To confirm that a file in hand is the published one, hash it at the published version. Do not
compare a `-dry-run`'s hash: it predicts the *next* version, which no machine runs.

```bash
cd tools && V=$(cast call "$SENDER" "endpointsVersion(bytes21)(uint64)" "$FEED" --rpc-url "$RPC" | cut -d' ' -f1) \
  && cast keccak "$(go run ./cmd/send-endpoints -encode -version "$V" -config ../deploy/config/endpoints/songbird.json)"
```

Then confirm each machine applied it. With `DIRECT_API_KEY` exported, every row's `live` must equal
its `endpoints`, and the tool names any machine running something else:

```bash
cd tools && go run ./cmd/fleet-status
```

## Step 2 — publish the admin sets

Once per feed. One payload, two sets; the sender assigns the version, so the file carries only the
sets. Songbird's ([`deploy/config/admins/songbird.json`](../deploy/config/admins/songbird.json)):

```json
{
  "backing":   { "threshold": 2, "admins": ["0x…", "0x…", "0x…"] },
  "providers": { "threshold": 2, "admins": ["0x…", "0x…", "0x…"] }
}
```

| Set | Governs | Floor | Why the floor differs |
| --- | --- | --- | --- |
| `backing` | `hex_api_key` and `hex_cash_url` | 1 | A wrong value degrades the feed to cash-less; it cannot move the price |
| `providers` | every other ref | 2 | A rogue RPC value denies service on every chain that uses it |

The mapping from ref to set is **pinned in the extension build and is not read from this
payload** — otherwise whoever publishes admins could move a credential under whichever set they
hold. A ref the build does not recognise falls under the stronger set. A threshold may not exceed
16, the most signatures an envelope can carry.

```bash
cd tools && go run ./cmd/send-admins -dry-run -config ../deploy/config/admins/songbird.json
```

```bash
cd tools && go run ./cmd/send-admins -config ../deploy/config/admins/songbird.json -claimBack <EXECUTOR_WALLET> -out /tmp/admins.json
```

Same hand-off model as step 1. After execution `latestAdminsHash` must equal the hand-off's
`payloadHash`, and the same `-encode` check works with `adminsVersion` and `send-admins`:

```bash
cast call "$SENDER" "latestAdminsHash(bytes21)(bytes32)" "$FEED" --rpc-url "$RPC"
```

Each machine's applied admin set is `liveAdminsHash` in `fleet-status -json`, which must equal
`adminsHash`; the table shows only the endpoints hash.

> Never publish `deploy/config/admins/dev-wellknown-keys.json` to a live feed. It names the addresses
> of private keys 1, 2 and 3 — anyone can sign as those admins.

## A machine that joins after a publication

A publication reaches only the machines that are in `PRODUCTION` when it executes. A machine
registered later — a replacement, an addition — holds no configuration, and one that was paused or
suspended at the time holds the previous one. Either is behind: `requestFeedUpdate` naming it
reverts `TeeIdNotConfigured` (or `FeedNotConfigured` while nothing is published), and a new machine
cannot accept a credential before it holds an admin set. A publication that executed while the
extension was emergency paused, or had no active machine, was recorded and dispatched to nobody,
and needs the same remedy.

The remedy is the permissionless, payable `pushEndpoints(feedId, teeIds, groups)` and
`pushAdmins(feedId, teeIds, roles)`. They take the published values back, re-encode them with the
version the sender holds, and revert `WrongConfigPayload` unless they hash to the latest
publication, so a wrong file cannot be pushed. The sender stores only the hash; the values are in
the publication's `EndpointsPublished` / `AdminsPublished` log, and that is where they are read
from. No tool in this repository wraps the push; with the variables from
[Conventions](#conventions) and any funded key:

```bash
cast call "$SENDER" "getEndpointsPushTargets(bytes21)(address[])" "$FEED" --rpc-url "$RPC"   # active machines behind
cast call "$SENDER" "getAdminsPushTargets(bytes21)(address[])" "$FEED" --rpc-url "$RPC"
TEE=<NEW_TEE_ID>
```

Endpoints first:

```bash
G='(bytes32,uint64,(uint8,string,bytes32,string)[])[]'
TS=$(cast call "$SENDER" "endpointsPublishedAt(bytes21)(uint64)" "$FEED" --rpc-url "$RPC" | cut -d' ' -f1)
B=$(cast find-block "$TS" --rpc-url "$RPC")
DATA=$(cast logs --from-block $((B-2)) --to-block $((B+2)) --address "$SENDER" \
  "EndpointsPublished(bytes21 indexed,uint64 indexed,bytes32,$G)" "$FEED" --rpc-url "$RPC" --json | jq -r '.[-1].data')
ENDPOINT_GROUPS=$(cast abi-decode --input "f(bytes32,$G)" "$DATA" | sed -n 2p)
FEE=$(cast call "$TM" "calculateFeeByTeeIds(bytes32,bytes32,address[])(uint256)" \
  "$(cast format-bytes32-string TEE_ORACLE)" "$(cast format-bytes32-string SET_ENDPOINTS)" "[$TEE]" --rpc-url "$RPC" | cut -d' ' -f1)
cast send "$SENDER" "pushEndpoints(bytes21,address[],$G)" "$FEED" "[$TEE]" "$ENDPOINT_GROUPS" --value "$FEE" --rpc-url "$RPC" --interactive
```

Then the admin sets, the same way:

```bash
R='(bytes32,uint64,address[])[]'
TS=$(cast call "$SENDER" "adminsPublishedAt(bytes21)(uint64)" "$FEED" --rpc-url "$RPC" | cut -d' ' -f1)
B=$(cast find-block "$TS" --rpc-url "$RPC")
DATA=$(cast logs --from-block $((B-2)) --to-block $((B+2)) --address "$SENDER" \
  "AdminsPublished(bytes21 indexed,uint64 indexed,bytes32,$R)" "$FEED" --rpc-url "$RPC" --json | jq -r '.[-1].data')
ADMIN_ROLES=$(cast abi-decode --input "f(bytes32,$R)" "$DATA" | sed -n 2p)
FEE=$(cast call "$TM" "calculateFeeByTeeIds(bytes32,bytes32,address[])(uint256)" \
  "$(cast format-bytes32-string TEE_ORACLE)" "$(cast format-bytes32-string SET_ADMINS)" "[$TEE]" --rpc-url "$RPC" | cut -d' ' -f1)
cast send "$SENDER" "pushAdmins(bytes21,address[],$R)" "$FEED" "[$TEE]" "$ADMIN_ROLES" --value "$FEE" --rpc-url "$RPC" --interactive
```

`TEE` may name several machines (`TEE=0x…,0x…`), and the fee then covers every one. With the governance
hand-off file at hand, the same values decode from its calldata:
`cast calldata-decode "setEndpoints(bytes21,$G,address)" "$(jq -r .calldata /tmp/endpoints.json)" | sed -n 2p`.
A push to a machine already at the latest version is accepted and changes nothing, which makes it
the retry for an instruction that never reached its enclave.

Afterwards `isTeeIdConfigured` is true for the machine, and `fleet-status` shows it `configured`
with `live` equal to `endpoints`:

```bash
cast call "$SENDER" "isTeeIdConfigured(bytes21,address)(bool)" "$FEED" "$TEE" --rpc-url "$RPC"
```

## Step 3 — upload the RPC credentials (the providers set)

Run by the party holding the RPC keys, once per machine. One envelope per ref, each signed by
distinct admins from the `providers` set — two of the three on Songbird.

**Coordinate first: the envelope expires one hour after it is built.** Every signer has to be
available in that window. Set `--threshold` to the published threshold (`PROVIDER_THRESHOLD`
otherwise, default 2), so the script hands the envelope on rather than submitting one the machine
will refuse. `ENDPOINTS_FILE` defaults to Coston2's file, so name the published one.

**A coordinator who is not an admin** — the usual arrangement — builds every envelope unsigned:

```bash
PROXY_URL=<PROXY_URL> ENDPOINTS_FILE=deploy/config/endpoints/songbird.json ./deploy/scripts/upload-rpc-keys.sh --build-only --all
```

`--build-only` verifies the machine, encrypts each credential to it and writes the envelopes out
**unsigned**. It reads no admin key — not even one that `.env` happens to name — and submits
nothing. It still asks the machine over the direct channel when it needs to: without `--refs` or
`--all` it must ask which refs are missing, and with `--all`, if `DIRECT_API_KEY` is set, it reads
the `endpointsHash` the machine runs, for you to check against the file. Run it once per machine, changing
`PROXY_URL`. Each file is named `<teeId>-<ref>.json`, so the second machine's envelopes sit beside
the first's rather than overwriting them while they wait for signers.

**Or an admin who holds the credentials** runs the same command without `--build-only`, with
`--threshold 2`. It signs with the one key it is given and hands off; holding fewer keys than the
threshold it does not ask for `DIRECT_API_KEY`, and says so before it starts.

`--all` takes the ref list from the endpoints file — every `secret` it names, less the backing
refs `hex_api_key` and `hex_cash_url`, which are step 4's even though the file names
`hex_cash_url`. Use the file the machine is actually **running**: when it can ask the machine, the
script prints the `endpointsHash` it runs, which must be the published one, and the file must hash
to it (step 1). `--refs <a,b>` names refs by hand.

That uploads every configured ref rather than only the absent ones. The difference costs nothing:
credentials are memory-only and each envelope carries a higher version, so a re-upload supersedes
whatever was there. It is the same command used for rotation.

> Run without `--all` or `--refs`, the script instead asks the machine which refs it is missing,
> through its signed readiness report (`GET_READINESS`, which needs the proxy's direct-channel
> key). A machine that does not answer makes the step say so rather than guess; naming the refs
> sidesteps the question.

The script prompts for the proxy URL and, hidden, for each credential, the admin keys (unless
`--build-only`) and the proxy API key (only when it holds enough keys to submit). Preset only from
a file you control:

```bash
( set -a; . ./provisioning.env; set +a; PROXY_URL=<PROXY_URL> ENDPOINTS_FILE=deploy/config/endpoints/songbird.json ./deploy/scripts/upload-rpc-keys.sh --build-only --all )
```

It verifies the machine against the chain, encrypts each credential to the attested key, signs
with whatever keys are in `ADMIN_KEYS`, and writes what it cannot finish to `OUT_DIR` (default
`deploy/envelopes/`, which git ignores). Each file holds ciphertext and signatures only — safe to
send over any channel.

**Each admin signs**, on their own machine, every file sent to them, in one go — `<ENVELOPE_DIR>`
is wherever they keep the files, given as an absolute path:

```bash
cd tools && read -rs ADMIN_KEY && export ADMIN_KEY
for f in <ENVELOPE_DIR>/*.json; do go run ./cmd/send-secret sign -in "$f"; done
unset ADMIN_KEY
```

`send-secret sign` refuses a duplicate signer, so the same admin cannot fill two slots of an
M-of-N. Before submitting, check a file carries enough:

```bash
cd tools && go run ./cmd/send-secret signers -in <ENVELOPE_JSON> -admins ../deploy/config/admins/songbird.json
```

It recovers each signature to the address that made it, names any that is not in the set
governing the ref, and exits non-zero below that set's threshold. The enclave answers a
shortfall with the same one-line rejection it gives everything else, so this is where a missing
or mistaken signer is found while the hour is still running.

**Whoever holds `DIRECT_API_KEY` submits** the signed files, per machine, since each is bound to
one:

```bash
cd tools && read -rs DIRECT_API_KEY && export DIRECT_API_KEY
for f in <ENVELOPE_DIR>/<TEE_ID>-*.json; do go run ./cmd/send-secret submit -in "$f" -p <PROXY_URL>; done
```

`submit` waits for the machine's own verdict. Afterwards the machine should report no missing
provider ref — `missingSecretRefs` in its readiness report, which `fleet-status` does not show:

```bash
cd tools && go run ./cmd/readiness -p <PROXY_URL> | jq .readiness
```

### Envelope limits

- **One hour.** An envelope is refused once its `issuedAt` is more than an hour old, and also when
  it lies more than two minutes ahead of the enclave's clock — a builder with a fast clock produces
  envelopes the machine rejects.
- **Sixteen signatures** at most. `submit` refuses more, because the enclave drops an
  over-signed envelope before it counts signers.
- **Versions strictly increase per ref**, for the life of the machine's process. The scripts use
  wall-clock seconds, which also survives a restart's reset to zero.
- **Bound to one chain and one `teeId`.** An envelope for another machine, or another chain, is
  refused.

**Rehearse before the day, once per role.** Signing is offline and never checks the expiry, so each
admin can sign a practice envelope in advance, and `signers -admins` then shows whether the key
each will sign with is the one the admins file names. The check is against the set that governs
the envelope's ref, so a provider admin rehearses on an envelope for a provider ref and a backing
admin on one for `hex_api_key`. On Songbird the same three addresses form both sets, so each admin
rehearses both. A practice file is never submitted.

## Step 4 — upload the Hex credentials (the backing set)

Independent of step 3, once per machine. Two refs fall under the `backing` set: `hex_api_key` and,
when `HEX_CASH_URL` and `HEX_CASH_SALT` are set, `hex_cash_url` — the restricted cash URL as the
preimage of the commitment the endpoints publication carries. The machine counts the cash figure
only once it holds both; without `HEX_CASH_URL` only the key goes up and the machine runs cash-less.
This is the case the split exists for: the Hex holder never sees an RPC credential, and the
provider admins never see theirs.

`upload-hex-key.sh` signs with every key in `ADMIN_KEYS` (comma-separated, or the single
`ADMIN_KEY`) and submits once they reach the backing threshold — `--threshold`, else
`BACKING_THRESHOLD`, else `backing.threshold` in `deploy/config/admins/<chain>.json`, else 1.
Holding fewer distinct keys than that, it refuses before building anything and points to
`--build-only`. On a deployment whose backing set is 1-of-N, a backing admin who holds the Hex key
therefore finishes in one run:

```bash
./deploy/scripts/upload-hex-key.sh
```

Songbird's backing set is 2-of-3, and its admins keep their keys apart (Rule 5), so there the Hex
holder builds and the admins sign:

```bash
( set -a; . ./hex.env; set +a; PROXY_URL=<PROXY_URL> ./deploy/scripts/upload-hex-key.sh --build-only )
```

`hex.env` holds `HEX_API_KEY`, `HEX_CASH_URL` and `HEX_CASH_SALT`. The run verifies the machine and
writes one unsigned envelope per credential to `OUT_DIR`, named `<teeId>-<ref>.json`, reading no
admin key and never touching the direct channel. Two backing admins then sign and the operator
submits, exactly as in step 3.

It reads `.env` first (the proxy URL, chain id and API key where set) and prompts for what is
missing: the proxy URL and — only when `.env` names no chain — the chain id, then, hidden, the
backing admin key(s) and the proxy API key (neither with `--build-only`) and the Hex API key. It
never prompts for `EXPECTED_CODE_HASH`, `HEX_CASH_URL` or `HEX_CASH_SALT`: set them in its
environment.

The same can be done by hand, keeping the envelopes somewhere durable so they can be handed over,
and remembering the one-hour window. Verification prints the attested `teeId`, and `-tee` makes
`build` refuse a key that derives to any other — without it, `build` encrypts to whatever the
proxy serves at that moment:

```bash
cd tools && TEE_ID=$(go run ./cmd/verify-tee -p <PROXY_URL> -codeHash <EXPECTED_CODE_HASH> -printTeeId)
```

```bash
read -rs SECRET_VALUE && export SECRET_VALUE
go run ./cmd/send-secret build -p <PROXY_URL> -tee "${TEE_ID:?verify-tee did not pass}" -ref hex_api_key -version $(date +%s) -out <API_KEY_ENVELOPE_JSON>
unset SECRET_VALUE
read -rs HEX_CASH_URL && read -rs HEX_CASH_SALT && export HEX_CASH_URL HEX_CASH_SALT
go run ./cmd/send-secret build -p <PROXY_URL> -tee "${TEE_ID:?verify-tee did not pass}" -ref hex_cash_url -version $(date +%s) -out <CASH_URL_ENVELOPE_JSON>
```

`verify-tee` checks the machine against the extension in `EXTENSION_ID` (or `-extensionId`) and
refuses with neither set. `SECRET_VALUE` is unset before the second build because `build` refuses
it for `hex_cash_url`: that envelope carries `abi.encode(salt, url)`, and the salt must be the one
the published `hex_cash` commitment was made with (`send-secret commit` prints that commitment
from the same two variables). Then two backing admins sign and the operator submits, exactly as in
step 3.

## Step 5 — run the feed

A machine serves once it is `PRODUCTION`, at the feed's latest configuration (`configured` in
`fleet-status`) and provisioned (`ready`). The store accepts a batch of updates from at least
`requiredSignatures` distinct such machines — two on Songbird — carrying one `observedAt`. There
is nothing to commission beyond that. The store's policy is read live:

```bash
cast call "$STORE" "requiredSignatures()(uint256)" --rpc-url "$RPC"
```

The keeper names its targets in a static `-tee` list, and `requestFeedUpdate` dispatches one
instruction to all of them. **Every named machine must be `PRODUCTION` and configured, or the whole
request reverts** (`TeeMachineNotAvailable`, `TeeIdNotConfigured`), every cycle. So name only
machines that are ready, and change the list whenever the fleet does
([usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md)). The registry supplies each
machine's proxy URL, nothing more.

The keeper runs as a container (`keeper.Dockerfile`). Build and push the image with Songbird's
addresses baked in; `<KEEPER_IMAGE>` below is the registry path it pushes to (`IMAGE_REPO`), and
`out/images/keeper-<tag>.env` records the commit and digest:

```bash
./deploy/scripts/build-keeper-image.sh --chain songbird --tag <TAG>
```

Its secrets go in an env file (mode 600), never on the command line: `KEEPER_KEY`,
`DIRECT_API_KEY`, and `EXT_PROXY_URL` set to one machine's proxy, which the readiness preflight
needs to be able to skip; `CHAIN_URL` is optional, since the image's chain names its public RPC.
Arguments given to `docker run` replace the image's defaults, so restate them:

```bash
docker run -d --name usdx-keeper --restart unless-stopped --stop-timeout 90 --env-file keeper.env \
  -v usdx-keeper-records:/var/log/usdx <KEEPER_IMAGE>:<TAG> \
  -tee <TEE1>,<TEE2>,<TEE3> -keeperId keeper-a -interval 10m -records /var/log/usdx/cycles.jsonl
```

Run **at least two** keepers, on separate hosts with distinct `-keeperId`s. Their submissions for
one `observedAt` collide harmlessly: one lands, and the others record `race_lost`, which is never
an alert. Changing a keeper's `-tee` list is a restart (`docker stop -t 90`, then `docker run` with
the new list), which is always safe: cycles are stateless. Restart the keepers one at a time, so
one is always running. [usdx-oracle-keeper.md § Running it unattended](usdx-oracle-keeper.md#running-it-unattended)
is the full list of what it needs.

To run single cycles by hand, from the control node:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE1>,<TEE2>,<TEE3>
```

Every cycle pays the request fee, `--dry-run` included (it stops before submitting).
`DIRECT_API_KEY` buys the `GET_READINESS` preflight, which skips the fee when every machine asked
reports it would not sign (`wouldSign` false); without it every cycle pays.

---

## Verify

One screen for the whole fleet — registry status, what was published, and what each machine says
about itself right now. Export `DIRECT_API_KEY` for the live columns.

```bash
cd tools && go run ./cmd/fleet-status
```

The rows are the extension's active set, which holds only `PRODUCTION` machines; a machine missing
from it is not `PRODUCTION`. The columns are `tee id`, `status`, `configured`, `endpoints`,
`admins`, `live` and `ready`, and the store's `observedAt` and its age follow the table.

| Column | Wanted | If not |
| --- | --- | --- |
| `status` | `PRODUCTION` | a machine absent from the list is not serving: the store rejects what it signs, and a keeper naming it reverts every request |
| `configured` | `true` | `isTeeIdConfigured(feedId, teeId)` is false: it missed a publication — [push](#a-machine-that-joins-after-a-publication) |
| `endpoints`, `admins` | the feed's latest published hashes, the same on every row | `—`: nothing published |
| `live` | equal to `endpoints` | the machine runs another configuration — it refused or never received the latest one — and every batch it contributes to reverts `StaleEndpoints`; the line under the table names it |
| `ready` | `yes` | `no`: provisioning outstanding, named by `missingSecretRefs` in its readiness report; `—`: not asked (no `DIRECT_API_KEY`) or no answer |

`fleet-status -json` prints the same as one JSON array, with `liveAdminsHash` (compare with
`adminsHash`) and each machine's `sourceHealth`. A machine's `missingSecretRefs` is
`go run ./cmd/readiness -p <PROXY_URL> | jq .readiness`.

Then the feed itself: `./deploy/scripts/oracle-status.sh` for the on-chain view and the readiness
report of the machine `EXT_PROXY_URL` names. `./deploy/scripts/oracle-observe.sh` runs the
observation path locally against the live sources, with no TEE, to show what they yield.

---

## Restart and rotation

**Credentials, endpoints and admin sets are memory-only, and so is the enclave key.** A restarted
machine is a new machine: it is registered afresh
([usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md)), brought to the feed's
configuration by a [push](#a-machine-that-joins-after-a-publication) rather than a new publication,
and given steps 3–4 again. This is deliberate: a machine that loses power loses its secrets.

| To change | Do | Then |
| --- | --- | --- |
| one RPC credential | step 3 with `--refs <ref>`, per machine | nothing — the next observation (every 2 min) uses it |
| every RPC credential | step 3 with `--all` and `ENDPOINTS_FILE=<the published file>`, per machine | nothing |
| the Hex API key | step 4, per machine | nothing |
| the endpoint config | step 1 with the new file — one publication | every active machine is sent it; check `fleet-status` and push to any that missed it; a new ref needs its credential (step 3 with `--refs`) |
| the cash URL or its salt | replace the machines (below) | — |
| the admin sets | step 2 — one publication | every active machine is sent it; installed credentials are untouched |

A publication moves the sender's latest hash for the feed at once, which is correct and
self-announcing: from that block the store rejects every update still carrying the previous hash
(`StaleEndpoints`/`StaleAdmins`), so the feed waits until `requiredSignatures` machines have
applied the new payload. The store keeps no per-machine record; `fleet-status` reconciles each
machine's live hash against the sender's latest one.

**A new cash commitment needs new machines.** A machine checks every credential it holds against
an arriving endpoint configuration, so one holding the old `hex_cash_url` refuses a publication
whose `hex_cash` commitment it does not match, and keeps signing under the old configuration,
which the store then rejects. Change the cash URL or its salt on fresh machines: register them,
push the admin sets, upload every credential including the new `hex_cash_url` (with no endpoint
configuration installed yet, there is nothing to check it against), and only then let the
publication execute. It reaches them as members of the active set; then move the keepers' `-tee`
to them and retire the old machines.

Rotating admins does **not** evict the credentials already installed, and does **not** reset the
per-ref envelope versions — that would let an envelope the outgoing admins signed be replayed
against the incoming set. A new upload still needs a version above the last one for that ref,
which the scripts get right by using wall-clock seconds.

---

## Rules

1. **Never put a secret on a command line.** Credentials, admin keys and `DIRECT_API_KEY` travel in
   the environment or a hidden prompt: argv is readable by any local process through `ps`, and a
   `VAR=value` prefix lands in the shell history. `send-secret` has no flag for a credential, and
   the scripts hand `DIRECT_API_KEY` to the tools in their environment. The tools' `-directApiKey`
   flag is an override that puts the key in argv; leave it alone on a shared host. For `cast`, use
   `--interactive`.
2. **Never skip `verify-tee`, and pin what it verified.** The step after it encrypts a credential
   to a key the proxy served; a proxy that lied about that key receives the plaintext.
   Verification closes the loop against the chain: the `teeId` is derived from the served key, and
   the registry's record of that machine must say `PRODUCTION`, in the extension `EXTENSION_ID`
   names, with a code hash and platform the extension accepts and, given `-codeHash`, the expected
   one. Registration verified the attestation, so the chain record is the authority; the token's
   claims are only a cross-check. `-printTeeId` passes the verified id to `send-secret build -tee`,
   which then refuses any other key; the scripts do this for you. `--no-verify` is for a local
   mocknode, which is registered on no chain, and never for a machine holding a real credential.
   A simulated machine verifies like a real one, provided `SIMULATED_TEE=true` is in the tools'
   environment — without it they parse `magic_pass` as a token and fail with
   `token contains an invalid number of segments`.
3. **Obtain the expected code hash out of band** — the `EXPECTED_CODE_HASH` that
   `build-image.sh` records in `out/images/<tag>.env`, or your own rebuild of the agreed commit,
   never the machine being checked. Without it, verification proves the machine runs *some*
   accepted build, not the one you reviewed.
4. **Never put provisioned credentials in `.env` on a live chain.** Every tool in this repo reads
   it, and it outlives the upload by months. `use-chain.sh` writes `HEX_API_KEY`, `HEX_CASH_URL`
   and `HEX_CASH_SALT` there when given them, and carries them over on every later switch — a
   convenience for the simulation. On a live chain do not pass them to it, and switch with
   `use-chain.sh <chain> --no-hex`, which does not carry them over. Keep the credentials in a file
   you control and load it only for the upload. `.env` does hold the operator's own keys
   (`DIRECT_API_KEY`, `EXTENSION_OWNER_KEY`, `MACHINE_OWNER_KEY`), never an admin's.
5. **One admin, one key, one machine.** M-of-N is only worth the complexity if the keys are
   genuinely separated; two keys on one laptop is 1-of-N wearing a disguise.
6. **Treat `DIRECT_API_KEY` as a real secret.** It cannot forge a price — the enclave signs only
   what it observed — and it cannot install a credential by itself, because `SET_SECRET` also needs
   the admins' threshold. But it is the gate to every machine's direct channel: whoever holds it
   can submit envelopes and read each machine's signed readiness report.

---

_Companion docs: [usdx-oracle-tooling.md](usdx-oracle-tooling.md) (what every script and command
does) · [usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md) (registering, pausing,
replacing and upgrading machines) ·
[usdx-oracle-failure-modes.md](usdx-oracle-failure-modes.md) (what breaks the feed and how it
shows) · [deployment-steps.md](deployment-steps.md) (getting the machine registered) ·
[testing.md](testing.md) (the same flow on a testnet) ·
[usdx-oracle-keeper.md](usdx-oracle-keeper.md) (the feed) ·
[usdx-oracle-flows.md](usdx-oracle-flows.md) (what each instruction does inside the enclave) ·
[usdx-oracle-findings.md](usdx-oracle-findings.md) (why the controls are shaped this way)._
