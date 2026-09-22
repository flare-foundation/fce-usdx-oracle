# USDX Oracle — Keeper Specification

**Status:** ✅ **BUILT — `tools/pkg/keeper`, `tools/cmd/keeper`** · **Raised:** 2026-08-05 ·
**Built:** 2026-08-05

_The cycle lives in `tools/pkg/keeper`: it triggers, polls with a bounded deadline and
exponential backoff, treats pending as pending, decodes `data` into the signed feed update and
relays it. It preflights every store guard so a rejection arrives named — `StaleEndpoints`,
`NotNewer` — instead of as a bare revert. `tools/cmd/keeper` wraps it in the timer;
`deploy/scripts/oracle-cycle.sh` runs the same code once by hand, so what an operator proves
interactively is what runs unattended._

_Every cycle raises its request on-chain through `requestFeedUpdate(feedId, teeIds)`, naming
the target machines explicitly. Each machine answers on its own proxy; the updates that agree on
`observedAt` go up together in one `submitFeedUpdates` call, and each update's own signature says
which machine produced it. Before paying the fee it asks those machines over
`GET_READINESS` whether they would sign at all — a fee spent on a machine with a stale
observation buys a predictable failure._

_**Not built, and tracked below:** replace-by-fee bumping, the ≥2-instance deployment, and the
chain-age watcher — which by design must run outside this process's failure domain._

_Companion docs: architecture and flow in `usdx-oracle-spec.md` §4 and WP5, missed-update
alerting in `usdx-oracle-monitoring.md`._

---

## Scope

The keeper is the only moving part between an instruction and a stored feed value. It does
three things, on a timer:

1. **Trigger** — raise `GET_FEED` through `requestFeedUpdate(feedId, teeIds)` on the
   `TeeOracleInstructionsSender` ("the sender").
2. **Collect** — poll each target machine's proxy for its `ActionResult`.
3. **Relay** — decode the signed payloads, keep the ones that agree on `observedAt`, and call
   `submitFeedUpdates()` on the `TeeOracleFeedStore` ("the store") with all of them.

Plus the keeper-side half of the alerting specified in `usdx-oracle-monitoring.md`.

That is the whole job. The functionality is simple; **the reliability semantics are the
substance of this document**, and they are where a straightforward implementation goes wrong.

### Non-goals

The keeper holds no authority. The store verifies the TEE signature through
`Fdc2Verification` against the TEE machine registry — the signer must be a registered
PRODUCTION machine on the store's extension — so a compromised keeper costs gas, not
correctness. Anyone may submit; colliding submissions are harmless, exactly one wins per
cycle. It must therefore never:

- compute, derive, adjust or sanity-"fix" a price;
- cache a price, or resubmit an old result on a later cycle;
- submit anything from a `status != 1` result;
- hold a secret beyond its own hot-wallet key.

---

## The cycle

```
every T:                                  # single-flight — never overlap cycles
    fleet = machineRegistry.getActiveTeeMachines(store.extensionId())   # re-read each tick

    # 0. preflight — ask before paying
    if all(not readiness(m).wouldSign for m in fleet):     # best effort; unknown means "try"
        emit(cycle, outcome = "request_skipped")           # no fee spent
        continue

    # 1. trigger
    fee     = operationFees.getOperationFee(TEE_ORACLE, GET_FEED) × len(teeIds)
    tx      = sender.requestFeedUpdate{value: fee}(feedId, teeIds)
    receipt = waitMined(tx, deadline = T_request)
    id      = receipt.logs.first(FeedUpdateRequested).instructionId

    # 2. collect — every addressed machine files its own result on its own proxy
    proxies = every target machine's url
    results = poll(proxies, "/action/result/" + id.hex() + "?submissionTag=threshold",
                   deadline = T_poll, backoff = exponential,
                   until    = every proxy answered with status in {0, 1})   # else pending
    updates = [abi.decode(r.data, (FeedUpdate, Signature)) for r in results if r.status == 1]
    if not updates:
        emit(cycle, errorClass = first status-0 log)        # fail closed — relay nothing
        continue

    # 3. relay — one submission, one signed update per contributing machine
    screened = [u for u in updates                        # each answer judged on its own:
                if signer(u) in teeIds                    # a failing one is dropped and named,
                and u.extensionId == store.extensionId()  # the rest go on
                and u.endpointsHash == sender.latestEndpointsHash(feedId)
                and u.adminsHash == sender.latestAdminsHash(feedId)]
    batch = largest group of screened sharing one observedAt, one per signer
    preflight(batch)                    # requiredSignatures, the ratchet, then
                                        # eth_call submitFeedUpdates(batch)
    submit(store.submitFeedUpdates(batch), deadline = T_submit)
```

