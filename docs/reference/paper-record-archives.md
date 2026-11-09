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

Archive sequence `N` closes the actual owner set present in epoch `N-1`.
Immutable creation metadata independently determines that complete set. Rewriting
an archive body, header, batch summary and membership rows to omit an owner is
rejected against the original creation records. Later records use a later epoch
and leave the earlier snapshot unchanged, even across clock representation or
historical event-time differences.

Schema 106 adds indexed owner intervals for historical paper/source checkpoint
replay. A checkpoint selects the actual decision/outcome identities created by
its paper archive epoch, with original event times at or before its exact
seconds/nanoseconds cut. Later records cannot enter an earlier epoch merely
because their event clock is historical. The checkpoint's own pins do not supply
the owner list; native replay must match the independently selected complete set.

An interval's optional `retired_epoch` is its exclusive end. This metadata is
separate from the immutable creation pin and does not change existing payloads,
record hashes or checkpoint envelopes. Archive `N` retains membership at epoch
`N-1`, so a later retirement cannot remove an owner from its original archive.
The existing immutable-epoch guard still rejects retirement writes in schema
106. No endpoint or scheduler removes active paper records yet.

Historical replay reads matching active originals first. After an authorized
retirement, the resolver can load missing records from their original archive
at `created_epoch+1`, verifying its header, body, independent membership and
matching creation pins before native checkpoint replay. A missing unretired
active row remains a conflict even if an archived copy exists. Selection is
bounded by the original checkpoint capacities and a 64 MiB total payload budget;
the resolver retains at most one raw archive segment at a time. PostgreSQL uses
the owner interval index and original-ID table indexes, without joining all
historical payloads into the current catalogue.

Targeted decision/outcome source reads and their existing record routes use
the same independently reserved identity. For an approved retired identity,
they resolve `created_epoch+1`, recheck actual scope and payload custody and run
the native evaluator against that archive's original feed/strategy context.
An outcome also resolves and replays its actual decision dependency. Decision
explanations render those finalized original facts. Exact retired command
retries return the original receipt with `duplicate: true` without sampling a
clock; changed commands conflict and failed replay returns no receipt or sources.
An archive alone never authorizes fallback for a missing unretired active row.
Lists and active-owner writes keep their complete active-catalogue validation.
This reader preparation does not authorize retirement, reuse an identity,
issue new outcomes for retired decisions or remove working capacity limits.

Current decision/outcome catalogue reads and writes now prove their working set
against the actual active owner intervals, then replay every native record.
PostgreSQL uses the existing GiST interval index and bounded metadata selection
at the current archive head; it does not scan historical payloads to enumerate
the working set. Memory scans its identity metadata while retaining only the
bounded active rows. Original hashes, exact clocks and strategy/decision scope
must match each active envelope. Historical checkpoint and archive replay keep
their complete original owner sets and counters.

Decision reviews use that active proof and resolve only their additional actual
retired decision dependencies from the first original archives. The review
catalogue/cases, source-bound findings and exact retries survive that dependency
move; a new typed review can also select the original decision. Review retirement,
allocation/attribution references, archive-writer transitions and automatic
retention still require their remaining owner work before physical removal is
enabled.

Private source-custody views use the same owner scope. Working reads load seals
only for actual active decision/outcome IDs; historical checkpoint inspection
uses its independently selected owner interval, and archive inspection uses its
sealed original membership. Each scope reads at most 512 source seals by their
original-ID index. Unrelated historical seals do not fill that working budget.
A required missing or changed seal still fails native replay. Complete audits
continue through every checkpoint and archive; this selection does not turn a
partial history check into a complete audit.

Schema 107 adds immutable selection metadata to decision/outcome creation rows:
the original strategy ID/version, decision ID/hash and exact seconds/nanoseconds
of decision finalization. An outcome derives its scope from its actual retained
decision basis. Its own evaluation clock remains the separate availability time.
These columns and the `mantik_paper_sample_window` index prepare bounded window
selection across active and archived owners. The window consumer below uses
these original scope pins for new assessments.

The migration backfills from retained native envelopes and checks original
record hashes and clocks. Replaying populated schema 107 rejects missing or
changed projections. Older running envelope writers obtain the same metadata
through their existing insertion trigger, in the original transaction. Full
archive reads independently compare these columns with archived owner payloads;
the index is selection metadata and grants no evaluation or financial authority.
Existing archive payloads, creation-pin hashes and original source hashes stay
unchanged. The retirement guard continues to reject removal of paper owners.

