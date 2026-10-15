# Recorded-news acceptance audit, 2026-10-15

This audit covers the operator-recorded, invented-news path delivered by
[PR 70](https://git.subcult.tv/subculture-collective/mantik/pulls/70),
[PR 71](https://git.subcult.tv/subculture-collective/mantik/pulls/71),
[PR 72](https://git.subcult.tv/subculture-collective/mantik/pulls/72) and
[PR 73](https://git.subcult.tv/subculture-collective/mantik/pulls/73).
PR 73 merged as `48cfd1506d3cf806dac716ae944d31340ba67f8f`; merged-main CI passed.
It does not qualify a news provider or machine extraction model.

| Issue 12 criterion | Implemented and verified scope | Remaining limit |
| --- | --- | --- |
| Retain originals and attributable structured claims with uncertainty, entities/instruments and availability | Strict source/claim contracts, exact UTF-8 excerpt binding, canonical hashes, private append-only journal and transactional service originals/results/receipts | Claims are supplied by an operator. Automated claim discovery/extraction and its accuracy have not been implemented or evaluated. |
| Link repeated/syndicated coverage while preserving sources and avoiding false corroboration | Declared event keys group separate source/article identities within an evidence class; independence remains `not_assessed`; exact retries do not add another source | Keys are operator declarations. Automatic event equivalence, syndication detection and source independence analysis are not implemented. |
| Reopen cases for corrections/retractions according to policy and retain targeted source access | Retained checksum-matched predecessors; old/new event union; dedicated research-case reopening; evidence-version-fenced closure; targeted normalized/original inspection | News cases have their own catalogue. Service investigation dispatch and the existing interactive supervisor are not connected to them. |

Issue 12 remains open. The recorded-input interfaces are usable for development
of bounded interpretation contracts, but this audit does not treat the broader
extraction requirement as complete or grant qualification credit.

## Executed evidence

The final schema-11 rehearsal used four invented article revisions and two event
cases in an isolated Compose project. Twenty-nine recorded commands verified
source retention, repeated-source linking, correction/retraction transitions,
stale-close rejection, exact retry, targeted original inspection, application
restart and `pg_dump` restore into a separate database. Restored case links,
originals and admission receipts matched. The fixture effect ledger had zero
entries, and there were no provider requests.

The retained private completion receipt is
`~/.local/state/mantik/news-case-rehearsal-1792091586021447415/completion-audit.json`.
Its source tree is `57e102429fd10d87562f1b516e9347870d187b07`, matching PR 73's
committed tree. Its immutable application image is
`sha256:08f86bce78b5feb17f6d04da302ff8af1b156a9fc490750a550071b9cdd86335`.
The dump SHA-256 is
`8724a4a8dc1436adab32a3f94e3d14395a366e241e3125a950d3a30ce26fc364`.
The two application containers and database were inspected stopped with PID 0
and exit 0. Their volumes and private artifacts remain retained.

Local verification, race tests, disposable PostgreSQL contracts and container
build passed before the PR. Database checks covered schema-10-to-11 migration,
restart retention, concurrent exact admissions, close/admission races and forced
persistence rollback. Initial test-helper aliasing and older-schema emulation
failures were corrected and the final checks passed; review also fixed nested
case-reference isolation on closure and added its regression check.

This is isolated same-host recovery evidence, not off-host restore or deployment
proof. Existing pilot application PIDs/images were inspected unchanged. No pilot
database was upgraded and the user-curtailed observation window was not restarted.
