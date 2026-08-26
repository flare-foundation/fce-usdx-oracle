# USDX Oracle — Production Setup Runbook

How to take a registered TEE machine to a live feed, securely: publish its configuration,
install its credentials under two separate admin sets, and start the keeper.

[deployment-steps.md](deployment-steps.md) gets you a machine that is **running and registered
at `PRODUCTION`**. This runbook starts there. [testing.md](testing.md) is the same ground on a
testnet with throwaway keys.

Everything here is **per machine**. Each has its own TEE key, so an envelope built for one
cannot be replayed into another, and every step below is repeated for every machine in the
fleet.

`<PROXY_URL>` is always *that* machine's proxy. The `cd tools` commands resolve `-config` and
`-a` relative to `tools/`, so use absolute paths or `../`.

---

## Who holds what

The split is the security model. No single party can both choose a credential and authorise it.

| Role | Holds | Used for |
| --- | --- | --- |
| **Flare governance** | the timelocked governance authority | publishing endpoints and admin sets on-chain (steps 1–2) via `setEndpoints`/`setAdmins` |
| **Backing admin(s)** | an admin private key | authorising `hex_api_key` only — ≥1 signature |
| **Provider admins** | one admin private key each, on separate machines | authorising the RPC credentials — ≥2 distinct signatures |
| **Hex credential holder** | the Hex API key | building that envelope (step 4) |
| **RPC credential holder** | the RPC provider keys | building those envelopes (step 3) |
| **Operator** | `DIRECT_API_KEY`, the keeper's hot wallet | submitting envelopes, running the feed |

Two properties worth stating plainly:

- **An admin never needs the credential.** Signing authorises a payload that is already
  ciphertext. The party who holds a credential is the one who builds the envelope.
- **A credential holder cannot install it alone.** They need the admins, and for the RPC keys
  at least two of them.

### What is public, and what is not

| | Where it goes | Visibility |
| --- | --- | --- |
| Endpoint configuration | on-chain calldata | **public forever** — URLs and secret *names*, never values |
| Admin sets | on-chain calldata | **public forever** — addresses and thresholds, by design |
| Credentials | `POST /direct`, ECIES-encrypted to the enclave | never on-chain, never on disk in plaintext |

`SET_SECRET` is the one instruction refused over the on-chain channel: a ciphertext in calldata
would be permanent public storage of something that must be revocable by restart.

---

## Order, and why

```
1. endpoints  →  names which refs exist at all
2. admins     →  nothing can authorise a credential until a set is installed
3. RPC keys   →  providers set, ≥2 signatures
4. Hex key    →  backing set, ≥1 signature
5. commission →  the machine lands a price and proves what it is running
```

Steps 3 and 4 are independent of each other and run by different people. Both need 1 and 2 done
first. Getting the order wrong is not dangerous, only silent: an envelope arriving before its
admin set is rejected with the same opaque error as everything else.

### Every step here is per machine

This runbook provisions **one** machine. Nothing in it is fleet-wide, and there is no broadcast:
`send-endpoints` and `send-admins` are addressed to a single `teeId`, and every credential envelope
is ECIES-sealed to one enclave's public key. A three-machine fleet runs all five steps three times —
six on-chain publications and nine envelopes.

Run them all from **one control node**, pointing `-p` at each machine's proxy in turn, rather than
from the machines themselves. Only the control node then needs the funded deployer key and the
provisioning admin keys; a machine needs nothing beyond Docker and its own proxy key, which matters
because a machine publishes a port to the internet and a control node does not.

Getting a machine *registered* in the first place — and retiring, replacing or upgrading one — is
[usdx-oracle-machine-lifecycle.md](usdx-oracle-machine-lifecycle.md). Note that a rebuild mints a
new enclave key, so it produces a machine that has never been provisioned: it needs all five steps
again, and its predecessor needs pausing.

---

## Step 1 — publish the endpoint configuration

The file names each RPC provider and, for the private ones, the **ref** whose credential is
substituted for `{secret}` at dial time. It contains no secret values. Model it on
[`testdata/endpoints/coston2.json`](../testdata/endpoints/coston2.json); keep the production
copy outside the repo or in `config/`.

