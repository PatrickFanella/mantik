# Owned social watches and attributed recordings

The authenticated service retains watch policies and exact social recording
originals in one replayed journal. This is a bounded dependency for issue #13,
not its closeout. Provider connection/authenticity, research/decision/rule
dispatch and the remaining acceptance checks still need binding. Copy-follow
execution remains separate under #14/#19/#20/#21. No existing Augr data or
service is used.

## Watch ownership and revisions

`POST /v1/social-watches` accepts an immutable request `id`, actual `receiver_id`,
`previous_sha256` and `policy`. The policy contains:

- Stable watch `id`, opaque `provider` and stable `account_id`.
- A sorted, unique list of 1–16 intended instrument symbols.
- Intended `behavior`: `research`, `decision_review`, `configured_rule`, or
  the inactive `bounded_paper_follow` intent described in [copy sources](owned-copy-sources.md).
  Only `configured_rule` accepts a required `rule_id`.
- `max_observation_delay_seconds` from 1–86,400 and an `enabled` flag.

Provider/account strings select a recorded namespace; they do not configure a
platform adapter or establish account access. Changing a display name is not an
identity binding. One watch owns each provider/account pair, including disabled
watches. The subject cannot be changed under an existing watch ID. Create uses
an empty previous pin; updates require the full current policy-record hash.
Stale updates conflict. Disabling a watch appends a policy version and retains
its earlier versions and recordings.

The service assigns policy version and registration time. New recordings must
pin the current policy; exact retries under older policies return their original
receipts without reading the current clock. Policies express intended behavior;
this slice reports dispatch unavailable and does not claim that a research case,
decision review or rule ran.

## Recording and source contract

`POST /v1/social-recordings` accepts request/receiver identity, `watch_id`, current
`policy_sha256`, `evidence_class`, exact `original` string and its raw-byte
`original_sha256`. For social manifests, accepted classes are `synthetic_unqualified` and
`recorded_social_unqualified`; the copy extension adds its own manifest and class.
Prospective/qualified labels, caller balances, admissions, execution/fill
authority, effects and qualification assertions are not accepted.

The original is a strictly decoded `mantik-social-recording-v1` manifest. It
contains `source` (`provider`, `account_id`, `post_id`), integer `revision`, `kind`,
`occurred_at`, declared `observed_at`, literal `text`, sorted unique `symbols`
and nullable `repost_of`. Source identity must match the watch. This manifest is
a caller-supplied source recording, not a platform-authenticated response. Exact
bytes and digest remain retained alongside the parsed representation.

Supported kinds are `post`, `edit`, `delete`, `repost`, `forecast` and
`declared_position`. Forecasts and positions retain source assertions and report
`structured_extraction_unavailable`; they do not become predictions, verified
holdings, orders or observed fills. Deletes have empty text/symbols. Reposts
require an explicit source reference. Malformed/unsupported manifests are
rejected rather than interpreted by a model. No model call is involved.

## Causality, duplicates and reliability

A first source revision must be revision 1 and cannot be an edit/delete. Later
revisions require the immediately preceding retained source version, nondecreasing
occurrence/observation times and an edit/delete transition. A deleted source
cannot be resurrected by an edit. Missing predecessors, revision forks and
invalid transitions remain inspectable recordings with a reason, without
advancing the accepted source head. A new request can resolve a previously
missing predecessor or repost after that source arrives; earlier receipts remain
unchanged.

Different captures of the same source revision may have different declared
observation times or JSON whitespace. The service compares parsed source
semantics, excluding the declared observation time, and marks an unchanged
revision as a duplicate. Contradictory semantics under that revision are retained
as `source_revision_conflict`. Exact command retries reuse their original
history entry; a different recording under the same request ID conflicts.

Edits/deletes retain their original source root. Reposts resolve through actual
retained source heads, including across watched accounts, and preserve the
parent's root and claim group. Unknown/deleted parents are unresolved. Repeated
literal claims share a grouping hash after whitespace/case normalization and
symbol binding. This is exact grouping, not semantic fact checking. Every
recording has `independent_evidence_credit: false`; alternate wording and
unresolved attribution cannot manufacture independent qualification evidence.

The receipt records both declared observation delay and actual service
availability delay. Reliability checks use retention time minus source occurrence
time, so late imports cannot hide delay by claiming an earlier observation.
Future observation timestamps are rejected. Disabled watches, excessive delay,
unwatched/unresolved symbols, deletes and unavailable structured extraction have
specific reasons. These annotations retain unqualified assertions; instrument
mapping and provider access remain unverified.

