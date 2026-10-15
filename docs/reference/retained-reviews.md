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
| `POST /v1/news-reviews` | `receiver_id`, `request_id`, `case_id`, `version` |
| `POST /v1/news-reviews/ID/complete` | `receiver_id`, `steps` array |
| `GET /v1/news-reviews/ID` | No query parameters |

All routes use the existing bearer authentication boundary. Successful writes
and inspection return the retained attempt and live applicability flags. Invalid
command envelopes return 400, invalid bounded commands 422, missing attempts 404,
receiver/identity/evidence/capacity conflicts 409, and persistence failures 503.
Structured interpreter abstentions are retained successful completions, not HTTP
errors. Provider clients, automatic dispatch, leases, hypothesis publication and
additional decision types remain separate units.
