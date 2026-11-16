# Capped equity paper book

`account.DefaultCappedConfig` declares the intended $2,000 opening capital and
$500 every-other-Friday schedule at 06:00 America/Chicago. The configuration
binds one book ID, expected Alpaca paper-account ID, account type and opening
clock. `storage.CreateCappedBook` retains it immutably. One broker account can
have one capped-book configuration in this registry.

`internal/cappedbook.Client` connects that ledger to the existing journaled
paper adapter. The caller supplies the store, paper broker and book ID. No
broker cash, equity or buying-power field enters the book. A $5,000 order can
fit Alpaca's $100,000 paper account and still be refused by the local $2,000
book before any POST.

`Rebalance` turns sleeve targets into retained fractional day-limit orders.
`Submit` accepts only an exact order from that distribution and a complete
current mark set for all held and pending symbols. Raw quantities cannot bypass
the [distributor and persistent risk checks](portfolio-gates-and-risk.md).
Adapter grammar is checked before reservation.
The durable reserve includes maximum execution price/quantity and a total fee
allowance. Account rules check all outstanding reservations together, using
settled cash and this book's marked positions. Independent pending orders
cannot net against each other: actual holdings stay reserved, reductions do
not release capacity before their fills, and opening legs consume separate
capacity after all pending fees and adverse limit-to-mark gaps are reserved.
A costly pending cover cannot leave new orders relying on its pre-loss equity. An order with an ambiguous
outcome keeps its reserve and original client ID. Reconciliation only looks
up the original order; it never resubmits.

A crash or deterministic failure before adapter intent can be recovered with
`AbortUnsubmitted`: the adapter first retains an abort record under its existing
process gate. That ID can never POST afterward. Only then may the capped book
reject/release its unbound reserve. If a durable submission intent already
exists, abort refuses and the reserve remains. This also fences a delayed
caller racing with recovery.

`Fund` appends configured simulated flows only for synthetic-schedule books. A
restart/late invocation skips retained IDs. Owner-receipt books require an actual
confirmed receipt for each slot and never receive automatic funding. Each event
retains application and economic clocks. Paper funding never transfers funds
into the venue.

`Sync` reads journaled order states and raw fill activities. It imports only
venue IDs bound to this book's client IDs, retains original activity hashes,
and compares cumulative quantities before releasing a terminal order's reserve.
Partial fills consume part of the reservation; other account activity remains
outside this book. Exact retries do not change cash or positions. Delayed fills
are replayed in economic time before later deposits, independently of receipt
order. A missing mark set is an input error; conflicting fills, overfills,
price/fee breaches or terminal quantity mismatches halt further admission.
Original venue responses remain available in the Alpaca journal.

PostgreSQL migration 121 adds immutable configurations and append-only events.
Migration 126 keeps those original rows unchanged and adds immutable, indexed
command custody plus a bounded replay head. A parent-row lock and expected
version serialize reservations, observations and head publication in one
transaction. The head's SHA-256 must match the immutable transition receipt;
source records reject updates, deletes and truncation. Memory and SQL stores
expose the same command contract.

A matching stored hash is not proof of a valid transition. Operational SQL
reads reconstruct the pinned head from the immutable configuration and original
commands, checking each command, previous-state hash and resulting-state hash
against actual `account.ApplyCappedCommand` output. A writer cannot turn an
observation into authority to rewrite an earlier control or checkpoint by
choosing matching receipt/head hashes.

Each `Postgres` instance keeps a private, detached cache of verified replay
prefixes. Cold reads and restarts warm it from bounded custody pages; later
reads verify only the unseen suffix. A fully warm read still fetches the SQL
head and checks its original version and hash, without scanning old history.
Transaction-local replay never publishes uncommitted state into that cache.
Collection and assessment writers share the same instance and compiled evaluator.
They warm committed custody before opening their transaction, then recheck the
current head under the book row lock. The shared custody writer leaves cache
publication to a later committed read, so an enclosing rollback or failed commit
cannot publish a collection or qualification grant.
Submission and write fences reread and validate the current head while holding
the book row; a cached older prefix cannot authorize that newer head.

