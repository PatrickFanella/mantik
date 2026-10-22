# Service-owned allocation comparisons

`POST /v1/allocation-comparisons` compares retained native paper entry proposals
under an owner-selected funding policy. `GET /v1/allocation-comparisons` replays
the full retained journal and returns its records and per-policy ledgers.
Both routes use the existing bearer authentication.

This is the first durable service binding for the allocation core. It records
admission deferrals and expired-decision rejections. It cannot reserve cash,
create orders, apply fills or qualify a strategy. Issue #19 remains open for
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
`mantik-owned-allocation-deferral-v2`, authority
`service_owned_allocation_deferral_only`, and false execution/qualification
flags. Returned core ledger values remain accounting evidence; they are not
portable admission or execution grants.

An exact command retry returns HTTP 200 and the original record without reading
a new clock. A new record returns 201. Reusing an ID with different pins returns
409. A new comparison ID records reconsideration under current owner policies;
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

New model-bearing comparisons use `mantik-owned-allocation-deferral-v4`. Their
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
