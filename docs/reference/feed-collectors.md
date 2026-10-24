# Feed registration and collector ownership

The authenticated feed-collector API persists immutable Alpaca feed declarations
and collector leases. It supplies ownership contracts for a future collector
client. It does not start collection, contact Alpaca, inspect a process, submit
research inputs, or configure global feed readiness.

## Registration

Read `GET /v1/feed-collectors` to obtain the persistent `receiver_id`. Every
mutation must name that receiver; restoring or addressing a different database
requires inspecting its identity before sending commands.

`POST /v1/feed-collectors` accepts one strict JSON object, at most 16 KiB:

```json
{
  "receiver_id": "receiver-REPLACE_WITH_READBACK",
  "command": {
    "request_id": "register-spy-iex-1",
    "operation": "register",
    "registration_id": "spy-iex",
    "registration": {
      "id": "spy-iex",
      "provider": "alpaca",
      "feed": "iex",
      "symbol": "SPY",
      "evidence_class": "prospective_market_unqualified",
      "journal_id": "journal-REPLACE_WITH_EXISTING_ID",
      "access_ref": "access-receipt-one",
      "lease_seconds": 30
    }
  }
}
```

The example identity placeholders must be replaced with actual readbacks.
`journal_id` and `access_ref` identify the private journal and provider-access
receipt. Registration declares these references; the server does not read or
validate the referenced files or provider entitlement. Use opaque identifiers,
never credential values or private paths. Registering `sip` does not grant SIP
access or select it as a fallback.

The current contract supports provider `alpaca`, feed `iex`, `sip`, or
`delayed_sip`, uppercase stock symbols, and class
`prospective_market_unqualified`. Lease duration is 5–300 seconds. Identifiers
use 1–128 ASCII letters, digits, underscores, or hyphens. Registration fields
are immutable. A different registration cannot reuse a journal or the same
provider/feed/symbol/class tuple; this prevents parallel ownership aliases.

## Claim, renew, and stop

Send the same envelope with a command such as:

```json
{
  "request_id": "claim-spy-iex-1",
  "operation": "claim",
  "registration_id": "spy-iex",
  "owner_id": "collector-run-one"
}
```

A claim succeeds only when there is no lease, the previous lease is stopped,
or its expiry has been reached. Every new claim increments the generation,
including a claim by the same owner. The receipt retains the registration,
owner, generation, claim/update time, and expiry.

A renewal names the generation from that receipt:

```json
{
  "request_id": "renew-spy-iex-1",
  "operation": "renew",
  "registration_id": "spy-iex",
  "owner_id": "collector-run-one",
  "generation": 1
}
```

Renewal requires the exact owner and generation and a lease whose expiry has
not been reached. It extends expiry from the service's current time by the
registered lease duration. An expired owner must acquire a new generation.
`stop` uses the same fields with a distinct request ID and records a stopped
lease. The matching owner may stop an expired lease; it cannot stop a successor.
Stop does not kill a process or cancel a provider request.
Owner IDs are logical ownership labels under the existing operator token;
they are not separate collector authentication credentials.

The service samples UTC time after acquiring the storage lock. Clients cannot
supply a clock. A new command earlier than the latest retained command time is
rejected. This clock fence applies across registrations in this bounded registry.

Each request ID identifies an immutable command across the receiver. An exact
retry returns its historical receipt, including after expiry or takeover. It
does not renew a lease or stop a later generation. Changing any command field
under that ID returns a conflict. Inspect current ownership after an uncertain
response; a historical successful receipt is not current authority.

Success returns HTTP 200. Invalid contracts return 422, unknown registrations
404, and conflicts, lost leases, backward clocks, receiver mismatches, or full
history return 409. Malformed or unknown JSON fields return 400. Storage failure
returns 503. Requests retain the existing bearer-authentication boundary.

## Inspection and limits

`GET /v1/feed-collectors` returns receiver identity, all registration and command
history, check time, and a status per registration:

