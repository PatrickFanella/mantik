# Versioned paper fill method

`internal/paperfill` implements `usd-equity-displayed-quote-v1`, a deterministic
whole-share, integer-cent USD-equity execution method shared by replay and ongoing
paper callers. This is an internal dependency for issues #20, #21 and #19. It has
no CLI, HTTP route, provider access, account writer or admission authority.
Service-owned order/effect persistence, admission and atomic allocation binding
remain unfinished. No existing fixture execution path uses this method yet.

## Declared assumptions

The model binds adverse slippage basis points, fee basis points, fixed commission
per partial fill, a fraction of displayed liquidity, order latency and maximum
quote age. These are explicit simulation assumptions, not measured venue behavior
or recommended trading parameters. Different assumptions require a different
model hash. Every fill pins the method, model hash, source quote, market update,
input evidence class and caller mode (`fixture`, `replay` or `ongoing_paper`).
All modes use the same arithmetic; a mode or prospective label grants no
qualification credit. Synthetic and stress tests remain unqualified.

Prices range from 1 through 100,000,000 cents; quantities are whole shares from
1 through 1,000,000. The owner must normalize provider size units to shares before
calling the method. The [REST quote share-size contract](alpaca-quote-share-units.md)
now supplies documented dimensions in supported owner reads; it does not remove
the fill method's own quantity bounds or its admission/custody prerequisites. Slippage and variable fees are 0 through 1,000 basis points,
commission is 0 through 1,000,000 cents per fill, displayed liquidity is 1 through
10,000 basis points, latency is 0 through 60,000 milliseconds and quote age is
1 through 3,600 seconds. Orders expire within 24 hours. Journal arithmetic caps
aggregate gross and fees at 1,000,000,000,000 cents each.

