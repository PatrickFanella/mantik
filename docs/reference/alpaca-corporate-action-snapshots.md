# Retained corporate-action REST snapshots

The existing `alpaca-collect` command accepts `capability: corporate_actions`.
It uses the same bounded GET client, durable attempt receipts, private capture
journal, identity retry rules and archive/restore path as quote collection.
It performs one page request; a returned continuation token does not start another
read. An explicit continuation spec retains its request token and the full response.
No collector, session or provider request starts with the service.

## Query and source contract

Contract `alpaca-us-rest-corporate-action-snapshot-v1` requests
`GET https://data.alpaca.markets/v1/corporate-actions` with:

- 1–10 unique, bounded symbols;
- explicit inclusive `start` and `end`, at most 31 dates;
- `region=us`, `data_quality=all`, `sort=asc`, and `limit=1000`;
- all action types, with no caller-selected type filter;
- an optional bounded `page_token` for one explicit continuation request.

The date interval selects **process dates**, not ex-dates, payment dates or
announcement times. The current [official REST reference](https://docs.alpaca.markets/us/reference/corporateactions-1)
documents these query semantics and permits incomplete records with
`data_quality=all`. It also warns that source receipt and processing can be
delayed. A terminal page therefore does not establish complete coverage or
prove what was known at an earlier decision time.

The implementation was reviewed against the official Market Data OpenAPI
snapshot with SHA-256
`2cd9b000441f24075cc3110580c25747fabd7799caefb0d4dc3c7c69832a4eb0`.
The reviewed public snapshot and research receipts remain outside Git.

An existing collection spec can use this shape:

```json
{
  "id": "corporate-source-001",
  "capability": "corporate_actions",
  "feed": "iex",
  "symbols": ["SPY"],
  "start": "2026-10-14",
  "end": "2026-10-14",
  "access_ref": "operator-selected-market-data-access",
  "max_quote_age_seconds": 60
}
```

`feed` remains the existing client's explicit supported quote-feed setting;
corporate-action requests do not send it to the provider. Retained corporate
recordings instead use `feed: us_rest_all_snapshot_v1` to identify their REST
query contract. The inherited maximum quote-age field remains required by the
recording envelope; it supplies no freshness claim for corporate actions.
Use the existing explicit credential-file and journal arguments for collection.
Reusing a completed collection ID verifies its original receipt without fetching;
a new retrieval needs a new identity. For a continuation, use another collection
ID with the same query scope and `page_token` copied exactly from the prior
page's retained `next_page_token`. The optional token is limited to 4,096 bytes,
valid UTF-8 and no control characters; URL encoding preserves its value. Quotes
and calendars reject nonempty tokens.

Failed and interrupted identities retain
their attempts and cannot be silently resumed.

## Retention and inspection

The complete response remains byte-for-byte in the recording's `original`
string with its SHA-256. The optional `corporate_actions` normalization output
adds query scope, original receipt time, evidence class, access reference and
source hash, plus typed records containing the provider ID, group name and each
original JSON object as a string with its own digest. Rates retain their source
precision; no dividend rounding, split conversion or symbol mapping is applied.
Unknown action groups and incomplete objects remain inspectable. A bounded
nonempty source ID is required; duplicate IDs or JSON object keys are rejected.
The existing failed collection path preserves rejected response bytes.

`next_page_token` is retained. A nonempty token gives
`page_state: more_pages_unavailable`; null or an empty token gives
`page_state: terminal_page_only`. Neither means complete coverage. The request token is retained separately from
the response token; a response cannot silently change the recorded request. The existing
1 MiB response bound and the 1,000-record page limit apply without truncation.

`Journal.CorporateActionSnapshots(asOf)` replays retained capture contracts and
returns every available snapshot at an explicit cut. Later captures do not
replace earlier records, prove deletion, or rewrite historical views. For
network captures, availability is the later of response receipt and terminal
collection completion; a capture without completion is excluded. Network snapshot reads also expose `requested_at` from the retained acquisition
intent. Manual
recordings retain their supplied receipt and unqualified evidence class, which
are attribution rather than independent authentication. The existing
`alpaca-normalize` command also exposes the typed projection for a recording.

The existing service feed-evidence publication owner explicitly accepts only
quotes and calendars; it rejects these source-only snapshots without changing
its retained evidence or collector clock.

The projection emits no routing input, session schedule or market price.
`coverage_complete`, `historical_knowledge_proven` and
`economic_application_ready` stay false. The normalization output continues to
list corporate-action economic handling as unsupported. Archive/restore checks
rederive projections from original bytes and reject altered projections even
when their enclosing digest has been recomputed.

## Historical page-chain inspection

`Journal.CorporateActionPageChain(captureID, revision, asOf)` starts from an
explicit retained first page at a historical cut. Method
`retained-corporate-action-cursor-chain-v1` links a page only when its request
token matches the prior response token, its query scope and source attribution
match the root, and its acquisition intent follows the prior page's availability.
Every linked page must have completed journal acquisition. Manual recordings
remain inspectable snapshots but cannot prove those acquisition links.

The result retains each page, raw-source hash, request cursor, response cursor,
receipt and completion availability. A later retrieval does not rewrite an old
page. Multiple eligible acquisitions for the same cursor produce
`ambiguous_next_page` with their capture references; the read does not choose the
latest. Missing pages, interrupted acquisitions, requests made before their
predecessor was available, cycles and overlapping source IDs stop the chain with
explicit statuses. A chain is bounded to 32 pages and reports
`page_bound_reached` when that bound prevents following another cursor. The
returned pages are the inspected prefix, not a silently truncated full result.

A terminal null/empty response cursor gives `page_chain_terminated` and
`pagination_terminated: true` only when the inspected chain has no earlier
link failure. This describes retained pagination, not a provider-atomic snapshot,
complete corporate-action coverage or knowledge before acquisition. Provider
records can change while pages are being read. The read never merges repeated
source IDs into an economic event history, infers deletions, or applies rates.
`coverage_complete`, `provider_atomicity_proven` and
`economic_application_ready` stay false. Quotes, calendars and service
publication retain their existing capability boundaries.

## Remaining economic work

These snapshots supply source custody, not a total-return benchmark. Provider coverage policy, point-in-time revision selection, instrument
identity changes, entitlement dates, dividend precision, split fractions and
cash-in-lieu rules still need versioned economic contracts. The passive
benchmark, applicable admission, durable allocation/effects and natural
qualification remain separate acceptance work. Empty or incomplete source
pages cannot authorize a transaction or establish an action-free interval.

Verification uses manufactured HTTP responses through the existing collection
and CLI paths, historical cut, revision and continuation-link tests, failed/interrupted capture
checks, and actual private journal archive/restore. It does not qualify provider
availability or corporate-action coverage.

## Verified journal acquisition receipts

`Journal.CorporateActionAcquisition(collectionID)` derives
`mantik-journal-corporate-acquisition-v1` from an actual retained corporate-action
network collection. It verifies the collection and capture, then binds the exact
intent, completion, capture, recording and original-response hashes. Every
bounded attempt contributes its start/response artifact hashes, clocks, terminal
state, HTTP status and partial-response flag. Failed attempts remain in the
receipt when a later attempt succeeds.

Method `retained-intent-attempts-capture-completion-v1` requires the request
intent to precede the first attempt, each response to follow its start, each
later attempt to follow the prior response, and completion to follow the last
response. Equal clocks are allowed. Impossible clock ordering fails even when
artifact envelopes have been rehashed. The receipt's `available_at` is the
verified completion clock, which cannot precede the original response.
`CorporateActionSnapshots` uses this verification for network captures before
exposing their request and availability clocks; manual captures retain no
acquisition clock. Missing, failed or unfinished collections cannot produce a
successful acquisition receipt. Readback never retries or resumes acquisition.

The receipt contains bounded metadata and references, not raw attempt bodies or
credentials. Existing raw journal artifacts and capture bytes remain unchanged.
Archive/restore preserves the receipt's identity and hashes. The authority is
`journal_owned_recorded_acquisition_only`: provider attestation, coverage,
economic application, admission, execution and qualification remain false.
This journal receipt does not by itself extend the service's caller-reported
source API or attest a remotely supplied receipt. Service-owned acquisition
binding remains a separate acceptance dependency.