Validate before anything is recorded — a rejected payload is silent, and leaves the chain
expecting a config the machine is not running:

```bash
cd tools && go run ./cmd/send-endpoints -dry-run -tee <TEE_ID> -config <ENDPOINTS_JSON>
```

Then build the publication, once per machine:

```bash
cd tools && go run ./cmd/send-endpoints -tee <TEE_ID> -config <ENDPOINTS_JSON> -out /tmp/endpoints-<TEE_ID>.json
```

`setEndpoints` is governance-only once the sender is in production mode, so the command does
not publish — `-out` writes the **governance hand-off** (exact calldata, the fee to attach, the
predicted `endpointsHash`) for the Flare governance executor, who runs it through the timelock
and attaches the fee at execution. Hand the file over as written: the recorded calldata hash
must match byte for byte.

After governance executes, confirm the machine is running the published config — its reported
`endpointsHash` must equal the hand-off's `payloadHash`:

```bash
cd tools && go run ./cmd/fleet-status
```

## Step 2 — publish the admin sets

One payload, two sets, published per machine. `version` must strictly increase across
publications for that machine.

```json
{
  "version": 1,
  "backing":   { "threshold": 1, "admins": ["0x…"] },
  "providers": { "threshold": 2, "admins": ["0x…", "0x…", "0x…"] }
}
```

| Set | Governs | Floor | Why the floor differs |
| --- | --- | --- | --- |
| `backing` | `hex_api_key` only | 1 | A wrong value degrades the feed to cash-less; it cannot move the price |
| `providers` | every other ref | 2 | A rogue RPC value denies service on every chain that uses it |

The mapping from ref to set is **pinned in the extension build and is not read from this
payload** — otherwise whoever publishes admins could move a credential under whichever set they
hold. A ref the build does not recognise falls under the stronger set.

```bash
cd tools && go run ./cmd/send-admins -dry-run -tee <TEE_ID> -config <ADMINS_JSON>
```

```bash
cd tools && go run ./cmd/send-admins -tee <TEE_ID> -config <ADMINS_JSON> -out /tmp/admins-<TEE_ID>.json
```

Same hand-off model as step 1: the file carries the exact calldata and fee for the Flare
governance executor, and the machine's reported `adminsHash` must afterwards equal the
hand-off's `payloadHash` (`fleet-status` shows both columns).

> Never publish `testdata/admins/dev-wellknown-keys.json` to a live feed. It names the addresses
> of private keys 1, 2 and 3 — anyone can sign as those admins.

## Step 3 — upload the RPC credentials (three admins)

Run by the party holding the RPC keys. One envelope per ref, each signed by distinct admins from
the `providers` set.

**Coordinate first: the envelope expires one hour after it is built.** Every signer has to be
available in that window. Set `--threshold` to the number you published, so the script hands the
envelope on rather than submitting one the machine will refuse.

**Admin A — holds the credentials, builds, signs, hands off:**

```bash
ENDPOINTS_FILE=<ENDPOINTS_JSON> ./scripts/upload-rpc-keys.sh --threshold 3 --all
```

`--all` takes the ref list from the endpoints file — every `secret` it names — so the machine is
never asked what it is missing. Use the file the machine is actually **running**: confirm with
`send-endpoints -dry-run` that its predicted hash matches `expectedEndpointsHash`, or the list
describes a configuration nobody is on. `--refs <a,b>` names them by hand for the same result.

That uploads every configured ref rather than only the absent ones. The difference costs nothing:
credentials are memory-only and each envelope carries a higher version, so a re-upload supersedes
whatever was there. It is the same command used for rotation.

It prompts for everything it needs, and every secret is prompted **hidden**. Prefer that to
presetting the variables: a `VAR=value ./script` line is not in argv where `ps` can read it, but
it is in the shell history. Preset only for an unattended run, from a file you control:

```bash
set -a; . ./provisioning.env; set +a; ENDPOINTS_FILE=<ENDPOINTS_JSON> ./scripts/upload-rpc-keys.sh --threshold 3 --all
```

