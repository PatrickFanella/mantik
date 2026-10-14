# Retained regular-session coverage

`alpaca-coverage` inspects one exact feed, symbol, and evidence class in a private
journal. It performs no provider or service request, creates no timer, submits no
input, and does not configure the global `feed_coverage` readiness capability.
It uses the actual wall clock; the CLI has no virtual inspection-time flag.

```bash
./bin/mantik alpaca-coverage --journal "$MANTIK_CAPTURE_JOURNAL" \
  --feed iex --symbol SPY \
  --evidence-class prospective_market_unqualified \
  --max-quote-age-seconds 60 --max-capture-age-seconds 300 \
  --max-calendar-age-seconds 86400
```

The source journal must exist and have the normal private ownership/permissions.
Inspection owns the process lock for a coherent read; an active collector or
submitter returns a busy error. No credentials are loaded. Corrupt captures or
normalization drift fail inspection instead of producing an optimistic report.

## Exact evidence selection

The default class is `prospective_market_unqualified`; it never adopts recorded
or synthetic fixtures silently. Quote feed, symbol, and class match exactly.
Calendar evidence uses the regular-session adapter for the same symbol/class.
IEX evidence cannot cover SIP, and one instrument cannot cover another.

Inspection selects the latest received quote capture, including unusable or
missing evidence. A newer quarantined response cannot fall back to an older
usable quote. Current quote age must satisfy both the inspection limit and the
capture's original maximum age. A caller can tighten that original bound but
cannot loosen it. Capture receipt age is checked independently, so unchanged
retained prices do not prove continued acquisition. Equal-receipt conflicting
quote values or original age contracts report unknown/unusable coverage. Future
matching receipts block eligibility rather than disguising a clock anomaly.

Calendar selection requires a declared range covering the **current New York
calendar date**, with fresh receipt evidence. A capture of tomorrow's calendar
alone cannot classify today's session. The latest covering calendar governs;
stale evidence, future receipts, or equal-receipt conflicting schedules produce
unknown status. A valid empty calendar for the covered date indicates no declared
regular session, without inventing an open or close. Next-open time is reported
only when present in the selected calendar's retained range.

## Session and price obligations

Session classification comes from retained scheduled times, not an observed
exchange-state feed. Intraday halts, exceptional closures, and extended sessions
are outside this contract.

| Scheduled position | Report | Price requirement |
| --- | --- | --- |
| Before the declared open | `closed`, `before_regular_open` | No regular-session price required |
| At/after open and before close | `open`, `within_regular_session` | Fresh usable quote required |
| At/after the declared close | `closed`, `after_regular_close` | No regular-session price required |
| No session in a valid covered-date calendar | `closed`, `calendar_no_session` | No regular-session price required |
| Calendar missing, stale, future, or conflicting | `unknown` | Required evidence is not satisfied |

Open is inclusive and close exclusive. Time-zone conversion retains the adapter's
DST and early-close behavior. A usable quote must also occur inside the current
regular session to satisfy its price requirement; a fresh pre-open or previous
session quote cannot be admitted by increasing age limits.

The quote section keeps input/capture identities, quality/reason, receipt and
occurrence times, current usability, and regular-session eligibility separate.
A closed scheduled session may satisfy its local calendar requirement while the
latest quote remains quarantined. That does not promote the quote into a price.

## Limits of the report

`required_evidence_satisfied` answers only the declared local regular-session
query. Every report explicitly leaves `collector_liveness_verified`,
`global_readiness_configured`, and `qualification_credit` false. There is no
server heartbeat, entitlement recheck, licensing approval, or current receiver
history check. A file receipt does not prove a process is live. Global readiness
continues to report unconfigured until a separate durable collector/coverage
contract is installed and qualified.

Session classification is read-only. It does not install future obligations,
change research cases, suppress existing operational deadlines, authorize a fill,
or establish profitability. Manufactured test clocks establish deterministic
boundary behavior and receive no elapsed-time or real-provider credit.
