# USDX Oracle — Keeper Specification

**Status:** ✅ **BUILT — `tools/pkg/keeper`, `tools/cmd/keeper`** · **Raised:** 2026-08-05 ·
**Built:** 2026-08-05

_The cycle lives in `tools/pkg/keeper`: it triggers, polls with a bounded deadline and
exponential backoff, treats pending as pending, decodes `data` into `(body, signature)` and
relays the body verbatim. It preflights every store guard so a rejection arrives named —
`StaleConfig`, `NotNewer`, `TooOld` — instead of as a bare revert. `tools/cmd/keeper` wraps it
in the timer; `tools/cmd/submit-price` runs the same code once by hand, so what an operator
proves interactively is what runs unattended._

_**Not built, and tracked below:** replace-by-fee bumping, the ≥2-instance deployment, and the
chain-age watcher — which by design must run outside this process's failure domain._

_Companion docs: architecture and flow in `usdx-oracle-spec.md` §4 and WP5, keeper design notes
in `usdx-oracle-operations.html` §1, missed-update alerting in `usdx-oracle-monitoring.md`,
working plan in `usdx-oracle-plan.md`. Google-Doc-ready: `usdx-oracle-keeper.html`._

---

## Scope

The keeper is the only moving part between an instruction and a stored price. It does three
things, on a timer:

1. **Trigger** — call `requestPriceUpdate()` on `InstructionSender`.
2. **Collect** — poll the TEE proxy for the resulting `ActionResult`.
3. **Relay** — decode the signed payload and call `submitPrice()` on `UsdxPriceStore`.

Plus the keeper-side half of the alerting specified in `usdx-oracle-monitoring.md`.

That is the whole job. The functionality is simple; **the reliability semantics are the
substance of this document**, and they are where a straightforward implementation goes wrong.

### Non-goals

The keeper holds no authority. The store verifies the TEE signature against
`TeeMachineRegistry`, so a compromised keeper costs gas, not correctness. It must therefore
never:

- compute, derive, adjust or sanity-"fix" a price;
- cache a price, or resubmit an old result on a later cycle;
- submit anything from a `status != 1` result;
- hold a secret beyond its own hot-wallet key.

---

## The cycle

```
every T:                                  # single-flight — never overlap cycles
    # 1. trigger
    fee     = operationFees.getOperationFee(USDX_ORACLE, GET_PRICE)
    tx      = sender.requestPriceUpdate{value: fee}()
    receipt = waitMined(tx, deadline = T_request)
    id      = receipt.logs.first(PriceUpdateRequested).instructionId

    # 2. collect
    result  = poll(proxy, "/action/result/" + id.hex(),
                   deadline = T_poll, backoff = exponential,
                   until    = result.status in {0, 1})    # anything else is pending
    if result.status != 1:
        emit(cycle, errorClass = result.log)              # fail closed — relay nothing
        continue

    # 3. relay
    (body, signature) = abi.decode(result.data, (bytes, bytes))
    submit(store.submitPrice(body, signature), deadline = T_submit)
```

---

## Where each part lives

| Concern | Code |
| --- | --- |
| One cycle, trigger → collect → relay | `keeper.RunCycle` — `tools/pkg/keeper/cycle.go` |
| Store guards, named before gas is spent | `keeper.Preflight` — `tools/pkg/keeper/preflight.go` |
| Timer, single-flight, overrun handling | `keeper.Run` — `tools/pkg/keeper/keeper.go` |
| Cycle records and the outcome taxonomy | `keeper.Record`, `keeper.Outcome` — `tools/pkg/keeper/record.go` |
| Long-running process, signals, flags | `tools/cmd/keeper` |
| One cycle by hand, with read-back | `tools/cmd/submit-price` |

`RunCycle` never exits the process and returns a `Record` for every outcome, which is what
lets the same code serve a one-shot and a timer. Run it:

```
go run ./cmd/keeper -instructionSender 0x… -interval 10m -keeperId keeper-a
```

`-once` runs a single cycle and exits, for a cron-driven deployment. `-records <file>` appends
the JSON records somewhere other than stdout.

---

## Step 1 — trigger

### Getting the instruction ID

`requestPriceUpdate()` returns `bytes32 _instructionId` and emits it in
`PriceUpdateRequested(address indexed requester, bytes32 indexed instructionId)`. Both exist
because they serve different callers: the return value is for contract callers, and **a
transaction returns nothing to an off-chain caller**, so a keeper reads the event.

