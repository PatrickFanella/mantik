# Mantik

Mantik is a terminal-first system for paper-trading research. It keeps
research, decisions and operations separate, retains the evidence behind each
step, and lets a person supervise everything from a command-line interface.

Mantik is in early development and trades on paper only. There is no live
trading, and no strategy in this repository is qualified as profitable. The
built-in demo runs entirely on a synthetic instrument (`DEMO`) with fixture
quotes and zero fees or slippage.

## Features

- **Synthetic paper lifecycle**: accept fixture quotes, evaluate an entry,
  finalize a decision with safety conditions, and simulate the entry and a stop,
  target or deadline exit.
- **Persistent service** (`mantik serve`): an HTTP service backed by
  PostgreSQL, with bearer-token authentication, a timer for holding deadlines,
  and retained audit events, exceptions and recovery attempts.
- **Supervision from the terminal**: `status`, `activity`, `cases`,
  `exceptions`, `library` and `operations` read the service state, and
  `supervise` gives interactive read-only views of activity, cases, news,
  portfolio, decisions, exceptions and the method library.
- **Conversational supervision**: `converse` routes operator messages through
  model-selected supervision tools, and `supervision-mcp` serves the same typed
  tools over MCP stdio.
- **Replay and experiments**: `replay` runs a sealed synthetic dataset at
  explicit information cutoffs, and `experiment` compares a threshold baseline
  against passive and no-action controls while retaining the recipe and results.
- **Market and reference data**: bounded, explicit reads from Alpaca (latest US
  equity quotes, regular-session calendar, corporate actions) and Polygon
  (dividends and splits) into private append-only journals, with archive,
  restore and coverage commands.
- **News research**: retain articles and claims, extract claims with a recorded
  or local model, check them against source excerpts, and open news research
  cases with reviews.
- **Hypotheses and trials**: declare a hypothesis, register trial plans, record
  attempts and compare their metrics with the declared conditions.
- **Candidate strategies**: seal candidate source with pinned datasets, compile
  and run it in credential-free containers, and compare its outputs with
  declared expectations.
- **Daily strategy engine**: next-session fills with costs, corporate actions,
  deposits and time-weighted returns. The [account rules](docs/reference/account-rules.md)
  enforce settled-cash spending, margin limits and coded refusals on this
  simulated book.
- **Recorded strategy trials**: the [trial registry](docs/reference/strategy-trials.md)
  saves attempts before execution, retains daily returns and outcomes, and
  counts failed and unfinished attempts in each strategy family's search total.
  [Walk-forward research](docs/reference/backtest-research.md) adds purged folds,
  cost stress, declared neighbours, DSR/CSCV and per-strategy minimum capital.
  [Recorded gates and portfolio risk](docs/reference/portfolio-gates-and-risk.md)
  preserve pass/fail reasons, forward-only model admission, sleeve sizing and
  persistent order brakes.
- **Multi-leg paper orders**: the [durable options lifecycle](docs/reference/alpaca-mleg-paper.md)
  requires an explicit versioned paper atomic-net-premium contract, admits up to
  four protected option legs against dedicated local funding, and reconciles
  whole closes before a shorts-first fallback. Package limits and fixed modeled
  fees bound the paper budget; separate orders cannot spend package credits.
  Legacy policy inputs remain inspection-only and contradictory fill evidence
  stays quarantined across restart. Interrupted reply classification also blocks
  mutations until inspected; a later GET cannot clear that fence.
  New opening IDs reserve the internal `mleg-` prefix to avoid custody collisions.
  Synthetic tests cover recovery and protection; real-paper acceptance evidence
  remains a separate regular-session check.
