# ADR-0003: Issuer-documented distribution coverage

Status: accepted for steps 1–3; split source deferred
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

Treat issuer coverage as a per-instrument, per-window result derived by the
owner from retained sources. Callers cannot assert coverage, rows or matches.

1. **Supported scope.** Coverage applies only to instruments with a registered
   issuer document method. The first method parses the sponsor workbook for one
   ticker and CUSIP. Other instruments remain `unsupported_instrument`.
2. **Custody.** Retain the original document bytes, retrieval URL, retrieval
   time and hashes in a bounded, append-only owner, following the corporate-source
   custody rules: exact retries, no rewriting, later revisions kept separately.
   Only an owner-configured acquisition journal, as for Alpaca imports, can supply
   coverage-eligible documents. Caller-reported documents stay inspectable and
   unqualified.
3. **Parsing.** Decode the workbook with the standard library. Keep every amount
   as its exact decimal string. Select rows by ticker and CUSIP; a duplicate
   ex-date, malformed date or amount, or a changed column header makes the
   document `unparseable` rather than partially used.
4. **Cash-distribution coverage for a window.** The window is covered only when:
   - the document was retained after the window end and before the assessment;
   - it lists an ex-date strictly after the window end, showing the listing
     extends past the window despite the missing as-of date;
   - every issuer distribution with an ex-date in the window matches exactly one
     Alpaca cash-dividend record in the assessment's reconciled page chain on
     ex-date and total amount, and every such Alpaca record matches an issuer row.

   Any unmatched, ambiguous or extra record leaves the window `unreconciled`,
   with both sides retained for inspection.
5. **Splits.** Until a documented split source is selected, split coverage is
   `split_documentation_unavailable`. Positive economic evaluation therefore
   remains unreachable, and the assessment says why.
6. **Economic application** of reconciled distributions to positions, cash and
   total-return benchmarks is a separate step. It uses ex-date entitlement and
   payable-date cash, with no rounding beyond the retained precision.

## Delivery

Each step is a reviewable PR that preserves legacy records and hashes:

1. Issuer document acquisition, custody and parsing.
2. A prospective recipe whose assessment retains per-window distribution
   coverage and reconciliation, with split coverage marked unavailable.
3. Total-return application of reconciled distributions to portfolio and
   benchmark accounting.
4. A documented split source, after the open decision below.

## Decisions recorded

On 2026-10-30 the user chose:

- **Split source: deferred.** Deliver steps 1–3 now. Split coverage stays
  `split_documentation_unavailable`, so positive economic evaluation remains
  unreachable until a documented split source is selected in a later decision.
- **Retention: private only.** Private retention of the sponsor workbook for
  internal research is accepted. Originals never enter Git, public exports or
  documentation; tests use manufactured bytes.

## Consequences

Coverage stays narrow: one instrument family, cash distributions first.
Holdout performance, sensitivity and benchmark checks can consume the coverage
result and fail closed on anything else. No inspection endpoint or caller flag
substitutes for the reconciliation, and fixtures never receive qualification
credit.
