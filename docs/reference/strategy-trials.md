# Daily strategy trial registry

The registry records attempts made with the [daily engine](engine.md), including
failures and unfinished runs. It supplies the search total for the later
backtest gates (#69, #70, #72); it does not implement those gates or grant
qualification. The older hypothesis-trial records keep their existing contract.

## Recording a run

`engine.RunTrial(ctx, config, store, trial)` is the recorded backtest entry
point. `store` is a `strategytrial.Store`, implemented by both `storage.Memory`
and `storage.Postgres`. `engine.Run` remains the low-level simulator; research
attempts used by statistical gates must use `RunTrial`.

The trial supplies a fresh ID, a stable strategy family, parent trial IDs, an
input-manifest SHA-256 and an evidence class (`synthetic_fixture` or
`historical_market_unqualified`). The engine retains:

- the strategy ID and full spec, including code version and parameters;
- the assets, window, capital, costs, deposits, account type and settlement
  calendar identity;
- lineage, input-manifest hash, evidence class and the start time;
- each session's time-weighted return and marked equity;
- one terminal outcome, when available.

A custom settlement callback must have a versioned `CalendarID`. The default
calendar is recorded as `us-equity-t1-v1`. The input hash and evidence class
are declarations from the caller. The registry does not establish source
coverage, correct family classification or that a code version matches its
source. It grants no trading authority.

The start commits before any source read. Each daily return commits after the
book is marked and before the next strategy decision. A failed decision can
therefore leave that session's return, even though no new decision was made.
The retained decimal strings come directly from the engine; returns exclude
deposits as profit under the engine's time-weighted accounting.

## Failures and recovery

The terminal statuses are `completed`, `failed`, `crashed` and `abandoned`.
Ordinary engine errors produce `failed`, an escaping panic produces `crashed`,
and cancellation or a deadline produces `abandoned`. A strategy panic already
contained by `strategy.Decide` is an engine error. Raw provider errors and panic
values are not written into the registry.

Cancellation uses a separate five-second context to retain the outcome. A
registry write failure stops execution and is returned to the caller. If the
outcome cannot be saved, the attempt remains unfinished rather than appearing
complete. A hard process exit likewise leaves its committed start and any
committed returns without an outcome. Absence of an outcome is not evidence
that a worker is still alive.

A recovery owner can call `FinishStrategyTrial` with `crashed` or `abandoned`
once it has established that execution stopped. There is no automatic timeout
or liveness inference in this issue. Recovery does not remove saved returns or
reduce the search total. Retrying execution requires a new trial ID, optionally
naming the earlier attempt as a parent. `RunTrial` never executes an existing ID.

## Storage and search totals

Schema 117 adds three tables: `mantik_strategy_trials`,
`mantik_strategy_trial_returns` and `mantik_strategy_trial_outcomes`. Starts,
returns and outcomes are append-only. Database triggers refuse updates,
deletes, truncation, out-of-order session dates and returns after an outcome.
Concurrent appends and completion serialize on the trial row.

The storage API also provides:

- `StartStrategyTrial`: save a declaration; report whether it was newly added.
- `AppendStrategyTrialReturn`: append a session in date order.
- `FinishStrategyTrial`: append the sole terminal outcome.
- `StrategyTrial`: read the declaration, ordered returns and optional outcome
  from a consistent snapshot.
- `StrategyTrialSearchTotal`: count every start in a named family, including
  completed, failed, crashed, abandoned and unfinished attempts.

Exact write retries are idempotent. A changed declaration, daily return or
outcome under an existing identity conflicts. Parents must already exist and
cannot have a later start time. Parameter and code variants belong to the same
stable family when they are part of the same search; making a new family for
every candidate would understate multiplicity. Counts include synthetic trials
and do not constitute a sample of independent or successful strategies.

The new tables must travel together in a backup. Migration is additive and
leaves historical trial records unchanged. An older binary refuses the newer
schema; rollback requires a compatible binary or restoration into an isolated
pre-upgrade database, never deletion of trial history.

## Verification

The memory and disposable PostgreSQL contracts cover immutable retries,
lineage, partial returns, all outcomes, family totals, concurrent starts and
competing completions. A PostgreSQL subprocess test exits without cleanup after
committing a start and one return, then checks that both remain and the search
total includes the unfinished attempt. Engine tests cover success, failure,
panic, cancellation and registry outages with synthetic data. These checks
provide no real-market or forward qualification evidence.
