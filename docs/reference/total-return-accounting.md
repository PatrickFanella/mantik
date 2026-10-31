# Total-return accounting

[ADR-0003](../adr/0003-issuer-distribution-coverage.md) step 5 applies
reconciled dividends and splits to paper portfolio accounting.
`papereconomics.EvaluateTotalReturn` (method
`usd-counterfactual-observed-bid-reconciled-total-return-v1`) replays the same
verified entry and exit fill books as the
[observed-equity accounting](paper-evaluation-assessments.md) and applies a
list of reconciled actions for the book's symbol. It is a pure function: it
reads no source, writes no record and grants no admission, execution or
qualification credit. Assessment v13
[uses it](paper-evaluation-assessments.md#reconciled-total-return-benchmarks)
to decide `benchmark_comparison`.

## Input

The fill-book input is unchanged. The actions cover one symbol and an inclusive
ex-date window:

```json
{
  "symbol": "SPY",
  "window_start": "2026-10-01",
  "window_end": "2026-10-31",
  "actions": [
    {"kind": "cash_dividend", "ex_date": "2026-10-21", "payable_date": "2026-10-31", "amount": "1.903516"},
    {"kind": "split", "ex_date": "2026-10-28", "from": "1", "to": "2"}
  ]
}
```

Amounts are USD per share and ratios are `from` old shares to `to` new shares,
all as exact positive decimals (exponent notation such as `19035.16e-4` is
exact and accepted; fractions are not). The intended source is a covered
[window coverage](corporate-window-coverage.md) result; the function does not
check coverage itself. `Actions.Validate` checks the actions alone, before any
book is supplied. Malformed input is an error: an unknown kind, an ex-date
outside the window, a payable date before the ex-date, a non-decimal amount, a
symbol other than the book's or more than 64 actions. Several dividends on one
ex-date all apply, since their order does not matter. A split's order relative
to another action on its ex-date is undocumented, so that case returns status
`same_ex_date_split_and_other_action_unsupported` with no values rather than a
guess.

## Rules

- **Clock.** An ex-date or payable date takes effect at 00:00 New York time on
  that date. Actions take effect before fills at the same instant.
- **Dividends.** Shares held at the ex-date boundary are entitled. The
  entitlement (shares × amount) is a receivable from the ex-date and moves to
  cash at the payable date. A missing payable date leaves it receivable. Either
  way it counts in equity from the ex-date, when the price drops by the
  dividend.
- **Splits.** Each open lot's quantity is multiplied by `to/from` at the
  ex-date. Cost basis is unchanged, so the basis per share changes.
- **Sales.** A sale closes basis in proportion to the shares sold. Exit order
  quantities were fixed before any action; a sale larger than the
  split-adjusted position stops with status
  `fill_exceeds_split_adjusted_position`.
- **Coverage.** Every ex-date boundary crossed while any shares are held must
  fall inside the window, since an action on an uncovered date would be
  unknown. Otherwise the status is `holding_outside_corporate_coverage_window`.
  Dates crossed with no position need no coverage.
- **Terminal mark.** Equity at the cut is cash plus receivables plus held
  shares at the observed bid, under the same freshness rule as observed equity
  (`terminal_mark_unavailable`). A mark observed before the latest applied
  action is in pre-action units and still includes the dividend, so it yields
  `terminal_mark_predates_corporate_action`.

Actions with an ex-date at or before the first order placement, or after the
cut, cannot affect the book and are not applied.

## Result

`status` is `observed_total_return` only when terminal equity was derived; then
`equity_cents` and `net_cents` (equity minus opening cash) are present. The
result always lists applied actions (boundary, quantity before and after,
entitlement and payment time), ending cash, dividend receivable, remaining
basis, realized net on closed shares and held quantity.

Values are exact rationals. They render as a terminating decimal, such as
`380.7032` cents for 2 shares × USD 1.903516, or as a reduced fraction, such as
`2/3` shares after a 3-for-1 reverse split. Nothing is rounded. With no actions,
equity equals the observed-equity result for the same books.
