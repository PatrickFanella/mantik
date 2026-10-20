# Retained candidate worker intents

`internal/candidateattempt` and `storage.CandidateWorkerIntentStore` retain
pre-execution declarations for a registered candidate's synthetic behavior job.
The contract has memory/PostgreSQL persistence, authenticated HTTP routes and CLI
commands. Retaining or inspecting an intent does not launch a worker or retain
execution outcomes.

The command includes receiver and request IDs, the exact registration ID and
SHA-256, and the full `mantik-candidate-behavior-job-v1` job. The job must validate
and carry exactly the candidate package retained by that registration, including
source, hypothesis, raw/canonical dataset and immutable toolchain-image pins. All
case IDs and prices are derived again from the registration's exact retained
dataset bytes. Rehashing a substituted price, harness, plan and job does not make
the substituted input valid. Expected Boolean annotations remain explicit caller
declarations; changing them produces another logical job.

`mantik-candidate-worker-intent-v1` retains the command, receiver-wide sequence,
UTC creation time and SHA-256 of compact Go JSON with its own `sha256` field empty.
Status is `intent_retained` and authority is `candidate_worker_intent_only`.
`execution_started`, `host_state_known` and `qualification_credit` are false.
These flags describe the intent's authority, not a later process state. The
embedded worker job and candidate retain their original unqualified provenance.

There are at most 64 intents per receiver, including those that never launch or
later fail. Sequences are contiguous from one, request IDs are unique, and clocks
cannot move backwards across the history or precede the bound registration.
An exact request retry returns the original sequence, timestamp and receipt,
including at capacity or after clock rollback. Changing registration, source,
dataset, annotations, worker profile or other job pins under that request ID
conflicts. A distinct request ID records a distinct intended physical attempt
even if its logical job is identical. Recording that request starts no process.

Both backends validate the complete candidate registry, its publications and exact
datasets, then every retained intent's structure, sequence, registration and job
binding before admitting another request or returning a selected intent. Reads
cannot hide corrupt unrelated declarations or intent rows. Repeated logical jobs
reuse their already-verified dataset-to-case check within the same snapshot;
different jobs are checked separately. A consistent supplied prefix alone cannot
prove that an owner retained all requests made outside this receiver or omitted
no complete suffix.

The memory backend serializes under its receiver mutex and detaches returned
records. PostgreSQL schema 31 adds `mantik_candidate_worker_intents`, referencing
the immutable candidate registration rather than copying its dataset again.
The receiver row lock serializes new sequences, capacity and exact retries. One
transaction retains each intent; an insertion failure leaves no acknowledged row
or sequence gap. Readback uses a repeatable-read snapshot across the registry,
intent metadata and exact datasets. No update, cancellation or deletion operation
is exposed.

Migration from schema 30 creates an empty intent table and preserves candidate
registrations, dataset bytes and operational state. Checksum and version fences
apply; a schema-30 binary refuses a schema-31 database. Existing pilots have not
been upgraded by implementing or testing this contract. The shared dataset
contract now also serves candidate fixture planning directly, removing its replay
execution/storage dependency without changing plan or worker schemas or hashes.

Verification covers concurrent identical requests, exact and changed retries,
distinct requests for the same logical job, nested-value isolation, capacity,
clock/registration/profile/case fences, strict command JSON, restart retention,
failed insertion, corruption refusal and migration preservation/checksum refusal.
The disposable database tests also reject fully rehashed case substitutions during
intent readback.

This is part of #18. An intent is neither an isolation receipt nor evidence of
compilation, successful behavior, activation or profitability.

## Authenticated access and coherent history

The existing service bearer-token boundary protects these routes:

| Route | Result |
| --- | --- |
| `POST /v1/candidate-worker-intents` | Retain the strict intent command and full sealed behavior job; exact retries return the original receipt. |
| `GET /v1/candidate-worker-intents` | Complete `mantik-candidate-worker-intent-history-v1` history from one backend snapshot. |
| `GET /v1/candidate-worker-intents/{id}` | One exact intent after whole-prefix validation. |

There are no query options, launch or cancellation routes. Invalid commands return
422, registration/job/clock/capacity conflicts return 409, and missing registrations
or intent IDs return 404. Requests reject ambiguous JSON, unknown fields, case
aliases and trailing documents. The server derives all case inputs again from its
retained registration dataset; the caller does not upload another dataset copy.

The history contains every full intent and compact pins for exactly the
registrations those intents reference. Pins retain registration ID, ordinal,
SHA-256, candidate SHA-256 and registration time in original registration order.
Unreferenced registrations are available through the separate candidate registry;
they are not repeated in an empty intent history. Every intent must match its
referenced pin and cannot predate it. Missing, duplicate, unused, reordered or
inconsistent pins invalidate the response.

Receiver, registration pins and intents are captured under one memory lock or
PostgreSQL repeatable-read transaction. The backend first validates the complete
registry, publications, dataset bytes and intent/job bindings. The history's
SHA-256 covers compact Go JSON with its own `sha256` empty. Authority is
`retained_candidate_worker_intents_only`; `execution_verified`, `host_state_known`
and `qualification_credit` are false. These pins and this hash describe retained
server declarations. A compact pin does not independently reconstruct its full
registration hash or attest ownership; use the registration inspection command
when that full artifact is needed.

```bash
mantik candidate-worker-intent-start --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id attempt-1 \
  --registration root --registration-sha256 REGISTRATION_SHA \
  --job job.json --job-sha256 JOB_SHA

mantik candidate-worker-intents --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER

mantik candidate-worker-intent --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id attempt-1 --intent-sha256 INTENT_SHA

mantik candidate-worker-intent --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id attempt-1 --intent-sha256 INTENT_SHA \
  --artifact job > intent-job.json
```

Use inspected receiver and content identities. `MANTIK_API_TOKEN` supplies the
existing service token. All commands require an expected receiver; selected reads
also require the expected intent SHA. Starting an intent validates the local job
and expected job SHA before contacting the service, then checks the returned
command against the exact submitted command. Readback uses strict bounded JSON and
checks identities, authority, history membership and hashes before emitting data.
Job export returns the same validated sealed job; it is not an execution command
or a host receipt. Histories are bounded to 64 intents and 32 registration pins.

The disposable CLI/HTTP contract registers intents across two registrations for
the same logical job, checks retries and job export, reopens PostgreSQL behind a
fresh handler, and compares the complete pinned intent/history responses. Tests
also cover authentication, unsupported query/launch/cancellation paths, forged
readbacks and refusal before output.

Worker ownership/reconciliation linkage, retained outcomes, generated-source
provenance and the final #18 audit remain unfinished. The original pilot services
have not been upgraded by adding or testing these endpoints.
