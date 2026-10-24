# Owned paper evaluation assessments

This is the first durable assessment boundary for issues #19 and #21. The
service selects actual retained evidence from a predeclared evaluation plan and
records the result of its compiled necessary checks. It does not complete the
full evaluation recipe or issue admission, execution or qualification authority.

The authenticated service provides:

- `POST /v1/paper-evaluation-assessments`
- `GET /v1/paper-evaluation-assessments`
- `GET /v1/paper-evaluation-assessments/{id}`

A POST command contains only `id`, `receiver_id`, `declaration_id` and
`declaration_sha256`. The declaration must belong to the service receiver and
its pin must match the actual stored declaration. Callers cannot provide a
clock, evidence, check results, admission, account selection, balances, snapshot
or effect. Unknown fields, duplicate fields, trailing JSON and query parameters
are rejected. There is no new offline command or financial write route.

The service replays the actual strategy, policy, declaration, finalized-decision
and outcome owners in one consistent snapshot. Issuance takes the existing
collector → state → receiver lock order in PostgreSQL; Memory uses its aggregate
mutex. The resulting `mantik-owned-paper-evaluation-assessment-v1` record retains
its command, service clock, recipe, complete selected evidence, individual check
states and a SHA256 digest. Its recipe is
`native-predeclared-necessary-checks-v1`.

Each predeclared variant receives all eight planned checks. Before the outcome
deadline, sample sufficiency remains `pending`. At or after that deadline, an
observed occurrence upper bound below the predeclared minimum proves the
necessary sample condition has failed. That check is `failed`, and the assessment
is `blocked`. Reaching the count minimum leaves sample sufficiency `unavailable`:
occurrence counts do not establish statistical independence. The remaining
checks are `unavailable` until their applicable evidence and validated methods
exist. An assessment without a proven failure after the deadline is `incomplete`.
Failed and unavailable checks remain inspectable alongside pending attempts.

`evaluation_complete`, `admission_available`, `execution_available` and
`qualification_credit` are always false. Source classes remain explicitly
unqualified. Forward-quote outcomes do not become fills, economic returns,
benchmark results or realized PnL. A blocked assessment is not a retirement
event, and an incomplete assessment cannot authorize an allocation.

## Retention and source ordering

The live [evaluation evidence read](paper-evaluation-declarations.md) can change
as new records arrive. An assessment retains its original complete selection
and replays it against actual owner records at the assessment clock. Later
records are excluded only from historical replay; a new assessment cannot omit
currently retained future-dated source records.

After an assessment is issued, a new declaration, finalized decision or outcome
must have a service timestamp strictly after every retained assessment. The
check runs under the same owner lock as issuance. Equal and earlier timestamps
fail before mutation and leave the source ID reusable. Historical exact retries
keep their original records and do not read a new clock. The ordering rule keeps
later source appends from changing an already issued assessment, including when
issuance races with a source append at the same timestamp.

A new assessment uses a clock at or after the collector source clock and all
previous assessments. The same ID and command return the original receipt with
`duplicate: true`, without consulting a clock. A changed command conflicts.
Catalogue reads and retries validate every retained assessment against actual
sources, including its check states and evidence selection. Rehashing a
fabricated check or omitting evidence does not make it valid.

A SQL or COMMIT failure returns no assessment receipt. A transport failure at
COMMIT may leave the outcome uncertain; read the owner catalogue and retry the
same command identity. Do not create a replacement ID to resolve uncertainty.

Schema 50 adds the assessment table and checksum fence. It changes no existing
assessment, declaration, decision, allocation or operational payload. A schema
49 binary must refuse the newer database. Rollback uses a verified matching
backup and binary; deleting a schema marker is not a downgrade procedure.

The owner retains at most 16 assessments, each at most 8 MiB plus 64 KiB, with a
16 MiB aggregate payload cap. Capacity exhaustion fails closed and does not
evict failed or incomplete attempts. There is no deletion or reset endpoint.

The remaining admission work still includes applicable leakage, warm-up,
out-of-sample, parameter and cost sensitivity, benchmark, calibration,
independence and search-multiplicity methods; legitimate qualification evidence;
a versioned admission/expiry/reassessment/retirement issuer; and execution-owner
reconciliation with atomic financial postings. Issues #19, #20 and #21 remain
unfinished.

## Allocation owner replay

Allocation comparison writes, catalogue reads and exact retries validate the
retained assessment owner before returning any result. PostgreSQL does this in
the same transaction that selects comparison sources, under the existing write
lock order or read snapshot. Memory uses the same aggregate mutex. Attribution
validates assessments against its already replayed declaration, decision and
outcome catalogues in that snapshot.

The service supplies both compiled decision and outcome evaluators. Internal
storage callers may omit the outcome evaluator only while the assessment
registry is empty. An explicit nil callback, multiple callbacks, a missing
callback with retained assessments, or an evaluator inconsistent with actual
retained outcomes fails closed. Callbacks are trusted service wiring; they are
not public API fields or caller-provided check results.

Rehashed fabricated checks, omitted assessment evidence and other owner replay
failures prevent comparison reads and historical retries as well as new writes.
No comparison receipt is returned on those failures, and retry does not read a
new clock. Restoring valid owner data restores the same original comparison
receipt; adding legitimate later assessments does not change its bytes or hash.

This is an owner-validation dependency for future assessment-to-proposal
bindings. It does not add assessment pins to old comparison records, upgrade an
incomplete assessment into an applicable full recipe, or issue admission or
financial effects. Comparison wire schemas and database schema50 are unchanged.
