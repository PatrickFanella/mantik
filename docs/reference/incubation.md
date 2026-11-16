# Real-quote paper incubation

`mantik incubation` runs one bounded cycle of reviewed stock strategies on the
existing capped paper book. It retains strategy decisions, next state and
execution quotes before asking the distributor for quantities. The resulting
orders pass through `cappedbook.Client.Submit`, its exact retained-order check,
account restrictions, fixed caps and persistent risk brakes. Broker buying power
never enters this path.

This is an incubation runner, not qualification. It does not create trial
results, forward windows or qualification credit. The four base-library gates
from the retained real-price check failed; this command does not change them or
provide a gate override. A usable implementation does not establish a protected
real-quote position. That operational exercise belongs to issue #107.

## Commands and inputs

All actions require `--id` and an explicit `--database-env`. The named environment
variable must hold the intended isolated PostgreSQL URL. The command does not
choose a development or production database implicitly.

- `--action init --input config.json` retains `Config`, an ordinary
  `account.CappedConfig` with the same ID. Use the intended $2,000 book and $500
  biweekly flow configuration. Init does not contact or fund a broker.
- `--action run --input cycle.json` compiles the reviewed library/expression
  recipes, makes decisions, retains sizing and submits eligible paper orders.
  It also requires `--journal`, `--credentials-file` and `--account-id`, using
  the existing private journal and mode-0600 paper credential contracts.
- `--action sync --input observation.json` requires the same paper adapter
  flags. It reads the original client IDs and owned fill activities, retains
  both fill results and emits the cost-conservative return report.
- `--action show` reads the retained book, including decisions, raw venue-fill
  metadata and the associated model comparisons. It needs no broker credentials.

A run input has these fields (Go JSON field names are accepted):

| Field | Contract |
| --- | --- |
| `ID` | Unique cycle ID, at most 60 characters so the derived rebalance ID fits. Retry the exact same file to recover a response. |
| `DecidedAt` | Strategy-view clock; it must follow all previously retained book receipts and gate decisions. |
| `ExecuteAt` | Later execution clock. A new submission must still be within 60 seconds of this clock and of the execution quote. |
| `Strategies` | Complete active roster. Each entry binds `SleeveID`, `GateID`, positive daily-volatility `Risk`, `Assets`, and exactly one `Library` configuration or `Expression` definition. |
| `Bars`, `Actions`, `Universe` within each strategy | Retained source material with original knowledge clocks. Bars bind the executable symbol as both `AssetID` and `Symbol`; a stable-ID-to-symbol resolver is not included here. Supply a reviewed universe when the recipe declares it. Option-chain strategies need a chain-capable application source and are outside this stock CLI adapter. |
| `Calendar` | Retained exchange-calendar coverage: `From`, `Through`, `SourceSHA256`, `ObservedAt`, `Sessions` with `Date`, `OpenAt`, `CloseAt`. Coverage must include every required warmup session through the current New York date. Observation must precede the decision and be at most 24 hours old. Keep original response bytes in private custody. |
| `Protective` | Optional `true` requests a flat target for every retained sleeve owner. It cannot add risk or change ownership, skips recipe compilation and strategy data/builds, and uses retained costs and state. Fresh executable quotes and limits remain required. |
| `Quotes` | Map from executable symbol to `account.IncubationQuote`: `Symbol`, decimal `Bid`, `Ask`, `BidQuantity`, `AskQuantity`, original `SourceSHA256`, `EvidenceClass`, `OccurredAt`, `ReceivedAt`. Sizes are shares, not unverified provider-native units. |
| `Limits`, `Fees` | Explicit symbol-to-decimal day-limit prices and conservative total venue-fee bounds. These are reviewed execution inputs, never strategy or model output. |

Sync uses `At` and `Quotes`; init uses `Config`. The input is bounded at 32 MiB,
and retained decision/quote plans at 2 MiB. Keep inputs, original provider bytes,
credentials, raw bars and generated observations outside Git. `InputSHA256`
pins the complete parsed run input, including recipes and source data. A hash
binds bytes; it does not establish that a manually supplied observation is real.
Source provenance, share-size normalization and review remain the input owner's
responsibility. The CLI does not fetch quotes or rewrite historical availability.

For command discovery without opening a database or broker connection:

```sh
mise exec -- go run ./cmd/mantik incubation --help
```

## Admission and fixed limits

