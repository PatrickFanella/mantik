# Finalized decision claim reviews

`POST /v1/paper-decision-reviews` checks explicit factual claims against an actual
service-finalized paper decision. It retains an immutable assessment and opens a
review case when a claim contradicts the record or asks it to support an
unsupported field. This is a typed supervision contract for #23. It issues no
admission, financial instruction or qualification, and does not complete #23's
conversation integration, authorized operator tools or review triage workflow.

## Request and evidence

Requests use the existing bearer authentication and contain only a command ID,
receiver, actual decision ID and SHA-256 pin, optional original explanation, and
one to 16 claims ordered by unique field name:

```json
{
  "id": "review-example",
  "receiver_id": "receiver-REPLACE_WITH_ACTUAL_OWNER",
  "decision_id": "actual-finalized-decision",
  "decision_sha256": "REPLACE_WITH_ACTUAL_DECISION_SHA256",
  "explanation": "Original explanation, retained without rewriting.",
  "claims": [
    {"field": "action", "value": "entry_candidate"},
    {"field": "reference_price_cents", "value": "10000"}
  ]
}
```

The service replays the actual decision's strategy and retained feed basis before
checking claims. A supplied hash or imported envelope cannot establish ownership.
It rejects unknown or duplicate JSON keys, trailing objects, query strings,
invalid identities, duplicate/unsorted fields and requests over 16 KiB. Explanation
text is valid UTF-8 without NUL, at most 8 KiB; claim values are strings of at most
256 bytes. Field names are lowercase identifiers of at most 64 characters.

The versioned method `retained-decision-fact-claims-v1` supports these exact facts:

| Field | Canonical value |
| --- | --- |
| `action` | Retained evaluation action, such as `entry_candidate` or `abstain` |
| `reason_code` | Retained evaluation reason |
| `reference_price_cents` | Base-10 integer, with no leading zeroes |
| `stored_signal` | `true` or `false` |
| `quote_input_id` | Retained coverage quote input ID, which may be empty |
| `basis_sha256` | Actual decision basis hash |
| `strategy_id` | Actual registered strategy ID |
| `strategy_sha256` | Actual strategy record hash |
| `finalized_at` | UTC RFC3339Nano timestamp |

Equal canonical strings produce `supported`; a different value produces
`contradiction` and includes the retained expected value. Other field names
produce `unsupported_field`, with no invented expected value. In particular,
reference prices and signals cannot establish realized profit, fills or future
success. Such claims require review rather than being accepted as facts.

The optional explanation is retained verbatim as untrusted text. The method does
not extract claims from it, understand arbitrary prose or verify that the explicit
claims faithfully represent it. A caller or future conversation adapter must
supply the claims to check. Even when all supplied claims match, status is only
`claims_consistent` and `prose_verified` remains false. No model is called.

## Cases, retries and inspection

Contradictory or unsupported claims return `review_required`, with a canonical
case ID derived from the actual finalized decision hash. Repeated assessments of
the same decision join one open case; matching assessments neither create cases
nor dismiss existing ones. Cases list the contradictory assessment IDs, original
decision/basis pins and earliest review time. There is no close, dismissal,
decision rewrite or execution route in this contract.

Inspect through:

- `GET /v1/paper-decision-reviews` for the complete replayed assessment/case catalogue.
- `GET /v1/paper-decision-reviews/{id}` for an assessment and original explanation.
- `GET /v1/decision-review-cases` or `/{id}` for grouped open cases.
- The returned `record_path` for the original finalized decision and its existing
  deterministic explanation/source inspection paths.

New assessments return 201. Exact command retries return 200 and their original
record without reading a new clock; changed content under an existing ID returns
409. New reviews cannot precede decision finalization or the latest retained
review. Missing selected decisions/reviews/cases return 404, malformed requests
422, owner/pin/capacity conflicts 409 and storage failures 503. Reads and retries
validate the entire review prefix, so unrelated corruption cannot be hidden by a
selected read. Self-rehashing findings, case IDs or authority flags fails replay.

## Persistence and rollback

Memory serializes the aggregate under its mutex. PostgreSQL schema 45 adds
`mantik_decision_reviews`, a bounded record table. Writers acquire collector,
state and receiver locks in the same order as finalization, verify actual owner
records, and atomically retain the assessment. Cases are deterministic projections
of that retained history, so an insert failure cannot leave a partial case.
Reads use one repeatable-read snapshot. At most 64 assessments of 32 KiB each are
retained; capacity fails visibly without eviction, and exact retries remain
available when full.

Prior schema-44 writers refuse schema 45. Rollback stops the new writer and
restores the retained pre-upgrade backup with its matching binary into separate
resources. Do not remove schema rows or tables in a running service.

Verification uses actual authenticated finalization/review handlers, concurrent
Memory/PostgreSQL contracts, unsupported and contradictory claims, opaque prose,
source pins, exact retries, changed IDs, clock rollback, capacity, corrupted
history, SQL rollback, restart and migration recovery. Reviews never mutate the
original decision, fixture operations or allocation/execution records. Model
availability is not consulted; this contract supplies no natural soak or
qualification evidence.
