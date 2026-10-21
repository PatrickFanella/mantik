# Allocation ledger core

`internal/allocation` now derives cash, reservations and position cost from an
immutable funded policy and retained comparison/effect transitions. This is the
accounting core for issue #19. It has no persistence, HTTP/CLI import, admission
issuer, execution capability or operational write route. Service integration and
issue #19 acceptance remain incomplete.

`NewLedger` validates the complete owned policy and its funding receipt.
`AppendLedger` replays the existing history, applies one command and returns a
detached replacement ledger, current projection and command receipt. Failure
returns no partial ledger or projection. The caller must serialize the eventual
validation and storage commit; concurrent pure calls do not commit reservations.

A comparison contains owner-selected proposals and admission records. The core
cannot attest their origin. A future service writer must select finalized
decisions, strategy/admission records and execution receipts from its own stores;
these structures must not become a public authority or balance import route.
The projection declares `owner_supplied_allocation_ledger_core_only`; execution
availability and qualification credit remain false.

## Retained comparison and accounting

Comparisons use the [allocation policy](allocation-policy.md), including
priority, cash, strategy/portfolio limits and symbol/group exposure. Accepted
plans deduct available cash and create order reservations. All results, including
deferred and rejected proposals, keep their original reasons and reconsideration
triggers in the journal. A later comparison can reconsider the same unchanged
proposal after admission or cash/exposure changes. It does not replace the prior
result. A changed proposal or a renamed decision hash conflicts, including when
the original proposal was declined.

Every accepted decision identity survives release and position close. Returned
cash cannot make it eligible for a second commitment. Admission and decision
expiry alone release no cash and invent no fill.

Effects identify the decision and a unique owner-supplied evidence ID/hash:

- `fill` moves a positive amount from reserved cash to filled position cost.
- `release` returns a positive unfilled amount to available cash.
- `unknown` holds an outstanding remainder without a financial change.
- `resolve` clears that hold with a separate explicit receipt, without inventing
  a fill or release. A subsequent known effect makes the financial transition.
- `close` removes a positive position cost basis, adds net proceeds to available
  cash and records proceeds minus cost as realized PnL. Zero proceeds are valid.
  Proceeds and resulting available cash are bounded at twice the maximum paper
  opening amount; realized PnL retains the planner's signed maximum-opening bound.

An unknown remainder refuses fills and releases until resolution. A known
position remains closable while its entry remainder is unknown. Partial position
cost and outstanding cash remain separate, and all transitions verify:

```text
available cash + reserved cash + remaining position cost
    = opening funding + realized PnL
reserved cash + remaining position cost <= account capital ceiling
original reservation = remaining reservation + cumulative fills + releases
remaining position cost = cumulative fills - closed cost basis
```

An evidence ID or hash cannot be consumed under a second command. Exact command
retries return the original sequence/hash and the current projection without
reading the clock, even after subsequent transitions. Commands compare their
retained JSON representation, so an equivalent Go time-zone label cannot break
an exact retry. Changed commands conflict.
Backends still need to bind each supplied effect to their actual execution or
recovery evidence; a portable evidence hash cannot provide that authority.

## Replay, capacity and integration boundary

Entries pin their previous head, command, operation time and complete comparison
plan. `ReplayLedger` checks the funded policy, root and entry hashes, monotonic
time and every transition, and rederives comparison results. Self-hashing a
changed plan or invalid effect does not make it valid. Derived balances are not
accepted as input. Evidence timestamps must also be JSON-encodable before any
hash or detached result is constructed. Returned values detach nested commands, reasons and policy.

The core retains at most 1,024 entries and 256 distinct proposal identities,
including declined proposals. It never evicts evidence or rewinds time to make
room; exhaustion returns an explicit capacity error, while an exact retained
retry still works. Operational integration must provide a durable retention and
capacity policy that preserves protective/recovery transitions before enabling
writes. This bounded core is not an operational availability guarantee.

The planner leaves room for both an order and a partial position per active
decision: no more than 128 active decisions under its 256-exposure bound. Closed
commitment identities remain retained separately.

Tests cover competing research/copy proposals, deferred admission followed by
reconsideration, immutable declined identities, partial fill/release/close,
unknown-effect recovery, closing known cost during uncertainty, receipt aliases,
clock-free retries, self-hashed corruption, canceled replay, detached values,
concurrent immutable comparisons and retention capacity. Fifty seeded lifecycle
sequences check cash/reservation/cost conservation and gain/loss reconciliation.
No fixture evidence establishes model authorship, market qualification or profit.
