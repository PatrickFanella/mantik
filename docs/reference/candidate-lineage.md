# Candidate registration and lineage contract

`internal/candidatelineage` defines the pure registration contract that the
service candidate registry will use. It has no storage backend, HTTP route or CLI
command yet. Constructing a consistent record outside the service does not make
it service-owned evidence.

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
and parent. Future persistence must load those dependencies from its own retained
records, serialize appends under its receiver lock and atomically retain the row.
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

This is a prerequisite for the persisted registry and attempt linkage in #18.
Service persistence, exact artifact retention, API/CLI access, generated-source
provenance and the final acceptance audit remain unfinished. A registration grants
no activation, profitability, independent execution or qualification proof.
