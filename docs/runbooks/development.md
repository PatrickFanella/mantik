# Development and verification

## Toolchain and checks

Use `mise install` and `mise exec -- COMMAND`, or place Go 1.26.9 on PATH.
Docker Compose and Python 3 are needed for container and integration checks.

```bash
mise exec -- make verify
mise exec -- make test-race
mise exec -- make test-integration
make compose-build
```

The integration harness creates a uniquely named PostgreSQL container and a
Go 1.26.9 test container. They share only the disposable database network
namespace, publish no ports, and copy test source rather than bind-mounting a
host path. Cleanup removes only those containers. It does not read a supplied
DATABASE_URL or touch existing database volumes. The storage and CLI integration
packages run sequentially (`go test -p 1`) against this disposable server, so
CPU-heavy storage contracts do not compete with HTTP contracts that enforce
production request deadlines. Concurrent transaction checks inside each package
and the race detector remain enabled. A timed-out HTTP request still fails the
suite; the harness does not retry requests or increase application deadlines.
The candidate HTTP test fences its own temporary database against new
connections before forcing its removal, so a canceled transaction still draining
its connection cannot prevent cleanup.

The PostgreSQL contract tests cover concurrent retries, conflicting content,
transaction rollback, state readback after reopening the pool, and migration
checksum rejection. The race suite also exercises local/HTTP CLI workflows.
Routing regressions cover concurrent registration and input retries, crossing
and state baselines, combined evidence windows, coalescing, reopening, expiry,
owner checks, and action-authority rejection. Disposable PostgreSQL checks
upgrade an open version 1 position without rewriting its original events,
reject older writers, and verify entry/future-condition transaction rollback.
Timer tests use fake clocks for no-new-quote deadlines and cancellation. They
cover overdue startup, missing-event evidence, backward/forward clock changes,
expired-lease fencing, concurrent claim/completion, price-unavailable exceptions,
and later protective resolution. Disposable PostgreSQL tests also cover version
2 upgrade, retained receipts, restart recovery, and timer-audit rollback.
Action regressions exercise crashes before/after the independent adapter effect,
lease fencing, expiry and changed authority, accepted/partial reconciliation,
idempotent acknowledgment, and rollback that preserves the effect. Disposable
version 3 upgrade checks preserve original history without reenactment.
Operational regressions cover scoped readiness, dependency outage/recovery,
stale workers, missing protection, API/CLI inspection, recovery idempotency and
limits, live-lease preservation, and new protective decisions that leave expired
authority intact. Disposable checks preserve recovery history through restart
and rollback and upgrade version 4 exception metadata without rewriting events.
Library regressions cover contract and check versions, complete safety
declarations, exact dependencies and cycles, immutable registry readbacks,
required-check admission, and fixture configuration compatibility. Workflow
tests preserve entry, stop-gap, target, deadline, and abstention behavior.
Replay regressions cover cutoff leakage, availability/receipt ordering,
repeatability, cutoff partition equivalence, late arrivals, correction review,
same-timestamp quality gaps, duplicate revisions, content pins, and CLI file
round trips. See [dataset contracts](../reference/datasets.md) for the local
command and artifact boundaries.
Experiment regressions cover immutable parameters and benchmark bindings,
repeatable fixture comparisons, missing/invalid input failures, concurrent
artifact retention, conflicting retries, and window marks without invented
liquidation. The CLI requires an artifact directory and an attempt ID for runs.

## Native service

Start only the local database, then set the URL and token in the shell:

```bash
docker compose up -d postgres
export DATABASE_URL='postgres://mantik:mantik-local-development@127.0.0.1:45432/mantik?sslmode=disable'
export MANTIK_API_TOKEN=mantik-local-development
mise exec -- make build
./bin/mantik serve
```

