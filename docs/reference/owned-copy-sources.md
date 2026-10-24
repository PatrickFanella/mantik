# Owned copy-source records and follow intent

Mantik retains copy-source recordings through the same account-watch owner as
social source material. This implements a bounded part of #14: attributed source
records and independent follow-policy intent. Provider-backed observation,
instrument/quantity verification, automatic dispatch, evaluated admission and
financial execution remain unavailable. No source quantity or claimed fill can
write an account, reserve capital, create a position or release protection.

## API and policy

Authenticated `POST /v1/copy-watches` accepts the existing watch-policy command:
an immutable request ID, receiver, current policy predecessor hash and explicit
provider/account identity. The behavior is `research`, `decision_review` or
`bounded_paper_follow`. Configured-rule invocations are not accepted through this
endpoint. Research and review have no follow bounds; bounded follow requires a
`follow` object with all four independent operator limits:

- `max_order_notional_cents`: positive and no greater than exposure limit;
- `max_exposure_cents`: positive, at most 1,000,000,000,000 cents;
- `stop_distance_bps`: 1 through 5,000;
- `max_holding_seconds`: 1 through 86,400.

These limits declare intent. They are not an allocation policy, account binding,
sizing calculation or executable strategy. The owner does not copy source size
into Mantik orders. The same provider/account has one watch in this shared
journal, so alternate social/copy endpoints cannot create overlapping watches.
An authorized update must pin the current predecessor; historical retries keep
their original receipt and do not read a new clock.

`GET /v1/copy-watches/{id}` reads the current watch and its scoped
`connector_not_configured` status. The social endpoints remain compatible;
both sets of routes operate on the same owner and immutable history. Full source
history is read through `GET /v1/social-history`, including all policy revisions,
social records and copy records. No connector is contacted by these operations.

[Typed supervision](supervision-tools.md) exposes the same owner through
`change_copy_watch_policy` and `inspect_record` kinds `copy_watch` and
`copy_applicability`, including the stdio MCP bridge. It retains the same request
IDs, predecessor checks, bound validation and inactive authority. The social
watch tool remains restricted to research and review.

## Attributed activity

`POST /v1/copy-recordings` accepts the retained-source capture command. The
original bytes must be a `mantik-copy-recording-v1` manifest with the same source
provider/account as the selected current watch. `source.post_id` retains its
existing wire name and identifies this source activity record. The manifest
uses `post`, `edit` or `delete` for its lifecycle and an `activity` object for
non-deletion records. The evidence class is `recorded_copy_unqualified` or an
explicit `synthetic_unqualified` fixture class. A social recording cannot use
the copy class, and a copy recording cannot use the social class.

The activity kind preserves what the source purports to describe:

| Kind | Retained states | Quantity/direction |
|---|---|---|
| `claimed_trade` | declared, retracted | positive buy/sell quantity |
| `observed_order` | open, cancelled, filled, rejected | positive buy/sell quantity |
| `observed_fill` | reported, reversed | positive buy/sell quantity |
| `observed_holding` | snapshot | positive long/short, or zero flat |

These are operator recording manifests, not authenticated platform responses.
An `observed_fill` label is not proof that a provider fill occurred. Readback
marks `provider_observation_verified:false`, distinguishes a claimed trade from
an unverified observed-record assertion, and keeps financial effects unavailable.
Changing an existing claimed-trade revision to observed-fill kind is rejected as
an invalid source transition. A distinct observed record must retain its own
source identity rather than promoting the earlier claim.

Each activity retains source symbol, requested canonical symbol, asserted
mapping version, side, quantity units and unit claim (`shares`, `contracts` or
`provider_native`). Source quantity is at most 1,000,000 units. Trade/order/fill
price claims may be absent as zero, or range up to 100,000,000 cents; holdings
snapshots have no trade price. The manifest's symbol must match its activity
symbol, but neither that equality nor a mapping label verifies instrument or
unit semantics. Readbacks explicitly report `source_mapping_unverified` and
`source_quantity_units_unverified`. No conversion or fill calculation occurs.

## Revisions, duplicates and announcements

The existing source owner retains occurrence, asserted observation, server
retention time and original digest. Its delay bound uses server availability,
so a prompt asserted observation time cannot repair a late import. Edits must
follow the current revision and preserve the activity kind and evidence class. Cancellation,
rejection, reversal, retraction and a flat holdings report remain visible as
terminal source states; they do not cancel Mantik orders or reconcile positions.
An earlier activity's applicability becomes `source_superseded` after an accepted
revision. Deletion and invalid revision behavior follow the same retained owner
rules as social records.

Exact source revisions share their original group and do not become new
accepted source versions under another capture ID. Structured copy grouping
also ignores prose, capture/source IDs, mapping labels, observation time and
announcement references for otherwise equal activity dimensions. It is a
conservative research grouping key, not verified economic identity or statistical
independence. Ambiguous mappings and overlapping sources need their actual
owners before any future exposure can be admitted. Independent-evidence credit
and all dispatch/admission/execution/qualification flags remain false.

An optional `announcement` source reference resolves only to an already retained,
accepted, non-deleted social source version available by the asserted observation
time. The receipt pins that exact capture hash, original digest, source version,
retention time and evidence class. Later edits do not rewrite the historical
link. Missing or ineligible references remain `source_reference_unavailable`.
A resolved reference is `retained_reference_not_verified_correlation`, with
`announcement_correlation_verified:false`; matching a caller-declared reference
does not prove that a post and trade describe the same economic event.

## Persistence and verification

The shared owner validates its entire journal before reads, writes and retries.
Memory uses the aggregate mutex. PostgreSQL serializes journal writes with the
receiver row lock and stores the bounded aggregate atomically. Copy commands and history
reads share the social owner's COMMIT acknowledgement boundary: failed COMMIT
returns a zero result and an error, with no successful receipt or catalogue. This change adds
no table or schema migration. Existing journal bytes and hashes remain unchanged
when the optional copy/follow fields are absent, verified against a fixture hash
captured from the pre-change source. Older readers cannot interpret new typed
records; rollback must retain the complete source journal and a compatible
reader or restore its approved backup. No running instance is upgraded here.

Tests cover bounded follow intent, source-kind separation, revisions and
reversals, duplicate/overlapping recordings, retained announcement versions,
unknown authority assertions, complete-history revalidation, detached reads,
concurrent clock-free retries, authenticated HTTP, PostgreSQL reconnect retention
and unchanged operations/capital. These are local/disposable fixture contracts.
They do not complete #13, #14 or #19, supply provider access, perform model work,
create financial effects or qualify #27's natural soak.