---

## Where each part lives

| Concern | Code |
| --- | --- |
| One cycle, trigger → collect → relay | `keeper.RunCycle` — `tools/pkg/keeper/cycle.go` |
| Trigger and collect, sending nothing | `keeper.Collect` — `tools/pkg/keeper/cycle.go` |
| Preflight and submit | `keeper.Relay` — `tools/pkg/keeper/cycle.go` |
| Store guards, named before gas is spent | `keeper.Preflight` — `tools/pkg/keeper/preflight.go` |
| Timer and fleet refresh | `keeper.Run` — `tools/pkg/keeper/keeper.go` |
| The readiness preflight | `keeper.wouldRefuse` — `tools/pkg/keeper/readiness.go` |
| Listing the fleet | `keeper.ActiveMachines`, `fleet` — `tools/pkg/keeper/fleet.go` |
| Cycle records and the outcome taxonomy | `keeper.Record`, `keeper.Outcome` — `tools/pkg/keeper/record.go` |
| Long-running process, signals, flags | `tools/cmd/keeper` |
| One cycle by hand, with read-back | `deploy/scripts/oracle-cycle.sh` (builds `tools/cmd/keeper` with `-once`) |

`RunCycle` never exits the process and returns a `Record` for every outcome, which is what
lets the same code serve a one-shot and a timer. It is `Collect` then `Relay`, split so a caller
can inspect a decoded update before deciding to submit it — which is what `oracle-cycle.sh
--dry-run` does; the record then carries the outcome `dry_run`. Run it:

```
go run ./cmd/keeper -tee a,b,c -interval 10m -keeperId keeper-a
```

The store resolves from `-store` or the `TeeOracleFeedStore` entry in the addresses file, and
the instructions sender is derived from the store's own `instructionsSender()` unless given
explicitly with `-instructionSender`. `-once` runs a single cycle and exits, for a cron-driven
deployment where the process is what repeats. `-records <file>` appends the JSON records
somewhere other than stdout.

### Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `-tee` | — required | Comma-separated TEE machine addresses to request from. The sender dispatches to these, and the fee scales with the count. |
| `-feedId` | the USDX/USD id | The feed to observe; must match the store's. |
| `-store` | `TeeOracleFeedStore` entry of the addresses file | The store to submit to. The sender is derived from its `instructionsSender()` unless `-instructionSender` is given. |
| `-interval` | `10m` | How often a cycle starts. Match the source refresh; faster costs a fee per cycle and buys nothing. |
| `-pollTimeout` | `5m` | How long to wait for the machines' terminal `ActionResult`s. The wait ends early once every addressed machine has answered; a machine that never answers costs the whole timeout, and the cycle goes on with the answers it has. |
| `-submitTimeout` | `3m` | How long to wait for the `submitFeedUpdates` receipt. |
| `-requestTimeout` | `2m` | How long to wait for the `requestFeedUpdate` receipt. Cycles are single-flight, so this is the bound that keeps an unmined trigger from stopping the keeper rather than costing it one cycle. |
| `-minBalance` | `5` | Warn each cycle when the paying account falls below this many coins. `0` disables it. A keeper that runs dry keeps cycling and failing at the request, which reads like a chain fault rather than an empty wallet. |
| `-directApiKey` | `$DIRECT_API_KEY` | Key for the `GET_READINESS` preflight. Without it every cycle pays the fee unasked. |
| `-p` | `$EXT_PROXY_URL` | Where the result is looked for first, and the machine the readiness preflight asks. Given explicitly, overrides *every* machine's registered url (right only with one machine); otherwise each machine is reached at its own registered url and this is the fallback when the registry lists none. `oracle-cycle.sh` passes it only when `PROXY_OVERRIDE` is set. |

