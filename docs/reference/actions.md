# Durable fixture actions

The persistent service separates finalized intent, enactment, and acknowledgment.
Ingestion and protective timers commit versioned action intents with their
decisions. A separate worker delivers those intents without requiring a model
or another HTTP request. Local memory demo/replay keeps synchronous deterministic
execution; `NewQueuedMemory` exercises the durable service contract in tests.

## Authority and identity

Each `paper-action-v1` intent retains its finalized decision, complete safety
contract, original quote, delivery mode, and optional timer source. Its stable
ID and idempotency key derive from the decision ID. Changing content under that
identity conflicts; an input retry never creates a second action. Status JSON
exposes the queue at `state.actions.items`.

The worker rechecks decision validity, immutable strategy and evaluation
versions, quantity/notional and protective levels, price freshness at delivery
time, the current quote watermark, and current position applicability. A newer
quote can invalidate pending authority. The worker cannot silently replace the
quote or widen the decision's validity. A new applicable decision requires new
evidence. One unresolved fixture action blocks another economic intent in that
aggregate. The retained history is bounded to 256 actions.

## Delivery and reconciliation

The worker first claims a lease under the existing aggregate lock. Claims use a
fresh worker identity and monotonically increasing generations. Default polling
is one second, leases last 30 seconds, and each storage operation has a
five-second context timeout. The existing `MANTIK_TIMER_INTERVAL` configures
both worker poll intervals. Cancellation joins both workers before pool closure.

Every claim begins with reconciliation by idempotency key. If an effect exists,
the worker retains its known status or applies its completed outcome. A completed
effect is acknowledged even after the decision expires: expiry cannot erase
an effect that already occurred under valid authority.

If no effect exists, current authority is evaluated before submission. The
local fixture ledger proves absence under the same aggregate lock that fences
submission. The worker then commits `unknown` status and a submission audit
record before invoking the adapter. The adapter commits its result separately
from the economic aggregate. A final acknowledgment transaction updates the
position, action result, and execution event together. Input receipts preserve
their original ingestion events and are not rewritten with later results.

| Status | Meaning |
| --- | --- |
| `pending` | Finalized intent awaiting delivery, or an uncertain attempt reconciled as absent |
| `unknown` | Submission intent was persisted; its economic result is not yet acknowledged |
| `accepted` | The adapter reports acceptance without a completed effect |
| `partial` | The adapter reports an incomplete result requiring further reconciliation |
| `completed` | A matching, validated adapter outcome is retained and applied |
| `rejected` | Authority or adapter evidence does not permit the effect |
| `expired` | The decision expired before an effect occurred |

Accepted and partial results block another submission and do not invent a
complete position. They are supported by the reconciliation contract and
adapter fixtures; the current zero-cost fixture adapter completes immediately.
Real provider adapters must establish their own idempotency and definitive
absence/reconciliation guarantees. A timeout or missing provider response alone
would not justify replaying an unknown action.

Crashes before enactment recover through proven absence. Crashes after the
adapter commit recover the original effect. Reclaimed leases fence the old
generation before any submission or acknowledgment. A failed acknowledgment
rolls back application state and audit while leaving the independent effect
available for reconciliation. Backward worker-clock changes pause incomplete
work until its persisted clock watermark catches up.

## Fixture and timer boundaries

Only synthetic `DEMO` quotes and the registered fixture strategies are supported.
The PostgreSQL fixture effect ledger is Mantik-owned simulation evidence; it
is not an external venue or a qualified economic model. Integer cents, zero
costs, and observed-quote fills retain their existing limitations.

A protective timer with usable retained price evidence now finalizes and queues
an exit. Its receipt reports `queued`; completion is visible through the action
and execution records. Missing/stale price evidence still leaves a retained
exception without inventing a quote or fill. An unresolved action keeps a
protective timer recoverable until that action reconciles. Applied exits close
routed action cases and resolve retained price-unavailable exceptions.

Service observations return admission receipts, not a guarantee of immediate
execution. Inspect `status --format json` for action progress and outcomes.
Use current-time fixture quotes for service delivery; January 2026 demo inputs
expire against the real worker clock. Local `demo`, `replay`, and `experiment`
remain suitable for deterministic fixture results.

## Schema compatibility

Schema version 4 adds action state and a separate fixture effect table. Audit
events may reference an observation, timer, or action. Existing migration bytes
and checksums are unchanged. Startup rejects incompatible versions/checksums;
the new state constraint prevents older writers from discarding the queue.

Previously executed positions and their original events/receipts remain intact.
The upgrade initializes an empty queue rather than resubmitting historical
decisions. Existing protection continues through the timer contract. Database
snapshots and rollback revisions must include the queue and independent effect
ledger together; deleting either is not a recovery procedure.

Crash, expiry, authority-change, concurrent delivery, status reconciliation,
acknowledgment rollback, and version 3 upgrade checks use disposable databases.
Operational exception recovery and readiness use the
[operational contract](operations.md). The post-M2 pilot
still needs its declared runtime evidence before its checkpoint is complete.
