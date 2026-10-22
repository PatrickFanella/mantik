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
calling the method. Slippage and variable fees are 0 through 1,000 basis points,
commission is 0 through 1,000,000 cents per fill, displayed liquidity is 1 through
10,000 basis points, latency is 0 through 60,000 milliseconds and quote age is
1 through 3,600 seconds. Orders expire within 24 hours. Journal arithmetic caps
aggregate gross and fees at 1,000,000,000,000 cents each.

For a buy, the starting price is ask; for a sell it is bid. Buy slippage rounds
adversely upward to cents, and sell slippage rounds downward. A result below one
cent or above the price bound does not fill. Gross is price times filled shares.
Variable fees round upward to a cent, then add the fixed per-fill commission.
Buy debit is gross plus fees. Sell net proceeds are gross less fees and can be
negative when fees exceed gross. Portfolio accounting must explicitly support and
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
unsupported. The execution owner must allocate liquidity across competing orders;
this per-order method does not provide a shared order book or portfolio lock.

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