Warmup can exceed a request deadline for a long archive. The request then fails
closed, with no partial-state response or submission authority. Fully verified
committed prefixes remain available to resume warmup on the next request;
unverified transitions and partial legacy pages are discarded. After process
restart, full source replay is required again. Allow for that recovery cost and
retry warmup before expecting admission to become available.

The hot replay projection remains capped at 4,096 slots. Continuous safety
monitoring folds only consecutive trailing `risk_observe` commands into an
internal checkpoint and the latest observation. Every original command remains
available in custody. Each observation still runs the fixed `risk.Observe`
transition, including its rounding, cash-flow adjustment, NAV, high-water and
daily baselines, persistent halt and owner controls. Changing prices consume
archive storage rather than unbounded hot replay slots. A checkpoint carries
the exact resulting risk state, observation count, last original observation and
rolling source hash. Ordinary `ApplyCappedBook` commands cannot submit one.

`CappedState.Version` counts original commands, not hot slots. Use
`account.CappedPrefix` when selecting a prefix and `account.CappedCommandCount`
when walking an original-version boundary in the hot projection. Only the current trailing run
can be folded: economic events, ownership, orders, deposits, fills, controls and
prefixes already bound by review hashes stay in the hot projection. Legacy
journals replay in their original representation until their next observation.
A full legacy journal can also fold its trailing observations before retaining
an executed fill or another protective command. New optional fields are omitted
from legacy JSON. Scheduled deposits and other non-observation events still
consume slots. Exhausting those slots or archive
storage stops publication and requires retained-history review; this change
does not provide indefinite economic-journal retention.

`storage.CappedHistoryStore.CappedBookHistory` reads detached originals by
exclusive original sequence, at most 256 commands and 8 MiB per page. New
commands have a 4 MiB archive bound. A legacy command larger than the page
bound returns an explicit error and requires direct inspection of its original
SQL row; it is never skipped or truncated. Exact retries, including observations no
longer present in hot replay, compare against immutable originals; changed
retries conflict. Legacy custody remains in the migration-121 table, with its
original 4,096-row cap. New commands use the migration-126 history table.

For inspection or recovery, read the immutable configuration and pin a target
version from `CappedBook`. `storage.ReplayCappedBookHistory` rebuilds that exact
version using bounded pages, checks sequence and source digests, keeps legacy
prefix bytes, and verifies every later before/after projection hash. It returns
a detached state and grants no store write or submission authority. PostgreSQL
backups must retain configuration, legacy events, command history and replay
heads together; do not prune custody or edit a checkpoint to reclaim space.
Archive size grows with monitoring frequency, so provision and monitor database
storage. There is no automatic archival deletion or retention expiry.
The book reports cash, settled cash, marked equity, owned positions and pending
orders, its retained distribution and the persistent risk-brake view. It supports
the opt-in [incubation runner](incubation.md), which retains decisions and both
fill results in this journal. It does not establish protected real-quote positions.

## Funding modes

`CappedConfig.FundingMode` is immutable when a book is created. An omitted value
or `"synthetic_schedule"` keeps legacy automatic simulated scheduled deposits.
`"owner_receipts"` requires the fixed USD 2,000 opening / USD 500 biweekly terms
and a confirmed owner receipt for each deposit. Schedule slots alone never add
cash in this mode, including through client `Fund`, `Rebalance` and `Submit`.
Actual receipt economic clocks and unique source references remain separate from
the eligible schedule-slot IDs. See [owner book controls](owner-book-controls.md).

The account ledger refuses unconfirmed receipt-mode deposits and mixed synthetic
books with owner-attested real flows. Existing synthetic events replay with their
original semantics and bytes. The default config JSON remains unchanged. No
implicit conversion, historical funding fabrication or account-limit change is
permitted by selecting a mode. A different funding mode requires a new book;
there is no migration in this change.
