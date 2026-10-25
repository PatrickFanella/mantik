# Service-owned allocation comparisons

`POST /v1/allocation-comparisons` compares retained native paper entry proposals
under an owner-selected funding policy. `GET /v1/allocation-comparisons` replays
the full retained journal and returns its records and per-policy ledgers.
Both routes use the existing bearer authentication.

This is the first durable service binding for the allocation core. It records
admission deferrals and expired-decision rejections. It cannot reserve cash,
create orders, apply fills or qualify a strategy. Issue #19's acceptance remains incomplete for
admission and effect bindings and funded execution; issues #20 and #21 still
require their actual execution and qualification owners.

## Request and assignment

The request contains only a command identity, the expected receiver, and a sorted
list of retained decision IDs and SHA-256 pins:

```json
{
  "id": "compare-baseline-001",
  "receiver_id": "receiver-REPLACE_WITH_ACTUAL_OWNER",
  "decisions": [
    {"id": "baseline-entry-001", "sha256": "REPLACE_WITH_RETAINED_DECISION_SHA256"}
  ]
}
```

The server rejects unknown fields, duplicate keys, trailing JSON, query strings,
invalid pins and bodies over 8 KiB. It accepts between one and 32 decisions,
ordered by ID with no duplicates. A pin is an identity check; the server also
replays the actual decision and verifies its retained strategy and feed basis.
Abstentions cannot enter allocation.

A new decision or capture ID cannot turn the same source occurrence into another
proposal for the same strategy. Before reading a new comparison clock, the owner
checks the selected batch and all earlier comparison decisions. The identity is
the exact retained strategy record plus the collector's provider/feed, canonical
symbol and quote occurrence time. Capture/registration IDs, receipt times, raw
record hashes and price corrections at that occurrence do not create additional
proposal identities. A conflict returns HTTP 409 without a journal write.

The original finalized decision may be reconsidered under a new comparison ID;
exact comparison retries stay clock-free. A genuinely later occurrence or a
different immutable strategy remains distinct, subject to the existing policy
and admission gates. This conservative rule does not establish independence
across providers or score evidence. Historical deferral receipts keep their
original bytes and replay behavior; the write guard does not retroactively
grant, repair or activate them. A future execution owner still needs canonical
source identities in its atomic admission/reservation/effect transaction.

For each decision, the server matches the actual strategy definition hash to
registered allocation-policy strategy rules. Exactly one policy must match,
and all decisions in a comparison must resolve to that same policy. Missing,
overlapping or mixed-account assignment returns a conflict. Research cannot
select an account or policy in this request. Policy limits and priority remain
operator configuration. The policy's funding account must still match its
retained owner, and fixture capital cannot own that account.

For legacy policies without an execution model, the proposal amount is the
retained price multiplied by registered quantity within the strategy's safety cap.
These records remain deferral-only and do not provide an execution cost ceiling. Expiry is the actual retained quote occurrence
plus the smaller of 60 seconds and the strategy's maximum quote age. Neither
comparison time nor a retry extends that expiry. This amount has no execution
authority: a future execution binding must reserve the full cost including fees
and preserve protective/recovery capacity before it can activate reservations.

## Admission, retry and replay

New comparisons retain `admission_checks` derived from the actual finalized
owner decision, immutable strategy and retained quote basis. Each check pins the
receiver, decision, strategy record and definition, basis, quote input ID and
actual evidence class. `checked_at` is the original comparison time;
`valid_until` is the proposal's quote-based expiry.

These are prerequisite availability checks. They confirm a retained finalized
decision, identify its unqualified source class, and explicitly report the
unavailable applicable evaluation receipt and owned execution evidence. They do
not run an evaluation recipe, certify source quality or issue an admission.
Synthetic, recorded and prospective market inputs remain unqualified. Before
expiry, status is `deferred` with source/evaluation/execution reconsideration
triggers. At or after expiry, status is `expired` and requires a new finalized
decision. Admission, execution and qualification flags remain false.

The service continues to supply an empty admission set to the allocation core. An otherwise applicable
proposal records `defer` with `admission_unavailable` and
`admission_reassessment`; an expired one records `decision_expired` and requires
a new finalized decision. No caller-supplied admission, balance, snapshot, effect,
notional or proposal is accepted. Cash remains at the owner's opening funding,
and positions, orders and commitments remain empty.

