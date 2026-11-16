# Prospective strategy assessment

Issue #92 adds policy `prospective-purged-day-sign-v1`, an immutable assessment
plan, a distinct qualification execution mode, and automatic paper admission.
It does not complete the natural portfolio window (#108), establish real market
performance, or authorize live trading.

## Declare before collecting

Keep the existing v1 forward declaration. Its strategy, benchmark strategy,
window, holding limit, outcome deadline, sample minima, excess-return floor,
drawdown ceiling, search cap, gate and book bindings remain authoritative.
Declare a separate `forward.AssessmentPlan` before `WindowStart`:

| Field | Meaning |
| --- | --- |
| `ID`, `DeclarationID` | Immutable assessment identity and existing forward declaration. One assessment plan per declaration. |
| `Policy` | Exactly `prospective-purged-day-sign-v1`. |
| `TrainEnd` | End of the in-window calibration segment, no earlier than the window start. |
| `PurgeSeconds`, `EmbargoSeconds` | Each at least the declaration's maximum holding duration. |
| `HoldoutStart` | At least `TrainEnd + purge + embargo`, strictly before the entry-window end. |
| `AlphaBasisPoints` | One-sided significance, 1–1000 basis points, corrected against the actual family starts at assessment. |

The store records the original declaration clock and full forward-record hash.
Exact retries preserve bytes and clock; changed retries conflict, including after
expiry. A short window with long holding limits may have no feasible holdout.
Declare a sufficiently long window in advance; do not shorten the purge later.
Migration 127 adds only the append-only assessment-plan table. It neither edits
v1 records nor converts old evidence.

## Collect through compiled service code

`IncubationRunner.Run` and the existing `mantik incubation --action run` command
support a `Qualification` object in their input. It contains `PlanID` and a
`Benchmark` observation. This selects `qualification_plan`, a separate journal
event kind. Use a fresh sleeve bound to the declaration; a sleeve with earlier
incubation plans cannot be converted into a qualification owner.

The compiled runner builds the strategy view, calls the same strategy interface,
retains the original source views or explicit code-controlled exit state, sizes
orders, and uses the existing journaled paper adapter and sync path. The caller
cannot supply `DecisionSources`; the service captures them. Strategy source
views retain the original bar publication, observation and availability clocks.
The CLI still requires its normal paper credentials, private journal, reviewed
source input, and service-owned safety worker. J2 adds no provider calls of its
own. Its unit and database tests use isolated fixtures only.

Every collection carries an observed total-return benchmark index, including
SPY distributions or observed cash yield. `BenchmarkObservation` pins
`StrategyID`, positive decimal `Index`, `EvidenceClass`, `ObservedAt`,
`ReceivedAt`, `SourceSHA256` and original `Raw` bytes. `Raw` is a byte slice
(base64 in JSON), preserving whitespace and original bytes. It is a collector
statement containing `StrategyID`, `Index`, `EvidenceClass`, `ObservedAt` and
`ReceivedAt`; those fields must match the envelope. The source hash is SHA-256
of the original bytes. Both benchmark and execution observations must be
`prospective_market_unqualified`, received by execution and at most one minute
old. Every qualifying fill must also execute within one minute of its
retained benchmark observation; delayed venue fills without that coverage are
unavailable. The benchmark identity must match the frozen forward declaration. A
constant cash index requires a retained observation; code never invents cash
returns, substitutes raw SPY prices for total return, or fills a missing sample
with zero. Imports are trusted source observations, not independent provider
attestations: their truth and completeness remain the collector's responsibility.

Venue response custody remains in the private Alpaca journal. Owned fill events
retain its original activity hash and execution clock separately from receipt
time. Qualification sync refuses a venue activity with missing commission
instead of inventing a zero fee; normal reconciliation failure handling stops
new exposure. Explicit observed zero commissions remain valid. Quote observations and benchmark statements stay in the retained execution
plan. Every assessment pins the complete journal prefix, source declaration,
window, assessment plan, fill-event hashes and complete family start snapshot.

## Assess and apply

`mantik forward-assessment` supports `declare`, `show` and `assess`, with the
same explicit `--database-env` and `--id` convention as `forward-declaration`.
`declare` reads `--input`; `assess` accepts no results or pass flags. Run
`mantik forward-assessment --help` for flags. Storage-backed HTTP operations are:

| Method and path | Operation |
| --- | --- |
| `POST /v1/forward-assessment-plans` | Declare an assessment plan. |
| `GET /v1/forward-assessment-plans/{id}` | Read its original record. |
| `POST /v1/forward-assessment-plans/{id}/assess` | Empty body; compute assessment and automatically apply a pass. |

These routes require the configured bearer credential, reject query parameters,
and bound input sizes. No model chooses an outcome, action, size or gate flag.
Collection remains bounded runner invocations; J2 installs no scheduler or host
service. An operator or supervisor invokes assessment after the outcome deadline.
There is no separate manual promotion step.

The assessment uses only actual fills attached to this plan's
`qualification_plan` events. Incubation returns, backtest returns and earlier
incubation plans never count. Whole-sleeve flat-to-flat holding episodes are the
independent decisions. Simultaneous/overlapping positions merge into one episode;
a multi-day holding never creates independent days merely by crossing midnight.
Episodes decided before the holdout, including those crossing its boundary,
are excluded. Episodes are clustered by their original entry decision date in America/New_York;
zero or negative day excess is a nonpositive sign-test observation.

Each episode uses the sum of its actual buy notionals as a conservative capital
denominator, the less favorable venue/model cash effect of every fill, and its
entry/exit observed benchmark indices. This policy reports the sum of episode
returns and benchmark excesses, not strategy or portfolio TWR. The drawdown gate includes retained execution and hold-cycle quotes, valuing
longs at bid and shorts at ask with the worse fill cash effects. These marks
do not become independent statistical samples. Between-observation extremes
remain unknown; this is observed drawdown, and current book risk checks remain
necessary. `Fees` and `ModelFees` report actual and modeled fees, `CashEffect` reports
aggregate conservative cash P&L, and `NetReturn` and `BenchmarkReturn` expose
the episode-return sums. Retained model
fills expose modeled fees and each adverse cash penalty for inspection.

A pass requires the declared session and independent-decision minima, net excess
strictly above both zero and the declared floor, drawdown within its ceiling, and the exact
one-sided binomial tail after Bonferroni correction. The correction uses **actual
family starts**, including failed, crashed and unfinished starts; the declared
maximum is only a bound. Pending family accounting, absent observations, malformed
costs, unfinished exposure and missing samples produce explicit `unavailable`
reports with empty unavailable metrics. Statistically valid negative outcomes
produce retained `failed` reports. Unavailable reports can be recomputed when
accounting completes; finalized pass/fail events have immutable retry behavior.

The book row and family registry fence cover evaluation and promotion in one
transaction (one mutex in memory). A pass appends a `qualification` event and
changes the real roster stage to `qualified`. Subsequent runner cycles can
allocate from the qualified pool after the original entry window closes. Every
new admission rechecks the current family total under the family fence; a new
search start invalidates the old admission. Later book writes and owner controls
cannot race an assessment into overwriting them. Raw `ApplyCappedBook` calls
cannot import qualification events or caller-chosen outcomes.

## Demotion and limits

The existing daily overseer computes evidence-backed reviews. Once it demotes
or retires a promoted sleeve, this qualification grant stops admitting new risk.
Subsequent allocations have a zero budget and the runner requests protective flat
targets without evaluating the strategy. A later grow review or retry of the old
assessment cannot restore the grant. Owner pause, retirement, kill, reduce-only,
and persistent hard halt remain independent overrides. Current position, pool,
sleeve and account caps still apply, including the existing 1% cap for a
model-driven version even after prospective admission. A new model identity gets
no historical gate credit and needs its own forward declaration and evidence.

This first policy uses a fresh sleeve for a separate window and has no reset of
an old grant. The existing immutable roster also prevents transferring an
unchanged strategy version to a new sleeve in the same book. Requalification
currently needs a separately versioned strategy and its own declared window;
this change does not add a reset or transfer operation. General multi-leg qualification is not implemented here; its
execution/evidence path is separate (#85). Benchmark source acquisition and
truthfulness are upstream collector responsibilities. Natural elapsed time,
real quote/venue behavior, and real qualification remain untested by fixtures.

## Durable collection authority

Ordinary `ApplyCappedBook` commands cannot append `qualification_plan` or carry
collection evidence, including under another command kind. Collection uses
`CollectQualification` on a store constructed with the compiled service evaluator.
A default store has no collection authority. The evaluator is a process dependency;
commands cannot supply an evaluator or an approval flag.

The supervised and one-shot CLI share the same strict input schema, compilation,
and full-input hash. Collection retains that original input manifest, then
recompiles its library or expression configuration under the book/family fence.
The compiled identity must match the gate and forward declaration. The evaluator
rebuilds the view from the original clock-validated bars, actions and universe,
reconstructs the sleeve's book from the journal prefix, and recomputes the decision.
It compares targets, state and retained view bytes. Storage stamps the collection-time
search count so later starts cannot change replayed decisions; assessment statistics
still use the current actual search ledger. A storage-observed pending family may
retain a protective flat control with `PendingSearchTotal` (`-1`), which explicitly
means unavailable rather than a finite N. It cannot earn assessment credit. New
entry still requires a reconciled family count. Flat collection exits also retain
the exact gate-declared fill costs; their entry-admission exception does not waive
cost validation. Code-control outcomes are
recomputed from the window and retained owner controls. Neither a self-consistent
hash nor a caller-written source/control record proves collection authority.

Retained views include the actual `View.Universe()` consumed by the strategy and
the original `UniverseReport`: membership, screening configuration, reference
observation clock, as-of clock and source limitation flags. The view owns a copy
and its report accessor returns a copy. Different consumed memberships therefore
produce different evidence bytes even when all bar bytes match.

Assessment repeats compiled validation against each collection's original journal
prefix before it can publish or return a retained qualification. Old null sources
or missing compiled manifests fail closed; this change does not rewrite historical
rows or grant them new credit. Qualified allocation and final submission also
recompute the retained grant and its collection evidence, rejecting a promotion
issued from invalid old evidence. No additional migration beyond 127 is required.
The callback performs no storage/provider reads under the fence. PostgreSQL takes
the book row before the family lock, including for flat collection decisions.

A collection for the current allocation may follow reconciliation. Promotion
changes the roster and invalidates that receipt. Qualified submission uses the
current fenced qualification and family count. After promotion, the safety worker
uses owner controls and ongoing risk/holding checks; the completed qualification
window does not prohibit post-window qualified operation. Demotion withdraws the
grant, and assessment retries cannot restore it or clear an owner pause.

These are synthetic contract tests. A natural observation window, including #108,
has not been performed. Collection still does not prove profitability or broader
release qualification. Option/model-dependent collection and recipe work remain
separate from this stock compiled path.

The normal disposable integration run includes the compiled service PostgreSQL
contracts alongside CLI and research contracts. They exercise collection,
promotion, post-window submission and owner/family fences against durable
checkpoint history. Original journal versions count commands, not hot slots.
