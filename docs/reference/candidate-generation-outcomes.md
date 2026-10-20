# Recorded generation outcome contracts

`internal/candidategeneration` defines portable reports and receipts for output
associated with an exact [generation intent](candidate-generation-intents.md).
This slice contains pure value contracts only. It adds no persistence, HTTP or CLI
admission, provider adapter, model call, compiler or process launcher.

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

Before using these constructors or acknowledging any retry, a future backend must
validate every retained full recording and each authoritative publication/exact
dataset in the same owner snapshot. A selected valid row cannot hide corruption
elsewhere. A portable artifact alone has neither those authoritative publication
bytes nor a database snapshot. Storage must retain paired metadata/recording rows
atomically, preserve failed reports, and compare full records before exact retry.

Pure tests cover source/failed-output preservation, original CRLF source bytes,
strict readers, forged metadata, detached results, temporal/identity conflicts,
sparse complements, all 64 intents, and retries that must not read the clock.
Durable Memory/PostgreSQL retention, authenticated output access, explicit
candidate-registration lineage, a provider adapter and the final #18 acceptance
audit remain unfinished. No output receipt activates a strategy or implies profit.
