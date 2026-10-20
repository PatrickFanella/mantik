# Recorded generation outcome contracts

`internal/candidategeneration` defines portable reports and receipts for output
associated with an exact [generation intent](candidate-generation-intents.md).
The pure contracts are now backed by `storage.CandidateGenerationOutcomeStore`
for Memory and PostgreSQL. Authenticated HTTP/CLI commands admit and inspect recorded output. They add no
provider adapter, model call, compiler or process launcher.

## Report and receipt

`mantik-candidate-generation-report-v1` contains `schema`, expected `receiver_id`,
`request_id`, `intent_sha256` and the full [recording](recorded-candidate-generation.md).
The recording preserves the exact output bytes, full immutable plan, claimed
capture time, and either a sealed candidate or a retained rejection. Invalid Go
source remains a candidate draft for separate isolated-worker checks. Empty,
binary and partial output remain recorded failures; they are not repaired.

`NewOutcome` compares the recording's complete plan with an owner-supplied intent,
including its exact dataset, toolchain and model-label declarations. The expected
receiver, request and intent hash must match. The claimed capture time must be at
or after the intent's server retention time, and server report retention cannot
predate that capture time. These comparisons reject inconsistent claims; portable
timestamps cannot prove when an external model actually ran.

`mantik-candidate-generation-outcome-v1` includes the receiver/request, original
intent ordinal/hash, plan/recording/output hashes, exact output byte count,
optional candidate hash, claimed capture time, server retention time and derived
status/reason. Its authority is
`retained_caller_reported_candidate_generation_only`. Provider call, model
identity/authorship, source compilation/execution and qualification flags remain
false. Its hash uses compact Go JSON with its own `sha256` empty.

The compact receipt validates metadata but cannot derive an output classification
from an output hash alone. `mantik-candidate-generation-outcome-artifact-v1`
therefore carries the full intent, outcome and recording. Its validator rebuilds
the expected receipt from the complete recording and intent and compares every
field. Construction detaches all nested input values and exact output bytes.

Report and artifact readers reject oversized input, unknown/mis-cased/duplicate
fields, invalid base64 and trailing documents. Their bounds include the complete
plan, candidate source and base64 raw output; they never truncate evidence.

## Ordered history and retries

`mantik-candidate-generation-outcome-history-v1` contains the complete coherent
intent history, compact outcomes in original intent order, and the exact ordered
complement `unreported_request_ids`. Reports may arrive in any order and leave
holes. Null arrays, duplicate/out-of-order outcomes, changed owner/intent/plan
pins and missing/reordered complement entries are invalid. Its authority is
`retained_candidate_generation_reports_only`, with false provider/authorship/
qualification flags. Its content hash does not independently prove completeness,
freshness or the absence of external provider activity.

`AppendOutcome` is the pure retry rule over an owner-validated snapshot. It
requires an existing matching intent. The first report derives a receipt at the
supplied server clock. An exact retry returns the original receipt without
consulting a new clock, including with all 64 intents reported. A changed output,
recording, capture time, plan or intent under the same request conflicts. The
function does not mutate the supplied history or retain output by itself.

Before using these constructors or acknowledging any retry, the backend must
validate every retained full recording and each authoritative publication/exact
dataset in the same owner snapshot. A selected valid row cannot hide corruption
elsewhere. A portable artifact alone has neither those authoritative publication
bytes nor a database snapshot. Storage must retain metadata and complete recordings
atomically, preserve failed reports, and compare full records before exact retry.

Pure tests cover source/failed-output preservation, original CRLF source bytes,
strict readers, forged metadata, detached results, temporal/identity conflicts,
sparse complements, all 64 intents, and retries that must not read the clock.
Explicit candidate-registration lineage, a provider adapter and the final #18
acceptance audit remain unfinished. No output receipt activates a strategy or implies profit.

## Atomic storage and complete-prefix validation

| Store method | Result |
| --- | --- |
| `ReportCandidateGeneration` | Retain one exact caller recording and derived outcome for an own retained intent, or return the original retry receipt. |
| `CandidateGenerationOutcomeHistory` | One complete intent/report snapshot and exact unreported complement. |
| `CandidateGenerationOutcome` | A selected outcome after complete-prefix validation. |
| `CandidateGenerationOutcomeEvidence` | A selected full intent/outcome/recording artifact from that snapshot. |

Before every report, retry or read, the backend validates every retained generation
intent and its authoritative publication/exact dataset, then every complete
recording and derived outcome. It rechecks raw dataset bindings, full plan and
intent bindings, exact output and candidate identities, flags and times. Missing
or damaged unrelated evidence prevents selected reads and retries. Returned
nested plans and output bytes are detached from storage.