The registry's own `TeeInstructionsSent` carries the same value, but parsing it would couple
every caller to the registry's event ABI for a value our own contract already has.

Scan the receipt for the event rather than indexing by position — the receipt also contains the
registry's logs, and a positional assumption breaks the moment the emit order changes. The
keeper must fail the cycle loudly if no matching log is found, rather than proceeding with a
zero hash. `tools/pkg/utils/instructions.go` is the reference.

### The fee

`requestPriceUpdate()` is `payable` and forwards `msg.value` to the registry. Read the fee;
never hard-code it:

```
fee = operationFees.getOperationFee(OP_TYPE_USDX_ORACLE, OP_COMMAND_GET_PRICE)
      ?: operationFees.getDefaultFee()
```

The fee is governance-settable, and a stale constant fails closed in the worst way: every
request reverts, no price is published, and nothing in the repo changed to explain it. Take the
op type and command from the deployed sender's own public constants so the lookup cannot drift
from the instruction actually sent.

`calculateFeeByTeeIds` is the more precise call but is not usable here — `requestPriceUpdate`
selects its machine internally via `getRandomTeeIds`, so the caller cannot know the tee id in
advance.

Overpayment is recoverable — `requestPriceUpdate` sets `claimBackAddress: msg.sender`, so any
refund goes to the keeper's own wallet.

### One machine per request

`requestPriceUpdate()` calls `getRandomTeeIds(extensionId, 1)`. Each cycle is dispatched to
**one randomly selected machine**. Consequences the keeper must respect:

- A failed cycle may mean one bad machine, not a broken data source. This is why
  `usdx-oracle-monitoring.md` requires cross-machine correlation before a single `status == 0`
  is allowed to page.
- Running ≥2 keepers means ≥2 independent machine selections per interval, which is
  redundancy at the machine level as well as at the keeper level. That is a feature, and it is
  the reason the wasted gas from losing a race is worth paying.

What several machines buy today is **availability, not integrity**. `UsdxPriceStore` accepts a
signed price from any `PRODUCTION` machine registered to the extension; the first valid newer
report wins and the rest revert `NotNewer`. There is no cross-machine agreement, so a second
machine covers the first being down — it does not corroborate its number. Quorum across machines
would be a real change to the store, and worth being clear about what it would and would not fix:
every machine takes its configuration from the same owner key (findings §15), so it would defend
against one machine being compromised or malfunctioning, not against a hostile configuration
pointing all of them at the same bad provider.

### The direct channel

`keeper -direct` raises the action on the proxy (`POST <proxy>/direct`) instead of on-chain. It
saves the registry fee and one transaction per cycle.

It costs nothing in trust, and the reason is worth stating precisely: `GET_PRICE` grants no
authority. It signs the observation the enclave already made on its own timer — the enclave does
not observe on request — and `submitPrice` verifies that signature against `TeeMachineRegistry`
however the signature was asked for. A caller holding `DIRECT_API_KEY` can make a machine sign
its own latest observation more often than usual. It cannot make it sign anything else.

What it does cost is permissionlessness, which is why **both channels stay**:

| | instruction | direct |
| --- | --- | --- |
| Who can call it | anyone, by paying the registry fee | whoever holds `DIRECT_API_KEY` |
| Cost per cycle | fee + trigger tx + submit tx | submit tx |
| Which machine serves it | random, via `getRandomTeeIds` | the one behind the proxy URL |
| Result submission tag | `threshold` | `submit` |

The last two rows matter more than they look:

- **Machine selection.** With N machines an on-chain request goes to a random one, and its result
  is retrievable only from *that machine's* proxy. A keeper polling a single URL therefore misses
  roughly (N−1)/N of its own requests and reports `no_result`. The direct channel removes the
  ambiguity — you address a machine by addressing its proxy — which is what makes per-machine
  commissioning possible at all.
- **Submission tag.** A direct action is filed under `submit`, an instruction under `threshold`,
  and `GET /action/result/{id}` defaults to `threshold`. Ask for the wrong one and the endpoint
  returns 404 for the whole poll window; the cycle then reports a timeout that looks exactly like
  a dropped instruction.

---

## Commissioning a machine