`-directApiKey` is **not** required — the preflight is a saving, not a gate, and a cycle without
it simply requests. The keeper warns at startup when it is missing, because the symptom otherwise
is fees quietly spent on machines that were always going to refuse. The key reaches the proxy's
`/direct` endpoint, which on this extension carries `GET_READINESS` and `SET_SECRET`; a feed
update never goes that way.

---

## Step 1 — trigger

### Getting the instruction ID

`requestFeedUpdate(feedId, teeIds)` returns `bytes32 _instructionId` and emits it in
`FeedUpdateRequested(address indexed requester, bytes21 indexed feedId, bytes32 indexed
instructionId)`. Both exist because they serve different callers: the return value is for
contract callers, and **a transaction returns nothing to an off-chain caller**, so a keeper
reads the event.

The registry's own `TeeInstructionsSent` carries the same id, but the sender's own event is
preferred: parsing the registry's logs would couple every caller to the registry's event ABI,
and the sender's event also proves which feed was requested.

Scan the receipt for the event rather than indexing by position — the receipt also contains the
registry's logs, and a positional assumption breaks the moment the emit order changes. The
keeper must fail the cycle loudly if no matching log is found, rather than proceeding with a
zero hash. `tools/pkg/utils/instructions.go` is the reference.

### The fee

`requestFeedUpdate()` is `payable`; `msg.value` is the instruction fee, and it is what bounds
spam on the permissionless path. Read the fee; never hard-code it:

```
fee = operationFees.getOperationFee(TEE_ORACLE, GET_FEED) × len(teeIds)
      ?: operationFees.getDefaultFee() × len(teeIds)
```

The fee is governance-settable, and a stale constant fails closed in the worst way: every
request reverts, no feed update is published, and nothing in the repo changed to explain it.
The op type and commands are file-level `bytes32 constant`s in the upstream
`ITeeOracleInstructionsSender` — shared by every deployed instance — so the lookup keys on
exactly the identifiers the instruction carries; the same constants are pinned in
`internal/config`, and a mismatch prices the wrong row and the instruction is rejected for
an underpaid fee. Upstream's deployment leaves the `TEE_ORACLE` rows unset, so the default fee
is the normal state on a fresh deployment; the lookup falls back to it rather than erroring.

The fee scales with the target count — one request dispatches to every named machine, so a
single-machine fee would underpay a fleet-wide request and revert.

### Named machines per request

`requestFeedUpdate(feedId, teeIds)` dispatches the request to **the machines the caller
names** — there is no random selection any more, and `-tee` is what routes the cycle. Every
named machine answers, and each result is retrievable only from that machine's own proxy.
Consequences the keeper must respect:

- **Poll every target's proxy, and wait for all of them.** Each machine's result lives on its own
  proxy. `Config.ResultProxies` is filled from the registry each tick; the round ends when every
  addressed machine has answered or the poll timeout passes, and the record names the first proxy
  to answer.
- **`teeIds` come from the signatures, not the request.** The recovered signers are the only thing
  that says which machines actually produced the updates. An answer whose signature recovers to a
  machine the cycle did not ask is dropped before submission: a proxy can only offer a signature
  the store would refuse anyway, and it must not cost the honest answers their round.
- Every target must already have endpoints AND admins published for the feed, or the request
  reverts `TeeIdNotConfigured` before any fee is spent. `isTeeIdConfigured(feedId, teeId)` is
  the check, and `fleet-status` reads it for the whole fleet.
- A failed cycle may mean one bad machine, not a broken data source. This is why
  `usdx-oracle-monitoring.md` requires cross-machine correlation before a single `status == 0`
  is allowed to page.
- Running ≥2 keepers means ≥2 independent requests per interval, which is redundancy at the
  keeper level. That is a feature, and it is the reason the wasted gas from losing a race is
  worth paying.

What several machines buy today is **availability, not integrity**. The store accepts a signed
feed update from any `PRODUCTION` machine registered to the extension; the first valid newer
update wins and the rest revert `NotNewer`. There is no cross-machine agreement, so a second
machine covers the first being down — it does not corroborate its number. Quorum across machines
would be a real change to the store, and worth being clear about what it would and would not fix:
every machine takes its configuration from the same governance publication (findings §15), so
it would defend against one machine being compromised or malfunctioning, not against a hostile
configuration pointing all of them at the same bad provider.

