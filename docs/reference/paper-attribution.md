# Service-owned paper attribution readbacks

## Paper execution

Current service catalogues use `mantik-owned-paper-attribution-v5`. A row whose
decision the allocation owner reserved now carries its paper execution in
`economics`:

- `effects`: every retained fill, close and release effect for the decision's
  proposal, in clock order, each with the comparison or execution step that
  committed it (`record_id`), its effect ID, amount and net proceeds. They come
  from comparison records and execution steps, archived ones included, so
  checkpoints that seal settled reservations do not remove them.
- `reserved_cents`, `filled_cost_cents`, `closed_cost_cents`,
  `net_proceeds_cents` and `released_cents`: the sums of those effects, with the
  reservation from the `reserve_plan` allocation.
- `realized_pnl_cents`: net proceeds less the closed cost basis, set once
  something has closed.
- `status`: `reserved_unfilled`, `released_unfilled`, `position_open` or
  `position_closed`. A decision without a reservation stays
  `owned_execution_unavailable` with every value null.

Fees sit inside each fill's cost and each close's net proceeds, and the ledger
does not retain share counts, so `fees_cents` and `executed_quantity` stay
null. These are paper fills from retained quotes, not qualification.

Since v5 each row compares the decision with two alternatives in
`economics.baseline`, in whole basis points rounded toward zero:

- Passive holds from the retained forward assessment's entry price to its
  forward quote. The latest `observed` assessment supplies both.
- No-action is cash, so `excess_over_no_action_bps` equals the strategy's own
  return.

`baseline_status` says what is available:

| Status | Meaning |
|---|---|
| `passive_and_no_action_compared` | Closed position and an observed forward quote: all figures set. |
| `passive_observation_unavailable` | Closed position, no observed forward quote yet: `baseline` is null. |
| `position_not_closed` | Reserved or open: no strategy return yet. Passive figures appear if observed. |
| `no_capital_committed` | Abstained, rejected, or released unfilled. Passive figures, if any, show the move the decision passed on. |

The passive window is the strategy's forward horizon, which can be longer or
shorter than the time a position was held, so a difference is not a like-for-like
holding-period return. The method is `owned-forward-quote-hold-vs-cash-v1`.
Catalogues from before v5 keep `economic_baselines_unavailable`.

The CLI and interactive view validate the sums, status and realized result
against the effects before display, and show each effect. They now also accept
`reserve_plan` allocations and `eligible` readiness records, which they
rejected before, so a receiver with a funded decision can read attribution
again. Older v1–v3 catalogues remain accepted, with execution unavailable.

## Post-decision claim reviews and source delays

Since `mantik-owned-paper-attribution-v3`, every row
includes `claim_review_history` with the actual retained typed decision reviews,
their contradictions/unsupported-field cases, and a digest. The service replays
the complete decision-review owner and any bound social/copy journal under the
same Memory lock or PostgreSQL read snapshot as decisions, outcomes, allocations
and admission reviews. Corrupt or orphaned review provenance fails the whole
read; a failed COMMIT returns no catalogue. No historical review is rewritten,
and this read creates no review, case, admission or financial effect.

For a source-bound review, `source_timings` identifies the exact capture and
records its claimed occurrence/observation times, service retention time,
decision finalization time, and whole-second observation/retention delays from
occurrence. These values come from the actual source prefix pinned by the
review, even after a later watch change, source edit or deletion. Observation
times remain recording claims, not provider attestations.

`clock_relation` distinguishes `retained_before_decision_clock`,
`retained_at_decision_clock_order_unproven` and `retained_after_decision_clock`.
The relation compares owner timestamps. Equal clocks do not establish ordering
between independent owners; an earlier clock does not prove that the source was
used by the decision. `decision_input_verified` remains false. These are sources
of a post-decision claim review, not newly discovered inputs to the finalized
market-data decision, independent evidence, observed financial fills or economic
performance. No fees, realized PnL or qualification are inferred.

