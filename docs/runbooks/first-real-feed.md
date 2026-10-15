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

## Closed-session readiness rehearsal, October 15, 2026 UTC

Merged source `2c24ebf2d1f96f4b888b02cb32b142b78e2ea3ad` passed hosted main CI
and a separate committed-image rehearsal of the
[explicit feed-readiness policy](../reference/feed-readiness.md). The rehearsal
restored the retained schema-10 evidence backup into a new PostgreSQL volume and
unique Compose project on loopback 41917. The source backup checksum and
source-manifest receipt were verified, and the source stack's actual containers
remained stopped throughout. No provider credentials were mounted or provider
requests made.

The policy pinned the restored receiver and required `spy-iex`. At actual
service times 03:05:17–03:05:20 UTC, the retained real calendar classified the
regular session as closed with `after_regular_close`. Observed transitions were:

| State | Feed readiness | Evidence |
| --- | --- | --- |
| Restored stopped generation 2 | HTTP 503 | `stopped` |
| Newly claimed generation 3 | HTTP 200 | `validated_feed_evidence_current` |
| Generation 3 stopped | HTTP 503 | `stopped` |
| Newly claimed generation 4 | HTTP 200 | Same closed-session calendar requirement |
| One declared app restart under generation 4 | HTTP 200 | Same policy, receiver, registry and publications |
| Generation 4 stopped | HTTP 503 | `stopped` |

The two retained IEX quote captures remained unusable and quarantined. No quote
was needed for this closed-session policy result; it grants no open-session
fresh-price credit. A retained lease remains an authority declaration, not an
inspection of a collector process.

All three canonical raw publication receipts survived restart unchanged.
Fixture readiness remained available, the case remained waiting, and fixture
observations, actions and the independent economic-effect ledger remained empty.
A coherent backup and SHA-256 receipt were retained with the policy file,
configuration hashes, exact source/image and raw request/handle evidence. The
private completion receipt is `feed-readiness-result.json`, with its audit in
`completion-report.md`, beside the new runtime manifest.

The disposable app and database were then stopped and independently confirmed
inactive with PID 0. The existing services at 41890, 41906 and 41911 retained
their original PIDs and images. No recurring collector or new 24-hour window was
started. This short rehearsal establishes closed-session policy behavior and
restart retention; it does not establish elapsed-time reliability, continuous
feed service, usable real-price readiness, or profitability.

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

Existing services without a startup policy still report `feed_coverage` as
`not_configured`. The isolated policy rehearsal above established finite
closed-session behavior. Open-session readiness with a usable fresh real quote,
continuous acquisition and calendar refresh, coverage obligations,
outage/recovery operation, retention policy, and licensing scope still need
qualification.

The next price checkpoint needs a newly identified capture whose provider
bid/ask, sizes, precision, and age satisfy the declared contract. A market-session
window can be selected from retained calendar evidence. Do not loosen quote age,
round unsupported prices, substitute synthetic values, or treat a missing quote
as a fill to make that checkpoint pass.

Research-worker dispatch, hypothesis evaluation, realistic paper execution and
cost assumptions, prospective profitability proof, and the later Alpaca execution
gates remain separate work. No broker execution adapter or live order path is
activated by this rehearsal.