### One channel, and why

The proxy also exposes `POST <proxy>/direct`, which raises an action on **that** machine without
a fee or a transaction. `GET_FEED` used to be served over it and no longer is: the extension
refuses a direct `GET_FEED` with `wrong action type`.

The saving was real — at a 10-minute interval, paying the registry fee ~144 times a day instead
of never — and it bought the one thing the on-chain path cannot give you, which is the ability to
address a **named** machine. What it cost is the property the on-chain path exists for:

| | instruction (`requestFeedUpdate`) | direct (removed for `GET_FEED`) |
| --- | --- | --- |
| Who can call it | anyone, by paying the instruction fee | whoever holds `DIRECT_API_KEY` |
| Cost per cycle | fee + trigger tx + submit tx | submit tx |
| Which machine serves it | the machines named in the request | the one behind the proxy URL |
| On the record | the request is a transaction | nothing on-chain |

A direct `GET_FEED` could not forge a price — the enclave signs only what it observed on its own
timer, and `submitFeedUpdates` verifies that signature against the TEE machine registry however
it was asked for. What it did was hand the same signed update to anyone holding one API key, for
free and with no on-chain trace of the request. Running the feed that way means the permissionless
path is the one nobody uses, and a path nobody uses is a path nobody notices has broken.

So there is one channel now, and every cycle exercises it.

`/direct` itself stays enabled and stays keyed: `SET_SECRET` and `GET_READINESS` — both
commands under the `TEE_ORACLE` op type — have no on-chain counterpart and still go through it.

### Keeping the fleet list current

The list is re-read every tick, so machines added or suspended mid-run are picked up without a
restart; a registry read that fails keeps the previous list, because an empty fleet would stop
the feed. Machines the registry lists **without a URL are dropped**, with a warning — there is
nowhere to poll for their results.

### The readiness preflight

Before paying, the keeper asks every machine it is about to request from over `GET_READINESS`
whether it would sign right now. If *all* of them say no, the cycle ends `request_skipped` with
no fee spent. All of them, because a fee spent entirely on machines that will refuse buys a
predictable failure — one willing machine is enough to make the request worth paying for.

Every uncertainty resolves to "request anyway": no API key, no reachable machine, a failed query.
"Not known" is not "do not try", and a preflight that suppressed real requests would be worse
than the fee it saves.

Every machine is asked, concurrently, rather than stopping at the first that would sign. A
readiness query costs no fee and no gas, so asking them all costs one round trip's latency
instead of the slowest sum — and it is the only way the cycle record can say *which* machine saw
a source fail. Two machines read the same providers independently and routinely disagree about
one of them, so `sourceHealth` is a map per machine, keyed by teeId:

```json
"sourceHealth": {
  "0x708f515f87da98E424c352030Cce4f9DdF0db5ab": { "rpc_flare_3": "lagging", "...": "ok" },
  "0x962d1D7264A2385d083B64a58eC18052470232bD": { "rpc_flare_3": "ok",      "...": "ok" }
}
```

A machine present with an empty map answered but has observed nothing yet; one that is absent
could not be asked at all, which is never the same as healthy. Records written before this
change carry a single flat map, which was whichever machine happened to answer first.

---

## Threshold submissions

The store's submission policy (`getSubmissionPolicy()`, governance-set) says how many distinct
machines must sign one submission — `requiredSignatures`, 1 to 32 — and how far their values may
diverge (`maxSpreadBIPS` of the median plus `maxSpreadAbsolute`). One `submitFeedUpdates` call
carries one `SignedFeedUpdate{feedUpdate, signature}` per contributing machine; the store checks
the batch's shape, takes the median, stores it, and names the machines whose value lay beyond the
bound in a `FeedOutliers` event. A one-element batch behaves exactly as a single submission always
did.

What that means for the keeper:

