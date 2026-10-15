# Bounded activity readback

`GET /v1/activity` and `mantik activity` expose pages of retained audit events
for terminal supervision. They are read-only and use the same bearer token as
other service operations. The execution mode remains `paper_fixture`; reading
real research evidence does not qualify a strategy or grant execution authority.

## API contract

Query parameters are optional:

| Parameter | Default | Meaning |
| --- | --- | --- |
| `after` | `0` | Exclusive nonnegative event-sequence cursor |
| `through` | Latest committed sequence | Inclusive fixed history boundary |
| `limit` | `25` | Integer from 1 through 100 |

The response contains `mode`, `through`, `next_after`, `has_more`, and `items`.
Each item carries its append-order `sequence` and complete structured `event`,
including any retained decision, safety conditions, outcome, or evidence
references. This endpoint covers the audit event ledger; research-routing input
history and raw provider captures retain their separate inspection contracts.
It does not promise that every research input produces an audit event.

The server reads the boundary and page in one consistent database transaction.
PostgreSQL uses the existing sequence index and fetches at most `limit + 1`
event rows, without loading the aggregate or its entire event history. The extra
row determines `has_more` and is not returned. Memory storage applies the same
contract under its existing lock. No schema change or new background worker is
required.

Sequences describe append order, not event occurrence time. They can have gaps
from rolled-back transactions; do not add one to a cursor or infer an event
count from the boundary. Current event writers serialize their mutations through
the aggregate lock before allocating sequences. Direct database edits and
history deletion are outside this readback contract.

For an empty history, `through` and `next_after` are zero, `items` is an empty
array, and `has_more` is false. An explicit `through=0` continues to select that
empty prefix even if new events have since arrived. A page with no items retains
its requested `after` as `next_after`.

Malformed, unknown, repeated, or empty query parameters return HTTP 400. So do
limits outside 1..100, negative cursors, `after > through`, or a boundary beyond
the currently retained history. Authentication failures return HTTP 401;
storage failure returns HTTP 503.

## Terminal journey

Start with a bounded page:

```sh
./bin/mantik activity --api-url http://127.0.0.1:40880 --limit 10
./bin/mantik activity --api-url http://127.0.0.1:40880 --limit 10 --format json
```

The table shows the execution mode, history boundary, next cursor, more flag,
and each event's sequence, time, kind and identity. It quotes event text so that
embedded terminal controls cannot change the display. JSON preserves the full
structured event for targeted inspection.

If the returned boundary is `42`, the next cursor is `10`, and more events remain:

```sh
./bin/mantik activity --after 10 --through 42 --limit 10 --format json
```

Use the actual returned values. Keep `through` fixed while paging this prefix,
and continue from each returned `next_after` until `has_more` is false. New
events are excluded from that readback. To discover events appended afterward,
retain the last cursor and omit the fixed boundary:

```sh
./bin/mantik activity --after 42 --limit 10 --format json
```

A failed request is not a fresh page. Keep the last successful cursor and retry
against the same service and retained history; there is no automatic polling,
reconnect, or cached-success display. These cursors are local to that database
history, not portable identities across unrelated instances or restored forks.
After switching service or restoring a different history, start a new readback
and verify its source. Disconnecting the CLI does not stop service workers or
acquire a collector lease.

## Interactive activity supervision

On Unix, open the read-only activity viewer with:

```sh
./bin/mantik supervise --api-url http://127.0.0.1:40880 --limit 10
```

Set `MANTIK_API_TOKEN` as for other CLI operations. The viewer makes one bounded
GET at startup and then waits for a command followed by Enter. It uses ordinary
terminal input, without an alternate screen, raw terminal mode or automatic
polling. The page limit remains fixed for the session.

| Command | Behavior |
| --- | --- |
| `n` | Next page within the current fixed boundary |
| `r` | Retry the current request with the same cursor and boundary |
| `b` | Begin a new readback from the start, selecting the latest boundary |
| `u` | At the end of a fixed history, fetch newly appended events |
| `d SEQUENCE` | Print full JSON details for an event on the displayed page |
| `q` or EOF | Exit the viewer |

Only one page and the current request are kept in memory. Event details come
from that retained page and do not fetch another history or generate a narrative.
Unknown commands and out-of-page event identities send no request. Appended
history is unavailable until the fixed history has been traversed or a new
readback begun; it does not silently skip unread events.

Successful requests are labeled `READBACK OK`, explicitly as an on-demand
readback rather than a continuous health check. A failed request labels the
last successful page `STALE`; next-page, appended-history and detail commands
are disabled until a retry or a new readback succeeds. Retrying a failed next
page repeats its pending cursor, without advancing from the stale display.
An initial connection failure leaves no successful page but still accepts
retry, begin-again and quit commands. The error message does not echo arbitrary
remote response text or authentication details.

The viewer owns a separate `/dev/stdin` handle. Cancellation closes that handle
to unblock input; quitting does not close the calling process's original stdin
handle or send a service mutation. Ctrl-C exits with cancellation. Command input
is bounded to 255 bytes per line; an oversized command ends the viewer. EOF
exits even while disconnected. The service and independent workers continue
after the terminal exits. Reconnecting starts a new viewer readback; for exact
cursor continuation across processes, use the explicit `activity` flags above.

This implements activity navigation for issue #22. [Case navigation](cases.md)
is available with `supervise --view cases`.
[Exception navigation](exceptions.md) is available with `supervise --view exceptions`.
Library/portfolio screens and mutations through shared operations remain separate
implementation units. `status --format json`
continues to return the existing full snapshot; it is not a paginated substitute.
