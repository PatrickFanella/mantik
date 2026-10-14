# First bounded real Alpaca feed rehearsal

This checkpoint verifies authorized provider reads, attributed capture retention,
and research routing on a separate Mantik instance. It does not establish a
profitable paper strategy, continuous-feed readiness, execution permission, or
live-money trading. The service's execution mode remains `paper_fixture`; real
market inputs use `prospective_market_unqualified` research provenance.

## Observed checkpoint, October 14, 2026

The owner authorized reuse of the existing Augr Alpaca credential pair. Only that
pair was read from its actual running NUC container and copied into a private
Mantik file. Augr's configured paper mode, PID, and image were checked before and
after; its service and data were preserved. Mantik does not depend on Augr data,
its database, its runtime, or automatic credential discovery.

Bounded read-only probes observed:

| Request | Result | Scope of proof |
| --- | --- | --- |
| Paper `GET /v2/account` | HTTP 200, active USD account | Credentials accepted at the paper endpoint |
| Latest SPY quotes, explicit `feed=iex`, USD | HTTP 200 | Read access to the requested IEX query |
| Latest SPY quotes, explicit `feed=sip`, USD | HTTP 403 | SIP query denied for this credential pair |
| Regular calendar for the declared next date | HTTP 200 | Calendar response usable by the current normalizer |

The account read uses Alpaca's [paper account endpoint](https://docs.alpaca.markets/us/reference/getaccount-1).
IEX and SIP have different coverage and access requirements; a denial does not
identify the account's full subscription or licensing state. See the
[Market Data FAQ](https://docs.alpaca.markets/us/docs/market-data-faq).
Requests did not create or alter an order or account. Account-response bytes,
credentials, access receipts, and provider responses stay outside Git.

The compatible instance used source `c31f5c9dfb0ac8f8c7ba36b647045e9ce68e0308`,
schema 7, an immutable image recorded in its private manifest, and loopback port
41911. The original ports 41890 and 41906 retained their separate pinned services.
The curtailed soak observer remained inactive; no new 24-hour window was started.

Two real captures passed through the committed-image CLI and were submitted to
the pinned research receiver:

- The IEX quote had a zero ask and zero ask size. It was retained as quarantined
  state evidence with `unsupported_price_precision_or_range`, no price value,
  and zero price-trigger matches. Its provider timestamp and original bytes were
  preserved; unusable evidence was not converted into a fill or usable price.
- The regular-session calendar produced one usable `market_session_schedule`
  event and opened one research-routing probe case for SPY. Future session times
  remained schedules, not observed market opens or automatic timer obligations.
- Both inputs were acknowledged with zero pending journal inputs. Repeated
  submission processed zero new inputs, with no additional research match.
- A declared restart of only this new app retained receiver identity, definitions,
  routing inputs, cases, and trigger runtime. Worker/clock metadata was allowed to
  advance independently.
- Journal archive verification and isolated restore passed with both acknowledgements
  and the original binding preserved. The restored copy was not activated.
- A coherent PostgreSQL backup was retained. This checkpoint did not perform an
  application-database restore or off-host retrieval.
- The fixture case remained waiting with zero fixture observations, zero actions,
  and an independently inspected empty economic-effect ledger.

Private completion evidence is `first-real-feed-result.json` beside the owned
runtime manifest. Raw command/API receipts, denied SIP response, and failed
rehearsal-driver attempts are retained there or in the private access directory.
Two driver assumptions were corrected: trigger registration returns HTTP 200,
and the schema table is `mantik_schema`. Actual committed state was inspected
before continuing; those harness failures were not application defects.

## Repeatable operator procedure

1. Verify authorization for the credential pair, intended paper environment,
   requested symbols/feed, and permitted retention/licensing scope. Supply a
   separate private file with only `key` and `secret`, owned by the operator with
   mode 0600. Keep it outside Git and the app's Compose environment. Never paste
   credentials into chat. Record the non-secret source/owner/custody receipt and
   a rotation review date. Account resets or revoked credentials require a new
   reviewed pair; do not silently overwrite the existing file or revoke another
   application's keys.
2. Verify actual provider read access using fixed, bounded GET requests. Retain
   responses privately and distinguish paper-account acceptance, feed-query
   acceptance, usable data, and licensing scope. Select the verified feed
   explicitly. A forbidden SIP request does not authorize a paid subscription or
   a fallback under SIP attribution.
3. Prepare a new compatible instance with [pilot controls](pilot-controls.md).
   Inspect its actual container/image/PID, schema, health, receiver identity, and
   loopback endpoint. Preserve prior services and use a new owned data volume.
4. Register bounded operator-owned **research** probes matching exact source,
   symbol, and `prospective_market_unqualified` evidence. The rehearsal's broad
   threshold is a routing probe, not an evaluated investment strategy. Calendar
   sources use `alpaca-regular-session`; quote sources include the explicit feed.
5. Create explicit quote and calendar specs with new capture identities and an
   access reference to the retained authorization/readback receipt. Use
   [journal collection commands](../reference/alpaca-journal.md) to acquire one
   request of each capability. Inspect raw attribution, coverage, quote age,
   precision/liquidity, and session times before submission. Keep every failed or
   quarantined capture.
6. Submit through the exact API-origin/receiver binding. Inspect actual retained
   inputs and routing receipts, then repeat submission to verify no additional
   work. Unusable prices must have no price value or matched price condition.
   Calendar matches must remain research-only with no execution authority.
7. For a declared interruption rehearsal, restart only the new owned application
   and compare relevant persisted routing/receiver history. Read actual process
   handles and fresh state; worker timestamps are not immutable business facts.
   Inspect the independent effect ledger and retain a coherent backup.
8. Run [archive/restore verification](../reference/alpaca-archives.md) in a fresh
   isolated directory. Keep the original journal canonical. Record the source,
   image, schema, exact CI state, access scope, outcomes, and remaining gates in a
   private completion receipt. Stop finite collection; do not start recurring
   work implicitly.

## Current limits and next gate

The `feed_coverage` readiness scope remains `not_configured`, even though this
bounded routing rehearsal passed. Continuous collector ownership, coverage
obligations, session-aware freshness, outage/recovery behavior, retention policy,
licensing scope, and usable fresh quote evidence still need qualification.

The next price checkpoint needs a newly identified capture whose provider
bid/ask, sizes, precision, and age satisfy the declared contract. A market-session
window can be selected from retained calendar evidence. Do not loosen quote age,
round unsupported prices, substitute synthetic values, or treat a missing quote
as a fill to make that checkpoint pass.

Research-worker dispatch, hypothesis evaluation, realistic paper execution and
cost assumptions, prospective profitability proof, and the later Alpaca execution
gates remain separate work. No broker execution adapter or live order path is
activated by this rehearsal.
