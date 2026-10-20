# Retained candidate worker intents

`internal/candidateattempt` and `storage.CandidateWorkerIntentStore` retain
pre-execution declarations for a registered candidate's synthetic behavior job.
This slice provides pure contracts and memory/PostgreSQL persistence. It does not
add HTTP/CLI intent commands, launch a worker or retain outcomes yet.

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

This is part of #18. Intent API/CLI access, worker ownership/reconciliation linkage,
retained outcomes, generated-source provenance and the final acceptance audit
remain. An intent is neither an isolation receipt nor evidence of compilation,
successful behavior, activation or profitability.
