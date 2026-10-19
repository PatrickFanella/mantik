# Fixture capital contract

`internal/fixturecapital` defines an internal capital reducer for the existing
synthetic action worker. It consumes finalized `paper-action-v1` intents and
fixture effect lookups, not rehearsal commands, model output or an imported
capital snapshot. An explicit queued-memory binding now connects it to the
worker's transaction path. PostgreSQL schema 29 adds a durable equivalent with
checkpoint replay and source validation. Public API/CLI binding remains
unfinished; existing pilots have not been upgraded or bound.

## Authority and transitions

`New` accepts a validated immutable paper account. A book has private fields;
`Snapshot` returns a detached accounting readback that cannot restore spending
authority. Internal checkpoint replay requires the owning action/effect ledger.
The first commitment pins the
fixture case and strategy version. This unit supports the existing registered
synthetic DEMO methods and one unresolved intent at a time, not competing strategy
allocation or general portfolio exposure.

`Reserve` checks the actual action claim and lease generation, stable action ID,
registered strategy, finalized decision, required evaluations, safety, current
quote and position applicability through the existing action/operations contracts.
An entry moves its observed-quote notional from available cash to reserved cash,
within both available funds and the account's capital ceiling. Repeating the same
intent never reserves twice; changed content under its identity conflicts.

A protective exit requires this book's funded entry to match the service position.
It retains a zero-cash commitment, so zero available cash does not block closing
the funded position. No alternative strategy, instrument or execution model is
selected from the caller's narrative.

`Reconcile` calls an effect reader supplied by the owning store. A matching known
result is validated against a detached pre-ack action/case using the existing
`actions.Settle` contract. The capital calculation does not mutate that source.

- Accepted, partial and unknown outcomes keep the full entry commitment reserved.
  No partial position or partial debit is invented from an incomplete result.
- A completed entry moves reserved cash to a funded position at cost.
- A completed exit credits validated proceeds, clears the funded position and
  records the fixture's realized PnL. It cannot credit the same effect twice.
- Known rejected/expired effects release only their remaining entry reservation.
- A nil lookup means definitive absence from the local fixture effect ledger
  under the same lock as submission. Capital is released only if current
  authority also rejects or expires the action. Valid but absent unknown work
  stays reserved for retry. Lookup errors retain the original book unchanged.
- A previously known accepted/partial effect disappearing is a conflict. A known
  completed effect that occurred under valid original authority is settled even
  when acknowledgment happens after that decision's expiry.

An effect reader is an ownership boundary, not self-authenticating evidence.
Timeouts, missing provider responses and caller-supplied absence assertions cannot
implement it for a real venue. The queued-memory bridge supplies the existing
fixture effect lookup under its aggregate lock. PostgreSQL uses the same
transition path inside its aggregate transaction; the reducer performs no networking or
independent database authentication.

## Conservation and limits

Each successful transition preserves:

```text
available cash + reserved cash + position cost = opening cash + realized PnL
reserved cash + position cost <= capital ceiling
```

Position cost is entry cost, not marked-to-market exposure. Commitments are
bounded to the existing action-history limit of 256. Quote prices and quantities
remain bounded by the existing fixture execution contract. Account limits are
not increased when a fixture trade profits. Readbacks sort commitments by action
ID and include intent/effect digests for targeted comparison.

The snapshot authority is `synthetic_fixture_capital_contract_only`, with
`service_integrated: false` and `qualification_credit: false`. Observed-quote,
zero-cost synthetic outcomes cannot establish profitability, realistic paper
execution or qualified strategy admission. Historical/prospective and live
admission must use their own evidence gates.

## Queued-memory worker binding

`Memory.BindFixtureCapital` requires the receiver ID, account ID, exact account
digest and `bind_synthetic_fixture_capital_v1` scope. It accepts only a pristine
queued synthetic DEMO fixture: no observations, actions, effects or entry/exit
may already exist. The account is immutable. An exact binding retry reads the
current capital; it cannot reset it. Switching accounts conflicts. Synchronous
memory ingestion cannot bind, because it bypasses the action worker.

Binding does not read or consume synthetic ledger rehearsal commands. It creates
a new private book from the account's opening funding. Each subsequent action
transaction checks that the account and receiver still match the binding.

- `BeginAction` reserves capital and records the write-ahead submission intent
  together. Insufficient cash or ceiling retains a terminal `rejected` action
  with code `fixture_capital_limit` and an audit event, without writing an effect.
- `SubmitAction` requires an existing live commitment before writing an effect.
  Submission retains the reservation and leaves the case unacknowledged.
