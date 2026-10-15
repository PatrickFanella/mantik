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
profitability or permission to trade. Aggregated search-history assessment,
multiple-testing controls and promotion remain separate work.
