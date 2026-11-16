# Forward strategy declarations

`forward-declaration` freezes a prospective evidence contract for a canonical
`strategy.Spec.ID()`. It uses the capped-book strategy runner introduced for
incubation, rather than extending the threshold runner's retained schemas.
A declaration and its eligibility response always have
`qualification_credit: false`. Minimums and the benchmark are requirements for
a later assessment; no results, assessment, promotion, or trading authority can
be supplied through this command. Automatic assessment and promotion are #92.

## Required pins

The JSON declaration contains:

- `id`, `family`, `strategy_id`, and the complete `spec` (name, code version,
  parameters, data kinds, warm-up and model-driven flag).
- `benchmark.strategy_id` and `benchmark.spec`, independently versioned from
  the candidate. A cash benchmark is a strategy whose targets are empty.
- `gate_id`, `book_id`, and `sleeve_id`. The existing gate must admit paper
  incubation, its canonical spec and family must match, and its account type
  must match the existing capped book. Registration retains a hash of the
  complete gate record and immutable book configuration.
- `window_start`, `window_end`, and `outcome_deadline` as RFC3339 timestamps.
  Registration must occur strictly before the start; an equal clock is refused.
- `min_sessions` and `min_independent_decisions` (each at least two),
  `minimum_net_excess` and `maximum_drawdown` as decimal return fractions, and
  `max_search_trials` as a prospective bound on the actual registry family.
- `max_holding_seconds` and `eligibility_ttl_seconds`, each from one second
  through 90 days. Eligibility TTL starts at the window start. New target
  decisions stop at the earlier of TTL expiry and window end.

The window may last up to 90 days and start at most 365 days after registration.
The outcome deadline must allow the entire maximum holding duration after window
end; the tail can last up to 90 days. Entry, TTL, holding and outcome intervals
exclude their ending clock. Entry includes the exact window start. Exact write
retries return the original record and clock, including after expiry; a changed
benchmark, minimum, clock, strategy, or bound conflicts.

Registration captures **every strategy-trial start** in the family, with its
original spec, configuration, evidence class, parents and input hash. Failed,
crashed, abandoned and unfinished starts count equally. Outcomes cannot remove a
start from N. Memory registration uses the registry mutex; PostgreSQL uses the
same family advisory lock as trial starts. A new start beyond the predeclared
bound closes eligibility without rewriting the declaration. Existing gate
freshness rules remain independently enforced and can refuse new exposure even
while N remains below this declaration's maximum. A model's admitted
`forward_only` gate can have zero historical starts; this grants no historical
performance or qualification credit. Records are bounded to 2 MiB.

## Callable paths

The local command uses an explicit PostgreSQL environment variable, with no
implicit development or production database:

```text
mantik forward-declaration --database-env MANTIK_LOCAL_DATABASE_URL --id trend-forward-nov --action declare --input /tmp/trend-forward-nov.json
mantik forward-declaration --database-env MANTIK_LOCAL_DATABASE_URL --id trend-forward-nov --action show
mantik forward-declaration --database-env MANTIK_LOCAL_DATABASE_URL --id trend-forward-nov --action eligibility
```

The named JSON file must contain the complete reviewed declaration above. The
CLI obtains registration time from PostgreSQL after acquiring its locks;
`eligibility` uses the current clock. These commands never call a model or broker.

The authenticated loopback service provides the same boundary:

| Method | Path | Result |
| --- | --- | --- |
| POST | `/v1/forward-declarations` | Retain or exactly retry a declaration |
| GET | `/v1/forward-declarations/{id}` | Validated immutable record |
| GET | `/v1/forward-declarations/{id}/eligibility` | Current clocks, registry N and holding-expiry flag |

These routes accept no query parameters or client-supplied result fields.
Eligibility is a clock/search check, not evidence selection, a sample-sufficiency
verdict, a return calculation, or a claim that observations qualify.

## Runtime binding and multiweek positions

In an incubation input's strategy entry, set `ForwardDeclarationID` to the
retained declaration ID. The runner verifies its strategy/gate/book/sleeve pins
and retains the binding in the incubation plan. Later runs inherit that binding;
omitting it cannot detach the sleeve. Each new plan includes bindings only for
its requested roster. A flat inactive sleeve may be omitted; its binding remains
in history and is inherited if the same sleeve returns. Pending orders or filled
exposure still prevent omission. This first version binds one declaration
per sleeve for its lifetime; use a new sleeve for a separately declared window.

During the entry interval the strategy uses its ordinary target interface.
After entry closes, a position opened within the window can remain in a
no-resize `HoldExposure` state until its holding limit or outcome deadline.
This is distinct from the distributor's existing `KeepPositions`, which reuses
old target weights and can resize after a deposit. The explicit hold creates
no orders and keeps the old target weights for replay. The store refuses raw
nonzero target writes outside the entry interval, even if they omit the binding.

Owned fills replay by actual execution time when deriving the holding clock;
late discovery of an earlier execution cannot extend the deadline. Receipt
validation and the retained receipt journal remain unchanged. The clock starts
at the oldest continuously open exposure in the sleeve. Adding or partially reducing positions does not
reset it; fully flattening does. This is conservative across multiple assets.
Pre-window exposure must flatten before it can participate in this window.
Both the decision and execution clocks are checked. At holding expiry or the
outcome deadline, the next run requests an explicit flat target without calling
`Decide` or loading strategy data. It still requires the existing paper gate,
current executable quote, order sizing, settlement and hard risk checks. It
cannot promise an actual fill or act while the runner is stopped. `sync` alone
reconciles fills; invoke `run` to request the clock-driven exit.

The book persists positions across sessions and weeks. Day-order fill clocks and
60-second quote freshness remain unchanged. Widening those would incorrectly
allow stale execution; they are separate from a filled position's holding time.
An eligibility check reads the retained book and actual registry; a historical
clock preceding retained book state is refused.

## Compatibility and qualification boundary

Migration `0124_forward_declarations.sql` adds an append-only table and is
registered in normal storage bootstrap after schema 123. Update, delete and
truncate are refused. Reapplying the migration and reopening storage retain
both forward records and threshold records. Rollback of application code can
leave this additive table in place; do not delete its retained declarations.

Threshold declarations continue through the original v1–v13 dispatch with their
original hashes, validation and replay rules. Their one-day holding/admission
and 48-hour outcome-tail limits are unchanged. `strategy.Spec` aliases the shared,
dependency-free canonical identity type; its hashing algorithm is unchanged.
New optional book/distributor fields omit zero values from JSON so legacy
journal and sizing hashes keep their original representation.

Incubation plans, modeled fills, real quote assertions, synthetic fixtures and
forward eligibility remain distinct from qualification. No incubation return,
trial status, declaration or eligibility flag earns qualification credit. The
future assessor must derive independent samples, benchmark observations, costs,
minimums and provenance from retained sources; this change does not implement
that assessor.