- `ReconcileAction` calculates capital from the pre-ack action and locked effect
  lookup, then commits capital, action, case and audit changes together. A failed
  update discards all of these calculated changes; an effect committed earlier
  remains available for recovery.
- Proven-absent valid work retains its reservation for retry. If authority
  subsequently expires or changes, either reconciliation or the next begin
  releases that reservation while terminating the action.
- Replacement lease generations reuse the same commitment. An unknown action
  without a commitment fails rather than acquiring capital retroactively.
  Terminal acknowledgment retries check the retained effect and outcome.

`Memory.FixtureCapital` returns a detached envelope with `worker_bound: true`,
`durable: false` and `qualification_credit: false`. Its nested capital snapshot
retains the reducer's `synthetic_fixture_capital_contract_only` authority and
`service_integrated: false`; it is not a declaration of a deployed, persistent
service balance. No HTTP or CLI command exposes this in-memory opt-in. Restarting
the process loses the in-memory account, actions and capital together.

## Durable store binding and restore

`Postgres.BindFixtureCapital` applies the same explicit, pristine binding
contract. Schema 29 adds `mantik_fixture_capital`, which references the account
and bootstrap aggregate. The aggregate retains an immutable receiver/account
pin. A database trigger prevents a writer from changing or discarding that pin;
startup also refuses unsupported newer schemas. No existing actions or positions
are assigned an account during migration.

The book records only transitions that change its capital snapshot: reserve,
hold, release, completed entry and completed exit. It retains each transition's
minimal pre-ack action/case/strategy, claim, clock, observed effect and result
digest. Unchanged polling and exact retries do not grow the history. At most
1,024 transitions and 4 MiB of serialized checkpoint material are accepted;
PostgreSQL also limits the stored JSON representation to 4 MiB. Capacity failures
abort the transaction. There is no compaction or unbounded fallback.

Every bound action transaction reads the checkpoint under the aggregate lock,
replays it from immutable opening funding, and compares the rebuilt result and
history digests. It then checks the owning store's current immutable intents,
effects, acknowledgment outcomes and case entry/exit. A completed effect that is
retained but unacknowledged may exist alongside reserved capital; it is credited
only by acknowledgment. Accepted/partial effects remain unresolved. A missing
checkpoint, changed intent, mismatched acknowledgment, missing known effect or
corrupt history fails both capital reads and action mutations.

Capital readback uses a single repeatable-read transaction for the account,
aggregate, checkpoint and effects. Queued memory follows the same replay/source
checks. Historic claim metadata in a checkpoint verifies the recorded transition;
it does not grant a current worker lease. Delivery still needs a fresh fenced
claim, and operations still independently rechecks current applicability.

The PostgreSQL envelope reports `worker_bound: true` and `durable: true`. The
nested snapshot retains its reducer authority and `service_integrated: false`;
the envelope describes storage integration, while neither flag qualifies a
strategy or declares a deployed pilot. Checkpoint bytes are internal storage
material, not an API import contract. A checksum is a consistency check, not
external execution attestation. Rehearsals and readback snapshots cannot restore
spending authority.

## Verification and public activation boundary

Tests drive actual queued fixture actions through claim, write-ahead submission,
independently retained adapter effect and acknowledgment. They cover entry/exit
conservation, zero-free-cash protective exit, uncertainty and partial holds,
definitive absence, rejected effects, late known effects, duplicate reserve and
settlement, lease fencing, missing evaluations, changed authority and invented
fills. Book calculations leave source state unchanged. Worker integration tests
also exercise the actual action cycle, background worker and holding-deadline
timer with an explicit binding, plus transaction rollback after begin,
submission and acknowledgment. They preserve funding and rehearsal records.

Storage tests cover migration without automatic activation, duplicate concurrent
binding, full entry/exit, partial status changes, late effect recovery, failed
acknowledgment rollback, corrupt checkpoints, missing checkpoints and refusal of
an older writer that discards the binding. Run `make verify`, `make test-race`
and `make test-integration`; integration uses disposable PostgreSQL containers.

An isolated native rehearsal also exercised real process kills before/after
effect submission, recovery in new processes, dump/restore at both boundaries,
expired absent-work release without an invented fill, one acknowledgment per
retained effect, full entry/exit, corrupt-checkpoint refusal, older-binary refusal
and rollback to the matching schema-28 binary/database. Its advanced synthetic
clock gives no elapsed-soak or market-performance credit. Raw receipts, dumps,
binaries and process records stay outside Git.

Public activation still needs a bounded operator command/readback and validation
of that command on an isolated stack. The internal storage rehearsal does not
validate an activation command that has not been implemented. Existing pilots
stay unchanged. Rollback must use the pre-upgrade database with its matching
binary, not delete the checkpoint or pin.