Every strategy's compiled identity and declared asset set must match its
retained eligible gate. A
historical strategy needs a `passed` decision classified as
`historical_market_unqualified`. Synthetic and legacy unclassified decisions are
ineligible. Runtime model strategies use the explicit `forward_only` gate route,
never an invented historical pass. The service interface accepts such compiled
strategies; the stock CLI compiles library and expression recipes only.

The whole roster is checked before any strategy runs. An existing sleeve owner
whose gate search count has changed may still evaluate its strategy, but only a
complete, explicit flat target can proceed. A hold or nonzero target still needs
a fresh admission; a new sleeve cannot use this exit exception. Flat exits use
the retained sleeve identity and risk settings. Plan retention, sizing and new
reservations repeat these admission checks atomically; new exposure uses the
same family fence as trial starts. A concurrent new search cannot authorize new
exposure using stale N.
Gate clocks must precede the strategy view. The distributor retains the existing
5% aggregate incubation pool, 1% cap for each model-driven version, equal pool
sharing, minimum-balance waits and permanent strategy-version sleeve ownership.
Strategies emit weights and state only. Owner risk controls remain separate.

The strategy view contains only that sleeve's positions, fills and previous
state. Point-in-time bars/actions go through `strategy.Build`. The CLI also
refuses bar availability backdated before publication or observation. Execution
quotes must be prospective, usable, uncrossed and no more than 60 seconds old;
the market clock must be strictly later than the decision, and receipt must be
between market time and execution time. Current observations cannot be reused as
historical knowledge.

### Health admission per cycle

New risk requires all four service checks to be known and healthy:

- Daily data: every declared asset must have the latest eligible exchange
  session and the complete declared warmup. Eligibility follows retained session
  closes plus the daily collector's SIP embargo. A Friday close can remain
  current through a weekend or holiday; daily bars do not use the quote's
  60-second age limit. Original publication, observation and availability clocks
  must be ordered and known before the decision. Every trading asset also needs
  a current execution quote, even when it did not become a target.
- Owned-ledger reconciliation: the runner performs the existing GET-only fill
  and order reconciliation before building decisions. Its in-memory proof lasts
  at most 60 seconds and is not restored from an old plan. `SyncState` returns
  the exact reconciled journal prefix. Its writes use that prefix's version;
  intervening commands during order/fill GETs produce a version conflict and
  require a complete new reconciliation. A later store reload cannot extend
  this baseline. Other economic receipts or another allocation invalidate the
  retained proof at submission. This includes idempotent command retries: their
  returned version must match the expected prefix result, even though the
  ordinary store permits an identical retry with a stale expected version.
- Worker liveness: new risk requires a running `IncubationSafetyWorker` bound
  to the exact book configuration and expected paper account. Only its executed
  safety cycles can create live coverage; generic `RunTimerWorker` and
  `RunActionWorker` successes cover the demo workflow and cannot authorize a
  capped book. The worker evidence names the book, account, reconciled version
  and immutable policy hash. It applies to every sleeve within its configured
  symbol universe and continuous holding deadline. Uncovered assets or due
  holding/review protection fail closed. Shared capability readiness remains
  an additional veto, not a replacement for book coverage. Missing, failed,
  stopped, future or expired evidence is unavailable. There is no health
  override in cycle or model input.
- Risk brake: the service replays the owned book and observes its marked equity
  and configured deposits. A missing risk observation is unavailable until the
  runner initializes it through the existing journal command. Owner controls,
  loss brakes and hard caps apply independently. Health recovery never clears
  pause, reduce-only, strategy breaker, kill or hard halt state.

A service embeds this path by constructing `NewIncubationSafetyWorker` with an
independent `IncubationSafetySource`, fixed symbols, maximum holding duration,
protective fee reserves and a poll interval of 100 milliseconds to 60 seconds.
It attaches that same object to `IncubationRunner.Safety`, runs `worker.Run`
with a service-owned context and clock, and cancels and joins it at shutdown.
Constructing an object or calling readiness creates no coverage. The one-shot
`run` action can perform protective work and GET-only recovery. New entries use
the supervised action below or a service embedding a running worker. A restart
needs a new successful safety cycle; retained plan health is inspection evidence
only. No schema change or persisted worker grant is involved.

