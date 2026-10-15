# Bounded native trial execution

The service can execute a retained trial intent against the built-in synthetic
dataset using the registered native experiment engine. It retains the input
before execution and the complete native result afterward. This path has no
provider, account or order adapter, arbitrary code runner or caller-supplied
input/result. It does not qualify a strategy or evaluate hypothesis conditions.

```sh
./bin/mantik trial-run-execute --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id ATTEMPT_ID
./bin/mantik trial-run --api-url http://127.0.0.1:40880 --id ATTEMPT_ID
./bin/mantik trial-run --api-url http://127.0.0.1:40880 \
  --id ATTEMPT_ID --artifact result
./bin/mantik trial-run --api-url http://127.0.0.1:40880 \
  --id ATTEMPT_ID --artifact input
```

First [register a trial](hypothesis-trials.md), then [retain an attempt intent](trial-attempts.md).
The adapter always selects `builtin-synthetic-dataset-v1`, the four-record dataset
exported by `replay --export-fixture`. Recipes use the existing native strategy
and control contracts. A declared dataset pin that differs from this input
produces a retained `dataset_pin_mismatch` failure; it does not fetch the declared
dataset elsewhere. The recipe, source access/mapping and admission rules remain
mandatory. No result is selected as a best outcome or granted qualification.

## Ownership and durable evidence

Execution atomically acquires one run for the intent, captures the registered
plan and original intent hash, and retains the exact input JSON. Only after that
transaction commits does the handler invoke the native experiment runner in
independent memory stores. No database transaction remains open during replay,
and the application's operational state, action queue and effect ledger are not
used for those synthetic runs.

One run exists per intent. A server-generated invocation ID fences completion;
concurrent execution requests conflict while it is running. Repeating execution
after a terminal outcome returns the retained run without recomputing it. A new
attempt ID is required for another execution, preserving the earlier attempt.
The native engine's attempt ID is `trial-` plus the SHA-256 of the intent ID so
that every supported intent ID maps into the engine's bounded ID contract.

Run metadata uses `mantik-native-trial-run-v1` and includes intent, registration,
recipe and input digests; invocation/engine identity; start/deadline/finish times;
status; result digest/size; deadline disposition; and a receipt hash. The adapter
version is `native-experiment-v1`. Its behavior must be versioned when changed.
Hashes identify configuration and evidence; they are not binary or image hashes.

Input and result artifacts are retained as exact text separately from metadata.
Metadata reads do not select artifact bodies. Targeted artifact reads return
attempt ID, kind, SHA-256 and content. The input hash is Go JSON of the complete
sealed dataset, including its own `sha256` field; it differs from the dataset's
internal seal. Results bind this input hash, the recipe, native attempt identity
and exact method pins. The service creates results internally; HTTP cannot submit
metrics, replay frames, authority or qualification flags. Artifact bodies are
bounded to 8 MiB. Persistence failure does not acknowledge a terminal result.

`completed` and `failed` retain the complete native result, including partial
results when supplied by the engine. `qualification_credit` remains false and
authority `synthetic_research_execution_only`. Inspect the result artifact for
metrics and replay frames. Fixture PnL is demonstration evidence, not profit or
prospective paper qualification. CLI execution prints the retained receipt and
returns a nonzero exit for failed or incomplete runs.

## Interruption and cancellation

The run has a fixed 30-second deadline and the handler uses a cooperative context
with the same maximum duration. No detached execution goroutine is created to
pretend an uncooperative call stopped. If a process or request ends before result
commit, the durable run remains `running`, with its original input, owner and
deadline. This is evidence of an unresolved run, not proof of a live worker.
Automatic restart, takeover, re-execution and result replacement are not allowed.

After the original deadline, explicitly record the absence of a durable result:

```sh
./bin/mantik trial-run-resolve --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id ATTEMPT_ID
```

Resolution sets `incomplete` with `no_retained_result_at_deadline`. It does not
assert that computation never finished or that an active process was killed.
Any later completion from the old invocation is fenced out. Exact resolution
retries preserve the terminal receipt. A finish that commits first remains the
retained outcome; resolution cannot overwrite it. A result committed at or after
the deadline is retained with `deadline_met: false`, not silently discarded or
presented as timely. Global run-clock checks reject backward start/finish clocks.

Intent cancellation is allowed only before a run exists. Cancellation and run
acquisition share the receiver lock, so one wins atomically. Once a run exists,
intent cancellation returns a conflict. The v1 intent receipt remains the
historical pre-execution artifact, including its original `execution_started:
false` field. Read current execution state through `trial-run`; intent history
and its pending label must not be used as a current worker/execution projection.
No existing intent or registration receipt is rewritten by execution.

## HTTP, storage and scope

Authenticated routes accept no query parameters:

| Route | Purpose |
| --- | --- |
| `POST /v1/trial-attempts/{id}/run` | Execute or recover a terminal receipt; body `{receiver_id}` |
| `POST /v1/trial-attempts/{id}/run/resolve` | Resolve a missing result after deadline; body `{receiver_id}` |
| `GET /v1/trial-attempts/{id}/run` | Current run metadata |
| `GET /v1/trial-attempts/{id}/run/input` | Retained input artifact |
| `GET /v1/trial-attempts/{id}/run/result` | Retained result artifact; 404 if none exists |

Write bodies are bounded to 4 KiB with exact names, no duplicates/unknown fields,
valid UTF-8 and one JSON value. Invalid input returns 422, missing intent/run or
artifact 404, ownership/state/clock/receiver conflicts 409, and storage/runner
errors 503. A failed native experiment is a retained outcome returned with HTTP
200, independently of the CLI's nonzero exit disposition.

Migration 16 adds run metadata and exact-text artifact tables. Acquisition,
completion and resolution serialize through the receiver row. Input/run creation
and result/terminal updates each commit atomically. There can be at most one run
per retained intent, bounded by the existing 32-intent capacity. Schema 15 intent
and schema 14 registration records retain their original bytes and hashes.
Back up before upgrading; older binaries reject schema 16 at startup. Rollback
requires a compatible database backup and binary/image. Existing pilot services
are not upgraded automatically.

This native in-process adapter does not deliver the external research-worker
protocol in issue #17. External tools, arbitrary datasets, resource isolation,
provider-backed research, statistical hypothesis evaluation and promotion require
separate reviewed contracts and evidence.