Predecessor backfill can omit the later scope columns only for an exact duplicate
of an already reserved identity with its original record hash and event clock.
Its candidate insert reuses that immutable original scope before PostgreSQL
checks the row shape and applies `ON CONFLICT`. A missing reservation or changed
hash/clock remains a conflict; this path never fills new identities by guessing.

Schema 108 makes the sample index use native byte ordering for decision IDs,
record kinds and record IDs. This keeps PostgreSQL and memory pagination aligned
when event clocks tie and IDs contain case or punctuation. The private sample
reader returns at most 128 metadata records per page for exact declared strategy
versions and decision finalization times in `[window_start, window_end)`. A row's
own event clock must be at or before the requested as-of cut. Late outcomes stay
inspectable rather than disappearing at the declaration's outcome deadline;
native evaluation decides their applicability.

Selection includes approved retired owners as well as active owners. It does not
load historical payload bodies or impose the active catalogue limit on complete
history. PostgreSQL merges bounded indexed batches from the declared variants;
memory retains only the next bounded batch while scanning its metadata. Cursors
bind the declaration, exact as-of cut and actual archive head, and pages must
share one owner transaction/snapshot or memory lock. There is no public cursor
API. These records are selection metadata, not evidence or authority. The native
assessment consumer resolves original payloads, verifies custody and replays
the evaluations before using them.

Private payload pages resolve those selections in the same owner snapshot. Each
page rechecks actual creation, retirement and scope metadata. An active row must
match its original record hash, clock, receiver and basis hash; a damaged active
row cannot fall back to an archive. Only an approved retirement permits a missing
active row to resolve from its first archive at `created_epoch+1`. The archive's
original body, header and independently closed membership must validate. A later
copy never substitutes for a missing first original.

Payload pages preserve a 64 MiB total selected-payload budget and retain at most
one archive segment while resolving a page. Returned native envelopes are
detached from retained state. Any row, scope, original or budget failure returns
no partial owners.

The private native reader accepts up to 64 selected records and reserves the
remaining payload-page slots for their actual decision dependencies. An outcome
on a later page resolves its decision through the original creation metadata,
including an approved retired decision. The reader replays each decision and
outcome against the actual strategy, feed and source seals in the same owner
snapshot. Its feed-custody view contains only those actual owners. Selected-page
counts never replace the full catalogue's checkpoint counts; full-catalogue
readers retain their existing completeness checks. A missing dependency,
changed source or different native result returns no partial owners.

New evidence reads, entry-source reads and assessment issuance use version 4
window evidence over those native pages. The source cut binds the complete
declared window to its original creation-epoch cutoff and ordered original pins.
It includes approved retired decisions and their outcomes, including inspectable
late attempts. A complete window can exceed active catalogue capacity, subject
to a 64 MiB total native-source budget and the existing 8 MiB evidence wire bound.
Issuance, exact retry, assessment inspection, archive replay, allocation and
attribution consumers validate the same owner-derived cut. Earlier version 3
evidence keeps its original complete-catalogue recipe and hashes. Physical
paper retirement and scheduling still require the remaining consumer work.

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

Assessment inspection and exact retries now use the original paper archive's
native source context when the assessment has been sealed. They rederive the
original assessment after its input rows move, preserving legacy recipes and
the complete original evidence hash. Corporate-source readbacks use the same
resolver. See [historical assessment replay](paper-evaluation-assessments.md#historical-assessment-replay).

Backups must include `mantik_paper_record_archives`,
`mantik_paper_record_epochs`, `mantik_paper_archive_members` and
`mantik_paper_archive_batches`, together with all original source owners, feed
archives/indexes, custody checkpoints, retention metadata and receiver/schema
history. Archive payloads, batch roots, membership and original identity metadata
are immutable. Predecessor startup binaries refuse schema 108; rollback requires
matching binaries and an isolated restored backup. Removing migration records
or custody rows in place is unsupported.

The disposable recovery contract now includes a populated paper archive over
decisions, waiting outcomes and an actual assessment. Restore replays its original
evidence, verifies first-ID lookup and exact retries, and checks archive audit
without starting workers or making provider requests.

Historical paper/source checkpoints and existing assessment inspection now have
bounded original owner views. Physical paper retirement still requires complete
historical sample selection for new assessments, pending outcomes, allocation
references and reviews. Admission-review and other
workflow/source retention, automatic scheduling and growing-history operational
cost checks also remain part of the complete archive work.
