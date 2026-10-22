# Mantik

Mantik is a terminal-first foundation for autonomous paper-trading research.
It separates research, structured decision-making, and operations. Humans
supervise the service through a CLI and read-only interactive terminal views.
Conversational direction remains planned. The
[terminal acceptance audit](docs/research/terminal-supervision-acceptance.md)
records the delivered inspection scope and its verification limits.

[Candidate source declarations](docs/reference/candidate-source.md) now retain
exact source with hypothesis, dataset and toolchain pins. The separate
[candidate build protocol](docs/reference/candidate-build.md) now checks a fixed
Go interface in a credential-free container. Behavioral replay remains
unfinished; neither declarations nor compile passes grant admission.
The [fixture test-plan comparator](docs/reference/candidate-fixture-tests.md)
pins explicit expectations and retains mismatches, missing outputs and failed
recordings without claiming that candidate behavior was executed.

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
./bin/mantik supervise --view news --limit 10
./bin/mantik supervise --view portfolio --limit 10
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
details. `supervise --view news` adds separate research cases, retained review
history and explicit attempt artifact reads. `supervise --view portfolio` adds
[validated fixture capital supervision](docs/reference/portfolio.md), with cash,
position-at-cost, local commitment pages and explicit unbound/stale states.
Competing-proposal allocation remains planned.
[Paper account configuration](docs/reference/paper-accounts.md) now retains finite
operator-declared opening capital and a ceiling, without allocating proposals or
linking existing fixture positions. `paper-ledger-audit` adds an
[offline synthetic ledger replay](docs/reference/paper-ledger.md) for reservation,
partial debit, release and unresolved-hold conservation; its results cannot be
imported into service accounts. The separate
[durable rehearsal commands](docs/reference/paper-ledger-rehearsals.md) retain
explicit account-bound synthetic assertions with head and retry guards; they
leave funding, operational balances and execution unchanged.
The [fixture capital worker bridge](docs/reference/fixture-capital.md) now
enforces an explicit account binding in queued-memory and PostgreSQL stores,
reserving before submission and settling with action acknowledgment. The durable
store replays a bounded checkpoint against its own action/effect ledger.
`fixture-capital-bind` explicitly binds a pristine fixture to pinned funding;
`fixture-capital` validates and displays its cash, reservations and position.
These commands operate only the synthetic fixture, with no automatic binding of
existing cases or qualified strategy allocation.
The [allocation policy core](docs/reference/allocation-policy.md) compares
competing proposals against owner-declared cash and exposure limits, with
commitment deduplication and reasons for rejection or deferral. It produces a
plan. [Owned policy registration](docs/reference/allocation-policies.md) now
retains one immutable policy per funded account with reciprocal fixture ownership
guards and pinned HTTP/CLI readback. The [allocation ledger core](docs/reference/allocation-ledger.md)
now derives reservations, cash and position cost with retained comparison/effect
history and replay checks. General service writes, authoritative decision/effect
bindings and qualified admission remain pending. [Service-owned native paper
strategy definitions](docs/reference/paper-strategies.md) now retain versioned
threshold configuration and safety under a receiver, with strict HTTP/CLI
registration and pinned readback. Configuration does not grant admission or
execution.

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
and revision declarations. `news-normalize` is offline.
[Recorded article extraction](docs/reference/news-extraction.md) adds a
source-only input and checks recorded extractor artifacts into claim candidates
or retained abstention results. It does not admit those candidates or establish
extractor accuracy. [Local model extraction](docs/reference/local-extraction.md)
retains one explicitly configured local request and its checked response in a
new private directory, without automatic retries or service admission. It derives
byte spans from unique exact quotes while retaining the original model content.
`news-local-inspect` validates the retained request/response/derivation offline,
including failures and unknown completion, without contacting the endpoint.

[OpenAI extraction through Switchyard](docs/reference/switchyard-extraction.md)
adds a separate retained request and offline inspection path for the selected
`openai/gpt-6.1-sol` route. The deployed route passed one checked synthetic live extraction; exact-release
fixtures verified request settings and retry behavior against a fake provider.
Neither gives market qualification or an independent provider spend attestation.
[Switchyard capture imports](docs/reference/switchyard-captures.md) retain checked
caller reports in their own service ledger, including failures and unknown
completion. Import does not admit news or relabel captures as local-model evidence.
[Switchyard capture evaluation](docs/reference/switchyard-extraction-evaluation.md)
compares all supplied finished captures and separately reports unknown completion,
without model calls or qualification credit.
[Explicit Switchyard approval](docs/reference/reviewed-switchyard-admission.md)
binds reviewed captures to research through separate provenance and atomic receipts.

