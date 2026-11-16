# Owner daily brief

`mantik daily-brief` renders one frozen paper-book brief for the terminal and,
when explicitly requested, delivers that same brief to an owner-selected JSON
webhook. It needs no model, broker or provider call. It does not fund the book,
change targets, resize sleeves, clear controls, grant qualification, schedule a
host timer or deploy anything.

## Calling it

The owner API credential comes from `MANTIK_API_TOKEN`, or the environment name
selected by `--token-env`. Keep its value in the existing private environment;
never pass the token on the command line. The service must have authentication
configured even in development mode.

For the `book` fixture on November 16, a terminal call is:

```sh
mantik daily-brief --url http://127.0.0.1:40880 --book book \
  --date 2026-11-16 --outbox "$HOME/.local/state/mantik/daily-brief"
```

The outbox's parent must already exist. The command creates the final directory
as mode 0700, requires current-user ownership, and retains regular 0600 files.
The date is an explicit America/New_York daily identity. Choose the actual book
and date when invoking it. `--as-of` optionally selects a precise RFC3339 clock;
otherwise the service supplies its read clock. The requested clock cannot be in
the future or precede the last event of the returned finalized journal prefix.
A history prefix is never silently truncated to make a historical clock fit.

The authenticated read operation is
`POST /v1/capped-books/{book}/daily-brief-source`, with body `{}` or an object
containing `AsOf`. It rejects query parameters, unknown fields and trailing JSON.
It reads the complete current replay projection and immutable configuration,
including checkpoints when present. Both memory and PostgreSQL return detached
records. SQL suppresses payloads over the read budget before pgx receives them. Book reads are limited to 8 MiB;
separately clocked family evidence has a separate budget below 8 MiB. The entire
source is limited to 16 MiB. Capacity failure returns unavailable/error, never a
partial book. Timeouts and storage failures fail closed.

The source freezes the logical book version, configuration and every retained
event ID, with a journal SHA-256 and a source SHA-256. Checkpoint counts and hashes
bind folded observations; their original commands remain in immutable custody. It includes the exact gate records and sampled
family search fences when the registry supports them. A pending family has no
finite count. Each family's separately clocked count is sampled once per read,
then reused for its sleeves. These observations are not a transaction across
families and book state, and never establish permission to submit an order.
An observation newer than an explicit historical `AsOf` is unavailable for that
brief. Later concurrent book events cannot mutate a retained prefix or receipt.

## Numbers and evidence gaps

Equity is USD ledger cash plus each owned asset's signed filled quantity times
one coherent retained mark set. Shared assets are valued once at book level;
assets outside a current strategy universe still require marks. Settled cash
comes from the existing settlement replay. Opening capital and recorded deposits
are distinct. Each deposit retains its identity, amount, effective clock and
receipt clock. Scheduled but unrecorded funding is not counted.

Valuation uses prospective, sourced daily-review marks or the midpoint of a
retained valid incubation bid/ask quote. A quote set must share one observation
clock; marks from different sets are never assembled into synthetic coverage.
The final mark must cover every nonzero holding and be no more than 60 seconds
old at the brief clock, with its receipt by that clock. Invalid, future, stale,
missing-source or incomplete marks make equity unavailable. Book cash remains
known independently. When all positions net to zero, ledger cash alone proves
equity; a no-trade all-cash book can therefore have a known zero return.

Book TWR covers opening through the brief clock. For each external deposit,
it values the book **before** that flow at the flow's exact effective clock,
compounds the preceding segment, then adds the deposit to the next segment's
starting equity. Retained fills and deposits are sorted by economic clock;
deposits precede fills at equal clocks, matching account replay. A late fill
receipt cannot move a fill past a deposit for return calculation. Every required
boundary needs a complete coherent valuation observed at exactly that boundary.
There is no interpolation or reuse of a nearby mark. Cash-only boundaries need
no quote. Missing boundary evidence or a nonpositive return base makes TWR
unavailable even if current equity is known. Returns are decimal fractions,
printed to 18 decimal places; dollar balances use 9 decimal places.

Daily review return on prior budget and incubation return on contributed capital
are different metrics. Neither supplies this TWR. The brief leaves those
contracts unchanged and does not use the risk brake's unitized NAV as TWR.

SPY is unavailable unless `--benchmark` supplies an explicit retained total-return
index pair. The brief never fetches a price or relabels price-only returns.
The bounded JSON has these Go field names:

| Field | Required meaning |
| --- | --- |
| `Symbol`, `Currency`, `Method` | `SPY`, `USD`, `total_return_index` |
| `EvidenceClass` | `prospective_market_unqualified` |
| `StartAt`, `EndAt` | Exactly the opening and brief valuation clocks |
| `ReceivedAt` | At or after the end, no later than the service capture clock |
| `StartIndex`, `EndIndex` | Positive decimal total-return index levels on the same methodology |
| `SourceID`, `SourceSHA256` | Source identity and lowercase SHA-256 of original retained source bytes |