A machine is *commissioned* once it has landed one price on-chain, which is what writes
`lastReport[teeId]` in `UsdxPriceStore` and makes `isProvenConfigured(teeId)` true.

This is deliberately a manual step rather than a contract mechanism. `submitPrice` already proves
both facts it needs — it recovered the signer, and it checked that signer's config hash against
the published one — so recording them costs three storage writes on a path that already runs. A
separate attestation entry point would be more machinery for something an operator does a handful
of times.

Order matters, because each step is what makes the next one possible:

1. Register the machine and get it to `PRODUCTION` in `TeeMachineRegistry`.
2. `send-endpoints -p <that machine's proxy>` — publishes its config and waits until the machine
   reports the matching `endpointsHash`. It will not report success until the machine is actually
   running what was published.
3. `usdxprov` — deposit its credentials. Each machine has its own TEE key, so envelopes are per
   machine and cannot be reused.
4. Confirm `ready: true` and an empty `missingSecretRefs` in `fleet-status`.
5. Land one price from *this* machine:
   ```
   go run ./cmd/submit-price -direct -p <that machine's proxy> -instructionSender 0x…
   ```
6. `fleet-status` should now show `commissioned yes` for it.

Two things to expect at step 5:

- **`NotNewer` when commissioning several machines back to back.** `observedAt` must strictly
  increase, and each machine stamps its own from its own observation timer. If machine B's cached
  observation is older than the one machine A just landed, B is rejected. Space the runs by about
  one `ObservationInterval` and retry. The cycle record distinguishes the two cases:
  `race_lost` means the store is ahead of B, `stale_submission` means B relayed something old.
- **Re-pushing a config decommissions the machine.** `isProvenConfigured` goes false the moment
  `setEndpoints` publishes something new, because the machine has not yet proven it runs the new
  config. That is correct and self-announcing — repeat step 5. `send-endpoints` prints the command
  on success for this reason.

`lastReport` is a record of **commissioning, not liveness**. Only the machine that wins a given
cycle updates its entry, so a stale `acceptedAt` means "has not won lately", not "unhealthy". For
liveness, read each machine's `GET /state` — which is what `fleet-status` does.

---

## Step 2 — collect

Poll `GET <proxy>/action/result/<instructionId>?submissionTag=<tag>` until the status is terminal.
The tag is `threshold` for an instruction and `submit` for a direct action; it is not optional,
because the endpoint's default silently 404s for the other kind.

| `status` | Meaning | Keeper action |
| --- | --- | --- |
| `1` | Success | Proceed to step 3 |
| `0` | Handler failed | Record the class from `log`, do not submit, end cycle |
| anything else | Still in progress | Keep polling until `T_poll` expires |

Three requirements the existing test helper (`fccutils.ActionResult`) does not meet, and a
keeper must:

- **A bounded deadline, not a fixed repeat count.** The helper sleeps 2 s a fixed number of
  times. A keeper needs a wall-clock deadline (`T_poll`) so the cycle's total duration is
  known and bounded — see the budget below.
- **Exponential backoff.** A tight fixed interval against the proxy for the whole poll window
  is wasteful and, with ≥2 keepers, doubles for no benefit.
- **Pending is not an error.** The helper retries on non-200 but has no concept of a *pending*
  result, which returns 200 with a non-terminal status. Treating pending as terminal will
  discard results that were merely slow.

A poll that expires without a terminal status is cause 3 in the monitoring taxonomy — TEE
down, proxy down, or instruction dropped. FCC is fire-and-forget: there is no delivery
guarantee, so this is an expected outcome, not an exceptional one. Record it and end the cycle.

---

## Step 3 — relay

### Decode before submitting

`ActionResult` has **no `signature` field**. The extension returns:

```
body = abi.encode(PriceReport)          # 6 static words, 192 bytes
data = abi.encode(body, signature)      # ActionResult.data
```

so the keeper must ABI-decode `result.data` into `(bytes body, bytes signature)` and pass both
to `submitPrice(body, signature)`. `report.DecodeData` in `go/internal/report/report.go` is
exactly this, and its doc comment names the caller: *"Used by tests and tooling; the store does
the equivalent on-chain."*

**The body must be relayed verbatim.** Never decode it into fields and re-encode. The store
verifies the signature over exactly the bytes it received and only then decodes them;
re-encoding would make acceptance depend on the keeper's encoder matching the TEE's byte for
byte, and any divergence would surface as an invalid signature rather than as the encoding bug
it is.

