# Service-owned paper attribution readbacks

`GET /v1/paper-attribution` joins the service's retained native paper decisions,
forward-price assessments and allocation comparisons. The authenticated targeted
read is `GET /v1/paper-attribution/{decision_id}`. Both use the existing bearer
token, accept no query parameters and make no model or provider requests.

This is a read-only part of #24. Funded execution attribution, passive/no-action
economic comparisons, social/copy observation-delay analysis, calibrated forecast
quality and CLI/TUI presentation remain unfinished. Issue #24 stays open. These
readbacks do not supply the admission or atomic funded effects still needed by
#19/#20/#21, and they do not start a soak or grant qualification.

## Trace each phase

Rows are sorted by finalized decision ID. Each row contains:

- The complete immutable [decision record](owned-paper-decisions.md), including
  strategy/record hashes, input identity, evidence basis, action and reason.
- The actual registered native method, symbol, evidence class and definition hash.
  `forecast_status: native_predicate_not_calibrated_forecast` distinguishes a
  threshold predicate from a calibrated prediction.
- All retained [forward-price assessments](owned-paper-outcomes.md) for that decision,
  preserving each assessment's ID, recipe, basis hash, status and observation
  timestamps. `not_assessed` means no assessment is retained;
  `assessments_retained` directs the reader to their individual statuses.
- Each retained allocation choice for the decision: comparison/record/policy
  identities, owner-selected account, comparison time, status, reasons,
  reconsideration triggers and requested funding.
- The corresponding owner-derived execution cost ceiling when that comparison
  declared an execution model. This is a ceiling, not an expense or fill.

Repeated assessments of one decision remain distinct assessments. They do not
become independent samples, repeated economic contributions or a larger claimed
sample size. The view computes no success rate, return average or profitability
score from these records. A finalized abstention remains a valid decision with
its own reason; it is not recast as a failed job. Assessment statuses such as
`waiting_horizon`, `price_unavailable` and `not_applicable` remain visible.

Forward quote deltas describe observations, not fills, realized returns or a
passive portfolio. There is no owned execution/economic-effect issuer for this
path yet. Accordingly each row has `economics.status: owned_execution_unavailable`
and `baseline_status: economic_baselines_unavailable`. Executed quantity, fees
and realized PnL are JSON `null`. They are never filled with zero from a missing
receipt, a positive price delta, a cost ceiling or a successful workflow.

## Snapshot, bounds and failures

Memory reads use one aggregate lock. PostgreSQL reads use one repeatable-read,
read-only transaction across decision, strategy, feed, outcome, policy and
allocation sources. Each existing owner replays its full retained history before
the join. A concurrent write appears wholly before or after the snapshot.
Selected reads validate all source histories too; unrelated corruption cannot be
hidden by choosing a valid decision ID. Invalid bindings, orphan outcomes or
corrupt histories fail the complete read without a partial response.

The catalogue's `source_sha256` binds the replayed decision, outcome and allocation
catalogues, including the allocation ledger histories. Each row and the complete
catalogue have their own SHA-256 with their own `sha256` field empty during
hashing. A hash is an integrity reference; it does not attest ownership outside
the actual service read. The authority is `service_owned_phase_attribution_only`.
Execution availability and qualification credit remain false.

The existing owner retention bounds apply, plus an 8 MiB serialized attribution
catalogue limit. Results are detached copies and retain deterministic owner
ordering. The API does not truncate history to meet the bound. A missing decision
returns 404, invalid IDs or query parameters return 422, and unavailable or
invalid storage returns 503. There is no attribution write route, supplied
balance/return import, caller clock, model selector or economic override.

No schema or stored record changes are required. Tests use manufactured local
sources to check known quote deltas, absent assessments, valid no-action
records, duplicate assessments, deferred allocations, ceilings versus costs,
concurrent reads/writes, detached results, self-rehashed source corruption,
authentication, query rejection and reconstruction after a PostgreSQL reconnect.
The actual public finalizer and outcome evaluator are exercised separately from
pure storage fixtures. Local checks, hosted CI, deployment and qualification are
separate evidence states.
