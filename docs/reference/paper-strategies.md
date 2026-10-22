# Service-owned native paper strategy definitions

`PaperStrategyStore` retains immutable, safety-bound native strategy configuration
under the service's research receiver. It supplies an owner record for future
finalized decision and allocation bindings in issue #19. Registration does not
admit, activate, evaluate or trade the strategy. General decision finalization,
qualified admission and operational ledger integration remain incomplete.

The only method is `native-paper-threshold-v1`, implemented by the native
`paperstrategy.Signal` predicate. It returns whether a bounded price is at or
below the declared threshold. That predicate alone checks no quote provenance,
calendar, freshness, statistical evidence or allocation; it cannot finalize or
authorize an entry. Generated source and copy-follow methods require their own
implemented contracts and the same later applicable admission gates.

## Definition and ownership

A `Definition` contains ID, method version, symbol, Alpaca feed, intended input
evidence class, entry threshold, quantity and mandatory safety limits. There is
no account, balance, admission receipt, executable source or finalized decision
field. Authenticated operator registration declares configuration only; it does
not attest that the requested data exists or that a provider was contacted.

Symbols use the price adapter's uppercase form, including digits, dot and hyphen,
with at most twenty characters. Feeds are `iex`, `sip` or `delayed_sip`. Input
classes are `synthetic_unqualified`, `recorded_market_unqualified` or
`prospective_market_unqualified`; none grants qualification credit.

Prices and safety levels use integer cents with a maximum of 100,000,000 cents.
Stop is positive and below the threshold; target is above it. Quantity is positive
and must fit maximum notional at the threshold, using division before any
multiplication. Maximum notional is at most 1,000,000,000,000 cents; holding time
is 1 through 86,400 seconds, and maximum quote age is 1 through 3,600 seconds.
These are contract bounds and operator parameters, not recommended settings.

The returned `Record` includes the complete command, server registration time,
definition hash and complete record hash. Its authority is
`operator_declared_paper_strategy_configuration_only`; admission availability,
execution availability and qualification credit are false. Definition hashes
pin the exact native configuration; record hashes additionally pin receiver and
retention time. Hashes alone do not prove a service owns a definition. A later
writer must select the actual owned record and compare all bindings.

IDs are immutable. An exact retry returns the original record without reading a
new clock, including at the 64-definition capacity. Changed configuration under
the same ID conflicts. The complete catalogue is validated before any read,
new write or retry, so a selected ID cannot hide unrelated corrupt configuration.
There is no replacement, deletion, activation or reservation route.

## HTTP and CLI

The bearer-authenticated API provides `POST /v1/paper-strategies`,
`GET /v1/paper-strategies` and `GET /v1/paper-strategies/{id}`. POST takes only a
strict `Command` with receiver and definition, bounded at 4,096 bytes. New writes
return 201 and exact retries 200 with `{record, duplicate}`. Query parameters,
duplicate or differently cased JSON fields, unknown fields, nulls, trailing
objects and account/admission/source injection are rejected. Invalid input is
422, conflicts or capacity 409, missing IDs 404 and storage unavailability 503.
Backend requests retain the existing five-second deadline.

```sh
./bin/mantik paper-strategy-register --api-url "$api" \
  --receiver-id "$receiver" --file definition.json
./bin/mantik paper-strategies --api-url "$api" --receiver-id "$receiver"
./bin/mantik paper-strategy --api-url "$api" --receiver-id "$receiver" \
  --id baseline-v1 --record-sha256 "$record_sha256"
```

The registration file is a `Definition`, not an enclosing command. The client
validates strict responses and expected receiver, definition ID, submitted
command or record hash before emitting output. No CLI command imports balances,
admission assertions, source, orders or ledger effects.

## Durability and verification

Memory serializes the operation under its aggregate mutex. PostgreSQL schema 37
adds only `mantik_paper_strategies`; writes hold the receiver row lock, and reads
use repeatable-read snapshots. Failed inserts roll back. The 36-to-37 migration
preserves receiver identity, accounts, allocation policies, research and fixture
state. Older binaries refuse schema 37. Rollback restores a compatible private
backup into separate resources with its matching binary; dropping the new table
in an existing service is not an operational rollback.

Tests cover parameter bounds, native threshold boundaries, receiver ownership,
concurrent exact registration, changed-command conflicts, capacity, clock-free
retries, detached readbacks, self-hashed authority forgeries, complete-prefix
corruption, strict HTTP/CLI behavior, restart, upgrade preservation and failed
insert rollback. Built-CLI and dump/restore rehearsals use disposable resources;
local tests, hosted CI, deployment and qualification remain separate evidence.
This change neither upgrades an existing pilot nor completes issue #19.