Each comparison appends one hash-linked entry to its policy's allocation ledger.
The owned record pins that entry, ledger head and policy. It has schema
`mantik-owned-allocation-deferral-v9`, authority
`service_owned_allocation_deferral_only`, and false execution/qualification
flags. Returned core ledger values remain accounting evidence; they are not
portable admission or execution grants.

An exact command retry returns HTTP 200 and the original record without reading
a new clock. A new record returns 201. Reusing an ID with different pins returns
409. New comparisons sample the service clock once under the owner lock/transaction
and reject a time earlier than the actual collector state's last clock with HTTP
422. Equality is allowed. The selected decision's finalization and proposal
expiry checks still apply. A rejected clock writes no comparison and consumes no
command identity, so the same command can be retried at a valid service time.
This floor applies only to new writes: later collector activity does not rewrite
historical readiness or invalidate an exact clock-free retry or restart replay.

A new comparison ID records reconsideration under current owner policies;
it never overwrites an old comparison. Captured policy catalogues remain bound
to current immutable owner records, so a later policy cannot rewrite an earlier
assignment. Reads and retries validate the whole journal before selecting a
record; a corrupt unrelated entry also fails the read. Readiness is reconstructed
from owner records at the original time, so self-rehashing counterfeit flags,
source classes, expiries or accepted statuses cannot bypass replay.

## Persistence and compatibility

Memory serializes the aggregate under its mutex. PostgreSQL schema 39 adds
`mantik_owned_allocations`, a bounded singleton journal committed atomically
with owner validation. Writers acquire collector, state and receiver locks in
that order. Reads use one repeatable-read snapshot. Failed inserts or updates
leave the old journal intact.

Retention stops at 32 comparisons or 8 MiB without evicting evidence. Exact
retries remain available at capacity. This bounded deferral journal is not an
operational order ledger; protective exits, recovery capacity, fee funding,
admission lifecycle and execution effects must be implemented before activation.

Schema 42 fences writers that cannot preserve v2 readiness fields in the same
journal. Its migration changes table metadata, with no new balance or admission
writer. Existing v1 records remain replayable with their original omitted
`admission_checks` field, wire shape and hashes; a journal may contain both
versions. Unknown schemas fail closed. Schema-41 binaries reject schema 42.
Accounts, policies, strategies, decisions and the fixture state are preserved.
Rollback requires stopping the new writer and restoring the retained pre-upgrade
backup with its matching binary; do not delete schema rows or tables in service.

Verification uses the authenticated handler with manufactured calendar/price
inputs, concurrent Memory/PostgreSQL owner contracts, SQL failure rollback,
self-rehashed corruption rejection, historical expiry, capacity, restart and
migration checks. Those fixtures supply no qualification or profitability proof.

## Owner-bound execution cost ceilings

