# Retained candidate worker outcomes

`storage.CandidateWorkerOutcomeStore` retains one immutable caller-reported
outcome for each [service intent](candidate-worker-intents.md). Memory and
PostgreSQL backends preserve the complete [portable capture](candidate-outcome-captures.md),
including failed or incomplete output, alongside a small structured receipt.
Authenticated HTTP and CLI commands submit and inspect these reports without
launching or inspecting a worker host.

## Admission and identity

A report names the receiver, request and expected service-intent SHA-256 and
includes the full portable capture. The backend uses its own retained intent,
not the service receipt supplied inside that capture. It validates the complete
candidate registry, authoritative publication links, exact retained datasets and
all service intents before comparing the report's full intent/job binding.
Case inputs are derived again from the server's dataset through existing intent
validation. Rehashing a caller's substituted service receipt does not rebind it
to a real retained intent.

The capture must validate all declared receipt, output and profile links. A new
server retention time cannot predate its claimed local capture time. An exact
retry returns the original receipt and retention time without consulting a new
clock. Changing the capture, reported local owner or any bound service receipt
under the request conflicts. The original capture is never overwritten. A new
physical attempt requires a distinct retained intent and request ID.

The receipt schema is `mantik-candidate-worker-outcome-v1`. It retains receiver,
request and original intent ordinal; service-intent, capture and job hashes;
reported journal identity and root hash; local intent/outcome and acquisition
hashes; reported host status/reason and available worker-result identity; server
retention time; and its own content hash. Hashing uses compact Go JSON with its
own `sha256` field empty.

Authority is `retained_caller_reported_candidate_outcome_only`.
`execution_verified`, `current_host_state_known` and `qualification_credit` are
false. A `completed` status is a consistent reported result, not independent
execution proof. Neither a retained report nor a successful compilation/example
check activates a strategy or implies profitability.

## Reads and preservation

| Backend method | Result |
| --- | --- |
| `ReportCandidateWorkerOutcome` | Retain a report or return its exact existing receipt. |
| `CandidateWorkerOutcomes` | Every structured receipt in original intent order. |
| `CandidateWorkerOutcome` | One receipt after complete history validation. |
| `CandidateWorkerOutcomeCapture` | One complete capture after complete history validation. |
| `CandidateWorkerOutcomeEvidence` | One receipt and capture from the same validated snapshot. |

Outcomes may arrive out of intent order. Gaps mean that those intents have no
retained outcome; they are not invented failures, successes or assertions of
current host state. There can be at most 64 outcomes because every row references
one of the receiver's at most 64 immutable intents. Failed, cancelled, incomplete
and pending work remains part of the corresponding intent/history evidence.
No winner filter, replacement, pruning, cancellation or deletion operation exists.

Before any outcome read, new admission or exact retry, the backend checks every
summary and capture against the same validated intent/registration/dataset
snapshot. Capture bytes that disagree with their receipt, self-hashed summary/
capture mismatches, or corrupt unrelated intent dependencies reject the complete read. A selected
receipt or artifact cannot hide another broken outcome. Memory detaches returned
captures from its retained bytes.

Lists return small receipts rather than every full capture. PostgreSQL loads
bounded metadata, closes those rows, then validates captures one at a time,
retaining only the selected artifact for export. Captures are stored as compact
typed Go JSON; original transport whitespace is not a separate artifact. The
portable format's pre-start/stdout/stderr byte arrays remain exact. These are
private source/output records.

The reported journal identity fixes the first retained report for this request.
It does not claim a service-owned execution lease or prevent another host from
executing the same exported intent. A caller can fabricate consistent historical
artifacts; content checks and authenticated service storage do not independently
attest that a worker ran.

## Persistence and migration

Memory serializes admission and readback under the receiver mutex. PostgreSQL
schema 32 adds `mantik_candidate_worker_outcomes`: a primary-key reference to the
service intent, a unique original intent ordinal, a bounded JSONB receipt and a
bounded BYTEA capture. BYTEA avoids storing another expanded JSONB copy of all
base64 artifacts. The application enforces `candidateevidence.MaxBytes`; the
database also caps capture bytes at 24 MiB and summary JSON at 8 KiB.

