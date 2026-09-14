# 🚀 TEE Extension Deployment — Step by Step

Linear recipe to deploy a TEE extension to Flare Coston or Coston2. Run the steps top to bottom.

## Prerequisites

- 🐳 Docker Desktop (Linux containers)
- 🐹 Go 1.25.1+
- 🔨 Foundry (`forge`, `cast`)
- `jq`
- Bash (Git Bash on Windows works)
- No indexer credentials: `deploy/docker-compose.<chain>-indexer.yaml` runs the chain's
  C-chain indexer locally (its configuration is rendered by `import-upstream-addresses.sh`)

## 1. Get the extension repo

The default build is **self-contained**: `go.mod` pins `tee-node` and
`proxy/Dockerfile` pins `tee-proxy`, both fetched from the network at build
time. You only need this repo — no sibling `tee-node/` or `tee-proxy/`
checkouts.

```text
<workspace>/
└── <your-extension>/     # this repo — builds standalone
```

> **Developing `tee-node`/`tee-proxy` locally?** Place them as siblings and use
> the opt-in toggle, which builds the node + proxy from your on-disk checkouts
> instead of the pinned versions:
>
> ```text
> <workspace>/tee/
> ├── tee-node/         # github.com/flare-foundation/tee-node
> ├── tee-proxy/        # github.com/flare-foundation/tee-proxy
> └── extension-examples/
>     └── <your-extension>/
> ```
>
> ```bash
> USE_LOCAL_SIBLINGS=1 ./deploy/scripts/start-services.sh --chain coston2
> ```

## 2. Generate a funded deployer key

```bash
cast wallet new
cast wallet address --private-key 0x<private-key>
```

The derived address becomes your `INITIAL_OWNER`. Fund it from the target chain's faucet.

| Chain   | Faucet                                 |
| ------- | -------------------------------------- |
| Coston  | `https://faucet.flare.network/coston`  |
| Coston2 | `https://faucet.flare.network/coston2` |

## 3. Create `.env.<chain>`

Copy `.env.example` to `.env.coston` or `.env.coston2`. Fill in:

```bash
CHAIN=coston2                                                         # or coston
CHAIN_URL=https://coston2-api.flare.network/ext/C/rpc                 # chain RPC
ADDRESSES_FILE=./deploy/config/coston2/deployed-addresses.json
NORMAL_PROXY_URL=https://tee-proxy-coston2-1.flare.rocks              # FTDC proxy
EXT_PROXY_URL=                                                        # leave empty — set in Step 6

LOCAL_MODE=false
SIMULATED_TEE=false
DEPLOYMENT_PRIVATE_KEY=<private key, no 0x prefix>
INITIAL_OWNER=0x<derived address from Step 2>
```

Activate it:

```bash
bash ./deploy/scripts/use-chain.sh <chain>
```

Copies `.env.<chain>` → `.env`, which all scripts auto-load.

## 4. The oracle contracts are deployed upstream — nothing to deploy here

> For a **test deployment** on Coston or Coston2 there is a shortcut: `deploy-oracle.sh --chain
> <chain>` deploys the same contracts deployer-governed (no production mode, no timelock, one
> key for everything) together with the state verifier, and checks or registers the extension.
> The deploy/README "Running on a live chain" section is that flow. The rest of this section is the
> production path.

This repo deploys **no contracts**. The `TeeOracleInstructionsSender` and one
`TeeOracleFeedStore` per feed are deployed by
`flare-smart-contracts-v2`'s own script, and the USDX extension is a *reserved*
extension with a fixed id:

| Fact | Value | Where it's pinned |
| ---- | ----- | ----------------- |
| Extension id | `1` (reserved) | `internal/config` + upstream `teeOracleExtensionId` |
| Feed id (USDX/USD) | `0x20555344582f555344…` (category `0x20` + `USDX/USD`) | `internal/config` |

So there is no `pre-build.sh` and no locally-minted `EXTENSION_ID`. The on-chain
side is run from the contracts repo and finished by governance:

```bash
# in flare-smart-contracts-v2
./deployment/scripts/deploy-tee-oracle.sh <network>      # deploys sender + store(s)
```

then the follow-ups the script prints (it cannot do them itself) —
`registerReserved(extensionId, owner)` by Flare governance (timelocked), then
`setExtensionContracts` by the extension owner, then the `TEE_ORACLE` operation-fee
rows and `FtsoV2.addCustomFeeds` through governance again. See that repo's
`docs/specs/FCC/TeeOracle.md` for the full list.

Once deployed, import the two addresses into this repo's addresses file so the
tooling can find them by name (`TeeOracleFeedStore`, `TeeOracleInstructionsSender`):

```bash
./deploy/scripts/import-upstream-addresses.sh coston2     # or coston
```

