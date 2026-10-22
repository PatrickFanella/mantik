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

## Ledger versions and fee-bearing exits

`NewLedger` continues to create `mantik-allocation-ledger-core-v1`. Its wire
hashes and nonnegative close-proceeds rules are unchanged. The schema-39 owned
allocation comparison service still uses this version and cannot reserve or
apply effects.

`NewCostLedger` explicitly creates `mantik-allocation-ledger-core-v2` for future
owners that bind realistic fills. Version 2 permits a negative close's net
proceeds when exit fees exceed gross sale proceeds. The debit must fit available
cash at that transition. Outstanding reservations and remaining position cost
cannot fund it. Zero available cash permits a zero-proceeds close but refuses a
net debit. Negative proceeds are bounded by the maximum opening amount; the
existing total cash, exposure, realized-PnL and conservation bounds still apply.
An unfunded close returns no replacement ledger or projection.

For example, a one-share entry costing 10,003 cents including its fee, followed
by a one-cent sale with a 20-cent exit fee, has net sale proceeds of -19 cents.
Closing from 100,000 cents of opening funding leaves 89,978 cents available and
records -10,022 cents of realized PnL. This is arithmetic under declared model
assumptions, not a qualified return.

The v2 entry chain starts from a schema/policy-bound root. Changing a retained
v1 ledger's schema and outer hash cannot reinterpret its entries as v2, and v1
cannot replay v2 entries. There is no automatic migration or conversion of old
balances. Existing v1 histories remain readable under their original contract.

Both versions remain internal accounting cores. A real execution owner must
select genuine admission, reservation, order and fill receipts; preflight the
full fee-inclusive transition before enacting execution; and commit the effect
and ledger atomically. Pure concurrent calculations commit nothing. No public
balance/effect import, funded service execution or qualification grant is added
by `NewCostLedger`.

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
  cash and records proceeds minus cost as realized PnL. Zero proceeds are valid;
  v2 also supports a funded net debit as described above.
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

Compatibility tests pin an existing v1 empty/history/entry hash. Cross-model
tests reconcile actual paper-fill outputs with both entry and exit fees, verify
JSON round-trip replay, refuse debits that would consume reserved cash, preserve
unknown remainders and completed commitments, and check exact historical retries,
duplicate effects, schema rewrites and self-rehashed unfunded closes. The service
continues to require genuine admission/effect owners before any activation.
