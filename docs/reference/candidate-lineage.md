# Candidate registration and lineage contract

`internal/candidatelineage` defines the registration contract used by the memory
and PostgreSQL candidate registry backends. HTTP routes and CLI commands are not
wired yet. Constructing a consistent record outside a receiver-owned backend does
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

API/CLI access, registry-linked worker attempts, generated-source provenance and
the final #18 acceptance audit remain unfinished. A registration grants no
activation, profitability, independent execution or qualification proof.
