# Corporate-action window coverage

[ADR-0003](../adr/0003-issuer-distribution-coverage.md) step 4 decides, for one
instrument and one inclusive ex-date window, whether retained Alpaca, Polygon
and issuer sources agree. Package `internal/corporatecoverage` derives that
result with method `alpaca-polygon-issuer-window-reconciliation-v1`. It reads
only retained, owner-verified sources; a caller cannot supply rows, matches or
a coverage flag. This page describes the derivation; assessment v12
[retains it per declared instrument](paper-evaluation-assessments.md#corporate-action-window-coverage).

A `covered` window is evidence that the sources agree. It applies no dividend
or split to positions, cash or benchmarks, and it grants no admission,
execution or qualification credit.

## Window

```json
{"symbol": "SPY", "start": "2026-04-01", "end": "2026-06-30", "process_lag_days": 45}
```

`start` and `end` bound ex-dates, inclusive, at most 366 days apart. The
window closes at the end of `end` (UTC). `process_lag_days` (0 to 120) is
declared because Alpaca filters corporate actions by process date, and a cash
dividend's process date is its payable date. SPY's June 2026 dividend went ex
on 2026-06-18 and was paid on 2026-07-31; a query over the ex-date window
alone would not return it.

## Sources and their checks

**Alpaca.** One or more terminated query chains, each for exactly this
symbol, acquired after the window closed. Their process-date ranges must tile
`[start, end + process_lag_days]` with no gap. A record repeated across chains
must be byte-identical. Records are read by kind:

- `cash_dividends`: `symbol`, `ex_date` and `rate` (an exact JSON number). A
  `sub_type`, `foreign: true` or a currency other than USD is unsupported.
- `forward_splits` and `reverse_splits`: `old_rate` and `new_rate`.
- Any other kind is unsupported, whatever its dates, because its relevance to
  the window cannot be decided.

Supported records with an ex-date outside the window are ignored.

**Polygon.** One dividend snapshot and one split snapshot for the symbol, each
spanning the whole window and acquired after it closed. Dividends of type `CD`
or `SC` in USD are matched; other types, such as capital gains, are
unsupported. Every in-window Polygon dividend must have a pay date no later
than the end of the Alpaca process span, so a dividend absent from both
providers' answers cannot hide beyond it.

**Issuer document.** Required when a supported issuer method exists for the
instrument; otherwise a supplied document is itself a reason. The document
must parse, be acquired after the window closed and list an ex-date strictly
after the window end, because the workbook states no as-of date. In-window
rows must have consistent dates and no capital gains, and must match the
reconciled dividends one-to-one. The workbook never documents splits.

## Matching

Dividends match on ex-date and exact amount; splits on ex-date and from/to
ratio. Amounts compare as exact rationals, so `1.9035160` equals `1.903516`;
nothing passes through floating point. A key held more than once on either
side is ambiguous.

A window with no actions is covered only when both providers, queried after
it closed, report none in it.

## Result

`status` is `covered` only when `reasons` is empty, and `unreconciled`
otherwise. `reasons` is a sorted set of codes, for example
`alpaca_process_span_gap`, `polygon_dividend_unmatched`,
`dividend_match_ambiguous` or `issuer_document_not_beyond_window`. The result
lists each matched dividend (with Alpaca and Polygon IDs and whether the
issuer row confirmed it), each matched split, the source identities used and
a content hash.