- **Paper stock orders**: the separate [Alpaca paper adapter](docs/reference/alpaca-paper.md)
  submits once per durable client ID, recovers ambiguous submissions, retains
  raw fills and reports account-position differences. The
  [capped equity book](docs/reference/capped-equity-book.md) reserves local
  capital before submission and reconciles its own fills and scheduled funding.
  Strategy orders must match a retained distributor plan and pass the fixed
  exposure, cash, loss and emergency-control checks before submission.
  The opt-in [incubation runner](docs/reference/incubation.md), including a
  supervised terminal mode with a separate owner-configured safety source, runs eligible
  strategies on retained prospective quotes and reports the worse venue/model
  fill result, with no qualification credit. It preserves explicit flat exits
  for retained owners with stale research and rechecks quote expiry immediately
  before the paper adapter's order POST. Cycle health blocks new risk on stale
  daily data or unknown reconciliation, worker liveness or risk evidence, while
  protective flattening, fill reconciliation and settlement remain independent.
  New risk also requires an active service safety worker bound to that book,
  paper account and protection policy; generic demo worker success and a
  standalone cycle invocation cannot supply this authority. Reconciliation
  retains its exact journal version and refuses concurrent economic changes.
  Continuous risk observations retain immutable originals behind a bounded
  replay checkpoint, preserving observed peaks and brakes across restarts.
  SQL reads verify replay transitions before trusting a head; a private cache
  bounds later verification to the unseen command suffix.
  Authenticated per-book readiness reports admission for each work class.
- **Kalshi data**: [public collection](docs/reference/kalshi-data.md) retains
  weather-market rules and order-book observations for point-in-time reads.
  [Weather probabilities](docs/reference/kalshi-weather.md) retain pre-close
  next-day forecasts and report Brier scores and buckets after final settlement.
  [Calibrated maker decisions](docs/reference/kalshi-maker.md) place fee-aware,
  quarter-Kelly post-only intents within the separate notional book.
- **Advisory research desk**: [bounded model tools](docs/reference/research-desk.md)
  compose inactive typed ideas, evolve and select variants using retained
  complexity/library-similarity penalties and count every proposal. Durable
  family fences block statistical and admission readers during interrupted
  batches. Post-cutoff simulations confer no gate or trading authority.
- **Callable model analyst**: the [four-stage analyst](docs/reference/model-analyst.md)
  uses subscription analysts, debate and trader views to select a constrained
  target that code sizes. Failed calls produce exposure holds; explicit flat
  remains a separate exit. Current budget previews include complete book marks,
  filled/pending commitments and review caps while model inputs stay frozen.
  Incubation resolves retained cycles by `AnalystCycleID`, retains provenance,
  and requires the existing supervised safety and final submission fences.
- **Subscription research calls**: the [model adapter](docs/reference/subscription-models.md)
  runs bounded Claude and Codex prompts using host logins, with a durable daily
  call budget and hash records, independently of trading.
- **Paper accounts and allocation**: operator-declared paper capital, immutable
  strategy definitions and allocation policies, a synthetic reservation ledger,
  and paged decision, outcome and allocation history.

Run `mantik help` for the full command list and `mantik COMMAND --help` for
flags.

## Quick start

Requirements: Go 1.26.9 (pinned in `mise.toml`), Python 3 for some checks, and
Docker with Compose for the service and integration tests.

```bash
git clone https://github.com/subculture-collective/mantik.git
cd mantik
make build
make demo
```

`make demo` needs no database or credentials. It prints JSON with the fixture
observations, finalized decisions and execution outcomes.

Replay the fixture at explicit cutoffs, or run the baseline experiment:

```bash
./bin/mantik replay --cutoffs 2026-01-05T15:01:00Z,2026-01-05T15:03:00Z
./bin/mantik experiment --attempt demo-1 --out-dir data/experiments
```

### Run the service

```bash
cp .env.example .env
make compose-build
docker compose up -d
export MANTIK_API_TOKEN=mantik-local-development
./bin/mantik status
./bin/mantik supervise --limit 10
```

Compose starts PostgreSQL on `127.0.0.1:45432` and the service on
`127.0.0.1:40880`. Change the ports in `.env` if they are taken.

### Checks

| Command | What it runs |
| --- | --- |
| `make verify` | gofmt check, `go vet`, Go tests, build, Compose validation and the Python script checks |
| `make test-race` | Go tests with the race detector |
| `make test-integration` | Persistence tests against a new disposable PostgreSQL container |