Deploy the extension's state verifier, the one contract this repository adds. The registry
passes each machine's attested extension state through it on every availability check, and
tee-node reports the extension's `GET /state` in that attestation, so without a verifier no
machine reaches production (the registry accepts an empty state only). The verifier has no
owner and no configuration; it refuses tee-node's "could not read the extension" sentinel and
a malformed report, and nothing else:

```bash
cd contracts && forge script script/DeployStateVerifier.s.sol:DeployStateVerifier \
    --rpc-url $CHAIN_URL --private-key $DEPLOYMENT_PRIVATE_KEY --broadcast
# add the printed address to deploy/config/coston2/deployed-addresses.json as UsdxTeeExtensionStateVerifier
```

Then wire the reserved extension — the extension-owner share, idempotent, and a
prerequisite for `register-tee`'s availability check (`setExtensionContracts` publishes the
sender and the verifier together):

```bash
cd tools && go run ./cmd/wire-extension -a ../config/coston2/deployed-addresses.json -c $CHAIN_URL
```

On a network whose machines are already in production, set the verifier **before** upgrading
tee-node to a version that reports the state: their periodic `confirmAvailability` goes through
the same check.

The scripts here only need the identity, which is fixed — `full-setup.sh` writes
`EXTENSION_ID=1` to `deploy/config/extension.env`:

```bash
cat deploy/config/extension.env
```

## 5. Build the Docker image

The image is built from the `Dockerfile` at the repo root, which is the Go module.

### Attestation mode

`MODE` selects the attestation backend:

| Value | Meaning | Used for |
|---|---|---|
| `0` | Production attestation | a real Confidential Space VM — **the image default** |
| `1` | Simulated attestation (test code hash) | the local simulation |

**The Dockerfile ships `MODE=0`**, the same default as the tee-node image, so the image a Confidential Space VM launches attests for real without any launch-time setting. The local compose stack sets `MODE=1` explicitly (`deploy/docker-compose.yaml`, `MODE=${MODE:-1}`), which is why the same image also runs against the simulation.

`MODE` is **not** in the `tee.launch_policy.allow_env_override` label, on purpose: the attestation backend is part of the attested identity. FTDC rejects simulated attestation, and a launch must not be able to turn it on without changing the image. Verify what ended up in the image before registering its hash on-chain — see the check at the end of this section.

Then build:

```powershell
$env:SOURCE_DATE_EPOCH = (git log -1 --format=%ct)
docker compose -f deploy/docker-compose.yaml build --no-cache extension-tee
docker tag <your-extension>-extension-tee:latest <your-extension>:v0.1.0
docker save <your-extension>:v0.1.0 -o <your-extension>-v0.1.0.tar
```

Setting `SOURCE_DATE_EPOCH` makes the build reproducible (same source → same `codeHash`). The image is reproducible **across machines** — see [REPRODUCIBILITY.md](../REPRODUCIBILITY.md) for what that guarantees before promising an auditor they can reproduce your hash.

Check what is baked into the image:

```powershell
docker inspect <your-extension>:v0.1.0 --format '{{range .Config.Env}}{{println .}}{{end}}' | Select-String MODE
# MODE=0
docker inspect <your-extension>:v0.1.0 --format '{{index .Config.Labels "tee.launch_policy.allow_env_override"}}'
# MODE must NOT appear in this list
docker inspect <your-extension>:v0.1.0 --format '{{json .Config.ExposedPorts}}'
# {"5501/tcp":{}} — Confidential Space opens every exposed port in the VM firewall;
# the sign server and the extension's /action bind loopback and must not be listed
```

## 6. Deploy the image on a Confidential Space VM

Hand off (or deploy yourself) to a GCP Confidential Space VM with:

- The image (tar or registry URL+tag)
- Workload-launch env: `INITIAL_OWNER`, `CHAIN_URL`, `EXTENSION_ID` (from Step 4), `PROXY_URL` (proxy URL reachable from the TEE)
- Public HTTPS routed to port `6664` of the proxy container

You receive back the **public proxy URL**. Add it to `.env.<chain>` and re-activate:

```bash
# in .env.<chain>
EXT_PROXY_URL=<public proxy URL>
```

```bash
bash ./deploy/scripts/use-chain.sh <chain>
```

## 7. Verify the proxy `/info`

```powershell
curl -s $env:EXT_PROXY_URL/info | jq '.machineData'
```

Required values:

