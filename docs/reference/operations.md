# Operational inspection and recovery

Mantik exposes structured operational exceptions and readiness for individual
capabilities. Process liveness, database access, worker progress, and available
feed coverage are separate observations. The fixture remains synthetic and
unqualified even when its operational capabilities are ready.

## Readiness scopes

`GET /healthz` remains public process liveness. Authenticated `GET /readyz`
defaults to the `storage` scope and preserves the `status: ready` response when
storage is accessible. Select another scope with `?scope=NAME`. A ready scope
returns HTTP 200; an unavailable scope returns 503. The body includes the
selected scope, `ready`, `status`, mode, check time, and every capability result.

| Scope | What its ready result establishes |
| --- | --- |
| `process` | The HTTP process can answer; storage may still be unavailable |
| `storage` | A consistent state snapshot can be read |
| `feed_coverage` | Validated current evidence for every registration in an [explicit startup policy](feed-readiness.md); disabled by default |
| `action_delivery` | Fresh successful worker progress without unresolved delivery faults |
| `protective_monitoring` | Fresh successful timer progress and applicable protection without unresolved protective faults |
| `fixture_operation` | Storage, action delivery, and protective monitoring are ready within the fixture contract |

`fixture_operation` deliberately does not claim real feed coverage. Each result
has a status, reason code, and evidence references. A storage outage makes the
dependent capability state unknown while process liveness can remain ready.
`GET /v1/readiness/{scope}` returns the same report with HTTP 200 for inspection,
including when `ready` is false. Unknown scopes return 400.

Both workers persist their latest cycle observation per capability: worker ID,
timestamp, last successful cycle, polling interval, error code, evidence ref,
and stopped state. A heartbeat becomes stale after ten seconds plus three poll
intervals (13 seconds at the default interval). Stale means no fresh completed
cycle was observed; it does not authorize stealing a live lease. A backward
clock or stopped worker is reported separately.

When storage cannot retain a worker failure, the running process keeps the
first unrecorded failure locally and tries to persist it with the next report.
A successful later report records the outage and recovery. Process death can
lose that unpersisted observation; logs and stale persisted heartbeats remain
separate evidence. The latest capability report is not an inventory of every
worker instance. Economic effects still use the existing generation fencing.

## Exception records

`GET /v1/operations` returns current readiness, persisted worker records, and
the exception history. The existing `state.routing.timers.exceptions` map is
the canonical exception store; original timer records retain their identities.
Records now include:

- Affected case and capability, severity, and reason code.
- Original detection time, latest observation time, and structured evidence refs.
- Related action or timer identities where applicable.
- Open/resolved state, attempted recovery, and terminal outcome.

Repeated observations coalesce under a stable capability/cause/subject identity.
Open and resolved transitions retain audit events; source decisions, receipts,
and earlier events are not rewritten. Worker failures, uncertain/accepted/partial
actions, terminal action failures, overdue timers, missing protective definitions,
unfulfilled queued protection, and unavailable deadline prices remain visible.
Readiness also derives current faults immediately from a copied snapshot; the
next worker report persists newly detected conditions. Inspection does not
modify state.

Resolved worker faults remain in history. A stopped or stale worker can still
make its capability unavailable without a new persisted cycle. A completed
protective exit resolves its price-unavailable exception. Queuing an exit is
not evidence that it executed.

## Bounded recovery

Authenticated `POST /v1/operations/exceptions/{id}/recover` accepts a strict JSON
body of at most 16 KiB:

```json
{"request_id": "operator-attempt-1", "operation": "reconcile-action"}
```

The service supplies the UTC time. A client cannot backdate recovery or replace
an expired decision's validity. The supported operations are:

| Operation | Permitted behavior |
| --- | --- |
| `recheck` | Reevaluate the retained condition and record whether it is still open or already resolved |
| `reconcile-action` | Read the existing adapter effect, or acknowledge a terminal action result; no submission or forced fill |
| `retry-protection` | Reevaluate an existing overdue holding obligation using current retained evidence, then queue a new finalized exit if applicable |

Reconciliation cannot steal an unexpired lease. It retains known adapter
outcomes before considering expiry, and releases its operator lease after
reconciliation so normal workers can continue. Proven absence may return an
uncertain action to pending; the normal worker still rechecks its original
authority before submission. Terminal acknowledgment does not reset an expired
action or make an unfulfilled protective obligation safe.

Protective recovery requires the registered immutable strategy, matching original
entry/subject/deadline bindings, successful mandatory evaluations, and a usable
retained quote. It blocks when price or authority is missing, another action is
unresolved, or the obligation is not due. It cannot invent a price, change
safety limits, replace original evidence, or force an economic result. An
expired action stays intact; a valid recovery creates a new decision/action
identity from the existing safety obligation. Its result is `queued` until the
action worker applies the effect.

Each exception retains at most three distinct operator attempts, including
blocked attempts. Reusing the same request ID and operation returns the original
receipt with `duplicate: true`, even after the limit is reached. Changing the
operation under a retained ID or exceeding the limit returns 409. Unknown
exceptions return 404; unsupported operations or clocks return 422. Recovery
commits its state changes and audit together, without requiring a model.

## CLI

With the service token exported:

```bash
./bin/mantik readiness --scope fixture_operation
./bin/mantik readiness --scope feed_coverage --format json
./bin/mantik operations
./bin/mantik operations --format json
./bin/mantik recover --exception EXCEPTION_ID --request-id operator-attempt-1 --operation reconcile-action
```

Inspection commands accept `--format table|json`. Recovery prints its structured
receipt. All commands accept `--api-url` for another local HTTP origin. A blocked
or queued receipt requires inspecting the retained state; it does not imply an
executed recovery. Use the same request ID when retrying an uncertain HTTP
response, and a new ID only for a distinct attempt.

## Schema and pilot evidence

Schema version 5 adds operational worker state and guards against an older
writer discarding it or exception metadata. Startup enriches existing exception
records with capability, severity, and evidence while retaining original audit
payloads and action authority. Previous migration bytes/checksums are unchanged.
Backup/restore must preserve the aggregate, action effect ledger, and history
together; removing guards or downgrading metadata is not recovery.

Fake-clock, API/CLI, and disposable PostgreSQL tests verify readiness, outages,
missing protection, bounded requests, safe reevaluation, concurrent retries,
restart retention, recovery rollback, and legacy upgrade. The first 24-hour
pilot still needs its own [runtime acceptance evidence](../runbooks/first-pilot.md).
A passing test suite or readiness response alone does not complete that pilot.