For a buy, the starting price is ask; for a sell it is bid. Buy slippage rounds
adversely upward to cents, and sell slippage rounds downward. A result below one
cent or above the price bound does not fill. Gross is price times filled shares.
Variable fees round upward to a cent, then add the fixed per-fill commission.
Buy debit is gross plus fees. Sell net proceeds are gross less fees and can be
negative when fees exceed gross. The internal [v2 allocation ledger](allocation-ledger.md#ledger-versions-and-fee-bearing-exits)
can reconcile a funded net debit. Portfolio accounting must explicitly select and
fund that debit before such a model can be admitted for operational use. This
method does not calculate cost basis or realized PnL.

## Eligibility, stops and partials

A quote cannot fill before order latency expires. Its market timestamp must also
be at or after that eligibility time, so a pre-latency quote cannot be reused
later just because it is still young. Future receipts, stale prices and absent,
missing or quarantined quotes produce no fill. A newer unusable quote suppresses
an older receipt. Immutable quote IDs cannot be rebound to changed content.

Market orders use the modeled bid/ask price. Limit orders fill only if the price
including slippage respects the limit. Stop-market orders trigger on ask for buys
or bid for sells and fill at the available modeled price; a gap does not invent a
transaction at the stop. Stop-limit orders keep their trigger once crossed, can
remain unfilled through a gap and become eligible if a later quote satisfies the
limit. Triggering alone produces no economic fill.

A fill is bounded by remaining order quantity and the whole-share floor of the
configured fraction of displayed side liquidity. Buys also fit the owner's
remaining debit ceiling, including all fees. The method chooses the largest
whole-share partial that fits. Spent debit and prior fills remain after expiry or
cancellation; cancellation does not fabricate a sell or a capital-release effect.

Each economically identical market update can supply liquidity once per order.
The identity includes symbol, market timestamp, bid/ask prices and quantities;
capture IDs, receipt time and source hash changes cannot replenish that update.
A new distinct update may supply the declared fraction again. This is a quote-
based liquidity assumption; queue position, hidden depth and venue matching are
unsupported. The per-order method does not allocate shared liquidity. The
internal shared-book method below supplies that dependency; a service owner must
still bind it to actual retained evidence, capital and atomic persistence.

## Journal and ownership

`New` declares a model and order. `Apply` validates/replays the full immutable
journal, applies one owner-supplied command and returns a detached journal,
current projection and historical outcome receipt. Exact command retries do not
append a fill, even after later partials or terminal state. Changed commands under
the same ID conflict. An earlier retry returns its original outcome together with
the current projection. Conflicts and invalid inputs return no partial result.

`Replay` rederives every entry and verifies its chain; self-hashing a fabricated
fill does not make it valid. JSON round-trip tests exercise reconstruction of the
internal journal. They do not prove service database durability, crash recovery
or deployment. At most 128 unique commands and 1 MiB are retained, with no eviction.
Exact retries remain readable at capacity. A future operational owner must retain
capacity for cancellation, protection and recovery before activation; this finite
research kernel must not be installed as a protective worker queue.

The projection authority is `owner_supplied_paper_fill_method_only`; execution
attestation and qualification credit are false. Supplied quote hashes do not prove
that the service owns the evidence. Before operational use, the service must select
actual retained quotes, applicable admission and finalized decisions, allocate
shared liquidity and capital, then persist order/fill/effect and ledger state
atomically. Public callers must never import model-generated effects or balances.

## Verification

Tests cover spread/fee/slippage rounding, replay/ongoing-paper mechanical parity,
partial and fee-limited fills, market-update aliases, stop gaps and unfilled
stop-limits, stale/missing/future/pre-latency evidence, expiry/cancellation,
signed proceeds, duplicate/conflicting commands, corruption, capacity and JSON
reconstruction. Seeded multi-step scenarios check quantity and cost conservation;
concurrent calls check that the pure method does not mutate shared input values.
The full operational acceptance criteria of #19, #20 and #21 remain open.

## Shared displayed-liquidity book

`NewBook`, `ApplyBook` and `ReplayBook` implement the distinct aggregate method
`usd-equity-shared-displayed-quote-v1`. A book declares one model and one immutable,
ID-sorted list of at most 32 orders. Lexical order ID determines priority. This
is an explicit simulation assumption, not venue queue priority or an allocation
ranking. All orders must use the same caller mode; symbols may differ.

A quote command visits working orders for its symbol in that declared order.
Eligible buys share the whole-share floor of the model's ask-liquidity fraction;
sells independently share the bid fraction. Only actual modeled fills consume
that allowance. An untriggered stop, nonmarketable limit, fee-limited partial or
ineligible order leaves the remaining allowance for later orders. Other symbols
do not consume it. The original quote, source hash and market-update identity
remain intact; the method caps execution without fabricating a smaller quote.

Consumption is retained across commands under the same market-update identity.
New capture IDs, source hashes, receipt times and evidence-class labels do not
replenish an economically identical update. A distinct market update supplies a
new allowance under the declared quote-based assumption. Quote IDs remain
immutable across all orders, including when no order can fill. Unusable quotes
retain the per-order suppression rules.

A command can instead cancel one working order by ID or advance eligible orders
with a quote-free tick. Exact retries return the historical outcomes and current
projection after full replay. The hash-linked journal regenerates every outcome
and shared allowance. Invalid inputs and conflicts return no partial aggregate.
The method is pure and returns detached values; a concurrent service writer must
serialize and atomically retain it with allocation and effects.

The bound is 128 commands and 8 MiB without eviction. Exact retries work at
capacity. This finite kernel has no reserved protective/recovery capacity and
must not be installed as a protective worker queue. Authority is
`owner_supplied_shared_paper_fill_method_only`, with false execution attestation
and qualification credit. There is no public book writer, CLI, database migration
or operational activation. Owner-selected source-to-fill binding, service-owned admission,
funded reservations, atomic effect/ledger persistence and recovery remain
unfinished under #19/#20/#21/#25.

Tests check shared bid/ask conservation, capture aliases, stable priority,
fee-limited fills, ineligible orders, symbol isolation, unusable-quote suppression,
single-order parity with the existing method, historical retries, cancellation,
self-rehashed corruption, bounded retention, JSON replay and concurrent immutable
calls. Seeded scenarios check quantities, costs and debit ceilings.

The allocation core can preflight entry debits from a fully replayed shared
book through `ApplyEntryFillBook`; see the [entry-fill binding contract](allocation-ledger.md#replayed-entry-fill-binding).
It requires existing reservations and an exact owner model. This internal
composition does not provide a durable service execution owner or admission.

## Conservative occurrence capacity

`paperfill.NewOccurrenceBook` selects the explicit
`usd-equity-shared-occurrence-quote-v2` book method. Within one owner-selected
provider/feed stream, all usable quotes at the same canonical symbol and UTC
occurrence time share one bid capacity and one ask capacity. Capture IDs,
receipt times, evidence labels, raw pins, price corrections and size increases
cannot replenish either capacity. The first available usable quote sets each
side's participation-adjusted capacity; subsequent corrections can only reduce
it. Capacity is recorded even if no order fills. When a correction reduces it
below quantity already consumed, remaining capacity is zero; historical fills
stay in the journal. A later occurrence receives a separate budget.

Future or quarantined quotes do not establish capacity. Original source quotes
remain unchanged in book commands and fill evidence. Replay rederives the
capacity history, fees and outcomes. This conservative timestamp rule may
understate liquidity when a provider reports multiple real updates at one
timestamp; it does not attest venue liquidity or independence across feeds.
The execution owner must select a single provider/feed stream and validate
whole-share sizes, availability and provenance before constructing a book.

`NewBook` and `usd-equity-shared-displayed-quote-v1` retain their original
market-update arithmetic and serialized replay. Version 1 distinguishes price
and size corrections as separate market updates; new financial owners must
explicitly choose their intended method rather than silently rewriting old
books. `RootBook` preserves the selected method when deriving entry-debit and
exit-proceeds bindings. Changing the method cannot relabel a retained hash
chain or its posted financial effects.

Both methods remain internal, owner-supplied simulation reducers. This change
adds no source importer, HTTP/CLI writer, admission, order owner, durable
financial commit or qualification. The real service execution/recovery owners
and applicable evaluation/admission remain unfinished under #19/#20/#21.
