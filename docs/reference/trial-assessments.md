# Native trial condition assessments

`trial-assessment` compares one retained native run with the integer conditions
in its registered hypothesis publication. It reads existing evidence without
executing, resolving or changing the attempt.

```sh
./bin/mantik trial-assessment --api-url http://127.0.0.1:40880 --id ATTEMPT_ID
```

The authenticated endpoint is `GET /v1/trial-attempts/{id}/assessment`. It accepts
no query parameters or write commands. A missing intent returns 404, malformed
IDs or query parameters 422, and unavailable or inconsistent retained evidence
503. An existing attempt without a comparable result returns a structured
`not_evaluable` response with HTTP 200. The CLI exits successfully when that
response is read; it does not treat research failure as a transport error.

## Evidence and comparison

The service reads the intent, registration, publication, optional run and exact
result body in one consistent storage snapshot. PostgreSQL uses one statement;
the memory store holds its mutex and returns a detached copy. The assessment
rechecks receipt hashes, publication/declaration/method/horizon bindings, run
ownership references, result digest and size, native result identity and method
order. Missing or null metric fields cannot become zero-valued evidence.

The response schema is `mantik-native-trial-assessment-v1`. It includes source
receipt hashes, hypothesis identity, all three declared conditions, an ordered
entry for every planned method and benchmark, and a hash of the complete
assessment with its own hash field empty. The parent registration remains a
pinned lineage reference; this targeted read does not audit the entire lineage.
No assessment timestamp is added, so the same retained inputs produce the same
response and hash. A running attempt can produce a different assessment after
its run finishes; source hashes identify the state that was assessed.

Only a `completed` native run committed before its deadline receives status
`compared`. For each applicable strategy, the response compares the declared
metric with the prediction, null and refutation thresholds using their declared
`lt`, `le`, `eq`, `ne`, `ge` or `gt` operator. Supported metrics are observations,
actions, realized PnL cents and marked PnL cents. Comparisons use integer values.

The three match flags are independent. Null and refutation may both match; all
three may be false when the declared conditions leave a gap. A gap remains
visible as three false flags. Benchmarks retain their metrics and explicit role,
with no hypothesis match flags. They cannot stand in for an applicable strategy.

For the built-in recipe and the declaration's default positive/nonpositive/
negative marked-PnL conditions, the two strategies produce -6000 and -3000
synthetic cents. Both match null and refutation. The no-action and passive
controls retain 0 and -7200 synthetic cents separately. These are arithmetic
condition matches under the fixture's zero-cost assumptions.

## Attempts that cannot be compared

| Retained state | Reason code | Metrics and matches |
| --- | --- | --- |
| Pending intent without a run | `not_executed` | No metrics or matches |
| Cancelled intent without a run | `cancelled_before_execution` | No metrics or matches |
| Unresolved run | `run_running` | No metrics or matches; no worker-liveness claim |
| Resolved missing result | `run_incomplete` | No metrics or matches |
| Failed native run | `run_failed` | Keep any partial metrics; no matches |
| Completed at or after deadline | `run_completed_after_deadline` | Keep metrics; no matches |

Every response includes all planned methods and benchmarks, even when their
metrics are absent. Failed-run error codes and result hashes remain available.
The original [intent](trial-attempts.md) and [run](native-trial-runs.md) receipts
are preserved. This endpoint does not select a best attempt or replace the
existing attempt-history API.

Authority is `synthetic_condition_comparison_only`; `qualification_credit` is
always false. A scalar condition match does not establish a mechanism, source
truth, statistical significance, research validity, prospective paper
profitability or permission to trade. Statistical search-history assessment,
multiple-testing controls and promotion remain separate work.

## History for one registered trial

```sh
./bin/mantik trial-assessments --api-url http://127.0.0.1:40880 --id TRIAL_ID
```

The authenticated endpoint is
`GET /v1/hypothesis-trials/{id}/assessments`. The ID identifies a registered
plan, rather than an attempt or hypothesis. It accepts no query parameters,
filters or write commands. A missing plan returns 404, invalid arguments 422,
and unavailable or inconsistent evidence 503.

