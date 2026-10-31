# ADR-0003: Independent corporate-action coverage

Status: accepted
Date: 2026-10-30

## Context

Issue #282 requires documented corporate-action coverage and reconciliation
before any positive economic evaluation. The user kept the strict requirement:
Alpaca's retained as-reported corpus and terminal pagination alone are not
coverage. Without coverage, holdout performance, sensitivity and total-return
benchmarks can neither pass nor reliably fail, because an unrecorded dividend or
split changes both the strategy and the benchmark result.

The user selected issuer-published documents as the independent source. For
SPY, the fund sponsor publishes a family-wide historical distributions workbook.
A retrieval on 2026-10-30 found one `dividend` sheet with the columns `FUND NAME`,
`TICKER`, `CUSIP`, `EX-DATE`, `RECORD DATE`, `PAYABLE DATE`, `DIVIDEND ($)`,
`SHORT TERM CAPITAL GAIN ($)`, `LONG TERM CAPITAL GAIN ($)` and `FREQUENCY`.
It held 136 SPY rows from 1993 to an ex-date of 2026-09-18. Three properties
constrain the design:

- The workbook states no as-of date. An absent row can mean "not yet declared"
  as well as "no distribution".
- The workbook and the sponsor's site, as searched, document no splits. A
  distribution list cannot show that a split did not occur.
- The site prohibits reproduction without consent. Retained originals stay in
  private custody, never in Git or public exports. Tests use manufactured bytes.

## Decision

Treat coverage as a per-instrument, per-window result derived by the owner from
retained sources. Callers cannot assert coverage, rows or matches. Two
independent sources can document a window:

- **Polygon** reference data is the market-wide second provider. It reports cash
  dividends (exact amount, ex/record/pay/declaration dates, type, stable record
  ID) and splits (execution date, from/to ratio) for any listed instrument.
- **Issuer documents** are an additional check where a supported issuer method
  exists, starting with the SPDR sponsor workbook.

1. **Supported scope.** Any instrument Polygon reports can be reconciled. Where
   a supported issuer method also exists, its document must agree too.
2. **Custody.** Retain original Polygon responses and issuer document bytes,
   with request scope, retrieval time and hashes, in a bounded, append-only owner, following the corporate-source
   custody rules: exact retries, no rewriting, later revisions kept separately.
   Only an owner-configured acquisition journal, as for Alpaca imports, can supply
   coverage-eligible sources. Caller-reported documents stay inspectable and
   unqualified.
3. **Parsing.** Decode the workbook with the standard library. Keep every amount
   as its exact decimal string. Select rows by ticker and CUSIP; a duplicate
   ex-date, malformed date or amount, or a changed column header makes the
   document `unparseable` rather than partially used.
4. **Coverage for a window.** A window is covered only when:
   - the Alpaca and Polygon sources were acquired after the window end and
     retained before the assessment, with every page of each query complete;
   - every Alpaca cash dividend with an ex-date in the window matches exactly one
     Polygon cash dividend on ex-date and exact amount, and every Polygon one
     matches an Alpaca record;
   - every split in the window matches on execution date and ratio, in both
     directions;
   - where a supported issuer document exists, it was retained after the window
     end, lists an ex-date strictly after the window end (the workbook states no
     as-of date), and its in-window rows match the reconciled dividends; a row
     marked `inconsistent_dates` in the window prevents coverage.

   A window with no actions is covered only when both providers, queried after
   it closed, report none. Any unmatched, ambiguous, extra or unsupported record
   (including action types other than cash dividends and splits) leaves the
   window `unreconciled`, with every source retained for inspection.
5. **Splits** are documented by the Polygon/Alpaca reconciliation. The issuer
   workbook documents no splits and does not contribute to split coverage.
6. **Economic application** of reconciled dividends and splits to positions, cash and
   total-return benchmarks is a separate step. It uses ex-date entitlement and
   payable-date cash, and applies split ratios to share quantities, with no
   rounding beyond the retained precision.

## Delivery

Each step is a reviewable PR that preserves legacy records and hashes:

1. Issuer document acquisition, custody and parsing.
2. Polygon corporate-action acquisition and parsing into a private journal.
3. Service custody and import of both source kinds from configured journals.
4. A prospective recipe whose assessment retains per-window coverage and
   reconciliation across Alpaca, Polygon and any supported issuer document.
5. Total-return application of reconciled dividends and splits to portfolio
   and benchmark accounting.

## Decisions recorded

On 2026-10-30 the user chose:

- **Split source: deferred**, then resolved the same day by the next decision.
- **Second provider: Polygon.** The user asked for broad coverage using an
  existing Augr provider account. Read-only probes on 2026-10-30 found Polygon
  and FMP serving dividends and splits, Alpha Vantage serving dividends and
  Finnhub denying both. Polygon was selected for its market-wide dividends and
  splits, exact amounts and stable record IDs. FMP remains a possible third
  source.
- **Credential: reuse Augr's Polygon key.** The user will place it in a private
  Mantik credentials file (mode 0600, outside Git). Mantik requests share that
  account's rate limit with Augr; acquisition must stay bounded and explicit.
  Mantik never reads Augr's configuration or services.
- **Retention: private only.** Private retention of the sponsor workbook for
  internal research is accepted. Originals never enter Git, public exports or
  documentation; tests use manufactured bytes.
- **Alpaca process dates.** Proposed by the agent and accepted by the user on
  2026-10-30. Alpaca filters corporate actions by process date, which for cash
  dividends is the payable date, not the ex-date. To keep the strict rule, a
  window declares a maximum process lag of at most 120 days. Alpaca chains
  must tile process dates from the window start to the window end plus that
  lag, and every in-window Polygon dividend must be paid within that span.
- **Benchmark acceptance.** Proposed by the agent and accepted by the user on
  2026-10-31. With total return applied, `benchmark_comparison` passes only
  when the strategy's total-return net exceeds both cash and the passive
  benchmark by the plan's `minimum_net_excess_cents` in the base and stress
  models. It fails, blocking the assessment, when any margin falls short, and
  stays unavailable without coverage, a computable total return or a fully
  filled passive entry. Admission, execution and qualification stay false.

## Consequences

Coverage extends to any instrument both Alpaca and Polygon report, with
issuer documents strengthening the instruments they cover. Shared provider
quota limits acquisition volume. Holdout performance, sensitivity and benchmark checks can consume the coverage
result and fail closed on anything else. No inspection endpoint or caller flag
substitutes for the reconciliation, and fixtures never receive qualification
credit.
