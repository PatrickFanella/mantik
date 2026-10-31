# Polygon corporate-action references

[ADR-0003](../adr/0003-issuer-distribution-coverage.md) selects Polygon reference
data as the market-wide source reconciled against Alpaca's corporate actions.
This page covers step 2: acquiring, retaining and parsing Polygon dividend and
split queries. It does not reconcile them, establish coverage or apply any
economic effect. [Service custody](corporate-reference-custody.md) covers step 3.

## Methods

| Method | Endpoint | Date filter |
| --- | --- | --- |
| `polygon-reference-dividends-v3` | `/v3/reference/dividends` | `ex_dividend_date` |
| `polygon-reference-splits-v3` | `/v3/reference/splits` | `execution_date` |

A spec selects a method, one instrument and an inclusive date range of at most
366 days. It cannot choose a URL, supply a response or assert coverage:

```json
{
  "id": "spy-dividends-2026",
  "method": "polygon-reference-dividends-v3",
  "symbol": "SPY",
  "start": "2026-01-01",
  "end": "2026-10-30",
  "access_ref": "polygon-reference-account"
}
```

The first request is fixed by the method: the ticker, both date bounds,
ascending order on the date field and `limit=1000`.

## Commands

```sh
./bin/mantik polygon-corporate-collect --journal /absolute/private/polygon-journal \
  --spec spec.json --credentials-file /absolute/private/polygon.json
./bin/mantik polygon-corporate-collection --journal /absolute/private/polygon-journal \
  --collection-id spy-dividends-2026
```

The credentials file is a current-user regular file with mode 0600 containing
`{"api_key": "..."}`. Its value is added to each request at send time and is
never printed or written to the journal; retained URLs are keyless. The
operator chose to reuse the Polygon account that Augr already uses, so Mantik
requests share that account's rate limit. Mantik reads only this file, never
Augr's configuration or services.

One collection follows a complete query. A `next_url` continuation is followed
only when it stays on `https://api.polygon.io` and the same path and carries
only a `cursor`. Pages are paced 13 seconds apart and bounded to 32, each body
to 4 MiB. Redirects are refused. Any non-200 status, including 429, an invalid
envelope or an escaping continuation, records a failure without retrying. A
completed identity returns its retained status without fetching again; a
changed spec under the same identity conflicts; a failed or interrupted
identity needs a new identity.

## Retention

The journal uses the same private directory rules as the
[issuer document journal](issuer-distribution-documents.md): an absolute 0700
current-user directory, 0600 write-once leaves and one exclusive owner. Each
collection retains its intent, every page's exact body with a receipt chained to
the previous page, and a completion or failure receipt. Reads verify the chain
and that each page's request URL is the continuation the previous body named;
an altered body or receipt fails.

## Parsing

Every read rederives the snapshot from the exact retained bodies. Each page must
be one JSON object with `status: "OK"` and a `results` array, and only the last
page may omit `next_url`. Numbers are decoded as exact JSON number text, never
through floating point, and quoted numbers are rejected.

Dividends keep `id`, `ex_dividend_date`, `cash_amount`, `currency`,
`dividend_type` and any declaration, record and pay dates. Splits keep `id`,
`execution_date`, `split_from` and `split_to`. A record for another ticker, a
date outside the requested range, a duplicate ID, a non-positive or
non-decimal amount, or a malformed date makes the snapshot `unparseable`; no
partial selection is returned. Records are sorted by date, then ID.

`provider_attested`, `coverage_complete` and `economic_application_ready`
remain false. A snapshot reports what Polygon returned for one query. Service
custody, reconciliation with Alpaca and issuer documents, and economic
application are later steps in ADR-0003.
