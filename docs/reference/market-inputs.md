# Attributed market inputs for research routing

The routing API accepts attributed price, event, and coverage-state records from
market sources. They open research cases through the existing threshold, event,
state, deadline, and missing-event contracts. Fixture observation ingestion and
paper execution keep their existing `fixture/DEMO` boundary.

This is the input-contract slice of issue #11. Alpaca is selected; [recorded normalization and a bounded read client](alpaca.md)
are available. [Durable capture retention and fenced manual submission](alpaca-journal.md)
are available. Authorized access, collection, calendar revision reconciliation,
continuous ingestion, and real-provider readback remain subsequent work. The service does not fetch raw references or contact a
provider. This change does not deploy a new revision to a running pilot.

## Attribution and identity

A non-fixture source is a bounded identifier; symbols use 1–20 uppercase letters,
digits, dots, or hyphens, beginning with a letter. Definitions with these sources
use `behavior: research`. Supported evidence classes are:

- `synthetic_unqualified` for manufactured adapter-contract fixtures.
- `recorded_market_unqualified` for retained market recordings.
- `prospective_market_unqualified` for declared prospective market collection.

Evidence classes are authenticated operator declarations, not proof of provider
access, data quality, prospective admission, or profitable performance. They
must match the definition exactly. Recording a class cannot grant execution
or qualification authority.

Each market input requires `provenance`:

| Field | Contract |
| --- | --- |
| `record_key`, `revision` | Bounded provider record identity and integer revision from 1 to 1,000,000 |
| `raw_ref`, `sha256` | Private artifact reference, at most 256 bytes, and lowercase SHA-256 of retained source bytes |
| `available_at` | Availability time between occurrence and receipt, inclusive |
| `source_symbol`, `mapping_version` | Original symbol and explicit mapping identity |
| `access_ref` | Private access/retention authorization reference, at most 256 bytes |
| `quality` | `usable`, `quarantined`, or `missing` |
| `reason` | Required for quarantined/missing quality, absent for usable quality; at most 256 bytes |

The adapter owns source-byte hash verification and retention. Routing validates
reference shape and retains it; it does not resolve or verify the artifact.
Keep credentials and licensed raw data out of source control and API references.

The input ID is `market-` followed by the lowercase SHA-256 of the compact JSON
array `[source, symbol, evidence_class, record_key, revision]`. The CLI flag
`route-input --derive-market-id` derives it from a file with no ID. An explicit
conflicting ID fails. API callers provide the derived ID. Identical retries
return the original receipt; changed bytes or metadata for the same ID conflict.

Revisions get separate IDs and preserve original records. A revision at the same
occurrence time does not replay an already-matched event or rewind a quote/state
baseline. A newer occurrence can match under the normal condition rules. This
is append-only routing history, not a calendar revision reconciler or a research
replay upgrade; those remain adapter/replay work.

## Freshness, coverage, and limits

Market inputs match only definitions with the same source, symbol, and evidence
class. Freshness uses `received_at - occurred_at` against the definition's
`max_age_seconds`, and evidence cannot be used before availability. A stale
input is retained without matching. Quarantined or missing inputs are also
retained without changing condition facts or suppressing a missing-event timer.

Use an attributed `state` input with `quality: usable` for an observed coverage
transition such as `feed: missing` or `feed: ready`. Do not invent a quote to
represent a missing price. An initial state establishes a baseline; a declared
state-change trigger matches a later transition. This state is research evidence;
it does not claim that operational `real_feed` readiness is implemented.

The existing 16 KiB request, 4,096 retained-input, 32 operator-definition and
128 research-case bounds apply. Full input storage rejects new identities;
identical retries remain readable. Adapter backpressure must stop or retain work
outside the service when this bound is reached. Pagination/pruning and continuous
feed admission require subsequent work; there is no unlimited collector here.

## Synthetic contract example

Save a research trigger outside Git using the trigger example in
[the routing reference](triggers.md), with `source: example-feed`, `symbol: SPY`,
`evidence_class: synthetic_unqualified`, and an event condition for `market_open`.
Use current, explicitly bounded validity times for a running service.

A manufactured input file may contain the following attribution. The digest and
raw reference are illustrative: an adapter must retain and hash actual source
bytes before submission.

```json
{
  "kind": "event",
  "source": "example-feed",
  "symbol": "SPY",
  "evidence_class": "synthetic_unqualified",
  "occurred_at": "2026-01-05T14:30:00Z",
  "received_at": "2026-01-05T14:30:01Z",
  "event_name": "market_open",
  "provenance": {
    "record_key": "session-20260105-open",
    "revision": 1,
    "raw_ref": "artifact:synthetic-session-20260105",
    "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "available_at": "2026-01-05T14:30:00Z",
    "source_symbol": "SPY",
    "mapping_version": "equities-v1",
    "access_ref": "manufactured-contract-fixture-v1",
    "quality": "usable"
  }
}
```

With an authenticated, compatible development service:

```bash
./bin/mantik trigger-register --file data/market-trigger.json
./bin/mantik route-input --derive-market-id --file data/market-input.json
./bin/mantik status --format json
```

The match points to a retained `routing:market-...` input with its provenance.
The fixture observation count, action ledger and economic position remain
unchanged by this research routing.

## Storage compatibility

Schema 6 retains version 1–5 migration checksums and adds the attributed-routing
marker under a database constraint. The version 5 upgrade preserves observations,
actions, cases, timers, receipts and exceptions. Binaries supporting only schema
5 reject the new schema at startup, and their aggregate writes cannot remove
the marker. Deployment needs a coherent backup and a compatible rollback plan.
The existing pilot instances remain on their pinned revisions and schema 5.
