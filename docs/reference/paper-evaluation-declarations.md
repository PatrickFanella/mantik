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

A command contains `id`, `receiver_id`, the fixed recipe
`native-threshold-paper-evaluation-plan-v1`, `policy_id`, `policy_sha256` and
`plan`. Callers cannot supply an account, balance, clock, finalized decision,
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

The stress model must be valid, differ from the base model and be no more
favorable in any declared dimension: fees, commissions, slippage and latency do
not decrease; displayed-liquidity participation and maximum quote age do not
increase. These are declared sensitivity assumptions, not measured costs or a
completed sensitivity test. Stop-gap and partial-fill behavior still belongs to
the versioned fill method.

## Time, family and sample boundaries

The service owns `declared_at`. `window_start` must be strictly later than that
clock and no more than 365 days away. The observation window is at most 90 days.
The outcome deadline is later than its end, no more than 48 hours afterward,
and covers every variant's complete maximum holding horizon. All timestamps
must be JSON-encodable. A new declaration cannot use an already-started window
or move the declaration clock behind an earlier retained declaration.

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
