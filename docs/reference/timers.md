# Independent deadlines and missing events

The persistent service runs a timer worker independently of HTTP requests,
incoming quotes, research workers, and models. Startup checks overdue work
immediately; subsequent polls default to one second. The worker uses the
machine's UTC clock. Unit tests inject a fake clock; the local replay commands
remain driven only by their declared dataset cutoffs.

## Definitions

The existing trigger registration endpoint and `trigger-register --file` command
accept three additional condition kinds:

| Condition | Required fields | Meaning |
| --- | --- | --- |
| `deadline` | `due_at` | Route when the explicit time is reached |
| `elapsed` | `after_seconds` | Route after the interval from `valid_from` |
| `expected_event_missing` | `event_name`, `due_at` | Route if usable evidence of the named event was not received before the due time |

Timer definitions require `repeat_policy: once`. The due time must be within
the definition's validity interval. Combined timer conditions and recurring
schedules are not supported. Existing owner, case key, behavior, and fixture
provenance requirements still apply. Operators can create research or decision
work; action definitions remain reserved for finalized protection.

For example, use the registration JSON from [trigger contracts](triggers.md),
changing the ID, validity times, repeat policy, and condition:

```json
{
  "kind": "expected_event_missing",
  "event_name": "research_complete",
  "due_at": "2026-01-05T15:30:00Z"
}
```

This is the condition object inside a complete definition. Receipt must be
strictly before `due_at` to satisfy an expectation. An event received at the
deadline or later remains retained evidence but cannot erase the missing-event
result. Events with wrong provenance, wrong names, occurrence before
`valid_from`, or excessive evidence age do not satisfy the expectation. A
received expectation suppresses the missing case; its job completes with
`event_observed`. New obligations need new immutable definition IDs.

Timers use the same owner/case-key coalescing and capacity rules as incoming
evidence. A valid obligation missed during downtime still routes on restart,
even after `valid_until`. That interval controls which obligation can be
registered, rather than granting downtime permission to drop overdue work.
Ordinary incoming-evidence definitions expire on timer polling as well as on
incoming inputs.

## Leases and clock changes

Registration and finalized entry ingestion create pending jobs atomically with
their definitions. Claim transactions lock the existing aggregate row, select
up to 16 due jobs in stable ID order, and persist the worker identity, generation,
and lease expiry. The service uses a fresh random worker ID and 30-second leases.
Claim and completion calls each have a five-second context timeout.

An abandoned job can be reclaimed at lease expiry. Reclaim increments its
generation. The old worker cannot complete that generation, and completion at
or beyond lease expiry is rejected. Completion commits routing, any protective
decision or exception, audit events, and the retained receipt together. Identical
completion retries return that receipt without another effect. A failed
transaction leaves the claim available for retry or expiry recovery.

The scheduler retains its latest accepted clock in PostgreSQL. A backward clock
change pauses claims and incomplete deliveries until UTC catches up with this
watermark. A forward change recovers overdue jobs; it can expire existing
leases, which then need a new claim. The worker logs failed cycles and retries
on subsequent polls. This policy does not claim to detect a wrong host clock;
operators remain responsible for clock synchronization.

`MANTIK_TIMER_INTERVAL` accepts durations from `100ms` through `1m`, with `1s`
as the default. SIGINT/SIGTERM cancels polling and joins the worker before
closing the database pool. A killed process leaves its claims for lease expiry.

## Protective deadlines and evidence

A finalized fixture entry retains three future definitions: stop, target, and
the holding deadline. The deadline binds the entry decision and subject case.
Operations applies only that current finalized obligation, without consulting
a model. A position already closed by a quote makes the timer `superseded`.

At a deadline, the worker can finalize and apply a synthetic exit using the
last admitted quote only when that quote remains within the safety contract's
maximum age at the execution time. The observation keeps its original occurrence
and receipt times; the decision and outcome record the timer's execution time.
The fill remains `observed_quote_zero_cost_v1`, with no qualification credit.

Missing or stale price evidence produces `deadline_price_unavailable` in
`state.routing.timers.exceptions`. The exception stays open, the position stays
open, and no price, observation, or fill is invented. A later usable observation
can execute the existing protective exit and mark the retained exception
resolved. Quotes received before an already claimed protective clock are
retained as unusable and cannot undo that protection.

`GET /v1/status` and `status --format json` expose jobs, lease generations,
completion receipts, and exceptions. These are structured operational records;
capability-scoped readiness and operator exception workflows remain issue #10.
External delivery and uncertain-outcome reconciliation remain issue #9.

The service uses real UTC while the fixed demo uses January 2026 simulation
inputs. An open fixed-demo position is therefore overdue to the service worker.
It may produce a price-unavailable exception before further historical demo
inputs arrive. Use local `demo`, `replay`, or `experiment` for deterministic
fixture economics; use quotes with explicit current times when exercising the
persistent worker. Restarting or repeating a command does not reset its state.

## Schema compatibility

Schema version 3 adds durable timer state and permits timer-sourced audit events
without manufacturing an observation. An event must reference either a retained
observation or a timer ID. State constraints prevent an older binary from
silently dropping routing or timer state, and older startups reject version 3.
Version 1 and 2 migration bytes and checksums are unchanged.

The transactional upgrade adds a holding-deadline definition to an existing
open position using its original finalized entry event. Original decisions,
events, and receipts retain their bytes and original condition history. The last
usable quote is recovered against its occurrence watermark; missing evidence
leads to an exception when the timer runs. Missing or incompatible finalized
entry authority aborts the upgrade.

Do not remove schema guards or downgrade metadata as a rollback procedure. A
deployment needs a snapshot and a revision compatible with timer state. These
changes have disposable upgrade, restart, concurrency, and rollback tests;
they have not deployed or qualified a real feed or trading strategy.
