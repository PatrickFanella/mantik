# Alpaca REST quote share-size contract

Method `alpaca-stock-rest-share-sizes-2025-11-v1` interprets recorded US-equity
bid/ask size fields for the existing `latest_quotes` recording capability.
The service selects this compiled contract after replaying its actual source
owners. No caller-supplied multiplier or unit assertion is accepted.

## Primary source and scope

The [official REST OpenAPI](https://docs.alpaca.markets/us/openapi/market-data-api.json)
connects `GET /v2/stocks/quotes/latest` to `stock_latest_quotes_resp`, whose quote
values reference `stock_quote`. Its `as` and `bs` fields use unsigned 32-bit
integers and describe share counts, with round-lot units before November 3,
2025. The endpoint's feed parameter includes `iex`, `sip` and `delayed_sip`,
which are the feeds supported by Mantik's recording schema. This contract is
limited to that REST capability and those supported feeds.

The reviewed source has SHA-256
`2cd9b000441f24075cc3110580c25747fabd7799caefb0d4dc3c7c69832a4eb0`.
Its response reported Last-Modified `2026-10-16T17:57:10.342Z` when retrieved.
The exact document and retrieval receipt are retained outside Git. The contract
pins this document, its property pointer and method version in each new
projection. A later documentation change requires a deliberate method review.

The [dated provider changelog](https://docs.alpaca.markets/us/v1.1/changelog/marketdata-bid-and-ask-size-display-change)
announces the CTA/UTP transition on November 3, 2025. The generic streaming
schema still describes round lots; that older description is not used to
reinterpret the REST response. The shared current REST schema supplies the
specific field definitions used here.

## Conservative date and range selection

Mantik excludes the entire New York transition date. Eligible quote occurrences
start at `2025-11-04T05:00:00Z`, midnight November 4 in New York. This is a
conservative application boundary, not a claim about the provider's exact
intraday switch time. Receipt time cannot promote an older occurrence.

| Condition | Share-quantity status | Result |
| --- | --- | --- |
| Occurrence before the supported boundary | `outside_documented_share_window` | Original sizes retained; shares unavailable |
| Either size outside 1 through 4,294,967,295 | `outside_documented_uint32_range` | Original sizes retained; shares unavailable |
| Both sizes supported | `documented_share_units` | Bid/ask share counts equal the corresponding reported integers |

There is no multiplication, rounding, saturation or fractional conversion.
Unusable quotes, including fractional, zero or unrepresentable size values,
still fail the original quote inspection. Unknown recording capabilities or
feeds fail the existing recording contract. No historical round-lot conversion
is inferred from a common lot size or from current security classifications.
The fill model's narrower quantity limit remains a separate input constraint.

## Owner binding and authority

`InspectQuoteDimensions` keeps the legacy raw interpretation and representation.
`InspectQuoteShareDimensions` explicitly adds `share_size_contract`,
`share_quantity_status`, `bid_shares` and `ask_shares` to a newly derived read.
Only supported values set `size_unit: shares` and
`share_quantities_available: true`. The full source digest, original sizes,
quote occurrence, receipt time and evidence class remain attached.

Decision and outcome market-evidence v3 routes call the explicit inspector only
after actual owner replay verifies capture, registration, normalized input,
publication, input admission and historical source cuts. These are new versioned
projections of retained bytes. They do not rewrite captures, normalized inputs,
finalized decisions, outcomes or assessments, and they do not retroactively
attest a recording's provider authenticity. Legacy size inspection stays intact.

Documented units establish the interpretation of recorded fields. They do not
establish that liquidity remains available, that a synthetic or recorded claim
is authenticated market data, or that a strategy is admitted. Evidence classes,
continuous-coverage limitations and admission/execution/qualification flags keep
their separate meanings. No financial effect or balance writer is introduced.

Verification covers the date boundary, supported feeds, unsigned range, exact
counts, tampered readbacks, legacy inspection, actual Memory/PostgreSQL source
owners, immutable-record retention, HTTP/MCP/terminal compatibility and gaps.
