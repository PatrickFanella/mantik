# Owned paper evaluation declarations

This is the predeclaration boundary for #21 and the future admission path under
#19. It retains an operator's intended evaluation recipe against actual immutable
strategy and allocation-policy owners. It evaluates no strategy, imports no
results and grants no admission, execution or qualification. The status is always
`declared_not_evaluated`; all four availability/credit flags remain false.

## Commands and retained sources

The existing bearer authentication protects:

- `POST /v1/paper-evaluation-declarations`
- `GET /v1/paper-evaluation-declarations`
- `GET /v1/paper-evaluation-declarations/{id}`

A command contains `id`, `receiver_id`, a versioned recipe, `policy_id`, `policy_sha256` and
`plan`. The original recipe is `native-threshold-paper-evaluation-plan-v1`;
`native-threshold-purged-holdout-plan-v2` adds the prospective partition choices
described below. `native-threshold-observed-benchmark-plan-v3` additionally pins
the observed benchmark method. Callers cannot supply an account, balance, clock, finalized decision,
check result, effect, admission or qualification assertion. Unknown/duplicate
fields, trailing JSON and query strings fail. The POST limit is 16 KiB.

The plan declares:

- `family_id` and an ID-sorted, unique `variants` array of `{id, sha256}` pins
  for the owned strategy records being planned together;
- `window_start`, `window_end` and `outcome_deadline`;
- `min_independent_decisions`, `minimum_net_excess_cents` and
  `maximum_drawdown_cents`;
- `admission_ttl_seconds`, an intended future reassessment limit, not a grant;
- `benchmarks`, exactly `["cash_no_action", "passive_same_symbol"]`;
- `stress_model`, a complete versioned paper-fill model.

Each strategy pin must resolve to an actual current owner record and a matching
strategy/definition rule in the pinned allocation policy. The policy must retain
an execution model. Both source owners must predate the declaration. The source
hash binds those complete records, including their immutable funding/model
configuration. Input classes remain their original unqualified classes.

Without corporate-source or holdout choices, new records use
`mantik-owned-paper-evaluation-declaration-v2`. Their
`variant_configurations` bind each actual strategy ID and record hash to the
versioned `native-definition-without-id-v1` digest. This digest covers every
native definition field except its ID: method, symbol, feed, evidence class,
entry threshold, quantity and safety conditions. Two IDs with identical
configuration digests conflict instead of increasing `planned_variants`.
Changing a name or registration time cannot create a parameter variation.

These are distinct configurations, not a completed parameter/cost sensitivity
test, independent observations or a complete search inventory. A one-variant
plan remains a one-variant plan and supplies no sensitivity result. Legacy v1
records keep their original bytes, counts and clock-free retries, including
records that pinned aliases; they do not gain a distinctness assertion.

The stress model must be valid, differ from the base model and be no more
favorable in any declared dimension: fees, commissions, slippage and latency do
not decrease; displayed-liquidity participation and maximum quote age do not
increase. These are declared sensitivity assumptions, not measured costs or a
completed sensitivity test. Stop-gap and partial-fill behavior still belongs to
the versioned fill method.

Schema 60 fences older declaration writers before new v2 records are issued.
It changes the declaration table comment and schema marker without rewriting
retained plans or downstream evidence. Rollback uses a compatible pre-upgrade
backup and binary on separate resources; do not remove migration rows from a
running service. Verification covers actual Memory/PostgreSQL alias rejection,
distinct configuration pins, concurrent retries, self-rehashed substitution,
legacy bytes/reopen and migration rollback, checksum and payload preservation.

## Prospective training and holdout partitions

Recipe `native-threshold-purged-holdout-plan-v2` requires `plan.holdout` and
retains declaration schema v4. The original recipe rejects that field. The
choices must exist before the observation window starts and cannot be added
or changed by retrying an existing declaration or family.

```json
"holdout": {
  "method": "predeclared-purged-label-availability-holdout-v1",
  "split_at": "2026-11-12T15:00:00Z",
  "embargo_seconds": 60,
  "minimum_training_occurrences": 20,
  "minimum_holdout_occurrences": 20
}
```

