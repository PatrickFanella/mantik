# Retained news event groups

`news-events` derives a research view from an existing private news journal. It
links retained claims with the same declared event key while preserving source,
article, revision, exact excerpt and original-record references. It does not
create or reopen service decision cases.

```sh
./bin/mantik news-events --journal /absolute/private/news-journal --as-of 2026-10-15T18:00:00Z
```

The receipt cutoff is required and accepts RFC3339 with optional fractional
seconds. Only records with `received_at <= as_of` contribute to the view. Each
record already requires `published_at <= available_at <= received_at`; an old
publication timestamp cannot make a late receipt visible at an earlier cutoff.
Times are emitted in UTC. A future cutoff is a query over retained records,
not a forecast or evidence of future collection.

The command opens only an existing manifest and acquires the journal ownership
lock. It audits the complete journal before deriving the view. Invalid later
records still fail the audit even when they fall after the selected cutoff.
The view does not write an index or review status. Journal limits remain 128
published revisions and 16 claims per recording.

## Identity and attribution

The output uses schema `mantik-news-events-v1` and policy
`declared-event-revision-review-v1`. Each group ID hashes the logical journal ID,
evidence class and declared event key. The same event key in different evidence
classes produces separate groups. A different logical journal also produces a
separate identity. Reopening the same journal preserves its IDs.

Event keys are operator declarations. Matching keys do not establish semantic
equivalence. Two publishers or syndicated copies can contribute to the same
group, but every claim retains its own source/article/revision identity. The
view and each claim keep `independence: not_assessed`. Neither distribution
count nor matching original text establishes independent corroboration.

Groups, claims and change notices have deterministic identity order, rather
than publication or receipt order. An exact retention retry adds no claim or
notice. The view includes excerpts, but never includes the full original text.
Use each claim's `record_key` with `news-inspect --claim CLAIM_ID`; explicitly
add `--original` when the full retained article is needed.

## Revision policy

A claim in the latest visible revision of its source/article/evidence class has
state `current`, or `retracted` when that revision is a retraction. Claims in
older visible revisions have state `superseded`. These states describe the
recording lineage; `current` does not mean verified, true, fresh or tradable.
Original claim assessments remain unchanged.

Each correction or retraction adds one change notice to every event key in the
union of its own claims and its immediately preceding revision's claims. A
claim moved to another event therefore flags both groups. An omitted claim
still leaves a notice in its previous group. Multiple claims for the same event
in one revision produce one notice for that revision in that group.

A notice carries the new and previous record keys, revision kind and causal
availability/receipt times. Both records remain inspectable. Affected groups
have `requires_review: true`, including groups with only superseded claims.
This flag records an unresolved review requirement in the projection. It is
not an acknowledgment ledger and cannot be cleared through this command.
Historical notices remain visible at later cutoffs; an earlier cutoff excludes
later revisions and their notices.

## Remaining routing work

[Receiver-bound news admission](news-cases.md) separately retains originals and
routes declared events to dedicated service research cases with version-fenced
closure. This local view does not mutate or acknowledge those cases, and its
journal-scoped group IDs differ from receiver-scoped case IDs. The view makes no
provider, model, HTTP, database, decision or execution calls and grants no
qualification credit.
