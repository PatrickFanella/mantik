# Validated server feed evidence

Mantik can retain a private raw Alpaca capture and independently normalize it
for server-side coverage inspection. This closes the gap between a local journal
report and the service's flattened research inputs. Publication is an explicit
operation under a collector fence; it does not fetch data or activate readiness.

## Publish one capture

The capture must already be retained in the bound journal. Every normalized
input from it must have the exact bytes, canonical routing receipt, and collector
admission in the target receiver. Submit pending inputs through the
[fenced submission protocol](collector-submission.md) first.

```bash
./bin/mantik alpaca-publish --journal "$MANTIK_CAPTURE_JOURNAL" \
  --api-url http://127.0.0.1:40880 --receiver-id RECEIVER_ID \
  --collector-registration-id spy-iex \
  --collector-owner-id collector-run-owner --collector-generation 1 \
  --capture-id quotes-run-001 --revision 1
./bin/mantik feed-coverage --api-url http://127.0.0.1:40880 \
  --collector-registration-id spy-iex
```

The registration, receiver, capture identity and revision must match actual
retained records. The command reads an existing journal, verifies its immutable
fenced target binding, and publishes exactly one capture. It never recreates a
missing journal, changes the binding, acquires a lease, reads a provider, or
submits research inputs.

Publication requires a current, unstopped lease under the exact supplied owner
and generation. An exact retry of a publication under its original publishing
fence can return the original receipt after expiry or stop; it creates no new
record. This allows retry after a lost HTTP response. If the earlier publication
did not commit, current authority is still required. A successor may read an
identical prior publication under current authority but cannot rewrite its first
publisher or time.

New [finite collector](finite-collector.md) runs publish each acquired capture
under the run's fixed lease generation after normalized delivery and before
releasing the lease. Historical v1 completed runs retain their original meaning
and have no automatic publication proof. Manual publication remains available
for explicit recovery; publishing an older capture does not make its quote
admission belong to the current generation. Global readiness policy remains
unconfigured.

## HTTP and validation

Authenticated `POST /v1/feed-evidence` accepts a strict object containing
`receiver_id`, `fence`, and `recording`. The recording uses the existing
[Alpaca recording contract](alpaca.md). The body is limited to 2 MiB and raw
provider content to 1 MiB. Unknown envelope/recording fields and trailing JSON
are rejected. A successful receipt includes registration, capture key, original
publishing fence/time, canonical recording digest, and `duplicate`.

The server verifies:

- Raw SHA-256, supported recording schema, capability, currency and limits.
- Exact registered symbol, evidence class, access reference, journal capture
  reference, and quote feed; calendars use `regular_session`.
- Deterministic normalization against retained input bytes, admission digests,
  registration attribution and original routing receipts.
- Current publication authority and service time for a new record.

Calendar captures retain their full declared range and session times. A valid
empty calendar range is distinct from missing calendar evidence. Such a capture
has no routing input to compare, but still requires raw validation, exact
registration provenance and current publication authority.

The server validates supplied bytes and retained provenance; it does not obtain
the raw response from Alpaca itself, attest provider entitlement, or qualify a
strategy. Credentials are not accepted by this API. Raw captures stay private
in PostgreSQL and the journal; the coverage response returns evidence references
and metadata without raw provider bodies.

New records are immutable under registration/capture/revision. Conflicting
content returns 409. The service retains at most 128 capture publications across
all registrations, with no pruning or replacement. Exact retries remain possible
at capacity. Invalid evidence returns 422, unknown registration 404, malformed
JSON 400, ownership/receiver/history/clock/capacity conflicts 409, and storage
failure 503.

## Server inspection

`GET /v1/feed-collectors/{id}/coverage` uses one consistent receiver snapshot,
rechecks retained raw captures against input/admission history, and computes
coverage at the current service clock. Client timestamps and age overrides are
rejected. The fixed limits are 60 seconds for quote occurrence age, 300 seconds
for quote capture age, and 86,400 seconds for calendar capture age. A quote's
original capture limit can tighten its occurrence-age requirement.

Latest bad or conflicting evidence suppresses older good evidence. A newer
admitted quote or calendar whose raw capture is not yet published also suppresses
the relevant older publication, with `quote_publication_pending` or
`calendar_publication_pending`. Historical data cannot silently fill a
publication gap.

The response separates:

- `evidence.required_evidence_satisfied`: validated regular-session calendar and,
  while open, an eligible usable quote from that session.
- `lease_status`: retained declaration of current, stopped, expired, unclaimed,
  or clock-uncertain ownership.
- `quote_admission_current_generation`: whether the selected quote admission
  matches the current lease owner and generation.
- `operational_evidence_satisfied`: evidence requirements plus a current lease,
  coherent clock, and current-generation quote admission when price is required.

`collector_liveness_verified`, `global_readiness_configured`, and
`qualification_credit` remain false. A current lease is not inspection of an OS
process. A valid closed-session calendar can satisfy the raw evidence requirement
without a usable price, while a stopped collector still fails the operational
evidence check. Global `feed_coverage` remains `not_configured`; fixture readiness
and strategy execution retain their existing contracts.

## Storage and recovery

Schema 10 adds `mantik_feed_evidence`, separate from the fixture/research
aggregate. Publication serializes with lease and routing mutations, validates
their snapshot, and commits the immutable capture and collector clock together.
A failure rolls back both. Previous migration bytes remain unchanged, and older
startup binaries reject schema 10.

Backups must include raw evidence with receiver identity, collector history,
research inputs/receipts, and the existing trading state. Restart and restore
must preserve canonical publication digests and original publisher receipts.
Restored evidence does not start collection or configure readiness. Schema
downgrade by deleting evidence or migration records is unsupported.
