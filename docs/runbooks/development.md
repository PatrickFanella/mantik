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
DATABASE_URL or touch existing database volumes.

The PostgreSQL contract tests cover concurrent retries, conflicting content,
transaction rollback, state readback after reopening the pool, and migration
checksum rejection. The race suite also exercises local/HTTP CLI workflows.
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

Use a second terminal with the same token for `status` or
`demo --api-url http://127.0.0.1:40880`. Change credentials and ports to match
`.env` if customized. `serve` applies the embedded bootstrap migration at
startup. It rejects an unsupported database name or changed migration checksum.
SIGINT/SIGTERM drains HTTP requests before the pool closes.

## API and fixture ingestion

- `GET /healthz`: process liveness, public.
- `GET /readyz`: storage reachability.
- `GET /v1/status`: consistent state and event history.
- `POST /v1/observations`: one fixture observation; 201 for new input, 200 for
  an identical retry, 409 for conflicting content, 422 for invalid fields.

All routes except health require the configured Bearer token when one is set.
Compose sets a token and publishes only loopback ports. Do not expose this
local development stack as a deployment.

To submit a quote through the CLI:

```bash
printf '%s\n' '{"id":"manual-1","kind":"quote","source":"fixture","symbol":"DEMO","occurred_at":"2026-01-05T15:01:00Z","received_at":"2026-01-05T15:01:00Z","price_cents":10000}' | ./bin/mantik observe
```

Times are simulation inputs. Freshness compares occurrence against receipt,
not wall-clock age. This endpoint is not a real-market ingestion path.
The fixed demo uses deterministic IDs and times; use a fresh in-memory demo
rather than expecting it to reset a persistent case.

## Local data and cleanup

`docker compose down` retains the local named database volume. Database removal
is an explicit destructive development action and is not part of verification.
Do not point Mantik at an Augr database, copy existing credentials, or reuse
production data to populate the fixture.
