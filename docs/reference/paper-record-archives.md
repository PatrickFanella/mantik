# Paper record custody and owner epochs

Schema 105 seals original decisions, outcomes, evaluation assessments and
decision reviews with the native replay context needed to inspect them. It
reserves their original IDs and records their creation order independently of
event clocks. This prepares the remaining paper-retirement work in the
[continuous runtime plan](../plans/continuous-paper-runtime.md).

Active originals remain retained in this version. Creating a paper archive does
not free the separate decision, outcome, assessment or review limits, schedule
retention, prune other source owners or change paper capital. Since schema 112
the [retirement owner](#retirement-owner) frees decision and outcome capacity
after an archive. Scheduling and the assessment and review limits are remaining
delivery units; these endpoints alone do not permit indefinite runs.

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
| `GET /v1/paper-history` | Page native sealed decisions or outcomes. Requires `receiver_id` and `kind` (`decision` or `outcome`); optional `limit` is 1–64, default 16. Supply a previous page's opaque `next_after` as `after` to continue. |

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

Schema 110 exposes sealed decision/outcome history through `paper-history`
with `--receiver-id`, `--kind`, `--limit` and `--after`. The CLI validates the
returned receiver, kind, record hashes, order, archive boundary and continuation
before printing JSON. Its default kind is `decision`. The existing
`paper-decisions` command continues to inspect the active working catalogue.

The first page closes its view at the latest committed paper archive, returning
that header in `through`. Only records with `created_epoch < through.sequence`
are eligible. With no committed archive, `through` is null and the page is
empty. The cursor binds the actual receiver, kind, closed header hash, creation
epoch and last ID. Resume pages keep that boundary even after new archives,
historical-clock insertions or later approved retirement. Cursors are selection
data, not grants or proof of native evidence. They may be retained privately for
restart; changing the page limit does not change the selected history boundary.

Records sort by creation epoch and then original ID in native byte order.
PostgreSQL seeks the schema 110 index and selects at most `limit+1` metadata
rows. Each selected creation interval resolves its first original archive and
replays only the selected decisions/outcomes and actual outcome dependencies
against that interval's original feed/strategy context and indexed source
custody. A missing unretired active row is a conflict even if a duplicate exists
in an archive. Failed source replay, cancellation or COMMIT returns no partial
page. A page is limited to 8 MiB and 64 native records; each original segment
retains its existing 64 MiB bound. Memory scans identity metadata while keeping
only the bounded page and lookahead.

These pages provide native source history. They do not join allocation effects,
admission reviews or financial attribution, and they do not replace a complete
archive audit or enable physical paper retirement.

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
The immutable-epoch guard rejected every retirement write from schema 106 to
111. Schema 112 replaces it with the [retirement owner](#retirement-owner)
guard. No scheduler removes active paper records yet.

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
move; a new typed review can also select the original decision. Review retirement
and automatic retention still require their remaining owner work before
physical removal is enabled.

### Writers after approved retirement

Archive `N` closes the owners active at epoch `N-1`, so writers drop a retired
owner by its `retired_epoch`:

- The next paper archive omits a record of the previous archive only when an
  approved retirement ends its interval by the new archive's epoch. Any other
  omission conflicts. The retired record stays in its original archive.
- The archive audit applies the same rule to every step of the chain.
- An unchanged-feed rotation, a new paper archive and a new feed/paper custody
  checkpoint prove their working set against the active owner intervals, as the
  readers do. They no longer compare it with the previous checkpoint's closed
  counts, which still include owners retired since.
- Reading or auditing an archive or checkpoint replays the owners selected by
  its epoch. That selection is the completeness proof; the closed counts are
  not.

### Retirement owner

`POST /v1/paper-record-retirements` with `{"receiver_id": "..."}` removes sealed
decisions and their outcomes from the active working set. The owner selects the
records; a caller cannot name one. The response is a
`mantik-paper-record-retirement-v1` receipt with the sealing archive (`epoch`,
`archive_sha256`), the creation pins of the retired records and a count of the
active decisions it kept, by reason. It grants no admission, execution or
qualification authority. A request that finds nothing eligible returns the same
receipt with an empty `retired` list.

A retired record keeps its reserved identity and stays readable from its
original archive through the readers above. It no longer counts against the
256-decision and 256-outcome working limits. Retirement happens at the current
paper archive epoch `N`: the record is a member of archive `N`, its active bytes
equal the sealed original, and archive `N+1` omits it.

A decision retires together with all of its outcomes, and only when every
condition holds. `retained` counts each kept decision under the first one that
fails:

| `retained` field | The decision stays active because |
| --- | --- |
| `unsealed` | the current paper archive does not hold it |
| `allocation_open` | a record of the working allocation journal names it in a comparison or an execution effect. The journal seals only when no entry book can still change, so open protection never depends on an archive read |
| `newest_decision` | it is the latest decision of its strategy record and registration; the decision worker continues from it |
| `outcome_unsealed` | one of its outcomes is newer than the current archive |
| `outcome_pending` | it is an entry candidate without an `observed` or `price_unavailable` outcome |
| `shared_signal` | another kept decision evaluated the same quote for the same strategy record and registration |
| `custody_bound` | a paper custody checkpoint already binds the current archive epoch |

A custody checkpoint replays the owners active at its paper archive epoch, so a
retirement at that epoch would change a checkpoint that already committed.
Retire after `POST /v1/paper-record-archives` and before the next
`POST /v1/feed-archives` or feed rotation. Decisions counted under
`custody_bound` retire after the next paper archive.

The transaction sets `retired_epoch` and removes the active rows together, under
the same collector, state and receiver locks as the paper archive. In
PostgreSQL the `mantik_immutable_paper_epochs` trigger still rejects deletes
and every other change to an epoch row. It accepts one change: setting
`retired_epoch` once, to the current archive sequence, for a decision or outcome
whose creation pin is a member of that archive and whose active row the same
transaction already removed, while no custody checkpoint binds that epoch. Assessments and decision reviews do not retire yet.

A decision review stays active after its decision retires, and a retired
decision can still be reviewed. Later archives seal those reviews without their
decision: the archive writer, reader and audit resolve it from its original
archive and replay it natively.

### Automatic retention

`mantik serve --automatic-paper-retention` opts the service into a retention
worker. The default is off. It uses no model or provider connection and calls
the same owners as the routes above; it decides when to ask, and the owners
decide what may leave.

A cycle runs on startup and then every minute:

1. When the active set holds at least 128 decisions or 128 outcomes (half of
   each working limit), it calls the retirement owner. If nothing retired and
   the receipt counts a decision under `unsealed`, `outcome_unsealed` or
   `custody_bound`, it writes a paper archive and calls the retirement owner
   again.
2. When the active feed holds at least 1,024 publications, 1,024 admissions or
   2,048 collector commands (half of each limit), it rotates the feed. This runs
   after the paper step because rotation keeps every source an active decision
   or outcome still reads.

Each new paper archive holds the whole active set, so the worker does not write
one per cycle. After an archive that retired nothing, it waits until the active
decisions and outcomes have grown by 32 before it writes the next. Feed rotation
waits the same way for 256 more publications, admissions and commands. A restart
forgets these counts and costs at most one extra archive of each kind.

Each step commits on its own; a later error keeps the earlier results. Decisions
held by a pending outcome, an open allocation or the newest-decision rule stay
active, so the working limits can still fill when those do not clear. A
custody checkpoint written by another caller between the worker's archive and
its retirement holds those records until the active set next changes.
Assessments and decision reviews keep their working limits.

Worker progress, failures and clean stop are reported under the
`paper_retention` capability (`GET /v1/readiness/paper_retention`). Schema 113
fences older writers that reject that capability. The scope measures retention
health only. It grants no admission, execution or qualification authority.

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
evidence keeps its original complete-catalogue recipe and hashes. Scheduled
retirement still requires the remaining retention work.

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
bounded original owner views. Allocation replay, comparison and execution
appends, exact retries and full-history audit can resolve their actual decision
dependencies from first original archives after approved retirement. The
complete active owner check remains separate; absence alone never permits
archive fallback. The paper attribution catalogue reads through the same
dependencies after approved retirement. Physical paper retirement still requires
pending outcome and review dependency contracts. Admission-review and other
workflow/source retention, automatic scheduling and growing-history operational
cost checks also remain part of the complete archive work.
