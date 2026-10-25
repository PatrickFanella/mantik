# Owned admission reviews

Issues #19 and #21 require a service-owned admission lifecycle. This first slice
retains non-grant reviews of actual finalized proposals and their captured
readiness. It cannot issue an admission, reserve capital, retire an active
strategy or authorize an effect. The full applicable evaluation recipes,
positive admission and retirement issuer, execution reconciliation and atomic
financial postings remain unfinished.

The authenticated service provides:

- `POST /v1/admission-reviews`
- `GET /v1/admission-reviews`
- `GET /v1/admission-reviews/{id}`

A command contains `id`, `receiver_id`, `comparison_id`, `comparison_sha256`,
`decision_id`, `decision_sha256`, `previous_id` and `previous_sha256`. The first
review uses empty predecessor fields. Reassessment names the exact current
review for that finalized decision. A stale predecessor conflicts, so concurrent
reassessments cannot branch the chain. A changed decision pin also conflicts.
The comparison and finalized decision must be actual records owned by the
receiver. Only readiness v3 comparisons are supported; older records do not
acquire new evaluation evidence retrospectively.

Callers cannot supply clocks, check results, narrative, account selections,
balances, snapshots, admissions or effects. Unknown or duplicate fields,
trailing JSON, query parameters and bodies on reads are rejected. There is no
new offline command or financial write route.

## Outcomes and reassessment

`mantik-owned-admission-review-v1` uses the compiled method
`retained-non-grant-admission-review-v1`. Every record retains the command,
global issuance sequence, service timestamp, comparison timestamp, exact owner
readiness, outcome, reason and reconsideration triggers. It has a SHA256 digest.

The method checks these conditions in order:

1. At or after the finalized proposal's `valid_until`, the review is `expired`
   with reason `finalized_proposal_expired`. Reconsideration requires a new
   finalized decision. This is proposal expiry, not expiry of a granted admission.
2. A retained failed prerequisite produces `blocked` with reason
   `retained_necessary_evaluation_check_failed`.
3. A retained pending prerequisite produces `pending` with reason
   `retained_evaluation_window_open`.
4. Otherwise the review is `deferred` with reason
   `required_admission_gates_unavailable`.

`admission_available`, `execution_available` and `qualification_credit` remain
false for every outcome. A matching assessment, an observed occurrence count or
an unqualified source cannot create a passing branch. The review preserves its
historical comparison's readiness; reassessing that same comparison does not
invent newer evidence. To review newer retained readiness, first create a new
owner comparison, then name it and the current review as predecessor. A
reassessment cannot move back to an earlier comparison timestamp.

Catalogue `records` are ordered by issuance sequence. `current_ids` identifies
the last retained review for each finalized decision, sorted by review ID.
These are historical review heads, not active grants. Reads do not consult a
new clock or rewrite a previous outcome.

## Persistence and recovery

Memory uses its aggregate mutex. PostgreSQL replays the actual strategy,
policy, finalized-decision, declaration, outcome, assessment and comparison
owners in the same transaction as review issuance. Writes use the existing
collector → state → receiver lock order; reads use a repeatable-read snapshot.
A matching envelope hash alone cannot establish source custody.

Issuance clocks cannot precede the selected comparison, collector clock or
previous review issuance. The same command identity returns the original
receipt with `duplicate: true` without reading a clock, including after later
reviews. Catalogue reads and retries replay the entire review chain against
actual owners. Fabricated statuses, omitted predecessors and rehashed source
corruption fail closed. Restoring valid source and review records restores the
original receipt.

An INSERT or COMMIT failure returns no review receipt. A transport failure at
COMMIT can leave the outcome uncertain: inspect the owner catalogue before
retrying the same identity. Do not create another ID to resolve uncertainty.
Restart retains the original chain without issuing another review or any
financial action. Operational fixture state and allocation balances are unchanged.

Schema 53 adds `mantik_owned_admission_reviews`, with at most 128 retained
reviews and a 32 KiB limit per record. Capacity fails closed without eviction.
There is no deletion or reset endpoint. The migration changes no existing
payload; a schema 52 binary refuses this newer database. Rollback requires a
verified matching binary and backup. Removing schema markers is not a downgrade.

Verification covers selected and excluded decision evidence in Memory and
PostgreSQL, exact retries, competing reassessments, the exact expiry boundary,
backdated clocks, rehashed corruption, restored readback, deferred COMMIT failure,
isolated restart, migration rollback/checksums and the authenticated HTTP flow.
Manufactured test evidence and these passing checks provide no runtime or
strategy qualification credit.

New v10 comparison records retain v4 admission readiness with the exact failed
checks of each latest owner-replayed assessment for the selected variant. For
selected decision evidence, any retained necessary failure dominates pending
sample status; absence of failures still grants nothing. The review consumes
that readiness and preserves expiry precedence. Historical v9/v3 records retain
their original bytes and clock-free retries. Schema 62 fences older writers
without rewriting payloads; rollback requires the matching backup and binary.
