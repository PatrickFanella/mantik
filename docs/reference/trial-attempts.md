# Retained trial attempt intents

An attempt intent records planned work against an immutable registered trial.
It is retained before an executor can run that work. This v1 boundary
supports starting, inspecting and cancelling an intent. The separate
[native run](native-trial-runs.md) operation now executes the built-in fixture and
retains results without rewriting the historical intent.

```sh
./bin/mantik trial-attempt-start --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id ATTEMPT_ID --trial TRIAL_ID
./bin/mantik trial-attempt --api-url http://127.0.0.1:40880 --id ATTEMPT_ID
./bin/mantik trial-attempts --api-url http://127.0.0.1:40880 --id TRIAL_ID
./bin/mantik trial-attempt-cancel --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id ATTEMPT_ID
```

Existing bearer authentication applies. Use explicit stable attempt IDs and the
pinned receiver identity. Attempt/trial IDs use 1..128 ASCII letters, digits,
underscores or hyphens. Start loads the authoritative [trial registration](hypothesis-trials.md)
and retains its receipt hash, hypothesis identity, recipe digest and declared
dataset identity/version/digest. Start time must be valid and no earlier than
registration or any previous attempt start/cancellation in this database.
Declared dataset identities do not prove input availability or admission.

V1 states are `pending` and `cancelled`. Every receipt explicitly retains
`execution_started: false`, `qualification_credit: false` and authority
`research_attempt_intent_only`. Pending means that an execution intent exists;
it is not evidence of an active worker, a completed experiment or a timeout.
There is no automatic deadline, lease, retry loop, failure classifier or pending
expiry for the intent itself. Current execution state belongs to a separate run. A process interruption leaves the retained intent visible
for inspection; it can be cancelled explicitly before a run exists. A run blocks intent
cancellation; inspect its outcome or unresolved execution state separately.

Cancellation records `operator_cancelled_before_execution` and the server close
time. It cannot delete the intent or change its original plan/source identities.
It does not assert that an external process was killed; intent cancellation itself
does not dispatch execution. A new attempt ID retains a separate intent for the
same plan.
Cancelled intents remain in history alongside pending ones. A trial's own parent
links continue to describe planned research lineage independently.

Start retries recover the current retained state without consulting a new clock.
After cancellation, retrying start therefore returns the cancelled record rather
than resurrecting work. The trial binding, original start time and intent hash
remain fixed. Reusing an attempt ID with another trial conflicts. Cancellation
retries recover the same terminal receipt without changing close time. A new
cancellation time cannot precede any retained attempt start or close time.

`intent_sha256` hashes the typed attempt with status normalized to pending,
close time zero, reason empty, and both hash fields empty. It binds the original
intent across disposition changes. `sha256` hashes the complete current typed
receipt with only its own hash field empty; it changes on cancellation. Both use
Go JSON. Readbacks validate these bindings without claiming source truth,
execution, profitability or qualification.

Up to 32 attempt intents are retained per database, across all trials. No history
is evicted. Exact start retries and explicit cancellation remain available at
capacity. Per-trial history returns every retained intent, sorted by start time
and ID. A registered trial with no intents returns an empty history; an unknown
trial returns 404. Case closure does not mutate intents or adjudicate research
validity.

Authenticated HTTP routes:

| Route | Input or readback |
| --- | --- |
| `POST /v1/trial-attempts` | `{receiver_id, request_id, trial_id}` |
| `POST /v1/trial-attempts/{id}/cancel` | `{receiver_id}` |
| `GET /v1/trial-attempts/{id}` | One current `mantik-trial-attempt-intent-v1` receipt |
| `GET /v1/hypothesis-trials/{id}/attempts` | `mantik-trial-attempt-history-v1` with all intents for that trial |

Write bodies are bounded to 4 KiB. Unknown, duplicate or incorrectly cased
fields, invalid UTF-8, excessive nesting and trailing JSON are rejected. No route
accepts query parameters. Callers cannot supply execution flags, timestamps,
results or qualification. Invalid input returns 422, missing trial/attempt 404,
receiver/identity/clock/capacity conflict 409 and storage failure 503.

Migration 15 adds `mantik_trial_attempts` with a trial foreign key. Start,
capacity/clock checks and cancellation serialize through the receiver row, in
one transaction. Failed persistence does not acknowledge a new or cancelled
intent. Memory storage uses the same contract under its mutex. Restart and
backup/restore preserve pending and cancelled receipts. Registration, review,
publication, news case and execution state are unchanged by intent operations.
Back up before upgrading an existing service. Older binaries reject schema 15;
rollback needs a compatible database backup and its matching binary/image.
Existing pilot services are not upgraded automatically.
