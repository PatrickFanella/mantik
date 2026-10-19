# Paper account configuration and opening funding

`paper-account-create`, `paper-accounts` and `paper-account` retain and inspect
Mantik-owned paper capital configuration. The account and its single opening
funding receipt are immutable. This is the first part of
[the allocation plan](../plans/paper-accounts.md), not a portfolio or broker
balance projection. Receipts explicitly report `allocation_available: false`
and `qualification_credit: false`.

## Commands

Read the service's receiver namespace and current account configurations:

```bash
./bin/mantik paper-accounts --api-url http://127.0.0.1:40880
```

To create a configuration, supply that exact receiver ID and choose a stable
account ID. These sample amounts are invented paper values:

```bash
./bin/mantik paper-account-create --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id pilot-paper \
  --opening-cash-cents 100000 --capital-limit-cents 50000
./bin/mantik paper-account --api-url http://127.0.0.1:40880 --id pilot-paper
```

Commands use `MANTIK_API_TOKEN` and emit JSON. `--help` lists flags. Creation
requires all capital fields and an explicit receiver; it does not discover a
receiver and then silently write to it. The receiver identifies this service's
persistent namespace, not a human identity or a broker account.

The only currency is USD, represented in integer cents. Opening cash must be
between 1 and 1,000,000,000,000 cents. The committed-capital ceiling must be
positive and no greater than opening cash. These are implementation limits, not
funding recommendations. The retained policy is `cash_only_no_leverage_v1`.
There is no top-up, transfer, withdrawal, position, order, reservation or effect
API. The capital ceiling is retained configuration; an allocator does not yet
apply it to proposals.

## API and persistence

- `POST /v1/paper-accounts` accepts exactly `receiver_id` and `config`, whose fields
  are `id`, `currency`, `opening_cash_cents` and `capital_limit_cents`. A new
  immutable account returns 201 with `duplicate: false`. An exact retry returns
  200 with `duplicate: true`, preserving the original time and funding identity.
- `GET /v1/paper-accounts` returns every account in ID order, at most 16, and the
  receiver namespace. An empty catalogue has an empty account array.
- `GET /v1/paper-accounts/{id}` returns one immutable funding/configuration record.
  Missing accounts return 404. The read routes accept no query parameters.

All routes share the existing bearer authentication. Invalid configuration is
422; changed account intent, receiver mismatch and capacity exhaustion are 409.
Malformed JSON/unknown fields use the existing 400 body contract. A failed
creation does not create a partial funding record. Exact retries work even when
the account catalogue is full.

PostgreSQL schema 27 adds `mantik_paper_accounts`. Creation locks the persistent
receiver row before checking existing intent and capacity, so concurrent writers
cannot duplicate opening funding or exceed the account limit. Reads use one
repeatable-read snapshot of the namespace and bounded complete catalogue. Memory
storage uses its existing mutex and returns detached values. Both stores validate
record identity, namespace, funding metadata and receipt digest before returning
or reusing a retained account; corrupt records fail rather than disappearing
from the catalogue. The digest is an integrity link, not third-party attestation.

Schema 26 upgrades with an empty account table and preserves existing state.
Older binaries refuse schema 27. Back up before upgrade; rollback uses the
pre-upgrade database with its matching binary. Do not delete migration rows to
run an older binary against the upgraded database. Existing pilot services are
not upgraded by building or merging this change.

No account is created automatically. No existing demo case, cash balance, action,
effect or research proposal is assigned to an account. No Augr data, Alpaca account,
provider credentials or external financial state is read. The [offline synthetic ledger audit](paper-ledger.md) now exercises reservation
and debit conservation without mutating these accounts. Durable service ledgers,
positions, competing proposal comparison and qualified admission remain
future units of #19–#21. A terminal portfolio view depends on those projections;
account configuration alone does not satisfy #22 or demonstrate profitability.
