# Persistent triggers and routing cases

Trigger definitions describe fixture evidence that should open or update a
routing case. They carry an immutable ID, owner, case key, behavior, condition,
validity interval, repeat policy, and source requirements. Owner names identify
records; they are not authentication roles. The service bearer token controls
access to the API.

Operator definitions support `research` and `decide`. A match records work in a
case; it does not dispatch a worker, call a model, or authorize an order. The
`act` behavior, `operations` owner, parent-decision bindings, and `future-` ID
prefix are reserved for protective conditions derived from finalized fixture
entry decisions.

## Conditions and evidence

Every input must declare `source: fixture`, `symbol: DEMO`, and
`evidence_class: synthetic_unqualified`. Occurrence and receipt times are
explicit simulation inputs; occurrence cannot be after receipt. Definitions
declare a maximum evidence age in seconds. Matching uses those times, rather
than the machine's wall clock.

- `event` matches an exact event name.
- `threshold` supports `at_or_below`, `at_or_above`, `cross_below`, and
  `cross_above`, with integer price cents. A crossing needs a prior quote on
  the other side; the first quote establishes its baseline.
- `state_change` matches a transition of one state key to the declared value.
  The first state input establishes a baseline. Repeated values do not create
  another transition.
- `all` combines two to four distinct leaf conditions within an explicit
  evidence window. At least one leaf must supply a fresh match. A crossed
  threshold remains satisfied while price stays on that side, but subsequent
  quotes do not refresh the crossing time. Nested combinations are rejected.

Older occurrence times cannot rewind a condition's baseline. Validity includes
`valid_from` and excludes `valid_until`. An input received at or after expiry
marks the definition expired. Expiry is input-driven; there is no independent
timer or missing-feed monitor yet.

## Cases and retries

Definitions with the same owner and case key coalesce into one open case and
must use the same behavior. Matches retain their definition and input refs,
timestamps, and count. Repeat policies are:

- `once`: consume the definition after its first accepted match.
- `coalesce`: update an open case, without reopening a closed case.
- `reopen`: update an open case or create the next generation after closure.

Closing a research or decision case requires its recorded owner and an explicit
closure time. An operator cannot close an action case. Reopening produces a
new deterministic case ID and preserves the earlier generation.

Manual routing inputs retain receipts by immutable input ID. Identical retries
return the original receipt with `duplicate: true`; changed content returns a
conflict. Quote ingestion retains its existing observation deduplication fence.
The `routing:` and `quote:` input-reference namespaces prevent cross-endpoint
collisions. A manual routing quote only matches conditions; the observations
endpoint remains the fixture evaluation and paper execution path.

The aggregate admits 32 operator definitions, 4,096 manual inputs, 128 retained
cases for each of research and decision behavior, and four action cases.
Definitions and inputs over their limits are rejected. Case-capacity, closed
case, and out-of-order matches appear in `rejected_definitions`; they do not
interrupt protective quote ingestion. Status exposes this bounded routing
state, including definitions, runtime facts, cases, and manual receipts.

## Finalized protection and persistence

Each finalized fixture entry decision contains its exact future stop and target
definitions. They bind the decision ID, subject case, safety conditions, and
holding deadline. They commit with the observation, decision events, execution
outcome, and aggregate under the same PostgreSQL row lock. A failed transaction
retains none of them. A match alone grants no execution authority: operations
continues to enforce the finalized safety contract independently of models.
The existing fixture exit closes its applied action cases. The holding deadline
still requires an incoming usable observation.

Schema version 2 adds routing state and a constraint that prevents an older
writer from silently removing it. Startup verifies both migration checksums and
applies the upgrade under an advisory lock in one transaction. An existing open
version 1 fixture position derives protection from its retained finalized entry
event. Missing or incompatible evidence aborts the upgrade. Original events and
receipts are preserved. The bootstrap migration checksum remains unchanged.

An older binary rejects schema version 2 at startup. Downgrading metadata or
removing the constraint is not a rollback procedure. A future deployment needs
a database snapshot and a compatible rollback revision. Local integration
tests use disposable databases; this source change is not a deployment.

## API and CLI

All three endpoints use the service's existing bearer authentication and strict
JSON bodies of at most 16 KiB:

| Endpoint | Body | Success |
| --- | --- | --- |
| `POST /v1/triggers` | Operator definition | Definition, including identical registration retries |
| `POST /v1/routing-inputs` | Fixture input | Retained routing receipt |
| `POST /v1/routing-cases/{id}/close` | `owner` and RFC3339 `at` | Closed status |

Conflicting IDs return 409, invalid contracts 422, unknown cases 404, and
malformed or unknown JSON fields 400. Status remains `GET /v1/status`.

Save this definition as `data/review-trigger.json` outside Git:

```json
{
  "id": "review-ready",
  "owner": "operator",
  "case_key": "fixture-review",
  "behavior": "research",
  "condition": {"kind": "event", "event_name": "research_complete"},
  "valid_from": "2026-01-05T14:00:00Z",
  "valid_until": "2026-01-05T16:00:00Z",
  "repeat_policy": "coalesce",
  "requirements": {
    "source": "fixture",
    "symbol": "DEMO",
    "evidence_class": "synthetic_unqualified",
    "max_age_seconds": 60
  }
}
```

Save a matching input as `data/review-input.json`:

```json
{
  "id": "review-input-1",
  "kind": "event",
  "source": "fixture",
  "symbol": "DEMO",
  "evidence_class": "synthetic_unqualified",
  "occurred_at": "2026-01-05T15:00:00Z",
  "received_at": "2026-01-05T15:00:00Z",
  "event_name": "research_complete"
}
```

With the local service running and `MANTIK_API_TOKEN` exported:

```bash
./bin/mantik trigger-register --file data/review-trigger.json
./bin/mantik route-input --file data/review-input.json
./bin/mantik status --format json
./bin/mantik close-routing-case --id CASE_ID --owner operator --at 2026-01-05T15:01:00Z
```

Use the case ID returned in the match receipt. Commands accept `--api-url` for
another local HTTP origin. These examples create synthetic research records
and carry no real-market qualification credit.
