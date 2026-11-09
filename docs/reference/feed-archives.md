# Feed archive custody

The authenticated source-archive owner seals the current
receiver's raw publications, collector registrations/leases/commands/admissions,
and routed input/projection state in one transaction. Schema 104 adds explicit
feed rotation: sources leave the active working set only after the owner seals
their original bytes, verifies retained paper dependencies and records exactly
which sources ongoing coverage and routing still own. Automatic retention and
physical paper archival remain unfinished in the
[continuous runtime plan](../plans/continuous-paper-runtime.md).

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
Before the first rotation, new cuts must extend all earlier immutable publications, admissions, command
receipts, registration definitions and routed input records in the preceding
snapshot. A missing or changed original prevents the new archive. Mutable leases
and routing projections can advance without rewriting an earlier cut.
After rotation, the next cut must extend the working set derived from the
previous archive's approved retirement. Sources can disappear only through that
owner operation; missing keepers or changed historical records remain conflicts.

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
partial segment or deleting sources. Creating a source cut alone keeps active
copies. Use the separate rotation operation below to retire them. There is no
automatic schedule or capacity increase.

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

The header schema is `mantik-feed-archive-v1` before retirement, then
`mantik-feed-archive-v2` for bounded working segments carrying the preceding
approved retirement and cumulative registration prefixes. Both include receiver, sequence,
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

## Original source identities

Schema 101 adds a receiver-owned index of original source identities. Archive
creation writes the snapshot, source index and complete segment manifest in one
transaction. A repeated source keeps its first archive reference and original
payload hash. Different bytes under that identity fail the complete transaction.
An index failure leaves neither a partial archive nor a partially indexed source.
Adding the index alone does not prune active source maps.

`GET /v1/feed-archives/source` accepts exactly one `kind` and one `id` query
parameter. Supported kinds are `publication`, `admission`, `input` and `command`.
A publication ID is its registration/capture key from the archived `Records`
map. The other kinds use the original input or command ID. No body, alternate
receiver, caller hash, source content or query override is accepted.

The response schema is `mantik-feed-archive-source-v1`. It contains the actual
archive header, selected kind/ID, original typed payload and its canonical
`record_sha256`. The owner loads only the indexed archive segment, verifies its
header and complete source digest, and checks the selected field against the
index and segment manifest. The payload retains the original source's occurrence,
availability, publication or command times; archive time does not replace them.
The normal service authentication protects these private bytes.

Portable envelope validation checks shape, header and payload digests. It cannot
prove field membership in an imported archive header without the corresponding
owner source snapshot. The actual source owner performs that selection. This
read grants no new evidence, model interpretation or operational authority.

## Source/index audit and upgrade

`GET /v1/feed-archives/source-audit` accepts no query or body. It reads every
archive in order, reconstructs the first source identity/hash/reference from the
actual stored source fields, verifies every complete segment manifest, and
compares that result with the entire source index. Missing/extra/changed index
rows, conflicting repeated identities, missing archive sequences and changed
source bytes fail without a partial success. A successful response reports the
receiver, archive/source counts, final archive hash and reconstructed index hash.

This is a full integrity audit whose work grows with history. It is separate
from bounded targeted reads and supplies no strategy evaluation or qualification.
The HTTP audit has a finite 30-second normal-build deadline; exhaustion returns
failure and does not mean the unexamined history passed. The audit does not
repair or rewrite an index.

Schema-100 snapshots retain their exact headers, payloads and hashes. During
schema-101 startup, the owner backfills unindexed snapshots one segment at a
time under the schema and collector locks, in the migration transaction. A
failed backfill rolls the upgrade back. Existing complete manifests are not
silently rebuilt during inspection. A deferred database guard rejects an older
running archive writer's COMMIT if it inserts a snapshot without its source
manifest. Schema-100 startup binaries also refuse schema 101.

Backups now include `mantik_feed_archive_sources` and
`mantik_feed_archive_source_batches` with the original archive table, active
owners and schema/receiver state. Restore all of them consistently; rollback
still uses an independently restored matching backup. Lookup/index delivery
does not yet authorize active-source removal or complete continuous retention.

## Archived registration prefixes

Schema 102 persists publication and collector-admission prefix points for each
registration. Counts and chained digests match the existing v3 decision/outcome
prefix contract: publication order is `(published_at, key)`, and admission order
is `(at, id)` with its actual routed input included in the digest. Repeated
sources keep their original point and first archive reference. Changed earlier
points fail the complete archive transaction.

