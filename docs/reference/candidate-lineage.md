# Candidate registration and lineage contract

`internal/candidatelineage` defines the registration contract used by the memory
and PostgreSQL candidate registry backends, authenticated HTTP routes and CLI
commands. Constructing a consistent record outside a receiver-owned backend does
not make it service-owned evidence.

The command contains `receiver_id`, `request_id`, `publication_id`, optional
`parent_id`, and the full sealed candidate package. Registration consumes a
supplied authoritative hypothesis publication, an optional earlier registration,
an ordinal and a timestamp. It checks the publication's schema, receiver, original
binding flag and complete sealed hypothesis against the candidate. It retains the
hash of the whole publication, including its review and source pins. Historical
publication binding does not assert that a research decision remains applicable.

The resulting `mantik-candidate-registration-v1` row retains the entire candidate,
publication hash, parent ID and hash, receiver, contiguous sequence and UTC
registration time. Its SHA-256 covers compact Go JSON with its own `sha256` field
empty. Nested candidate declarations are detached from caller-owned slices.
The parent must already exist, belong to the same receiver and exact hypothesis
declaration, and precede the child in sequence and time. Branches may change source,
dataset or toolchain pins. A later publication of the identical hypothesis may
serve as the child's publication; its distinct publication hash remains visible.

`Append` validates the entire retained prefix before admitting another request.
The receiver-wide bound is 32 registrations, including declarations whose future
worker attempts fail or never start. Sequences start at one and contain no gaps;
IDs are unique and clocks cannot move backwards. Every parent must occur earlier
with the exact retained hash. Invalid unrelated roots also reject the operation.

An exact request retry returns the original row, sequence and time, including at
capacity or after clock rollback. Reusing a request ID with changed candidate,
publication or parent pins conflicts. A new request ID records another declaration
even when its candidate bytes match an earlier one. Candidate content identity
and registration request identity serve different purposes.

`Validate` and `ValidateHistory` check supplied structure and hashes.
`VerifyBinding` additionally reconstructs a row against its original publication
and parent. Both backends load those dependencies from their own retained records,
serialize appends under the receiver lock and atomically retain the row and exact
dataset bytes.
These helpers do not prove that a supplied prefix is the complete search history;
an owner can omit a complete suffix without creating a gap in that prefix.

Authority is `candidate_registration_declaration_only`. `qualification_credit`,
`execution_started` and `dataset_availability_verified` remain false. The embedded
candidate keeps `caller_supplied_research_source_only` authority and its original
false service-binding and execution flags. Invalid Go remains a valid source
declaration so later compilation failures can be retained. No dataset bytes are
loaded, image inspected, model called or worker launched by this package.

The bounded command reader rejects duplicate or unknown JSON fields, case aliases,
trailing documents, invalid nested candidate pins and oversized input. Commands
and rows are capped at the candidate package bound plus 4 KiB.

## Persistence and readback

`storage.CandidateStore` exposes registration, complete ordered history, selected
registration and exact dataset readback. Every operation validates the entire
retained prefix, publication bindings and raw dataset pins before selecting a row
or admitting an exact retry. Damaged unrelated evidence blocks a read or write;
it is not filtered out. Missing or modified dataset bytes and changed publications
therefore cannot disappear behind an otherwise valid selected registration.

Registration checks the submitted dataset's exact byte count, raw SHA-256 and
canonical seal. Source text remains embedded in its candidate package, including
invalid Go. Returned records and bytes are detached from stored values. The outer
registration does not rewrite the embedded candidate's original provenance flags.
Dataset validation on a particular read grants no durable provider availability
or qualification credit; `dataset_availability_verified` stays false.

Schema 30 adds `mantik_candidate_registrations` and `mantik_candidate_datasets`.
The receiver row lock serializes capacity, ordinal, clock and retry decisions.
Metadata and dataset insertion share one transaction, so a failed artifact insert
cannot leave an acknowledged or partial registration. PostgreSQL reads use one
repeatable-read snapshot. They stream and validate dataset rows individually,
retaining only candidate metadata and an explicitly selected artifact in memory.
No update, deletion, pruning, import or restore operation is exposed.

