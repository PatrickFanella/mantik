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

## Original recorded-news evidence

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


## Recorded extraction checkpoint through PR 102

The following additions supplement the original PR 73 audit. They cover
[PR 99](https://git.subcult.tv/subculture-collective/mantik/pulls/99),
[PR 100](https://git.subcult.tv/subculture-collective/mantik/pulls/100),
[PR 101](https://git.subcult.tv/subculture-collective/mantik/pulls/101) and
[PR 102](https://git.subcult.tv/subculture-collective/mantik/pulls/102).
PR 102 merged as `8d01581763aa70cc56bed46c00e1fde14a11e583` after its exact-head
PR check and normal merge gates passed. This checkpoint does not claim
merged-main CI, deployment or provider-backed extraction from that PR-head pass.

| Issue 12 boundary | Added implementation and evidence | Still unproven or absent |
| --- | --- | --- |
| Source before interpretation | A source-only article contract separates original metadata from claims. A bounded recorded extractor checks strict response shape, uncertainty, attribution and exact source excerpts; malformed artifacts remain captured abstentions. | The recorded adapter does not discover claims from an article by itself. Exact quotes do not prove an assertion follows from them. |
| Retained attempts and failures | Schema 21 retains receiver-bound intents before completion, complete raw artifacts, contract/source/result hashes, deadlines and every pending/completed/expired attempt. Exact retries preserve original receipts. | Recorded artifact provenance does not attest a model invocation or when a caller created it. No provider or automatic extraction dispatch runs. |
| Explicit admission and downstream research | Schema 22 atomically admits eligible completed candidates with exact attempt/result pins and explicit caller approval. News records retain provenance; ordinary submission cannot fabricate it. Corrections and retractions reopen affected cases, and a recorded downstream case review completes against admitted evidence. | Approval records caller intent, not reviewer identity, factual accuracy or semantic quality. Automatic event equivalence, syndication detection and source independence remain absent. |
| Evaluation tooling | An offline corpus evaluator replays every recorded output against supplied expected claims or deliberate abstentions. It retains all case results, malformed output and disagreements, with one-to-one exact claim matching and corpus/contract/report hashes. | Its invented demonstration measures no real extractor. Annotation agreement is not source truth, provider provenance or generalization accuracy. No reviewed real-source evaluation corpus or provider-backed result is established here. |

Reference contracts: [standalone extraction](../reference/news-extraction.md),
[retained attempts](../reference/retained-extractions.md),
[reviewed admission](../reference/reviewed-extraction-admission.md) and
[offline evaluation](../reference/extraction-evaluation.md).
These additions preserve the original operator-recorded encodings and the
separate research/operations authority boundary. They grant no qualification
credit and do not create an operation or economic effect.

### Retained execution evidence for the additions

Receipt directories below are private under `~/.local/state/mantik/` and remain
outside Git. Their hashes identify the exercised source; later source is not
covered automatically by an earlier pass.

| Receipt and source tree | Observed result |
| --- | --- |
| `retained-extraction-rehearsal-1792123912027754682`, tree `61e02bac26a22d16719633d9774207bc71af46ba` | 85 commands; schema 20→21; five attempts comprising three completed, one pending and one real-deadline expired. Pending/completed evidence and older research survived restart and isolated restore. Failed completion persistence left the intent pending; raw invalid UTF-8 remained exact; corrupted clone history and an older binary were rejected. |
| `reviewed-extraction-rehearsal-1792125011703326420`, tree `472fb416e6d04c4b87e8fb6def567ad26530fec2` | 116 commands; schema 21→22; twelve attempts and three approvals. Correction/retraction reopening, exact retry, generic-provenance bypass rejection, source conflicts, receipt-write rollback and downstream recorded review passed. Restart/restore preserved exact evidence, a corrupted clone receipt failed, and the older binary refused schema 22. |
| `extraction-evaluation-rehearsal-1792126225642625678`, tree `91101affe651183343967ae74dcd4dd2aa1cdf40` | Eight invented cases retained, including a structurally valid wrong assertion and malformed output; three exact cases, three matching claims, two missing claims and three unexpected claims. Replay was byte-identical. Changing annotations changed corpus/report hashes without changing actual extraction. Invalid corpus input emitted no partial report and an outcome-filter flag was rejected. |

The schema-21 dump SHA-256 is
`61669aa520e52a8b9601e9236d598bf798c6293332e6d06d17eec18d2ed06e68`;
the schema-22 dump SHA-256 is
`3a8976d0c75b02ca8c40f6a69385a1d31a3af51c504752d714bfd26b6f43eab6`.
These were isolated same-host restores, not off-host disaster recovery.
All three rehearsals recorded zero provider requests and zero economic effects.
The eight final containers from the schema-21 and schema-22 rehearsals were
inspected stopped with PID 0 at this checkpoint. The evaluator is a finite
process, with no retained background worker.

The earlier failed extraction rehearsal remains at
`retained-extraction-rehearsal-1792123265315833239`. Its driver chose a restore
database name outside the allowed disposable namespace, and the application
correctly refused it. The corrected rehearsal used fresh resources and an
allowed name; the failed evidence was not replaced. This driver failure is
separate from the deliberately injected write failures in the passing runs.

Each implementation PR records its applicable local checks separately from
hosted CI. The schema changes ran verification, race tests, disposable database
checks and container builds. The evaluator ran verification, race tests and its
built CLI rehearsal; it changed no storage, schema, dependencies or container
behavior. Original pilot PIDs/images remain unchanged at this checkpoint, and
the user-curtailed observer remains inactive. No 24-hour proof is added.

### Next implementation boundary

Issue 12 remains open. The next extraction step needs a selected model/adapter
and a reviewed evaluation corpus with explicit expectations. Provider access,
permitted source material and finite request/cost bounds must be established
before a provider-backed rehearsal. A supplied-annotation evaluator provides a
way to retain disagreements; it does not supply those decisions or evidence.
Automatic event grouping and source-independence assessment also remain
separate requirements. Do not close dependent issues from the recorded path
or the invented evaluator demonstration.
