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
./bin/mantik activity --limit 10
./bin/mantik activity --limit 10 --format json
./bin/mantik supervise --limit 10
./bin/mantik supervise --view cases --limit 10
./bin/mantik supervise --view exceptions --limit 10
./bin/mantik supervise --view library --limit 10
./bin/mantik cases
./bin/mantik cases --id demo-case-v1
./bin/mantik exceptions --limit 10 --format json
./bin/mantik library
./bin/mantik library --id demo-threshold-v1
```

The [bounded activity readback](docs/reference/activity.md) pages retained audit
events with a fixed history boundary and structured JSON details. It is a
read-only activity view. `supervise` adds interactive page navigation, event
details, and explicit stale/retry behavior using short commands followed by
Enter. The [case inspection commands](docs/reference/cases.md) expose the current
fixture/routing catalogue and one case's latest retained references.
`supervise --view cases` adds page navigation, current case details, and stale-state
refresh. The [exception readback](docs/reference/exceptions.md) pages retained faults and
recovery history without deriving readiness or loading the audit ledger.
`supervise --view exceptions` adds bounded navigation and retained recovery
details with explicit stale/retry behavior. The [library readback](docs/reference/library.md) exposes the service's compiled
synthetic method pins, safety declarations and required checks. `supervise --view library` adds validated catalogue navigation and method
details. Portfolio and service-owned research-attempt views remain planned.

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
authorization for each credential/account and continuous collection remain
separate work. `alpaca-normalize` converts retained Alpaca quote/calendar
recordings offline; the bounded read client is tested against local servers. See [Alpaca contracts](docs/reference/alpaca.md).
The [recorded news boundary](docs/reference/news.md) validates operator-supplied
claims against exact source excerpts, with causal timestamps, explicit uncertainty
and revision declarations. `news-normalize` is offline. The
[private news journal](docs/reference/news-journal.md) retains source/claim
revisions with content conflicts, verified predecessors and explicit original
inspection. The [event-group view](docs/reference/news-events.md) links declared
events at an explicit receipt cutoff and flags revision review requirements.
[Receiver-bound news admission](docs/reference/news-cases.md) retains each
original and routes declared events to dedicated research cases, with revision
reopening and evidence-version checks on closure. The
[bounded review runner](docs/reference/research-review.md) uses recorded model
fixtures and targeted excerpts to finalize unqualified research proposals or
abstentions. [Retained service reviews](docs/reference/retained-reviews.md) pin
case versions, preserve attempts and fence stale completion. Automatic dispatch
and operations authority remain separate. The
[hypothesis declaration contract](docs/reference/hypothesis-declarations.md)
validates proposed mechanisms, test conditions, horizons and exact native method
pins before lifecycle publication or experiment execution.
The opt-in `news-method-review-v1` profile permits targeted native method-contract
inspection and requires independently checked method pins on hypothesis
candidates. Service attempts retain their selected profile and bind finalized
results to its hash; older unpinned reviews continue to use v1.
The local `news-reuse-review-v1` recorded profile can inspect a validated
`hypotheses` export and propose reusing an exact declared question. It binds the
catalogue/inventory hashes and checks current evidence and inspected methods.
Retained service reviews capture their own catalogue inventory at start and
independently recheck the exact declaration when publishing reuse.
`news-reviews --case ID` exposes the complete retained review history for an
observation case, including unpublished abstentions and stale or expired output,
with targeted links by review ID and hashes for the full retained attempts.

The [capture journal](docs/reference/alpaca-journal.md) retains validated recordings
and supports receiver-bound submission and recovery through `alpaca-retain`,
`alpaca-submit`, and `alpaca-journal`. `alpaca-collect` performs one explicit,
bounded provider read with durable attempt evidence; `alpaca-collection` inspects
its retained status. `alpaca-session` runs a finite count within journal capacity,
with inspection through `alpaca-session-status`. No provider connection or
scheduler starts with the service. [Journal archive and restore](docs/reference/alpaca-archives.md)
commands preserve private evidence and verify an isolated recovery copy.
The [first real-feed rehearsal](docs/runbooks/first-real-feed.md) verified
IEX/calendar ingestion and restart retention without economic effects; usable
fresh quote coverage and continuous-feed readiness remain separate gates.
`alpaca-coverage` provides a [read-only regular-session evidence report](docs/reference/alpaca-coverage.md)
from the journal without configuring global readiness.
The [feed-collector API](docs/reference/feed-collectors.md) retains immutable
registrations and fenced ownership leases. It does not start a collector or
configure feed readiness.
`alpaca-submit` can [fence journal delivery](docs/reference/collector-submission.md)
with an explicit registration, owner, and generation; input and admission
receipts commit together.
The [finite leased collector](docs/reference/finite-collector.md) combines
explicit ownership, bounded quote acquisition, fenced delivery, raw capture
publication, and release
through `alpaca-run-collector`. Its optional `--regular-session-seconds` mode
refreshes and publishes a calendar first, then bounds quotes by count, deadline,
and regular-session close. It has no automatic restart or catch-up schedule.
`alpaca-publish` retains [validated server evidence](docs/reference/feed-evidence.md)
for inspection with `feed-coverage`. An explicit [startup policy](docs/reference/feed-readiness.md)
can enable scoped feed readiness for a pinned receiver and required registrations;
it does not start acquisition or alter fixture execution.
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

- Executable observations are limited to fixture quotes for the synthetic
  `DEMO` instrument. Attributed market inputs enter research routing separately.
- The versioned library declares typed inputs, instruments, outputs, exact
  dependencies, evidence classes, safety requirements, and mandatory evaluation
  recipes. It registers two immutable, unqualified fixture strategies for
  experiments; the service uses the original threshold method. See the
  [library contract](docs/architecture.md#versioned-library).
- Stop and target checks run on usable incoming quotes. Holding deadlines also
  run independently; missing usable price evidence leaves an explicit exception.
  Expected-event detection uses declared fixture obligations; continuous real
  feed availability policies remain future work.
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
- External reads require explicit bounded Alpaca commands. Continuous collection,
  a copytrading connector, LLM workflow, dynamic code runner, TUI, and live trading
  remain future work.

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

See [retained hypothesis publications](docs/reference/retained-hypotheses.md) for
current-review binding, exact-question reuse, retained attribution and the
`hypotheses` catalogue command for discovering existing research questions.

See [hypothesis-linked trial registration](docs/reference/hypothesis-trials.md)
for retained experiment plans, publication binding and planned lineage.

The [native trial adapter](docs/reference/native-trial-runs.md) executes retained
intents against the built-in synthetic dataset and preserves completed, failed
and interrupted attempts. [Condition assessments](docs/reference/trial-assessments.md)
compare retained metrics with the declared prediction, null and refutation
conditions while keeping benchmarks and missing results explicit. These
comparisons grant no qualification or trading authority.
`trial-assessments --id TRIAL_ID` reads all retained attempt assessments for one
registered plan, with explicit failure/missing-result counts and a consistent
source boundary. A plan never attempted remains visible with zero attempts.
`hypothesis-assessments --id HYPOTHESIS_ID` combines all registered plans for
the exact question, including parent/sibling lineage and never-attempted plans,
without selecting a winning result.

The [offline worker protocol](docs/reference/research-workers.md) pins jobs and
validates native-fixture results through `worker-job`, `worker-execute` and
`worker-result`. `worker-launch` runs an explicit local container attempt with
host-verified settings and durable evidence; `worker-reconcile` inspects or
explicitly cancels that owned attempt after interruption. The separate
[Backtesting.py adapter](docs/reference/backtesting-fixture.md) compares the
four built-in synthetic methods in a pinned Python container. Its separate
`external-worker-job`, `external-worker-launch`, `external-worker-reconcile` and
`external-worker-result` commands retain and validate owned attempts, mismatches
and interruptions. Native worker v1 remains unchanged. Automatic service dispatch
and qualification of imported results remain separate work. The service can
[declare an external job against a retained native trial](docs/reference/trial-workers.md)
through `trial-worker-declare`, `trial-worker` and `trial-worker-input`; it retains
the exact input bundle for explicit local launch without dispatching a process.
The `trial-worker-attempt-*` commands retain an intent, caller-reported outcome
and exact output bytes for every submitted external attempt. History includes
pending and unsuccessful attempts; a report grants no host attestation or
qualification credit.
`trial-research-history` and `hypothesis-research-history` join all retained
native assessments with external declarations and attempt reports in a
[consistent history view](docs/reference/trial-assessments.md#combined-native-and-external-history).
