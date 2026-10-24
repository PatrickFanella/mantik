# Retained service-case reviews

The service retains recorded-model review attempts against an exact news-case
version. Starting an attempt captures application-owned evidence; completion
reruns the bounded review contract against that retained snapshot and checks
current applicability before committing. No client-supplied snapshot, check
record or finalized result is accepted.

```sh
./bin/mantik news-cases --api-url http://127.0.0.1:40880
./bin/mantik news-review-start --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id REVIEW_REQUEST_ID --case NEWS_CASE_ID --version CURRENT_VERSION
./bin/mantik news-review-complete --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id REVIEW_REQUEST_ID --model-fixture /absolute/private/model.json
./bin/mantik news-review --api-url http://127.0.0.1:40880 --id REVIEW_REQUEST_ID
```

Use the existing `MANTIK_API_TOKEN` environment variable. Every write requires an
explicit logical receiver and stable request ID. The model fixture uses the same
artifact array as [local research review](research-review.md). All attempts carry
`model_mode: recorded_model_fixture`; this does not activate a provider or worker.

## Captured evidence

A start request names an open news case and its exact evidence version. Under
the same news mutation lock used by article admission, the application validates
the bounded retained original/result chains and captures the case's event claims,
lineage states and original-record references. It retains the receiver, case ID,
evidence version, generation, start cutoff and canonical event-snapshot hash.
Full original article bodies are not copied into an attempt.

The snapshot permits at most 32 claims and 256 KiB of serialized group evidence.
A broader case requires a separately reviewed evidence-selection policy; it is
rejected before creating a partial attempt. The retained source articles remain
available through the news-record inspection path. Model frames still expose
only claim references until the model requests a specific excerpt.

A receiver retains at most 32 attempts, including pending and terminal attempts.
Capacity does not evict failures or completed work. Repeating an existing start
with the same case/version returns that original attempt, even if the case has
since changed. Rebinding its request ID to another case/version is a conflict.
A fresh request ID is required for a new investigation. Start retries do not
reset the original deadline or replace the captured evidence.

## Completion and current applicability

Completion accepts only recorded model artifacts. The application canonicalizes
the artifact array as Go JSON, normalizing insignificant whitespace while
preserving object field order and duplicate fields inside raw artifacts, and
hashes those bytes. It evaluates the pinned evidence outside the database/news
lock. No model callback runs while that lock is held.

The completion transaction reacquires the news lock, then locks the attempt. It
rechecks receiver identity, attempt state, server time and the current case. It
retains the result, artifact digest, raw artifact strings and completion time:

| Stored status | Meaning |
| --- | --- |
| `pending` | Snapshot retained; no completion committed |
| `completed` | Finished before its deadline while the same case version/generation remained open |
| `superseded` | Completed with a changed version/generation or closed case; output retained for audit |
| `expired` | Completed at or after the original deadline; output retained for audit |

The completion deadline is 30 seconds after start, matching the current review
contract. It bounds investigation completion, not the validity of a finalized
trading decision. A backward server clock blocks a new completion. Exact retries
of an already committed completion return its original result without resampling
its completion time. Different artifact bytes under the same attempt conflict;
whitespace normalized by the canonicalization step does not create a conflict.

Readback computes `evidence_current` and `review_applicable` from the current
case in the same statement/lock snapshot as the attempt. `evidence_current`
requires the pinned version/generation and an open case. `review_applicable`
also requires a completed research result with unqualified authority. A result
can therefore remain historically `completed` while its live applicability turns
false after new news or an operator closure. Historical results are not rewritten
to disguise that change.

An applicable result remains research-only. Completion never closes a case,
publishes a hypothesis, changes risk or qualification, or creates operations or
economic effects. A checked abstention is a current research result too; an
applicable flag does not mean a tradable hypothesis exists.

## Persistence, recovery and bounds

Migration 12 adds `mantik_news_reviews`, separate from news originals, fixture
state and operations. PostgreSQL transactions take the news row lock before an
attempt row lock, so starts, article admission, closures and completion checks
serialize consistently. Completed result and artifact writes commit together.
An interrupted persistence write leaves the pending attempt intact.

Raw model artifacts are retained as JSON strings rather than JSONB objects.
This preserves duplicate-field malformed artifacts and their original canonical
ordering for audit; JSONB normalization cannot silently deduplicate their fields.
The bounded interpreter separately retains its check records and artifact hashes.
A fixture contains 1..16 JSON artifacts and at most 128 KiB after canonicalization;
individual artifacts larger than the interpreter's 16 KiB limit produce abstention.
The HTTP command envelope is limited to 160 KiB. Inspection reads one attempt and
the current case projection, not every source original or the full attempt ledger.

Start/completion retries recover retained identity after a lost response. Database
restart and isolated restore preserve pending snapshots, original deadlines,
completed results and artifact digests. If a request is canceled before its
completion transaction commits, the attempt can remain pending; inspect and retry
against the same receiver before assuming it failed. No background lease,
expiry sweep or retry worker is created here. An overdue pending attempt stays
pending until a completion request records its expired disposition.