Each worker cycle collects quotes independently of entry requests, reconciles
owned orders/fills, observes the existing book risk brake and evaluates holding,
review and forward-outcome deadlines. When protection is due, it conservatively
flattens retained sleeves through the existing reduce-only runner without
calling strategy/model code. Protective limits include the most adverse retained
cost model and round down to ledger precision. Missing observations keep the
cycle unsuccessful. Protective work freezes its decision clock and waits up to
ten seconds, with cancellation, for a genuinely newer execution observation. A
collector may refresh the same quote file during that wait; rereading an older
snapshot cannot execute an exit or silently move the decision clock.
When protection is due, unresolved opening orders or pending
exits also prevent success; an accepted exit is not fulfillment. This broker contract cannot cancel outstanding opening
orders. It retains their reservations and continues reconciliation rather than
claiming protection or releasing capital. Successful evidence expires after the
existing heartbeat grace (10 seconds plus three poll intervals), becomes
unavailable while the next cycle is in progress, and is withdrawn on failure or
cancellation. These are service contract tests, not real-paper protection or
qualification evidence.

The final adapter check reloads live worker coverage after blocking reads.
It then checks current owner controls, the exact retained reservation and family
admission together under the book/family storage fence. No further database read
follows that snapshot; the live worker and execution clocks are checked again.
Cancellation or a failed cycle withdraws that coverage immediately. Coverage
also checks every retained exposed sleeve's forward holding and outcome clocks,
including sleeves unrelated to the proposed order, and expires at the earliest
protection deadline. A pending trial family denies new exposure but cannot stop
the worker from delivering retained-owner protective exits.

### Supervised terminal operation

`incubation --action supervise` uses the same explicit database, book, journal,
credentials and expected-account flags as `run`. Supply `--input` for the cycle
manifest and a separate `--safety-config` for the operator's fixed worker policy.
The supervisor keeps the worker running and watches the cycle file for changed
inputs. It executes each unchanged input once; the journal remains authoritative
for retries and changed IDs. Entry refusals or invalid cycle files do not stop
safety work. A fresh cycle needs a new ID and current decision/execution clocks.

The safety configuration contains `QuotesFile` (an absolute path), `Symbols`,
`Fees`, `MaximumHoldingSeconds` (1 second through 90 days), and `IntervalSeconds`
(1–60 seconds). It is read once when supervision starts. `QuotesFile` must differ
from both configuration and cycle paths. The configuration and quote file must
be regular files owned by the invoking user with mode 0600; symlinks are refused.
They are service configuration, never strategy or model output.

The independent quote file is a JSON map from each configured symbol to its
`IncubationQuote`. A trusted collector or operator must refresh it, preserve the
original source/receipt clocks and hash, and retain the source material outside
Git. Selecting this file does not attest its provenance. The supervisor does not
fetch, invent, backdate or approve quotes. Missing, stale, future or misbound
observations leave new entries unavailable. The cycle's own quotes cannot renew
worker evidence.

Output is a JSON stream of safety transitions, cycle results/refusals and a final
`supervision_stopped` event. Status inspection grants no entry authority; the
runner repeats all checks at execution. Cancellation stops and joins the worker.
It does not claim to flatten remaining venue positions. Keep supervision running
while exposure requires protection, and use the retained ledger and original
adapter IDs for restart reconciliation. No host timer or unattended deployment is
installed by this command.

The paper adapter must implement `SubmitChecked`. The service repeats admission,
owner controls, exact distributor-order checks, forward clocks and quote age
**after** account/asset reads and durable intent retention, immediately before
POST. Slow preparation cannot extend a health, quote or holding-window clock.
Reservations and ambiguous orders are recovered by GET only, without renewed
entry permission. A blocked entry does not prevent later protective reductions
in the same distribution. A missing strategy build holds existing exposure;
explicit `Protective: true` can flatten retained sleeves without compiling or
calling a strategy. `HoldExposure` and forward-window expiry retain their
separate meaning.

Results include `Admission`, with separate `entry`, `reduce`, `exit`, `reconcile`
and `settle` classes, exact check codes, observation clocks and evidence refs.
The initial cycle report is retained in `Plan.Health` beside its input hash,
quotes, decisions and costs. It is evidence, never a permission token. The CLI
also emits the result when a run fails, then returns the error.

