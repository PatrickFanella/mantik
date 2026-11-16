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

The real paper adapter repeats the execution-clock and quote-age checks after
its account/asset reads and durable intent retention, immediately before the
order POST. A quote that expires during this preparation cannot be submitted.

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