### Classifying the outcome

A revert is not necessarily a failure. `NotNewer` is the expected result for every keeper that
loses a race, and with ≥2 keepers **exactly one submission succeeds per cycle and the rest
revert `NotNewer`**. The full revert-classification table lives in `usdx-oracle-monitoring.md`
§"Cause 6"; the keeper's obligation is to distinguish, not to alert:

```
if revert == NotNewer:
    if store.observedAt >= our report's observedAt:
        record cycle as SUCCESS          # someone else's landed — the system working
    else:
        record cycle as STALE_SUBMISSION # our own report was old — a latency problem
```

---

## Reliability model

### The keeper is stateless, and that is the reliability argument

A keeper that crashes mid-cycle needs no recovery, no journal and no resumption, because
**every partial cycle is already harmless**:

| Crash point | Consequence | Recovery needed |
| --- | --- | --- |
| After the request tx | Instruction is dispatched; TEE computes; result sits in the proxy unread | None — one wasted fee |
| After polling, before submit | A signed result is discarded | None — the co-keeper submits it, or the next cycle supersedes it |
| After submit is broadcast | Tx lands or does not; either way the store's monotonicity check settles it | None |

Nothing in the cycle is a two-phase commit. Nothing needs to be exactly-once. The next cycle
supersedes the last one, and `observedAt` monotonicity makes duplicate submissions safe by
construction.

**Do not add persistence to make the keeper "more reliable".** A write-ahead log or a resumable
cycle adds a corruptible dependency and buys nothing that the ≥2-instance design and the next
tick do not already provide. The one thing the keeper does persist is its **emitted cycle
records** — write-only, for monitoring, never read back to make a decision.

### Single-flight

A cycle must never overlap its successor. If a cycle is still running when the timer fires,
**skip the tick** rather than starting a second cycle concurrently. Overlapping cycles produce
two in-flight instructions from one keeper, two nonce-consuming transactions, and a real chance
of submitting reports out of order — burning fees to accomplish nothing, since the store
accepts only the newer one.

### The deadline budget must fit inside `maxAge`

`submitPrice` reverts `TooOld` when `block.timestamp - reportedAt > maxAge`, and `reportedAt`
is stamped **inside the enclave** at the end of the observation, before signing. Every
subsequent step spends that budget:

```
observedAt ──────────────────────────────────────────────► submitPrice mined
             sign │ return to proxy │ T_poll │ build+send │ mine
             └──────────────── must stay under maxAge ─────────────┘
```

So the keeper's own timeouts are bounded by a store constant:

```
T_poll + T_submit  <  maxAge − (signing + proxy return latency + mining)
```

`deploy-store` defaults `maxAge` to **3600 s**, and `usdx-oracle-operations.html` suggests
`T_poll = T/2` — 300 s at the planned 10-minute cadence. That is comfortable today. It is
written down because it is not obviously coupled: **tightening `maxAge` toward the poll window
makes `TooOld` reachable on every slow cycle**, and the symptom would appear as a flaky feed
rather than as the configuration change it was.

### Stuck transactions

A submission that misses its window is not worth chasing — the next cycle supersedes it, and a
replacement landing after `maxAge` reverts `TooOld` anyway. So the implementation waits for the
receipt until `submitTimeout`, records `not_mined`, and gives up.

**Replace-by-fee is deliberately not implemented.** Bumping a transaction that is about to be
superseded buys nothing, and the bounded-ceiling machinery would be state the keeper otherwise
does not have.

What a stuck transaction *does* cost is the nonce behind it: a looping keeper eventually queues
every later cycle behind an unmined one, which a one-shot never sees. The cycle therefore
records `pendingNonceGap` — the distance between the mined and pending nonces — and the loop
logs it separately, because that failure degrades into a total outage rather than a bad cycle.
It is reported, not retried into: the fix is operational.

---

## Configuration

