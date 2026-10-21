# Paper allocation policy core

`internal/allocation.Evaluate` compares one finite batch of research and
copy-follow proposals against an owner-supplied policy and account snapshot.
It is a pure Go contract for issue #19. No HTTP route or CLI command accepts
these snapshots. The separate [owned policy registration](allocation-policies.md)
retains configuration only; there is no snapshot import, general reservation
writer, admission issuer, provider call or execution path. Issue #19 remains open.

## Ownership and inputs

The policy pins one immutable Mantik paper funding receipt. The owner selects
that account, strategy artifact hashes, priority, capital limits, supported
symbols and correlation groups. A proposal contains a finalized-decision ID and
hash, strategy version, symbol, origin, maximum notional and expiry. It cannot
select an account, priority, group membership or admission receipt.

The snapshot carries available cash, realized paper PnL, positions at cost,
outstanding order reservations, admission assertions and retained commitment
identities. Partial fills have disjoint filled cost and remaining reserved cash.
Unknown-effect holds stay in outstanding orders until independently reconciled;
there is no automatic expiry release. Every position/order must match a retained
commitment's decision ID/hash, strategy and symbol. Different IDs cannot disguise
the same decision hash. Commitment identities survive closed orders/positions,
so returned cash does not make an already allocated decision eligible again.

Cash conservation is checked before comparison:

```text
available cash + position cost + reserved order cash
    = opening funding + realized paper PnL
position cost + reserved order cash <= account capital ceiling
```

Realized PnL is an owner assertion in this function. A future service writer must
derive it and every exposure from its own effect/fill ledger. Unrealized gains
are not available cash. The ceiling does not increase when a snapshot reports a
gain. There is no leverage, shorting, account transfer or balance import here.

The snapshot must not predate funding or come from the future. Its maximum age
is an explicit policy value from 1 through 3,600 seconds; the boundary is
inclusive. Admissions must match registered strategy artifact hashes and have
valid evidence pins and time intervals. Eligibility requires `valid_from <= at
< valid_until`; proposal expiry is exclusive. An existing commitment cannot
produce a second reservation even when admission changes.

## Comparison and results

Lower owner priority numbers are considered first; ties use ascending decision
ID. Input arrival order and research/copy origin do not affect arbitration.
Earlier eligible proposals consume a simulated cash/exposure budget before later
proposals are considered. This is an explicit priority policy, not an optimizer
or a claim of expected returns.

Each result includes the complete proposal, status, reason codes and
reconsideration triggers:

- `reserve_plan`: the amount fits available cash, the account ceiling, strategy
  limit, symbol limit and every declared group limit after earlier winners.
- `already_committed`: the retained decision identity matches; no new cash is
  reserved, including when its former position is closed.
- `defer`: admission needs reassessment, cash/exposure must change, or commitment
  history or partial-fill projection capacity is full. Each applicable temporary
  limit remains visible.
- `reject`: unsupported strategy/symbol, expired or conflicting decision identity,
  or an amount exceeding a policy limit even with no competing exposure. A new
  finalized decision is required for reconsideration.

Correlation groups are explicitly declared gross-notional buckets. Overlapping
memberships count the amount against every applicable bucket; the implementation
does not estimate statistical correlation or infer hedging benefits. Symbol and
strategy limits include existing positions and orders as well as planned winners.
Policies may retain a rule below an existing exposure, but no additional proposal
can exceed that rule. A snapshot already above the account's capital ceiling is
invalid and produces no partial plan.

Bounds are 128 proposals per batch, 256 active exposures, 256 retained commitment
identities and 64 rules of each kind/admission assertions. An active decision
reserves room for both an order and a partial position: the planner defers a new
commitment at 128 active decisions, even if only order exposures exist so far.
Duplicate proposal IDs
or decision hashes invalidate the batch. History is never evicted to make room;
capacity defers new commitments while preserving duplicate detection. Funding
and individual notionals are bounded by the existing paper-account contract.
All money uses integer USD cents. Symbol syntax matches the price adapter's
uppercase, digit, dot and hyphen form, up to 20 characters.

The plan pins policy, snapshot, exact supplied proposal array and complete output.
Arbitration is independent of proposal order; the input hash still records that
order. The plan includes simulated remaining cash and reservations. It carries
`owner_asserted_allocation_plan_only`, with execution availability and
qualification credit both false. Hashes provide integrity, not ownership,
authentication, finalized-decision verification or qualification attestation.

## Verification and remaining integration

Tests cover owner priority, research/copy competition, every budget dimension,
admission time boundaries, partial position/order accounting, unknown holds,
closed-decision retries, changed identities, historical capacity, invalid money,
corrupt funding, stale snapshots and canceled evaluation. Two hundred seeded
batches of forty competing proposals check conservation, all exposure ceilings
and order-independent arbitration. Inputs remain unchanged by planning.

Owner policy configuration now has durable registration and readback. The
[ledger core](allocation-ledger.md) derives accounting and retains comparisons
and effects with full replay checks, without a service write route. The next
#19 unit must serialize finalized-decision/admission bindings, complete
allocation history and reservation commits with authoritative
account projections in Memory/PostgreSQL. It must retain declined decisions and
reconsideration history, reject caller-supplied balances/admissions, and expose
validated service/terminal readbacks. Realistic fills (#20) and qualification
admission (#21) must use those same commitments before operational activation.
This core alone does not complete any of those acceptance gates or authorize
an entry, a natural soak or a profitability claim.
