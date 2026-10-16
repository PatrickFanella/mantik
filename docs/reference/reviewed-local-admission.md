# Reviewed local-capture admission

An operator can explicitly approve an eligible service-retained local capture
for news research. The service loads the immutable capture, replays all local
checks and takes the original source, event key and complete checked claims
from that evidence. The command cannot replace source metadata, claims,
provider/model configuration or evaluation checks.

This path is separate from recorded-fixture approval. A local capture remains
`caller_reported_local_capture` with false execution attestation and qualification
credit. Approval adds reviewed research provenance; it does not attest a model
ran, establish source truth or accuracy, qualify a strategy, or authorize trading.

## Review and approve

First inspect the full capture and its original source/response bytes through
`news-local-capture`, and review every candidate claim. Pin the intended
receiver, capture hash and finished result hash explicitly:

```sh
./bin/mantik news-local-admit \
  --api-url http://127.0.0.1:40880 --receiver-id RECEIVER \
  --id local-capture-1 --capture-sha CAPTURE_SHA --result-sha RESULT_SHA \
  --approve-candidates > /private/local-approval.json

./bin/mantik news-local-admission \
  --api-url http://127.0.0.1:40880 --id local-capture-1
```

The CLI refuses the write without `--approve-candidates`. The HTTP command uses
approval code `approve_local_claim_candidates_v1`. Both paths require exact
receiver, capture ID/hash and result hash. The CLI validates the returned receipt
against targeted capture and news-record reads before printing it; metadata
hash integrity alone is insufficient.

An eligible capture has a consistent finished receipt, disposition
`response_complete`, extracted claims and successful quote derivation with the
original model content retained. Prepared-only unknown completion, abstention,
transport/HTTP failure, truncation, rejected quotes and inconsistent captures
cannot be approved. The local capture has no service-owned generation deadline:
its reported model times remain caller evidence. Approval must be at or after
its server `imported_at` and satisfy the existing causal news/source clock and
revision rules. A reported future model finish time does not become independent
clock authority.

## Atomic provenance and retries

Schema `mantik-reviewed-local-capture-admission-v1` binds its exact approval
command to a news receipt and `mantik-reviewed-local-capture-provenance-v1`.
The recording and normalized result use extraction method
`reviewed_local_capture_v1` and a separate `local_extraction` field. Existing
`extraction` provenance remains exclusively for recorded fixtures; both fields
cannot be present together. Old operator/recorded wire bytes omit the new field.

Local provenance retains receiver and capture identity/hash, prepared/finished
hashes, source article and result hashes, original model and derived candidate
artifact hashes, contract hash and prompt version. Authority remains
`caller_reported_local_capture_only`, with false execution attestation and
qualification credit. The approval receipt's authority is
`reviewed_caller_reported_local_claims_only`, also with both flags false.

The news record/case changes and one approval receipt per capture commit in the
same transaction. An exact retry returns the original receipt without reading a
clock, rewriting rows, reopening a closed case or advancing its version. Changed
approval pins conflict. The capture itself is not modified. A second capture
for an already admitted source/article/class/revision conflicts; an untracked
existing news record cannot be adopted as an approval receipt.

Corrections and retractions use existing predecessor hash, source identity and
causal checks, and revisit affected predecessor events. Closing a case remains
explicit. Admitted local evidence can enter ordinary recorded research review;
that review grants no additional source-truth, qualification or trading authority.

Generic news submission and journal retention reject caller-supplied local
provenance. `news-extraction-admit` cannot approve a local capture. Approval is
available only through the separate local endpoint and its retained evidence
lookup, not a provenance object fabricated by a caller.

## HTTP and persistence

| Endpoint | Body or response |
| --- | --- |
| `POST /v1/news-local-captures/ID/admit` | `receiver_id`, `capture_sha256`, `result_sha256`, `approval_code` |
| `GET /v1/news-local-captures/ID/admission` | Checked approval receipt |

Both routes require bearer authentication and reject query parameters. The
bounded 4 KiB command rejects unknown, duplicate and mis-cased fields. It cannot
supply article text, alternate claims, an inspection or a result. Wrong receiver
and conflicting/ineligible approval return HTTP 409; malformed commands return
400 or 422; missing captures/approvals return 404. Receipt reads replay the full
retained capture and verify the corresponding news record and normalized result.
A corrupt link fails the read instead of yielding a partial approval.

Schema 24 adds `mantik_local_capture_admissions`, foreign-keyed to the existing
local capture table. Capture capacity also bounds local approvals to 32. The
migration does not rewrite existing captures, recorded attempts/admissions or
news records. Its version fence prevents older service binaries from writing the
new provenance shape. Before upgrading, retain a verified full schema-23 backup;
rollback means the earlier binary plus that backup on separate resources.
Do not delete version rows or local approvals to make an older binary start.

Back up captures, approvals, news state, receiver identity and schema ledger
together. Verify targeted capture/admission/news readbacks after restart and an
isolated restore. Operational state and economic effects are outside this
research admission path. No provider request or automatic model retry is made.