| Key | Notes |
| --- | --- |
| `T` | Cycle interval. Match the ~10-minute Hex refresh; polling faster costs a fee and gas per cycle and buys nothing. **Open** — spec WP5. |
| `T_request`, `T_poll`, `T_submit` | Per-step deadlines. Must satisfy the `maxAge` inequality above. |
| Instruction fee | Read from the registry, not hard-coded. See step 1. |
| `InstructionSender`, `UsdxPriceStore` addresses | From `config/<chain>/deployed-addresses.json`. |
| Proxy URL | For `/action/result/<id>`. |
| Chain RPC | The keeper's own endpoint — a failure here is cause 2, distinct from the extension's supply RPCs. |
| Hot wallet key | Low-value, monitored, funded. Compromise costs gas, not correctness. |
| `keeperId` | Stable identifier, so cross-keeper correlation and dedup can tell instances apart. |

---

## Monitoring hooks

The design, taxonomy and routing tables are in `usdx-oracle-monitoring.md`. What the keeper
owes it is one **cycle record per tick**, emitted regardless of outcome — implemented as
`keeper.Record`, one JSON object per line:

| Field | Emitted after |
| --- | --- |
| `keeperId`, `cycleStartedAt`, `cycleEndedAt` | always |
| `requestTxStatus`, `instructionId` | step 1 |
| `resultStatus` (`1` / `0` / `timeout`), `errorClass` | step 2 |
| `sourceHealth` | step 2, from proxy `GET /info` |
| `submitOutcome` (`accepted` / revert name / not attempted) | step 3 |
| `storeObservedAtAfter` | step 3 |

Two constraints carried over from the monitoring doc, because they are keeper-side:

- **The keeper must not be the only thing watching.** The page comes from on-chain feed age,
  evaluated by a process outside the keepers' failure domain — a dead keeper reports nothing,
  and its silence is indistinguishable from health.
- **`NotNewer` must never alert.** It is the designed outcome of the ≥2-instance topology.

---

## Gaps found while writing this — all fixed

Three, found by checking the existing cycle sketch against the deployed contracts. None was a
live bug — nothing here is built yet — but each would have been a defect the moment someone
implemented from the text as it stood. **All three were fixed on 5 Aug 2026**; recorded here
because the reasoning explains why the code looks the way it does.

| # | Gap | Fix |
| --- | --- | --- |
| **1** | `id = sender.requestPriceUpdate{value: fee}()` — the function returned `void`, and its `PriceUpdateRequested` event carried only the caller, so the ID was reachable only by parsing the registry's `TeeInstructionsSent` out of the receipt. | `requestPriceUpdate()` now returns the id and emits it in `PriceUpdateRequested`. Tooling reads our own event. |
| **2** | `store.submitPrice(result.data, result.signature)` — `ActionResult` has no `signature` field, and the E2E test still JSON-decoded the Phase-2 stub shape, so it only passed against a stub extension. | `run-test` now ABI-decodes `data` into `(body, signature)` and asserts the domain, the instruction binding, the signature form and the config hash. |
| **3** | The instruction fee was a hard-coded `instructionFeeWei` constant. Fine for a test, a silent feed-stopper in a keeper. | Both instruction paths now read the fee from `OperationFees`, with the op constants taken from the deployed sender. |

**Not fixed, and deliberately:** `setEndpoints` still emits `EndpointsSet(address,bytes32)`
without an instruction id, so its tooling continues to scan the registry event. Changing that
signature would break the `endpointsSetTopic` constant that other tooling matches on, for a path
no keeper runs on a timer. Worth doing if that event is ever revised for another reason.

---

## To decide

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 1 | Cycle interval `T`, and the fee/gas budget it implies. Straw man in spec WP5 is 5 min; the plan argues for ~10 to match the source refresh. | Engineering | Build |
| ~~2~~ | ~~Should `PriceUpdateRequested` carry the instruction ID?~~ **Done 5 Aug 2026** — it does, and `requestPriceUpdate()` returns it too. | Engineering | Closed |
| 3 | Where does the keeper run, and who operates it? Open as spec WP5.7. Interacts with the monitoring doc's requirement that the chain-age watcher sit in a different failure domain. | Infra | Build |
| 4 | Is an immediate off-schedule update on large deviation wanted? Straw man in spec WP5. Shares an owner with the deviation guard, spec §6.2. | _TBD — risk owner_ | No |
| 5 | `maxSkew` has no `deploy-store` flag and no value in the generated binding, so its deployed value is currently undetermined. Confirm before the budget inequality can be checked end to end. | Engineering | No |

Questions 1 and 4 share an owner with the deviation guard (spec §6.2) and the fail-closed
question in `usdx-oracle-availability.md`.
