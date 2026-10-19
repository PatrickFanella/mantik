# Synthetic paper ledger replay

`paper-ledger-audit` checks recorded synthetic accounting transitions offline.
It starts from one validated [paper account configuration](paper-accounts.md),
replays a bounded recording and produces a deterministic cash projection and
hash-linked ledger. It opens no API connection and writes no service account,
reservation, action or economic effect.

```bash
./bin/mantik paper-ledger-audit --export-fixture > /private/ledger-fixture.json
./bin/mantik paper-ledger-audit --file /private/ledger-fixture.json
```

Use exactly one of `--file` or `--export-fixture`. There is no `--api-url`, import,
execute or provider flag. The input file is not rewritten. Invalid recordings
return an error with no partial result on stdout. Keep failed recordings and
stderr as separate private evidence when running a rehearsal.

## Recording contract

The schema is `mantik-synthetic-paper-ledger-recording-v1`, with evidence class
`synthetic_unqualified`, one complete immutable account receipt and an `entries`
array. The account must pass its existing funding/configuration integrity checks.
Its digest is not proof that it was created by a running service or belongs to
the caller. This recording never becomes service admission evidence.

Each entry contains `id`, `kind`, `reservation_id`, `amount_cents` and an explicit
`occurred_at`. IDs use the existing bounded account ID syntax. Currency is the
account's USD integer cents. Commands are processed in supplied order; new entries
cannot predate the account or preceding unique entry. Equal timestamps are
allowed. Times are normalized to UTC for comparison and hashing.

The reader rejects unknown/duplicate JSON fields, trailing documents and files
over 1 MiB. Recordings contain at most 256 commands and 64 distinct reservations,
including closed reservations. Empty arrays are valid; missing/null arrays are
not. No state is inferred from missing entries.

| Kind | Recorded transition |
| --- | --- |
| `reserve` | Positive amount moves available cash into a new reservation. Both cash and the account's committed-capital ceiling must permit it. Reservation IDs cannot be reused. |
| `settle` | Positive amount moves part or all of a known reservation into cumulative settled debits. The unused remainder stays reserved. |
| `release` | Positive amount returns part or all of a known reservation's unused remainder to available cash. Settled debits cannot be released. |
| `unknown` | Zero amount marks a nonempty reservation unresolved. Its entire remaining amount stays reserved. |
| `resolve` | Zero amount clears a recorded unresolved marker. It changes no cash amount and does not itself settle or release anything. |

Settlement and release are rejected while a reservation is unresolved. There is
no timeout release, automatic retry debit, sale/proceeds credit, top-up,
withdrawal or leverage. `resolve` is an asserted step in a synthetic scenario,
not a provider query, authenticated effect receipt or proof of reconciliation.
Any later service writer must bind settlement/resolution to its own durable,
authoritative evidence before this model can govern actual paper commitments.

An exact repeat of an entry ID returns the original sequence and entry hash,
with `duplicate: true`, even after later transitions. It neither advances the
clock nor changes money. Reusing that ID with changed fields conflicts. A new
ID cannot disguise reuse of an existing reservation ID. Duplicate commands count
toward the recording bound and remain visible in the result's receipt list.

## Projection and evidence limits

After each unique entry the model enforces:

```text
available cash + reserved cash + settled debits = opening cash
reserved cash + settled debits <= committed-capital ceiling
```

All values remain nonnegative. Each reservation also preserves
`remaining + settled + released = original`. Settled debits represent recorded
cash committed at cost; they are not security positions, marked-to-market values,
realized profit, reconciled fills or a broker balance.

The result contains every unique entry, ordered command receipts, reservation
projections sorted by ID, unresolved count, last unique-entry time and ledger
head. The first entry links to the account digest; each later entry links to its
predecessor. The recording digest covers the normalized complete input, including
retries. The result has its own digest. These integrity links make replay
inspectable; they are not signatures or independent execution attestation.

The result schema is `mantik-synthetic-paper-ledger-audit-v1`, with authority
`recorded_synthetic_paper_ledger_only`. Allocation availability, execution
attestation and qualification credit are false. The invented fixture ends with
55,000 cents available, 30,000 reserved, 15,000 in settled debits and one unresolved
reservation. These numbers verify accounting behavior only.

This unit supplies the transition model and offline audit path for #19.
Durable service ledger writes, allocator-owned account selection, strategy
admission, security positions, concentration/correlation limits, realistic paper
execution and terminal portfolio supervision remain separate work.
