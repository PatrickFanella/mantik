# Durable synthetic ledger rehearsals

`paper-ledger-record` retains one explicit synthetic accounting assertion against
a service-owned paper account. `paper-ledger-rehearsal` reads that account's
complete bounded rehearsal. It reuses the [offline transition model](paper-ledger.md)
and preserves its synthetic evidence class. These are rehearsal records, not
current account balances, authenticated economic effects or allocation authority.

## Read a rehearsal

```bash
./bin/mantik paper-ledger-rehearsal --account pilot-paper \
  --api-url http://127.0.0.1:40880
```

The account must already exist. Before the first append, the service derives an
empty rehearsal from the immutable account receipt. The initial ledger head is
that account's digest. Reading this empty state does not insert a ledger row.

A read includes the account receipt, each accepted command and original append
receipt, the replayed cash projection and integrity digests. The service and CLI
replay every retained prefix, checking predecessor heads, accepted times, original
receipt hashes, prefix result digests and the complete result. A corrupt retained
ledger fails rather than appearing empty or dropping entries.

## Record one synthetic assertion

Prepare a private JSON file containing these fields, taking the receiver, account
digest and current ledger head from a fresh readback:

```json
{
  "receiver_id": "RECEIVER_ID",
  "account_sha256": "ACCOUNT_SHA256",
  "expected_head_sha256": "LEDGER_HEAD_SHA256",
  "scope_code": "record_synthetic_ledger_rehearsal_v1",
  "entry": {
    "id": "reserve-example",
    "kind": "reserve",
    "reservation_id": "example",
    "amount_cents": 10000,
    "occurred_at": "2026-10-19T21:00:00Z"
  }
}
```

The placeholders and timestamp are illustrative. Use actual returned identities
and an explicit event time at or after account creation and no later than server
acceptance; new event times cannot go backward within the rehearsal.

```bash
./bin/mantik paper-ledger-record --account pilot-paper \
  --file /private/append-command.json --api-url http://127.0.0.1:40880
```

The command uses the existing `MANTIK_API_TOKEN`. It does not discover and replace
missing identity/head pins or rebase a stale command automatically. The input is
one command, at most 16 KiB; unknown/duplicate fields, trailing documents and
whole offline recordings are rejected. The unchanged `paper-ledger-audit` command
still has no API connection or service import path.

Only the named account's funding digest is accepted. A new command's expected
head must equal the current head. Competing commands against one head therefore
produce at most one append; the other caller must inspect current state and make
an explicit new decision. No automatic retry performs another economic debit.

An exact retry must preserve every original field, including its original head
pin and event time. It returns the original accepted receipt with an outer
`duplicate: true`, even after later entries or at capacity. It does not sample a
new clock, append another command or advance the ledger head. Changing the entry
or its head under an existing entry ID conflicts. The receipt's result digest is
from its original acceptance, not a claim about the current ledger; read the
rehearsal separately for current state. The CLI validates the receipt's command,
account, time and entry-hash linkage; full prefix replay occurs on ledger reads.

## API, persistence and recovery

- `GET /v1/paper-accounts/{id}/ledger-rehearsal` reads one complete rehearsal.
- `POST /v1/paper-accounts/{id}/ledger-rehearsal/entries` accepts the single command
  above, returning 201 for a new append or 200 for an exact retry.
- Routes accept no query parameters and share existing bearer authentication.
  Missing accounts return 404; malformed bodies return 400; invalid bindings or
  retained records return 422; stale/conflicting commands, limits and receiver
  mismatches return 409. Storage failures return 503.

Schema 28 adds `mantik_paper_ledger_rehearsals`, keyed by and referencing an
existing paper account. The account's immutable funding row is unchanged. The
existing receiver row serializes appends across PostgreSQL processes; replay,
head checking and the new ledger payload commit in one transaction. Memory uses
its existing mutex and detached values. Reads use a repeatable-read snapshot of
the receiver, account and rehearsal.

PostgreSQL reads and append acknowledgements, including exact retries, are
returned only after their transaction commits. A failed COMMIT returns an error
and a zero result. This does not erase an entry retained by an earlier successful
append: inspect the owner history before retrying an uncertain operation. Exact
retries still return the original receipt without sampling a clock or appending
another entry once their transaction succeeds.

There is one rehearsal per account, at most 256 unique accepted entries and 64
reservation IDs, including closed reservations. Stored payloads and full readbacks
are bounded to 1 MiB. At most 16 accounts exist; no unbounded event history or
model artifact inventory is loaded. There is no reset/delete/rebase route.

Schema 27 upgrades with no rehearsal rows and preserves account funding. Older
binaries refuse schema 28. Back up before upgrade; rollback restores the matching
pre-upgrade database and binary, not a database with deleted migration rows.
Existing pilot services are not upgraded by merging this code.

## Authority and remaining work

The wrapper authority is `service_retained_synthetic_ledger_rehearsal_only`.
Allocation availability, execution attestation and qualification credit stay
false. The underlying result remains `recorded_synthetic_paper_ledger_only`.
An operator's `settle` or `resolve` entry is a recorded synthetic assertion; its
server acceptance time proves retention, not provider execution or reconciliation.
Unresolved amounts stay reserved until an explicit synthetic resolution, and that
resolution alone moves no cash.

These records cannot change account configuration, demo positions/actions/effects,
research cases or admission. They are not an account balance projection to spend,
and no operation consumes them as authority. Durable operational reservations and
settlement must bind qualified admission and authoritative effect/reconciliation
evidence through a separate contract. Competing proposal allocation, security
positions, realistic execution and portfolio supervision remain incomplete.