Memory serializes access with its receiver mutex and retains serialized complete
recordings alongside metadata. PostgreSQL schema 34 adds
`mantik_candidate_generation_outcomes`: one row contains the derived JSONB
receipt and complete recording JSON as BYTEA. The intent FK and unique bounded
ordinal pin each report to one own intent. A single insert commits metadata and
recording together; an insert failure acknowledges neither. Exact retries compare
the full recording-derived receipt without consulting a new clock. There is no
replace, delete, pruning, provider launch or registration method.

PostgreSQL writers lock the receiver row before reading authoritative dependencies;
readers use one repeatable-read transaction. At most 65 metadata rows are loaded
to enforce the 64-intent bound. Recordings and datasets are checked one at a time;
only a selected full recording survives validation for artifact export. Metadata
is capped at 8 KiB and stored recording JSON at 4 MiB; application validation
applies the tighter `MaxRecordingBytes` and exact nested bounds. No raw recording
is omitted from backend validation when constructing a compact history.

Schema-33 migration creates an empty output table, preserves existing generation
intents/publications/raw datasets/candidate registry/operations, and reports
existing intents as unreported. Old binaries refuse the newer schema. Version and
checksum guards remain enforced. Existing pilot services/databases are not
upgraded by implementing or testing schema 34.

Shared Memory/disposable PostgreSQL contracts exercise concurrent exact reports,
source/empty/binary output preservation, sparse/out-of-order admission, all 64
intents, clock-free retry at capacity, changed ownership/plan/output/time refusal,
failed insert with unchanged history, full reopened-database readback, unrelated
recording/metadata/dataset/publication corruption, migration preservation and
checksum refusal. This proves retained caller evidence behavior; it does not
prove model generation, authorship, qualification or profitability.

## Authenticated output access

All routes require the service bearer token and reject query parameters,
including malformed queries. POST accepts strict bounded report JSON. Unknown,
mis-cased or duplicate fields, invalid base64, oversized bodies and trailing
documents are rejected. Invalid input returns 422, conflicting owner/binding or
capacity 409, and missing own intent/report 404. Store operations have a five
second context deadline.

| Route | Result |
| --- | --- |
| `POST /v1/candidate-generation-outcomes` | Retain an exact report or return its original retry receipt. |
| `GET /v1/candidate-generation-outcomes` | Complete coherent intent/report history and unreported complement. |
| `GET /v1/candidate-generation-outcomes/{id}` | Selected compact receipt after full-prefix validation. |
| `GET /v1/candidate-generation-outcomes/{id}/recording` | Full intent/outcome/recording artifact from one validated snapshot. |

With `MANTIK_API_TOKEN` already supplied privately:

```sh
mantik candidate-generation-outcome-report --api-url http://127.0.0.1:41890 \
  --receiver-id "$RECEIVER" --id generation-1 \
  --intent-sha256 "$INTENT_SHA" --plan-sha256 "$PLAN_SHA" \
  --recording recording.json --recording-sha256 "$RECORDING_SHA"

mantik candidate-generation-outcomes --api-url http://127.0.0.1:41890 \
  --receiver-id "$RECEIVER"

mantik candidate-generation-outcome --api-url http://127.0.0.1:41890 \
  --receiver-id "$RECEIVER" --id generation-1 \
  --intent-sha256 "$INTENT_SHA" --plan-sha256 "$PLAN_SHA" \
  --recording-sha256 "$RECORDING_SHA" --outcome-sha256 "$OUTCOME_SHA" \
  --artifact output
```

The report command validates the bounded local recording and its expected hashes
before contacting the service. It then fetches and validates the exact pinned
service intent and compares the full recording plan and claimed capture time
before POST. It rederives every returned outcome field from that intent and local
recording before emitting the receipt. This read followed by admission does not
start generation; the backend repeats authoritative validation at admission.

The selected command requires all receiver/request/intent/plan/recording/outcome
pins and supports `outcome` (default), `recording`, `output`, `candidate` and
`source`. It always validates the complete coherent artifact before output.
`output` preserves raw failed or source-bearing bytes without a newline;
`source` preserves exact original Go text including CRLF. Candidate/source exports
for rejected recordings fail without writing output. History has no filters and
validates the entire envelope and expected receiver before output.

Shared Memory/PostgreSQL HTTP and CLI contracts cover success and rejected output,
exact retries, strict JSON/auth/query checks, wrong pins, byte-preserving exports,
forged receipts/artifacts/histories and full reopened-database readback. Bad local
recording pins cause no service request; an unexpected intent causes no POST.
These commands do not register candidates, call a provider or activate a strategy.
