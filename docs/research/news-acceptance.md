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

## Local model and capture checkpoint through PR 109, 2026-10-16

PRs 104–109 added a local endpoint adapter, quote-only extraction with
application-derived spans, offline retained-receipt inspection, schema-23 capture
imports and schema-24 explicit reviewed local admission. PR 109 merged as
`6196f7be9e1000c16b08cce8c8c03bda1553ee82`; its exact-head and merged-main CI passed.
The provider boundary described in the earlier checkpoint has been crossed only
for the explicitly bounded local-model synthetic rehearsals.

| Boundary | Verified scope | Remaining limit |
| --- | --- | --- |
| Local extraction | Retained prepared/finished bytes; bounded local compatibility request; deterministic unique-quote anchoring; original model content retained separately from derived candidates | The earlier offset-based local result was invalid and remains retained. A later quote-based result passed structural checks on one invented article. Neither establishes real-news accuracy. |
| Capture retention | Separate caller-reported imports, including unknown and failed completion; immutable receiver-bound records, bounded complete history and replay on reads | Imported files do not attest provider execution, timing or source truth. Service-owned model dispatch and recovery remain separate work. |
| Explicit local research admission | Exact capture/result approval pins; atomic news provenance and receipt; correction/retraction handling; downstream recorded review startup; restart, restore and rollback checks | Approval records explicit caller intent. It does not qualify the source, model, strategy or economic results. No automatic local approval or operational action is added. |
| Cohort evaluation | [Local capture evaluation](../reference/local-extraction-evaluation.md) retains full snapshots, compares finished results against supplied annotations and exposes unknown completion separately | This is annotation-agreement tooling. A reviewed real-source corpus, representative sampling and measured actual-extractor cohort remain unproven. |

The schema-23 rehearsal is retained privately at
`local-capture-rehearsal-1792167181426656015`, source tree
`d5cf650bd549eeb4683f0d72154f912244fd9ad0`: 58 recorded commands covered upgrade,
three capture types, exact retries, restart/restore, corruption rejection,
older-binary refusal and backup-based rollback.

The schema-24 rehearsal is retained privately at
`local-admission-rehearsal-1792169396056760889`, source tree
`9bdf9fc906379163ea15fce7cf2ed81a1a6efdc2`: 86 recorded commands covered upgrade,
explicit admission of one previously captured synthetic candidate, preservation
of the original captures and older recorded approval, recorded research-review
startup, restart/restore, resealed metadata and broken-link rejection,
older-binary refusal and a separate pre-upgrade-backup restore. It recorded zero
new model requests, economic effects and qualification credit. Its four owned
stacks were inspected stopped at completion; original pilot handles were
unchanged and the curtailed observer remained inactive. These are source-pinned,
same-host historical observations, not deployment or new 24-hour proof.

### Next implementation boundary

Issue 12 remains open. The selected local-model path can now retain and explicitly
admit checked candidates, but accuracy on real news requires a separately
reviewed corpus with permitted source material, sampling and exact expectations.
Confirm finite source, request and access bounds before another actual-extractor
cohort. The evaluator preserves disagreements; it cannot supply independent
annotations or a representative sample. Automatic event grouping, syndication
and source-independence assessment remain separate requirements. Do not close
dependent issues from the synthetic rehearsal or annotation-agreement counts.