## API readback and capability scope

`GET /v1/social-history` returns the actual receiver, journal head, complete
history and current watches. `GET /v1/social-watches/{id}` selects a watch.
Each watch reports `connector_not_configured` for its provider/account scope,
with provider access and dispatch false. It does not change global operational
readiness. No connector, account availability check or external request runs.

All routes require the service bearer token and reject query strings, duplicate
or unknown field names and trailing JSON. Policy requests are bounded to 4,096
bytes, capture requests to 64 KiB, originals to 16 KiB and text to 8 KiB. New
entries return 201, exact retries 200, invalid shape/time 422, policy/identity or
capacity conflicts 409, missing watches 404 and storage failures 503.

The journal is limited to 128 entries, 32 watch identities and 8 MiB, with no
eviction/delete endpoint. Capacity errors preserve existing history and retries.
It is a research retention registry, not a protective-operation queue or a
provider streaming capacity claim.

## Storage, verification and rollback

Memory serializes replay and append under its aggregate mutex. PostgreSQL schema
41 adds `mantik_social_watch_journal`, a bounded `bytea` singleton. The actual
receiver row serializes writers; this owner takes no collector/state lock after
receiver and writes no routing, operation, capital or effect state. Catalogue
reads use repeatable read. Each entry links its predecessor; full replay checks
commands, source/policy derivation, results, time, sequence and hashes before
reads, writes or retries. Self-rehashing result flags does not grant authority.
Failed insert/update transactions preserve the preceding journal.

Verification units cover source history/recovery, scope and authority guards,
actual Memory/PostgreSQL ownership, concurrent exact retries, authenticated HTTP,
failed insert/update rollback, schema upgrade and restart/restore. Run pinned Go
1.26.9 `make verify`, `make test-race` and disposable `make test-integration`.
These are fixtures and compatibility checks, not provider or qualification proof.

Schema-40 data is preserved. Old schema-40 writers refuse schema 41. Rollback
uses a private pre-upgrade backup restored into separate resources with its
matching binary; dropping live tables is not rollback. Local checks, hosted CI,
merge, deployment, provider behavior and natural soak evidence remain distinct.

## Source applicability before dispatch

`GET /v1/social-recordings/{capture-id}/applicability` inspects the selected
recording against the service clock and the retained history. Authentication is
required. The endpoint accepts no query parameters or caller-supplied cutoff,
source history, provider verification, admission, or effect assertions.

The read validates the complete journal, including entries newer than the
cutoff, before selecting the available prefix. The response pins both the full
history and that prefix. `selected` identifies the exact capture and original
byte digest; `current` identifies the latest accepted revision available at the
cutoff. A future capture is reported as `not_available_at_cutoff`. Later edits,
deletions, watch-policy changes, disabled watches, stale source material, and
unsupported structured extraction have explicit statuses. Previous capture
receipts remain unchanged; a later source recovery cannot promote an unresolved
receipt.

Repost lineage pins each exact parent capture visible when the repost was
retained, in journal sequence order. A parent revision occurring after the repost
is inapplicable. Nested reposts retain the entire parent chain. Edits preserve
that chain; they cannot erase a chronology fault. A later parent edit or deletion
invalidates the current repost view while a historical inspection still uses
only its available prefix. Earlier cutoffs are supported by internal replay
inspection; HTTP callers cannot select one. Mixed evidence classes produce an
explicit repost-applicability fault. An unversioned platform repost reference does not
prove which historical text the author saw; the view refuses ambiguous chronology
rather than inferring a different parent revision.

`current_source_assertion_dispatch_unbound` means the source revision and watch
policy are current within this recorded history. It does not authenticate the
provider/account, verify instrument mapping, establish independent evidence,
dispatch work, admit a strategy, or authorize an economic effect. Those flags
remain false. Applicability responses are read-time projections, not persisted
dispatch or qualification receipts. The response hash includes its service-clock
cutoff, so separate reads at different times can have different hashes. The
read describes the fetched storage snapshot; it is not a transactional dispatch
check. A future dispatch owner must recompute applicability under its write
transaction rather than accepting this response as a token.

## Copy activity

[Owned copy-source records](owned-copy-sources.md) extend this same journal with
explicit claimed-trade/order/fill/holdings kinds and bounded follow-policy intent.
Copy and social routes share watch identity, revision history and capacity. The
new fields are optional for existing records; source observations, unit/mapping
claims and announcement references retain no dispatch or trading authority.