`GET /v1/incubation/{id}/readiness` uses the normal bearer-token authentication.
It reports current admission per work class and exposes the last retained cycle
report separately as `retained_cycle_evidence`. A read-only GET or a service
restart cannot renew cycle data or reconciliation: without a current cycle those
checks say unavailable and entry is blocked. Safety-work readiness means health
permits that class; its own quote, ledger and reduce-only checks still apply.
It does not claim an exit, reconciliation or settlement actually completed.

`sync` remains independent of strategy compilation, market-bar availability,
liveness admission and model availability. Owned fills and unsettled sale credits
can still reconcile and settle while entry admission is blocked. Its existing
fresh quote/mark contract remains in force. Health snapshots do not change
lifecycle policy, promotion, daily reviews, family search accounting or capital
ownership. No SQL migration or option-paper implementation is required.

## Fill comparison and returns

Historical strategies retain the gate's frozen base fee/slippage model. A
forward-only strategy without historical costs uses the engine's initial
zero-commission, five-basis-point model. Replay and incubation share exact
`executioncost` arithmetic. Incubation uses the placement quote's ask for buys
and bid for sells, then moves the price adversely and computes fees. The model
clock is the placement clock. It is distinct from the later venue-fill and local
receipt clocks.

Admission requires the whole sized order to fit displayed quote liquidity and
its limit under that model. Reservations sharing a symbol/side observation
cannot multiply its liquidity across sleeves, partial fills or restarts. A model
that cannot execute the retained limit order refuses submission. It does not
invent a modeled fill or substitute an optimistic venue price. This is an
immediate-execution model; fills outside 24 hours of placement are rejected.
It does not simulate later replenishment or queue position.

`Sync` retains the original venue price, fee, quantity, source hash and economic
clock in `BookFill`. The same event retains `ModelFill`: modeled price/fee,
quote hash, model clock, the worse net cash effect and the nonnegative cost
penalty against the venue. Buy debits use the larger total cost; sell proceeds
use the smaller net receipt. Fees participate in that comparison. Exact retries
add no economic event; changed venue activities or comparison records conflict.
Journal commands reject `Incubation` outside `incubation_plan` and `ModelFill`
outside `fill`; an observation cannot attach a plan or comparison retrospectively.

The return report subtracts accumulated penalties from venue marked equity.
`NetPnL` excludes opening capital and scheduled deposits.
`NetReturnOnContributions` divides that net P&L by opening capital plus deposits;
it is explicitly not a time-weighted or qualification return. Venue cash,
settlement, reservations and positions remain available for reconciliation.
A book containing fills outside the incubation path cannot produce this report.
Every output carries `QualificationCredit: false` and an unqualified status.

## Restart and evidence limits

The existing append-only capped-book JSON journal stores plans and fill
comparisons; no new table or schema migration is needed. Older binaries that
do not understand these event kinds cannot replay a book after its first
incubation plan. A restart with this contract recovers the
original decisions and sizing before asking research for admission again. It
does not call the strategy again or POST an already reserved client ID.
Ambiguous submission retains its reservation and is looked up by the same ID.
Use sync to reconcile a crash between reservation and adapter intent: only the
adapter's durable abort can release an order that was never submitted. An
unsent expired cycle requires fresh quotes and a new ID; it cannot reopen an
old order after its quote has expired.
If the adapter's final pre-POST check refuses a new intent, its durable abort
fences the ID and allows the capped reservation to be released. Previously
submitted or ambiguous intents remain recoverable by GET after quote expiry;
they never run the new-submission check again or receive another POST.

Local tests use explicit admission and quote contract fixtures with a fake paper
broker. The disposable SQL contract checks plan/model persistence, replay and
stale-search rejection. These tests exercise the running path; they establish no
real market observation, broker connectivity, protected position, performance or
qualification result. No external order is part of this implementation proof.

Canonical strategy forward windows use the separate [forward declaration contract](forward-declarations.md). It binds prospective clocks, its own benchmark, minimums and actual trial-family search bound to the capped-book runner, with multiweek holds and no qualification credit. Legacy threshold replay retains its original rules.

## Daily oversight

[Daily strategy review](daily-strategy-review.md) retains evidence-bound budget
transitions in the same capped-book journal. The runner uses the strictly later
decision clock, makes retired strategies explicitly flat without calling Decide,
and retains pending ownership and all existing admissions. Whole-book
`IncubationReturns` is not used as strategy forward performance. Owner retirement
is permanent and distinct from the resumable strategy breaker.