[Local capture evaluation](docs/reference/local-extraction-evaluation.md) compares
a complete retained cohort with supplied annotations, preserving failed results
and reporting unknown completion separately without new model requests.
[Local capture imports](docs/reference/local-captures.md) retain those checked
caller reports in a separate service ledger; import does not admit news.
[Explicit local-capture approval](docs/reference/reviewed-local-admission.md)
binds reviewed candidates to research with distinct caller-reported provenance.
[Retained extraction attempts](docs/reference/retained-extractions.md)
capture an article and deadline before completion, retain exact recorded output
and abstentions, and expose the complete bounded attempt history.
[Explicit reviewed admission](docs/reference/reviewed-extraction-admission.md)
requires exact retained hashes and commits provenance with news case routing;
ordinary recording submission cannot fabricate that provenance.
[Offline extraction evaluation](docs/reference/extraction-evaluation.md) compares
every recorded output with supplied expected claims or abstentions, retaining
structural failures and exact claim mismatches without qualification credit. The
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


Historical sources with date-only or unknown publication can use the separate
[Switchyard diagnostic input](docs/reference/switchyard-extraction.md#historical-diagnostic-publication-precision)
for private extraction and offline evaluation. Exact observation and receipt
times remain required. Diagnostic captures cannot be imported or admitted as
service news; existing exact-time inputs and operational rules are unchanged.


The optional [reported-revenue diagnostic profile](docs/reference/revenue-diagnostic-profile.md)
uses a fixed revenue claim code and explicit assertion/evidence instructions.
It remains separate from the general extractor and supports private fixture
checks and offline replay. Actual model requests require their own finite budget.


`supervise --view news` adds read-only [news research navigation](docs/reference/news-cases.md#interactive-news-supervision),
with case evidence references, retained review history and explicit excerpt/original reads.
Select a case with `d CASE_ID`, then use `h` to read all review attempts and their
applicability; changed case snapshots require refresh. Use `a ATTEMPT_ID` after
`h` to explicitly inspect an attempt’s pinned evidence, result and recorded
artifacts, checked against its displayed history summary. It makes no
model requests and does not submit, close, approve or import research evidence.

[Candidate behavioral workers](docs/reference/candidate-behavior.md) now run the
sealed synthetic examples in an owned, bounded container, retaining mismatches,
partial failures and timeouts without qualification or activation credit.

The [local candidate attempt journal](docs/reference/candidate-attempt-journal.md)
retains intent before worker launch and every local request, including pending
and failed attempts. Reusing a request ID reconciles its original handle.

[Candidate sample properties](docs/reference/candidate-properties.md) separately
check declared repeated-price and monotonic signal relations over retained worker
outputs, preserving violations, missing pairs and inapplicable requirements.

[Candidate cutoff replay](docs/reference/candidate-replay.md) derives jobs from
only the synthetic records visible at an explicit cutoff, retains every parent
exposure and exports the exact derived dataset bytes.

The [candidate lineage contract](docs/reference/candidate-lineage.md) defines
publication-bound registrations, parent pins and bounded retry rules. Memory and
PostgreSQL backends retain the complete lineage and exact dataset bytes.
`candidate-register`, `candidate-registrations`, `candidate-registration` and
`candidate-dataset` expose authenticated registration and verified readback.
The [recorded generation bridge](docs/reference/generated-candidate-lineage.md)
now defines explicit source-pinned registration links, atomic-retention rules and
refusal to adopt an existing unlinked registration. Memory/PostgreSQL storage now
retains linked registrations and exact datasets atomically (schema 35), with
complete provenance validation before ordinary or generated registration reads
and retries. `generated-candidate-register`, `generated-candidate` and
`generated-candidates` now expose authenticated registration, pinned full-artifact
inspection and coherent manual/generated history.
The [candidate worker intent backend](docs/reference/candidate-worker-intents.md)
now binds pre-execution requests to exact registrations and behavior jobs.
`candidate-worker-intent-start`, `candidate-worker-intents` and
`candidate-worker-intent` retain and inspect those declarations, with coherent
registration pins and validated job export. `candidate-worker-run` and
`candidate-worker-reconcile` bind an exported intent to a receiver-bound local
journal and its original owned handle. `candidate-worker-capture` exports
[portable caller-reported outcome evidence](docs/reference/candidate-outcome-captures.md),
and `candidate-capture-inspect` validates it or exports exact output bytes.
[Outcome backends](docs/reference/candidate-worker-outcomes.md) now retain reports
against the service's own intent/dataset snapshot and preserve full captures and
exact artifact bytes.
`candidate-worker-outcome-report` submits a pinned capture;
`candidate-worker-outcome` inspects the retained receipt or exports capture and
exact stdout/stderr bytes through authenticated HTTP. `candidate-worker-outcomes`
returns one coherent intent/report snapshot and the request IDs without retained
reports, without inferring host state. Cross-owner execution fencing remains
unfinished.


[Recorded candidate generation](docs/reference/recorded-candidate-generation.md)
now seals an offline request and preserves exact recorded output before worker
checks. `candidate-generation-plan`, `candidate-generation-record` and
`candidate-generation-inspect` retain candidate or rejection evidence with explicit
hash pins and exact artifact export. These commands make no provider request and
do not attest model authorship. [Generation intent backends](docs/reference/candidate-generation-intents.md) now
retain publication-bound plans and exact datasets before output admission.
Authenticated `candidate-generation-intent-start`, `candidate-generation-intent`
and `candidate-generation-intents` commands now submit and inspect these declarations.
Generated-registration links and a provider adapter remain unfinished.

[Generation output contracts](docs/reference/candidate-generation-outcomes.md)
now bind full caller-recorded output to an exact generation intent, preserve
rejections and define coherent report history and clock-free retry rules. They
now have atomic Memory/PostgreSQL retention with complete-prefix validation.
`candidate-generation-outcome-report`, `candidate-generation-outcome` and
`candidate-generation-outcomes` now admit and inspect pinned full recordings and
exact output/source bytes. Generated-registration lineage remains unfinished.
No provider execution or model authorship is asserted.

The [allocation ledger's opt-in v2 contract](docs/reference/allocation-ledger.md#ledger-versions-and-fee-bearing-exits)
can reconcile exit fees that exceed sale proceeds when available cash funds the
difference. Existing v1 replay and the service's deferral-only behavior remain
unchanged; no execution authority is granted.

The internal [shared paper-liquidity method](docs/reference/paper-fill-method.md#shared-displayed-liquidity-book)
now allocates each displayed bid/ask allowance across competing orders under one
replayable model, preserving source quotes and preventing recapture aliases from
replenishing liquidity. Service-owned admission, capital and effect bindings
remain unfinished.

Service-owned [allocation comparisons](docs/reference/owned-allocation-comparisons.md)
now retain native decision/policy bindings, owner-derived admission readiness
checks and replayable deferral ledgers in
Memory/PostgreSQL. The server selects funding from exact strategy-policy matches;
clients supply retained decision IDs and pins. Optional immutable policy execution
models now bind full buy debit ceilings, including worst-case whole-share partial
fees, to actual finalized proposals. Admission and execution bindings
remain incomplete, so these comparisons cannot reserve cash or create orders.

The opt-in [automatic outcome worker](docs/reference/owned-paper-outcomes.md#automatic-sealed-collection)
(`serve --automatic-paper-outcomes`) now seals one retained forward quote outcome
per matured entry decision without a client assessment request. Missing prices,
retries, capacity and worker health remain explicit; no provider/model call or
admission authority is added.

Authenticated [finalized-decision explanations](docs/reference/owned-paper-decisions.md#grounded-explanations)
render retained reasons and evidence pins without a model request or changes to
decision, admission, allocation or execution authority.

Service-owned [social watches and recordings](docs/reference/owned-social-watches.md)
bind versioned provider/account policies to exact, explicitly unqualified source
recordings. Revisions, deletions, repost roots and repeated-claim groups remain
inspectable. Connectors report unavailable per watch; no provider request,
research dispatch, independent evidence, fill or trading grant is implied.
The authenticated applicability read reports current source revisions, causal
repost lineage, deletions, policy changes, and staleness without granting dispatch
or trading authority.

Service-owned [forward quote outcomes](docs/reference/owned-paper-outcomes.md)
assess actual retained native proposals against a fixed later observation window.
The service selects prices from its own publication history; callers select only
a decision identity and hash. Waiting, missing and observed assessments are
immutable. Quote deltas are decision observations, with no fill, PnL, admission
or qualification grant.

Service-owned [paper proposals and abstentions](docs/reference/owned-paper-decisions.md)
retain native evaluations from owned strategy and price/calendar evidence.
They grant no admission, allocation or execution authority.

The internal [paper fill method](docs/reference/paper-fill-method.md) declares
spread, cost, latency, liquidity and stop behavior for replay and future ongoing
paper use. Its order journal grants no operational execution authority.