An archive, its source index, prefix points and both complete segment manifests
commit together. A deferred database guard rejects a schema-101 writer that
supplies a source manifest but omits the prefix manifest. Existing archive
headers, source snapshots and v1-v3 paper envelopes keep their hashes. Startup
backfills schema-101 archives one segment at a time under the existing schema
and collector locks. It does not silently repair complete manifests on a read.

The internal `FeedArchivePrefixStore` read accepts a registration and source
cut. It selects the first archived snapshot covering the requested cut, or the
latest archive when the requested cut is later. Its returned `At` explicitly
limits coverage to the earlier of the request and that archive's cut; active
sources beyond the archive are not included. A known registration can have an
empty prefix before its first publication/admission. An absent archive or
registration is unavailable rather than a fabricated empty history.

PostgreSQL uses indexed archive-cut and prefix-end lookups. The read loads one
bounded archive segment, verifies its prefix manifest and rederives the selected
ends from that segment's original source fields. Missing or changed selected
index points fail instead of returning a shorter prefix. It does not load every
prior archive. The Memory implementation maintains an ordered prefix index with
binary cut selection. These are internal source-custody reads; no additional
HTTP endpoint or operational authority is introduced.

Source and archive cuts retain separate integer Unix seconds and nanoseconds in
the SQL indexes. PostgreSQL timestamp rounding must not admit a source one
nanosecond after a historical cut or drop an equal-clock ID tie. The complete
source/index audit also rederives every prefix point from actual source snapshots
and compares the entire point index and each manifest. The Memory audit checks
its ordered index too. Full audit work grows with history, separately from the
bounded cut read.

Backups additionally include `mantik_feed_archive_prefixes` and
`mantik_feed_archive_prefix_batches`. Restore these with all archive/source,
receiver and schema tables. Schema-101 startup binaries refuse schema 102;
rollback requires an independently restored compatible backup and binary.

Prefix custody is a prerequisite for archive-aware paper replay. Existing paper
readers still verify against active sources; pruning, archived paper records and
automatic continuous retention remain unfinished. This schema does not grant
permission to delete active sources independently.

## Paper source checkpoints

Schema 103 extends `POST /v1/feed-archives` with a paper source checkpoint in the
same owner transaction. The request still accepts only `receiver_id`. The
service reads every retained decision and outcome, verifies its original basis
against the archived feed and re-executes the compiled evaluators. It retains
original record, basis and outcome-observation digests with the registration
prefixes. A failed replay, INSERT or COMMIT leaves both owners unchanged.

The receipt adds `paper_custody`, containing its own header and `duplicate`
flag. Feed and paper identities advance independently: an unchanged feed returns
HTTP 200 and the original feed header, while newly finalized paper records can
produce a new paper checkpoint. An unchanged pair returns both original headers
without sampling a clock. Paper checkpoints are limited to 1 MiB and bind the
complete active paper membership at their cut.

The checkpoint's `cut_at` is the later of its feed archive time and the latest
retained decision/outcome time. This also closes the known-empty feed interval
when paper records advance on an unchanged feed. Subsequent new source events,
decisions and outcomes must be strictly later than this cut. Equal or earlier
clocks fail; exact historical retries remain readbacks. SQL guards enforce the
paper fence for older running writers using separate seconds and nanoseconds,
without PostgreSQL timestamp rounding. Migration acquires the collector lock
before installing those guards, in the same order as existing paper writers.

`GET /v1/feed-archives/paper-custody/{sequence}` returns a checkpoint by positive
sequence. It requires normal bearer authentication and accepts no query, body,
caller clock, source content or evaluator. The owner loads the named bounded
feed archive and actual paper records through the sealed cut, replays them, and
compares complete membership, identities and prefixes. Removing a pin and
rehashing the envelope cannot replace the owner's closed record set. Later
paper records leave the original readback unchanged. The header schema is
`mantik-paper-feed-checkpoint-v1`; its authority is
`service_owned_paper_source_custody_only`, with false admission, execution and
qualification flags.

Current operational reads replay retained paper bases and verify the owner
checkpoint and any still-active source overlap. They do not acquire an
archive-body dependency. The original schema-103 checkpoint format fixes
`paper_archive_sequence` at zero. Feed rotation uses these checkpoints.
Schema 105 adds [paper owner epochs](paper-record-archives.md): subsequent custody
checkpoints use v2 and bind their actual paper archive sequence. Earlier v1
checkpoints retain their original epoch-zero identities. Physical paper
retirement and automatic retention remain unfinished.