- **Every update in a batch must carry the same `observedAt`**, or the store reverts
  `ObservedAtMismatch`. The enclave signs the timestamp of the on-chain event that requested the
  update, which is how the store defines the field, so machines answering one request agree by
  construction. The keeper still groups the decoded updates by `observedAt` and submits the
  largest group (the newest on a tie): another keeper's request in a different block carries a
  different stamp, and an answer to it is left out rather than sinking this round.
- **Every answer is judged on its own before the batch is formed.** A signature that recovers
  to nobody or to a machine the cycle did not ask, a wrong extension or feed, a configuration
  commitment behind the feed's latest publication: each such answer is dropped and named in the
  record's `dropped` list with the guard that would have rejected it, and the rest go on. One
  machine that missed a configuration push costs itself the round, not the fleet. A round with
  nothing left is rejected under the one guard everything failed, or `NoUsableUpdate`.
- **Fewer agreeing machines than `requiredSignatures`** is a named preflight rejection,
  `NotEnoughSignatures`, before any gas is spent. The record's `teeIds` says who made the batch
  and `dropped` who answered but was left out.
- **The preflight ends with the contract's own dry run.** An `eth_call` of `submitFeedUpdates`
  with the batch enforces every rule a real submission would — signatures and registry status,
  the pause, the spread bound — and a revert is decoded to its guard name, so `SpreadTooBig`
  arrives as a named rejection without the keeper reimplementing the aggregation.
- **A machine that never answers costs the poll timeout, not the cycle.** The round waits for
  every addressed machine until `-pollTimeout`, then goes on with what arrived.

---

## Commissioning a machine

A machine is *commissioned* once it has signed one feed update the store accepted — its teeId
appears in a `FeedUpdated` event. The store keeps no per-machine bookkeeping any more: one
global `observedAt` stamp is all it holds, so commissioning is an event-log fact, not a storage
one. This is deliberately lean: `submitFeedUpdates` already proves both facts it needs — it
verified the signature through the machine registry, and it checked the signer's configuration
commitments against the published ones — so a separate attestation entry point would be more
machinery for something that falls out of the normal one.

**Everything addresses specific machines now.** `requestFeedUpdate(feedId, teeIds)` takes the
target list, so commissioning a particular machine is a matter of naming it in `-tee`; there is
no waiting for a random turn. `isTeeIdConfigured(feedId, teeId)` is the sender's check that both
commitments are published for the feed, and it is the precondition `requestFeedUpdate` enforces
per target — a machine without configuration cannot even be asked, which is what makes
commissioning state visible before any fee is spent.

Order still matters when bringing a machine up, because each step is what makes the next
possible:

1. Register the machine and get it to `PRODUCTION` in the TEE machine registry —
   `post-build.sh` runs `allow-tee-version` → `set-governance` → `register-tee`.
2. `send-endpoints -feedId <feed>` — publishes the endpoint configuration to every active machine and
   waits until the machine reports the matching `endpointsHash`. It will not report success
   until the machine is actually running what was published.
3. `send-admins -feedId <feed>` — same for the admin signer sets. The store
   requires both commitments, so a machine with endpoints but no admin set still cannot land an
   update — it reverts `NoAdminsPublished`.
4. `upload-hex-key.sh` and `upload-rpc-keys.sh` — deposit its credentials. Two uploads under
   two admin sets: the backing set governs `hex_api_key`, the providers set governs the RPC
   credentials. Each machine has its own TEE key, so envelopes are per machine and cannot be
   reused.
5. Confirm `configured`, `ready: true` and an empty `missingSecretRefs` in `fleet-status` —
   which reads the sender's commitments on-chain and asks the machine directly over
   `GET_READINESS`, and so does not depend on it having landed anything.
6. Let the keeper run with the machine in `-tee`; `oracle-cycle.sh -tee <machine>` drives one
   cycle by hand. Its first accepted update is its commissioning.

Two things to expect at step 6:

- **`NotNewer` while a fleet settles.** `observedAt` must strictly increase, and each machine
  stamps its own from its own observation timer. A machine whose cached observation is older than
  the one already stored is rejected. The record distinguishes the cases: `race_lost` means the
  store is ahead, `stale_submission` means the machine relayed something old. Both resolve on
  their own within about one observation interval.
