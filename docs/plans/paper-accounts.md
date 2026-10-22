# Paper account foundation

Issue #19 owns allocation; issue #22 needs a portfolio readback backed by that
state. The account foundation retains immutable funding/configuration and an
explicit binding with cash/position projections for one synthetic fixture case.
Interactive portfolio supervision now reads that validated state. Allocation
across competing proposals remains incomplete.

## Ordered delivery

1. Retain Mantik-owned paper account configurations and one immutable opening
   funding receipt per account. Bind writes to the explicit service receiver,
   require USD integer cents, finite opening cash and a committed-capital ceiling,
   preserve exact retries, and expose bounded API/CLI readbacks. This unit does
   not create positions, spend capital, accept proposals or activate strategies.
2. Add an append-only reservation/settlement ledger and cash/position projections.
   The [synthetic offline replay](../reference/paper-ledger.md) now verifies the
   bounded transition model: conservation, partial debits, releases, unknown holds,
   explicit resolution and exact retries. It has no service mutation/import path.
   A [durable rehearsal writer and readback](../reference/paper-ledger-rehearsals.md)
   now retain pinned synthetic commands separately from funding and execution.
   They are not operational account balances. The separate
   [fixture capital contract](../reference/fixture-capital.md) now reserves and
   reconciles from finalized fixture actions and effect lookups, with funded
   position/cash conservation. Explicit queued-memory and PostgreSQL bindings
   now commit reservations and capital with worker action transitions under one
   aggregate lock. PostgreSQL schema 29 retains a bounded transition checkpoint
   that must replay and agree with the actual action/effect ledger and case.
   Explicit API/CLI binding and validated readback now expose this synthetic
   account state without an import, reset or detach path. Isolated native
   rehearsal has checked
   real pre/post-effect process kills, dump/restore and matching-binary/database
   rollback. Rehearse the public binding/readback and real worker recovery on
   fresh resources before using it beyond isolated qualification.
   Qualified admission and realistic
   execution remain separate gates; rehearsal assertions cannot supply authority.
3. Add allocator-owned proposal comparison and reservation, with strategy and
   account limits, exposure/concentration/correlation checks, existing-order
   commitments and retained defer/reject reasons and reconsideration triggers.
   Research proposals cannot choose their account or supply admission authority.
   The [allocation policy core](../reference/allocation-policy.md) now compares
   finite owner-supplied snapshots and competing proposals, with cash/exposure
   limits, admission assertions, retained commitment identities and structured
   reasons/triggers. It returns a plan only.
   The [owned policy registry](../reference/allocation-policies.md) now retains
   one immutable policy per service-owned account and fences reciprocal fixture
   ownership, with strict HTTP/CLI registration and pinned readback. Ledger-derived
   projections, decision/admission bindings and reservation transactions remain
   required before this step is complete. The [allocation ledger core](../reference/allocation-ledger.md)
   now derives cash, reservations and position cost, retains comparison/reconsideration
   history and checks effect/recovery transitions by replay. It has no public
   import or operational writer; service-owned decision, admission and effect
   evidence and durable retention/capacity handling must be integrated next.
   [Native paper strategy definitions](../reference/paper-strategies.md) now retain
   a receiver-owned method/configuration/safety record as the next decision
   binding input. They neither admit a method nor authorize a reservation.
4. Bind qualified strategy admission and finalized decisions to reservations,
   preserving existing action delivery and independent safety checks. Realistic
   paper execution and qualification are separate gates (#20 and #21).
5. Expose current portfolio projections in terminal supervision. The
   [fixture portfolio view](../reference/portfolio.md) now reads the explicit
   binding's cash, reservations, position at cost and realized fixture PnL, with
   bounded commitment details and stale/owner guards. Broader portfolio
   projections depend on competing-proposal allocation and qualified admission.

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

The [owned paper decision registry](../reference/owned-paper-decisions.md) retains
service-computed research proposals and abstentions with their evidence basis.
Account selection, genuine admission and execution-effect bindings, and the
atomic operational allocation ledger remain incomplete for issue #19.
