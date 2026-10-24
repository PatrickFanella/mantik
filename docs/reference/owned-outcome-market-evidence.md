# Retained outcome market evidence

`GET /v1/paper-outcomes/{id}/market-evidence` provides authenticated,
read-only inspection of the quote observations retained with an actual
service-owned outcome. This stages source inspection for issues #19, #20, #21
and #24. It does not calculate execution costs or complete those issues.

The service replays the complete actual strategy, decision and outcome owners
before selecting the requested outcome and its historical source basis. Memory
uses its aggregate mutex. PostgreSQL uses one read-only repeatable-read
transaction and returns no source result if COMMIT fails. The inspector uses
the retained outcome timestamp; it never consults wall time or acquires data.
There is no new table, journal, write command or offline CLI.

The `mantik-owned-outcome-market-evidence-v1` response pins the receiver,
outcome, basis, finalized decision and collector registration. It retains the
decision finalization, outcome horizon, observation-window end and assessment
timestamp alongside the outcome's existing status.

## Observations and gaps

Each retained `latest_quotes` normalized input contributes a row. Calendar
recordings are validated against their owner but are not projected as quote
rows. A row retains:

- Input kind, ID and SHA256, evidence class, quality and quality reason.
- Capture key and original capture hash.
- Occurrence, receipt, service publication and collector admission timestamps,
  with both publication and admission fences.
- `available_at`, the later of publication and admission.
- Provider-native bid/ask dimensions when the normalized input is a usable quote.

Missing or quarantined quote captures normalize to quality-state inputs. Their
rows remain visible with `quote: null` and
`dimension_status: quote_dimensions_unavailable`. Their state-event clock is
not presented as a quote occurrence: `occurred_at` is zero,
`occurrence_sha256` is empty and `temporal_status` is `timestamp_unavailable`.
A gap cannot silently disappear because it lacks usable dimensions.

An admitted quote or price-coverage input whose original capture was not
published is retained as a `raw_capture_unavailable` row. It pins the admitted
normalized input and admission fence, with no quote dimensions, capture hash,
publication timestamp or availability timestamp. Its unverified capture
provenance does not supply quote occurrence or share-unit claims.

Usable quotes retain one temporal classification:

- `before_finalization`: occurrence precedes the finalized decision.
- `after_horizon`: occurrence follows the outcome horizon.
- `published_or_admitted_after_horizon`: occurrence is within the holding window,
  but its service availability follows the horizon.
- `within_holding_window`: occurrence and service availability are within that
  temporal window, including the exact horizon boundary.
- `decision_abstained`: the finalized decision has no entry candidate.

These classifications describe time only. They do not establish regular-session
eligibility, execution eligibility or complete coverage. Rows are ordered by
known availability, then occurrence, input ID and capture key. Unpublished
rows follow known availability and are ordered by admission time and input ID. Repeated captures of the
same provider/feed/symbol occurrence retain the same occurrence hash and remain
separate rows. Different capture IDs do not establish independent samples.

For an `observed` outcome, exactly one row is marked `selected_forward_quote`.
Its actual input, capture, ask price, occurrence and publication must match
the retained forward observation. Quote-only `available_at` must be no later
than `selected_observation_available_at`, which preserves the outcome
evaluator's availability of all required evidence, including calendar/session
evidence. A calendar can arrive after the quote; these clocks are not merged. This flag identifies the existing
quote-level outcome; it does not identify a fill or an economic return.

## Boundaries and historical readback

`coverage_status` is `retained_observations_only`.
`continuous_coverage_available`, `share_quantities_available`,
`admission_available`, `execution_available` and `qualification_credit` remain
false. Provider-native sizes retain `size_unit: provider_native_unverified`;
there is no conversion to shares. Missing updates, unknown size units,
regular-session path coverage, liquidity assumptions and actual cost-bearing
execution remain unresolved inputs to a future applicable recipe.

An outcome with no retained quote observations reports
`quote_observations_unavailable`. Abstentions report
`not_applicable_abstention`. Other outcomes report
`retained_quote_observations`, including explicit quality gaps. Neither the
status nor a successful read upgrades unqualified evidence.

Later legitimate publications cannot expand an old outcome's basis or change
this readback. Restart preserves the same response and hash. Rehashed fabricated
outcomes, changed source bytes, missing selected captures and future publication
or admission timestamps fail closed. Source selection validates the entire
outcome catalogue, including records other than the requested ID.

The route accepts only the path ID. Query parameters, forced queries, malformed
IDs and request bodies are rejected. Authentication is required. A missing
outcome returns 404; owner replay or inspector failures return 503 without a
partial evidence response.

Verification covers actual Memory/PostgreSQL owner reads, source detachment,
concurrent retries and reads, corruption of selected and unrelated outcomes,
missing/quarantined captures, duplicate occurrence identity, publication delay,
selected forward-quote pins, body/query/authentication rejection, later
publication stability and isolated restart. Manufactured fixtures and passing
checks provide no runtime qualification or performance evidence.
