# Account rules

`internal/account` checks the capped Mantik book independently of Alpaca's
larger paper-account balance. It uses exact rational arithmetic and needs no
network, model or wall clock. The [daily engine](engine.md) calls it before
each fill. This is simulator enforcement; it does not demonstrate broker
order handling or profitability.

`account.New(type, cash)` opens an empty, single-owner cash ledger. Callers
retain marked positions and update them only after `Book.Execute` accepts a
trade. They must supply all holdings, distinct asset identities, current
prices, payment provenance and a later settlement date. Refusals leave cash
and settlement credits unchanged. `Cash`, `Settled` and `Status` return
detached numbers. `Advance` releases due credits; `AddCash` applies settled
deposits or dividend income (negative for short dividend obligations).

## Cash

The default account type is `cash`. Purchases, including fees, must fit
settled cash. Net sale proceeds remain unsettled until T+1. Buys that need
unsettled proceeds return `unsettled_funds`; buys without enough trade-date
cash return `free_riding`. This deliberately stricter settled-only policy
prevents unpaid purchases. A cash-account sale touching an imported position's
`Unpaid` quantity returns `good_faith_violation`. Selling a fully paid position
on its purchase date is allowed. Short sales return `short_not_allowed`.

`SettlementDate` skips weekends, ordinary US banking and market holidays,
and the 2018 and 2025 national days of mourning. It models T+1 throughout the
2016-onward research window, including dates historically under T+2. It is a
conservative union of closure dates, rather than a historical clearing-service
calendar. The engine accepts a calendar override for exact or exceptional
settlement dates. Missing asset bars never determine settlement.

## Margin

At equity of at least $2,000, `margin` permits shorts and up to twice equity
as total overnight gross exposure, including the effect of execution costs.
`Status` reports equity, gross exposure, total overnight buying power,
maintenance requirement, excess and shortfall. Maintenance follows Alpaca's
ordinary-equity house schedule: long positions use 30%, 50% at prices from
$2.50 through $6, and 100% below $2.50; shorts use the greater of 30% or
$5/share at prices of at least $5, and the greater of 100% or $2.50/share below
$5. Both sides count toward gross exposure; short proceeds cannot create
extra equity.

`Execute(..., account.Intraday, ...)` uses current marked equity and
maintenance excess with a 4x ceiling. It refuses exposure increases that
would create a maintenance deficit. This selects real-time prevention under
the intraday framework, so accepted trades cannot create an intraday margin
deficit requiring a cure or freeze. Market-driven shortfalls remain visible
and reductions remain possible. No PDT trade count or $25,000 gate applies.
The daily engine always selects `Overnight`, since its positions can persist
across sessions; it never spends intraday-only capacity.

If equity before or after a trade is below $2,000, new longs must be funded
by settled cash after reserving existing short liabilities, and new or larger
shorts are refused. Existing positions can be reduced or closed even with a
debit. Deposits or price recovery can restore margin eligibility. Shortfalls
are reported; this module does not place liquidation orders.

Other refusal codes are `margin_equity_below_minimum`,
`initial_margin_exceeded`, `maintenance_margin_shortfall`,
`intraday_margin_deficit` and `invalid_trade`.

## Evidence and scope

The [#58 spike](https://github.com/subculture-collective/mantik/issues/58#issuecomment-6870787280)
observed no PDT fields on the paper account and a configured maximum
multiplier of 4. It did not test order enforcement. The implementation was
checked against Alpaca's [margin schedule](https://docs.alpaca.markets/us/docs/margin-and-short-selling)
and [intraday definitions](https://docs.alpaca.markets/us/docs/the-intraday-margin-rule)
on 2026-11-14; the [SEC T+1 bulletin](https://www.investor.gov/introduction-investing/general-resources/news-alerts/alerts-bulletins/investor-bulletins/new-t1-settlement-cycle-what-investors-need-know-investor-bulletin)
describes the settlement cycle. The calendar combines the
[Nasdaq market schedule](https://www.nasdaqtrader.com/trader.aspx?id=Calendar)
with banking holidays; an operational paper adapter must reconcile actual
broker settlements and supply its calendar.

Instrument-specific overrides, leveraged ETFs, non-marginable securities,
borrow checks and financing costs are outside this ordinary-equity module.
Options and multi-leg collateral belong to the later options issues. The
caller must not apply these ordinary-equity assumptions to those instruments.

Tests cover coded refusals, fee-inclusive boundaries, T+1 and holidays,
same-session cash rotation, current intraday exposure, maintenance shortfalls,
cash fallback, short collateral, reductions, and split/dividend obligations.
These use synthetic fixtures, not a connected paper account.

The [capped equity paper book](capped-equity-book.md) now uses these rules before
the existing Alpaca adapter can submit, retaining local funding, reservations
and owned fills in its own append-only ledger.
