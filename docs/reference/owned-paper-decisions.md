# Service-owned paper proposals and abstentions

The service finalizes a research proposal from its own retained native strategy
and price/calendar history. It does not accept a caller's decision, account,
balance, quote, evaluation, admission receipt or execution effect. This is an
issue #19 dependency; admission, account allocation and execution remain separate
unfinished owners. A finalized `entry_candidate` is not an executable decision.

## Selection and evaluation

`POST /v1/paper-decisions` takes only an immutable request `id`, expected
`receiver_id`, `strategy_id`, full `strategy_sha256` record pin, and
`registration_id`. The authenticated service selects the actual records and its
clock under storage locks. Requests are limited to 4,096 bytes and reject unknown
fields, duplicate or differently cased names, trailing objects and query strings.

The selected collector must match the strategy's symbol, feed and intended input
class. Evaluation uses the existing server coverage calculation: raw captures,
normalized retained inputs and their admission/publication receipts must agree.
Newer unusable or unpublished evidence suppresses an older usable quote. The
collector lease must be current, regular session open and quote eligible. Existing
coverage limits remain 60 seconds for quote age, 300 seconds for capture age and
86,400 seconds for calendar age. A stricter strategy quote-age limit also applies.
A quote above the native threshold or at/below the safety stop abstains. A usable
quote within the threshold and above the stop yields an `entry_candidate` with
`native_threshold_met_admission_required`.

Finalized records retain the complete selection command, server time, evaluation,
coverage, evidence-basis hash and complete record hash. Authority is
`service_finalized_research_proposal_only`. Admission availability, execution
availability and qualification credit remain false for every result. A hash or
an entry candidate cannot substitute for the future admission/allocation gates.
Prospective input labels and fixture tests do not establish profitability or
qualification.

## Retention and retries

The private retained basis copies the selected strategy, collector entry, relevant
published raw captures, admitted inputs and immutable receipts at finalization.
Reads replay the evaluator at the original time and check source continuity
against actual retained owner records. Historical lease receipts remain available
for that check. Later inputs or lease renewal/stop do not rewrite the earlier
basis; a new request ID evaluates the new state. Source corruption, missing owner
records or a changed result prevents a successful catalogue read or retry.

Exact command retries return the original record without reading a new clock.
Changed content under the same ID conflicts. The full retained prefix is checked
before new writes or retries. There are at most 32 records, each with a maximum
2 MiB retained envelope; capacity errors preserve existing history. No eviction,
replacement or deletion route exists. This bounded research registry must not be
used as an operational ledger or protective-exit queue.

## API and terminal client

New finalizations return 201, exact retries 200, invalid commands 422, identity or
capacity conflicts 409, missing selected records 404 and storage failures 503.
`GET /v1/paper-decisions` and `GET /v1/paper-decisions/{id}` replay retained results.
The client verifies strict response envelopes, command/receiver identity and
selected record pins before emitting output. Public envelope validation alone
cannot establish ownership of the private basis.

```sh
./bin/mantik paper-decision-finalize --api-url "$api" \
  --receiver-id "$receiver" --id proposal-001 \
  --strategy-id baseline-v1 --strategy-sha256 "$strategy_record_sha256" \
  --registration-id spy-iex
./bin/mantik paper-decisions --api-url "$api" --receiver-id "$receiver"
./bin/mantik paper-decision --api-url "$api" --receiver-id "$receiver" \
  --id proposal-001 --record-sha256 "$decision_sha256"
```

## Storage and rollback

Memory serializes selection, evaluation and retention under its aggregate mutex.
PostgreSQL schema 38 adds `mantik_paper_decisions`; writes lock the receiver,
collector and state in that order before selecting inputs and retaining a result
atomically. Catalogue reads use a repeatable-read transaction. The bounded JSON
envelope uses `bytea` so JSONB field reordering cannot rewrite the captured basis
hash. Failed inserts roll back without changing economic or research input state.

The migration preserves schema-37 state. Older binaries refuse the new schema.
Rollback uses a compatible private backup restored into separate resources with
its matching binary. Dropping tables in a running service is not a rollback.
Local tests, hosted CI, deployment, runtime and qualification are distinct checks.