Migration from schema 29 creates an empty registry and preserves prior research
and operational state. The migration checksum and maximum-version fences apply;
an older schema-29 binary refuses a schema-30 database. This is an upgrade boundary:
do not point an older binary at the upgraded database as a rollback. Existing
pilot databases have not been migrated by developing or testing this contract.

Disposable database tests cover concurrent identical registration, restart and
byte retention, artifact-insert rollback, capacity/retries, corruption refusal,
upgrade preservation and migration checksum refusal. They establish backend
behavior without claiming an HTTP deployment or worker run.

## HTTP and CLI

The existing service bearer-token boundary protects all four routes:

| Route | Result |
| --- | --- |
| `POST /v1/candidates` | Register the command and exact dataset atomically; exact retries return the original receipt. |
| `GET /v1/candidates` | Complete `mantik-candidate-registry-v1` receiver history, including an empty array when no declarations exist. |
| `GET /v1/candidates/{id}` | One retained registration after full-prefix validation. |
| `GET /v1/candidates/{id}/dataset` | `mantik-candidate-dataset-v1` containing the registration and exact dataset from one backend snapshot. |

Submission is a JSON object with `command` and `dataset`. The latter is the base64
encoding of the exact file bytes, including whitespace. The bounded reader rejects
unknown or duplicate fields, case aliases, trailing JSON documents and inputs that
do not match the command's raw and canonical pins. Requests have no query options;
the endpoints do not filter to successful candidates or launch a worker. Invalid
input returns 422, identity/clock/capacity conflicts return 409 and missing
registrations or publications return 404. Backend corruption is refused.

The registry envelope retains receiver identity, ordered full registrations,
`retained_candidate_declarations_only` authority and false execution/qualification
flags. Its SHA-256 covers compact Go JSON with its own `sha256` empty. A consistent
envelope is a content pin, not independent attestation of server ownership or
complete search outside that receiver. Dataset envelopes include their declaration
so clients can validate the returned raw bytes before exporting anything.

```bash
mantik candidate-register --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id root --publication PUBLICATION \
  --candidate candidate.json --candidate-sha256 CANDIDATE_SHA \
  --dataset dataset.json

mantik candidate-registrations --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER

mantik candidate-registration --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id root --registration-sha256 REGISTRATION_SHA

mantik candidate-dataset --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER --id root --registration-sha256 REGISTRATION_SHA \
  --format raw > retained-dataset.json
```

Use the actual receiver ID and content hashes from inspected artifacts and retained
receipts. Child registration adds `--parent EXISTING_ID`. `MANTIK_API_TOKEN` supplies
the existing service token. All four commands require an expected receiver ID;
selected registration and dataset reads also require the expected registration
SHA. Registration verifies the local candidate and dataset before sending a request
and checks the response against its command. Read commands reject malformed or
changed identities before emitting an artifact. Candidate response readers enforce
strict bounded JSON, including rejection of trailing documents.

Dataset output defaults to a JSON envelope with base64 bytes. `--format raw` emits
the verified dataset without appending a newline. The shell opens a redirection
target before starting the command; choose a new path so a rejected request cannot
truncate an existing artifact. None of these commands contacts a model/provider,
compiles code or invokes Docker. Existing pilots have not been upgraded by adding
these commands.

The disposable PostgreSQL harness now also runs the CLI/HTTP integration contract.
It registers a synthetic source, compares retries, reads the full lineage, exports
exact bytes, reopens the backing database behind a fresh HTTP handler and verifies
the same pinned receipts and artifacts. Memory tests cover authentication, strict
request/response JSON, unsupported query/launch paths and refusal of forged
readbacks before output.

[Candidate worker intent backends](candidate-worker-intents.md) now retain exact
registration/job bindings before execution, with authenticated intent API/CLI
access and coherent history. Receiver-bound local execution journals now retain
the full exported intent and require its exact pins before launch or reconciliation.
[Outcome backends](candidate-worker-outcomes.md) now retain complete caller-reported
captures against the service's own intent and dataset bindings. Authenticated
outcome access, cross-owner execution fencing, generated-source
provenance and the final #18 acceptance audit remain unfinished. A registration grants no activation,
profitability, independent execution or qualification proof.
