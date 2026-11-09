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
The owned record pins that entry, ledger head and policy. New records have schema
`mantik-owned-allocation-deferral-v14`, authority
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

Retention stops at 256 active records or 64 MiB without evicting evidence
(32 records and 8 MiB before [paper entry execution](#paper-entry-execution));
[checkpoints](#checkpoints-and-the-archive) seal older records into an archive.
Exact retries remain available at capacity. This bounded deferral journal is not an
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

Historical `mantik-owned-allocation-deferral-v9` comparisons retain
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

## Finalized strategy execution requirements

New v11 comparisons with an execution model also retain `execution_safety`, one
`mantik-owned-execution-safety-v1` record per proposal. The owner derives it from
the same validated policy, finalized decision, registered strategy and cost
ceiling used by the comparison. It pins the comparison, decision, strategy,
definition, basis, policy, account, model and cost hashes, exact quote input,
finalization clock, proposal validity, quantity and entry limit. Its `safety`
contains the registered stop, target, notional, holding-time and quote-age limits.
Policies without an execution model have no execution requirements record.

These are requirements for a future financial owner, not placed orders or
asserted effects. Holding time starts with an actual fill; this record invents
neither a fill clock nor a holding deadline. Admission, execution and
qualification flags remain false, with authority
`service_owned_execution_requirements_only`. Unknown market size units remain
unverified and cannot supply whole-share liquidity.

Append and replay reconstruct every requirement from actual owner records in the
existing aggregate lock/transaction. The comparison payload and ledger binding
commit together. Rehashing a changed stop, target, holding time, quote age, source
pin or authority flag cannot manufacture a matching owner record. Attribution
projects the retained per-decision record, and the CLI checks its hash and cross
pins; portable validation is not independent source attestation.

Historical v1-v10 comparisons keep their original omitted field, hashes and
clock-free retries, including PostgreSQL reopen. Schema 63 adds a comment and
checksum writer fence without rewriting journal payloads. Rollback requires the
matching verified backup and binary. Applicable admission, atomic order/fill and
ledger effects, protection and recovery remain unfinished for issues #19/#20.

### Safety requirements in admission readiness

New v12 comparisons issue readiness v5. Its binding adds
`execution_safety_sha256`, derived from the exact proposal's owner-replayed
execution-safety record alongside the existing account, policy, cost, evaluation
and ledger pins. Requirements are reconstructed before readiness in the same
owner lock or transaction. A policy without an execution model has no safety
record or safety pin. Callers supply neither the record nor its hash.

Owner reads and retries reject omitted or substituted pins even after portable
hashes are recomputed. Retained admission reviews carry that exact readiness;
attribution validates the projected pin before rendering. Earlier v1-v11
comparisons preserve their original shapes, hashes and clock-free retries.
Schema 64 adds a checksum-protected writer fence without rewriting journals.

This binds requirements to the admission context. It issues no admission, order,
fill, holding deadline, economic effect or permission to write balances. Positive
evaluation, execution reconciliation and atomic operational ledger ownership
remain separate unfinished gates.

A targeted `GET /v1/allocation-comparisons/{id}` returns the exact retained
comparison after native owner replay. Indexed archived comparisons replay only
their original sealed segment and compare its resulting checkpoint with the
retained original. Active comparisons replay the active journal after checking
its checkpoint against the indexed original. Legacy unindexed segments use the
complete-history reader. The route requires authentication, accepts no
query parameters or GET body, returns 404 for a missing ID, and preserves owner
unavailability errors. The supervision `inspect_record` tool exposes this read
as `allocation_comparison`, alongside `admission_review` and
`decision_attribution`; none is an economic writer.

## Execution-model quote-age expiry

New v13 comparison records bound proposal validity by the shortest of the
strategy quote-age limit, the existing 60-second owner cap, and the actual
owner-selected execution model's `max_quote_age_seconds`. Every deadline starts
at the retained decision quote's occurrence time, never at comparison or retry
time. A stricter model shortens validity; a looser model cannot extend it.
Model-free policies retain the strategy/owner-cap rule.

The proposal, debit ceiling, execution-safety requirements and admission
readiness carry the same resulting expiry. At the exact deadline a new
comparison and its readiness are expired. Earlier comparisons remain historical,
and their exact retries do not sample a clock or gain a new deadline. This is
proposal validity, not the lifetime of a granted admission or an actual order.

Schema 65 fences older writers without rewriting existing journals. v1-v12
records retain their original derivation, bytes and retries, including their
historical quote-age behavior. Readback never treats such a historical record as
new execution authority. Positive admission, account reservations, financial
effects and atomic execution remain unfinished; this correction supplies no
grant, fill, balance change or qualification credit.


## Known failures for excluded candidates

New v14 comparisons issue readiness v6. When the exact finalized candidate is
retained by its pinned assessment as `source_unavailable_at_decision`, a failed
check for that strategy still makes the evaluation gate `failed`. Excluding a
candidate from scored research evidence does not erase the known failure.
The admission review records `blocked` while the proposal is current; its
existing expiry rule still takes precedence at the deadline.

An absent decision, abstention, synthetic exclusion, or decision/quote outside
the declared window cannot inherit this failure merely by sharing a strategy
or policy. The owner still replays the actual assessment and complete source
cut before binding it; caller-supplied checks or hashes are not evidence.

Schema 66 fences older writers without rewriting existing journals. Historical
v1-v13 comparisons retain their original readiness, hashes and clock-free
retries. This change adds no positive admission, retirement, reservation,
order, fill, capital posting, provider request or qualification credit.

## Owner-issued paper eligibility

Comparison v15 keeps the `mantik-owned-allocation-deferral` family name, so
every earlier version rule still applies, but its authority is
`service_owned_paper_allocation_reservation_only`. It is the first comparison
that can reserve cash.

### Admissions

For each distinct proposed strategy, the owner derives at most one
`allocation.Admission` from records it has already replayed in the same
transaction. Callers still select only decisions.

- A declaration counts when it names this policy and policy hash, lists the
  exact registered strategy record, and was declared by the comparison clock.
- Only each declaration's latest assessment at the clock counts. If any is
  `blocked`, the strategy gets no admission, so a later failure withdraws an
  earlier grant.
- A latest assessment grants when it is `complete` under
  `native-predeclared-holdout-sign-test-v15`, lists the strategy in
  `complete_strategy_ids`, and carries no admission, execution or
  qualification flag.
- The admission's evidence is that assessment's hash. It is valid from the
  assessment clock for the declaration's `admission_ttl_seconds` (at most one
  day). The latest complete assessment wins; equal clocks use the greater ID.
- A policy without an execution model has no protective funding and gets no
  admission.

The allocation core then compares proposals as before. An admitted proposal
that fits cash, capital and every strategy, symbol and group limit gets
`reserve_plan`; the ledger reserves its protective funding and records a
commitment. Competing proposals are ordered by the policy's priority, then ID,
in one all-or-nothing comparison.

### Expiry

A reservation is held for order placement while its proposal is valid: at
most 60 seconds after the quote, or the execution model's tighter quote age.
At the next comparison on or after that time, the owner first appends one
`release` effect per expired, reserved, not-unknown remainder, in ledger
order. These are retained in the record's `expirations`, applied before
`entry`, so released cash can fund the new proposals. Each release ID and
hash derive from the reservation, so a release happens once. No execution
owner records fills yet, so every reservation ends in a release.

### Readiness, reviews and capital

Readiness v7 adds `evaluation_complete` and `paper_operationally_eligible`.
Eligibility needs an admission valid at the comparison clock, an unexpired
proposal, bound execution safety, and an `evaluation_recipe` gate that a
complete evaluation turned `verified`. A failed or pending gate from any bound
declaration wins. An eligible proposal has status `eligible`, trigger
`admission_expiry` and `admission_available: true`.
`execution_reconciliation` stays `unavailable`, and `execution_available` and
`qualification_credit` stay false. Eligibility does not need the natural
qualification window.

[Admission review](owned-admission-reviews.md) v2 reviews readiness v7. A
current eligible proposal reviews as `eligible` with
`owner_issued_paper_eligibility`. Expiry, failed and pending gates keep their
earlier precedence.

A ledger that holds reservations reads back as capital v2
(`mantik-owned-allocation-capital-v2`) with its reservations. Cash plus
reserved amounts must equal opening cash, with no fills, positions or realized
result. A ledger without reservations keeps the v1 readback.

### Replay

Replay rederives each v15 record's admissions from its captured declarations
and the owner-replayed assessments at its clock, and its releases from the
ledger state before it. A forged or missing admission, or a dropped or altered
release, fails replay. Exact retries return the retained record without a
clock. Schema 88 fences earlier writers and rewrites no retained record; v1-v14
comparisons keep their reservation-free invariant and hashes.

## Paper entry execution

Execution is opt-in: `serve` enables it when `MANTIK_PAPER_EXECUTION=on`.
Once the journal holds a v16 record, allocation replay needs it and fails
closed with `owned paper execution is not configured` without it. With it,
new comparisons are v16 (`mantik-owned-allocation-deferral-v16`). Without
it, comparisons stay v15, and so does everything above.

### Entry books

Each v16 comparison opens one entry book per registration and strategy quote
scope (symbol, feed, evidence class, quote age). Each reserved proposal adds a
buy limit order:

- order ID equal to the proposal ID;
- quantity, limit and debit ceiling from the record's bound execution cost;
- placed at the comparison clock, expiring at the cost's validity;
- mode `ongoing_paper`, under the policy's execution model.

Orders reserved by different comparisons get separate books, but they share
displayed liquidity. The owner advances books in reservation order (record
order, then book key). Before a later book applies a quote, its displayed bid
and ask shrink by the shares earlier books filled at the same occurrence
(registration, symbol and occurrence clock). The reduced size gives the book
exactly the modeled capacity that remains, or zero. An earlier book counts
even after it settles, as long as its orders overlapped the later book's.
Replay reduces the same quotes in the same order. Schema 92 fences writers
whose books do not share liquidity.

### Advancing books

Every later record, comparison or step, first advances every open book to its
own clock. A book receives the registration's retained quotes that became
available after placement and by that clock, in availability order, plus an
expiry timer at each order's expiry. Timers run before quotes at the same
clock, and nothing is applied after the last order expires. A quote counts only
with documented share sizes, the strategy's symbol and evidence class, a
fill-method range, an age within the strategy's limit at availability, and open
regular-session evidence at that clock. This is the same filter the
evaluation's protective replay applies, and captures are validated against
owner custody as the outcome market inspector validates them.

The existing ledger reducers turn each advanced book into effects:
`ApplyEntryFillBook` posts fills, and `ApplyUnfilledEntryReleases` releases an
order that ended cancelled or expired with no fill. A filled or partly filled
order keeps its whole remaining hold, including protective funding. Effects
from all books are merged in clock order and retained in the record's
`executions`, grouped by policy, before its `expirations` and `entry`. The
interim expiry rule above no longer releases proposals that a book owns.

### Protective exits

Each entry order carries its decision's stop, target and holding limit. A
filled position triggers its exit at the first of:

- a quote that occurred after the first fill, within the strategy's quote age,
  whose bid is at or below the stop or at or above the target;
- its holding deadline, the first fill plus the holding limit, run as a timer.

A trigger cancels the order's unfilled remainder. Once every order in the book
is terminal, the next quote after both the trigger and the last entry event
places one market sell for the filled quantity, expiring 24 hours later, in
its own exit book. Exit books take later quotes from the bid side, after entry
books, and share displayed bid size the same way entry books share ask size.
At one clock, entry expiries run first, then holding deadlines, then exit
expiries.

`ApplyExitFillBook` posts each exit fill as a `close` effect with its cost
basis and net proceeds, and `ApplyClosedPositionReleases` releases the
reservation's unspent notional once the position is fully closed. Both bind
the entry book's final hash, so they run only after the entry book is frozen.
An exit that expires with shares left is replaced: the first eligible quote
after it ended places another market sell for the rest, in its own book,
expiring a day later. This repeats until the position is closed, so a
weekend or a thin market delays the exit but cannot end it. A position's
exit books are applied together, in order, by `ApplyExitFillBooks` and
`ApplyClosedPositionReleaseBooks`. A replacement may only be placed after the
book before it ended and may only sell what is left; cost basis continues
across the books. A book is finished when its entry orders are terminal and
every position is closed. Schema 95 fences writers whose books do not exit,
and schema 97 fences writers that do not replace an expired exit. A journal
holding an exit that expired with shares left under an earlier writer fails
replay.

### Position status

A new step's response lists every open position in `positions`, ordered by
policy and order ID, with `policy_id`, `order_id`, `status` and `since`:

| Status | Meaning | `since` |
| --- | --- | --- |
| `holding` | Filled; nothing has triggered. `deadline` is the holding deadline. | first fill |
| `exit_pending` | Triggered, or the last exit ended with shares left; the next eligible quote places an exit. | trigger, or when the last exit ended |
| `exit_working` | The exit order is placed and working. | exit placement |
| `exit_unfilled` | The ledger holds an open position that no open book tracks. Replacement exits mean this should not happen; it marks an inconsistency. | the position's last ledger effect |

A closed position is not listed, and an exact retry lists nothing because it
does not advance. Missing or stale quotes never produce a fill: a position
whose exit triggered, or whose exit expired, while no eligible quote arrives
stays `exit_pending`, for example overnight after a holding deadline.

### Execution steps

`POST /v1/paper-executions` takes `{"id","receiver_id"}` and advances every
open book to the service clock. The caller names only the step identity.
A step that would commit an effect is retained as
`mantik-owned-execution-step-v1` and returns 201. One with nothing to commit
returns 200 with status `no_effects`, retains nothing and leaves its identity
unused. An exact retry returns the retained step with 200. Steps appear in
the allocation catalogue's `executions`; comparison readers never see them.

### Execution worker

With execution on, `serve` also runs an execution worker. It issues a step
immediately and then every 15 seconds, so fills, protective exits and
releases do not wait for a comparison or a caller. Its step identities start
with `automatic-execution-`; that prefix is reserved, and the route rejects
it with 422.

The worker keeps a step identity until a response settles it. After an
error, including one returned after the step committed, the next cycle
retries the same identity and receives the retained step instead of a second
one. A step with no effects retains nothing, so its identity is dropped. A
restart starts with a fresh identity, which still cannot repeat an effect:
effects come only from owner records, and the books already hold every
committed one.

A step that lists an `exit_unfilled` position still settles its identity, but
the worker reports the cycle as failed with code `protective_exit_unfilled`
and the evidence reference `paper-position:<policy>/<order>`. The scope stays
not ready, with an open `worker_protective_exit_unfilled` exception, on every
cycle while the position is open. `exit_pending` is expected while quotes are
absent and is not an exception.

Progress, failures and clean stop are reported under the `paper_execution`
readiness scope. Inspect `GET /v1/readiness/paper_execution` or
`mantik readiness --scope paper_execution`. Without an observed worker the
scope is not configured; a stopped, stale or failed worker is not ready.
The scope measures stepping health only and grants no qualification. Schema
96 fences writers that reject this worker's health reports.

### Cost ledgers

A policy ledger whose first record is a v16 comparison is a cost ledger
(`mantik-allocation-ledger-core-v2`). A paper exit whose fees exceed its sale
proceeds can then close against available cash instead of being refused. A
ledger begun by an earlier comparison keeps its schema and hashes, even after
v16 records join it, so a negative-proceeds exit cannot close there. Schema 91
fences writers that start v16 ledgers without the cost schema. Any v16 journal
written before that fence fails replay.

### Capital and replay

A ledger holding a v16 record reads back as capital v3
(`mantik-owned-allocation-capital-v3`). For each reservation, held, filled
and released amounts sum to its notional. Cash plus every held and filled
amount, less closed cost, equals opening cash plus the realized result.
Positions are the reservations with filled cost not yet closed. No owner
records unknown outcomes yet, so unknown effects must still be absent. A
sealed ledger whose closed reservations left the base still reads back as v3.

Replay rederives every record's execution effects from the ledgers as they
stood before it and from the owner's quote selection, so a fabricated quote
path, a rehashed amount or a dropped effect fails replay. A v1-v15
comparison written after a book opened fails replay too, because it left
open books unadvanced. Schema 90 fences earlier writers.

The active journal holds at most 256 records, comparisons and steps
together, in at most 64 MiB; checkpoints below keep it far smaller. Unknown
outcomes are later work.

## Checkpoints and the archive

Every append replays the active journal, about 1 ms per record per replay on
Kvant. Once the active journal holds 64 records and every book has finished,
the next append first seals all of them into a checkpoint
(`mantik-owned-allocation-checkpoint-v1`), retained in the journal's
`checkpoint`. The sealed records move to `mantik_owned_allocation_archive` in
the same transaction, and the active journal starts empty. Append cost then
depends on the records since the last checkpoint, not on history. On Kvant on
2026-11-08, with other CI jobs running, one append with 32 records active took
65 ms after 160 records and 74 ms after 1056
(`MANTIK_ALLOCATION_APPEND_BENCH=1 go test ./internal/storage -run
TestOwnedAllocationAppendCostIsBounded -v`). The remaining growth comes from
the sealed alias index and consumed identities.

A checkpoint holds the replay state after its records:

- each policy ledger, sealed by `allocation.SealSettledLedger`. A sealed
  ledger replays from its base (snapshot, live reservations and their
  proposals, consumed evidence and command identities) and continues the same
  entry hash chain. A settled reservation (nothing held, no unknown outcome,
  no open position) leaves the base with its commitment and proposal, so a
  long-lived ledger stays under the allocation core's 256-commitment limit;
- the policies that hold grant or v16 records, and the proposals entry books
  own, so capital readback and the interim expiry rule behave as before;
- the proposal alias index;
- `sealed_sha256`, a hash chained over every sealed record in order, and the
  sequence of every checkpoint so far.

Routine replay starts from the checkpoint and trusts it. Records appended
after it bind the sealed ledger hashes, so a forged sealed ledger fails replay
at once, but a forged alias index would not until the audit. Readers that list
records (`GET /v1/allocation-comparisons`, admission reviews, attribution)
load the archive and check it against the chain, so a changed archived record
fails every read. An exact retry of a sealed comparison or step returns it
from the archive, and its identity stays unusable.

Allocation reads and writes check the complete active decision set against its
actual creation and retirement intervals. Decisions referenced by the journal
or a new comparison can also be resolved from their first original paper
archive after approved retirement. Each private dependency replays its original
native decision, strategy and source custody; its original basis still supplies
quote expiry, funding, readiness and economic-source alias checks. These
dependencies do not appear in the public active decision catalogue. An active
row missing without approved retirement fails, even if an archive copy exists.
Exact allocation retries retain their original receipts and do not read a new
clock. The full-history audit also resolves decisions referenced by sealed
segments before re-executing them.

This reader support does not enable paper-record retirement. Attribution,
admission reviews, archive writers, unfinished dependencies and automatic
retention still need their remaining consumer and retirement contracts. Full
history reads and audits retain their existing archive-loading costs.

`GET /v1/allocation-comparisons/audit` re-executes the whole history against
current owner records, one sealed segment at a time, rebuilding each
checkpoint from its own replay. The last rebuilt checkpoint must equal the
retained one, and the listed records, steps and ledgers must match. It returns
`verified` with the record and sealed counts, or 409 on a mismatch. Schema 93
adds the archive table and fences earlier writers.

After a checkpoint, capital readback lists only live reservations; the
records keep the full history. A policy can hold at most 256 live
reservations at once. Proposal identity across checkpoints is kept by the
proposal alias index, not by the sealed ledger. Schema 94 fences writers that
seal settled reservations into the base.

Schema 109 additionally retains each new seal's original checkpoint, complete
segment membership and actual decision pins in
`mantik_owned_allocation_checkpoints`, `mantik_owned_allocation_batches`,
`mantik_owned_allocation_members` and `mantik_owned_allocation_decision_refs`.
Checkpoint/index rows are immutable and commit in the same owner transaction
as the original archived records and new active journal. A deferred guard
rejects a checkpoint advance without its complete index, including from an
already-running predecessor. Replacing or removing a retained checkpoint is
also rejected. Failed inserts or COMMITs return no receipt or partial index.

The private segment reader loads at most 256 original records in 64 MiB,
their exact pins and the two boundary checkpoints. It checks complete ordered
membership and the original sealed chain, then native replay rebuilds the end
checkpoint from the actual owner inputs. An envelope or membership hash alone
does not establish that replay. Selected reads do not reconstruct later
segments. Existing unindexed segments explicitly report that absence; their
original records and full-history audit remain available.

Targeted comparison inspection uses this reader for indexed segments, including
the supervision adapter. It resolves at most 256 distinct decision dependencies
from active owners or their approved original archives. Missing indexed payloads,
changed membership and a mismatched native end checkpoint fail the selected
read. Damage in a later, unrelated segment does not prevent inspection of an
earlier intact segment; complete-catalogue reads and audits still check the
complete history. These reads use the existing Memory lock or PostgreSQL
repeatable-read transaction and return no record after cancellation or a failed
COMMIT.

Historical attribution and catalogue paging still need segment integration.
The v1 checkpoint's alias and consumed-identity state still grows; this reader
does not establish a growing-history operational bound or enable physical paper
retirement. Backups must retain all four index tables
with the original journal/archive and owner sources. A schema-108 predecessor
refuses schema 109; rollback requires the matching pre-upgrade backup and binary.

### Sealed allocation-history pages

`GET /v1/allocation-history` and `mantik allocation-history` inspect original
sealed comparisons and standalone execution steps in their committed sequence.
Each comparison retains its original allocation, readiness and execution
context; each execution step retains its original fill, close and release
effects. These records grant no new financial authority or qualification.

Pass the service receiver as `receiver_id` or `--receiver-id`. The page size is
`limit` or `--limit`, defaults to 16, and accepts 1 through 64. Supply the returned
`next_after` as `after` or `--after` to continue. The HTTP route requires operator
authentication and accepts no GET body, duplicate parameters or unknown query
keys. The CLI emits the validated JSON page.

The first page closes at the active journal's original sealed checkpoint.
`through_sequence` and `checkpoint_sha256` bind that cut. Later pages keep it
through appends, new seals and approved decision retirement. Active journal
records are outside this scope; a journal without a seal returns an empty page.
Missing original checkpoints, absent index members, incomplete original segments
and changed native results fail without returning a partial page. Older
unindexed history needs its original-index backfill before this endpoint can
page it; existing targeted comparison inspection retains its legacy fallback.

The service seeks at most the requested number of sequence-index members,
checks their complete original segments, resolves their native decision
dependencies and rebuilds each segment's end checkpoint. Original segment data
for one page is capped at 64 MiB, and the returned JSON page at 8 MiB. Memory uses
one lock; PostgreSQL uses one repeatable-read snapshot and returns the page only
after COMMIT. Cancellation returns no page. A selected intact segment can be
read independently of damage in a later unrelated segment; the full audit still
checks the complete history.

These pages provide original financial records for inspection. Decision-specific
financial attribution still needs complete execution-effect references and a
bounded native contribution reader. The v1 checkpoint identity collections,
legacy-index backfill and automatic retention remain required archive work.

