# Explicit feed readiness policy

`serve --feed-readiness-policy FILE` enables the `feed_coverage` readiness scope
for an explicit receiver and required registration list. Without the flag, the
scope remains `not_configured`. Loading this policy does not start a collector,
fetch provider data, change fixture readiness, qualify a strategy, or enable
trading execution.

## Startup configuration

Use the persistent `receiver_id` from `/v1/research-receiver` and existing
registration IDs from `/v1/feed-collectors`:

```json
{
  "schema": "mantik-feed-readiness-policy-v1",
  "receiver_id": "receiver-REPLACE_WITH_ACTUAL_ID",
  "required_registrations": ["spy-iex"]
}
```

The receiver value is a placeholder; replace it with the actual service identity.
The file contains IDs, never credentials. It must contain one JSON object, at
most 16 KiB, with no unknown fields. The policy requires 1–16 unique valid
registration IDs. Every listed registration is required; the server does not
choose an available subset or discover feeds automatically. Registration IDs
are immutable within the receiver and pin their provider, feed, symbol, journal,
access reference, and evidence class through that registration.

```sh
./bin/mantik serve --listen 127.0.0.1:40880 \
  --feed-readiness-policy /private/mantik/feed-readiness.json
```

The service still requires its normal database and API-token configuration.
For a container, mount the policy file read-only and pass the flag in the
service command; make the mounted file readable by the container user. The
default Compose command does not enable this policy.

Malformed policy fails startup before the database is opened or a listener is
created. A valid policy with a missing registration or different receiver starts
with feed readiness unavailable. That permits explicit registration setup while
keeping the scope closed. The handler copies and sorts the policy at startup;
editing the file has no effect until an explicitly managed service restart.
A restored receiver retains its identity; a different receiver requires an
explicitly updated policy. There is no HTTP policy mutation or automatic rebind.

## Current evidence requirements

Every readiness inspection uses one consistent feed-evidence snapshot and the
service clock. It calls the same raw validation and coverage derivation as
[`feed-coverage`](feed-evidence.md), including pending newer-input suppression:

- Validate original capture bytes and provenance against retained normalized
  inputs and canonical collector admission receipts.
- Require coherent service/collector/publication time and an unexpired,
  unstopped collector lease.
- Require a usable calendar for the requested New York regular session. Unknown,
  stale, conflicting, future, or unpublished newer calendar evidence fails.
- During an open regular session, require a usable fresh quote whose admission
  belongs to the current lease owner and generation. Claiming a replacement
  generation does not refresh an earlier quote.
- During a known closed regular session, no quote is required. Calendar evidence
  and current lease authority remain required.

The fixed server limits remain 60 seconds for quote age, 300 seconds for quote
capture age, and 86,400 seconds for calendar age. A capture's declared quote-age
limit can tighten the 60-second maximum. Client query parameters cannot choose
looser limits or another clock. Readiness does not recheck provider entitlement.

A finite collector releases its lease when done, so its completed run is not
continuing feed readiness. It can satisfy the scope while it holds authority
and has the required evidence. Persistent acquisition and calendar refresh need
separate orchestration; this policy does not install that orchestration.
A current lease is a retained declaration, not inspection of the collector's OS
process. Ready means current validated evidence under this policy; it does not
prove uninterrupted collection, performance, or profitability.

## Inspection and failure behavior

Authenticated `GET /v1/feed-readiness-policy` reports `configured` and the frozen
policy. `collector_liveness_verified` and `qualification_credit` remain false.
`GET /v1/feed-collectors` and each server coverage report expose whether global
readiness is configured. Coverage also reports `required_by_readiness_policy`
for registration selection within the matching receiver.

`GET /readyz?scope=feed_coverage` returns HTTP 200 only when all requirements pass;
otherwise it returns 503. `GET /v1/readiness/feed_coverage` returns the same report
with HTTP 200 for inspection. `/v1/operations` uses the same derivation. Successful
coverage has code `validated_feed_evidence_current`. Failure reports receiver,
registration, evidence, lease, clock, or quote/calendar reason codes. References
identify the pinned receiver, all required collectors, and the failing
`feed_requirement` when a registration check fails. Multiple failures are
reported by the first failed registration in sorted ID order; inspect each
coverage endpoint for its full evidence detail.

Database/evidence-read failures and inconsistent retained history fail closed;
private backend errors and raw provider bodies are not returned. The readiness
check is read-only and uses the existing bounded request contexts. Default
`/readyz` remains the storage scope, `/healthz` remains process liveness, and
`fixture_operation` still depends only on storage and fixture worker capabilities.
Reports retain `mode: paper_fixture`; feed readiness supplies no authority to
create orders or use real money.

## Persistence and rollout

This policy adds no database schema or persisted execution changes. Keep the
startup file with private deployment configuration and include it in the
operator's configuration backup alongside the source/image/receiver receipt.
Database backups retain the separate collector, normalized-input and raw-evidence
history. Journal archives retain client acquisition and publication attempts.
Removing the flag on a separately planned restart restores the default disabled
scope; no registration or evidence history is deleted. Existing services are
not upgraded or restarted merely by creating a policy file.