The `mantik-trial-assessment-history-v1` response includes every retained
attempt for that plan in start-time/ID order, up to the existing global limit of
32 intents. It contains each complete targeted assessment, total attempt count,
compared/not-evaluable counts, and counts by reason code. A plan never attempted
returns an empty assessment array and zero counts with its registration and
hypothesis pins. No successful-only or best-result projection is offered.

Before reading result bodies, the service captures an index of the plan and all
attempt/run receipt hashes in one storage snapshot. The index schema is
`mantik-trial-evidence-index-v1`; its hash binds the ordered catalogue, including
attempt start times and absent-run references. Each targeted read must match
that index. The service reads one bounded result body at a time, retains only
the compact assessment, and reads the metadata index again at the end.
The response includes the initial index and a hash of the complete history.
Identical retained state produces identical hashes.

A new attempt, cancellation, acquisition, completion or resolution during the
read changes the index or an individual reference. The endpoint then returns
409 with `trial assessment history changed during read; retry the whole request`.
It returns no partial successful history and does not retry automatically.
Storage checks occur within a five-second request context; source evidence is
not locked during comparison. The stability check relies on immutable plans
and intent identities, cancellation before acquisition, and monotonic run transitions;
there is no operation for reverting an attempt to a previous state.

This history describes the current retained state of all attempts for one plan.
It does not retain every intermediate assessment snapshot or infer active worker
processes. The hypothesis-wide view below combines sibling and parent plans.
Existing intent, registration and run records are preserved. Reading the history
does not resolve or execute work, select a winner, grant qualification or alter
operational state. PostgreSQL schema 16 is unchanged.

## History across a hypothesis's plans

```sh
./bin/mantik hypothesis-assessments --api-url http://127.0.0.1:40880 \
  --id HYPOTHESIS_ID
```

The authenticated endpoint is `GET /v1/hypotheses/{id}/assessments`. The ID is an
exact retained hypothesis identity, as returned by publication. It accepts no
query parameters or write commands. Invalid IDs or queries return 422, unknown
hypotheses 404, changing history 409, and unavailable or inconsistent retained
evidence 503. The CLI validates the response's source references, lineage,
counters and hashes before printing anything.

The `mantik-hypothesis-assessment-history-v1` response contains every registered
plan for that exact question in registration-time/ID order. Each entry includes
the registration summary, full registration/publication/recipe identity hashes,
parent registration ID and receipt hash, and complete per-plan assessment
history. Parent receipt links, parent-before-child times and complete acyclic
ancestry are checked, including for plans without attempts. Full recipes and
raw result/replay bodies remain available through targeted inspection commands.
Plans are included across publications of the same exact question; declaration
and publication hashes remain distinct. Different exact question IDs are not
combined by prose similarity or instrument.

The report includes plan count, count of plans never attempted, total attempts,
compared/not-evaluable counts and counts by reason. These are counts of retained
records, without pooled financial metrics, a best-result pointer, or a winning
plan selection. A known hypothesis with no registered plans returns a pinned
empty catalogue. A plan with only cancelled intents has attempts and is not
counted as never attempted.

The service first pins the registration catalogue and each plan's attempt/run
index. It validates full registration receipts one at a time, then reads bounded
native results one at a time using the same per-plan assessment rules. Before
returning anything, it rechecks the registration catalogue and every plan index,
including plans whose bodies were already read and plans with no attempts.
Detected additions or state changes return the same 409 retry-whole-request
error as the per-plan view, with no partial report and no automatic retry. The
report carries the registration catalogue hash, all per-plan index/history
hashes and a hash of the complete response. Repeating a stable read produces the
same output and hash. The request has a five-second context, the existing
32-plan and global 32-intent limits, and no transaction or mutation lock held
across result comparisons.