Use a second terminal with the same token for `status` and the current-time
fixture ingestion example below. Change credentials and ports to match
`.env` if customized. `serve` applies embedded migrations through schema version
5 at startup. It rejects an unsupported database name or changed migration
checksum. Version 2 preserves retained events and derives protection for an
existing open fixture position; missing finalized evidence aborts the upgrade.
Older binaries cannot write this schema safely. See the
[trigger compatibility contract](../reference/triggers.md#finalized-protection-and-persistence)
before planning a deployment or rollback. Version 3 adds durable deadlines and
timer-sourced events; the worker runs immediately at startup and then every
second by default. Set `MANTIK_TIMER_INTERVAL` between `100ms` and `1m` to change
the poll interval. See [timer contracts](../reference/timers.md) for historical
demo inputs, exceptions, and clock recovery. Version 4 adds the action queue and
independent fixture effect ledger. Its worker uses the same poll interval and
reconciles effects before delivery. See [action contracts](../reference/actions.md).
Version 5 retains worker cycle observations and operational metadata. Inspect
`readiness --scope fixture_operation`, `operations`, and their JSON formats;
see [operational contracts](../reference/operations.md) for bounded recovery.
SIGINT/SIGTERM drains HTTP requests before the pool closes.

## API and fixture ingestion

- `GET /healthz`: process liveness, public.
- `GET /readyz`: storage scope by default; `?scope=NAME` selects a capability
  and returns 503 when that scope is unavailable.
- `GET /v1/readiness/{scope}`: inspect the scoped report without a healthcheck
  HTTP failure for a degraded result.
- `GET /v1/operations`: retained worker observations and exception history.
- `POST /v1/operations/exceptions/{id}/recover`: a bounded, idempotent recovery
  request evaluated at service time.
- `GET /v1/status`: consistent state and event history.
- `POST /v1/observations`: one fixture observation; 201 for new input, 200 for
  an identical retry, 409 for conflicting content, 422 for invalid fields.
- `POST /v1/triggers`: register an immutable operator definition.
- `POST /v1/routing-inputs`: match a fixture input and retain its receipt.
- `POST /v1/routing-cases/{id}/close`: close a research/decision routing case
  with its owner and explicit time.

See [trigger contracts](../reference/triggers.md) for CLI files and complete
condition, retry, validity, and authority rules.

All routes except health require the configured Bearer token when one is set.
Compose sets a token and publishes only loopback ports. Do not expose this
local development stack as a deployment.

To submit a quote through the CLI:

```bash
printf '%s\n' '{"id":"manual-1","kind":"quote","source":"fixture","symbol":"DEMO","occurred_at":"2026-01-05T15:01:00Z","received_at":"2026-01-05T15:01:00Z","price_cents":10000}' | ./bin/mantik observe
```

Times are simulation inputs. Freshness compares occurrence against receipt,
while the delivery worker also checks age and decision expiry at real execution
time. This endpoint is not a real-market ingestion path.
The fixed demo uses deterministic IDs and times; use a fresh in-memory demo
rather than expecting it to reset a persistent case.

For a service entry fixture, create and retain a current-time input outside Git:

```bash
mkdir -p data
python3 - <<'PY' > data/current-entry.json
import datetime, json
at = datetime.datetime.now(datetime.timezone.utc).isoformat()
json.dump({"id": "current-entry-1", "kind": "quote", "source": "fixture",
           "symbol": "DEMO", "occurred_at": at, "received_at": at,
           "price_cents": 10000}, __import__("sys").stdout)
PY
./bin/mantik observe < data/current-entry.json
./bin/mantik status --format json
```

The service receipt retains admission events; inspect status again after the
worker poll for the outcome. Retry the retained file unchanged. Regenerating
timestamps under the same ID creates a content conflict. The one-case fixture
does not provide a destructive reset after closure.

## Local data and cleanup

`docker compose down` retains the local named database volume. Database removal
is an explicit destructive development action and is not part of verification.
Do not point Mantik at an Augr database, copy existing credentials, or reuse
production data to populate the fixture.

## Deliberate fixture interruption

The isolated [pilot controls](pilot-controls.md#controlled-action-delivery-boundaries)
support opt-in delivery gates for crash qualification. Normal services leave
`MANTIK_FIXTURE_GATE_DIR` and `MANTIK_FIXTURE_ACTION_GATE` unset. A gate stalls
fixture actions outside transactions without changing authority or expiry; it
can make readiness unhealthy and is forbidden for the continuous pilot window.