An operator may register an immutable funding policy with an optional
`execution_model` alongside `receiver_id`, `account_id` and `policy` in
`POST /v1/allocation-policies`. The object is the versioned
[`paperfill.Model`](paper-fill-method.md#declared-assumptions), including method,
slippage, fees, per-fill commission, liquidity fraction, latency and quote age.
It declares simulation assumptions and supplies no provider evidence or admission.
This configuration is accepted at policy registration; allocation-comparison
requests still contain only retained decision IDs and pins. The existing policy
file CLI continues to register policies without a model.

Model-bearing policy records use `mantik-owned-allocation-policy-v2`; the whole
model is included in the immutable record hash. Changing it under the same policy
or account identity conflicts. Policies without a model retain their original v1
wire shape and hash.

For a model-bearing policy, each comparison derives a buy cost ceiling from the
actual retained decision, registered strategy and quote-based expiry. The limit
is the smaller of the reference price plus adversely rounded modeled slippage,
the strategy's entry threshold and its gross-notional ceiling per share. That
limit remains at or above the reference price. An execution owner must enforce
it as a buy limit; a modeled price above it must remain unfilled.

The fee ceiling assumes every whole share fills separately. For quantity `Q`,
limit `L`, fee basis points `F` and fixed commission `C`, the maximum debit is:

```text
Q * L + Q * (ceil(L * F / 10000) + C)
```

This bounds upward fee rounding and commissions for any partition into whole-share
partials at or below the limit. Quantity and arithmetic must fit the declared
paper-fill method bounds. The model is pinned even when some of its parameters do
not affect this debit ceiling. It does not normalize provider size units or
estimate real venue fills.

Schema-43 comparisons for these policies use `mantik-owned-allocation-deferral-v3` and
retain `execution_costs` with decision/model/quote pins, reference price, limit,
quantity, gross/fee/debit ceilings and original expiry. The allocation core
compares the full debit ceiling as the proposal amount, so account and strategy
limits cannot later treat entry fees as free capital. Replay reconstructs that
amount and every cost field from owner records. Self-rehashing a smaller fee or
changed model pin cannot bypass the binding.

Schema 43 fences older writers that do not retain these fields. The migration
changes policy-table metadata. Legacy policies and v1/v2 comparison records remain
replayable with unchanged hashes, alongside new model-bearing records. Rollback
requires the retained pre-upgrade backup and its matching schema-42 binary.

Admission remains unavailable: v3 comparisons still defer or reject expired
proposals, reserve no cash and create no order or effect. Actual normalized source
quotes, applicable evaluation/admission, shared execution ownership, atomic funded
ledger/effect persistence and protective recovery remain unfinished. This cost
binding does not complete #19, #20 or #21.

## Protective exit funding

Model-bearing comparisons introduced `mantik-owned-allocation-deferral-v4`. Their
execution costs also retain `protective_funding`, derived from the same actual
owner model and strategy quantity. This separates the entry order's
`max_debit_cents` from the total capital required before a future entry could be
funded. The proposal amount is `protective_funding.total_funding_cents`.

A sale can have negative net proceeds when commissions exceed its proceeds.
For the supported whole-share fill method, there can be at most one fill per
share, prices are at least one cent, and percentage fees are at most 1,000 basis
points. The maximum shortfall per share is:

```text
max(0, commission_per_fill_cents - 1 + (fee_bps > 0 ? 1 : 0))
```

Multiply this by registered quantity to obtain `exit_shortfall_ceiling_cents`;
add it to the entry maximum debit to obtain `total_funding_cents`. One-cent,
one-share partial fills attain this bound. Larger proceeds can pay their own
fees. Spread and stop gaps can worsen prices but cannot cross the method's
one-cent supported execution floor. Missing prices, an unfilled stop-limit,
expiry and unresolved delivery do not imply that an exit occurred.

The combined amount must fit the method's money bounds. Replay derives every
field from immutable owner records. A self-rehashed smaller shortfall, changed
recipe or removed protective object fails validation. Client requests still
accept only decision IDs and pins; they cannot select budgets or balances.

Schema 44 fences writers that cannot preserve this field. Prior v3 records keep
their omitted protective object, original entry-only amount and wire hashes.
Exact retries remain historical. A different comparison cannot change the
proposal amount of a decision already retained under v3; it returns a conflict
when the new budget differs. A new owner-finalized decision is required for a
new funding requirement. Mixed journals can retain v3 and v4 records for distinct
decisions. Rollback requires stopping the new writer and restoring the retained
pre-upgrade backup with its matching schema-43 binary.

This binds a funding requirement, not a reservation. Admission remains
unavailable; no cash, position, order or effect changes. A funded execution owner
must keep protective cash separate from the entry debit, preflight and reconcile
actual fills atomically, and retain recovery capacity before activation. This
change does not complete #19, #20 or #21 and supplies no qualification credit.

## Unfilled terminal reservation binding

The internal `allocation.ApplyUnfilledEntryReleases` reducer derives a release
only from a fully replayed entry book that records cancellation or expiry with
zero filled shares. The book's orders must already resolve to reservations under
the exact owner policy and fill model. All entry debits in that book must already
be present with their exact replay bindings; release cannot insert missing fills
or supply an admission. No caller release amount is accepted.

The release uses the original proposal's full reserved amount and pins the book
root, first terminal entry and order ID. A later unrelated book entry or ledger
comparison preserves its original identity and timestamp, so retries replay the
same receipt. Prior amount-only releases, alternate roots, truncated histories,
unknown effects and missing reservations fail closed. New postings must fit the
ledger's event order; a late call cannot backdate a previously unposted release.
Failures return no staged ledger or receipts and leave both inputs unchanged.

Working orders and missing quotes keep their reservation. Any partially or
fully filled order keeps its entire residual hold, including protective funding;
this reducer does not infer that position or protection obligations have ended.
It does not settle positions, release filled-order safety cash, attest market
units, qualify source evidence or write storage. The future execution owner must
select actual service-owned decisions, admissions, orders and market evidence,
and commit book and ledger changes atomically. This is a bounded #19/#20 binding
dependency, not operational authority or completion of either issue.


## Owner-bound admission readiness

The v5 comparison format uses `mantik-owned-allocation-deferral-v5` for policies with or
without an execution model. Each readiness receipt uses
`mantik-owned-admission-readiness-v2` and includes a `binding` derived during the
same locked owner append or replay:

- The comparison request identity and selected policy identity/hash.
- The selected Mantik account identity/hash.
- The exact proposal hash, including decision pin, requested funding and expiry.
- The resulting allocation ledger head.
- The derived execution-cost receipt hash when the policy has a model. This field
  is omitted when no execution-cost receipt exists.

These fields identify what the readiness check describes when inspected outside
the enclosing comparison record. They are not caller inputs. The request contract
still accepts only the request/receiver identities and retained decision pins.
The service selects the account, policy, proposal, costs and ledger from its own
validated sources; caller `binding`, account, balance, admission, effect, budget
and hash assertions are rejected. Replay reconstructs the binding from those
sources and rejects removed or substituted bindings even when both public record
hashes are recomputed. Hashes alone do not supply custody or authority.

The existing four prerequisite checks and all authority flags are unchanged.
Readiness remains deferred or expired, admissions remain empty, no money is
reserved, and no order or financial effect is created. This is a durable binding
dependency for #19, not an admission issuer or completed atomic financial
execution owner. Applicable evaluation, qualified source evidence, execution,
protection and recovery still need their own implementation and acceptance.

PostgreSQL schema 47 fences older binaries before startup. Its migration changes
the journal's descriptive table comment and schema checksum; it does not rewrite
retained records or opening funding. V1-v4 comparison records keep their exact
wire shapes, hashes and historical retries, including readiness without a
`binding` and v3 costs without protective funding. V5 may coexist with these old
records. There is no in-place downgrade: rollback to a binary supporting at most
schema 46 requires restoring a compatible retained backup into an isolated
replacement database and verifying it before activation. Do not delete version
rows or strip new fields to make an older binary accept a live journal.

Disposable integration checks cover concurrent exact retries, detached binding
readback, rehashed substitutions, old/new journal replay, failed owner writes,
restart, checksum rejection and schema-fence rollback. The migration failure
fixture verifies that the prior journal bytes and operational state survive and
that a failed version insert also rolls back the table comment. These are local
unqualified fixtures, not operational or economic qualification.


## Commit acknowledgment and failed reads

The owner store returns a comparison receipt only after its transaction commits.
A constructed record, a successful `INSERT` or `UPDATE`, or a completed replay
inside a transaction is not a committed acknowledgment. SQL write errors,
deferred constraint failures at `COMMIT`, replay errors and commit errors return
zero receipt/catalogue values alongside the error. Memory also returns a zero
catalogue when owner replay fails, so an invalid or partial projection cannot be
mistaken for a usable readback. The existing HTTP error responses are unchanged.

A deferred-constraint failure has a known rollback outcome. A transport or
connection failure during commit can have an uncertain outcome even though the
returned receipt is zero. Read the actual owner journal for the request identity
before retrying the same command; a retained exact command returns its original
receipt. An error is not proof that no record committed. This contract adds no
automatic retry, financial effect or admission authority.

Disposable PostgreSQL regressions force failures at statement and commit time
for both a new journal and an update to an existing one. They check zero returned
receipts, preserved prior journal/ledger state, and successful exact-command
retry after the rejected initial commit. The existing concurrent retry checks
remain in place. Memory and PostgreSQL also reject a damaged replay without
returning a partial catalogue. These tests use unqualified decision fixtures and
exercise the implemented deferral owner; funded admission/execution and complete
issue #19 acceptance remain unfinished.

## Capital readbacks

The existing `GET /v1/allocation-comparisons` response includes `capital`, sorted
by account ID. It derives one readback for each retained allocation policy from
the same locked Memory or repeatable-read PostgreSQL snapshot as the replayed
comparison history. An account without a registered allocation policy has no
row in this view.

Before a policy has any comparisons, `source: registered_opening_capital` derives
its balance and empty exposure lists from the registered account and policy.
Its ledger hash identifies the derived empty ledger; reading inserts no genesis
event. After comparisons, `source: replayed_deferral_journal` pins the actual
replayed ledger hash and its last recorded time. `as_of` describes that source
state, not a fresh market observation or reconciliation heartbeat.

Rows use `mantik-owned-deferral-capital-v1` and authority
`service_owned_deferral_capital_readback_only`. They retain the complete account,
policy and ledger pins, available cash, realized PnL, positions, orders and
commitments. A row hash covers its representation with `sha256` empty. Detached
readbacks cannot alter owner records, and a failed source replay returns no
partial capital catalogue.

This owner still permits only deferrals. Its recorded cash equals registered
opening cash, recorded realized PnL is zero, and exposure/commitment lists are
empty. These values describe the owner's implemented journal; they do not
assert that external holdings, effects or losses are absent. Funded ledger
state fails this version's readback contract rather than acquiring authority
through an imported projection. Execution and qualification flags remain false.
There is no balance import, capital write endpoint, admission grant or new CLI.
Issue #19's funded admission and atomic execution/ledger owner remains unfinished.

## Source aliases during owner replay

New comparisons already reject multiple finalized decision IDs derived from the
same retained strategy/provider/feed/symbol occurrence. Replay now rechecks that
same source rule for `mantik-owned-allocation-deferral-v5` records against their
prior journal prefix and within each batch. A second capture ID, altered receipt
time, recomputed ledger/record/readiness hashes, or an exact retry of a different
valid comparison cannot hide a v5 source-alias conflict. The complete read/write
fails without a partial catalogue or receipt.

The v5 writer was introduced after the append-time source-alias rule. This
replay check therefore preserves valid v5 bytes and exact historical retries.
Older v1-v4 records retain their prior replay contracts; new comparisons still
check those earlier sources when enforcing the current append rule. No public
route accepts a replacement journal or chooses an older record schema.

This is source identity validation for the existing deferral owner. It does not
assert statistical independence, qualify a source, issue an admission or create
an economic effect. No schema migration, source rewrite or balance change is
performed.


## Captured evaluation declaration context

Version 7 introduced `mantik-owned-allocation-deferral-v7`; version 8 preserves its complete declaration capture. The allocation owner
reads and validates the declaration catalogue in the same Memory aggregate lock
or PostgreSQL transaction as the finalized decisions, strategies and policies.
It retains that exact catalogue with the journal entry. Callers still supply only
comparison identity, receiver and finalized-decision pins.

The record's `evaluation_context` binds the captured catalogue hash and each
selected decision/strategy to its owner-selected policy. Matching declarations
require exact policy and strategy record pins. The sorted declaration pins are
reported as `declared_not_evaluated`; no match is `declaration_unavailable`.
A match does not establish temporal evaluation applicability, independent samples,
passing economic/statistical results, admission, execution or qualification.
All authority flags remain false and allocation continues to defer.

Replay validates captured declarations against their actual current owner
records and rejects declarations recorded after the comparison clock. Later
catalogue additions do not change earlier contexts or exact retries. For v7, replay also requires that the capture exactly matches all current owner
declarations recorded at or before the comparison clock. Omitting a plan and
recomputing every portable context/record hash fails. To keep that historical cut
stable, a new declaration must have a service clock strictly later than every
retained v7 comparison. Equal or earlier clocks fail without consuming the plan
ID; exact old declaration retries remain clock-free. PostgreSQL serializes both
owners through the existing state and receiver locks; Memory uses its aggregate
mutex. This is owner-history consistency, not independent source attestation.
Existing v1-v6 records retain their exact representation and retry behavior;
v1-v5 acquire no retrospective declaration context, and v6 retains its historical
capture-subset validation. Corrupt captured sources or
rehashed context fields fail the complete read and exact retry.

Schema 49 fences older binaries before startup. Its migration changes only the
table comment and migration checksum record; it does not rewrite prior journals
or activate an admission/effect writer. Upgrade and rollback checks run against
new disposable PostgreSQL containers. Atomic funded execution and applicable
positive evaluation/admission remain unfinished.

## Validated ledger prefix replay

The owner validates every retained comparison command against its actual policy
and finalized decision sources, then reconstructs each policy ledger in one
validated walk. Every entry hash, chain link, derived plan, temporal transition
and conservation check still runs. Entry time must exactly match its owner
comparison time, even if a mismatched entry and its readiness are self-rehashed.
The walk returns each prefix's ledger hash
and detached accounting projection, so the owner can verify every historical
comparison receipt without replaying all earlier transitions for each entry.

An invalid later entry discards all prefix results. Source validation remains
separate from accounting validation: the internal core cannot attest decision,
admission or effect ownership, and this read path grants no financial authority.
Historical wire bytes, schema versions, hashes and clock-free retries are
unchanged. This does not implement issue #19's admission/effect issuer or an
atomic durable financial execution transaction.

Before comparison creation, catalogue reads and exact retries, the service now
replays any retained [evaluation assessments](paper-evaluation-assessments.md)
against actual declaration, decision and outcome owners in the same transaction.
A missing outcome evaluator or corrupt assessment prevents a receipt; even a
fully rehashed assertion cannot bypass owner replay. Valid later assessments do
not rewrite an earlier comparison. This prerequisite does not grant admission
or add assessment pins to the retained comparison context.

## Retained assessment context

Version 8 introduced `mantik-owned-allocation-deferral-v8` with
`mantik-owned-allocation-evaluation-context-v2`. The owner resolves and replays
its actual assessment catalogue once inside the existing Memory lock or
PostgreSQL transaction, before returning a new comparison, catalogue, or exact
retry. Each decision binding retains assessment ID/hash, declaration ID/hash,
and the actual assessment status for declarations matching its strategy and
owner-selected policy. The context also retains a hash of the complete sorted
assessment ID/hash cut at the comparison clock, including assessments outside
that decision's scope. Full assessment evidence remains in its original owner.

A matching assessment sets the binding status to `assessed_not_admitted`.
Individual pins preserve `pending`, `incomplete`, or `blocked`; none proves full
evaluation applicability or an admission. The evaluation-recipe readiness gate
remains unavailable. Admission, execution and qualification flags stay false.
The public command still accepts only comparison identity, receiver and actual
finalized-decision pins.

Replay derives the context again from actual owner assessments at or before the
retained comparison clock. Omitted pins, changed statuses, scope mismatches and
fabricated catalogue hashes fail even after the comparison is rehashed. A new
comparison cannot predate any current assessment. A new assessment must be
strictly later than every retained v8 comparison, under the same owner locks;
equal or earlier issuance fails without consuming its ID. Exact assessment and
comparison retries retain their original bytes without reading a new clock.
Later legitimate assessments remain outside historical cuts.

Attribution projects the retained binding and assessment-source hash as
`mantik-paper-attribution-evaluation-context-v2`. It does not substitute the
latest assessment for the historical comparison. Existing v1-v7 comparison
receipts retain their representation and retry behavior; older context fields
remain omitted. Schema 51 fences older writers before startup. Its migration
changes a table comment and checksum marker only, with disposable PostgreSQL
rollback and checksum checks; existing journals are not rewritten. Recovery
requires the matching binary and backup rather than deleting a schema marker.


## Exact decision evidence in readiness

New `mantik-owned-allocation-deferral-v9` comparisons retain
`mantik-owned-admission-readiness-v3`. Readiness binds the hash of its exact
owner-derived decision/strategy/policy evaluation binding. For each matching
declaration, the owner selects the latest actual assessment at the comparison
clock; equal clocks choose the lexically greatest assessment ID. The retained
selection includes assessment and declaration pins, assessment time, evidence
hash, the exact decision row's selection status, and that strategy's necessary
sample-check status. Assessments remain replayed once in the same transaction.

A shared strategy or policy does not establish decision membership. An
assessment issued before the decision reports `decision_not_retained`; an
out-of-window, unavailable, synthetic or duplicate row preserves its actual
exclusion status. These cases leave the recipe gate unavailable. An exact
`selected_research_evidence` row can expose a pending outcome window or a proven
necessary sample failure. Otherwise the recipe remains incomplete/unavailable.
No branch reports a passing full recipe, grants admission, or creates an effect.
Expiry, unqualified-source and execution-reconciliation gates remain in force.

The owner reconstructs these fields during every read and exact retry. Rehashed
row status, sample status, evidence hash, selection omission, binding hash or
invented authority flags fail closed. Historical v1-v8 receipts keep their old
wire shapes and hashes. Schema 52 adds a comment/checksum startup fence so older
writers cannot bypass v9 declaration and assessment clock cuts. It does not
rewrite journals. Attribution projects retained readiness; CLI shape validation
is not independent source attestation or operational authority.

New v10 comparison records retain v4 admission readiness with the exact failed
checks of each latest owner-replayed assessment for the selected variant. For
selected decision evidence, any retained necessary failure dominates pending
sample status; absence of failures still grants nothing. The review consumes
that readiness and preserves expiry precedence. Historical v9/v3 records retain
their original bytes and clock-free retries. Schema 62 fences older writers
without rewriting payloads; rollback requires the matching backup and binary.
