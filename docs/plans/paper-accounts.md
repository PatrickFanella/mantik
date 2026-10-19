# Paper account foundation

Issue #19 owns allocation; issue #22 needs a portfolio readback backed by that
state. As of October 19, 2026, the service exposes one synthetic executable case
and no account/portfolio API. A terminal wrapper around that case cannot satisfy
account allocation.

## Ordered delivery

1. Retain Mantik-owned paper account configurations and one immutable opening
   funding receipt per account. Bind writes to the explicit service receiver,
   require USD integer cents, finite opening cash and a committed-capital ceiling,
   preserve exact retries, and expose bounded API/CLI readbacks. This unit does
   not create positions, spend capital, accept proposals or activate strategies.
2. Add an append-only reservation/settlement ledger and cash/position projections.
   Verify cash conservation, release/partial/unknown effects, competing requests,
   idempotency and restart/restore before any admission path uses the account.
3. Add allocator-owned proposal comparison and reservation, with strategy and
   account limits, exposure/concentration/correlation checks, existing-order
   commitments and retained defer/reject reasons and reconsideration triggers.
   Research proposals cannot choose their account or supply admission authority.
4. Bind qualified strategy admission and finalized decisions to reservations,
   preserving existing action delivery and independent safety checks. Realistic
   paper execution and qualification are separate gates (#20 and #21).
5. Expose current portfolio projections in terminal supervision once those
   projections exist. Do not relabel account configuration or fixture outcomes
   as portfolio performance.

## First-unit contract and checks

The account ID is an operator-chosen stable command identity. Reusing it with
changed currency, opening cash or capital ceiling conflicts. At most 16 accounts
are retained per receiver. Each opening amount is 1 through 1,000,000,000,000 cents;
its committed-capital ceiling is positive and no larger than opening cash.
These are implementation bounds and declared paper configuration, not recommended
funding levels or balances held at a broker. There is no top-up, withdrawal,
transfer, live account identifier, credential or external balance import.

Memory and PostgreSQL must share the contract. PostgreSQL schema 27 adds only an
account table; startup fences older writers. No existing fixture case, action,
effect or research record is assigned to these accounts. Rollback restores the
pre-upgrade database with its matching binary; do not drop the new table and
continue with an older binary against a modified database.

Verification covers invalid amounts and identity, detached readbacks, exact
retries, changed-command conflicts, concurrent duplicate creation and account
capacity, namespace fencing, corrupt retained receipts, schema 26 upgrade and
restart/restore. Run `make verify`, `make test-race`, `make test-integration` and
an isolated built-CLI rehearsal. Existing services and financial data stay outside
the rehearsal. Issue #19 remains incomplete after this first unit.