| Field          | Expected                                                          |
| -------------- | ----------------------------------------------------------------- |
| `platform`     | starts with `0x4743505f414d445f534556…` (GCP_AMD_SEV)             |
| `codeHash`     | real measured hash (**not** `0x194844cf…` — that's simulated)     |
| `extensionId`  | matches your `deploy/config/extension.env` `EXTENSION_ID`                |
| `initialOwner` | matches your `INITIAL_OWNER`                                      |

If `extensionId` is wrong, ask the VM operator to restart the container with the correct `EXTENSION_ID` env override (no image rebuild needed — it's a launch-policy override).

## 8. Register the TEE machine

Run:

```bash
bash ./deploy/scripts/post-build.sh
```

- `allow-tee-version` whitelists the codeHash for your extension.
- `register-tee` pre-registers the TEE, requests attestation, runs the availability check and promotes the machine to production; a machine already in production is left alone.

## 9. End-to-end test

Getting a price on-chain takes three steps — publish the endpoint configuration
and the admin set to the machine(s), install the credentials, then run a keeper
cycle that requests an observation and relays the signed update into the
`TeeOracleFeedStore`. The store checks both commitment hashes, so a machine
missing either configuration cannot land an update.

Publish to every machine in one call each (`-tee` is a comma-separated list,
`-feedId` defaults to USDX/USD). On a production deployment these are
governance calls; the tools' direct-send path works before
`switchToProductionMode`:

```bash
cd tools && go run ./cmd/send-endpoints -config ../deploy/config/endpoints/public-only.json
```

```bash
cd tools && go run ./cmd/send-admins -config ../deploy/config/admins/dev-wellknown-keys.json
```

> `<STORE>` is the `TeeOracleFeedStore` address; the instructions sender is
> derived from the store's own pointer. `dev-wellknown-keys.json` names the
> addresses of private keys 1, 2 and 3 — local/testnet only, anyone can sign as
> those admins. It publishes two roles: `backing` (1-of-1, for `hex_api_key`)
> and `providers` (2-of-3, for the RPC credentials). Publish real sets before
> any deployment serving a live feed.

Then install the credentials. **Two uploads, by two different parties, under two
different admin sets** — the split is the point, so whoever holds the Hex
credential never needs an RPC key and neither of them needs this repo's
deployment context:

```bash
./deploy/scripts/upload-hex-key.sh
```

```bash
./deploy/scripts/upload-rpc-keys.sh
```

Both verify the machine against the chain before encrypting anything to it, read
every secret from the environment or a hidden prompt rather than argv, and
confirm afterwards from the machine's own published state. `upload-rpc-keys.sh`
uploads whatever the machine reports as `missingSecretRefs`, and hands the
envelope off when it does not hold enough admin keys to meet the `providers`
threshold.

> **For a live feed, follow [usdx-oracle-production-setup.md](usdx-oracle-production-setup.md)
> instead of the commands above** — it covers who holds which key, the multi-admin
> signing handoff, and what to do on restart and rotation. [testing.md](testing.md)
> is the same flow with throwaway keys.

> **Per machine, and lost on restart.** Each machine has its own TEE key, so an
> envelope built for one cannot be replayed into another — run both scripts once
> per machine, with that machine's proxy URL. Credentials live in memory only, so
> a restarted machine needs them again. `fleet-status` shows `missingSecretRefs`
> for the whole fleet at once.

Finally run one keeper cycle — it requests a feed update from the named machines,
polls each of them for its signed result, and submits the agreeing updates to the store:

```bash
./deploy/scripts/oracle-cycle.sh -tee <TEE1>,<TEE2>
```

Add `--dry-run` to run the preflight without submitting, or `--loop 10m` to keep
cycling on a timer. The full walkthrough, including what to check at each hop and
what each revert means, is in [testing.md](testing.md) under "Running the USDX
oracle end to end yourself".

---

## When the extension image changes

1. Rebuild and hand off the new image.
2. The VM is re-deployed → `codeHash` changes.
3. `bash ./deploy/scripts/post-build.sh` whitelists the new codeHash.
4. Run a keeper cycle to confirm the feed still advances:
   `./deploy/scripts/oracle-cycle.sh -tee <TEE1>,<TEE2>`.

## When the `FlareTeeManager` diamond is re-deployed

All extension registrations on that chain are wiped. Because the USDX extension
is a *reserved* id, it is re-registered by governance at the same id rather than
minted afresh:

1. Re-run the upstream deploy + governance follow-ups (Step 4) — `registerReserved`
   restores extension id `1`, and the sender/store addresses may change, so
   re-slot them into the addresses file.
2. The `EXTENSION_ID` handed to the VM is unchanged (`1`), so no container
   restart is needed unless the sender/store addresses moved.
3. Re-curl `/info` and confirm `extensionId` is still `1`.
4. `bash ./deploy/scripts/post-build.sh`.
5. Re-publish endpoints and admins (Step 9) — the wipe clears the published
   commitments — then run a keeper cycle.