Schema-11 binaries reject schema 12. Deployment needs a verified backup and
planned rollback/restore; this development change does not migrate existing
pilots. Same-host disposable restart/restore tests do not establish off-host
recovery or provider behavior.

## API

| Method and path | Input |
| --- | --- |
| `POST /v1/news-reviews` | `receiver_id`, `request_id`, `case_id`, `version`, optional `type_id` |
| `POST /v1/news-reviews/ID/complete` | `receiver_id`, `steps` array |
| `GET /v1/news-reviews/ID` | No query parameters |
| `GET /v1/news-cases/ID/reviews` | Complete bounded case review history; no query parameters |

All routes use the existing bearer authentication boundary. Successful writes
and inspection return the retained attempt and live applicability flags. Invalid
command envelopes return 400, invalid bounded commands 422, missing attempts 404,
receiver/identity/evidence/capacity conflicts 409, and persistence failures 503.
Structured interpreter abstentions are retained successful completions, not HTTP
errors. Provider clients, automatic dispatch, leases, hypothesis publication and
additional decision types remain separate units.

## Explicit retained review profiles

New review attempts pin an application-owned `mantik-review-profile-pin-v1`
record containing the complete review contract, advertised native method
inventory and a canonical SHA-256. The contract fixes the type, allowed tools,
required evidence/checks, completion outcomes, budgets, deadline policy and
research authority. The caller selects a known type, never supplies a contract,
method inventory or risk configuration.

```sh
./bin/mantik news-review-start --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id method-review-1 \
  --case "$MANTIK_NEWS_CASE_ID" --version 1 --type news-method-review-v1
./bin/mantik news-review-complete --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id method-review-1 \
  --model-fixture /absolute/private/method-review-artifacts.json
```

