# Advisory research desk

`mantik research-desk` runs the roadmap #86 desk through the owner's existing
Codex or Claude subscription. The desk composes typed definitions from
`internal/strategy/spec` and the reviewed stock library. It only creates
inactive research ideas. It cannot write a gate decision, approve a source,
activate a strategy, change capital or risk limits, size a live position, or
submit a broker order. Operations does not depend on this package.

## Invocation and trust boundary

The CLI accepts `--action run`, `show`, or `trials`, an immutable `--id`, and
`--database-env`, the name of an explicit PostgreSQL URL environment variable.
`run` additionally requires `--input`, `--input-sha256`, and `--journal`.
The input is a bounded private JSON `research.Declaration`; the SHA-256 flag
must match its exact file bytes. The journal is the stable, mode-0700 private
subscription budget directory used for all calls by this desk. Rotating that
directory creates another budget and must not be used to bypass a limit.
Do not point an exercise at development, production, or Augr databases.
Use a new disposable `mantik_test_*` database and keep its dump outside Git.

The invoking owner, scheduler or overseer adapter is trusted to authenticate
its caller and review the declaration **before** supplying the external hash.
The CLI is a local operator interface, not an unauthenticated trigger endpoint.
A hash proves byte identity; it does not prove the truth of a source or an
actor's identity. Model tools cannot supply or change the declaration.

The declaration freezes these fields:

| Field | Required content |
| --- | --- |
| `id`, `family` | Session identity and the search family whose trial count includes every attempt |
| `identity` | Exact provider and model ID, inclusive training cutoff date, official provider source URL, reviewed metadata receipt and its exact-byte SHA-256 |
| `trigger` | Kind, occurrence/request/finding ID, time, exact receipt and its SHA-256 |
| `data` | Complete selected raw daily bars, asset-keyed corporate actions, retained source description/hash and evidence class |
| `as_of` | Frozen data clock, no later than the actual session clock |
| `max_calls` | At most 12 model turns, one tool per turn |
| `daily_budget` | At most 100 subscription calls per UTC day in the stable adapter journal |
| `timeout_seconds` | 1–120 seconds per model call |
| `goal` | At most 2,048 bytes of untrusted research context |
| `gate_ids` | At most 16 separately authored gate reports the model may inspect |

`identity.metadata` has `provider`, `model`, `cutoff`, `source`, `reviewed_by`
and `reviewed_at`. The operator must inspect trustworthy configuration or
provider metadata and retain the source before freezing the receipt. Unknown,
missing, altered or mismatched cutoffs fail before storage or model invocation.
The date is inclusive: a cutoff of April 30 excludes all April 30 observations.
No cutoff is inferred from a model response, model name, or current date.
Keep the original declaration file: the nested raw receipt hashes bind its
original JSON bytes, even though PostgreSQL normalizes journal JSON for reads.

`trigger.receipt` has `kind`, `id`, `at`, `actor`, and `action: "research"`.
The receipt must agree with the wrapper ID, kind and time and cannot come from
the future. Kinds are `owner_request`, `schedule`, or `overseer_finding`.
A schedule requires `scheduled_for` equal to its occurrence clock and
`finalized: true`; an overseer finding requires `finalized: true` and no
schedule clock. An owner request has no schedule clock. Timers and overseer
services invoke this same bounded path with their authenticated receipts;
this change does not install a host timer or a model-facing write endpoint.

## Tools and evidence

The subscription receives a fixed protocol: exactly one JSON request per
turn. Native Codex shell/execution, browser, computer, apps, MCP, plugins and
delegation tools are disabled for desk calls. Host skill discovery is skipped.
Claude retains the adapter's restricted safe mode with all native tools disabled.
No API-key fallback exists; the existing adapter removes API-key and token
variables and enforces call budgets, persisted backoff and process deadlines.
The model ID is passed explicitly to the subscription CLI and hash journal.

| Tool | Scope |
| --- | --- |
| `read_library` | Reviewed stock recipes and the typed operator vocabulary |
| `query_data` | At most eight tail bars for one asset in the frozen manifest |
| `read_journal` | This session's attempted specs, outcomes and daily returns |
| `compose_spec` | Retains and validates one inactive typed definition |
| `run_backtest` | Retains one attempt, runs the existing engine and stores daily returns |
| `read_gate` | Reads an allowlisted report on the identical manifest, with post-cutoff calendar and benchmark dates |
| `done` | Ends the session |

There are no free-form commands, URLs, code uploads, gate-writing tools,
source-review tools, account controls, budgets, order quantities or activation
actions in this dispatcher. A spec's `gross` is a simulated recipe weight;
it cannot change the fixed simulation balance or any live allocation.
Backtests use the existing engine, a fixed $2,000 cash account, default
next-open costs, no deposits, and unlevered targets. Only daily-bar definitions
can run against this manifest; chain/universe operators are retained as failed
attempts when their data kind is unavailable. Every attempt remains in the
search count. A compose success has no return series; a backtest is a separate
attempt and can name the compose trial as its parent.

