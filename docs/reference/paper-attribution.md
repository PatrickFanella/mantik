# Service-owned paper attribution readbacks

`GET /v1/paper-attribution` joins the service's retained native paper decisions,
forward-price assessments, allocation comparisons and retained non-grant admission
reviews. The authenticated targeted
read is `GET /v1/paper-attribution/{decision_id}`. Both use the existing bearer
token, accept no query parameters and make no model or provider requests.

This is a read-only part of #24. Funded execution attribution, passive/no-action
economic comparisons, social/copy observation-delay analysis, calibrated forecast
quality and remaining CLI/TUI presentation remain unfinished. Issue #24 stays open. These
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
and `baseline_status: economic_baselines_unavailable`. Executed quantity, fees
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
ledger histories. The response schema is `mantik-owned-paper-attribution-v2`;
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
or a performance rate. Forecast calibration remains unavailable.

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

This delivers CLI readback for the implemented phase attribution. The interactive
TUI, calibrated forecasts, economic baselines, funded execution measurements and
social/copy delay analysis remain open under #24 and its dependencies.


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
