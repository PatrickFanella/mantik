# Retained allocation policy ownership

`allocation-policy-register`, `allocation-policies` and `allocation-policy`
retain and inspect one immutable operator-declared policy per Mantik paper
account. This is the ownership/configuration part of issue #19. The service does
not accept allocation snapshots, balances, admissions or planned reservations
through these commands. A receipt has `reservations_available: false` and
`qualification_credit: false`.

## Operator commands

Inspect the existing paper funding catalogue to select its exact receiver,
account ID and funding SHA-256. Prepare a private JSON file containing the policy
fields from [the policy core](allocation-policy.md): `id`, `account_sha256`,
`max_snapshot_age_seconds`, `strategies`, `symbols` and `groups`. The account hash
must equal that service's retained funding receipt. Strategies declare IDs,
artifact hashes, limits and priority; symbols declare limits and group membership.
These are operator declarations. Registration does not prove the named strategy
exists in an executable registry or that its artifact has qualified.

```bash
./bin/mantik allocation-policy-register --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --account-id PAPER_ACCOUNT --file /private/policy.json
./bin/mantik allocation-policies --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID
./bin/mantik allocation-policy --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id POLICY_ID --record-sha256 RECORD_SHA256
```

All commands use `MANTIK_API_TOKEN` and emit JSON after complete response
validation. `--help` lists flags. Writes require explicit receiver and account;
they do not discover a receiver and silently write into it. Selected reads also
require the expected full record digest. The catalogue is validated as a whole,
including its receiver, immutable funding receipts and unique account owners.

Policy files are strict JSON objects of at most 128 KiB. Unknown, duplicate or
case-alias fields, trailing documents, invalid policy limits and missing required
arrays are rejected. Nested policy slices are detached from callers. Responses
are bounded, strictly decoded and validated before output; a valid record for a
different command or receiver is rejected. Hashes are integrity links, not
signatures or proof of strategy admission.

## API and retry contract

- `POST /v1/allocation-policies` accepts exactly `receiver_id`, `account_id` and
  `policy`. A new record returns 201 with `duplicate: false`; an exact retry
  returns 200 with `duplicate: true`, preserving registration time and digest
  without calling the clock again.
- `GET /v1/allocation-policies` returns the full receiver-owned catalogue, sorted
  by policy ID. The bound is the existing sixteen-account capacity; an account
  may own at most one policy.
- `GET /v1/allocation-policies/{id}` returns a complete policy record with its
  retained funding receipt. A missing ID returns 404.

All routes require the configured bearer token and reject query strings,
including malformed or empty queries. Invalid configuration/JSON is 422; changed
policy intent, changed funding pin, namespace mismatch or conflicting account
ownership is 409. Missing funding is 404; unavailable/corrupt storage does not
produce a successful readback. Policy validation never creates funding.

There is no policy update, replacement, detach or deletion path. Reusing a policy
ID with different content conflicts, as does claiming an already owned account
under another policy ID. Exact retries still succeed when all account slots are
occupied. There is no automatic policy for existing accounts.

The fixture worker and general allocation policy cannot own the same account.
Both registration and fixture binding check the reciprocal claim under the same
aggregate lock order. A different, explicitly funded account can be configured
without changing the fixture's cash, position, actions or observations. Synthetic
ledger rehearsals remain separate assertions and do not supply operational
balances or admission authority.

## Persistence, verification and rollback

Memory uses the aggregate mutex. PostgreSQL schema 36 adds
`mantik_allocation_policies`, with a policy primary key, unique account owner and
foreign key to retained paper funding. Writes lock the executable aggregate and
then receiver row, matching fixture binding's order. Every read/retry validates
the complete funding/policy prefix and existing fixture capital binding; reads
use one repeatable-read snapshot. An insertion failure commits no policy.

Schema 35 upgrades with an empty policy table and preserves existing accounts,
fixture state and candidate/research evidence. Older writers refuse schema 36.
A rollback restores the pre-upgrade database with its matching binary. Do not
delete migration rows or drop the policy table to run an older binary against
modified data. Building or merging this code does not upgrade existing pilots.

Shared Memory/PostgreSQL checks cover concurrent duplicates, conflicting
registrations, reciprocal fixture ownership, detached values, catalogue capacity,
clock-free retries and unrelated corrupt records. Disposable tests also exercise
35-to-36 upgrade, failed-insert rollback, restart and checksum fencing. HTTP/CLI
checks cover auth, strict shapes, namespace/content pins and rejecting injected
balances/admissions. Local checks, hosted CI, deployment and runtime evidence
remain separate proof states.

The remaining #19 work is general reservation and position projection tied to
owner-retained finalized decisions, qualified admission, complete commitment and
reconsideration history, and ledger-derived account state. Realistic fills (#20)
and qualification admission (#21) must use those same commitments. Policy
configuration alone does not complete #19 or authorize a natural soak.

## Isolated native rehearsal

On 2026-10-21, a built CLI/service rehearsal used a fresh loopback PostgreSQL
container and separate general/fixture paper accounts. Exact policy retries and
pinned selected/catalogue readbacks matched. General-policy ownership blocked
fixture binding with HTTP 409; fixture ownership blocked policy registration
with HTTP 409. Policy/funding evidence remained identical after process restart
and a logical dump restored into a second disposable database. The restored
service had no observations, audit events or fixture effects.

The schema-35 binary was built from source `193a3c98caa03d443184e58fb14c40a3b5d980bd`
with Go 1.26.9. It refused the upgraded database before starting workers. Both
service processes and the restored process exited cleanly; the owned PostgreSQL
container was stopped and inspected with PID zero. Existing Mantik pilot and
Switchyard container identities, images, start times and PIDs were unchanged.
This was a local policy/configuration rehearsal, not deployment, admission,
reservation execution, off-host disaster recovery or soak evidence.

The initial driver attempt selected an unsupported disposable database name;
Mantik correctly refused it. That failed attempt remains retained. The corrected
driver used fresh resources with an allowed `mantik_test_*` name; no application
change was made to pass the guard. The earlier strict-response regression also
retains its failed result alongside the corrected check. Raw logs, backups,
binaries and runtime receipts remain outside Git.
