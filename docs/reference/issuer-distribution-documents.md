# Issuer distribution documents

[ADR-0003](../adr/0003-issuer-distribution-coverage.md) selects issuer-published
documents as the independent corporate-action source for issue #282. This page
covers step 1: acquiring, retaining and parsing those documents. It does not
reconcile them with Alpaca, establish coverage or apply any economic effect.

## Method

Method `ssga-spdr-historical-distributions-xlsx-v1` reads the fund sponsor's
family-wide historical distributions workbook. The method fixes the URL:

```text
https://www.ssga.com/library-content/products/fund-data/etfs/us/spdr-etf-historical-distributions.xlsx
```

A collection spec selects the method and one instrument; it cannot choose a URL,
supply bytes or assert coverage:

```json
{
  "id": "spy-2026-10-30",
  "method": "ssga-spdr-historical-distributions-xlsx-v1",
  "symbol": "SPY",
  "cusip": "78462F103",
  "access_ref": "public-issuer-document"
}
```

## Commands

```sh
./bin/mantik issuer-document-collect --journal /absolute/private/issuer-journal --spec spec.json
./bin/mantik issuer-document --journal /absolute/private/issuer-journal --collection-id spy-2026-10-30
```

`issuer-document-collect` performs at most one GET per collection identity.
Redirects are refused, the body is bounded to 4 MiB, and any status other than
200 records a failure. A completed identity returns its retained status without
fetching again; a changed spec under the same identity conflicts. A failed or
interrupted identity is never retried: use a new identity. No service, scheduler
or provider credential is involved.

`issuer-document` inspects a retained collection. Both commands print the
derived status and parse, never the original file.

## Retention

The journal follows the [capture journal's](alpaca-journal.md) private rules: an
absolute directory owned by the current user with mode 0700, regular 0600 files,
write-once publication and one exclusive owner. Each collection retains its
intent, the original bytes and a completion or failure receipt. The completion
binds the request URL, HTTP status, content type, byte count and SHA-256 to the
intent's hash. Reads verify the chain; altered bytes or receipts fail.

The sponsor's terms prohibit reproduction without consent. The user approved
private retention for internal research only. Keep journals out of Git, public
exports and documentation. Tests use manufactured workbooks.

## Parsing

Every read rederives the parse from the retained bytes. The workbook must have
exactly one sheet, `dividend`, reached through its workbook relationship, with
the exact header row `FUND NAME`, `TICKER`, `CUSIP`, `EX-DATE`, `RECORD DATE`,
`PAYABLE DATE`, `DIVIDEND ($)`, `SHORT TERM CAPITAL GAIN ($)`,
`LONG TERM CAPITAL GAIN ($)`, `FREQUENCY`. Decompressed parts are bounded.

Rows are selected by ticker and CUSIP. A CUSIP under another ticker, the ticker
with another CUSIP, a duplicate ex-date, a date not in `MM/DD/YYYY`, an amount
that is not a plain non-negative decimal, or a row with no amount makes the
document `unparseable`; no partial selection is returned. Amounts keep the
issuer's exact decimal text, and an empty component stays empty rather than
becoming zero. Dates are reported as ISO dates, sorted by ex-date.

A row whose ex, record and payable dates are out of order stays in the result
with status `inconsistent_dates`. The sponsor's own workbook contains such rows;
a 2026-10-30 retrieval listed SPY's 2006-06-16 payable date before its record
date. A later coverage owner cannot treat a window containing that row as
documented. An unparseable document stays retained with its parse error.

The result records `as_of_stated: false` and `splits_documented: false`: the
workbook states no as-of date and says nothing about splits.
`coverage_complete`, `economic_application_ready` and `provider_attested` remain
false. Service custody, reconciliation and economic application are later steps
in ADR-0003.
