# Weather probabilities and outcome calibration

Issue #87 adds an opt-in research path for Kalshi daily-high temperature
markets. It does not place orders, size positions, simulate fills or enable a
worker. Forecasts are real provider inputs when collected; the fixed model is
an **unqualified baseline**. Brier scores and buckets measure its calibration
after settlement. They do not establish profitability or forecast accuracy in
advance, and they do not automatically refit the model.

## Supported contracts and inputs

The first version supports next-day `KXHIGHCHI` (Midway, NOAA station `KMDW`,
climate identifier `CLIMDW`) and `KXHIGHNY` (Central Park, `KNYC`, `CLINYC`).
It verifies the event date and station identifier in the venue's daily-maximum
Fahrenheit rules. Unsupported series, custom strikes, partial current days,
closed markets and markets with a result are refused. Minimum-temperature
markets and other stations need a separately reviewed model version.

Inputs come from fixed public HTTPS endpoints, without credentials:

1. The Kalshi market response supplies identity, rules, close time and numeric
   strike bounds. The model never guesses a strike from the ticker or title.
2. NOAA station metadata supplies the forecast coordinates. `/points` supplies
   the station's hourly forecast path; only NOAA's documented gridpoint hourly
   path can be followed.
3. NOAA hourly Fahrenheit temperatures must cover the current hour and the
   entire target day, without missing, duplicated or overlapping target hours.
   The forecast generation time must precede collection and be at most six
   hours old.
4. The same NOAA station's latest Celsius observation is converted to
   Fahrenheit. Missing temperatures, future observations and observations more
   than two hours old are refused.
5. Open-Meteo supplies `temperature_2m_max` for the exact target date and
   Fahrenheit unit. Missing, null, wrong-date and wrong-timezone values fail.

The target forecast day uses the city's civil timezone: `America/Chicago`
or `America/New_York`, checked against NOAA's point metadata. Hourly coverage
follows local midnight to the next local midnight, including 23-hour and
25-hour daylight-saving transition days. The day comes from the market event
date rather than an inference from its close time. NOAA station temperatures and gridded
forecasts can differ from the venue's final resolution source. The venue's
current rules identify The Weather Company; NOAA observations are input
context, never substituted for that final result.

## Model and persistence

`daily-high-normal-v1` models the daily high with a normal distribution in
Fahrenheit. Let `N` be the maximum NOAA hourly forecast over the target day,
`O` Open-Meteo's daily high, `S` the station's latest temperature, and `H` the
NOAA forecast for the current hour:

```text
mean = (N + O) / 2
sigma = sqrt(3^2 + ((N - O) / 2)^2 + ((S - H) / 2)^2)
```

The 3°F uncertainty floor is a declared baseline parameter, not an estimated
error rate. Forecast disagreement and station/grid disagreement widen its
uncertainty. The two forecasts are not assumed to be independent ensemble
members. Integer Fahrenheit resolution uses a half-degree continuity
correction: `greater 70` means at least 71 (boundary 70.5), `less 70` means
at most 69 (69.5), and `between 70 and 72` includes 70, 71 and 72
(boundaries 69.5 and 72.5). Noninteger and unsupported bounds are refused.

Every input retains its complete original response, URL, SHA-256 and local
observation time. NOAA's generation/observation clocks are also validated.
Open-Meteo's current endpoint does not supply a model issue time here; its
availability clock is the local response collection time. Collection does not
invent forecast vintages or reconstruct historical inputs from current APIs.
`AsOf` is assigned after collection; no input may have a later observation.
The model therefore supports forward collection, not historical backfilling.

The existing private raw journal fsyncs each bounded HTTP body before parsing,
including malformed, partial and error responses. Capture errors stop the
command. The same 1 MiB response limit, ten-second request timeout and refusal
to follow redirects apply to weather reads.

Schema 119 adds append-only prediction and settlement tables. The prediction
payload includes the complete model inputs and output; its SHA-256 is the
prediction ID. PostgreSQL assigns `RecordedAt` and enforces
`AsOf <= RecordedAt < CloseTime`. The command returns a prediction only after
its transaction commits. Backdating a caller-supplied clock cannot admit a
new late prediction. Exact retries retrieve the original receipt, including
after close; conflicting contents are refused. Forecast reruns create new
observations and retain their own predictions.

Settlement requires a venue `finalized` status, a binary `yes`/`no` result and
a settlement timestamp after close and no later than collection. Canceled,
voided, preliminary and fractional outcomes remain unscored. A settlement
without a stored prediction is refused. Polling the same final result keeps
the first response; a conflicting result or settlement time fails. Updates,
deletes and truncation are blocked on both tables.

## Commands and reports

Build with `mise exec -- make build`. All three commands accept an isolated
Mantik `--database-url` or `DATABASE_URL`. The two provider commands also
require `--journal` (absolute path, private mode 0700) and `--market-ticker`.
Their full help is available through:

```sh
./bin/mantik kalshi-weather-predict --help
./bin/mantik kalshi-weather-settle --help
./bin/mantik kalshi-weather-calibration --help
```

Run `kalshi-weather-predict` with a supported next-day ticker obtained from
`kalshi-collect`. After the venue finalizes that same ticker, run
`kalshi-weather-settle`. Run `kalshi-weather-calibration` to report retained
outcomes without any network request. There is no automatic forecast or
settlement scheduler in this slice.

Calibration accepts `--model-version` (defaults to `daily-high-normal-v1`)
and an optional RFC3339 `--as-of` cutoff. Only predictions stored by the cutoff
and final results both observed and stored by it are visible. For each market
and model version, the **earliest stored prediction** is the scoring unit;
recorded-time ties use the prediction ID. This prevents frequent polling from
overweighting a market. Every later prediction remains available in storage.

The report includes prediction, settled and pending counts, mean Brier score
`mean((p - outcome)^2)`, and ten fixed probability buckets: `[0, 0.1)` through
`[0.9, 1]`. Each bucket reports count, mean prediction, observed yes frequency
and mean Brier score. Empty buckets and an aggregate with no settled outcomes
have null metrics. Versions are not pooled. Reports do not mutate predictions
or fetch newer provider data.

Keep both new tables and the existing source tables together in backups.
Migration is additive; an older binary refuses schema 119. Rollback needs a
compatible binary or an isolated restored database, not deletion of evidence.
Raw captures, local databases and generated reports remain outside Git.

Verification uses loopback provider fakes, known numerical calibration cases,
and disposable PostgreSQL contracts for pre-close admission, exact/concurrent
retries, immutable evidence and cutoff visibility. These tests are synthetic
verification evidence, not measured calibration on settled real markets.

Provider references: [NOAA API documentation](https://www.weather.gov/documentation/services-web-api),
[Open-Meteo daily forecast fields](https://open-meteo.com/en/docs), and
[Kalshi market fields and results](https://docs.kalshi.com/api-reference/market/get-market).
