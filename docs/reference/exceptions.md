# Bounded retained-exception pages

`GET /v1/exceptions` and `mantik exceptions` return a page of persisted operational
exceptions without loading the full status snapshot or audit event history.
They use the existing bearer token and add no mutation, worker or schema change.

The response scope is `retained_exceptions`, with execution mode `paper_fixture`.
It is not a readiness assessment. Workers may not yet have persisted a new fault;
an empty page, or a page containing only resolved exceptions, does not prove
current readiness, collector liveness, strategy qualification or profitability.
Use the capability-scoped `readiness` command for a fresh assessment.

## API

| Query parameter | Default | Meaning |
| --- | --- | --- |
| `after` | Omitted | Exclusive exception identity cursor |
| `limit` | `25` | Records per page, from 1 through 100 |

The response contains `mode`, `scope`, `next_after`, `has_more`, and `items`.
Each item preserves the complete retained exception, including capability,
severity, status, code, evidence references, timestamps, bounded recovery
attempts and any terminal outcome. No narrative or new readiness fault is
created by the readback.

Records sort by identity using byte order, with matching PostgreSQL `C`
collation. If more records remain, request the next page with the returned
`next_after`; do not construct a cursor from a timestamp or increment an ID.
An empty page preserves the requested cursor and returns an empty array and
`has_more=false`. A valid cursor need not identify an existing record; it selects
records strictly after that identity.

Each page is one consistent current-state read, not a fixed immutable history.
Existing records can be updated and new IDs can sort before an earlier cursor.
To discover these changes, begin a new traversal without `after`. Repeating a
cursor is read-only but can return updated records. Cursors belong to the same
service/database history; they do not establish identity across restored forks.
An exception's recorded timestamps are not the time of the operator's readback.

PostgreSQL selects at most `limit + 1` exception records from the aggregate's
exception map; the extra row determines `has_more` and is not returned or decoded
as an exception. It does not query the event ledger or return unrelated state.
The JSON exception map still requires key enumeration/sorting inside PostgreSQL;
this change does not add an exception index or promise constant read cost as that
map grows. Memory storage selects ordered keys under its lock and copies only
the requested records. Persisted records are not pruned by this endpoint.

Malformed, unknown, repeated or empty query parameters return HTTP 400. Cursor
identities accept 1..128 ASCII letters, digits, underscores or hyphens. Invalid
limits or cursors return HTTP 400, missing authentication HTTP 401 and storage
failure HTTP 503. Requests have the existing five-second server context bound.

## CLI

```sh
./bin/mantik exceptions --limit 10
./bin/mantik exceptions --limit 10 --format json
./bin/mantik exceptions --after EXCEPTION_ID --limit 10 --format json
```

Use an actual returned `next_after` value instead of the placeholder. Supply
`--api-url` and `MANTIK_API_TOKEN` as for other service commands. The table labels
these as retained records, quotes stored text and reports the cursor/more flag.
JSON preserves the full structured exception and recovery history. The CLI
performs one bounded GET, with no cached display, automatic retry or recovery
request. Its existing response-size and network timeout bounds also apply.

## Interactive exception navigation

```sh
./bin/mantik supervise --view exceptions --limit 10
```

Use short commands followed by Enter:

| Command | Behavior |
| --- | --- |
| `n` | Fetch the next page after the returned exception identity |
| `r` | Repeat the current request with the same cursor |
| `b` | Begin again without a cursor to discover updated or earlier-sorting records |
| `d EXCEPTION_ID` | Print complete retained details from the displayed page |
| `q` or EOF | Exit without changing the service |

The viewer keeps one page and its pending request, with the same 1..100 page
limit and response-size bound as the standalone command. Details include the
retained evidence references, recovery attempts and terminal outcome; they do
not issue another request or invoke recovery. Off-page identities and unknown
commands also send no request. There is no automatic polling or retained page
stack.

Successful requests are labeled as retained on-demand readbacks, not current
readiness. A failed or invalid page marks the last successful page `STALE` and
blocks navigation/details until retry or begin-again succeeds. A failed next-page
request retains its pending cursor for exact retry. Because these are mutable
current-state pages, repeating a request can return updated records; it does not
promise an immutable replay. At the end, begin again to discover changes before
an earlier cursor.

The viewer rejects wrong scope/mode, excessive item counts, unordered or duplicate
identities and inconsistent continuation cursors. An empty valid page still
carries the retained-only label. An initial request failure permits retry,
begin-again, quit and EOF. The shared owned Unix stdin handle, bounded command
input and cancellation behavior match the [activity viewer](activity.md).
Terminal exit leaves workers and service state unchanged.

This implements read-only exception navigation for issue #22. The existing `operations` command/API still
combines all retained exceptions with fresh readiness and internally uses the
full snapshot; this endpoint does not silently change that contract. Recovery
continues through the explicit shared `recover` operation described in
[operational contracts](operations.md).