The default type remains `news-review-v1`. The opt-in
[method-aware profile](research-review.md#method-aware-local-review-profile)
adds targeted native method inspection and mandatory method binding. Completion
has no type-selection field: it uses the retained profile and binds the result's
`profile_sha256` to that exact pin before computing the finalized result ID.
Profile definitions and method inventories must match a supported compiled
version. Changed or unknown stored profiles fail before any model call; they
are not silently reinterpreted using a new default.

Older attempts without a profile retain their legacy v1 interpretation, including
pending attempts completed after upgrade. Their results have no profile hash.
New default-v1 attempts also retain an explicit profile/hash. An exact start
retry returns its original attempt, including after completion or at capacity,
without sampling the clock. Selecting a different type for the same request ID
conflicts. All profiles share the existing global 32-attempt limit, version/
generation fence, deadline, immutable completion and retained stale/expired
output behavior. No model or provider runs under a storage lock.

The start API adds optional `type_id` to `POST /v1/news-reviews`. Omitted or empty
values select v1; unknown types return 422. Start bodies are bounded to 4 KiB and
reject duplicate, unknown and noncanonical field names. Existing completion
bodies retain their separate bound and original model-artifact preservation;
invalid model JSON remains evidence that can finalize abstention. Neither
completion nor start accepts a caller-supplied profile or automatic check record.

A current completed method-aware review may support hypothesis publication only
when all six application checks passed, its exact inspected compiled method
contracts remain bound, and the declaration uses precisely the candidate's
method set. Changing that set conflicts. Existing source-excerpt, origin,
mechanism/refutation, receiver, case applicability and final-result hash checks
still apply. Publication remains an unqualified research act and starts no
trial or operation automatically.

Schema 19 adds a review-profile shape constraint and fences out schema-18
binaries. It leaves existing review payloads untouched. Deployment requires a
verified backup and planned restore rollback; this development change does not
upgrade original pilot databases. Verification covers legacy pending/completed
records, concurrent starts/completions, immutable profile retries, method-set
publication conflicts, stale/expired retention, atomic write failure, capacity,
restart, migration checksums, authenticated CLI/HTTP and isolated backup/restore.

## Retained hypothesis inventory and reuse

```sh
./bin/mantik news-review-start --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id reuse-review-1 \
  --case "$MANTIK_NEWS_CASE_ID" --version CURRENT_VERSION \
  --type news-reuse-review-v1
./bin/mantik news-review-complete --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id reuse-review-1 \
  --model-fixture /absolute/private/reuse-review-artifacts.json
```

The service captures its complete retained hypothesis catalogue under the same
lock used for news admission, review starts and hypothesis publication. It
validates all sealed declarations and derives a canonical inventory of at most
32 distinct declaration references. Repeated identical publications share a tool
reference; their complete attribution remains in the catalogue identified by its
hash. An empty catalogue produces a valid empty inventory. Corrupt or excessive
catalogues reject the start as a whole, without a partial attempt. A review start
cannot precede a captured publication's timestamp.

The attempt retains server-owned `hypothesis_inventory` with its schema,
receiver, source catalogue hash, sealed declarations and inventory hash. HTTP
start accepts only the existing receiver/request/case/version/type fields.
Neither start nor completion accepts caller-supplied inventories, catalogue pins,
checks or finalized results. Exact start retries return the original inventory
without consulting the clock or a later catalogue. All profiles share the same
32-attempt capacity; completed, failed, expired and superseded work is retained.

Completion runs the [same bounded interpreter](research-review.md#inspect-existing-hypotheses-in-a-recorded-review)
outside storage locks. Models see compact summaries and receive full declarations
only after targeted inspection. A reuse candidate must bind an inspected exact
declaration, inspected native methods and eligible current news. Finalized
results bind the profile, inventory and source catalogue hashes plus receiver.
Storage checks those pins again before committing. Later catalogue additions do
not change the captured inventory. Stale news and deadline expiry still retain
the result while preventing current applicability; no fallback captures newer
evidence during completion or retry.

Hypothesis publication remains a separate request. It independently checks the
selected declaration against authoritative retained publications under the news
mutation lock and requires the proposed declaration to name the same exact
question. New attribution gets a new receipt; no original origin or condition is
rewritten. The interpretation remains unqualified even when every check passes.

Schema 20 extends the review-profile constraint and requires inventory pins for
reuse attempts while forbidding them on older profiles. Application reads also
check the canonical inventory hash and sealed declarations before evaluation.
Existing pending/completed attempts and JSON payloads are unchanged by migration.
Schema-19 binaries refuse schema 20. Deployment requires a verified backup and
a planned restore rollback; preserving post-upgrade evidence is part of that
plan. Do not downgrade by removing schema records or constraints. No original
pilot database is upgraded by development checks.

## Complete case review history

```sh
./bin/mantik news-reviews --api-url "$MANTIK_API_URL" \
  --case "$MANTIK_NEWS_CASE_ID"
./bin/mantik news-review --api-url "$MANTIK_API_URL" --id REVIEW_ID
```

The authenticated history route returns `mantik-case-review-history-v1` for one
exact news case. It includes every retained review across evidence versions and
case generations, including unpublished abstentions, pending attempts, rejected
tool calls, expired results and superseded output. An existing case with no
attempts returns an empty list; an unknown case returns 404. The same global
32-attempt storage limit bounds each complete response. There is no pagination,
ranking or outcome filter; query parameters are rejected.

The response contains the current case metadata and one compact summary per
attempt, ordered by start time then request ID. Each summary retains the subject
and evidence snapshot hash, selected type/profile hash, optional hypothesis
inventory/catalogue hashes, stored status and timestamps, live applicability,
result ID, artifact digest, outcome/reason, proposal reason codes, optional exact
reuse reference, artifact/tool counts, rejected-tool count and mandatory checks.
`attempt_sha256` binds the complete Go-JSON attempt returned by targeted
`news-review` inspection. Original excerpts, raw model artifacts, complete news
groups and full hypothesis inventories are not copied into the history response.

Status and outcome counts are separate: an expired result can contain a checked
hypothesis proposal while remaining inapplicable. A pending attempt stays pending
until a completion records its disposition; this read does not sample a clock or
expire it. Automatic abstentions retain their failed/not-run check records and
reason codes, rather than disappearing from the search history. Closing or
reopening a case changes current applicability without rewriting old attempts.

Memory reads use one lock. PostgreSQL reads pin the receiver, current case and
all matching attempts in one read-only repeatable-read transaction. The reader
validates source snapshot/result/artifact bindings and retained profile/inventory
pins before deriving summaries. Corrupt or excessive history fails the complete
request rather than omitting an attempt. Detached output cannot mutate stored
history. The complete response has its own SHA-256 over compact Go JSON with
`sha256` set to the empty string; stable retained state produces the same hash.
The CLI checks metadata bindings, ordering, counts, profile pins and report hash.

Authority is `recorded_review_attempt_history_only`, with false qualification
credit. This is retained recorded-model review activity for an observation case;
it is not hypothesis validity, trial performance or evidence that a search was
exhaustive. Use publication links and the separate combined hypothesis/trial
history for research outcomes. Rejected commands that never created an attempt,
unregistered experiments and provider activity are outside this report. No
automatic dispatch, publication, trial, migration or operation is triggered.

## Transaction acknowledgments

PostgreSQL review starts and completions return a review view only after their
transaction commits successfully. Deferred constraints and cancellation at COMMIT
return an error and a zero view, including on exact retries. A confirmed rollback
preserves the previous review history and leaves a new request identity reusable.
A transport error may leave the commit outcome uncertain: inspect retained history
or retry the exact same request; do not infer rollback from a missing response.
Successful exact retries preserve the original attempt without reading a new clock.
These rules do not authenticate social/copy sources, issue research dispatch, grant
admission or create financial effects.
