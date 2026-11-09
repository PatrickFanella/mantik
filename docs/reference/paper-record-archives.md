# Paper record custody and owner epochs

Schema 105 seals original decisions, outcomes, evaluation assessments and
decision reviews with the native replay context needed to inspect them. It
reserves their original IDs and records their creation order independently of
event clocks. This prepares the remaining paper-retirement work in the
[continuous runtime plan](../plans/continuous-paper-runtime.md).

Active originals remain retained in this version. Creating a paper archive does
not free the separate decision, outcome, assessment or review limits, schedule
retention, prune other source owners or change paper capital. Those are remaining
delivery units; this custody endpoint alone does not permit indefinite runs.

## Owner operations

All routes require the normal service bearer authentication. Responses contain
private original evidence and must be retained privately.

| Route | Result |
| --- | --- |
| `POST /v1/paper-record-archives` | Seal the actual current paper owners, replay context, membership and identity metadata atomically. |
| `GET /v1/paper-record-archives?after=0&limit=16` | Page small headers without reading raw archive payloads. `after` is the last sequence read; `limit` is 1–32 and defaults to 16. A nonzero `next_after` selects the next page. |
| `GET /v1/paper-record-archives/{sequence}` | Inspect one bounded archive and re-execute its original paper evidence. |
| `GET /v1/paper-record-archives/source?kind=decision&id=original` | Resolve an original ID to its first archive and retained payload. Kinds are `decision`, `outcome`, `assessment` and `review`. |
| `GET /v1/paper-record-archives/audit` | Re-execute the entire archive chain and verify complete original membership and identity lookup. |

The POST body is a strict object containing only the actual `receiver_id` from
the service, limited to 4 KiB. It accepts no records, source content, hashes,
retention choices, query parameters or caller clock. The service reads all
owners itself under collector → routing state → receiver ownership locks and
uses its compiled decision/outcome evaluators. Memory uses the same owner mutex
as those writers. No model is called.

A new cut returns 201. The same paper record set returns its original header
with `duplicate: true` and 200, without sampling another clock. Later feed or
unrelated source activity alone does not duplicate an unchanged paper archive.
Before returning that retry, the owner still verifies the actual current paper
records and the original archive. A missing or changed original is a conflict.

Invalid bodies, IDs and cursors return 422; absent positive sequences and
unarchived IDs return 404. Source, membership and archive-capacity conflicts
return 409. Storage failures return no receipt. Failed validation, cancellation,
statements or COMMIT leave the archive, membership and active owners unchanged.

## Original membership and replay

Each archive binds receiver, sequence, previous hash, original archive time,
record counts, a record root and a complete context/payload root. Context retains
strategy and policy catalogues, declarations, corporate/reference source
records, the relevant social journal and the current feed snapshot. The retained
decision/outcome payloads include their original bases. Provider `original`
strings, original record envelopes and record hashes remain unchanged.

Replay reconstructs the native catalogues and assessments from this context,
rederives source-backed review findings and independently verifies paper windows
against original feed evidence. Archive source material remains subject to its
original evidence class and access assertions. Hash integrity and successful
native replay do not attest provider authenticity or profitability.

One archive is limited to 64 MiB, and each record keeps its existing byte bound.
Original-ID lookup uses an indexed first-archive reference and reads one bounded
segment. Header pages do not load raw history. Complete audit work grows with
history; a deadline, unavailable original or incomplete replay never returns a
partial success. Established operational reads do not use these raw payloads.

## Creation epochs

Each actual record insertion reserves `(kind, original ID)` with its original
record hash, exact UTC seconds/nanoseconds and the last committed paper archive
sequence. Predecessor records are backfilled at epoch zero without rewriting
their payloads. Inserts and their identity reservations commit together. Database
triggers also record the correct epoch for already-running older paper writers.
Deleting an active row cannot make its original identity reusable.

Archive sequence `N` closes the actual owner set created before epoch `N`.
Immutable creation metadata independently determines that complete set. Rewriting
an archive body, header, batch summary and membership rows to omit an owner is
rejected against the original creation records. Later records use a later epoch
and leave the earlier snapshot unchanged, even across clock representation or
historical event-time differences.

After a paper archive exists, new feed/paper custody checkpoints use
`mantik-paper-feed-checkpoint-v2` and bind the actual `paper_archive_sequence`.
The cut covers at least that paper archive's original time, even when the feed
has not changed; an unchanged-feed retry does not sample a replacement clock.
Earlier v1 checkpoints retain epoch zero and their original identities. A
database guard prevents an older running custody writer from assigning a later
cut to epoch zero. These epoch references are checked through small sealed
headers; their validation does not add a raw paper-archive payload dependency to
established paper/protection reads.

## Recovery and remaining work

Backups must include `mantik_paper_record_archives`,
`mantik_paper_record_epochs`, `mantik_paper_archive_members` and
`mantik_paper_archive_batches`, together with all original source owners, feed
archives/indexes, custody checkpoints, retention metadata and receiver/schema
history. Archive payloads, batch roots, membership and original identity metadata
are immutable. Schema-104 startup binaries refuse schema 105; rollback requires
matching binaries and an isolated restored backup. Removing migration records
or custody rows in place is unsupported.

The disposable recovery contract now includes a populated paper archive over
decisions, waiting outcomes and an actual assessment. Restore replays its original
evidence, verifies first-ID lookup and exact retries, and checks archive audit
without starting workers or making provider requests.

Physical paper retirement still requires historical owner views for assessments,
pending outcomes, allocation references and reviews. Admission-review and other
workflow/source retention, automatic scheduling and growing-history operational
cost checks also remain part of the complete archive work.
