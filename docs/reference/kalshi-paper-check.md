# Retained Kalshi paper settlement check

`scripts/kalshicheck` joins the existing public weather forecast collector,
recorded-depth notional book, official binary settlement validator and outcome
calibration. It makes public GET requests only. It has no venue credentials,
order endpoint, Alpaca connection or SPY return field.

This is an **explicit one-contract diagnostic taker trade** in a separate $100
notional book. The unchanged maker policy runs first and abstains when its
20-settled-market calibration bucket is unavailable. The diagnostic is not a
maker fill, calibrated strategy decision or automatic taker fallback. No limit
or evidence threshold is lowered to manufacture a strategy trade.

## Commands and custody

- `-action start` requires a new absolute private `-root`, supported next-day
  `-ticker`, explicit `-side yes|no` and dollar `-limit`. Original public
  responses are fsynced before parsing. The forecast is retained before close,
  the operator order clock follows that receipt and event-fee capture, and the
  modeled taker execution consumes a strictly later actual book capture.
  Exactly one contract must fill; an uncrossed limit or empty executable depth
  returns `diagnostic_requires_one_filled_contract` and cannot produce an
  accepted opening or completed settlement report.
- The immutable `input.json` includes the complete original forecast receipt,
  normalized/raw market and order-book records, series/event fee evidence,
  order and execution clock. It is bounded to 8 MiB. Forecast content IDs
  retain their original compact JSON bytes; pretty-printing a nested RawMessage
  would invalidate them. Exact original HTTP bytes remain in `captures/`.
- Local admission keeps attempted bytes under `prediction-attempt.json` and
  publishes `prediction.json` only after durability and cutoff checks. A cutoff
  crossed during publication removes only the newly published link, retaining
  the non-admitted attempt. Failed admission never supplies an executable input.
- `-action show` requires the exact `-input-sha256`. Without a database flag it
  is fully offline: it reconstructs a fresh book from the immutable source
  records and replays the original order once. It does not fetch a new quote,
  create another order, or overwrite the input.
- `-action admit -database-env` mirrors that original forecast into a named,
  explicitly isolated PostgreSQL database **at the current server admission
  clock**, while the market remains open. Its canonical receipt is retained
  separately. It does not pretend the earlier local diagnostic had SQL
  admission at that time. No default development/production URL is selected.
- `-action settle` requires the input pin. It accepts only Kalshi's final
  binary result for the original ticker, never a NOAA observation or inferred
  result. It replays the original fill and settles the separate book, then
  scores the original forecast. Repeating the same retained settlement cannot
  pay again. A changed result fails its source checks.
- With the explicit database flag, settlement additionally requires the
  canonical prediction to exist already. It will not insert a missing late
  prediction. It retains the official outcome through the existing immutable
  weather tables and emits the actual canonical calibration readback. SQL
  retry returns the original settlement observation rather than a later poll.

Local financial replay and canonical PostgreSQL admission are separate
receipts. An offline read emits a deterministic financial report. An explicit
database read also reports that store's current calibration cutoff. There is
no new schema, migration, daemon or indefinitely running worker.

Reports include original prediction ID, maker abstention, source-bound fill and
fees, remaining inventory, official settlement and Brier/bucket calibration.
They carry `prospective_market_unqualified`, `QualificationCredit: false`, and
`IncludedInSPY: false`. Hashes bind retained bytes, not independent authenticity.
One settled forecast measures one result; it cannot satisfy a 20-sample bucket
or qualify the model.

## November 16, 2026 check: pending settlement

The retained contract is `KXHIGHCHI-26NOV17-B57.5`. The actual pre-close forecast
was retained at 01:01:36 UTC on November 17 (November 16 in Chicago). A separate
later order-book observation at 01:01:36.907765 UTC modeled one YES contract at
$0.73 with $0.0138 conservative fees. The $100 notional book has $99.2562 cash,
one contract and no payout. No venue order or actual venue fill occurred.
Maker output is `calibration_unavailable`, with no maker intent.

The same original forecast was subsequently admitted into a **new isolated**
PostgreSQL 17.11 database at 01:22:08.886699 UTC. Its canonical calibration
contains one pending prediction and no settled score. Both original local and
later SQL receipt clocks are retained. The private dump was readable, and the
owned loopback-only container is stopped with its volume and dump retained.

Kalshi's captured close is November 18 at 07:00 UTC (01:00 Chicago); expected
expiration is November 18 at 20:00 UTC (14:00 Chicago), with a later possible
maximum. Official final settlement remains **unobserved**. #56 must remain
open until the separate payout/inventory and canonical calibration readbacks
are actually observed. The first development attempt exposed JSON indentation
changing a forecast's content ID; that failed attempt remains retained and
supplies no accepted fill or calibration credit.

Retained root:
`/home/onnwee/.local/state/mantik-epic51-56-20261116/weather-chi-nov17-v2/`.
Its immutable input pin is
`05af8c573d3379e96ba390a54e21c2c427e3edd3bac0853dc3cd743a7e81a314`.

```sh
root=/home/onnwee/.local/state/mantik-epic51-56-20261116/weather-chi-nov17-v2
mise exec -- go run ./scripts/kalshicheck -action show -root "$root" \
  -input-sha256 05af8c573d3379e96ba390a54e21c2c427e3edd3bac0853dc3cd743a7e81a314
```

See [weather source/admission rules](kalshi-weather.md),
[notional execution](kalshi-simulation.md), and
[the unchanged maker policy](kalshi-maker.md). Synthetic tests cover YES/NO
settlement, source tampering, no-later-book rejection, replay without duplicate
payout, content-address preservation and publication crossing the close clock.
