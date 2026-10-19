# Fixture capital contract

`internal/fixturecapital` defines an internal capital reducer for the existing
synthetic action worker. It consumes finalized `paper-action-v1` intents and
fixture effect lookups, not rehearsal commands, model output or an imported
capital snapshot. It is not wired into service transactions yet, and no current
service balance, position, route, worker or database schema changes.

## Authority and transitions

`New` accepts a validated immutable paper account. A book has private fields;
`Snapshot` returns a detached accounting readback, with no constructor that can
turn that JSON back into spending authority. The first commitment pins the
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
implement it for a real venue. The future storage bridge must supply the existing
fixture effect lookup inside its transaction; the reducer performs no networking
or independent database authentication.

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

## Verification and activation boundary

Tests drive actual queued fixture actions through claim, write-ahead submission,
independently retained adapter effect and acknowledgment. They cover entry/exit
conservation, zero-free-cash protective exit, uncertainty and partial holds,
definitive absence, rejected effects, late known effects, duplicate reserve and
settlement, lease fencing, missing evaluations, changed authority and invented
fills. Book calculations leave source state unchanged.

The next unit must atomically bind an account, reserve before write-ahead
submission, and commit capital changes with action acknowledgment under the same
aggregate lock/transaction. It needs restart/restore, submission/ack crash and
rollback proofs before enabling a service path. Existing accounts, rehearsals and
pilots stay unchanged. Rehearsal records must never become capital authority.
