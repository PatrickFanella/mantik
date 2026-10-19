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
news cases have their own catalogue, separate from fixture/routing `cases`.
The interactive `supervise --view news` view below reads that catalogue.

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


## Interactive news supervision

```sh
./bin/mantik supervise --view news --api-url http://127.0.0.1:40880 --limit 10
```

This read-only Unix terminal view uses the same bearer token and existing news
GET endpoints. It loads one bounded catalogue (at most 128 cases and 128 distinct
record references) and pages it locally; `n`/`p` do not fetch source bodies.
The page shows receiver identity, status, evidence class, declared event key,
evidence version, generation, source-record count and unassessed independence.
An empty catalogue displays zero items.

Commands require Enter:

- `n` / `p`: move between case pages, clearing the selected case.
- `r`: reread the catalogue and reset to its first page with no selection.
- `d CASE_ID`: select a case on the displayed page and show its retained snapshot,
  including ordered record references. This does not claim a new service read.
- `h`: read all retained review-attempt summaries for the selected case, including
  pending, completed, expired and superseded attempts, outcomes, structured checks,
  evidence currency and review applicability. This uses the existing targeted
  review-history GET endpoint, with a 1 MiB response limit; it fetches no raw
  model artifacts or source bodies and does not start or complete a review.
- `a ATTEMPT_ID`: after `h`, explicitly read a referenced attempt from that
  history through the existing review GET endpoint (4 MiB response limit).
  This displays its pinned evidence, structured result and recorded model
  artifacts. These can contain sensitive source/model text; keep terminal output
  private. Reading `h` alone never fetches these artifacts.
- `e RECORD_KEY`: fetch normalized claims and excerpts for a record referenced by
  the selected case. It does not request the full original article.
- `o RECORD_KEY`: explicitly request and display the full retained original for
  that referenced record. Original text can be sensitive; keep terminal output
  and recordings private.
- `q`, EOF or interruption: leave the view without changing the service.

Page navigation, case selection, refresh and stale reads clear the displayed
review-history references. A case must be selected again after page navigation
or refresh. Unknown/off-page
case IDs, unrelated record keys, path-like input and mutation commands produce
no history or source request. No submit, close, review write, model, URL fetch or admission command
is available in this view. Private diagnostic capture files are not imported.

Catalogue shape/capacity, receiver, case identities, versions, reference order and
independence markers are checked before navigation. Targeted reads verify record
identity, recording/result receipt pins, displayed-case linkage and source
identity. An explicitly requested original also has its digest and claim spans
checked. These checks are not independent source truth or model-execution
attestation; excerpt-only reads cannot fully replay original normalization.

History readbacks must pass the existing history validator and match the
displayed receiver and complete selected-case snapshot. A newer evidence version,
closure or reopened generation requires a catalogue refresh and case selection
before history can be displayed. Applicability describes that history readback;
it is not continuously monitored and grants no execution or qualification
authority. A valid empty history is displayed explicitly with zero attempts.

Attempt reads rebuild the existing history-summary contract from the returned
retained attempt and require an exact match to its displayed summary, including
its complete-attempt digest and applicability flags. An attempt completing after
`h`, changed applicability, invalid artifacts or a failed read requires `r`,
case selection and a new `h`. No raw model output is treated as an instruction.
A historical attempt can remain inapplicable while the case changes again;
attempt reads do not independently reread or attest the current case catalogue.

A request failure or invalid readback marks the catalogue stale. Displayed rows
remain labeled stale, and navigation/history/source reads stop until `r` succeeds. A
fresh source read is separate from the displayed catalogue: a correction may
have changed the case since the last refresh. Refresh to see its new evidence
version or reopened state. No old version is silently treated as current.

The client retains no growing history, polls no background loop, and closes its
owned stdin handle on cancellation. Reconnecting starts with a new catalogue
read. The service continues when the terminal exits. Existing machine-readable
`news-cases` and `news-record` commands and API/storage contracts are unchanged.
