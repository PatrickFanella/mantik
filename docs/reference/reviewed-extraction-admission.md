# Reviewed extraction admission

An operator can explicitly approve the complete claim candidates of one retained
recorded extraction for news research admission. The service loads the attempt
and its checked result, constructs the recording itself, and commits the source,
case transitions and provenance receipt together. No caller-supplied recording,
claim edit, source metadata, event key or finalized check is accepted here.

```sh
./bin/mantik news-extraction --api-url "$MANTIK_API_URL" --id extraction-1
./bin/mantik news-extraction-admit --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id extraction-1 \
  --attempt-sha EXACT_REVIEWED_ATTEMPT_SHA256 \
  --result-sha EXACT_REVIEWED_RESULT_SHA256 --approve-candidates
./bin/mantik news-extraction-admission --api-url "$MANTIK_API_URL" \
  --id extraction-1
```

Inspect the original, raw extractor output, claims and checks before approving.
Use the existing `MANTIK_API_TOKEN`. The required flag expresses approval of
this exact full result; it does not attest that a human reviewed it, establish
source truth or evaluate extractor accuracy. The service records the explicit
approval command, not the identity of a particular reviewer. Model investigation
tools do not expose this admission operation.

## Eligibility and immutable provenance

The attempt must belong to the pinned receiver, have status `completed`, contain
outcome `extracted`, and validate against its original article, contract and raw
artifact. Pending, expired and abstaining attempts cannot be admitted. Supplied
attempt/result hashes must match the retained objects exactly. Admission does
not rewrite the attempt or its completion hash. Completion deadline eligibility
is distinct from source freshness or trading-decision validity.

The constructed `mantik-news-recording-v1` object has extraction method
`reviewed_recorded_extraction_v1` and optional `extraction` provenance with schema
`mantik-reviewed-extraction-provenance-v1`. It binds receiver, attempt ID/hash,
result hash, article hash, raw artifact hash and compiled contract hash. The
normalized result carries the same provenance. Recording/result hashes include
these links; old operator recordings omit the field and retain their original
bytes and identities.

Normalization checks provenance shape and its article-metadata binding. It does
not look up a service ledger or prove approval. The ordinary operator journal
and news-submission path reject provenance-bearing recordings. Only this
service-owned admission path verifies the exact retained attempt and result
before constructing a reviewed recording. Changing an ordinary file's method
or adding claimed hashes cannot bypass that path.

`mantik-reviewed-extraction-admission-v1` contains the approval command, source/
artifact/contract hashes, original news receipt, research-only authority and its
own hash. Authority is `reviewed_recorded_claims_only`; qualification credit is
false. The attempt ID is also the stable identity of its single approval.
Different source/claim content requires a new extraction and review, not a patch
to the old result.

## Atomic routing, revisions and recovery

Admission uses the existing news policies for causal clocks, original retention,
source/revision identity, predecessor existence and checksum, terminal
retractions, evidence classes, capacity and old/new event union. Corrections can
reopen a closed predecessor event while opening the newly declared event.
Syndicated distribution retains its own source attribution and remains
`independence: not_assessed`. Event keys are operator declarations.

The approval and news record/case changes commit in one transaction. A failed
receipt insertion rolls back source retention and case changes. A source revision
already admitted under another attempt or through the operator path conflicts;
an untracked matching recording is not adopted as an approval. Exact retries
recover the original validated receipt without resampling time, reopening closed
cases or adding another source. Changed hashes or approval command conflict.

Memory holds one mutex for the operation. PostgreSQL locks receiver first, then
news state; existing news/review mutations do not take an exclusive receiver
lock in the reverse order. The source attempt remains immutable. A foreign key
and unique record-key index prevent orphaned approval identities and duplicate
approval ownership. At most 32 approvals can exist within the retained extraction
capacity. There is no eviction or selected-result promotion.

Targeted receipt reads validate the complete stored recording/result against the
retained attempt in one snapshot. Corruption or missing source evidence fails
the read. The CLI first checks metadata, then reads the retained attempt and news
record and verifies the receipt against those exact pins before printing it.
Loss of the response after commit can leave an admitted record; inspect the
stable attempt ID or repeat the exact approval command before assuming failure.

## HTTP and schema

| Method and path | Input |
| --- | --- |
| `POST /v1/news-extractions/ID/admit` | `receiver_id`, `attempt_sha256`, `result_sha256`, `approval_code` |
| `GET /v1/news-extractions/ID/admission` | No query parameters |

The explicit approval code is `approve_claim_candidates_v1`. Commands use bearer
authentication and a 4 KiB strict JSON envelope. Unknown/duplicate/noncanonical
fields return 400; malformed commands or corrupt evidence 422; wrong receiver,
changed pins, ineligible state, duplicate source or capacity/clock conflicts 409;
missing attempts/approvals 404; persistence failures 503. Successful creation and
exact retry return 200 with the retained approval. Use the complete extraction
history to discover attempt IDs; a result without approval returns 404 here.

Migration 22 adds the separate approval table and indexes. It does not rewrite
existing news, extraction, hypothesis or operational payloads. Schema-21 binaries
refuse schema 22, which prevents them from overlooking reviewed provenance.
Deployment requires a verified backup and restore rollback preserving later
evidence; deleting tables or migration records is not a downgrade procedure.
Development rehearsals do not upgrade original pilot databases.

This is explicit admission of recorded, unqualified research evidence. It does
not activate a model/provider, extract articles autonomously, verify source
truth, infer event equivalence or independence, publish a hypothesis, grant
operational authority or establish profitability. Issue 12 remains open for
measured extractor behavior and its remaining acceptance work.