The CLI and interactive attribution view show claim-review status, case IDs and
source delay/clock relation. They check the portable history hash, exact decision
and capture cross-pins, typed findings and timing arithmetic before display;
those checks do not attest service custody of imported JSON. Opaque source prose
is not printed as an explanation or used to revise authority. Typed supervision
returns the same owner readback. The existing 8 MiB catalogue and 2 MiB MCP bridge
bounds remain in force; oversized results fail rather than become partial
success. Older v1/v2 catalogue presentations remain accepted with their original
row contracts, and durable review v1/v2 records keep their bytes and hashes.

## Read routes and retained phase data

`GET /v1/paper-attribution` joins the service's retained native paper decisions,
forward-price assessments, allocation comparisons and retained non-grant admission
reviews. The authenticated targeted
read is `GET /v1/paper-attribution/{decision_id}`. Both use the existing bearer
token, accept no query parameters and make no model or provider requests.

This is a read-only part of #24. Strategy analysis is described below; funded
paper execution is covered above. Issue #24 stays open for its acceptance audit. These
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
- The exact retained admission-readiness record when the allocation comparison
  has one, including its gates, reconsideration triggers and any comparison,
  policy, account, proposal, ledger and cost bindings. Legacy comparisons omit
  this field; absence does not imply readiness. These are deferred or expired
  prerequisite checks, never an admission grant.
- The retained evaluation-declaration context for v6 comparisons, including the
  captured catalogue hash and this decision's exact strategy, policy and declared
  plan pins. A plan is `declared_not_evaluated`; an empty captured match is
  `declaration_unavailable`. Legacy comparisons omit this optional context. These
  states do not establish applicability, evaluation success or admission. Later
  declarations do not rewrite the historical attribution.
- The complete retained admission-review history for that decision, in owner
  sequence order, with the current review ID and a hash of the scoped review
  catalogue. Each review keeps its comparison and decision pins, predecessor,
  captured readiness, review time, status, reason and reconsideration triggers.
  An empty history means no review is retained. Review statuses remain deferred,
  pending, blocked or expired; a current review is never an admission grant.
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
and `baseline_status: no_capital_committed`. Executed quantity, fees
and realized PnL are JSON `null`. They are never filled with zero from a missing
receipt, a positive price delta, a cost ceiling or a successful workflow.

## Snapshot, bounds and failures

Memory reads use one aggregate lock. PostgreSQL reads use one repeatable-read,
read-only transaction across decision, strategy, feed, outcome, policy and
allocation and admission-review sources. Each existing owner replays its full retained history before
the join. A concurrent write appears wholly before or after the snapshot.
Selected reads validate all source histories too; unrelated corruption cannot be
hidden by choosing a valid decision ID. Invalid bindings, orphan outcomes or
corrupt histories fail the complete read without a partial response.

The catalogue's `source_sha256` binds the replayed decision, outcome and allocation
catalogues and the complete admission-review catalogue, including the allocation
ledger histories. Current v3 catalogues also bind the owner-replayed claim-review
catalogue. The response schema is `mantik-owned-paper-attribution-v3`;
every row has `admission_review_history`, including an explicit empty history.
Review chains are scoped by decision, so a review of another decision changes
the complete source hash without rewriting unrelated row hashes. Global review
sequence numbers can have gaps within a decision's chain. Each row and the complete
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
concurrent outcome/review writes, detached results, exact clock-free review
retries, empty and expired review histories, self-rehashed source corruption,
authentication, query rejection and reconstruction after a PostgreSQL reconnect.
The actual public finalizer and outcome evaluator are exercised separately from
pure storage fixtures. Local checks, hosted CI, deployment and qualification are
separate evidence states.

## Terminal readback

