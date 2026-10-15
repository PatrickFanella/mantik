# Receiver-bound news research cases

The service admits one operator-recorded news revision at a time through a
receiver-bound endpoint. It retains the original article, normalized claims,
admission receipt and affected research-case links in one transaction. This is
a research path: it does not register trading triggers, finalize decisions,
request operations, or create economic effects.

## Submission and inspection

Retain and inspect the article in a [private journal](news-journal.md) first.
Read `news-cases` and inspect its `receiver_id`, then pass that identity explicitly
to every write. Do not substitute another receiver after an uncertain response.

```sh
./bin/mantik news-cases --api-url http://127.0.0.1:40880
./bin/mantik news-submit --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --journal /absolute/private/news-journal --record RECORD_KEY
./bin/mantik news-record --api-url http://127.0.0.1:40880 --record RECORD_KEY
./bin/mantik news-record --api-url http://127.0.0.1:40880 --record RECORD_KEY --original
./bin/mantik news-close --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id NEWS_CASE_ID --version CURRENT_EVIDENCE_VERSION
```

These commands use the existing `MANTIK_API_TOKEN` environment variable. Keep the
service on loopback or behind its authorized transport. `news-submit` audits the
existing journal, reads one record, releases its local lock and submits that
recording. The service independently normalizes it and checks the predecessor;
it does not trust a client-supplied derived result or receipt. Submit ancestors
before their successors. The service neither resolves nor fetches supplied raw
references, URLs or access references.

An acknowledgment binds the canonical recording hash, record key, acceptance
time and resulting case transitions. The record key has the same
source/article/evidence-class/revision identity as the local journal. Copying
that recording into a different local journal does not create a new server
identity. A changed recording under an existing identity returns a conflict.
An exact retry returns the original acceptance time and transition receipt with
`duplicate: true`, without reopening cases or adding evidence again.

The local client does not persist an acknowledgment ledger. If delivery or the
response is interrupted, retry that same retained recording against the same
receiver. Read back `news-record` and its receipt to resolve uncertainty. Never
assume a failed client response proves the service did not commit.

`news-cases` lists at most 128 dedicated news research cases. Each includes its
current evidence version, generation, state and ordered accepted record keys.
It does not return original articles. `news-record` selects one retained source
and returns its normalized claims and original admission receipt. Full original
text requires `--original`; excerpts remain present in normalized claims. These
news cases have their own catalogue and are not yet displayed in the existing
fixture/routing `cases` or interactive supervisor views.

## Policy and time

The stored schema is `mantik-news-cases-v1`; policy is
`news-research-reopen-on-evidence-v1`. Case identity hashes evidence class and
declared event key within the receiver. It is independent of the local
journal-scoped IDs used by [news-events](news-events.md).

- A new accepted article links every declared event to a research case. It opens
  a missing case, updates an open case, or reopens a closed case for review.
- A correction or retraction must match a retained predecessor checksum in the
  same source/article/evidence class, immediately preceding revision, with
  nondecreasing availability and receipt times. A retraction is terminal in v1.
- Revisions link the union of their own and their predecessor's event keys.
  Moved or removed claims therefore update/reopen their former cases too.
  Multiple claims for one event produce one transition per accepted revision.
- Repeated coverage and syndicated copies preserve separate source records in
  the same event case. Independence stays `not_assessed`; more distribution
  does not establish corroboration. Assessments and original text remain intact.
- Every new admitted record increments each affected case's evidence version.
  Reopening also increments its generation. Closing requires that exact current
  evidence version. A stale close returns HTTP 409; it cannot clear newer news.
  An exact close retry for the same still-current version leaves the case closed.

A case is open because it needs research review, not because a news claim is
true, fresh, qualified or tradable. Closing it records an explicit operator
review boundary; it does not issue a decision or grant execution authority.

Client publication/availability/receipt times remain evidence declarations.
Admission rejects a receipt in the service's future. Acceptance and case times
come from the service clock sampled after the mutation lock is acquired; a
backward service clock blocks new mutations. Old publication or receipt times
are allowed as late evidence and cannot rewind case timestamps. Exact record
retries retain their original receipts even if the current clock is uncertain.

## API and persistence

All routes use the existing bearer authentication boundary:

| Method and path | Contract |
| --- | --- |
| `GET /v1/news-cases` | Receiver identity and bounded current case catalogue; no query parameters |
| `POST /v1/news-recordings?receiver_id=ID` | One strict recording JSON body, at most 2 MiB; 201 on new admission, 200 on exact retry |
| `GET /v1/news-recordings/KEY` | One normalized retained record and its original receipt |
| `GET /v1/news-recordings/KEY?original=true` | Explicitly include original text |
| `POST /v1/news-cases/ID/close` | JSON `receiver_id` and current `version`; service supplies the clock |

Invalid recordings return 422, missing records/cases 404, identity/lineage/time/
version/capacity conflicts 409, and unavailable persistence 503. Mutations retain
at most 128 source revisions and 128 event cases per receiver. A capacity failure
rejects the whole admission before changing any case or retaining the new source;
exact retries still work at capacity. Review an archive/continuation plan before
reaching these limits; no deletion, eviction or rotation is automatic.

Migration 11 creates a separate `mantik_news_cases` aggregate. PostgreSQL row
locking serializes source admission, case changes and closures, and their single
transaction commits the original/result/receipt/links together. Catalogue reads
select only the case projection; targeted record reads select only that record.
The database retains article text, so existing database access and backup custody
also apply to these originals. This is distinct from local journal file modes.

Existing fixture/routing/operations state, research receiver identity and feed
records are not rewritten. Startup binaries capped at schema 10 reject schema
11; deployment requires a verified backup and a planned rollback/restore rather
than running an older binary on the upgraded database. No existing pilot is
upgraded by this development change. Isolated upgrade/restart and transaction
rollback checks are distinct from deployment or off-host recovery qualification.
