# Retained recorded extraction attempts

The service retains a source-only article and extraction intent before accepting
one recorded extractor response. It owns the contract, clock, checks and result.
Clients supply the article and event key at start, then exact artifact bytes at
completion. They cannot submit finalized results or change the captured source.

```sh
./bin/mantik news-extraction-start --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id extraction-1 \
  --article /absolute/private/article.json --event-key declared-event
./bin/mantik news-extraction-complete --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id extraction-1 \
  --model-fixture /absolute/private/extractor-response.bin
./bin/mantik news-extraction --api-url "$MANTIK_API_URL" --id extraction-1
./bin/mantik news-extractions --api-url "$MANTIK_API_URL"
```

Use the existing `MANTIK_API_TOKEN` bearer credential. The article uses
[`mantik-news-article-v1`](news-extraction.md); the recorded response is one raw
artifact, not an array or completed extraction report. Completion permits invalid
JSON and UTF-8 as retained evidence. A file exceeding 128 KiB is rejected before
submission. No model provider or background extraction worker is activated.

## Start, completion and retries

The `mantik-news-extraction-attempt-v1` receipt pins receiver and request ID,
mode `recorded_model_fixture`, the complete compiled extraction contract and
hash, canonical article and hash, operator event key, server start time and
30-second deadline. Source receipt time cannot follow the start time. Each
attempt has a hash over compact Go JSON with its own `sha256` empty.

Start commits before any completion evaluation. A pending attempt remains
discoverable after interruption, lost response or application restart. Repeating
the same start returns its original pending or terminal receipt without reading
the clock or capturing new metadata. Rebinding the ID to another source/event
key conflicts. Different IDs retain distinct attempts even when their source and
artifact bytes are identical. These records do not prove when a caller produced
the recorded model artifact or that no earlier experiment happened elsewhere.

Completion loads and validates the retained intent, computes the bounded
recorded extraction, then reacquires the mutation lock and checks the source,
contract, result, receiver and clock again. It atomically stores the exact raw
artifact capture, checked result, completion time and attempt hash. Model-output
errors produce retained abstentions; a persistence failure leaves the intent
pending with its original source and deadline.

| Status | Meaning |
| --- | --- |
| `pending` | Intent retained; no completion committed |
| `completed` | Result committed before the original deadline, including checked abstentions |
| `expired` | Result committed at or after the deadline; raw output and checks retained |

An expired result may contain structurally valid extracted claims. Expiry is a
separate processing disposition and does not erase that output. Reads never
expire pending work or sample a clock. A completion clock earlier than start
conflicts. Exact terminal retries compare the complete raw bytes and return the
original receipt without resampling time; changed whitespace, malformed bytes
or other content under the same ID conflicts. There is no automatic retry or
timeout sweep.

Both stores revalidate retained source, compiled contract, artifact capture,
checks, result and receipt hashes on read. Recorded completion validation
recomputes the result from its captured bytes; resealing a forged check or
qualification flag is insufficient. Unknown stored payload fields are rejected.
This is interpretation of bounded local bytes, with no external model callback
under the storage lock. Context cancellation before commit can leave pending
work; inspect the stable ID before deciding whether to retry.

## Complete bounded history

A receiver retains at most 32 extraction attempts across all statuses. Capacity
does not evict failures or pending work. Exact start retries work at capacity.
The history endpoint returns every retained summary in start-time/ID order,
including pending, abstaining and expired attempts. It offers no outcome filter,
pagination, ranking or selected winner. Corrupt or excessive history fails the
whole request without a partial report.

`mantik-news-extraction-history-v1` contains receiver identity, attempt count,
status counts, source/article/revision and event identities, timestamps,
outcome/reason codes, and targeted attempt/result/source hashes. Original bodies,
claim excerpts and raw artifacts are available through targeted inspection, not
copied into the history. The history has its own hash; identical retained state
produces identical reports. The CLI validates compact metadata and report hash;
targeted inspection also recomputes the full recorded result.

Memory reads hold one mutex and return detached data. PostgreSQL history reads
use one read-only repeatable-read transaction. Writes serialize on the research
receiver row, which also makes the global capacity check atomic. The extraction
ledger does not mutate news cases, hypothesis publications or fixture state.

## HTTP, persistence and recovery

| Method and path | Input |
| --- | --- |
| `POST /v1/news-extractions` | `receiver_id`, `request_id`, `article`, `event_key` |
| `POST /v1/news-extractions/ID/complete` | `receiver_id`, `artifact_base64` |
| `GET /v1/news-extractions/ID` | No query parameters |
| `GET /v1/news-extractions` | Complete bounded history; no query parameters |

All endpoints use bearer authentication. Start envelopes are bounded to 2 MiB
plus 4 KiB; completion envelopes to 180 KiB. Base64 must be canonical and decode
to at most 128 KiB; empty bytes are a valid malformed-artifact completion.
Unknown/duplicate/noncanonical JSON fields or invalid envelopes return 400.
Invalid bounded commands or retained evidence return 422, missing IDs 404,
identity/capacity/clock conflicts 409, and unavailable persistence 503. A retained
extractor abstention is a successful completion response, not an HTTP failure.

Migration 21 adds `mantik_news_extractions` without rewriting older research or
operational payloads. Schema-20 binaries refuse schema 21. Deployment requires a
verified backup and planned restore rollback that preserves post-upgrade
evidence; do not downgrade by deleting migration records or the new table.
Development rehearsals use disposable databases and do not upgrade original
pilot services. Restart and isolated same-host restore evidence remains distinct
from off-host disaster recovery and deployment proof.

Authority remains `claim_candidates_only`; history authority is
`recorded_extraction_history_only`, with false qualification credit. These are
retained recorded-model attempts, not verified source truth or extractor quality.
Predecessor references retain their supplied shape but this ledger does not
verify their existence or infer a current correction chain. Source admission,
reviewed conversion of candidates with exact provenance, event equivalence,
source independence and measured extractor behavior remain separate work in
issue 12. No extraction receipt closes a news case, admits a recording, publishes
a hypothesis, grants execution authority or creates an economic effect.