`mantik paper-attribution --api-url http://127.0.0.1:40880 --receiver-id RECEIVER`
reads the live service catalogue. The command uses `MANTIK_API_TOKEN`, accepts
an HTTP origin, and sends only GET requests. It does not open a local ledger,
run a model, assess an outcome, finalize a decision or change balances.

The default table keeps finalized abstentions separate from failed requests.
It prints each retained assessment status and allocation deferral/rejection,
prints admission-readiness gates and their retained reasons, lists each
`ADMISSION_REVIEW` with its current/historical flag, status, reason and
reconsideration triggers, labels funding
ceilings as bounds, and shows unavailable execution quantities,
fees, realized P&L and economic baselines explicitly. A quote delta appears only
for an `observed` assessment; waiting or inapplicable assessments show an
unavailable delta. Repeated assessments do not become independent sample counts
or a performance rate. `SIGNAL` shows the stored boolean predicate and marks
probability scoring not applicable: the native threshold emits no probability
forecast. `OUTCOME_EVIDENCE` explicitly reports insufficient evidence when no
assessment is retained.

Use `--format json` for the complete catalogue, source hash and row hashes.
Select an exact row with `--id DECISION --record-sha256 ROW_SHA256`; this pin is
the attribution row hash, not the nested decision record hash. The row can
change when the service retains another assessment, allocation comparison or
admission review for that decision.
A stale pin fails before producing output; read the current catalogue to review
the new row before selecting its hash.

The client checks bounded, strict JSON, receiver identity, envelope hashes,
nested decision/outcome links, readiness identity and context bindings, the
retained unavailable prerequisite gates, complete review-chain links and review
status derivation, and the current unavailable-economics
boundary. It still accepts legacy v1 responses without a review history;
absence in a legacy response does not establish that no review exists.
These are representation checks. Source replay and consistent snapshot
selection remain service responsibilities; a self-computed hash establishes no
qualification. Wrong receivers, invalid arguments, stale pins, HTTP failures,
oversized responses and malformed records return errors without attribution
output. An empty valid catalogue reports insufficient evidence. Source strings
are quoted in tables so control characters cannot act as terminal instructions.
The command preserves full JSON for inspection rather than summarizing absent
measurements as zero.

`mantik supervise --view decisions` uses this same table for `a ID` on the
displayed page. Use `aj ID` for the full validated row as JSON. Both forms reject
stale catalogues, changed receivers, malformed rows and substituted decisions;
failed or canceled inspection does not retain a result as a fresh readback.

`SOURCE_TIMING` identifies each review and capture, source kind (`social` or
`copy`), capture hash, declared observation delay, total delay until owner
retention, and ingestion delay from declared observation to owner retention.
Durations are whole seconds truncated toward zero. The owner-retained timestamps
are used directly; independently rounded components need not sum to the rounded
total when timestamps contain fractions of a second. The existing clock relation
states whether retention was before, after or at the decision clock; equal clocks
do not prove ordering. Missing timings remain unavailable. These are retained
post-decision review sources, with `decision_input_verified=false`; the view does
not derive causal decision inputs or counterfactual copy returns from them.

The `--analysis` flag selects the strategy and source-delay analysis below.
Interactive `aa ID` displays the selected row's analysis table, and `aaj ID`
returns its JSON. Both use the same pinned decision inspection as `a ID`.

## Strategy outcomes and source-delay analysis

`paper-attribution --analysis` accepts the existing receiver, selected-row pin,
and `--format table|json` options. It derives `mantik-paper-attribution-analysis-v1`
from the validated service readback without another request, a model call, or
an owner write. `scope: returned_rows_only` and `rows_sha256` identify the input
set. The complete report has its own `sha256`. Catalogue analysis covers that
returned catalogue; selected-row and interactive analysis cover one decision.
Neither claims complete archived-history coverage.

