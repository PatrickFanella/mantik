# Strategy gates, sizing and order risk

Wave 9 (#72–#74) joins retained research decisions to the capped paper-book
order path. It does not start an incubation worker or grant qualification.

## Recorded gate decisions

`backtest.DeclareGate` freezes the base strategy identity, named neighbours,
session calendar, purge/embargo design, balance, benchmark series and reviewed
source assertions before registry execution. Schema 122 keeps declarations and
final decisions append-only. Exact retries preserve their original clocks;
changed retries conflict. Migration replay retains existing books and trials.
Store results are detached from retained history, including conflict and
decision-validation error results.
The declaration also binds assets, engine version, account type, leverage mode
and exact base/stress costs. Undeclared deposits or larger test capital cannot
make a current-size gate pass. Incubation must use the gate's account type.
Each variant's full minimum trade plan is frozen with the declaration and must
match every retained fold. The reviewed default settlement-calendar identity
must also match. A malformed retained config yields a failed integrity check
and a complete failed report rather than leaving the declaration undecided.

`EvaluateGate` reads the complete family once, including failed and unfinished
starts in search multiplicity. Expected attempts must match the declaration,
version, data identity and exact fold manifests. The report lists each failed
check and preserves numerical values or unavailable reasons. Policy
`backtest-gate-v1` requires:

- at least two complete folds of twenty or more OOS sessions;
- all predeclared training, test and cost-stress attempts complete;
- verified historical coverage and availability;
- positive costed excess over cash and the declared benchmark;
- DSR at least 0.95;
- CSCV probability at most 0.20 when enough comparable configurations exist;
- at least two-thirds of predeclared neighbour OOS tests beat cash;
- the predeclared minimum trade plan fits the current book balance.

Each stress component must be no more favourable than base. PBO with fewer than
four comparable configurations is explicitly unavailable and skipped, never
reported as zero. Other missing statistics fail the gate. A pass records stage
`incubation`; a failure records stage `idea`. A model-driven spec is a distinct
content-addressed version and takes `forward_only` incubation without a
historical performance gate. None of these statuses is qualification.

Deterministic decisions retain the registry family's evidence class. Every
attempt contributing to that family must be `historical_market_unqualified`;
any `synthetic_fixture` attempt fails the `evidence_class` check even when
coverage and availability were asserted. Synthetic evidence cannot grant
operational admission. Models record `forward_only` explicitly. Both the public
client and transactional order boundary reject synthetic or older unclassified
decisions with `gate_evidence_ineligible`; existing positions can still use
bounded flat exits. Older unclassified decisions stay readable and immutable
and require a new reviewed gate before new exposure.

The local CLI uses an explicitly selected database environment variable:

```sh
mise exec -- go run ./cmd/mantik strategy-gate --action declare --input /tmp/mantik-gate-declaration.json --database-env MANTIK_GATECHECK_DATABASE_URL
mise exec -- go run ./cmd/mantik strategy-gate --action evaluate --id wave9-trend --database-env MANTIK_GATECHECK_DATABASE_URL
mise exec -- go run ./cmd/mantik strategy-gate --id wave9-trend --database-env MANTIK_GATECHECK_DATABASE_URL
```

The declaration is an operator-reviewed experiment, not a strategy-supplied
admission. A digest binds bytes; it does not attest coverage or source truth.
The CLI does not accept a caller-authored final pass. A declared experiment
cannot be substituted for the later prospective qualification record.

## One sizing authority

`cappedbook.Client.Rebalance` applies due deposits and reads stored gate
admissions. The distributor uses the book's marked equity; broker buying power
never enters its inputs. Initial qualified-sleeve budgets share 65% by inverse
daily volatility, capped at 20% per sleeve. The fixed incubation pool is 5%;
model-driven/forward-only versions are capped at 1% each. Idle budget remains
cash. The 70% gross target is not a reason to exceed a hard limit or force an
order. Production qualified admission awaits #92; this wave's public client
admits only stored incubation/forward-only decisions.

Targets are complete sleeve-relative signed weights. They contain no order
quantity, budget or risk-control fields. The distributor creates fractional
quantities truncated at nine decimal places, a $1 minimum opening order and
5%-of-equity per-position sizing. It subtracts possible fees before sizing.
Insufficient minimum capital produces a `minimum_balance` wait; pending orders
produce `pending_orders`. Underfunding can still produce flattening exits.
After a deposit, the next rebalance recalculates every eligible sleeve from
current equity. Deposits are unallocated capital flows until then.
Rebalances must include every retained sleeve with holdings or pending orders.
A strategy version keeps one stable sleeve identity, even after an inactive
sleeve is omitted. The order check counts the entire book against the fixed
incubation pool and per-model-version cap; omitted or offsetting holdings cannot
create extra capacity. A `KeepPositions` signal reuses the last complete
target weights when capital is redistributed, so deposits and roster changes
can resize it without asking the strategy for an extra signal.

The retained distribution binds order ID, strategy, sleeve, side, quantity,
limit, fee and clock. `Client.Submit` accepts only an exact retained order.
An exact rebalance retry returns its original distribution by replaying the
journal prefix through that request, even after a later rebalance at the same
or a later clock. The current journal remains unchanged.
Retry recovery compares the retained effective request, marks and clock before
consulting research. A subsequent trial cannot block reading that response;
changed retries still conflict. New rebalances and order reservations enforce
family freshness transactionally alongside new trial starts.
Quantity changes and raw orders fail before a broker POST. Independent pending
orders cannot net or release capacity, and a sleeve waits for reconciliation
before replacing an outstanding target. The existing journaled broker adapter
continues to fence ambiguous submission retries.
Opening quantities use the greater of mark and executable limit, with a
conservative reserve for adverse price gaps. Mark-priced sizing cannot produce
an order that immediately breaches its own risk cap. Reducing orders retain a
reduce-only flag and cannot become flips if holdings change before admission.

## Persistent risk brake

The capped-book event stream retains risk observations and owner controls.
Admission checks execute with the book version lock, then the latest brake is
checked again before POST, including retries of already-reserved orders.
Displayed `Risk.At` identifies the last persisted observation; new submissions,
rebalances and portfolio reconciliation update it with complete current marks.
New rebalances and reservations validate gate freshness in the same transaction
as persistence. PostgreSQL takes sorted family advisory locks shared with new
trial starts; memory uses its store mutex. A newly started attempt cannot slip
between a search-total check and admission. Already retained explicit flat
targets remain executable when a research family becomes stale.

Hard refusal codes include `risk_gross_limit` (100%), `risk_position_limit`
(5%), `risk_sleeve_limit` (20%) and `risk_cash_reserve` (20%). The unitized loss
series removes recorded deposits: 5% drawdown warns, 10% pauses, 15% hard halts,
and 3% daily loss pauses. `risk_kill_switch`, `risk_strategy_breaker` and
`risk_reduce_only` report explicit controls. Daily boundaries use New York
civil dates. Pauses persist until an explicit resume; current loss conditions
can immediately pause again.

Reductions remain available under emergency controls, subject to the account's
funding and settlement rules. A reduction cannot cross zero or reuse stock
already reserved by another reduction. Exits do not clear a halt. Insolvency
sets a persistent halt while preserving the ability to size flattening exits.

Only an explicit `owner_clear_halt` control clears a hard halt. It requires
positive observed equity and resets the recorded loss baseline. Ordinary
resume, deposits, price recovery, a new day and process restart do not reset it.
Controls are operator commands, separate from the strategy interface. The
local CLI caller must already have owner access to the selected database.
Reconciliation and bad-submission halts use distinct journal identities after
an owner clear; an old halt ID cannot prevent a later discrepancy from halting
the book again. Public portfolio reservations use the same owned-fill recovery,
partial-fill reconciliation and terminal release path as earlier ledger orders.

```sh
mise exec -- go run ./cmd/mantik capped-risk --id book --at 2026-11-16T15:00:00Z --marks /tmp/mantik-current-marks.json --database-env MANTIK_GATECHECK_DATABASE_URL
mise exec -- go run ./cmd/mantik capped-risk --id book --at 2026-11-16T15:00:00Z --marks /tmp/mantik-current-marks.json --action kill --command-id owner-kill-20261116 --database-env MANTIK_GATECHECK_DATABASE_URL
```

Schema 121's older low-level accounting commands stay replayable. They are not
the public strategy submission path. Existing unassigned holdings require
operator reconciliation rather than invented strategy attribution. The older
threshold-specific allocation system and the manual paper-adapter diagnostics
retain their own ownership contracts. New strategy workers must use the capped
client; real-quote incubation and that running integration remain #77.

## Real-price gate check and limits

`scripts/gatecheck` runs all four pinned base recipes and two declared gross
neighbours each, retaining every training/test/stress attempt in disposable
PostgreSQL. It reads the hashed Wave 5 SPY input and explicitly assigns synthetic
historical clocks and one-ETF membership in a separate replay fixture. The
original input and its acquisition clocks are not changed. The 520-session
check is a real-price gate rejection exercise, not large-cap strategy coverage.
Coverage and historical availability checks fail; no fixture earns incubation.

The command takes the database URL only from the named environment variable:

```sh
mise exec -- go run ./scripts/gatecheck -input /tmp/mantik-wave5-close/spy-input.json -sha256 3ca211509049e6dae320f73e6bafc178cd59aa808be53e4e102500f57dceee4f -prefix wave9 -database-env MANTIK_GATECHECK_DATABASE_URL
```

Reports and original observations remain outside Git. The issue report records
the tested source, input/report hashes, decision reasons and fresh-process CLI
readbacks. Full stock-universe coverage and prospective evidence remain separate
requirements for future admission and qualification.
The checker also exports every family start, daily return and outcome and
verifies that its hash matches the decision's retained family snapshot before
the disposable database is removed.

## Rollback

Stop the new caller and retain its gate rows and capped-book commands. Do not
delete or rewrite admissions, reservations, fills, controls or halt history.
Older binaries do not understand the new journal command kinds, so do not run
one against a book that has received these commands. Restore the tested binary
or reconcile with the current reader; a failed downgrade must not invent cash.
