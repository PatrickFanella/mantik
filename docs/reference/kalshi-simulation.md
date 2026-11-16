# Separate Kalshi notional simulation

`internal/kalshi.NewNotionalBook` creates an offline book in integer
microdollars. `Execute` reads stored market/book observations through
`SimulationStore`, together with an explicit captured `EventObservation`.
It has no credentials, venue order method or equity-book mutation path.
`Report` has kind `kalshi_notional`, always `IncludedInSPY=false`, cash,
inventory, fill fees and official settlement payouts. It is not an
`engine.Result` and supplies no portfolio/SPY return. Unresolved inventory is
reported as inventory, not marked at an invented fair value.

## Fill policy

Version `kalshi-notional-depth-fees-v3` supports whole-contract limit buys and
sales of owned contracts. The execution cutoff must be after the decision,
and its selected recorded book must also be strictly later than the decision.
Future/mismatched market and book reads are refused. Market and event-fee
evidence must also be known by the selected book's simulated fill time, not
merely by a later replay cutoff. The market must be known,
open, and before its close; normalized fields and book levels must agree with
their retained raw source. Missing/unexecutable depth produces no fill.

The API returns YES and NO bids. A YES buy consumes the complementary NO bid
at `1 − no_bid`; a NO buy consumes the complementary YES bid. A sale consumes
the held outcome's own bid. Matching proceeds through executable levels in
price order and cannot exceed displayed depth or owned inventory. Displayed
fixed-point quantities may be fractional; their fractional remainder is
unavailable to whole-contract orders. It is conservatively floored.

Consumption is shared by resting outcome side and price across orders and
snapshots. Re-reading an unchanged snapshot never replenishes liquidity. Even
a newer snapshot is reduced by already simulated consumption at that level;
this deliberately conservative replay cannot infer canceled/replenished orders
or queue priority. Maker fills are not fabricated from a resting quote.
Maker-first strategy/queue behavior remains later work in #104.

Order IDs are idempotent for the same complete request and cutoff; conflicting
reuse fails. An insufficient-cash or invalid execution changes nothing. All
operations must be replayed in nondecreasing cutoff order, on one sequential
book instance. Fill times must also be no earlier than the latest applied fill
or settlement cash event; a later cutoff cannot use new proceeds to fund an
older book. An empty execution does not advance this financial-event clock. Copies returned by execution/report cannot mutate its state.
Initial notional capital is bounded at $1 million and one order at one million
contracts. No production account, database or capital flow is shared.

## Fee evidence and rounding

Kalshi's July 7, 2026 schedule uses quadratic taker fees
`M × 0.07 × contracts × price × (1 − price)`. M is not assumed to be 1.
The series raw response must contain a known quadratic `fee_type`, numeric
`fee_multiplier` and matching series ticker. A separate event observation must
identify the matching event and series and be known by the execution cutoff;
its non-null type/multiplier overrides take precedence. Unknown/flat fee models
and missing metadata fail. Old market/book records remain inspectable but
cannot be economically simulated without this event evidence.

`Client.FetchSimulationEvent` captures the public `/events/{ticker}` response
in the existing private acquisition journal before returning its bytes, hash
and observation clock. Simulation consumes that retained observation; it does
not fetch data itself. This adds no SQL schema or background worker.

The model fee rounds upward to microdollars, then the balance change aligns
downward to a centicent ($0.0001), following the direct-member precision. Each
displayed price level rounds independently. Order-level accumulated rounding
rebates are not credited, so this is a conservative fee estimate. It is not a
claim to reproduce FCM/retail cent precision, maker rebates or actual account
fees. Fill receipts retain both series and event source hashes.

Sources: [official fee schedule](https://kalshi.com/docs/kalshi-fee-schedule.pdf),
[fee rounding](https://docs.kalshi.com/getting_started/fee_rounding),
[orderbook response semantics](https://docs.kalshi.com/getting_started/orderbook_responses),
and [event metadata](https://docs.kalshi.com/api-reference/events/get-event).

## Settlement and checks

`Settle(ctx, reader, ticker, cutoff)` selects the retained official binary
outcome through `KalshiWeatherSettlements`, the existing weather collection
store boundary. It rejects a future, duplicate or inconsistent reader result,
requires the raw finalized payload and SHA to match its ticker/outcome/time,
and pays $1 for winning YES or NO contracts and $0 for losing contracts.
It clears both sides exactly once. Forecast probabilities cannot settle a
position. Nonbinary/scalar settlement products are unsupported.

`go test ./internal/kalshi` covers depth/limit matching, fractional-depth floors,
shared complementary liquidity, repeated snapshots, order replay, cash and
inventory bounds, source/fee evidence, event overrides, future-data refusal,
official YES/NO settlement and the separate report type. New HTTP fakes use
the required `loopbacktest.GoClientOnly` wrapper. These tests establish
simulation mechanics, not actual fills or strategy profitability.

[Calibrated maker-first decisions](kalshi-maker.md) add separately pending
post-only quotes and cash reservations. Depth execution remains taker-only;
resting maker fills are never inferred from snapshots.
