# Retrospective income-option research

This offline command runs covered-call and cash-secured-put recipes through
the existing cash OptionBook on archived Alpaca daily prices. It implements
the owner's November 16 acceptance choice for #101: preserve the real
acquisition clocks and make historical availability an explicit assumption.
It has no broker, database, model, trial-registration or qualification client.

The fixed policy is `retrospective-final-snapshot-close-plus15m-v1`:

- Each supplied daily bar is assumed known 15 minutes after its retained
  New York calendar close. No later session's OHLC can enter an earlier cut.
- Archived reference metadata is assumed known only from the first supplied
  option bar for that symbol. It does not establish historical listing dates,
  complete chain membership or freedom from selection bias.
- The archives are final snapshots acquired later. Corrections and listing
  survivorship are unresolved. Actual request URLs, body hashes and receipt
  times remain in the output, and exact bodies remain in the pinned input.
- Scenario clocks exist only in detached in-memory records inside this
  command. The ordinary point-in-time store and its acquisition clocks are
  unchanged.
- All results are `retrospective_indicative_unqualified` with
  `QualificationCredit: false`. Neither these results nor their internal
  chains are operational or gate evidence.

The owner approval is specific to retrospective research. Natural forward
qualification, real paper fills and #85/#107 acceptance remain separate.

## Run

On Kvant, an exact retained scenario can be replayed with:

```sh
manifest=/home/onnwee/.local/state/mantik-wave11-options-research-20261116/2026-cash_secured_put-input.json
mise exec -- go run ./research/options \
  --input "$manifest" \
  --input-sha256 "$(sha256sum "$manifest" | cut -d ' ' -f 1)"
```

Use `--help` for discovery without reading an archive. The input is limited to
32 MiB, 128 pages of at most 1 MiB each and 50,000 market rows. The command
requires complete, nonpaginated response envelopes. A larger capture must
first be split into explicit bounded research scenarios; do not drop a page
token and call an incomplete response complete. Generated inputs and reports
stay outside Git.

The manifest fields are:

| Field | Meaning |
| --- | --- |
| `Policy` | The exact policy above; no implicit selection. |
| `AssetID`, `Underlying` | Explicit stable scenario identity and provider symbol. |
| `Recipe` | Complete `library.OptionConfig`; only `covered_call` and `cash_secured_put` are permitted. |
| `SettledCash`, `PaidShares` | Declared simulation dollars and fully paid collateral shares. Puts require zero imported shares. These are never inferred from a broker. |
| `Assumptions` | Explicit `options.Assumptions`: rate, dividend yield, maximum age in nanoseconds. |
| `FeePerContract`, `SlippageBps` | Positive fees and adverse fill-model slippage, validated by OptionBook. |
| `Times` | Increasing explicit decision opportunities, at most 10,000. No session is manufactured. |
| `Pages` | Source records with `Kind`, `URL`, actual `ReceivedAt`, `SHA256` and exact original response `Body` as a JSON string. |

Page kinds are `contracts`, `calendar`, `stock_bars` and `option_bars`. They
bind the existing Alpaca contract/calendar and daily stock/option endpoints.
Stock bars must request `adjustment=raw`; adjusted closes cannot substitute.
Hash mismatches, duplicate bars/references/sessions, absent calendar coverage,
unrequested symbols and market data acquired before the session completed
are refused. Hashes establish byte identity, not independent provider
authenticity. Keep the original capture receipts with the manifest.

Reports include the original page metadata, explicit assumptions, calendar
coverage, missing underlying/option dates and the complete OptionBook event
trace. Only supplied contracts compete. Decisions can hold, refuse, cancel an
untouched order or end with an open liability. `Complete` means no outstanding
option exposure/reservation; it does not mean a trade occurred. Errors after
initial validation emit partial evidence and return a failing process status.

## Real archived-price exercise, November 16, 2026

Six scenarios ran against bounded February SPY samples from 2024, 2025 and
2026. The samples are **not a continuous 2024–2026 portfolio backtest**. They
use the recipe defaults except a one-contract ceiling, $20m declared settled
cash, $0.65 per contract per side, five basis points adverse slippage and
European BSM assumptions of 4% rate / 1% yield. Covered calls additionally
import 100 paid SPY shares. This intentionally ample research capital is
unrelated to the owner's $2,000 capped paper book.

The explicit opportunity timeline intersects actual underlying and both
option series; missing dates remain in report coverage. The 2024 option
series omit February 2. No bar or historical membership was filled in.

| Sample | Recipe | Opening / closing fills | Horizon |
| --- | --- | --- | --- |
| February 1–16, 2024; 11 opportunities | Covered call | 2 / 1 | One short call remains covered by 100 paid shares; $99,456 reserved; incomplete. |
| Same 2024 sample | Cash-secured put | 0 / 0 | Opening limits never reached; untouched order cancelled, reserve released. |
| February 3–14, 2025; 10 opportunities | Covered call | 1 / 1 | Closed; no option position, pending order or reserve. |
| Same 2025 sample | Cash-secured put | 0 / 0 | Opening limits never reached; untouched order cancelled, reserve released. |
| February 2–17, 2026; 11 opportunities | Covered call | 0 / 0 | Opening limits never reached; untouched order cancelled, reserve released. |
| Same 2026 sample | Cash-secured put | 1 / 1 | Closed; no option position, pending order or reserve. |

The initial 2025/2026 opportunity also reports an incomplete eligible chain:
the retained expiry is 46 days away, outside the default 45-day ceiling.
Limits and the DTE window were left unchanged after those refusals. The
covered-call 2025 round trip debits $109.75 net cash; the put 2026 round trip
credits $9.2225. Imported stock returns and outstanding 2024 option marks
are separate from those cash effects. These tiny selected samples establish
the retrospective cash-book execution path, not an expected return.

Original pages, six pinned manifests, six full reports and the summary are
retained privately under
`/home/onnwee/.local/state/mantik-wave11-options-research-20261116/`.
The tests use separate synthetic data to verify next-session execution,
unchanged original receipt clocks, byte-identical replay, cash collateral,
gap/error handling and liability retention at a truncated horizon.

The indicative source is provider-default-unverified, not OPRA execution.
Missing strikes, market impact, assignment/exercise and complete corporate
action history are not reconstructed. No profitability, readiness, admission
or qualification is claimed.
