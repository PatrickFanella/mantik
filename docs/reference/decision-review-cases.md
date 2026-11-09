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

The current decision catalogue is checked against independently reserved active
owner intervals. A review may reference an independently approved retired
decision: the service resolves that decision's first archive, verifies its actual
creation/scope/payload custody and replays its native strategy and feed evidence
at the original time. Existing findings, source bindings, case IDs and exact
clock-free review retries retain their original values. New typed claim reviews
can select that same original decision by its unchanged ID and hash. Missing
unretired decisions cannot fall back to an archive, and a later archived copy
cannot replace an unavailable first original.

Historical decision dependencies are private and bounded by the 64 retained
reviews plus one requested target. They do not enlarge the public active
decision catalogue. Review records themselves still occupy their bounded active
registry, and actual social/copy journal validation remains required. This does
not enable physical paper retirement or retire review records; allocation,
attribution and complete archival consumers remain separate delivery work.

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

## Select a retained social or copy source

The same authenticated POST accepts an optional source selector:

```json
"source": {
  "capture_id": "retained-recording-id",
  "capture_sha256": "<full retained recording hash>"
}
```

With this selector, omit `explanation`. The operator still supplies explicit typed
`claims`; selecting a source does not assert that its original text contains those
claims. The original bytes remain in the recording owner and the returned binding
links to their authenticated record path. Caller-provided originals, policy facts,
provider assertions and extraction results are rejected.

The storage owner selects the actual recording, current watch policy and historical
source lineage in the same transaction as the finalized-decision review. The watch
must be enabled with behavior `decision_review`, and its hash must match the policy
captured by the source. Deleted, superseded, stale, unresolved or conflicting
sources and changed policies cannot start a new review. Eligible social posts,
edits and reposts can be reviewed. Copy activity remains mapping- and
admission-unavailable; that status permits only this explicitly authorized fact
comparison, with no follow or execution permission. Other unavailable structured
extraction remains unavailable.

New source-bound records use `mantik-finalized-decision-review-v2` and retain the
source kind, recording/original/policy/claim-group hashes, journal cutoff and
applicability hash. Their `claims_extraction_verified` and
`provider_access_verified` flags remain false. Review and qualification authority
flags remain false. The generic recording applicability and dispatch flags are not
promoted by this operation. Manual v1 reviews retain their original bytes and
semantics.

One source claim group can produce at most one source-bound review for a finalized
decision. A new request ID for that same group and decision conflicts; retry the
original request ID to recover its receipt. Exact retries return the original
record without a new clock or reevaluating a later policy. Historical readback
reconstructs the original owner source cutoff; later source updates do not rewrite
it. New social/copy writes must be strictly later than every retained source-bound
review cutoff. Exact historical source retries remain unchanged.

PostgreSQL uses the existing collector/state/receiver lock order; source updates
serialize on the same receiver lock. No receipt or catalogue is returned on failed
COMMIT. Failed transactions preserve review identity for retry after a confirmed
rollback. Inspect owner history after an uncertain transport failure before
assigning a replacement identity.

Schema 59 fences older writers before v2 issuance or source-clock enforcement. It
updates table comments and the schema marker without rewriting retained review or
social journal payloads. Rollback requires a compatible pre-upgrade backup and
binary on separate resources. The typed `review_decision_claims` supervision/MCP
contract accepts the same selector and introduces no model or provider request.
This is a bounded source-to-decision review path, not general research dispatch,
provider authentication, complete copy-follow behavior or issue completion.
