# Feed archive custody

Schema 100 adds an authenticated source-archive owner. It seals the current
receiver's raw publications, collector registrations/leases/commands/admissions,
and routed input/projection state in one transaction. Existing active sources
remain in place. Archive-aware replay and active pruning are subsequent units
in the [continuous runtime plan](../plans/continuous-paper-runtime.md); this
contract alone does not remove the current multi-day limits.

## Create a source cut

`POST /v1/feed-archives` accepts a strict object containing only the actual
`receiver_id` returned by `GET /v1/feed-collectors`. The body limit is 4 KiB.
Unknown, duplicate or differently cased fields, caller source content, clock
arguments, hashes, pruning options and query parameters are rejected.

The owner locks collector and routing state in the same order as publication
and decision writers. It reads the source records itself, validates normalized
quote/calendar publication against retained input/admission receipts, and
samples its clock under those locks. Expired or stopped leases can be archived;
archiving supplies no current collection authority or readiness credit.

The owner canonicalizes recording JSON for portable integrity while preserving
the exact provider `original` strings and their hashes. It captures the complete
routing state, including inputs not owned by the collector. Those other
projections are retained bytes; this operation does not re-execute unrelated
workflow owners or attest the semantics of every routing transition.

Each archive has a source digest, monotonically increasing sequence, original
archive time and previous archive hash. The archive hash binds that header,
including the source digest. The source digest binds every captured source field.
New cuts must extend all earlier immutable publications, admissions, command
receipts, registration definitions and routed input records in the preceding
snapshot. A missing or changed original prevents the new archive. Mutable leases
and routing projections can advance without rewriting an earlier cut.

New collector commands, input admissions and publications must have a service
clock strictly after the latest archive time. Equal or earlier clocks fail
before retaining a new source event. Exact historical source retries remain
readbacks and do not move that fence. Archive creation and source writers hold
the same collector lock, so neither can backfill an already sealed cut during
a concurrent transaction or after a clock step backwards.

The same current source cut returns its original header with `duplicate: true`
and HTTP 200, without sampling another clock. A new cut returns 201. A historical
cut cannot replace a later archive head. An uncertain response is resolved by
reading the archive owner and retrying against unchanged current sources; callers
cannot choose a replacement payload, sequence or receipt time. If new source
writes occur before the retry, the owner can legitimately seal a new later cut;
this endpoint has content identity rather than caller request-ID identity.

One archive is limited to 64 MiB. Oversized cuts fail visibly without storing a
partial segment or deleting sources. There is no automatic schedule, retention
move, deletion route or capacity increase in this version.

## Inspect archives

`GET /v1/feed-archives` returns headers only, with receiver identity and ordered
records. It accepts `after` (the last sequence read, initially zero) and `limit`
(1–32, default 16). A nonzero `next_after` selects the next page. Header pages
do not load every archived raw payload. They verify the listed header envelopes
and contiguous sequence/chain within the page; a page is not a full-history audit
of every source payload.

`GET /v1/feed-archives/{sequence}` returns one complete bounded archive. The
owner checks stored header/payload digests, sequence and receiver bindings, and
revalidates quote/calendar normalization and admission/input receipt links at
the original archive cut. Later lease changes or current clock do not rewrite
that historical view. The route accepts no caller clock, query overrides or body.
The response contains private source material and requires the normal service
bearer authentication; retain it privately.

The header schema is `mantik-feed-archive-v1`. It includes receiver, sequence,
archive time, previous/source/archive digests and registration/publication/
admission/command counts. Authority is `service_owned_feed_archive_only`, with
false qualification credit. A digest validates bound bytes; it does not attest
provider authenticity, profitability or an operational grant.

Invalid commands, cursors and selected sequences return 422; unknown positive
sequences return 404; source/chain or segment-capacity conflicts return 409.
Unavailable storage returns 503. Failed statements or COMMIT return no receipt
or read result. A failed archive leaves active sources and existing archives
unchanged.

## Persistence and recovery

`mantik_feed_archives` stores a small independently readable header and a byte
payload per sequence, with unique source/archive hashes. Source writers and
archive creation serialize through the collector/routing locks. PostgreSQL
reads use one repeatable-read transaction, and Memory uses its owner mutex.
Returned source maps are detached copies. No existing envelope or hash is
rewritten by the migration.

Backups must include the archive table together with active source owners,
receiver identity and migration history. Schema-99 binaries reject schema 100;
rollback uses an independently restored compatible backup and matching binary.
Dropping archives or migration records in place is unsupported. Reconnect and
failed-write tests establish storage contracts, not a deployed restore or
natural-run qualification.