## Configuration

`.env.example` lists the local settings:

| Variable | Purpose |
| --- | --- |
| `POSTGRES_PASSWORD`, `POSTGRES_PORT` | Local PostgreSQL password and host port |
| `MANTIK_PORT` | Host port for the service |
| `MANTIK_API_TOKEN` | Bearer token for the service API and CLI |
| `MANTIK_TIMER_INTERVAL` | Deadline timer polling, from 100ms to 1m |
| `DATABASE_URL` | Database for running `mantik serve` outside Compose |

The service refuses to bind to a non-loopback address unless
`MANTIK_API_TOKEN` is at least 16 characters. Provider commands read
credentials from a file passed with `--credentials-file`, and model-backed
commands read `MANTIK_MODEL_TOKEN` or `MANTIK_LOCAL_MODEL_TOKEN`. The defaults
in `.env.example` are for local development only.

The background workers are off by default. Each is a `mantik serve` flag:

| Flag | Worker |
| --- | --- |
| `--automatic-paper-decisions` | Finalizes decisions on new quotes for declared strategies ([reference](docs/reference/owned-paper-decisions.md)) |
| `--automatic-paper-outcomes` | Retains sealed forward quote outcomes ([reference](docs/reference/owned-paper-outcomes.md)) |
| `--automatic-paper-evaluations` | Retains post-deadline assessments ([reference](docs/reference/paper-evaluation-assessments.md)) |
| `--automatic-social-research` | Dispatches retained social and copy requests into research cases ([reference](docs/reference/owned-social-watches.md)) |
| `--automatic-paper-retention` | Archives and retires sealed decisions and outcomes and rotates feed sources at half of each working limit ([reference](docs/reference/paper-record-archives.md#automatic-retention)) |

Retention does not cover assessments or decision reviews; they keep their
working limits. The retention worker grants no admission, execution or
qualification authority.

## Deploy

The Dockerfile builds a static `mantik` binary on Alpine and runs `serve` as a
non-root user. To run it elsewhere, provide PostgreSQL 17, set `DATABASE_URL`
and a strong `MANTIK_API_TOKEN`, and keep the service on a private network: TLS
and remote operator access are not built in.

## Project layout

| Path | Contents |
| --- | --- |
| `cmd/mantik/` | CLI and service entry point |
| `internal/` | Service, storage, CLI and one package per research area |
| `migrations/` | PostgreSQL schema migrations |
| `research/` | Backtesting fixture adapter |
| `scripts/` | Integration, rehearsal and release check scripts |
| `docs/` | Architecture and reference notes |

Useful reading: [architecture](docs/architecture.md),
[dataset and replay contracts](docs/reference/datasets.md),
[experiments](docs/reference/experiments.md) and the
[Alpaca adapter](docs/reference/alpaca.md). The
[documentation index](docs/README.md) lists the rest.

## Contributing

- Bugs and feature requests: [GitHub issues](https://github.com/subculture-collective/mantik/issues).
- Code: open a pull request on GitHub against `main`. See
  [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).

CI runs `make verify`, the image build, the race-detector tests and the
PostgreSQL integration tests on every pull request.

## License

GNU General Public License, version 3 only (`GPL-3.0-only`). See
[LICENSE](LICENSE).

Historical stock bars can be collected with [`bars-backfill`](docs/reference/daily-backfill.md), using a private resumable journal and an explicitly selected database.

The [`stock-assets` and `stock-universe` commands](docs/reference/stock-universe.md) retain active/inactive references and screen stored bars by asset identity with explicit survivorship limits.

[`options-backfill`](docs/reference/option-data.md) retains resumable contract listings and explicitly indicative daily option bars and trades in an isolated, append-only store.

Strategies can read [point-in-time indicative option chains](docs/reference/option-chains.md)
with explicit same-session spot inputs and reference-tested European-model IV and Greeks.
The [indicative option order simulator](docs/reference/option-structures.md) admits
up-to-four-leg structures, holds maximum-loss and partial-fill reserves, enforces
cash-book permissions and fixed Greek caps, and records conservative bar fills.
It grants no qualified or broker submission authority.
The [reviewed option recipes](docs/reference/option-library.md) select and size
covered calls, cash-secured puts and margin-only spreads, then run through that
simulator's admission, protected fills and closing. Their retained-data adapter
preserves acquisition clocks. A separate, offline
[retrospective research command](docs/reference/option-retrospective.md) runs
selected 2024-onward archived-price samples under an explicit availability
assumption, preserving original receipts and granting no qualification credit.
The pure [indicator adapter](docs/reference/indicators.md) provides SMA, EMA, RSI,
Wilder ATR, Bollinger bands and rolling return volatility with maintained TA-Lib parity tests.

The [stock strategy library](docs/reference/stock-library.md) adds reviewed trend,
rotation, reversal and low-volatility recipes with pinned upstream replication
tests. [Typed strategy specs](docs/reference/strategy-specs.md) compile bounded
expressions or library recipes into the same engine. The [Kalshi notional
simulator](docs/reference/kalshi-simulation.md) consumes recorded depth and fee
evidence in a separate book, excluded from the portfolio's SPY measure.

The [minimum-capital reporter](docs/reference/strategy-minimum-capital.md)
derives complete fractional recipe baskets at one reviewed snapshot and proves
their financial cent boundaries without changing failed gates. The separate
[Kalshi paper check](docs/reference/kalshi-paper-check.md) retains a diagnostic
notional fill and joins its official settlement to the original forecast and
canonical calibration; its first real-source outcome is still pending.

Daily stock history can be collected by the opt-in [`serve` daily-bar worker](docs/reference/daily-bars-worker.md), with missing-session catch-up and its own readiness scope. The [independent SPY comparison](docs/reference/spy-comparison.md) checks the retained history against another source; its explicit primary-close check passes at the unchanged 5 bps tolerance, with the original raw-SIP and Yahoo-only failures retained as diagnostics.

The [SPY engine check](docs/reference/spy-engine-check.md) compares cash/margin
accounting with scheduled deposits against an independent reference on the
manifest-verified 2016-to-date history.

[Daily strategy oversight](docs/reference/daily-strategy-review.md) adds authenticated, retained evidence reviews and owner retirement. Review reductions take effect on a strictly later decision/rebalance; growth stays inside the existing admitted stage and grants no qualification. Missing comparisons hold or request inactive investigation.

[Owner daily briefs](docs/reference/daily-brief.md) freeze a finalized book prefix for terminal output and an explicitly configured notification outbox. Deposit-adjusted TWR needs exact flow-boundary valuations; SPY needs comparable sourced total-return evidence. Missing evidence remains unavailable. Delivery retries retain one payload and idempotency key; no model, host timer or trading authority is added.

Canonical strategy forward windows use the separate [forward declaration contract](docs/reference/forward-declarations.md). It binds prospective clocks, its own benchmark, minimums and actual trial-family search bound to the capped-book runner, with multiweek holds and no qualification credit. Legacy threshold replay retains its original rules.

Prospective strategy qualification uses [predeclared assessments](docs/reference/forward-assessments.md), distinct execution evidence, and automatic paper sleeve admission. Incubation and synthetic fixtures earn no qualification credit.

Qualification collection is accepted only by the compiled durable collection
path, which recomputes pinned inputs and decisions. Ordinary journal commands
cannot supply collection authority; see [forward assessments](docs/reference/forward-assessments.md#durable-collection-authority).

The [`book` terminal controls](docs/reference/owner-book-controls.md) show capped
paper equity, sleeves, lifecycle and retained refusal reasons. The configured
owner service session can confirm pause, permanent retirement, one real cash
receipt or hard-halt clear with immutable action IDs and version fences. Real
receipts require a new immutable `owner_receipts` funding mode; legacy synthetic
books cannot mix in real receipts, and receipt-mode workers never auto-credit
missed schedule slots. Missing
owner authentication refuses mutations even in development. The isolated
walkthrough is automated evidence; the owner's terminal checkpoint is outstanding.