It verifies the machine against the chain, encrypts each credential to the attested key, signs
with whatever keys are in `USDXPROV_KEYS`, and writes what it cannot finish to `OUT_DIR` (default
`$TMPDIR/usdx-envelopes`). Each file holds ciphertext and signatures only — safe to send over any
channel.

Admin A is not asked for `DIRECT_API_KEY`: holding fewer keys than the threshold, this run can
only hand off, and it says so before it starts.

> Run without `--all` or `--refs`, the script instead asks the machine which refs it is missing,
> by reading `/state` out of the proxy's `/info`. That only works where tee-node folds the
> extension's state into the attested payload, which it does not do today — so the read comes back
> empty and the step dies with `cannot read <proxy>/info`, blaming an endpoint that answered
> perfectly well. Name the refs and the question never arises.

**Admin B — signs, passes on:**

```bash
cd go && USDXPROV_KEY=<ADMIN_B_KEY> go run ./cmd/usdxprov sign -in <ENVELOPE_JSON>
```

**Admin C — signs and submits over the direct channel:**

```bash
cd go && USDXPROV_KEY=<ADMIN_C_KEY> go run ./cmd/usdxprov sign -in <ENVELOPE_JSON> && USDXPROV_API_KEY=<DIRECT_API_KEY> go run ./cmd/usdxprov submit -in <ENVELOPE_JSON> -url <PROXY_URL>
```

Only the submitter needs `DIRECT_API_KEY`. `usdxprov sign` refuses a duplicate signer, so the
same admin cannot fill two slots of an M-of-N.

Repeat per ref, and per machine. Afterwards the machine should report no missing provider ref:

```bash
cd tools && go run ./cmd/fleet-status -instructionSender <SENDER>
```

## Step 4 — upload the Hex credential

Independent of step 3, and simpler: the `backing` set can be 1-of-N, so if the party holding the
Hex API key also holds a backing admin key, the whole thing finishes on their machine in one
command — no handoff, no coordination window:

```bash
./scripts/upload-hex-key.sh
```

It prompts for the proxy URL, the chain id and the expected code hash, then — hidden — the
backing admin key, the proxy API key and the Hex API key itself. This is the case the split
exists for: they never see an RPC credential, and the provider admins never see theirs.

If the Hex key holder is **not** a backing admin, this script does not apply — it signs and
submits in one pass. Use the three commands directly, keeping the envelope somewhere durable so
it can be handed over, and remember the one-hour window:

```bash
cd tools && go run ./cmd/verify-tee -p <PROXY_URL> -code-hash <0x…>
```

```bash
cd go && USDXPROV_VALUE=<HEX_API_KEY> go run ./cmd/usdxprov build -url <PROXY_URL> -chain-id <CHAIN_ID> -ref hex_api_key -version $(date +%s) -out <ENVELOPE_JSON>
```

Then the backing admin signs and submits, exactly as admin C does in step 3.

## Step 5 — commission and run the feed

A machine is only usable once it has landed one update under its published config and admin set.
The keeper names its target machines (`-tee`), and `requestFeedUpdate` dispatches one instruction
to each — so this happens as the keeper runs, not as a step you drive:

```bash
cd tools && DIRECT_API_KEY=<DIRECT_API_KEY> go run ./cmd/keeper -store <STORE> -tee <TEE1>,<TEE2>,<TEE3> -keeperId keeper-a
```

