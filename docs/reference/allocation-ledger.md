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

## Replayed entry-fill binding

`allocation.ApplyEntryFillBook` stages entry debits from the existing shared
paper-fill book. The internal function accepts a ledger and book, replays both
complete histories, and derives fee-inclusive debit amounts from the resulting
fills. It accepts no separate amount, replacement balance, admission or effect
assertion. The exact execution model must be retained in the ledger's policy.

Each book order must be a USD buy limit whose ID matches an already reserved
proposal, whose symbol matches that proposal, whose placement follows the
retained reserve decision, and whose expiry is within the proposal's validity.
Its debit ceiling cannot exceed the proposal's original funding. Fill replay
retains shared displayed liquidity across competing orders; a four-share offer
cannot become four shares for each order. Missing or unfilled quote outcomes
produce no debit. They do not release the reservation.

Each derived effect and ledger command uses `entry-fill-` followed by the hash
of the binding method, immutable book-root hash and exact replayed fill hash.
The method is `replayed-shared-book-entry-debit-v1`. Existing entry debits for
these decisions must match the same derived commands, hashes and fill times.
A revised root, a truncated book that omits an existing debit, or an earlier
amount-only effect conflicts. Adding book commands preserves prior fill
identities; exact retained debits are duplicates and new fills consume the
remaining reservation once. The ledger's normal time, uncertainty, capacity and
funding rules continue to apply. An old prefix cannot hide later debits.

All debit appends are staged in detached values. If a later debit fails, the
function returns an error with zero ledger/projection results and no receipts;
both supplied histories remain unchanged. This is an immutable preflight,
**not a durable transaction**. The execution owner still needs actual source
selection, admission, order/position/protection ownership and an atomic durable
commit of the source book and resulting allocation ledger. The exit-fill reducer
below derives closes; recovery/release bindings remain separate work. There is no HTTP or CLI
book import, no storage write, and no execution or qualification grant.

Tests use manufactured owner inputs to verify shared partial-fill costs,
conservation, exact retries, incremental book history, missing quotes, missing
admission, model/order/proposal mismatches, rehashed invalid outcomes, prior
effect conflicts, unknown holds, cancellation, detached concurrent preflight
and rollback of an entire staged debit batch after a later failure. These
checks advance #19/#20's internal binding dependency; they do not establish
service integration or complete either issue's acceptance gates.

## Replayed exit-fill binding

`allocation.ApplyExitFillBook` accepts an owner-supplied ledger, a completed
entry book and an exit book. It replays all three and requires every entry debit
to have already been committed through the entry-fill binding. It cannot post
a missing entry as a side effect of closing a position. Both books must use the
exact policy execution model.

Each sell order selects an entry position through the same proposal/order ID.
The symbol, currency and evidence mode must match. The entry order must be
terminal (filled, cancelled or expired), with positive filled quantity, before
exit placement. The exit quantity cannot exceed that position's actual filled
quantity. Requiring terminal entries freezes the cost basis before any close;
adding later entry fills or revising the entry book cannot silently change past
closing allocations.

The versioned method is `replayed-shared-book-exit-proceeds-v1`. Each close uses
the exact replayed sell fill's net proceeds, including fees and commissions.
Cost basis includes entry fees. It follows cumulative average cost in whole
shares: `floor(total_entry_debit * cumulative_closed_quantity / entry_quantity)`.
Each close receives the difference from the previous cumulative amount; the
last share receives any rounding remainder. Integer arithmetic divides first
to avoid overflowing the cost-times-quantity product. Partial closes conserve
every cent of entry basis when the full position is closed.

Derived close identities bind the method, complete terminal entry-book hash,
immutable exit-book root and sell-fill hash. An exit-book prefix can be extended
without changing earlier close identities; exact retained closes are duplicates.
A truncated close history, changed root, revised entry book or amount-only prior
close conflicts. New closes still obey ledger time and capacity limits. Negative
net proceeds require the cost-ledger schema and sufficient available cash;
outstanding reservations cannot fund closing fees. Missing quotes, unfilled
stop-limits, cancellation and expiry without fills do not fabricate proceeds or
release entry reservations.

All closes are staged together in detached values. A later funding or replay
failure returns no partial ledger, projection or receipts. Tests cover shared
partial exits, stop gaps, unfilled stop-limits, exact and extended retries,
source/model/position mismatches, missing entry postings, working entry orders,
oversells, conflicting prior closes, signed net proceeds, reconstruction,
cancellation, concurrent immutable preflight and rollback after a later close
cannot be funded.

This remains an internal execution-method dependency for #19/#20. It does not
attest order ownership, choose strategy exits, issue admission or persist a
transaction. The execution owner must retain the actual order/position/protection
bindings, manage shared liquidity across its complete execution scope, and
atomically commit books with the financial ledger. No public import, new CLI,
storage writer, execution grant or qualification credit is added.

## Fully closed position release binding

`ApplyClosedPositionReleases` is an internal accounting reducer for a remaining
entry/protective reservation after a position closes. It accepts the retained
ledger, terminal entry book and exit book. It accepts no caller amount or clock.
It fully replays both books and the ledger and requires all exact entry-debit
and exit-close postings to be present already; requesting a release cannot
silently post a missing fill or close.

Only an exit that fills the entry's entire actual acquired quantity is eligible.
Its ledger cost basis must be completely closed, and its outstanding effect
state must be known. Working entries, incomplete exits, missing quotes and
expired unfilled exits keep the hold. The amount is the original proposal's
funding less its replayed entry debits. Zero remaining funding creates no effect.
An exit with negative net proceeds must first have passed the existing funded
close checks; a held reservation cannot retroactively fund that close.

The release identity pins the full terminal entry book, the exit book root, the
first exit entry that completes the position and the proposal/order ID. Appending
unrelated exit observations does not relabel that first close. The release uses
the actual closing fill's time. Exact retries retain their historical timestamp
after unrelated ledger progress; altered roots, truncated closes, amount-only
releases and backdated new postings fail. All stages are detached, and any
failure, including capacity exhaustion after an earlier staged release, returns
zero results without changing the inputs or evicting evidence.

The existing zero-fill cancellation/expiry path is unchanged. In a mixed book,
zero-fill orders can be released before a filled position closes; this reducer
checks releases for its selected exit orders. The future owner must replay each
retained transition against its actual prior state and commit its books, ledger,
order and protective state atomically. These pure reducers are not a substitute
for that durable transaction or for owner reconciliation of remaining protective
obligations.

Both book methods are covered by manufactured internal fixtures, alongside
partial closes, terminal partial entries, negative exit costs, unknown effects,
exact retries, immutable concurrent calls and capacity failures. These fixtures
do not supply a service-owned admission or attest market/source ownership. No
HTTP/CLI balance writer, deployment, economic recovery qualification or issue
#19 completion is provided by this binding.
