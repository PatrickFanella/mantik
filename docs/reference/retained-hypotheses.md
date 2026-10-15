# Retained hypothesis publications

The service retains a full synthetic declaration only when a completed recorded
review proposed a hypothesis against the case's current evidence. Publication
adds research history; it does not close the news case, execute a method, register
a trial or grant qualification. This is the publication/reuse boundary of issue
#16; trial lineage, outcome evaluation and promotion remain separate work.

## Operator flow

Prepare a full declaration using [hypothesis-declare](hypothesis-declarations.md).
Inspect a completed [service review](retained-reviews.md) before publication:

```sh
./bin/mantik news-review --api-url http://127.0.0.1:40880 --id REVIEW_ID
./bin/mantik hypothesis-declare --file /absolute/private/declaration.json
./bin/mantik hypothesis-publish --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id PUBLICATION_REQUEST_ID --review REVIEW_ID \
  --file /absolute/private/declaration.json
./bin/mantik hypothesis --api-url http://127.0.0.1:40880 --id HYPOTHESIS_ID
```

Use the service's receiver identity and an explicit stable request ID. Existing
`MANTIK_API_TOKEN` authentication applies. Publication IDs and review IDs are
1..128 ASCII letters, digits, underscores or hyphens. Readback targets one exact
`hypothesis-` plus 64-hex question ID; it includes every retained publication of
that question, sorted by publication time and request ID. There is no network
source fetch, external model call or new background worker in this path.

The declaration's mechanism code must equal the checked candidate's `claim_code`;
its refutation code must equal `refutation_code`. The operator supplies the full
mechanism statement, prediction, null, refutation, horizon and exact native method
references. Matching codes link the proposal to this declaration; they do not
prove that the source supports the mechanism or that the prediction will hold.

Replace offline fixture origins with the exact service references from the
review's `attempt.pin.group.claims`: `reference`, `claim.key` and
`claim.recording_sha256`. Each origin must correspond exactly to a candidate
`evidence_keys` entry and a successfully inspected claim. V1 requires current
`reported_unverified`, `synthetic_unqualified` claims explicitly naming `DEMO`.
It rejects stale, retracted, disputed, uninspected, substituted or extra origins,
and does not convert real market evidence into synthetic evidence.

## Publication checks and retention

The service loads the authoritative attempt and news state. It checks the review
schema/type, finalized result identity, required passed checks, proposal outcome,
research-only authority, receiver, completion before its original deadline, and
current open case version/generation. It rebuilds and verifies the pinned source
snapshot and exact claim/tool bindings before inserting a publication. A
publication clock cannot precede the review completion, retained news clock or
any earlier publication. Expired, pending, superseded and abstaining attempts
cannot publish. There is no additional age limit for a completed review whose
evidence remains current.

PostgreSQL migration 13 adds `mantik_hypothesis_publications`, separate from
execution state. Publication, currentness checks, total capacity and question
reuse are serialized with news/review writes under the same news-row lock.
Memory storage implements the same contract under its mutex. Failed persistence
does not acknowledge a publication or change the case. Migration checksums are
verified at startup; older binaries that do not support schema 13 must not open
this database. Back up before upgrading an existing service; a database restore
and compatible binary/image are needed for rollback.

The application retains up to 32 publications per database, across all questions.
It never overwrites a publication or evicts history. At capacity, new requests
fail with a conflict; exact retries still recover their original receipt. This
finite bound is deliberate for the current pilot. Publication is atomic with
case changes: a new publication either precedes the case change or rejects the
now-stale review. A successful publication remains historical after that change;
it must not be interpreted as present-day review applicability. Inspect its
linked review for the current projection.

An exact request retry returns the immutable original receipt, including its
publication time and `question_reused` flag, even after the case changes. Reusing
a request ID with another review or different canonical declaration conflicts.
Using a new request ID retains another publication. The exact question identity
excludes label and origins but includes mechanism, conditions, instrument,
evidence class, horizon and method pins. Matching questions therefore reuse the
same ID while preserving each declaration hash, label, review/result identity,
pinned subject, origin set and publication time. Different questions stay
separate. This is exact matching, not semantic deduplication or independence
assessment. No trial, failure, contradiction or abstention is deleted or converted
to qualification by publication; original review attempts remain inspectable.

`origin_binding_verified: true` means source identities and inspected excerpts
were checked against application-retained evidence at publication time.
`sealed.origins_verified` remains false because source truth and causal support
are unverified. Authority remains `research_declaration_only`, qualification
credit false. A server timestamp records admission; it does not establish that
the declaration preceded existing experiments. Future trials need an explicit
publication link and their own method/data/resource/outcome contracts before
claiming pre-test registration.

## HTTP contract

Authenticated `POST /v1/hypothesis-publications` accepts one object:

```json
{
  "receiver_id": "RECEIVER_ID",
  "request_id": "PUBLICATION_REQUEST_ID",
  "review_id": "REVIEW_ID",
  "declaration": {}
}
```

`declaration` must contain the full declaration schema, not the empty example
above. The envelope is bounded to 68 KiB and the canonical declaration to 64 KiB.
Unknown, duplicate or noncanonical fields, trailing JSON, excessive nesting,
invalid UTF-8 and caller-supplied pins/results/authority are rejected. Success
returns a `mantik-hypothesis-publication-v1` receipt with the sealed declaration,
resolved native method configurations and authoritative review linkage.

Authenticated `GET /v1/hypotheses/{id}` returns
`mantik-retained-hypothesis-v1`, with research-only authority and the bounded
publication history for that question. It does not select full articles, raw
model artifacts, unrelated questions or execution history. Neither route accepts
query parameters. Invalid input returns 422, missing question/review 404,
receiver/evidence/identity/clock/capacity conflict 409, and storage failure 503.
