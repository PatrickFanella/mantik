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

An [assessment-pinned inspection](paper-evaluation-assessments.md#inspecting-a-retained-source-cut)
resolves the exact records retained by a v5 assessment and returns bounded query,
revision and pagination metadata. It preserves the difference between reported
response custody and the acquisition journal's request/completion evidence.

## Owner-configured journal acquisition reads

`serve --corporate-source-journal /absolute/private/journal
--corporate-source-journal-id journal-EXPECTED` enables
`GET /v1/corporate-action-acquisitions/{collection-id}`. Both flags are required.
The journal must already exist, have the expected identity and satisfy the
journal's private ownership and permissions checks. The service holds its
exclusive journal lock until shutdown, so collectors and other journal owners
must finish and release that journal first. The reader does not start a
collector, open provider credentials or fetch a response.

The authenticated GET accepts only a collection ID. It returns the verified
[acquisition receipt](alpaca-corporate-action-snapshots.md#verified-journal-acquisition-receipts)
and exact original recording from that configured journal. The recording hash
and original response hash are bound by the receipt. Requests cannot supply a
path, clock, recording or acquisition assertion; query parameters and bodies
are rejected. The route is absent unless the server has a configured reader.
Missing collection evidence returns 404; an interrupted, failed or inconsistent
acquisition returns 409; other read failures return 503 without local path
information. Reads are serialized and revalidate the retained journal evidence.
Closing or reopening the reader does not change the source identity.

This is a read of a locally retained acquisition, including when its bytes were
originally observed by a separate collector. It does not attest provider origin,
complete coverage or execution by the current service process. The response's
coverage, economic-application, admission, execution and qualification flags
remain false. The existing caller-reported `POST /v1/corporate-action-sources`
contract is unchanged. The durable import described below uses this configured reader; direct
caller-reported sources remain a separate custody class.

## Durable import from the configured journal

When the journal reader is configured, authenticated
`POST /v1/corporate-action-imports` accepts only `id`, `receiver_id`,
`collection_id` and `acquisition_sha256`. The acquisition pin comes from the
configured reader's GET. The service obtains the recording and receipt from
that journal; a request cannot provide recording bytes, proof fields, balances,
admission or effects. The supplied receiver must match the durable source owner.

New imported records use `mantik-owned-corporate-action-source-v2`. They retain
the original recording, full bounded acquisition receipt and its hash under
`service_custody_of_configured_journal_acquisition_only`. The durable owner
validates the receipt's schema, method, identity, attempt clocks, digest and
exact original-recording binding. The journal owner verifies the referenced
acquisition artifacts when the import is created. Retained hashes are not a
provider signature or independent proof of remotely supplied artifacts; the
import path is restricted to the configured local reader.

The source append and shared source clock commit under one Memory lock or
PostgreSQL transaction, in the existing collector-then-receiver lock order.
Retention cannot precede acquisition completion or enter an issued assessment
cut. A failed clock update rolls back the source append too. Exact retries return
200 and the original receipt without sampling the clock or reopening the
journal, including after the reader has closed. A changed source identity,
collection, acquisition pin or original-reference alias conflicts. New imports
return 201. Failed, incomplete, missing or changed journal evidence cannot create
a source record. The existing targeted source GET provides durable reconciliation
after an uncertain HTTP response.

Schema 73 fences older writers without rewriting v1 source payloads. Existing
caller-reported receipts retain their original hashes and authority. The public
caller-reported writer cannot adopt an imported record by copying its command.
Assessment cuts bind the actual imported source record hash, and assessment
inspection identifies the acquisition hash and configured-journal custody.
Durable readback and exact retry can use the retained binding after a database
restore; a new import still requires the configured journal owner. The original
intent, attempt and completion artifacts remain in the journal; preserve its
verified archive separately. A database backup does not replace that journal
archive.

Neither terminal pagination nor successful import establishes complete or
historical provider coverage, action-free intervals or economic relevance.
Provider-attestation, coverage, economic-application, admission, execution and
qualification flags stay false. Causal page selection and economic benchmark
application remain separate acceptance work.

An [assessment-bound page-chain inspection](paper-evaluation-assessments.md#inspecting-causal-corporate-page-links)
can resolve causal cursor links from the imported acquisitions in an existing
assessment cut. It retains missing and ambiguous links and does not promote
pagination termination into complete coverage or economic authority.
