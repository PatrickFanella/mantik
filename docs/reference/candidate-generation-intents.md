# Service-owned candidate generation intents

`storage.CandidateGenerationIntentStore` retains generation declarations before
any model output is admitted. It binds a complete [offline generation plan](recorded-candidate-generation.md)
to the service's own authoritative hypothesis publication and exact dataset.
This backend slice has no HTTP/CLI admission route, provider client, generation
output or candidate-registration action.

## Binding and time

A command names an expected receiver, request ID, publication ID and full typed
publication SHA-256, plus the complete sealed plan. The receiver selects its own
publication; a caller's publication copy is not accepted as authority. The backend
requires the publication's verified origin binding, an exactly matching sealed
hypothesis and the expected hash over compact Go publication JSON.

The plan's declared request time cannot predate the publication. Server retention
time cannot predate that plan time, and new intents cannot move the receiver's
retention clock backwards. Exact dataset bytes must match the plan's byte count,
raw hash, canonical identity and synthetic dataset contract. Image and model
labels remain declarations: no actual compiler/image/model is inspected here.

`mantik-candidate-generation-intent-v1` retains the full command, original
sequence, server `created_at`, status `intent_retained`, authority
`candidate_generation_intent_only`, and its own hash. The hash uses compact Go
JSON with its own `sha256` empty. `provider_called`, `model_identity_verified`,
`source_generated` and `qualification_credit` are false. Retaining an intent does
not establish that generation started or that a worker/model is available.
A timestamp and consistent caller plan cannot prove when an external model ran.
Future output admission must compare its plan with this own retained intent and
respect server retention time; it must not rebind old output as execution proof.

The receiver retains at most 64 intents, including unresolved requests. Distinct
request IDs remain distinct intentions even when every plan byte is identical.
An exact request retry returns the original receipt without reading a new clock,
even at capacity or after clock rollback. Changing the model label, requirement,
plan, publication pin, hypothesis, dataset or image under that ID conflicts.
There is no replacement, deletion, pruning, cancellation, output or launch method.

## Reads and preservation

| Backend method | Result |
| --- | --- |
| `StartCandidateGeneration` | Atomically retain the intent and exact dataset, or return an exact existing receipt. |
| `CandidateGenerationIntentHistory` | One receiver/history envelope containing every ordered full intent. |
| `CandidateGenerationIntent` | One selected receipt after full-prefix validation. |
| `CandidateGenerationIntentEvidence` | One receipt and exact dataset from a single validated snapshot. |

Before any read, new intent or exact retry, the backend checks the complete
ordered prefix and every publication/dataset dependency. Missing datasets,
changed hashes, invalid plan flags, sequence gaps, duplicate requests or changed
authoritative publication content reject the complete read. A selected valid row
cannot hide damage elsewhere in this generation history. Memory detaches returned
plans and dataset byte arrays.

`mantik-candidate-generation-intent-history-v1` includes the expected receiver,
all ordered intents, authority `retained_candidate_generation_intents_only`, false
provider/source-generation/qualification flags and its own content hash. Empty
intent arrays are present rather than null. It contains full plans and typed
publication pins, not every raw dataset or full publication. Its content hash is
not an independent completeness, freshness or provider-attestation mechanism.

`mantik-candidate-generation-intent-artifact-v1` contains `schema`, `intent` and
`dataset`. Validation checks the intent and supplied dataset against the plan.
The backend also checks authoritative publications before returning the artifact;
artifact validation alone cannot recover a full publication from its hash.

## Persistence and compatibility

Memory serializes generation mutations under the receiver mutex. PostgreSQL
schema 33 adds `mantik_candidate_generation_intents`, referencing the original
publication, and `mantik_candidate_generation_datasets`, referencing the intent.
Both rows are inserted in one transaction; a dataset insertion failure cannot
acknowledge a partial intent. Receiver-row locking serializes writers, and readers
use one repeatable-read snapshot. PostgreSQL streams at most 65 rows to enforce
the 64-intent bound and validates datasets one at a time, retaining only a selected
raw artifact for export.

Application receipt bounds are `candidategeneration.MaxIntentBytes`; history is
bounded by `MaxIntentHistoryBytes`. The database caps receipt JSONB at 1 MiB and
each exact dataset at 8 MiB. Ordinals are unique and restricted to 1..64, publication
and dataset links use foreign keys, and database checks reject false authority
claims in the receipt metadata. Application validation also checks all nested
plan fields and exact artifact bindings.

Migration from schema 32 creates empty generation tables. It preserves existing
candidate registry, publication, raw dataset and operational state; it does not
invent generation attempts. Schema-32 binaries refuse schema 33 rather than
silently ignoring it. Version/checksum guards remain in place. Existing pilot
services/databases are not upgraded by implementing or testing this backend.

Shared Memory/disposable PostgreSQL contracts cover concurrent starts, detached
reads, distinct requests, exact clock-free retries at capacity, changed bindings,
request/publication/retention clock conflicts, restart identity, failed second-row
insertion rollback, unrelated-prefix corruption refusal and migration preservation.
These are retained declaration checks, not model calls or generated source proof.

This is part of #18. Authenticated intent access, retained generation outcomes
bound to these own intents, explicit generated-candidate registration links, a
provider adapter and the final acceptance audit remain unfinished. No generation
intent or successful source validation activates a strategy or implies profit.
