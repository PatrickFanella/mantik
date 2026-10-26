# Restored storage-owner replay

`mise exec -- make test-integration` creates its own PostgreSQL server and
uniquely named test containers. It never accepts a development or production
database. The recovery seed retains a synthetic storage fixture's source,
strategy, policy, finalized decision and deferred allocation comparison.

The dump uses the same exported PostgreSQL snapshot as its public-table data
identity. A write committed after that snapshot remains in the source database
and is absent from the restored copy. The restore must match the snapshot's
schema and table identity before owner replay starts.

A separate test container then opens the exact runner-owned restored database
through `Postgres`. It replays the allocation owner, including decision, policy,
strategy, capital, ledger and execution-requirement bindings, and the social
owner. An exact comparison retry must return the original record without reading
a new clock or changing restored data. The test uses the same explicit compiled
storage-fixture evaluator as seeding. It does not start an application worker or
qualify a strategy.

The runner copies source from the stopped original test container rather than
from the current host checkout. The receipt retains that archive's SHA-256,
container identity, exact test events and cleanup result. A package exit without
one run and one pass of `TestPostgresContractsRecoveryReplay` is insufficient;
skips and failures are rejected.

The contract also corrupts the restored comparison's safety payload, requires
the actual owner to return its invalid-history error, and restores the original
payload in a `finally` block. Snapshot identity must match again after that
repair. A separate byte-corruption check still verifies the generic identity
validator. Both replay containers and the disposable server are removed.

Set `MANTIK_RECOVERY_RECEIPT_DIR` to a directory outside the repository to retain
the dump and detailed recovery receipts. Failed replay attempts retain their
container/source metadata there as well. Without that setting, temporary test
evidence is removed at the end of the contract.

This proves fixture storage-owner replay after isolated restore. It does not
prove financial execution, protective recovery, unknown-effect reconciliation,
production restart, release rollback or natural-soak qualification. Those remain
separate gates for issues #19, #20, #25, #26 and #27.

## Evaluation owner restore coverage

The disposable recovery seed also retains an actual predeclared evaluation plan
and a v4 assessment issued after its outcome deadline. The assessment is blocked
for insufficient evidence and grants no admission, execution or qualification.
These are storage fixtures using compiled fixture evaluators, not observations
from providers or an application worker run.

The exported snapshot and restored table identities must contain both evaluation
owner tables. Restored replay checks the declaration/assessment source pins and
non-grant status, then retries each exact command without sampling a clock.
The restored database also rejects an assessment whose admission flag was changed
and whose hash was recomputed. Its original payload bytes are restored before
the outer snapshot identity comparison. No source database is corrupted, and no
assessment or economic effect is created during restored replay. This extends
storage recovery coverage; financial/protective recovery and deployment readiness
remain separate acceptance gates.