This is a readback of server-admitted registrations and retained native attempts.
It does not re-open decision cases, re-evaluate publication applicability, inspect
original news sources, or verify claim truth. Its scope excludes unregistered
experiments and review-stage abstentions without a published hypothesis/trial
link. It neither proves that a search was exhaustive nor establishes research
validity, statistical significance, qualification or trading authority. Schema
16 and existing receipts are unchanged.

## Combined native and external history

The native assessment commands above retain their v1 schemas and behavior.
Two additional read-only views join that evidence to every retained external
job declaration and its reported attempts:

```sh
./bin/mantik trial-research-history --api-url "$MANTIK_API_URL" \
  --id trial-plan-1
./bin/mantik hypothesis-research-history --api-url "$MANTIK_API_URL" \
  --id "$MANTIK_HYPOTHESIS_ID"
```

The HTTP paths are `GET /v1/hypothesis-trials/ID/research-history` and
`GET /v1/hypotheses/ID/research-history`. They require the existing bearer token,
reject query parameters and accept no writes. Unknown plans/questions return
404, invalid IDs/queries 422, changed captured catalogues 409 and backend or
corrupt-evidence failures 503. The CLI validates the complete response and its
requested identity before printing it; a conflict prints no partial history.

`mantik-trial-research-history-v1` contains the unchanged native assessment
history and `mantik-external-trial-history-v1`. The external component includes
all job declarations, their complete source native-run metadata and every
retained external intent/outcome. It counts unattempted jobs, pending intents
and each reported disposition. Job and attempt IDs are ordered lexically.
Declarations with no external attempt remain visible. This component includes
metadata and sealed recipe/input references; it does not load native replay
bodies, external stdout/stderr or host receipt files. Use the targeted commands
in [trial workers](trial-workers.md) to inspect exact output separately.

`mantik-hypothesis-research-history-v1` contains the unchanged native hypothesis
history plus one external history in the same order as each registered plan.
Parent/child lineage, plans with no native attempts and plans with no external
jobs remain visible. Native and external counts have separate fields. External
job and attempt identities are unique across plans and remain subject to the
receiver's global 32-job and 64-attempt bounds. Report status counts include
completed comparisons, mismatches, library failures, incomplete captures,
cancellations, timeouts and interruptions. A missing report stays pending.
Neither view filters to a winning attempt or ranks plans.

Each source run must be complete and bound to the declaration's receiver,
registration, recipe, native artifact byte hash and native deadline flag. The
combined view also binds that exact source run and result to its native
assessment. A late completed source keeps its `not_evaluable` native disposition
and false deadline flag, even when an external comparison is available.
External intents pin their declaration and job; terminal reports pin their
original intent. Hashes cover each nested history and the combined response.

Native condition matches and external metric agreement answer different
questions. The synthetic threshold fixture can have matching native/external
metrics while simultaneously matching the hypothesis's null and refutation
conditions. The combined view preserves both findings. External reports retain
`caller_reported_external_attempt_only` authority and unknown host state. Their
validity checks establish protocol consistency, not independent reproduction,
process liveness, isolation or deadline proof. The combined authority is
`research_process_history_only`; qualification credit remains false.

Memory storage reads external metadata under one lock. PostgreSQL uses a
read-only repeatable-read transaction for registration, job, source-run and
attempt metadata. The combined reader builds native assessments through their
existing bounded, one-result-at-a-time path, then rechecks the external metadata
hash, native attempt index and, for a question, the complete plan catalogue.
Changes to a captured boundary cause a conflict so callers can request a fresh
view. A change incorporated before its boundary is captured can appear in a
consistent response. No automatic retry or process action is hidden in a read.

These views use schema 18 without changing tables or existing assessment
schemas. Tests cover every external report status, pending and never-attempted
work, a late native source, retained native failures/cancellation, native
null/refutation evidence, full catalogue capacity, source-binding corruption,
changed counters/lineage, concurrent additions/completions, authenticated CLI
readback and restart retention. Isolated runtime verification also checks the
complete combined response after backup/restore. Synthetic history does not
establish profitability, provider-backed qualification or permission to trade.
