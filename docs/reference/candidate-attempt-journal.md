# Local candidate attempt journal

`candidate-attempt-run` wraps the [behavioral worker](candidate-behavior.md) with
an intent retained before launch and a journal-scoped request identity. Every
request remains in the same bounded history, including candidates that disagree,
fail, time out, are cancelled, or have no collected outcome.

This is a private local journal. It does not register service candidates, prove
complete searches outside this journal, generate source, grant qualification or
activate a strategy. It does not replace the lower-level worker commands.

## Run and retry

Use a sealed behavior job and its exact original dataset. The journal parent
must exist; the journal is created as a real current-user directory with mode
0700. Each request ID accepts 1..80 ASCII letters, digits, underscores or hyphens.

```sh
umask 077
./bin/mantik candidate-attempt-run \
  --journal /absolute/private/candidate-attempts --request-id threshold-001 \
  --job behavior-job.json --job-sha256 "$(jq -r .sha256 behavior-job.json)" \
  --dataset dataset.json --approved-image-sha256 "$image" > attempt.json
```

The command verifies the job, dataset and approved image before reserving an
intent. `mantik-candidate-attempt-intent-v1` retains the full job and its embedded
candidate/plan provenance, a journal identity, request ID, contiguous sequence,
creation time and content hash. `execution_started=false` describes the intent's
authority, not the subsequent process state. Intent writes use immutable files,
file synchronization, atomic link publication and directory synchronization.

The first request launches into `attempt-REQUEST_ID` under the journal. Reusing
the same request ID and job only reconciles that original directory/handle;
it never launches a replacement. A changed job, dataset or image is rejected.
A new physical retry needs a new explicit request ID and remains another history
row. There is no automatic retry or request-ID reset command.

A crash after intent but before acquisition leaves an unresolved request. A
retry does not infer that no execution occurred, allocate a container, or treat
the retained intent as new launch permission. A crash after a worker's terminal
receipt but before journal outcome capture leaves a pending row until the
original handle is reconciled. The metadata lock is released during execution,
so another process can read the pending intent while the worker runs.

## Inspect and collect

```sh
./bin/mantik candidate-attempt-history \
  --journal /absolute/private/candidate-attempts > history.json
./bin/mantik candidate-attempt-reconcile \
  --journal /absolute/private/candidate-attempts --request-id threshold-001 \
  --approved-image-sha256 "$image" > recovered-attempt.json
# Add --cancel only when requesting a stop of this request's owned handle.
```

History contains every retained intent, in sequence order, including the full
job. `outcome=null` contributes to `pending`; it means no journal outcome has been
collected. It does not establish whether a container is live, stopped, absent,
or still being acquired. History performs no Docker inspection and does not stop
workers. Current host state and qualification flags remain false.

Reconciliation checks the actual owned container through the behavioral worker
protocol. A live handle before its deadline remains running unless explicitly
cancelled. An overdue owned handle can be stopped under the original host policy.
A missing, changed or uncertain handle remains unresolved; it is not recreated.
A terminal host receipt can be captured after launcher loss without rerunning
candidate code. Repeated capture retains the first outcome and capture time.

`mantik-candidate-attempt-outcome-v1` captures the original host receipt, binds it
to the intent hash, and records capture time. Before retention/readback, the
journal cross-checks the job, exact dataset/source/harness, acquisition,
receipt, stdout/stderr byte hashes, and available worker result. History reports
counts by the retained receipt status. Those are historical captures, not fresh
host-liveness attestations. The intent, outcome and history authorities are
respectively `local_candidate_attempt_intent_only`,
`local_candidate_host_receipt_capture_only` and
`local_candidate_attempt_history_only`.

Run/reconcile emits a retained intent even when collection is unresolved and
exits nonzero. It also exits nonzero for mismatches, failures, cancellation,
timeout or incomplete outcomes. Read the returned status; JSON output alone does
not mean success. Invalid pins rejected before reservation produce no intent.
A successful history command means the retained journal passed its consistency
checks, not that candidates passed their tests.

## Bounds and preservation