The receiver row lock serializes writers. One transaction inserts the receipt
and capture; insertion failure acknowledges neither. Readback uses a repeatable-
read snapshot across registry, datasets, intents, summaries and captures.
Migration from schema 31 creates an empty table and preserves existing intent
receipts, registry, dataset bytes and operational state. Version/checksum fences
apply; a schema-31 binary refuses a schema-32 database. No downgrade or evidence
deletion command is provided.

Shared memory/disposable PostgreSQL checks cover concurrent reports, out-of-order
arrival, exact retries, changed capture/owner/service identity refusal, opaque
binary output, clock refusal, restart, insertion rollback, complete-history
corruption refusal and migration preservation/checksum refusal. These are backend
contract checks, separate from actual worker execution or qualification.

This is part of #18. Coherent pending-work summaries, cross-owner execution
fencing, generated-source provenance and the final acceptance audit remain
unfinished. Existing pilot databases and services have not been upgraded by
implementing or testing this contract.

## Authenticated transport and CLI

The bearer-authenticated endpoints are:

| Method and path | Result |
| --- | --- |
| `POST /v1/candidate-worker-outcomes` | Submit a `candidateevidence.Report` and return its retained receipt. |
| `GET /v1/candidate-worker-outcomes/{id}` | Inspect the selected receipt. |
| `GET /v1/candidate-worker-outcomes/{id}/capture` | Return the receipt and full capture in one artifact envelope. |

The capture endpoint uses `mantik-candidate-worker-outcome-artifact-v1`:
`schema`, `outcome`, `capture`, and its own `sha256` (compact Go JSON, own hash
empty). The outcome must reconstruct exactly from the capture at its retained
server time. Memory obtains both under one mutex; PostgreSQL obtains both from
one repeatable-read snapshot. No separate intent/history requests are combined
to assert a coherent pending-work view. There is no HTTP outcome list yet.

POST bodies are bounded to `candidateevidence.MaxReportBytes` (the maximum
capture plus 1,024 bytes of report pins). Unknown, duplicate, mis-cased, trailing,
null and invalid nested data are rejected before admission. Query parameters
are refused. Storage operations use a five-second request context. Invalid
reports return 422, identity/evidence conflicts 409, and missing retained intents
or outcomes 404. Authentication is required for all three paths. There is no
launch, cancellation, replacement or deletion endpoint.

With `MANTIK_API_TOKEN` supplied privately, submit an exported capture:

```sh
mantik candidate-worker-outcome-report \
  --api-url http://127.0.0.1:PORT \
  --receiver-id RECEIVER --id REQUEST \
  --intent-sha256 INTENT_SHA --capture-sha256 CAPTURE_SHA \
  --capture capture.json
```

The CLI validates the capture and all explicit pins before sending a request.
It then reconstructs the returned receipt against that exact capture before
printing it. A successful command means the caller report was retained; even
an incomplete or failed report can be validly retained.

Inspect the selected outcome, or export its capture or exact output bytes:

```sh
mantik candidate-worker-outcome \
  --api-url http://127.0.0.1:PORT \
  --receiver-id RECEIVER --id REQUEST \
  --intent-sha256 INTENT_SHA --capture-sha256 CAPTURE_SHA \
  --outcome-sha256 OUTCOME_SHA \
  --artifact outcome
```

`--artifact` accepts `outcome` (default), `capture`, `stdout`, or `stderr`.
The latter two write exact bytes without an added newline, including invalid
UTF-8 and partial binary output. Capture/artifact exports validate the complete
bound envelope before writing anything. Every read requires expected receiver,
request, intent, capture and retained-outcome identities. These commands accept
an HTTP origin without credentials, path, query or fragment, and no positional
arguments. CLI JSON responses use strict bounded readers.

Shared Memory/disposable PostgreSQL HTTP/CLI checks cover exact retry and
receipt preservation, changed reported-owner conflict, strict request parsing,
authentication, query refusal, exact binary output, and full capture/receipt
readback after reopening PostgreSQL. Client checks reject changed local pins
before HTTP and inconsistent or ambiguous responses before output. These checks
remain separate from native worker execution, deployment and qualification.
