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
