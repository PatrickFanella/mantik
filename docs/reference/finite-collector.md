# Finite leased Alpaca quote collector

`alpaca-run-collector` combines a declared finite quote session, collector
ownership, and fenced journal delivery. It acquires an existing registration's
lease, renews ownership before each acquisition and delivery, delivers each
retained input, and records a bounded stop at the end. It starts no persistent
scheduler or service worker.

## Prepare and run

Initialize an empty private journal explicitly:

```bash
./bin/mantik alpaca-journal --journal "$MANTIK_CAPTURE_JOURNAL" --init
```

The result contains `journal_id`. Inspection without `--init` still requires
an existing journal and never recreates a missing one. Register that journal
through the [collector API](feed-collectors.md), with the exact receiver, feed,
symbol, prospective evidence class, opaque access receipt reference, and
`lease_seconds: 300`. This command uses an existing registration; it does not
grant provider access or replace a registration.

Create a finite session spec:

```json
{
  "id": "iex-spy-run-001",
  "count": 2,
  "interval_seconds": 5,
  "collection": {
    "id": "",
    "capability": "latest_quotes",
    "feed": "iex",
    "symbols": ["SPY"],
    "access_ref": "alpaca-iex-access-receipt",
    "max_quote_age_seconds": 60
  }
}
```

