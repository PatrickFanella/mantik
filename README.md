# Mantik

Mantik is a terminal-first foundation for autonomous paper-trading research.
It separates research, structured decision-making, and operations. Humans
supervise the service through a CLI; a TUI and conversational direction are
planned interfaces.

The bootstrap implements one deterministic synthetic workflow. It accepts
fixture quotes for `DEMO`, evaluates an entry, finalizes a decision with safety
conditions, simulates an entry and stop/target/deadline exit, and retains the
records in PostgreSQL. It does not connect to markets, use an LLM, or qualify a
profitable strategy.

## Run the local demo

Install Go 1.26.9, or use the repository's `mise.toml`:

```bash
mise install
mise exec -- make verify
mise exec -- make demo
```

`make demo` needs no database or provider credentials. It prints structured
JSON: four observations, two finalized action decisions, and two execution
outcomes. The fixture buys 10 units at 100.00 and exits at 94.00 after crossing
a 95.00 stop. Its synthetic realized loss is 60.00. Filling at the observed gap
price rather than the stop threshold is intentional. Fees and slippage are
zero in this fixture model; these results have no qualification credit.

To replay the fixture at explicit information cutoffs:

```bash
./bin/mantik replay --cutoffs 2026-01-05T15:01:00Z,2026-01-05T15:03:00Z
```

The first frame contains the entry; the second contains the stop-gap exit.
Replay runs locally with its own clock and memory store. It does not submit
observations to a service. See [dataset and replay contracts](docs/reference/datasets.md)
for file export, content pins, provenance, and data-quality behavior.

To compare the threshold baseline, its half-size variant, and passive/no-action
controls while retaining a declared recipe and results:

```bash
./bin/mantik experiment --attempt demo-1 --out-dir data/experiments
```

See [experiment contracts](docs/reference/experiments.md) for recipe export,
parameter and benchmark pins, failure retention, and the synthetic cost model.

## Run the persistent service

Docker Compose uses project `mantik-dev`, its own named volume, and loopback
ports 45432 for PostgreSQL and 40880 for the service. Check that these ports are
available; change `.env` if needed.

```bash
cp .env.example .env
make compose-build
docker compose up -d
mise exec -- make build
export MANTIK_API_TOKEN=mantik-local-development
./bin/mantik status
./bin/mantik status --format json
```

Compose reads `.env`; the native CLI does not load it. The token above matches
the example's local-only default. Export your chosen token if you change it.
Service ingestion now queues finalized actions for an independent worker.
The fixed demo uses historical times; its pending service actions expire against
the real worker clock. Use current-time fixture observations as described in
[development](docs/runbooks/development.md), and inspect `status --format json`
for delivery results. Use local `demo` or `replay` for deterministic economics.
Identical observation retries return the original receipts without new actions.
Restarting the service preserves the case and event history. A fresh demo
starts with the in-memory command; the persistent fixture intentionally does
not expose a destructive reset operation.

To stop local services while retaining the database:

```bash
docker compose down
```

See [development and verification](docs/runbooks/development.md) for native
execution, API requests, and disposable integration tests.

The service also persists event, threshold, state-change, and combined
trigger definitions. `trigger-register`, `route-input`, and `close-routing-case`
manage research and decision cases with deduplication and explicit repeat
policies. See [trigger contracts](docs/reference/triggers.md) for JSON examples
and migration compatibility. Matches record case work; research-worker dispatch is
future work. Attributed market inputs can open research cases with exact source
and evidence-class matching; `route-input --derive-market-id` preserves revision
identity. See [market input contracts](docs/reference/market-inputs.md). Provider
collection and authorization remain separate work. `alpaca-normalize` converts
retained Alpaca quote/calendar recordings offline; the bounded read client is
tested against local servers. See [Alpaca contracts](docs/reference/alpaca.md).
Finalized entry decisions retain their protective definitions
atomically with the fixture execution records.

An independent service worker now handles deadlines, elapsed-time conditions,
and expected missing events. It recovers overdue work after restart through
fenced leases. A protective deadline uses usable retained price evidence or
records an operational exception. See [timer contracts](docs/reference/timers.md),
including the real-clock boundary for the historical fixed demo.

Finalized actions are delivered through a durable queue and independent fixture
effect ledger. Restart reconciles uncertain results before attempting delivery,
and stale lease generations cannot submit another effect. See
[action contracts](docs/reference/actions.md) for status and recovery behavior.

Use `readiness --scope fixture_operation` and `operations` to inspect worker
progress, missing protection, and retained exceptions. `recover` supports bounded
rechecking, action reconciliation, and evidence-backed overdue protection.
See [operational contracts](docs/reference/operations.md). Real feed coverage
remains unavailable and is reported separately from fixture readiness.

## Current boundaries

- Only fixture quotes for the synthetic `DEMO` instrument are accepted.
- The versioned library declares typed inputs, instruments, outputs, exact
  dependencies, evidence classes, safety requirements, and mandatory evaluation
  recipes. It registers two immutable, unqualified fixture strategies for
  experiments; the service uses the original threshold method. See the
  [library contract](docs/architecture.md#versioned-library).
- Stop and target checks run on usable incoming quotes. Holding deadlines also
  run independently; missing usable price evidence leaves an explicit exception.
  Expected-event detection uses declared fixture obligations; real feed adapters
  and their availability policies remain future work.
- The synchronous simulator fills at the observed quote with zero costs.
- One executable fixture case and bounded research/decision routing cases
  share one aggregate. PostgreSQL serializes their updates; portfolio
  allocation is future work.
- Status returns the complete small fixture history. Pagination is needed
  before continuous ingestion.
- Dataset replay supports the synthetic fixture quote schema. Corrections,
  missing inputs, and quarantined inputs block further replay admission and
  remain visible as evidence; historical/provider adapters are future work.
- Bearer authentication is available. A non-loopback bind requires a token of
  at least 16 characters. TLS and remote operator access are not configured.
- No external feed, copytrading connector, LLM workflow, dynamic code runner,
  TUI, or live trading path is implemented.

## Design and source

- [Roadmap and milestones](docs/roadmap.md)
- [First running-app pilot checkpoint](docs/runbooks/first-pilot.md)
- [Project board](https://git.subcult.tv/subculture-collective/mantik/projects/5)
- [Architecture and agreed product model](docs/architecture.md)
- [Documentation index](docs/README.md)
- [Open-source design references](docs/research/README.md)
- [Bootstrap architecture decision](docs/adr/0002-mantik-bootstrap.md)

Derived from SUBCULT's `project-template-go-worker-postgres-docker`. The existing
Augr repository remains a separate reference implementation; Mantik does not
access its services, database, or credentials.

GPL-3.0-only. See [LICENSE](LICENSE).

## Prepare the isolated operational pilot

M2 is implemented. Use the [pilot controls](docs/runbooks/pilot-controls.md)
to build an exact-revision image, rehearse a current-time fixture lifecycle, and
restore a coherent backup into separate resources. The [24-hour pilot contract](docs/runbooks/first-pilot.md)
requires additional interruption and deadline scenarios before qualification.
Preparation and a passing smoke check do not complete that pilot.