All bars, warm-up dates and corporate-action ex-dates must be strictly after
the model's cutoff. Publication, observation, availability and action knowledge
clocks are checked against the cutoff and frozen `as_of`. Future revisions,
duplicate asset/date revisions, and pre-cutoff adjustment inputs fail closed.
The manifest is limited to eight assets and 2,000 raw bars. Point-in-time reads
filter bars by availability and session date before adjustment. Original source
bytes stay in private custody and are referenced by the source hash; the desk
does not fetch or purchase data. The source reviewer remains responsible for
coverage, revisions and authenticity. `historical_market_unqualified` and
`synthetic_fixture` remain distinct, and neither gives qualification credit.
The journal intentionally excludes other sessions' evidence that might have a
different model cutoff. Evolution across sessions remains separate work.

Duplicate JSON object members are rejected before decoding can replace an
earlier value. Multiple top-level `spec` members each create a failed trial;
none is executed. The first keeps the usual session/turn trial ID, and later
candidates have `-spec-2`, `-spec-3`, and subsequent suffixes. Invalid parent
lineage, including a parent whose start follows the reply, is removed from the
trial's lineage and retained in the original attempted command. It still counts
as a failed attempt.

Bar availability must be at or after actual observation as well as publication.
A capture observed later cannot be backdated into a historical decision view;
an invalid ordering is rejected before any session event or model call.

## Durability and recovery

`0123_research_desk_events.sql` is installed by the normal storage bootstrap
under its migration lock and recorded in `mantik_schema`. The CLI opens that
storage before opening the research journal. `research.OpenPostgresJournal`
requires an initialized schema and performs no independent DDL. The migration
can be replayed while retaining existing events.

The append-only SQL journal retains the declaration, a call intent before each
subscription launch, exact successful reply text before dispatch, each tool
result and terminal status. Ordinary `show` emits only hashes, counts and trial
IDs. Explicit `trials` exports attempted commands/specs and returns;
redirect it to a private evidence file. Commands over 8 KiB have a bounded
prefix and full-reply hash in the trial; their exact bytes remain in the session
reply event. Raw replies and source data are private
research inputs and must never be committed.

When the subscription transport rejects a call that produced stdout, the
adapter returns at most 65,536 bytes as private evidence. The desk retains that
bounded output and its SHA-256 in a rejected reply record, registers one failed
attempt conservatively, and ends with `model_unavailable`. That hash covers
the retained bytes; it does not claim to cover any discarded suffix. Rejected
output is marked incomplete and never dispatched, even if it contains a valid
tool request. Provider diagnostics remain excluded.

A PostgreSQL advisory lock fences each session across processes. Exact event
and trial retries are idempotent; changed declarations or event writes conflict.
A retained reply without a result is recovered through the dispatcher without
calling the model again. If its trial already finished, it is read back. If it
started without an outcome, recovery marks it crashed and does not rerun it.
A retained terminal tool result completes its missing terminal event without
another model call. Rejected-output replies recover only the failed attempt
and terminal status, never tool execution. All event clocks use microsecond
precision before retention and hashing, matching PostgreSQL round trips.
A call intent without a retained reply stops with `ErrInterrupted`; the system
cannot know whether that subscription request ran. It never relaunches that
uncertain request. Starts and partial stored returns still count toward search
multiplicity. Invalid tool JSON and invalid specs are retained as failed trial
attempts, rather than disappearing in the parser.

Successful backtests retain every returned daily observation. A process crash
inside the engine can leave an unfinished start with no returned daily series;
that attempt still counts. No unfinished or completed desk trial grants
incubation. Source review and gate evaluation run under separate authorities.

## Verification

Focused tests cover invalid and failed attempts, unsupported money tools,
post-cutoff warm-up and adjustment bounds, future clocks, trigger receipt
binding, scoped gate reads, call limits and recovery without relaunch.
Run `mise exec -- go test -race ./internal/research ./internal/model` with
`GOFLAGS=-p=2 GOMAXPROCS=2`. `TestPostgresContractsResearchDesk` runs in the
existing disposable CLI integration shards through `make test-integration`.
For a focused run, `MANTIK_TEST_DATABASE_URL` (or `MANTIK_DESK_TEST_DATABASE_URL`)
must point at a newly created disposable database;
it checks SQL immutability, migration replay, restart identity and session
fencing. The combined Wave10 owner runs full `make verify`, repository race and
integration checks after composing the sibling changes.

On Kvant on 2026-11-16, one real GPT-6.1 Sol subscription session completed
with eight calls and four listable trials: one failed spec, two inactive ideas,
and one completed engine run retaining 45 daily returns. Its retained SPY
source covered September 14 through November 13, after the provider's
published April 30 cutoff. Because those historical bars were first observed
at capture time, their availability was conservatively set to capture time.
The historical engine views therefore made zero decisions and zero fills.
Those all-cash returns are tool/retention evidence, not historical performance,
source approval, gate admission or qualification. Private source, trial and dump
paths and hashes are supplied to the coordinator outside Git.
