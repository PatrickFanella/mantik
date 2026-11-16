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

`Fund` appends configured due flows by scheduled date. A restart/late invocation
skips retained IDs. Each event retains both application and scheduled effective
time. Paper funding never transfers funds into the venue.

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
A parent-row lock and expected version serialize competing reservations;
sequence guards reject gaps, updates, deletes and truncation. Memory and SQL
stores expose the same command contract. Events are bounded at 4,096 per book;
capacity or storage errors stop publication and require retained-history review.
The book reports cash, settled cash, marked equity, owned positions and pending
orders, its retained distribution and the persistent risk-brake view. It does
not start incubation workers or establish protected real-quote positions.
