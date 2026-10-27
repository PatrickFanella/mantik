# Corporate-action source custody

The service can retain original corporate-action recordings separately from
quote/calendar publication. This is a dependency for later economic research,
not a corporate-action accounting or passive-benchmark implementation.

`POST /v1/corporate-action-sources` accepts an authenticated command with `id`,
the actual `receiver_id`, and an existing Alpaca `recording` object. Only
`corporate_actions` recordings under `us_rest_all_snapshot_v1` are accepted.
The existing normalizer validates the query contract, original response hash,
JSON shape, source identities and page bounds. It does not interpret dates,
amounts, entitlements or adjustments economically. No provider request occurs.
Original and access references must be nonempty bounded UTF-8 without control
characters; they remain reported provenance labels, not provider attestations.

The service retains the complete command, including the exact original response
string, its hash, query symbols/dates/cursor, evidence class and reported receipt
time. Schema `mantik-owned-corporate-action-source-v1` adds a service `retained_at`,
sequence number, predecessor hash and full record hash. The journal is bounded to
32 records; commands are bounded to twice the one-MiB response limit plus 32 KiB.
Oversized commands fail rather than truncating source evidence.

An exact command retry returns the original record with `duplicate:true` without
consulting a new clock. Reusing an ID for different content or retaining the same
original reference under another ID conflicts. New records require a valid service
time at or after the reported receipt and the journal's last retention time.
Distinct revisions keep their original bytes and separate retention receipts;
later revisions never rewrite earlier pages or imply deletion of earlier actions.

`GET /v1/corporate-action-sources` returns bounded discovery references with the
receiver, source and record hashes, sequence and service receipt time.
`GET /v1/corporate-action-sources/{id}` returns one complete original record.
Reads are clock-free. Neither route accepts an as-of cutoff or caller-selected
path. The service replays the complete journal before returning a record, so an
invalid predecessor, altered payload or self-rehashed authority grant fails.

Memory appends serialize under the existing owner mutex. PostgreSQL appends lock
the actual receiver row and commit the record atomically; reads use one repeatable
snapshot. Schema 71 adds an independent table. It does not alter retained quote,
calendar, decision, assessment, social or copy payloads.

The authority is
`service_custody_of_caller_reported_corporate_action_source_only`.
`provider_attested`, `coverage_complete`, `economic_application_ready`,
`admission_available`, `execution_available` and `qualification_credit` remain
false. Authentication identifies the submitting service client; it does not
authenticate a provider response or prove network acquisition. The journal does
not accept a balance, admission, effect or caller-supplied coverage assertion.

The reported `recording.received_at` is not the service's knowledge time.
`retained_at` describes this custody operation. Under schema 72, new source
writes also serialize against the shared source clock and retained decision,
outcome and assessment cuts; they must be strictly later than those receipts.
Exact retries keep their original timestamps and payloads. Existing source
record schemas and bytes are unchanged.

New [v5 assessments](paper-evaluation-assessments.md#corporate-source-custody-cuts)
retain the actual custody prefix at issuance. This is not economic source
selection or provider historical knowledge. A future economic owner must still
bind relevance, acquisition provenance, causal market cuts and a declared method;
it cannot substitute a later page for earlier knowledge. A terminal cursor does
not prove provider-atomic or complete action coverage. Acquisition-intent custody,
service-owned page-chain selection, instrument and entitlement rules,
revision/deletion handling, rounding, fractional shares, cash-in-lieu,
total-return benchmarks and economic effects remain separate work.

The [acquisition journal](alpaca-corporate-action-snapshots.md) preserves the earlier
source collection contract. Corporate pages remain rejected by
`POST /v1/feed-evidence`; this new custody journal grants no routing, readiness,
allocation or execution authority.

Tests use manufactured source bytes with actual Memory and disposable PostgreSQL
owners. They cover immutable retries, concurrent append, original readback,
capacity, corruption, receiver/clock validation, restart, migration rollback and
checksum rejection. The existing HTTP finalization harness exercises these routes
against both backends without changing operational state. Recovery seeds and
replays an original page in the actual backup/restore flow and rejects a
self-rehashed coverage grant before restoring the original payload.