The split minus the embargo must be strictly after `window_start`; the split
plus the embargo must be strictly before `window_end`. Embargo values range
from zero to 86,400 seconds. Each partition minimum ranges from 2 to 1,000,000.
These are necessary occurrence bounds, not sufficient independent-sample sizes.

The assessment owner derives horizons from the actual pinned strategy and
feature clocks from retained decision sources. Feature availability includes the
matching original recording’s durable publication clock as well as receipt and
admission clocks. Missing, ambiguous or mismatched publications reject replay
and issuance. Training requires the holding
horizon and observed label availability to be strictly before the split minus
the embargo. Label availability includes retained outcome creation time, so
an earlier quote cannot make a later-created label available for training.
Holdout decisions, feature occurrences and feature availability must all be
at or after the split plus the embargo. Rows overlapping these boundaries are
purged from the partition counts. Waiting or unavailable outcomes supply no
observed label. Existing duplicate, synthetic and other evidence exclusions
continue to apply before partitioning.

V7 assessments retain the source pins, partition rows and per-variant bounds.
Before the deadline the necessary out-of-sample check is pending; after it, a
partition below its predeclared minimum fails. Meeting the bounds leaves the
check unavailable because temporal separation does not prove independence or
holdout performance. No evaluation, admission, execution or qualification grant
is produced. See [retained holdout checks](paper-evaluation-assessments.md#predeclared-holdout-checks).

Schema 76 fences old writers without rewriting legacy declarations or
assessments. Existing v1-v3 declarations and v1-v6 assessments keep their bytes,
hashes, methods and exact retries. The worker uses a separate v7 recipe identity
for holdout plans; ordinary plans retain their existing v5/v6 worker identities.

## Time, family and sample boundaries

The service owns `declared_at`. `window_start` must be strictly later than that
clock and no more than 365 days away. The observation window is at most 90 days.
The outcome deadline is later than its end, no more than 48 hours afterward,
and covers every variant's complete maximum holding horizon. All timestamps
must be JSON-encodable. A new declaration cannot use an already-started window
or move the declaration clock behind an earlier retained declaration. After a
v7 allocation comparison, a new declaration must be strictly later than that
comparison clock, including when the plan window is still in the future. This
preserves the complete historical declaration cut used by allocation replay.
Equal or earlier clocks return an error and an empty receipt without retaining
the plan; the same ID remains available for a valid later attempt.

One family ID has one immutable declaration. A second request ID cannot reset
that same family, replace its variants or lower its thresholds. Exact historical
retries return the original record without reading a clock, including after the
window/deadline has passed or capacity is full. Changed commands conflict.
Families are operator-declared planned groups; registration does not prove that
all earlier searches, external variants or selection attempts were disclosed.
Future multiplicity evaluation must account for the actual broader search
history. A family ID or variant count is not an independent-sample count.

Sample minima range from 2 to 1,000,000; these are explicit requested criteria,
not a claim that any chosen value is statistically sufficient. Actual independent
samples, missing outcomes, leakage, parameter/search selection and statistical
requirements still need relevant evidence and a validated evaluation method.
No successful job count or duplicate assessment satisfies these declarations.

Every retained record lists the eight planned checks: leakage, warm-up,
out-of-sample behavior, parameter/cost sensitivity, benchmark comparison,
forecast calibration, sample sufficiency and search multiplicity. Listing a check
does not run or pass it. The native method is a deterministic threshold, not a
calibrated forecast; applicable calibration/warm-up treatment must be justified
by the future evaluator. Economic metrics, baselines, sample scores and positive
admission remain unavailable until their actual owners and recipes exist.

## Durability and verification

Memory uses the aggregate mutex. PostgreSQL schema 46 adds a bounded declaration
table. Writes take state then receiver locks, matching account/policy writers;
no collector lock is acquired afterward. Reads use one repeatable-read snapshot.
All retained declarations and both source catalogues are checked before selected
reads or retries. Rehashing an altered authority, source pin, planned-check list
or invalid time window cannot make it valid. Hashes do not independently attest
database provenance.

PostgreSQL returns a declaration receipt only after a successful commit. Write,
catalogue-validation and commit failures return empty results, so a staged plan
cannot be mistaken for a durable declaration. A transport failure during commit
can leave the outcome uncertain: read the owner catalogue before retrying the
same command identity. This does not grant evaluation or admission authority.

The catalogue retains at most 16 declarations of at most 32 KiB each. Capacity
returns an error without evicting past attempts. Retained records and command
slices are detached. Insert failure or cancellation commits no partial record
and changes no operational state. Older schema-45 writers refuse the newer
schema; any operational upgrade/rollback must retain its actual backup and
release identity. No running service is upgraded by this PR.

Contracts cover concurrent duplicate registration, clock-free historical retry,
late windows, source/model mismatch, adverse stress assumptions, family conflicts,
sample/benchmark requirements, cancellation, capacity, self-rehashed corruption,
failed PostgreSQL inserts, reconnect retention, and actual HTTP owner routing.
Local tests and disposable database evidence remain separate from hosted CI,
deployment, runtime qualification and #27's natural soak. No provider/model call
is part of declaration registration. This slice does not complete #19 or #21.

## Owner-selected evidence

`GET /v1/paper-evaluation-declarations/{id}/evidence` resolves a retained family
to actual owner decisions and forward-quote outcomes. It uses the same bearer
authentication and accepts no query string, request body, caller clock, result,
decision list or replacement source history. Memory holds its aggregate mutex;
PostgreSQL reads one repeatable-read, read-only transaction. All declarations,
strategies, policies, decisions and outcomes are revalidated against their owner
records before selection. Missing declarations return 404; unavailable or invalid
owner evidence returns 503. Invalid paths, bodies and queries return 422.

The service reads its clock once. It refuses a clock behind retained collector,
declaration, decision or outcome history. The report binds that clock, the exact
declaration and the complete current source catalogues. It is a current read,
not an immutable evaluation receipt. A source-catalogue or clock change can
change its hash. The response is bounded to 8 MiB and detached from retained
storage. Reading it writes no journal, account, admission or operational state.

Rows include every decision matching an exact declared strategy-record pin,
ordered by finalization time and then ID. Decisions outside the observation
window, abstentions, unavailable-as-of sources and synthetic evidence remain
visible with exclusion statuses. Both finalization and quote occurrence must
fall within `[window_start, window_end)`. A selected source must have occurred,
been received, become available and been published by decision finalization.
The original evidence class remains visible; no class is promoted.

Within a strategy, aliases of the same provider/feed/canonical-symbol occurrence
select only the first available decision. Capture IDs, raw pins, receipt times
and price corrections at the same occurrence cannot create another selection.
Different variants may select paired decisions from that occurrence, but the
report counts the provider occurrence once across the family. This is not proof
of independence across feeds/providers or overlapping holding horizons. The
`independent_samples_available` flag stays false; these counts cannot satisfy
`min_independent_decisions` or an admission rule on their own.

The evidence response uses schema `mantik-owned-paper-evaluation-evidence-v3`.
All actual forward-outcome attempts remain attached to their decision, including
waiting and unavailable statuses. Each row partitions attempts by the owner's
`evaluated_at`: `outcomes` contains attempts finalized at or before the declared
`outcome_deadline`; `late_outcomes` retains attempts finalized strictly afterward.
The exact deadline is inclusive. A report clock before any retained attempt still
fails, and advancing that clock cannot move a late attempt into the declared
sample. The full source hash includes both partitions; this is still a current
read whose hash can change when later evidence arrives.

`observed_outcome_decisions` counts a selected canonical decision once if its
`outcomes` contains an observed forward-quote outcome. Late observed attempts
cannot increase that count. Retrying an assessment under a new ID cannot
increase it either. Being available by the deadline does not establish sample
independence, qualification or an economic result. An observed quote is not a
fill, realized PnL, benchmark result or completed evaluation check.

The phases are `awaiting_window`, `observation_window`,
`awaiting_outcome_deadline` and `outcome_deadline_elapsed`. Elapsing a deadline
does not imply complete outcomes or a passed window. Evaluation, admission,
execution and qualification flags all remain false. Relevant economic replay,
statistical checks and the deterministic admission issuer still need their
actual owners before the financial execution aggregate can act.

## Predeclared sample-count bound

Each variant's `sample_bound` uses method
`predeclared-observed-occurrence-upper-bound-v1`. It binds the plan's
`required_independent_decisions` to the same read's
`observed_occurrence_upper_bound`. The latter is the existing deduplicated,
in-window, by-deadline `observed_outcome_decisions` count for that variant.
Variants are not pooled, repeated assessments do not add samples, and late
outcomes remain excluded.

Before the declared outcome deadline, status is `pending_outcome_deadline`.
At the deadline and afterward, a count below the predeclared minimum reports
`insufficient_occurrences`: even treating every counted occurrence as independent
would not supply enough samples. A count at or above the minimum reports
`independence_unproven`, never a passing sample check. Distinct occurrences can
still share underlying information, feeds or overlapping outcome horizons.

This is a necessary count check, not a completed statistical or economic recipe.
`independence_established`, `admission_available` and `qualification_credit` stay
false on every bound; the report's evaluation/execution/admission/qualification
flags also stay false. No independent sample count, p-value, performance score,
admission expiry or retirement event is manufactured from the count.
The v3 response adds these derived bounds without changing stored declarations,
decisions or outcomes. Report hashes change with the representation; no source
or retained historical receipt is rewritten.

Durable service-issued attempts are described in
[owned paper evaluation assessments](paper-evaluation-assessments.md). They retain
necessary sample failures and unavailable checks without issuing admission.
Once an assessment is retained, a new declaration must be strictly later than
every assessment clock; historical exact retries retain their original identity.

## Entry counterfactual replay

The [entry replay read](paper-evaluation-entry-replay.md) consumes this declared
policy and stress model together with canonical owner-selected decisions and
on-time historical outcomes. It derives bounded shared-liquidity entry books,
not a complete evaluation or an admission. Existing declarations, evidence and
assessment recipes retain their separate meanings.

## Protective counterfactual replay

The [protective replay read](paper-evaluation-protective-replay.md) reuses the
same owner-selected source snapshot and declared base/stress models. Its
versioned application method derives holding clocks from modeled fills, seals
partial entries and retains unclosed exposure at the actual source cut. It does
not amend this plan's deadline or supply a passing economic assessment.

## Finite-capital counterfactual replay

The [capital replay read](paper-evaluation-capital-replay.md) applies the actual
owner account and declared policy's finite budgets to canonical candidates,
then reruns only funded candidates through protection. Its versioned study
method does not reuse cash within the window. Reconciled model cash and a nominal
cash comparison do not complete portfolio evaluation or grant admission.


## Prospective observed benchmark method

Recipe `native-threshold-observed-benchmark-plan-v3` requires the existing
`holdout` plan and `benchmark_method` set to
`owner-observed-first-quote-whole-share-passive-v1`. It retains declaration
schema v5. Original recipes reject this method field; schema v4 cannot be used
to remove it. The method, costs, capital policy, variant family and temporal
choices are fixed before the observation window, including exact retries.

This method supplies finite-capital cash and passive observed-price studies
inside the retained assessment. It does not declare corporate-action coverage
or turn a selected counterfactual quote path into continuous observation of the
whole declared window. See [observed benchmarks](paper-evaluation-assessments.md#retained-observed-benchmarks)
for the entry clock, costs, expiry, unavailable marks and total-return boundary.

## Prospective native feature causality

Recipe `native-threshold-causal-feature-plan-v4` requires the observed benchmark
and purged holdout choices above, plus `feature_causality_method` set to
`native-threshold-owner-feature-availability-v1`. It retains declaration schema
v6 and fixes the native feature check before the observation window. Earlier
recipes reject the new field, and the new recipe requires it. Unknown methods,
missing benchmark/holdout choices and declaration-schema downgrades are invalid.
Exact retries keep the original declaration; an existing plan cannot gain or
lose this method after issuance.

The compiled threshold predicate consumes one quote and has no training labels,
learned state or rolling features. Its strategy definition and parameters are
owner-pinned before declaration. The assessment replays each selected feature's
receipt, collector admission and original publication availability against the
decision's finalization time. See [native feature causality](paper-evaluation-assessments.md#native-feature-causality)
for check outcomes and the remaining economic/statistical requirements.


## Prospective disjoint observation windows

Recipe `native-threshold-disjoint-sample-plan-v5` retains declaration v7. It
requires the causal-feature recipe's existing choices and a `sample_design`
object with `method: native-closed-observation-windows-v1` and
`minimum_disjoint_occurrences`. The minimum must be at least the existing
`min_independent_decisions`, and no greater than 1,000,000. These are prospective
requirements, not supplied counts or results. Original recipes reject the new
object; the new recipe requires it. Unknown methods and schema downgrades fail.

The deadline must cover the last possible declared decision's maximum holding
period plus `min(60, max_quote_age_seconds)` for every actual registered variant.
The owner rejects a plan whose bound omits this final quote acceptance window.
Existing declarations retain their earlier rules and exact hashes.

The compiled assessment derives each selected decision's observation interval
from retained owners. See [disjoint sample design](paper-evaluation-assessments.md#disjoint-sample-design)
for closed-window selection, missing labels and the statistical boundary.

## Prospective registered-search bound

Recipe `native-threshold-search-history-plan-v6` retains declaration v8. It
requires the disjoint-sample recipe's existing choices and a
`search_multiplicity` object with `method:
native-receiver-strategy-registry-history-v1` and
`maximum_searched_strategies`. The bound must be at least the number of declared
variants and no greater than the 64-definition strategy registry capacity. It is
a prospective upper limit on the receiver's actual registered search, not a
supplied count, significance level or result. Earlier recipes reject the object;
the new recipe requires it. Unknown methods and schema downgrades fail.

The declaration does not inspect the registry beyond its pinned variants. The
assessment owner counts the search at its own clock, including strategies
registered after the declaration. See [registered search history](paper-evaluation-assessments.md#registered-search-history)
for the count, failure and statistical boundary. Existing declarations keep
their earlier rules and exact hashes.

## Prospective corporate-action coverage

Recipe `native-threshold-corporate-coverage-plan-v7` retains declaration v9 and
adds `corporate_coverage` to the planned checks. It requires the
registered-search recipe's existing choices and a `corporate_coverage` object:

```json
{
  "method": "alpaca-polygon-issuer-window-reconciliation-v1",
  "process_lag_days": 10,
  "instruments": [{
    "symbol": "SPY",
    "alpaca_journal_id": "journal-...",
    "alpaca_root_collection_ids": ["spy-process-2026-10"],
    "polygon_journal_id": "polygon-journal-...",
    "polygon_dividends_collection_id": "spy-dividends-2026",
    "polygon_splits_collection_id": "spy-splits-2026",
    "issuer_journal_id": "issuer-journal-...",
    "issuer_collection_id": "spdr-workbook-2026-10"
  }]
}
```

The ex-date window is the plan's `window_start` to `window_end`, as UTC dates.
`process_lag_days` (0 to 120) bounds how long after the window Alpaca may
process an in-window action; see
[window coverage](corporate-window-coverage.md). Each variant's instrument
needs exactly one entry, sorted by symbol, with 1 to 8 distinct Alpaca root
collections and distinct Polygon dividend and split collections.

The plan names acquisition identities only. It supplies no rows, hashes, matches
or result, and the collections need not exist yet: they are acquired after the
window closes. Issuer fields are required exactly for instruments with a
verified issuer method, currently SPY with the SPDR workbook and CUSIP
`78462F103`; other instruments must omit them. Earlier recipes reject the
object, and schema downgrades fail. Existing declarations keep their earlier
rules and exact hashes.

## Prospective total-return benchmarks

Recipe `native-threshold-total-return-plan-v8` retains declaration v10. It
requires the coverage recipe's choices and adds one field:

```json
{"total_return_method": "usd-counterfactual-observed-bid-reconciled-total-return-v1"}
```

It commits the assessment to apply each covered instrument's reconciled
dividends and splits through
[total-return accounting](total-return-accounting.md) and to decide
`benchmark_comparison` against the plan's existing `minimum_net_excess_cents`.
That threshold was declared from the first plan version but unused until now.
The planned checks are those of v9. Earlier recipes reject the field, the
field requires a `corporate_coverage` plan, and a downgrade to v9 fails.

The coverage window of a v10 plan ends at the UTC date of `outcome_deadline`
instead of `window_end`. Positions and the passive benchmark stay open until
the outcome cut, which can fall up to 48 hours after the window, so an action
on those dates changes their total return. Coverage sources must therefore be
acquired after that later date ends. v9 plans keep the observation window.

## Prospective threshold sensitivity

Recipe `native-threshold-sensitivity-plan-v9` retains declaration v11. It
requires the total-return recipe's choices and adds:

```json
{"sensitivity": {
  "method": "native-threshold-neighbor-total-return-v1",
  "minimum_neighbor_net_cents": 0,
  "neighborhoods": [
    {"strategy_id": "baseline", "neighbor_ids": ["baseline-above", "baseline-below"]}
  ]
}}
```

Each neighborhood names an assessed variant and at least two neighbors. Each
neighbor is another declared variant with the same method, symbol, feed,
input evidence class, quantity and safety terms. Only `entry_at_or_below_cents`
differs, and at least one neighbor's threshold lies on each side of the
center's. Neighborhoods are sorted by strategy ID and neighbors are sorted and
distinct. `minimum_neighbor_net_cents` is zero or more, so a loss never meets
it. Neighbors are declared variants, so they count toward the
`search_multiplicity` bound.

The assessment decides `parameter_and_cost_sensitivity` from the
[retained total returns](paper-evaluation-assessments.md#threshold-neighbor-sensitivity).
The cost half comes from the stress model, which every plan already requires to
cost at least as much as the policy's base model on every term. The planned
checks are those of v10. Earlier recipes reject the field, it requires a
`total_return_method`, and a downgrade to v10 fails.

The user accepted this rule on 2026-11-01, as proposed by the agent.

## Prospective holdout returns and sign test

Recipe `native-threshold-holdout-sign-test-plan-v10` retains declaration v12.
It requires the sensitivity recipe's choices and adds:

```json
{"statistical_tests": {
  "method": "native-holdout-return-day-clustered-sign-test-v1",
  "minimum_holdout_net_cents": 0,
  "minimum_trading_days": 20,
  "alpha_basis_points": 500
}}
```

`minimum_holdout_net_cents` is zero or more. `alpha_basis_points` ranges from
1 to 1,000 (0.01% to 10%). `minimum_trading_days` ranges from 1 to 32, the
most decisions a variant's book can hold. The plan must make a pass
reachable: with every one of `minimum_trading_days` units positive, the sign
test's p-value of 2^-n must not exceed alpha divided by
`search_multiplicity.maximum_searched_strategies`. With alpha 5% and a bound of
3, that needs at least 6 days.

The assessment decides `out_of_sample`, `sample_sufficiency` and
`search_multiplicity` as described in
[holdout returns and sign test](paper-evaluation-assessments.md#holdout-returns-and-day-clustered-sign-test).
The planned checks are those of v11. Earlier recipes reject the field, it
requires a `sensitivity` plan, and a downgrade to v11 fails.

The agent chose these methods on 2026-11-07, under the user's delegation for
the remaining #282 checks.