| Status | Meaning |
| --- | --- |
| `unclaimed` | No collector lease has been declared |
| `lease_current` | A retained, unstopped lease has not reached expiry |
| `expired` | Expiry has been reached |
| `stopped` | The matching owner recorded a stop |
| `clock_uncertain` | Inspection time precedes the latest retained command |

`process_liveness_verified` and `qualification_credit` remain false.
`global_readiness_configured` reflects the optional [startup policy](feed-readiness.md).
A lease proves a declaration, not actual process health or usable market evidence.
Without that policy, `feed_coverage` remains `not_configured` and unavailable.

The registry retains at most 16 registrations and 4,096 successful distinct
commands, including renewals. Conflicting or rejected commands do not consume
history. Exact retries still work when capacity is full. There is no pruning,
reset, or automatic expansion; a full registry blocks new ownership commands.
This bounded contract must be extended before unattended continuous collection.

Ownership fences lease commands and the [collector submission endpoint](collector-submission.md).
Manual research-input submission retains its own receiver and provenance
contract. The bounded journal client can select collector-fenced submission;
the [finite leased collector](finite-collector.md) performs explicit lease
acquisition/renewal during quote collection. Combining ownership with independently
validated [regular-session coverage](alpaca-coverage.md) is a subsequent
implementation unit. Registration never enables trading or establishes profitability.

## Persistence and recovery

Schema 8 adds `mantik_feed_collectors`, a singleton JSON state separate from the
fixture/research aggregate. Its transaction retains lease and command receipt
together. Older aggregate writers cannot discard this table's contents; older
startup binaries reject schema 8. Prior migration bytes remain unchanged.

Backups must include this table and `mantik_research_receiver` with the existing
aggregate, audit, and effect ledger. A restart preserves owner, generation,
expiry, immutable declarations, and historical receipts. Restoring a backup
does not make a previously running collector current or authorize activation;
inspect actual runtime handles and restored ownership before recovery. Downgrade
by removing migration records or ownership history is unsupported.

Unit, API, race, and disposable PostgreSQL checks cover lease boundaries,
competing claims, stale generations, retry retention, clock rollback, command
capacity, isolated aggregate writes, migration, restart, and persistence failure.
Provider and runtime qualification require their own retained evidence.

Schema 9 adds collector admission history and a guard against older lease
writers discarding it. Upgrade preserves schema 8 registrations, owners,
generations, expiry, and command receipts. Backups must retain admission history
and its corresponding research inputs together.

## Transaction acknowledgement failures

PostgreSQL ownership commands, normalized input admissions and raw publications
return no receipt when COMMIT fails. Exact retries and collector/evidence
snapshot reads also return no result if their transaction cannot commit. A
staged result or `duplicate` flag is not an acknowledgement of a completed
transaction.

A transport failure at COMMIT can leave the outcome uncertain. Read the source
owner and retry the same immutable command, input or capture identity with the
original bytes and required fence. A rolled-back write leaves that identity
available for a legitimate retry; a committed write returns its retained
receipt. Do not create a replacement identity to resolve uncertainty. This rule
does not extend a lease or grant strategy admission, execution or qualification.

## Retained observation clock fence

New collector commands, input admissions and raw publications must use a service
time strictly after every retained finalized decision, outcome observation and
evaluation assessment. Earlier and equal clocks return a clock conflict before
any owner state or new identity is retained. A legitimate later retry can reuse
the same immutable identity. Callers cannot supply the service clock.

Exact historical retries retain their existing contract and do not create a new
source event or move the clock fence. A successor-fence submission that updates
source state remains subject to current ownership and the new-write clock rule.
The source writer checks retained envelopes under the same ownership lock used
by decision, outcome and assessment issuance. Actual source replay stays with
those owners; a clock envelope is not a source or evaluation attestation.

Schema 56 fences older writers that lack this rule. The migration changes no
historical payload or receipt. A schema-55 binary must refuse the newer database;
rollback requires a matching verified backup and binary. The rule constrains
new writes. It does not retroactively establish completeness or ordering of
legacy history, pass an evaluation or grant admission, execution or qualification.
