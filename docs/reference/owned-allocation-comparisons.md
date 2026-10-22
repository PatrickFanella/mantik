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

The proposal amount is the retained price multiplied by the registered quantity,
within the strategy's safety cap. Expiry is the actual retained quote occurrence
plus the smaller of 60 seconds and the strategy's maximum quote age. Neither
comparison time nor a retry extends that expiry. This amount has no execution
authority: a future execution binding must reserve the full cost including fees
and preserve protective/recovery capacity before it can activate reservations.

## Admission, retry and replay

The service currently supplies an empty admission set. An otherwise applicable
proposal records `defer` with `admission_unavailable` and
`admission_reassessment`; an expired one records `decision_expired` and requires
a new finalized decision. No caller-supplied admission, balance, snapshot, effect,
notional or proposal is accepted. Cash remains at the owner's opening funding,
and positions, orders and commitments remain empty.

Each comparison appends one hash-linked entry to its policy's allocation ledger.
The owned record pins that entry, ledger head and policy. It has schema
`mantik-owned-allocation-deferral-v1`, authority
`service_owned_allocation_deferral_only`, and false execution/qualification
flags. Returned core ledger values remain accounting evidence; they are not
portable admission or execution grants.

An exact command retry returns HTTP 200 and the original record without reading
a new clock. A new record returns 201. Reusing an ID with different pins returns
409. A new comparison ID records reconsideration under current owner policies;
it never overwrites an old comparison. Captured policy catalogues remain bound
to current immutable owner records, so a later policy cannot rewrite an earlier
assignment. Reads and retries validate the whole journal before selecting a
record; a corrupt unrelated entry also fails the read.

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

The migration is additive and preserves accounts, policies, strategies,
decisions and the fixture state. Older schema-38 writers reject schema 39.
Rollback requires stopping the new writer and restoring the retained pre-upgrade
backup with its matching binary; do not delete schema rows or tables in service.

Verification uses the authenticated handler with manufactured calendar/price
inputs, concurrent Memory/PostgreSQL owner contracts, SQL failure rollback,
self-rehashed corruption rejection, historical expiry, capacity, restart and
migration checks. Those fixtures supply no qualification or profitability proof.