- **Re-pushing a configuration strands the machine on the old one until it applies the new.**
  The sender's `latestEndpointsHash` moves the moment `setEndpoints` publishes, so a machine
  still running the previous configuration fails every submission with `StaleEndpoints` until
  it catches up. `send-endpoints` checks delivery and says so on success; `fleet-status`
  reconciles the fleet.

For liveness, ask each machine over `GET_READINESS` — which is what `fleet-status` does.

---

## Step 2 — collect

Poll `GET <proxy>/action/result/<instructionId>?submissionTag=threshold` until the status is
terminal. `threshold` is the tag an instruction's result is filed under, and it happens to be the
endpoint's default — but pass it explicitly: a readiness query files its result under `submit`,
and asking for the wrong one 404s for the whole poll window rather than erroring.

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
update = FeedUpdate struct                  # 7 static words, 224 bytes
data   = abi.encode(update, signature)      # ActionResult.data — 10 words, 320 bytes
```

so the keeper ABI-decodes `result.data` into the update and its signature and hands them on as
one `SignedFeedUpdate{feedUpdate, signature}` element of `submitFeedUpdates`. `feedupdate.Decode` in
`tools/pkg/feedupdate` is exactly this: it decodes through the extension's own
`pkg/wire/feedupdate`, so there is one layout, and adds what the enclave never does with its
own output, recovering the machine that signed. The layout is pinned to Solidity's bytes in
`deploy/testdata/wire`.

**The old verbatim rule is retired for this payload.** Every `FeedUpdate` field is static, so
its ABI encoding is canonical — exactly one valid byte string per set of values — and the store
itself re-encodes from calldata. The rule still binds for the dynamic `SET_ENDPOINTS` and
`SET_ADMINS` payloads, which must never be re-serialised by a relayer.

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

### The deadline budget is a liveness budget, not an acceptance one

The upstream store has no acceptance window — no maximum-age bound, no minimum-age bound. Its
only time rules are that `observedAt` strictly increases (`NotNewer`) and is strictly older than the
accepting block (`TooFarAhead`), and `observedAt` is the timestamp of an on-chain event emitted
during observation — always in the past by construction — so `TooFarAhead` is unreachable for
an honestly produced update. A slow cycle therefore does not revert; it just loses the race to
a fresher one. `T_poll` and `T_submit` bound liveness, not acceptability: keep them tight so a
cycle lands before the next one supersedes it, not because the store will reject a late one.

### Stuck transactions

A submission that misses its window is not worth chasing — the next cycle supersedes it. So the
implementation waits for the receipt until `submitTimeout`, records `not_mined`, and gives up.

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
| `T_request`, `T_poll`, `T_submit` | Per-step deadlines. Liveness bounds, not acceptance bounds — see the deadline budget above. |
| Instruction fee | Read from `OperationFees` (`TEE_ORACLE`, `GET_FEED`), not hard-coded; scales with the target count. See step 1. |
| Feed id | `0x21555344582f555344000000000000000000000000` — category 0x21 + "USDX/USD", 21 bytes. Named in the request and inside the signed update; a mismatch with the store reverts. |
| `TeeOracleInstructionsSender`, `TeeOracleFeedStore` addresses | From `deploy/config/<chain>/deployed-addresses.json`; the keeper resolves the store entry and derives the sender from the store's `instructionsSender()`. |
| Target machines | The `-tee` list — named explicitly in every request. |
| Proxy URLs | Read from the registry per machine, for `/action/result/<id>`. `EXT_PROXY_URL` is looked at first and is the fallback when the registry lists none; `-p` (the wrapper's `PROXY_OVERRIDE`) overrides all of them. |
| Chain RPC | The keeper's own endpoint — a failure here is cause 2, distinct from the extension's supply RPCs. |
| Hot wallet key | `KEEPER_KEY`: low-value, monitored, funded. Both of the keeper's transactions are permissionless and the store verifies TEE signatures against the machine registry itself, so this address has no authority at all — compromise costs the gas on it. Falling back to `EXTENSION_OWNER_KEY` works and is warned about; that key can also configure the extension, which nothing here needs. |
| `keeperId` | Stable identifier, so cross-keeper correlation and dedup can tell instances apart. |

---

## Running it unattended

Everything a system administrator needs to run the keeper as a service. It is a long-running
process that holds one key, keeps no state and listens on nothing.

**What to install.** One binary, `go build ./cmd/keeper` from `tools/`, and one file from the
repository: `deploy/config/<chain>/deployed-addresses.json`, which is the only repository file it
reads. Nothing else from the tree is needed at runtime.

**Environment.** Two secrets and three settings:

| Variable | |
| --- | --- |
| `KEEPER_KEY` | the only secret the process requires. Both of its transactions are permissionless and the store verifies TEE signatures against the machine registry itself, so this address has no authority: it holds gas and nothing else. Without it the keeper falls back to `EXTENSION_OWNER_KEY` and warns — that key can also configure the extension, and no part of this process needs to. |
| `DIRECT_API_KEY` | optional. Buys the readiness preflight; without it every cycle pays the registry fee without asking the machines first whether they would sign. |
| `CHAIN_URL` | the chain node, as an absolute URL. |
| `ADDRESSES_FILE` | absolute path to the addresses JSON. The compiled-in default resolves against the source tree as it stood at **build** time, which does not exist on a server, so this must be set explicitly. |
| `EXT_PROXY_URL` | where results are looked for first, and the machine the preflight asks. Each machine is otherwise reached at the URL it registered. |

`-tee` is the one required argument: the machines to request from. `-keeperId` defaults to the
hostname and only matters when two keepers run.

**Network.** Outbound HTTPS to the chain node and to each machine's proxy. No inbound ports, no
listening socket.

**Logs.** Everything goes to **stdout** — the human log lines and the JSON cycle records
together, interleaved. Anything that parses the records should therefore be given `-records
<file>`, which writes them to a file of their own (roughly 100 KB a day at a ten-minute
interval, and it needs rotating); otherwise a consumer has to select the lines beginning with
`{`. The log level is DEBUG and there is no flag to change it.

**Stopping and restarting.** SIGTERM cancels the cycle in flight, which unwinds at its next
checkpoint and exits **0**; a second SIGTERM kills a process still winding down, so allow around
90 seconds before sending it. A startup misconfiguration — an unreadable addresses file, a
missing `-tee`, a chain node that does not answer — exits **1** before any cycle runs, and
restarting will not fix it; the log line above the exit names the cause.

A restart is always safe: cycles are stateless, `observedAt` on chain is a ratchet, and a
duplicate submission is rejected rather than harmful. So a supervisor that restarts on exit is
right. A failed cycle is not an exit — the process logs it, records it and waits for the next
tick.

**Funds.** The keeper spends and never tops itself up. Every cycle records `keeperBalance` in wei
and warns below `-minBalance` (default 5 coins). At the measured ~0.2 CFLR per cycle on Coston, a
ten-minute interval is about 29 CFLR a day.

**Why it does not hang.** Every wait is bounded: the chain client caps each request at 30 s —
`ethclient.Dial` leaves that unbounded, which is the default `support.DialChain` deliberately
replaces — and the trigger, the result poll and the submission each carry their own deadline. A
node that stops answering mid-call, or a transaction that is broadcast and never mined, costs one
cycle rather than the process.

**What to alert on**, from the records rather than from the process: `pendingNonceGap` above zero
for more than a cycle or two, `keeperBalance` approaching the floor, and — the only one that
matters to consumers — the age of `observedAt` on chain.

---

## Monitoring hooks

The design, taxonomy and routing tables are in `usdx-oracle-monitoring.md`. What the keeper
owes it is one **cycle record per tick**, emitted regardless of outcome — implemented as
`keeper.Record`, one JSON object per line:

| Field | Emitted after |
| --- | --- |
| `keeperId`, `cycleStartedAt`, `cycleEndedAt` | always |
| `requestTxStatus` (`mined` / `failed`), `instructionId` | step 1 |
| `resultStatus` (`1` / `0` / `timeout`), `errorClass` | step 2 |
| `proxy` — the first machine to answer; `teeIds`, `prices`, `observedAt` — who made the batch and what each signed | steps 2–3 |
| `sourceHealth` | step 1, the readiness preflight — one map per machine, keyed by teeId |
| `dropped` — the answers left out of the batch, each with the store guard it would have failed | step 3 |
| `submitOutcome` (`accepted` / revert name / not attempted) | step 3 |
| `storeObservedAtAfter` | step 3 |

`teeIds` are recovered from the updates' own signatures, which is the only thing that names the
machines that actually answered. `prices` lists each contributor's value at its own scale, in the
same order — the keeper never aggregates them; the store takes the median. Together with `proxy`,
they are what attribute a failure to one machine rather than to the feed. `dropped` completes the
picture: a machine that answered under a stale configuration, or a signature from a machine the
cycle never asked, appears there by guard name while the batch went up without it.

Two constraints carried over from the monitoring doc, because they are keeper-side:

- **The keeper must not be the only thing watching.** The page comes from on-chain feed age,
  evaluated by a process outside the keepers' failure domain — a dead keeper reports nothing,
  and its silence is indistinguishable from health.
- **`NotNewer` must never alert.** It is the designed outcome of the ≥2-instance topology, and
  it is reported as `race_lost` rather than a failure. `stale_submission` is the one to watch:
  same revert, but the store was *behind* the report, so something relayed something old.

---

## Gaps found while writing this — all fixed

Three, found by checking the existing cycle sketch against the deployed contracts. None was a
live bug — nothing here is built yet — but each would have been a defect the moment someone
implemented from the text as it stood. **All three were fixed on 5 Aug 2026**; recorded here
because the reasoning explains why the code looks the way it does.

| # | Gap | Fix |
| --- | --- | --- |
| **1** | `id = sender.requestFeedUpdate{value: fee}()` — the function returned `void`, and its `FeedUpdateRequested` event carried only the caller, so the ID was reachable only by parsing the registry's `TeeInstructionsSent` out of the receipt. | `requestFeedUpdate()` now returns the id and emits it in `FeedUpdateRequested`. Tooling reads the sender's own event. |
| **2** | `store.submitFeedUpdate(result.data, result.signature)` — `ActionResult` has no `signature` field, and the E2E test still JSON-decoded the Phase-2 stub shape, so it only passed against a stub extension. | The keeper / `oracle-cycle.sh` now ABI-decodes `data` into the signed feed update and asserts the domain, the feed binding, the signature form and both configuration commitments. |
| **3** | The instruction fee was a hard-coded `instructionFeeWei` constant. Fine for a test, a silent feed-stopper in a keeper. | Both instruction paths now read the fee from `OperationFees`, with the op constants pinned to the vendored interface. |

**Not fixed, and deliberately:** `setEndpoints` still emits
`EndpointsSet(feedId, teeId, version, endpointsHash)` without an instruction id. Nothing scans
for it any more — `send-endpoints` sends the transaction itself and reads the receipt — so there
is no registry-event coupling left to break. Worth revisiting only if that event is ever revised
for another reason.

---

## To decide

| # | Question | Owner | Blocking |
| --- | --- | --- | --- |
| 1 | Cycle interval `T`, and the fee/gas budget it implies. Straw man in spec WP5 is 5 min; the plan argues for ~10 to match the source refresh. | Engineering | Build |
| ~~2~~ | ~~Should `FeedUpdateRequested` carry the instruction ID?~~ **Done 5 Aug 2026** — it does, and `requestFeedUpdate()` returns it too. | Engineering | Closed |
| 3 | Where does the keeper run, and who operates it? Open as spec WP5.7. Interacts with the monitoring doc's requirement that the chain-age watcher sit in a different failure domain. | Infra | Build |
| 4 | Is an immediate off-schedule update on large deviation wanted? Straw man in spec WP5. Shares an owner with the deviation guard, spec §6.2. | _TBD — risk owner_ | No |
| ~~5~~ | ~~Confirm the deployed `maxSkew` before the budget inequality can be checked end to end.~~ **Moot** — the upstream store has no acceptance-window knobs at all; the only time rules left are strict monotonicity and `TooFarAhead`, and the latter is unreachable because `observedAt` is an on-chain event timestamp. | Engineering | Closed |

Questions 1 and 4 share an owner with the deviation guard (spec §6.2) and the fail-closed
question in `usdx-oracle-availability.md`.