Quality groups separate exact strategy hashes and evidence classes. Each group
counts decisions, entry candidates, valid abstentions, and all retained outcome
assessments. The selected outcome is the latest assessment by evaluation time,
then greatest original ID on a clock tie. A later unavailable assessment remains
unavailable even if an earlier assessment observed a favorable quote. The report
counts positive, flat, and negative quote deltas only for entry candidates whose
selected assessment is observed. These descriptive counts are not forecast
accuracy, independent samples, or trading returns.

`probability_scoring` is `not_applicable_boolean_predicate`, and
`independent_samples` is null. A native threshold has no probability forecast.
The status distinguishes no entry candidates, insufficient observed outcomes,
and available descriptive observations. Repeated assessments do not increase
the number of observed candidates.

Each decision trace binds the decision and row hashes, the selected outcome and
its evaluation time, and the latest retained comparison by time and then ID.
The original comparison hash, time, readiness hash, evaluation gate, and exact
applicable assessment evidence remain in the report. The table names retained
sample status, source applicability, and every failed check with its reason.
These facts describe the comparison's original cut; inspection does not rerun
an evaluation at the current clock or issue new admission authority.

Delay groups separate strategy version, evidence class, and source kind.
`unique_captures` counts distinct original capture identities within a group;
`review_links` counts the decision-review links. Reusing a capture in multiple
reviews does not weight that capture's delay more heavily. Conflicting hashes
or occurrence, observation, or retention clocks for one capture are rejected.

Observation delay runs from claimed occurrence to claimed observation.
Ingestion delay runs from claimed observation to owner retention. Availability
delay covers the entire occurrence-to-retention interval. Minimum, median, mean,
and maximum use the original timestamp precision, then truncate to milliseconds.
An even-sized median is the mean of the two central durations. Missing source
timing produces no delay groups and `source_timing_unavailable`.

Source traces preserve every review hash, capture identity and hash, original
timestamps, decision pin, and clock relation. Counts and clock relations describe
retained provenance. `decision_inputs_verified`, `copy_returns_verified`, and
`qualification_credit` remain false. Original recordings keep their declared
evidence class and provider-access uncertainty.


The terminal readback displays `EVALUATION_PLANS` with the captured source hash,
status and false admission/execution/qualification flags, followed by any
`DECLARATION` IDs and hashes. It validates the projected decision, strategy and
policy pins, sorted unique declaration pins and no-grant boundary before printing.
These integrity checks do not independently attest source custody or historical
snapshot completeness. The field is derived from the replayed comparison, not a
fresh lookup of the current declaration catalogue. No stored record, command,
schema migration, economic metric or authority changes.


Version 8 allocation comparisons project their captured assessment pins and
assessment-source hash as `mantik-paper-attribution-evaluation-context-v2`.
The CLI validates identity, declaration scope and the explicit pending,
incomplete or blocked status before printing the record. This context comes
from the retained comparison cut; later assessments do not rewrite it. Older
comparisons preserve their original attribution context. These pins establish
owner provenance only; economic results, admission and qualification remain
unavailable.


Readiness v3 additionally reports the exact decision's retained assessment
selection and necessary sample status. It binds the projected evaluation
binding hash, retains the latest assessment per declaration at the comparison
clock, and distinguishes absent/excluded decisions from selected research
rows. The CLI checks these portable shapes and pins before rendering; the
service remains responsible for replaying actual sources. Pending or failed
necessary checks never establish a passing full recipe or economic authority.

When execution-safety requirements are present, the CLI also requires admission
readiness with an account binding and checks that both account hashes match.
Rehashing a safety record for another account or removing that binding is
rejected before rendering. Historical rows without safety requirements retain
their existing compatibility. These checks verify relationships within the
projection; the service still owns source replay and economic authority.

For readiness v5, the CLI additionally requires `execution_safety_sha256` to
match the projected safety record, or to be absent when there is no such record.
Older readiness versions retain the omitted pin. Owner replay supplies the
actual requirement record; portable consistency remains separate from custody
and economic authority.