Use the existing private credential-file format from
[bounded acquisition](alpaca-journal.md#one-bounded-acquisition): a current-user
regular file with mode 0600 containing only `key` and `secret`. Do not put
credentials in the spec, API registration, terminal arguments, or Git.

```bash
./bin/mantik alpaca-run-collector --journal "$MANTIK_CAPTURE_JOURNAL" \
  --spec /private/path/session.json \
  --credentials-file /private/path/alpaca-credentials.json \
  --api-url http://127.0.0.1:40880 --receiver-id RECEIVER_ID \
  --collector-registration-id spy-iex \
  --collector-owner-id iex-spy-run-001-owner
./bin/mantik alpaca-collector-status --journal "$MANTIK_CAPTURE_JOURNAL" \
  --collector-id iex-spy-run-001
```

Receiver and registration placeholders must match actual readbacks. The first
collector supports one registered stock symbol and `latest_quotes`, with
1–16 acquisitions and 1–300 seconds of pacing after each retained/delivered
sample. Calendar collection is separate by default; the opt-in mode below coordinates
a fresh calendar with its quote window. A run has a
two-hour deadline; each acquisition and delivery has at most two minutes.
Control requests have five-second deadlines.

## Optional regular-session window

Add `--regular-session-seconds 300` to the same invocation to opt into
calendar-first collection. The value is a wall-clock limit of 1–3,600 seconds
starting at the retained collector intent. The quote spec still declares
1–16 samples, with pacing restricted to 1–30 seconds in this mode. Omitting the
flag, or passing zero, preserves the existing quote-only behavior. A new run
identity is required when changing the mode or duration.

After claiming the fixed generation, the collector acquires one fresh calendar
for the intent's New York date through the deadline's New York date, at most two
dates. It uses the registered symbol/access reference and derives the calendar
capture identity as `RUN_ID-calendar`. It retains, admits and publishes the
calendar before any quote request. A known closed session ends the run without
waiting for a future open or making a quote request. Unknown/failed calendar
history, failed admission or publication, and uncertain time stop the run.

A valid open calendar bounds quote activity by both the declared deadline and
its regular-session close, including an early close. The collector checks the
calendar before and after renewal and pacing. Acquisition and delivery contexts
also inherit the earlier deadline. A quote that becomes unusable or stale by
post-publication inspection is retained and published as evidence, then stops
further acquisition. Session classification and freshness use the existing
normalizer/coverage contract; the client does not invent session times or
loosen age/price requirements. A response interrupted by a deadline retains its
original attempt history rather than claiming successful acquisition.

Preflight budgets include one extra journal capture/collection, up to two extra
admissions, calendar control requests, and artifact headroom before claiming.
The journal remains exclusively owned, but other server writers can consume
shared capacity after preflight. Server admission and raw-publication limits
remain authoritative at mutation: exhaustion
stops the run and retains the acquisition; no history is pruned or replaced.
The existing server-wide bounds still apply. The maximum duration does not
expand the declared count or retention capacity. Longer operation requires a
new explicitly named run within remaining capacity, never automatic rollover.
Each new regular-session run refreshes its own calendar once. There is no
mid-window calendar refetch, overnight wait, scheduler, automatic restart,
catch-up, or capacity reset.

Regular-session intent/result artifacts use v3; v1/v2 history remains readable.
`calendar` reports the retained calendar collection, `calendar_published`
requires its validated publication receipt, and `published` continues to count
**quote** publication receipts only. Full completion requires the calendar,
all declared quotes and acknowledgements, publication receipts, and confirmed
lease release. The calendar may normalize to zero inputs for an empty date
range response; its raw publication is still required.

`stop_code` distinguishes `declared_count_completed`, `regular_session_closed`,
`window_deadline`, `calendar_unavailable`, `clock_backwards`, `quote_unusable`,
`cancelled`, and `collector_failed`. Ending at close/deadline before the declared
count is `stopped`, not full completion. The CLI returns a nonzero result for
stopped runs, including a known closed session: inspect the retained code and
actual runtime/lease before deciding on a new run. Known-lease cleanup still has
its independent five-second deadline; an unconfirmed release stays unconfirmed.
A stopped or incomplete identity never resumes automatically.

These windows coordinate evidence acquisition only. They do not prove
continuous feed availability, collector OS liveness, strategy performance,
profitability, or authority to execute an order.

## Ownership and delivery

The journal must be empty or fully acknowledged with the same receiver and
collector registration binding. Pending/uncertain delivery must be inspected
and recovered explicitly before another collector run. A new, explicitly named
finite run can reuse a fully acknowledged journal within its remaining capacity.
The command checks journal and registry retention budgets before claiming.

The required 300-second lease exceeds each bounded acquisition/delivery
deadline. The collector renews before acquisition and again before delivery.
Pacing waits are split into at most 30-second parts, with renewal before each
part. The lease generation from the accepted claim stays fixed throughout the
run; the client never adopts a replacement generation from a later snapshot.
The server's transactional admission fence remains authoritative if the client
stalls, loses ownership, or encounters a clock change.

Every acquisition uses the existing retained request/response protocol. Missing
or quarantined quote evidence is delivered as such; collection success does
not make it a usable price. Delivery verifies receiver readback before recording
an acknowledgment. A lost delivery response stops the run with the input and
attempt intact. The existing [fenced submission recovery](collector-submission.md)
can reconcile an already accepted input after expiry or stop, without another
submission.

After each acknowledged delivery, the collector saves the exact raw publication
request, posts it to `/v1/feed-evidence`, validates the canonical capture digest,
registration and fixed owner/generation, then saves the receipt before another
sample. Publication and delivery share a two-minute deadline. A lost publication
response permits at most three identical publication attempts within that
deadline; no new generation or provider read is used for a retry. A rejected,
unconfirmed or mismatched publication stops further acquisition and preserves
the request for explicit inspection. The known lease still receives bounded
cleanup. An older server without this endpoint cannot complete a new run.
Only captures acquired by this run are published automatically; older journal
captures require explicit publication. Server publication capacity exhaustion
also stops the run without deleting evidence or pruning history.

Control requests and successful receipts are retained separately in the journal,
before subsequent work. Request IDs include journal and run identity. A lost
claim response allows no acquisition and has no trusted generation to stop.
A lost renewal stops further work; a known generation can still be released.
Cancellation uses a separate five-second cleanup context to request a stop.
A lost or rejected stop is reported as unconfirmed, even if collection and
delivery finished. A stop receipt declares lease release, not process liveness.

## Retained outcomes and recovery

Collector status reads the private intent, control history, session, and terminal
receipt. It reports `completed`, `stopped`, or `incomplete`; it does not inspect
processes or infer that an incomplete run is alive. Completion requires the
declared captures, acknowledged delivery, validated raw publication receipts,
and a verified stop receipt. `published` counts receipts for this run;
`publication_required` distinguishes receipt-requiring v2/v3 runs from
historical v1 results.
Historical v1 completion remains readable and does not imply raw publication.

An exact completed invocation returns retained history without claiming,
renewing, fetching, or submitting again. Changing the spec, owner, receiver, or
registration under the same identity conflicts. A stopped or incomplete run
does not automatically resume, retry control requests, reacquire a lease, or
catch up missed samples. Inspect actual runtime handles, server ownership and
command history, and private acquisition/delivery attempts before choosing an
explicit recovery action or a new run identity. A status receipt alone cannot
authorize replacing a live process.

Collector control artifacts are private journal evidence and travel with
[journal archives](alpaca-archives.md). Existing archive verification retains
their bytes; collector-status performs their semantic inspection. A crash or
partial local publication preserves unfinished evidence. Restoring a journal
does not activate a collector. Older clients do not implement this orchestration;
use the current revision for collector inspection and recovery.

This finite command can read authorized market data and open research work. It
does not change fixture execution, configure global feed coverage, prove
profitability, or enable orders. Provider entitlement, usable fresh-price
coverage, prospective strategy evaluation, and live-money execution retain
their separate gates.
