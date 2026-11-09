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

For a future isolated paper release, use the
[source/image preflight and release gates](docs/reference/paper-release.md).
Its receipt identifies one local build; deployment, recovery and qualification
remain separate requirements.

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
MANTIK_API_TOKEN=... ./bin/mantik supervise --view decisions --limit 10
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
`supervise --view decisions` adds authenticated, read-only navigation of finalized
research decisions, their grounded explanations, retained market evidence and
phase attribution. On the displayed page, `a ID` reads allocation choices and
non-grant admission-review history bound to that exact finalized decision.
It pins each inspection to the displayed decision and receiver. See
[decision inspection](docs/reference/supervision-tools.md#terminal-decision-inspection).
Use `a ID` for readable attribution and `aj ID` for the complete attribution
row as JSON. The view shows explicit missing evidence, native signal scoring
limits, and retained social/copy observation and ingestion delays.

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
leave funding, operational balances and execution unchanged. PostgreSQL
readbacks and exact-retry acknowledgements return zero results when COMMIT fails;
a previously retained original entry remains available for inspection.
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
guards and pinned HTTP/CLI readback. Account and policy writes, reads and exact
retries return no result after a failed PostgreSQL COMMIT. The [allocation ledger core](docs/reference/allocation-ledger.md)
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
PostgreSQL review starts, completions and exact retries return zero views on
failed COMMIT.
Retained service reviews capture their own catalogue inventory at start and
independently recheck the exact declaration when publishing reuse.
`news-reviews --case ID` exposes the complete retained review history for an
observation case, including unpublished abstentions and stale or expired output,
with targeted links by review ID and hashes for the full retained attempts.

The [capture journal](docs/reference/alpaca-journal.md) retains validated recordings
and supports receiver-bound submission and recovery through `alpaca-retain`,
`alpaca-submit`, and `alpaca-journal`. `alpaca-collect` performs one explicit,
bounded provider read with durable attempt evidence, including
[corporate-action source snapshots and explicit page continuations](docs/reference/alpaca-corporate-action-snapshots.md) with verified journal acquisition receipts; `alpaca-collection` inspects
its retained status. `alpaca-session` runs a finite count within journal capacity,
with inspection through `alpaca-session-status`. No provider connection or
scheduler starts with the service. [Journal archive and restore](docs/reference/alpaca-archives.md)
commands preserve private evidence and verify an isolated recovery copy.
The [first real-feed rehearsal](docs/runbooks/first-real-feed.md) verified
IEX/calendar ingestion and restart retention without economic effects; usable
fresh quote coverage and continuous-feed readiness remain separate gates.
`alpaca-coverage` provides a [read-only regular-session evidence report](docs/reference/alpaca-coverage.md)
from the journal without configuring global readiness.
`issuer-document-collect` acquires one method-bound
[issuer distribution document](docs/reference/issuer-distribution-documents.md)
into a separate private journal, and `issuer-document` inspects its rederived
parse. Retained documents establish no coverage or economic effect.
`polygon-corporate-collect` acquires one complete
[Polygon dividend or split query](docs/reference/polygon-corporate-actions.md)
with a private credentials file, and `polygon-corporate-collection` inspects its
rederived snapshot. Snapshots establish no coverage or economic effect.
`serve --polygon-reference-journal` and `--issuer-document-journal` let the
service [import retained references into custody](docs/reference/corporate-reference-custody.md)
with their original bytes, reverified on inspection. Custody establishes no
coverage or economic effect.
[Window coverage](docs/reference/corporate-window-coverage.md) reconciles
retained Alpaca, Polygon and issuer sources for one instrument's ex-date window.
Declaration v9 predeclares the collections and assessment v12 retains the result
per instrument; a covered window applies no economic effect.
[Total-return accounting](docs/reference/total-return-accounting.md) replays a
fill book with exact arithmetic and applies reconciled dividends and splits.
Declaration v10 and assessment v13 apply it to covered instruments and decide
`benchmark_comparison` against the predeclared minimum net excess; a pass
grants no admission.
Declaration v11 and assessment v14 decide `parameter_and_cost_sensitivity`
from the total returns of predeclared threshold neighbors in the base and
stress models; a pass grants no admission.
Declaration v12 and assessment v15 decide `out_of_sample` from the holdout
decisions' standalone total returns, and `sample_sufficiency` and
`search_multiplicity` from a day-clustered exact sign test with a
Bonferroni-corrected alpha. An assessment whose variant passes every check is
`complete`; completion grants no admission.
Declaration v13 and assessment v16 also decide `maximum_drawdown` from each
variant book's observed bid-marked drawdown against the plan's
`maximum_drawdown_cents`.
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
and regular-session close. Each run has no restart or catch-up of its own;
`alpaca-collector-loop` repeats runs on one journal until capacity runs out.
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
restore a coherent backup into separate resources. New pilot backups bind a public-table
data identity to the dump snapshot and check it before restored workers start. The [24-hour pilot contract](docs/runbooks/first-pilot.md)
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
reject new proposal aliases of the same strategy and provider market occurrence,
including aliases of earlier comparisons. They retain native decision/policy bindings, owner-derived admission readiness
checks and replayable deferral ledgers in
Memory/PostgreSQL. The server selects funding from exact strategy-policy matches;
clients supply retained decision IDs and pins. Optional immutable policy execution
models now bind full buy debit ceilings, including worst-case whole-share partial
fees, to actual finalized proposals. Admission and execution bindings
remain incomplete, so these comparisons cannot reserve cash or create orders.
Model-bearing comparisons also bind entry debit plus worst supported protective
exit shortfall into the proposal funding requirement. These remain deferrals;
no cash is reserved and no execution authority is issued.

New owner allocation comparisons also bind proposal, cost, safety and readiness
expiry to a stricter execution-model quote-age limit measured from the retained
quote occurrence. Historical records and retries retain their original expiry;
this supplies no admission or execution authority.

Comparison v15 is the first owner record that can reserve cash. For each
proposed strategy the owner issues a paper admission from the latest complete
v15 evaluation assessment of a declaration that names that policy and strategy
record, valid for the declaration's admission TTL. A later blocked assessment
withdraws it. Admitted proposals reserve capital in the policy ledger; the next
comparison after a proposal expires releases its unfilled reservation. Readiness
v7 and admission review v2 report `paper_operationally_eligible`; execution and
qualification credit stay unavailable. See
[owner-issued paper eligibility](docs/reference/owned-allocation-comparisons.md#owner-issued-paper-eligibility).

With `MANTIK_PAPER_EXECUTION=on`, `serve` also executes reserved proposals as
paper entry orders. Each comparison opens buy limit orders at the bound cost
ceiling. Each later comparison, and each `POST /v1/paper-executions` step,
fills them from retained quotes and releases orders that expire unfilled.
A filled position that crosses its stop or target, or reaches its holding
limit, closes with a market exit on a later quote, and its unspent notional is
released. An execution worker steps every open book every 15 seconds and
reports under the `paper_execution` readiness scope. An exit that expires
with shares left is replaced on the next quote until the position closes.
Each step lists open positions as holding, exit pending or exit working. Capital reads back as v3
with open and closed positions. See
[paper entry execution](docs/reference/owned-allocation-comparisons.md#paper-entry-execution).

The allocation journal seals older records into a checkpoint and an archive
table, so an append replays only recent records. Reads still list every
record. `GET /v1/allocation-comparisons/audit` re-executes the full history
and checks the checkpoint against it. See
[checkpoints and the archive](docs/reference/owned-allocation-comparisons.md#checkpoints-and-the-archive).

The opt-in [automatic outcome worker](docs/reference/owned-paper-outcomes.md#automatic-sealed-collection)
(`serve --automatic-paper-outcomes`) now seals one retained forward quote outcome
per matured entry decision without a client assessment request. Missing prices,
retries, capacity and worker health remain explicit; no provider/model call or
admission authority is added.

Authenticated [finalized-decision explanations](docs/reference/owned-paper-decisions.md#grounded-explanations)
render retained reasons and evidence pins without a model request or changes to
decision, admission, allocation or execution authority.
Typed [decision claim reviews](docs/reference/decision-review-cases.md) now retain
contradictory or unsupported claims in an open review case per actual finalized
decision. An optional retained social/copy recording pin binds the review to
actual owner provenance and its enabled `decision_review` policy, with claim-group
deduplication. Source authenticity and extraction remain unverified.
Original prose stays unverified; reviews do not revise decision or
financial authority.
New v2 finalized decisions also reject omission of selected-registration inputs
and raw publications available at finalization. Retained v1 decisions keep their
legacy replay contract; this is no qualification or financial-effect grant.

The explicit [model-assisted decision draft](docs/reference/model-decision-drafts.md)
command reads a pinned finalized decision, retains one advisory model attempt and
checks its typed claims through the same service review owner. Prose remains
unverified, and model failure does not alter the deterministic explanation or
strategy/protection workers.
`paper-decision-draft-reconcile` reads the original intent and actual service
review after an uncertain submission, without another model request or review
POST. A missing review at the read time does not authorize a retry.

An explicitly configured `serve --corporate-source-journal PATH --corporate-source-journal-id ID`
can inspect verified local acquisitions through `GET /v1/corporate-action-acquisitions/{id}`.
The server owns the existing journal lock and does not start collection.
With that reader configured, `POST /v1/corporate-action-imports` imports an exact
acquisition pin and original recording into the durable source catalogue; callers
cannot supply source bytes, proof assertions or financial authority.

The separate [corporate-action source custody](docs/reference/corporate-action-source-custody.md)
API retains original reported pages with service receipt times, immutable hashes
and targeted readback. It preserves unknown source fields and revisions without
granting provider authenticity, complete coverage, economic interpretation or
admission. Assessment-pinned `GET /v1/paper-evaluation-assessments/{id}/corporate-sources`
resolves query, revision and pagination metadata from the retained owner cut.
`GET /v1/paper-evaluation-assessments/{id}/corporate-page-chains/{source-id}`
inspects causal cursor links within that same cut, preserving ambiguity and
missing acquisition evidence without granting coverage or economic authority.
Evaluation assessments from v5 onward pin the actual custody inventory at issuance and
reject later backdated source writes. Economic source selection and passive
benchmark accounting remain unfinished; quote/calendar publication keeps its
existing capability boundary.

Individual retained sources are available through authenticated, clock-free
`GET /v1/social-recordings/{capture-id}` and `GET /v1/copy-recordings/{capture-id}`;
these resolve claim-review source links without granting applicability or execution.
Typed supervision and MCP expose the same originals through fixed `inspect_record`
kinds `social_record` and `copy_record`, with no caller path or cutoff.

Service-owned [social watches and recordings](docs/reference/owned-social-watches.md)
bind versioned provider/account policies to exact, explicitly unqualified source
recordings. Revisions, deletions, repost roots and repeated-claim groups remain
inspectable. Connectors report unavailable per watch; no provider request,
research dispatch, independent evidence, fill or trading grant is implied.
PostgreSQL receipts and history catalogues are returned only after successful
COMMIT; failed commits return zero results and preserve retryable identities.
The authenticated applicability read reports current source revisions, causal
repost lineage, deletions, policy changes, and staleness without granting dispatch
or trading authority.

Service-owned [forward quote outcomes](docs/reference/owned-paper-outcomes.md)
assess actual retained native proposals against a fixed later observation window.
The service selects prices from its own publication history; callers select only
a decision identity and hash. Waiting, missing and observed assessments are
immutable. New v2 outcomes reject omission of selected-registration inputs or
raw publications available at assessment time; legacy v1 membership remains
unproven. PostgreSQL decision/outcome catalogue reads return zero results on
failed COMMIT. Quote deltas are decision observations, with no fill, PnL, admission
or qualification grant.

[Owned copy-source records and follow intent](docs/reference/owned-copy-sources.md)
retain claimed trades, order/fill records and holdings separately, with original
attribution and independent follow limits. Provider observations and instrument
mapping remain unverified; these records grant no dispatch or execution.

Authenticated [retained market dimensions](docs/reference/owned-paper-decisions.md#retained-market-dimensions)
bind finalized decisions to original bid/ask recordings, raw provider sizes,
and retained calendar evidence available at finalization.
Share-unit normalization remains unavailable; these readbacks grant no admission
or financial execution authority.

Service-owned [paper proposals and abstentions](docs/reference/owned-paper-decisions.md)
retain native evaluations from owned strategy and price/calendar evidence.
They grant no admission, allocation or execution authority.

The internal [paper fill method](docs/reference/paper-fill-method.md) declares
spread, cost, latency, liquidity and stop behavior for replay and future ongoing
paper use. Its order journal grants no operational execution authority.
The internal allocation binding can release a wholly unfilled reservation only
from replayed cancellation or expiry evidence; filled-order protection stays held.

[Typed supervision calls](docs/reference/supervision-tools.md) let authenticated
conversation adapters inspect retained records, request research, change research
watch policies, declare inactive bounded copy-follow intent, inspect source
applicability, recheck exceptions and submit finalized-decision claim reviews
through the existing owners. They make no model calls and grant no financial
authority; conversation UI, prose interpretation and case triage remain open.
The typed inspector also reads finalized decision explanations and retained
market evidence through their existing owners, without a model request or
admission/effect authority.
`supervision-mcp` exposes these same owners through a bounded stdio MCP bridge
for conversation clients; it requires the expected receiver and existing API token.

The owner rechecks source-occurrence aliases when replaying v5 comparisons, so
recomputed portable hashes cannot bypass the append-time source identity rule.
Earlier record contracts retain their historical bytes and retries.

The existing allocation-comparison read also returns policy-bound capital
readbacks from registered opening capital or replayed deferral journals. These
are read-only projections of that owner's implemented state, with no funded
execution or qualification authority.

[Service-owned paper attribution](docs/reference/paper-attribution.md) joins
finalized decisions, retained forward-price assessments and allocation choices
in one consistent read, including retained admission-readiness gates and owner
bindings for allocation deferrals. Cost ceilings remain separate from expenses; unavailable
execution, PnL and economic baselines remain explicit. The `paper-attribution`
CLI reads this service view as a table or complete JSON; selected rows require
their attribution hash and the expected receiver identity. The current v3
readback includes each decision's complete retained non-grant admission-review
chain and current review ID. Review status and comparison pins remain visible;
admission, execution and economic effects remain unavailable.
It also joins actual post-decision claim reviews and social/copy source delays.
Earlier, equal and later source clocks are distinguished without claiming that
the source was a decision input, an independent sample or a financial fill.

The internal [entry-fill binding](docs/reference/allocation-ledger.md#replayed-entry-fill-binding)
derives allocation debits from replayed shared paper fills under existing
reservations and an exact owner model. It stages changes without a storage
write; admission and atomic durable execution remain required before activation.
The companion [exit-fill binding](docs/reference/allocation-ledger.md#replayed-exit-fill-binding)
derives closing cost basis and net proceeds from terminal entry history and
replayed sell fills. It conserves partial-close basis and refuses unfunded fees
without creating a public balance writer or granting execution authority.
A [fully closed position release binding](docs/reference/allocation-ledger.md#fully-closed-position-release-binding)
stages unused funding only after exact entry debits and complete exit closes are
already posted. It remains internal; durable owner transactions and protective
reconciliation are still required.

[Owned paper evaluation declarations](docs/reference/paper-evaluation-declarations.md)
pin planned variant families, future observation windows, owner models, baselines
and sensitivity/sample criteria before evaluation. New declarations pin distinct
native configurations from actual strategy owners; different IDs cannot turn
identical configurations into parameter variants. Registration retains a plan;
it supplies no result, admission or qualification credit.
The evidence view also checks a necessary predeclared sample-count bound.
Too few observed occurrences can establish insufficiency; meeting the count
still leaves independence unproven and grants no admission or qualification.
The declaration's [owner-selected evidence view](docs/reference/paper-evaluation-declarations.md#owner-selected-evidence)
resolves actual family decisions and forward outcomes in one consistent read,
retaining exclusions and duplicate-source lineage without scoring qualification.
Its v2 response keeps post-deadline attempts in `late_outcomes`; only outcomes
available by the predeclared deadline contribute to the observed-decision count.


Owned allocation comparisons now bind each admission-readiness receipt to its
actual owner-selected account, policy, proposal, execution-cost receipt (when
present) and resulting ledger head. New v9 comparisons retain v3 readiness with exact assessment decision-selection evidence, the complete owner declaration catalogue, and compact pins to actual replayed assessments at their comparison clock;
[the schema-49 fence and historical declaration context](docs/reference/owned-allocation-comparisons.md#captured-evaluation-declaration-context)
preserve prior records without granting admission or financial execution. Issue
#19's admission issuer, effect ownership and atomic durable financial execution
remain unfinished.

New allocation comparisons reject service-clock rollback before the collector
state read by their owner transaction. The same command can be retried at the
source clock after rejection; historical receipts and exact retries retain their
original time. This strengthens the existing deferral boundary without issuing
admission or financial effects.
Allocation reads validate owner-derived commands and replay each policy ledger
once, retaining every historical prefix hash and accounting check. This removes
repeated prefix evaluation without changing retained bytes or granting authority.


Allocation owner failures now return zero receipt/catalogue values, including
when PostgreSQL rejects a write at commit time. A transport failure can still
leave the commit outcome uncertain; inspect the owner journal before retrying
an exact request. See [commit acknowledgment](docs/reference/owned-allocation-comparisons.md#commit-acknowledgment-and-failed-reads).


Paper attribution and its terminal readback now expose the evaluation-declaration
context retained by v6-v9 allocation comparisons. Captured plan pins remain
`declared_not_evaluated`; absence remains `declaration_unavailable`. This adds
traceability without evaluation, admission, execution or qualification authority.
See [paper attribution](docs/reference/paper-attribution.md).

[Predeclared entry replay](docs/reference/paper-evaluation-entry-replay.md)
binds canonical retained decisions, actual historical outcome sources and declared
base/stress models in one owner snapshot. Each variant shares occurrence liquidity
among its entry orders. This bounded read reports entry counterfactuals and explicit
source gaps; exits, portfolio economics, admission and durable effects remain
unavailable.

[Protective counterfactual replay](docs/reference/paper-evaluation-protective-replay.md)
uses the same atomic owner sources to derive holding timers from modeled fills,
seal partial entries and replay stop/target/timeout market exits with shared
liquidity. Missing post-trigger observations and short source cuts leave exposure
unclosed. These unfunded counterfactuals do not complete portfolio economics,
admission or durable financial execution.

[Finite-capital counterfactual replay](docs/reference/paper-evaluation-capital-replay.md)
applies actual opening cash and policy budgets before replay, including modeled
exit-fee shortfalls. Unfunded candidates cannot consume fill liquidity; cash,
partial cost basis and realized net reconcile. This no-reinvestment study keeps
marked equity, drawdown, passive benchmarking and completed evaluation unavailable,
and does not write operational capital or admissions.

[Owned paper evaluation assessments](docs/reference/paper-evaluation-assessments.md)
retain service-derived pending, failed and incomplete checks against actual
predeclared owner evidence. They grant no admission, execution or qualification.

Allocation comparison operations also replay retained assessment owners against
actual declaration, decision and outcome evidence in the same snapshot before
returning a receipt. This preserves existing comparison hashes and grants no
admission or financial authority.

New allocation assessment context uses the [schema-51 startup fence](docs/reference/owned-allocation-comparisons.md#retained-assessment-context). Pending, incomplete and blocked assessment pins remain non-authoritative; they grant no admission, execution or qualification.

[Readiness v3](docs/reference/owned-allocation-comparisons.md#exact-decision-evidence-in-readiness) distinguishes actual decision selection from matching assessment scope, with a schema-52 writer fence. It grants no admission or financial authority.

[Service-owned admission reviews](docs/reference/owned-admission-reviews.md)
retain non-grant outcomes and exact predecessor chains from actual finalized
proposals and owner-replayed readiness. Schema 53 adds durable review retention;
positive admission, retirement and financial effect authority remain unfinished.

[Retained outcome market evidence](docs/reference/owned-outcome-market-evidence.md)
includes quote-specific regular-session context at the service availability cut,
with calendar custody pins and explicit pending-publication gaps. It
exposes historical quote dimensions, publication/admission clocks and quality
gaps from actual owner-replayed outcomes. It supplies no execution, continuous
coverage or qualification authority.

Native paper evaluation assessments now derive warm-up applicability from the
actual predeclared strategy owner. The compiled stateless threshold predicate
requires no lookback; this does not complete the other evaluation checks or
grant admission, execution or qualification. Recipe v2 preserves historical v1
assessments, and schema 54 fences older writers. See
[owned evaluation assessments](docs/reference/paper-evaluation-assessments.md).

Native paper evaluation recipe v3 also derives forecast-calibration applicability
from the exact registered threshold method. Its boolean signal has no probability
forecast to calibrate. This method applicability grants no performance result,
admission, execution or qualification. Historical v1/v2 records keep their bytes
and checks; schema 55 fences older writers. See
[owned evaluation assessments](docs/reference/paper-evaluation-assessments.md#native-forecast-calibration-applicability).

Collector source operations now return no receipt or snapshot on PostgreSQL
COMMIT failure, including exact retries. Reconcile uncertain outcomes through
the actual owner and the same immutable identity; see
[collector submission recovery](docs/reference/collector-submission.md#transaction-acknowledgement-failures).

Finalized native decision bases now enforce service custody timestamps during
creation and owner replay. Later collector clocks, registrations, input admissions
or raw publications cannot enter an earlier decision through rehashed basis bytes.
See [decision source custody](docs/reference/owned-paper-decisions.md#source-custody-times).

New collector/source writes must occur strictly after retained decision, outcome
and assessment clocks, under the shared owner lock. Earlier or equal clocks fail
without consuming an identity; exact historical retries keep their meaning.
Schema 56 fences older writers without rewriting history. See
[the retained observation clock fence](docs/reference/collector-submission.md#retained-observation-clock-fence).

Owned evaluation recipe v4 also retains known temporal-source leakage failures;
[assessment semantics](docs/reference/paper-evaluation-assessments.md#known-source-unavailability-leakage)
remain failure-only and grant no admission or economic authority.

A prospectively selected [native feature-causality recipe](docs/reference/paper-evaluation-assessments.md#native-feature-causality)
now derives a passing leakage check for owner-replayed, causally available inputs
to the fixed native threshold predicate. Empty evidence remains unavailable and
known leakage remains failed. The other economic/statistical checks still gate
complete evaluation and paper eligibility.

A [predeclared disjoint-window sample design](docs/reference/paper-evaluation-assessments.md#disjoint-sample-design)
now derives closed observation intervals from actual feature, decision and safety
owners. Overlapping episodes cannot satisfy its declared disjoint minimum by
increasing the raw occurrence count. Meeting the structural count leaves
statistical independence and complete evaluation unavailable.

A [predeclared registered-search bound](docs/reference/paper-evaluation-assessments.md#registered-search-history)
counts every strategy the receiver registered by the assessment clock, declared
or not. Exceeding the bound fails the search-multiplicity check and blocks the
assessment. Staying within it leaves the check unavailable: a count is not a
multiplicity-adjusted result.

New v10 comparison records retain v4 admission readiness with the exact failed
checks of each latest owner-replayed assessment for the selected variant. For
selected decision evidence, any retained necessary failure dominates pending
sample status; absence of failures still grants nothing. The review consumes
that readiness and preserves expiry precedence. Historical v9/v3 records retain
their original bytes and clock-free retries. Schema 62 fences older writers
without rewriting payloads; rollback requires the matching backup and binary.

Owned allocation comparisons also bind [finalized strategy execution requirements](docs/reference/owned-allocation-comparisons.md#finalized-strategy-execution-requirements)
to the retained decision, policy and cost ceiling. The stop, target and timing
limits remain requirements only; they grant no admission or financial effect.

The disposable integration suite checks [restored storage-owner replay](docs/reference/storage-owner-recovery.md),
including deferred allocation requirements, clock-free retries, corrupted-owner
rejection and restoration of the original payload. These are storage-fixture
recovery checks, with no financial recovery or qualification claim.

New allocation comparisons bind owner-derived execution-safety requirements
inside admission readiness, alongside account, policy, cost, evaluation and
ledger pins. See [owned allocation comparisons](docs/reference/owned-allocation-comparisons.md).
This is a requirements binding; admission and economic execution remain unavailable.

Typed supervision can inspect retained allocation comparisons, non-grant
admission reviews, and decision attribution through fixed owner GET routes.
See [supervision tools](docs/reference/supervision-tools.md) for the read-only
contracts and response limits.


Owner comparison readiness retains known failed assessment checks even when
that exact candidate was excluded because its source was unavailable at
finalization. New v14 comparisons/readiness v6 bind the failure to the owner
assessment and block admission review; historical records keep their original
replay behavior. This does not issue admission or financial effects. See the
[owned allocation contract](docs/reference/owned-allocation-comparisons.md#known-failures-for-excluded-candidates).


A retained social post can explicitly request pending owner research with
`request_research: true` under an enabled research watch. The request pins the
source and policy, deduplicates repeated normalized claims, and is retained
atomically with the recording. It runs no worker/model and grants no dispatch,
admission or financial authority. See [social research requests](docs/reference/owned-social-watches.md#explicit-owner-research-requests).

Already-retained social material can be selected with the typed
`request_social_research` tool or `POST /v1/social-research-requests`, using the
capture ID and exact receipt hash. The owner checks current source/policy
applicability and retains a pending request without rewriting the source.

Social research requests require current, causal source applicability at issuance,
including the canonical source reused for deduplication. Invalid repost lineage
is rejected atomically; historical receipts and exact retries remain immutable.
See [owned social watches](docs/reference/owned-social-watches.md).


Retained copy observations under a current research-only watch can use the same
ID/hash research selector. The owner records a separate versioned pending copy
research request, rejects stale, superseded, terminal or reversed activity, and
preserves the original capture. Source mapping and quantity claims remain
unverified; this does not activate copy following, dispatch or financial effects.


Typed supervision can inspect retained outcomes, their market evidence, evaluation
declarations and evaluation assessments through `inspect_record`. These selectors
return the existing owner readbacks, preserving the distinction between quote
observations, planned checks and retained assessment results. They accept no
replacement evidence or financial instructions. See the
[supervision tool contract](docs/reference/supervision-tools.md).


The compiled `serve` assessment owner now retains v6 base/stress portfolio
accounting from its actual policy, source cut and verified counterfactual books:
cash, whole-share cost basis, realized net proceeds, observed bid equity and
event-path drawdown. Stale marks remain unavailable and open positions are not
liquidated for reporting. Studies containing quotes received after their command
clock are rejected before valuation or benchmark comparison. Causal historical
studies keep their original bytes and hashes.
Corporate entitlements, passive total return and complete statistical evaluation
remain unfinished; this accounting grants no admission, execution or qualification.
See [retained observed portfolio accounting](docs/reference/paper-evaluation-assessments.md#retained-observed-portfolio-accounting).

Prospective [training/holdout plans](docs/reference/paper-evaluation-declarations.md#prospective-training-and-holdout-partitions)
now retain owner-derived temporal partitions. Horizons or labels crossing the
split and quotes carried into holdout are excluded with the declared embargo.
Closed partitions below their declared occurrence minima block the necessary
out-of-sample check; sufficient counts do not establish independence or performance.
Original plans and historical assessments keep their methods and hashes.

A [prospective observed benchmark recipe](docs/reference/paper-evaluation-declarations.md#prospective-observed-benchmark-method)
now binds finite-capital cash and passive price-path accounting to retained
assessments. It preserves partial/unfilled orders, source hashes, fees and
unavailable marks. The comparison remains insufficient for total-return
acceptance without documented corporate coverage and reconciliation; observed
price differences issue no admission or execution authority.

`serve --automatic-paper-evaluations` optionally retains the existing necessary
checks once a predeclared outcome deadline has closed. It uses stable owner
identities, live write clocks and atomic assessment storage; failed or incomplete
checks stay failed or incomplete. The worker is disabled by default, invokes no
provider/model, and grants no admission, execution or qualification. See
[automatic necessary-check assessments](docs/reference/paper-evaluation-assessments.md#automatic-necessary-check-assessments).

Typed supervision can inspect a declaration's current owner-selected evidence
with `inspect_record` kind `evaluation_evidence`. This service-clock read exposes
exclusions and missing outcomes without creating an assessment or granting
admission; see [supervision tools](docs/reference/supervision-tools.md).

Disposable owner recovery also retains and replays blocked evaluation plans and
assessments, including exact retries and rejection of a self-rehashed admission
claim. See [storage owner recovery](docs/reference/storage-owner-recovery.md).
This fixture coverage does not qualify economic or protective recovery.

Decision and outcome market-evidence v3 reads now derive documented share sizes
from actual retained quote owners using a pinned [Alpaca REST unit contract](docs/reference/alpaca-quote-share-units.md).
Historical transition-day/unsupported values remain explicit, and evidence
classes, admission, financial execution and qualification remain separate gates.
## Predeclared corporate acquisitions

Evaluation declarations may predeclare intended corporate-action acquisition
identities and query scopes. The service resolves those intents through the
[assessment corporate selection readback](docs/reference/paper-evaluation-assessments.md#predeclared-corporate-source-selection).
This is source selection only; economic evaluation and admission remain unavailable.


The [conversational supervision client](docs/reference/supervision-tools.md#conversational-tool-routing)
accepts operator messages and routes one typed owner call per message through a
configured Switchyard model. It displays original owner results, defaults to
inspection and retains private request/result receipts. `--allow-writes` exposes
only the existing bounded supervision operations; financial authority remains
with the existing service owners. Actual conversation and model-outage release
acceptance remain unfinished.

Social and copy source research requests can be explicitly dispatched into owner-bound research cases; see [social watches](docs/reference/owned-social-watches.md). Source assertions retain their unverified status and confer no financial authority.

`mantik serve --automatic-paper-decisions` finalizes a decision for each declared strategy on each new quote from a matching feed registration, and compares every entry candidate no comparison pins yet. It is disabled by default, reports under its own `paper_decision` readiness scope, and stops at the existing decision and feed retention limits. See [automatic decisions](docs/reference/owned-paper-decisions.md#automatic-decisions).

The [isolated runtime rehearsal](docs/reference/runtime-rehearsal.md) prepares a
new PostgreSQL-backed service with SPY/IEX declarations and advisory Switchyard
reviews. Its model-connection outage affects only a rehearsal-owned proxy;
feed pauses and restart checks use only its recorded processes. Preflight,
live-session observations and qualification remain separate evidence. The
[continuous runtime plan](docs/plans/continuous-paper-runtime.md) tracks the full
archive and remaining #24 attribution work.

[Feed archive custody](docs/reference/feed-archives.md) seals publications,
collector history and routed inputs in one receiver-owned snapshot, with exact
source-cut retries and paged archive headers. The source-cut endpoint keeps
active copies; explicit rotation can remove sources after verifying their
original custody and retained dependencies.

Schema 101 adds original-ID archive lookup and a full source/index integrity
audit to that custody contract. Snapshot, index and manifest commit together;
older snapshots are backfilled without changing their hashes.

Schema 102 retains the same v3 registration-prefix counts and hashes in the
archive, with nanosecond cut indexes, atomic manifests and source-backed audit.
These internal prefix reads support archive-aware replay without loading all
historical feed records.

Schema 103 adds paper source checkpoints to the same feed-archive transaction.
The owner replays retained decisions and outcomes against original archived
sources, seals their complete membership and fences subsequent backdated writes.
Authenticated inspection at `/v1/feed-archives/paper-custody/{sequence}` replays
that original cut. Finalized paper records remain retained in their active owners.

Schema 104 adds `POST /v1/feed-archives/rotate`: archive, verify paper-source
custody and retire the feed working set atomically, retaining current coverage,
lease proof, pending admissions and routing references. Original IDs remain
reserved and retry against their archived receipts. New paper v4 records can
recover original forward observation windows across retired source segments;
existing paper envelopes and hashes are unchanged. The authenticated
`/v1/feed-archives/paper-custody-audit` independently replays original sources.
Rotation is explicit; physical paper/assessment/review archival and automatic
continuous retention remain unfinished.

[Paper record custody](docs/reference/paper-record-archives.md) adds immutable
decision, outcome, assessment and review snapshots with original-ID lookup and
native source replay. Schema 105 records creation epochs and binds subsequent
paper-source checkpoints to their actual archive epoch, preserving earlier
envelopes and hashes. The authenticated `/v1/paper-record-archives` routes expose
creation, paged headers, targeted originals and full audit. Active paper records
remain retained; physical retirement and automatic retention are still pending.

`mantik serve --automatic-social-research` opts into bounded dispatch of retained social/copy research requests. It is disabled by default and has an independent `social_research_dispatch` readiness scope; it grants no financial authority.