(`-store` resolves the `TeeOracleFeedStore` entry in the addresses file if omitted; the
instructions sender is derived from the store's own pointer.) Each cycle asks every named machine,
so a fleet of three commissions itself within the hour at the default 10-minute interval. Run
**at least two** keepers: their submissions collide harmlessly, exactly one wins each cycle. See
[usdx-oracle-keeper.md](usdx-oracle-keeper.md).

To force updates faster, run single cycles rather than waiting on the timer:

```bash
./scripts/oracle-cycle.sh -tee <TEE1>,<TEE2>,<TEE3>
```

`DIRECT_API_KEY` is for the `GET_READINESS` preflight, which skips paying the fee when every
machine reports it would refuse to sign. Without it the cycle simply pays.

---

## Verify

One screen for the whole fleet — registry status, what was sent, and what each machine says about
itself right now. The instructions sender is derived from the store's pointer.

```bash
cd tools && go run ./cmd/fleet-status -store <STORE> -feedId <FEED_ID>
```

| Line | Wanted | If not |
| --- | --- | --- |
| `registry` | `PRODUCTION (2)` | the store rejects every update it signs, whatever else is true |
| `configured` | `yes` | `isTeeIdConfigured(feedId, teeId)` is false — endpoints or admins not published for this (feed, machine) |
| `endpoints` / `admins` hash vs `live` | the same hash | it rejected or never received the last `setEndpoints`/`setAdmins` — every `submitFeedUpdate` reverts `StaleEndpoints`/`StaleAdmins` |
| `live` | `ready=yes` | a credential is missing; the `missingSecretRefs` note names which refs |

Then the feed itself: `./scripts/oracle-status.sh` for the on-chain view, and
`./scripts/oracle-observe.sh` for per-source health inside the machine.

---

## Restart and rotation

**Credentials, endpoints and admin sets are memory-only.** A restarted machine returns to
`not_configured` and needs steps 1–4 again, in that order. This is deliberate: a machine that
loses power loses its secrets.

| To change | Do | Then |
| --- | --- | --- |
| one RPC credential | step 3 with `--refs <ref>` | nothing — the config is re-resolved every 2 min, no restart |
| every RPC credential | step 3 with `--all` and `ENDPOINTS_FILE=<the published file>` | nothing |
| the Hex credential | step 4 again | nothing |
| the endpoint config | step 1 with the new file | **re-commission** — a new endpoints hash decommissions the machine |
| the admin set | step 2 again (the contract assigns the new version) | **re-commission** — installed credentials are untouched |

Publishing a new config or admin set raises the sender's expected hash for those machines, which
is correct and self-announcing: a machine still running the old one now signs updates carrying a
stale hash, and the store rejects them `StaleEndpoints`/`StaleAdmins` until the machine applies
the new payload and lands an update. The store keeps no per-machine bookkeeping — there is no
`isProvenConfigured` flag any more — so commissioning is reconciled with `fleet-status`, which
compares each machine's live hash against the sender's expected one.

Rotating admins does **not** evict the credentials already installed, and does **not** reset the
per-ref envelope versions — that would let an envelope the outgoing admins signed be replayed
against the incoming set. A new upload still needs a version above the last one for that ref,
which the scripts get right by using wall-clock seconds.

---

## Rules

1. **Never pass a credential as a flag.** `usdxprov` reads them from the environment and warns
   if you use the equivalent flag — argv is readable by any local process through `ps`, and
   lands in shell history. The upload scripts prompt hidden when a variable is unset.
2. **Never skip `verify-tee`.** The step after it encrypts a credential to a key the proxy
   served; a proxy that lied about that key receives the plaintext. Verification closes the loop
   against the chain — the `teeId` is derived from the attested key, and the chain says whether
   that `teeId` is registered and whether its extension accepts that code hash. `--no-verify`
   exists for a machine that has no attestation to check — a local mocknode, or one built with
   `SIMULATED_TEE=true`, which serves the literal string `magic_pass` and fails verification with
   `token contains an invalid number of segments`. Never for a machine holding a real credential:
   without it, nothing has established that the key the credential is sealed to belongs to a
   registered enclave rather than to whoever answered the port.
3. **Obtain the expected code hash out of band.** Without `-code-hash`, verification proves the
   machine is *some* accepted machine, not that it runs the build you reviewed.
4. **Never put real credentials in `.env`.** Every tool in this repo reads it, and it outlives
   the upload by months. Export them in the shell that runs the upload, or answer the prompt.
5. **One admin, one key, one machine.** M-of-N is only worth the complexity if the keys are
   genuinely separated; two keys on one laptop is 1-of-N wearing a disguise.
6. **Treat `DIRECT_API_KEY` as a real secret.** It cannot forge a price — the enclave signs only
   what it observed — but it is the only gate in front of `SET_SECRET`.

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