A journal accepts at most 64 requests across all candidates and jobs. Capacity
includes failed and pending attempts. Retries of an existing request remain
available at capacity. There is no winner filter, pruning, overwrite or deletion
operation. Unknown top-level artifacts, malformed entries, orphan outcomes/directories,
sequence gaps and changed retained output reject the whole history rather than
silently omitting records. Uncommitted top-level `.pending-*` artifacts remain
visible and prevent new reservations pending explicit inspection. Concurrent
metadata writers receive a busy error and do not allocate duplicate intents.

Keep the journal and all nested attempt artifacts private and intact. Inputs and
outputs retain the bounds of the behavioral protocol; history is bounded by 64
full jobs and their receipts. Paths and acquisitions are tied to their original
host directories and Docker handles. Copying a journal to another path is not a
supported recovery or rebinding operation. This format is not tamper-proof
against its filesystem owner, and its sequence checks cannot prove that an owner
has not removed a complete suffix or run candidates elsewhere.

## Receiver-bound execution

`candidate-worker-run` and `candidate-worker-reconcile` connect an exported
[service intent](candidate-worker-intents.md) to this local ownership protocol.
Use a new private journal for one receiver and preserve that exact root for every
retry. The commands read local files and do not contact a service or provider.

```sh
# First export the full intent with candidate-worker-intent, using independently
# inspected receiver, request and intent SHA pins, into service-intent.json.
./bin/mantik candidate-worker-run \
  --journal /absolute/private/receiver-candidate-attempts \
  --intent service-intent.json --intent-sha256 "$intent_sha" \
  --receiver-id "$receiver_id" --request-id threshold-001 \
  --dataset dataset.json --approved-image-sha256 "$image" > attempt.json

./bin/mantik candidate-worker-reconcile \
  --journal /absolute/private/receiver-candidate-attempts \
  --intent service-intent.json --intent-sha256 "$intent_sha" \
  --receiver-id "$receiver_id" --request-id threshold-001 \
  --approved-image-sha256 "$image" > recovered-attempt.json
# Add --cancel only for an explicit stop of this request's original owned handle.
```

The full exported receipt is parsed with strict bounded JSON. Its hash, receiver,
request and approved image must match the operator's expected pins. Run also
checks the job and exact dataset before reservation. Local request IDs retain the
1..80 character bound; longer service request IDs cannot run through this owner.

The new manifest/history schema is
`mantik-registered-candidate-attempt-journal-v1`. Its receiver is fixed when the
journal is created, including if creation is interrupted before its first intent.
It cannot be rebound to another receiver or mixed with an unbound journal.
Existing unbound journals retain their original schema and hashes and remain
readable; this change does not migrate, adopt or rebind their requests.

Each `mantik-registered-candidate-attempt-intent-v1` retains the complete exported
service intent in `service_intent`, alongside the full job, local owner, sequence,
request, creation time and hash. The embedded service request/job must match the
local request/job exactly, its receiver must match the manifest, and local intent
creation cannot predate service intent creation. The local hash includes the full
service receipt. History validates these links for every row before returning any
selected reconciliation result. A changed registration pin, service sequence or
timestamp conflicts even when the logical job is unchanged. Legacy unbound run
and reconcile commands cannot adopt a bound request.

The existing local outcome schema binds the local intent hash, so retained host
receipts and their exact artifacts are now linked back to this exported service
intent. Interrupted launch/collection remains pending; exact retries inspect only
the original handle. A pre-acquisition gap is unresolved and does not authorize
another launch. History remains a historical capture with current host state and
qualification credit false.

This binding establishes identity within the chosen local owner. An exported
receipt is not a service execution lease or independent registration attestation.
Using the same exported receipt under another journal or host is not globally
fenced by this protocol. Preserve one designated owner rather than moving or
recreating it; cross-owner execution fencing needs a service-owned contract.
No service outcome upload, strategy activation or qualification is performed.

`candidate-worker-capture` can export a collected bound outcome with its exact
artifacts as [portable caller-reported evidence](candidate-outcome-captures.md).
It validates the whole journal and reads existing files without querying Docker
or reconciling pending requests. The portable capture preserves historical
assertions and does not grant host verification or qualification credit.

The separate [authenticated outcome commands](candidate-worker-outcomes.md)
submit and inspect an exported capture against the service's own retained intent.
The service history pairs all retained intents and reported outcomes in one
snapshot; unreported requests do not imply a live or absent worker.
Issue #18 remains open for cross-owner execution
fencing, generated-source provenance and the final acceptance audit.