Use `--as-of` to match the supplied end clock. `CapturedAt` records the service
read clock separately from the valuation interval. Benchmark evidence may arrive
after its end clock but must already be available by capture. The end clock
must still match exactly; the brief never shifts the comparison window. Original source bytes remain
in owner custody outside Git. The receipt retains the imported fields before
use or delivery and hashes them with the source. This is an explicit owner
import, not independent verification of the total-return methodology or a
provider collector. Missing, price-only or incomparable evidence stays
unavailable, including on an all-cash book. A hash does not attest truth.

## States, actions, refusals and health

Every retained sleeve and finalized plan's declared strategy appears with its
stage, latest review action, scale and ceiling, owner pause/retirement, pending
order identities and retained targets. Sizing budgets are the retained sizing
result, not a new allocation. The latest review may apply only to later
decisions; the display does not rewrite earlier budgets. Missing sizing or review
evidence is unavailable. Pending families and changed search histories are
reported separately from lifecycle and retained sizing.

Refusals include retained sizing waits, persistent hard halt, pause, kill and
reduce-only controls, sampled family fences, and retained unhealthy admission
checks. Idle-cash reasons include those refusals, absent sleeves/sizing,
outstanding reconciliation and the fixed pool/sleeve caps. The brief does not
invent an exhaustive refusal history: failures that were never retained in the
book cannot be reconstructed here. Cash is reported as cash, not inferred
unallocated budget or free buying power.

Health shows the last retained book-specific admission report, its source event,
observation/expiry clocks, codes, evidence references and independent work
checks. Missing health is unavailable. Expired/unbounded ready observations are
explicitly marked; even a recent retained report does not establish current
health or renew entry permission. No synthetic workflow heartbeat becomes
book-specific safety proof.

## Explicit notification and recovery

A current-user mode-0600 `--notification-config` JSON file selects `URL` and an
optional `TokenEnv`. For a local receiver at port 40990, configuration is:

```json
{"URL":"http://127.0.0.1:40990/mantik-brief","TokenEnv":"MANTIK_BRIEF_WEBHOOK_TOKEN"}
```

Set that environment variable privately, then add the config path to the
terminal command. Add `--notify` to request delivery; selecting configuration
alone does not send. Production destinations require HTTPS. Cleartext is
restricted to literal loopback IPs. User-info, query parameters and fragments
are rejected. HTTP redirects are refused for both service and notification;
credentials are never forwarded to another endpoint. Transport errors and
remote bodies are not printed. No destination is inferred from another service,
model output or research data.

The immutable daily receipt contains the request, detached source, imported
benchmark, deterministic brief and digest. The identity is book plus New York
date inside one private outbox. Restart and exact retries read that receipt
without requiring the API or its credential; changed clocks, benchmark content,
service URL or destination conflict. Reuse the same outbox across invocations.
No regeneration silently replaces a day's retained evidence.

Before delivery, the outbox fsyncs an immutable intent. It sends JSON fields
`ID`, `ReceiptSHA256`, `Text` and `Brief`, with an `Idempotency-Key` header derived
from book/date/brief digest. `Text` is the identical terminal rendering. A bounded
2xx acknowledgement produces a durable delivered receipt. Failed or ambiguous
delivery leaves the intent retryable with exactly the same payload and key;
a retained success suppresses another send. A nonblocking process lock prevents
concurrent publishers/deliveries in that outbox. Tokens are never retained.

Transport is at least once until acknowledged. A receiver must deduplicate the
idempotency key for exactly-once handling: if it accepts a notification and the
client crashes before recording the acknowledgement, retry repeats that key.
A 2xx proves receiver acceptance, not human reading. This implementation installs
no timer and sends no external messages during verification. Owner walkthrough
#100 and natural-window qualification #108 remain separate work.

## Checkpoint-aware source reads

The source freezes the complete current replay projection, including retained
risk checkpoints, with its logical command version. A checkpoint represents
multiple original risk observations; its hot event count is not the journal
version. The brief validates the projection before deriving numbers and hashes
its unchanged representation. It does not expand checkpoints into observations
or replace original source custody.

PostgreSQL pins the current version and source hash, then verifies the original
commands using the same replay reader as operational admission. Matching
writer-chosen head and receipt hashes alone cannot establish that history.
The immutable command pages preserve the selected version across concurrent
appends. Verified committed prefixes can warm the private cache; a deadline
returns no partial book or authority. SQL checks the projection byte budget
before returning configuration or head bytes to the client. Both stores return
a detached complete state or an error at the byte limit; a truncated book is
never a brief source.
