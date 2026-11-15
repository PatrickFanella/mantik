# Indicative strategy option chains

`internal/options.Read` reads an underlying's known standard option references
and latest daily closes from the retained store at an explicit decision time.
Both Memory and PostgreSQL provide `OptionChainAsOf`; no migration is needed.
Availability is filtered before selecting a reference or a price revision.
An old-session correction cannot replace a newer session's close. Trades are
retained by acquisition but are not used as daily-chain closing prices.

`strategy.Spec.Data` can declare `option_chains_indicative`. `strategy.Build`
then requires its bar source to also implement `strategy.ChainSource` and calls
`OptionChain(ctx, assetID, asOf)` for each underlying asset. The source must
bind the stable underlying ID to its symbol and call `options.Read`. A
stock-only spec does not load chains. `View.OptionChain(assetID)` returns a
copy and a presence flag. This adds research data, not option order targets.

## Clocks and missing data

The caller supplies an **unadjusted**, same-session underlying close with a
stable asset ID, session date, actual session-close time, observation time
and availability time. The option bar's session must match that date. The
caller also supplies annual rate and continuous dividend-yield scenarios and
an elapsed `MaxAge` for the underlying close. No spot, rate, yield or freshness
policy is silently inferred. Acquisition remains a separate responsibility.

References, prices and spot must be available by the decision. Original price
and reference clocks remain inspectable. Data acquired today cannot populate
a yesterday decision, even if the bar's market date is old. The chain can be
empty or incomplete; it never implies that a listing was historically complete.
Only standard OCC symbols consistent with their metadata and a `size` of 100
are included. Expired contracts and nonstandard deliverables are omitted.

Each entry has `EstimateStatus`: `ok`, `missing_same_session_close`, or
`unsolvable_indicative_price`. Only `ok` permits use of `IV` and `Greeks`.
Boundary prices, out-of-bounds prices and unidentifiable IV fail explicitly;
missing prices never become usable zero-risk estimates. A mismatched session
retains its price clocks but has no estimate. No bid/ask midpoint is invented.

## Pricing conventions and limits

Model `black-scholes-merton-european-v1` uses European Black-Scholes-Merton
with continuous dividend yield. The estimate's market valuation time is the
underlying session close, `Spot.At`, not a claim of a new live price at the
later decision clock. Time to expiry is ACT/365 to **16:00 America/New_York**
on the contractual expiration date. That is a declared approximation; it does
not model product-specific final trading cutoffs, American early exercise,
discrete cash dividends or adjusted deliverables.

Spot, strike, option price and Greeks are per underlying share in dollars.
IV is an annual fraction. Delta is shares per option share, Gamma is delta
change per dollar, Vega is dollars per unit volatility (1.0, not one percentage
point), and Theta is dollars per calendar day. Multiply Vega by 0.01 for one
volatility percentage point; contract exposure requires the 100-share
multiplier, quantities and signs in later risk work. IV bisection is bounded
at annual fractions `[0.000001, 8]` and rejects numerically unidentifiable
solutions. These estimates do not prove executable pricing or American risk.

All chains are labelled `indicative`; prices retain
`feed_selection=provider_default_unverified` from the
[collector](option-data.md). This is not OPRA provenance or fill evidence.

## Verification

`go test ./internal/options ./internal/strategy` checks pricing and the
strategy boundary, including correction visibility, session/freshness rules,
missing-price status and copied entries. Storage tests run the same discovery
contract against Memory and disposable PostgreSQL. Six worked examples cover
calls, puts, dividend yields, negative rates and short maturities against
QuantLib 1.40's `AnalyticEuropeanEngine`, at `1e-10` absolute tolerance for
every price/Greek and recovered IV. See
[the retained reference generator](../../internal/options/testdata/generate.py).
QuantLib is a test-reference generator, not an application dependency. Its
[European option tests](https://github.com/lballabio/QuantLib/blob/v1.40/test-suite/europeanoption.cpp)
provide the upstream reference implementation context.
