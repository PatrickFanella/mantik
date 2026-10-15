# Private news source journal

The news journal retains original article text and validated claims before any
case routing. It extends the [recorded news boundary](news.md) with private,
append-only local persistence, verified predecessor chains and targeted source
inspection. It makes no provider, model, service API, broker or routing request.

## Ownership and publication

`news-retain` creates one logical journal in an explicit absolute directory.
The directory must be a real directory owned by the current user with mode 0700;
its parent must already exist. Artifacts must be regular current-user files with
mode 0600. Symlink roots/leaves and incompatible ownership or permissions fail.
The journal takes a nonblocking exclusive Linux file lock for the whole handle,
including read/audit commands. Another handle/process fails instead of queuing a
writer or inspecting a changing publication. Calls within one handle serialize.

The manifest carries schema `mantik-news-journal-v1` and a random logical journal
ID. Every published bundle binds that ID. Opening or using a journal audits its
manifest, lock identity, records and revision chains; records from another
logical journal cannot silently be adopted. An existing directory with artifacts
but no manifest is not initialized as a new journal.

Each article revision publishes one `mantik-news-retained-v1` bundle containing
the complete recording and its normalized result. Publication writes a private
temporary file, flushes its bytes, links the complete bundle into its immutable
destination, and flushes directory entries before reporting retention. There is
no separately mutable index or partial normalized/source pair. An exact retry
of an existing record also flushes the directory before acknowledging it.

A successful publication removes only its own temporary link and flushes that
removal. Leftover temporary artifacts are retained and listed separately; they
are not decoded as published records or automatically deleted. A temporary link
may remain beside an already published record after an interrupted operation.
Do not infer completion from its name. A later explicit retry audits the actual
published binding and returns its retained identity without creating another
record. This implementation/reopen evidence is not a power-loss, off-host backup
or archive/restore qualification.

## Immutable identity and verified revisions

A record key hashes source ID, article ID, evidence class and revision. The
canonical recording digest covers the source text, claims, excerpts, metadata
and declarations described by the normalization contract. A new identity is
retained once. The same identity/digest returns `duplicate=true`; changed content
under an existing identity returns a conflict and does not rewrite it. Equivalent
UTC instants retain the same identity/content. Caller source files and custody
references are preserved; the receipt adds a separate `news-journal:` reference.

Before a correction or retraction can publish, the journal requires:

- The immediately preceding revision of the same source/article and evidence
  class is already retained.
- Its canonical recording digest matches the declared predecessor digest.
- The new source availability and receipt times do not precede its predecessor.
- The predecessor is not a terminal retraction.

Retention is sequential; out-of-order or cross-class ancestry is rejected.
Retractions are terminal in journal v1: there is no implicit reinstatement
through a later correction. A separately reviewed contract is needed to express
reinstatement. Already retained ancestors remain inspectable, including after a
later correction/retraction. A missing or changed ancestor causes subsequent
journal audits to fail rather than silently accepting a broken chain.

Audits recompute normalization from the retained original recording and compare
the complete derived result, filename identity and logical journal binding.
Original text, source digests, uncertainty and independence declarations remain
separate from truth, semantic entailment and qualification. Identical syndicated
text stays separately attributed; retention does not infer independent
corroboration or produce a narrative evaluation.

## Bounds

A journal permits at most 128 published article revisions, 256 total artifacts
including manifest/lock/temporary files, and 4 MiB per retained bundle. The input
recording's stricter [normalization bounds](news.md) still apply. Audits process
this bounded inventory and retain summaries rather than all source bodies in
memory. Capacity failure does not prune, overwrite, silently rotate or delete
pending evidence. Exact retries of a valid published record remain possible at
published-record capacity. Archiving and a new continuation journal require a
separate workflow; no archive command is supplied by this unit.

Unknown files, malformed artifacts, broken hashes/lineage, missing authority
files and unacceptable permissions fail closed. The ownership/locking contract
assumes cooperating processes preserve the journal directory and lock. It is
not a signature or protection against an owner deliberately rewriting all
related evidence.

## Commands

```sh
./bin/mantik news-normalize --export-fixture > /tmp/mantik-news-fixture.json
./bin/mantik news-retain --journal /absolute/private/news-journal --file /tmp/mantik-news-fixture.json
./bin/mantik news-journal --journal /absolute/private/news-journal
./bin/mantik news-inspect --journal /absolute/private/news-journal --record RECORD_KEY --claim demo-estimate
./bin/mantik news-inspect --journal /absolute/private/news-journal --record RECORD_KEY --original
```

Replace the absolute path with the owned private journal directory and
`RECORD_KEY` with the key returned by retention/status. Use a separate private
source recording for actual material. The exported article is invented and
unqualified, and retention supplies no market or strategy qualification credit.

`news-retain` validates the input before creating the journal and returns its
logical ID, record key, canonical digest, journal reference and duplicate flag.
`news-journal` opens only an existing manifest, audits all retained sources/chains
and returns bounded summaries plus temporary artifact names. It does not return
article bodies or claim that a pending file is an unpublished article.

`news-inspect` opens only an existing journal and audits it before selecting one
record. By default it returns that record's normalized result and excerpts.
`--claim` selects one bound claim from it. The complete original text is returned
only with `--original`, whether or not a claim selector is supplied. A selected
excerpt can itself contain substantial source text; it is not redacted. Unknown
record/claim identities fail. Inspection never fetches the article URL, resolves
private custody references, or submits a research input. Read commands can
create/acquire the lock file but do not initialize a missing manifest or alter
published source records.

This completes local retention and targeted source inspection for the first
news implementation units of issue #12. Provider collection, journal archive/
restore, related-event routing, correction/retraction case reopening and
prospective qualification remain separate work.

[Event groups](news-events.md) provide an audited view at an explicit receipt
cutoff, including revision review notices and original-record references.