Backups now include `mantik_paper_feed_checkpoints` together with every feed
archive/index, active paper/source owner and receiver/schema history. Schema-102
startup binaries refuse schema 103. Restore matching state and binaries in
isolated resources; removing migration records or checkpoint rows in place is
unsupported. The checkpoint tests cover exact retries, concurrent creation,
clock fences, restart, an older writer during upgrade, populated DDL replay,
source corruption, self-rehashed omissions and rollback at both statement and
COMMIT boundaries. The disposable backup/restore contract also retains populated
archive indexes and a paper checkpoint with a waiting forward outcome, then
replays that checkpoint and its exact retry against the restored original owners.

## Rotate the feed working set

`POST /v1/feed-archives/rotate` accepts the same strict receiver-only body and
authentication as source-cut creation. The owner performs source archival,
original-ID indexing, prefix indexing, paper-source replay/checkpoint issuance,
retirement approval and active-source removal in one transaction. Any failed
validation, statement, cancellation or COMMIT leaves all those owners unchanged
and returns no receipt. No caller can select a cut, retention rule, source body
or hash.

Rotation keeps the current coverage window, including receipt-time ties, the
current lease's original command proof, unpublished admissions and their routed
inputs, and every current routing fact/case reference. Unreadable publication
metadata conservatively keeps the affected registration's admissions. Routing
inputs owned by other workflows remain active until those owners support their
own archival. Registrations, current leases, projections and finalized paper
envelopes keep their original identities. Rotation supplies no admission,
execution or qualification authority.

The nested `retention` receipt names the retirement sequence/hash, paper
checkpoint hash and counts removed from each active map. A new source archive
returns 201. An existing source cut returns 200 and can still create its first
retirement (`retention.duplicate: false`). Repeating rotation against the same
working source returns the original archive, checkpoint and retirement
identities with duplicate flags, without sampling a clock. Paper records created
after that cut have their own source seals; paper creation alone does not require
another copy of unchanged source data.

Retired command, admission, publication and standalone routed-input IDs remain
reserved. Exact retries return the original receipts without resubmission or a
new observation time. Different bytes under an old identity conflict. Negative
identity lookup uses immutable source indexes, and a historical hit is verified
against its original bounded archive. A missing source that was designated a
keeper is corruption, not an authorized historical retry.

New decisions/outcomes after retirement use schema v4. They retain v3 selection
and prefix rules while canonicalizing the recording metadata JSON; provider
`original` strings and their digests remain exact. Existing v1–v3 envelopes and
hashes are unchanged. Cumulative prefixes continue from the approved cut without
counting keepers twice. An outcome whose forward observation reaches retired
history retrieves the actual selected publications/admissions from original
segments, including pre-decision coverage and delayed publications that lower
the receipt-time window. Point counts and chained digests verify range
completeness and order. The range and resulting paper-envelope byte limits
remain finite; a window exceeding them fails visibly.

Established paper reads verify their retained bases against the actual closed
checkpoint or post-cut source seal, check complete closed membership, and
compare any still-active source overlap. Original archive-body corruption
therefore does not make established paper/protection reads depend on archive
availability. Historical source inspection and new research that needs retired
observations can fail when those originals are unavailable.

Source-seal reads are scoped to actual active paper owners or the complete
historical owner set being inspected. They use original-ID index lookups within
the 512-owner read budget instead of scanning all historical seals. Historical
checkpoint and paper-archive replay still require their original owners' seals,
including records which have left the working set. Missing required custody is
an error; unrelated history does not occupy the working view.

`GET /v1/feed-archives/paper-custody-audit` accepts no query or body. It performs
the complete source/index/retirement audit, then independently reconstructs every
retained paper record's original observation window and replays its evaluator.
It also rederives every checkpoint's closed membership. A self-rehashed paper
row and matching source seal cannot replace original archived source bytes.
The response binds the actual paper record root and source archive/index head,
with false admission, execution and qualification flags. Full audit work grows
with history; a deadline, missing original or incomplete replay never returns a
partial success. Checkpoint issuance independently performs the same original
source replay for new paper records before trusting their next closed cut.

Schema 104 persists `mantik_feed_retention` and
`mantik_paper_feed_source_seals`. Restore both with all source indexes,
checkpoints, paper owners and schema history. Collector/archive writers must
preserve the approved retirement root, and paper inserts must atomically retain
their matching source seals. Database guards reject older running writers that
discard these fields; schema-103 startup binaries reject schema 104. Rollback
requires matching binaries and an isolated restored backup. The disposable
recovery fixture includes a real retirement and post-retirement v4 paper records
and verifies their original-source replay and exact retries after restore.

Rotation extends collector storage beyond the publication, admission and command
limits without increasing those limits. It currently requires explicit calls.
The separate 256-decision/outcome limits, evaluation assessment and advisory
review limits, other routing owners and automatic retention still require the
remaining archive units. This endpoint alone does not permit indefinite runs.
