# Recorded research contract audit, 2026-10-15

This audit maps issues [15](https://git.subcult.tv/subculture-collective/mantik/issues/15),
[16](https://git.subcult.tv/subculture-collective/mantik/issues/16) and
[17](https://git.subcult.tv/subculture-collective/mantik/issues/17) to the
implemented recorded-fixture contracts through
[PR 97](https://git.subcult.tv/subculture-collective/mantik/pulls/97), merged as
`bfb1a1755e33c961de9d2586159b271b38287f7c`.
It separates contract coverage from dependency completion, automatic research,
deployment and financial qualification. It does not close the M4 exit gate. The extraction checkpoint below supplements
this PR 97 baseline through PR 102.

## Decision workflow and tool contracts — issue 15

| Acceptance criterion | Delivered contract and verification | Limit |
| --- | --- | --- |
| Each type declares evidence, tools, outcomes, mandatory checks, completion bounds, timeout and abstention | The three compiled news-review profiles declare their complete contracts. New service attempts retain the selected profile and hash; unknown or changed profiles fail before a model call. Legacy attempts preserve their original interpretation. | These are the supported research-review types. Other decision domains and provider-backed model execution are not implemented by this contract. |
| Typed claims, references, uncertainty, candidates and checks; targeted source excerpts only | Frames expose references and summaries. Explicit inspection returns the selected claim excerpt, native method or sealed hypothesis. Typed proposals carry uncertainty and candidate bindings; application checks run independently. Raw malformed artifacts remain retained. | Syntactic and provenance checks do not establish source truth, extraction accuracy, semantic relevance or model usefulness. |
| Tools cannot change risk, grant authority, self-qualify or narrate before finalization | Strict artifact schemas reject risk/qualification/narrative fields and unknown tools. Finalized results retain `research_only_unqualified` authority and false qualification credit. Completion does not publish a hypothesis or create an operation. | Passing review checks is not trading admission. No provider or automatic dispatch is activated. |

See [review contracts](../reference/research-review.md) and
[retained attempts](../reference/retained-reviews.md). Executable coverage includes
`TestTargetedReviewAndNoExecutionAuthority`,
`TestMalformedArtifactsAndAuthorityAttempts`,
`TestEvidenceRequirementsAndProposalShape`,
`TestBoundedCompletionAndCancellation` and
`TestRetainedProfileRefusesChangedContractBeforeModel` in
[`internal/investigation`](../../internal/investigation).
Memory and disposable PostgreSQL contracts also cover concurrent completion,
atomic persistence failure, legacy upgrade, stale evidence, deadline expiry,
restart and immutable retries in
[`internal/storage/reviews_test.go`](../../internal/storage/reviews_test.go).

## Hypothesis lifecycle and search history — issue 16

| Acceptance criterion | Delivered contract and verification | Limit |
| --- | --- | --- |
| Declare mechanism, prediction, null/refutation, horizon and methods before testing | Sealed declarations bind these fields and exact native method pins. A trial registration loads an authoritative publication and retains the recipe, dataset identities and optional parent before execution through its protocol. Native assessments compare all declared integer conditions. | Current executable declarations support the synthetic DEMO methods. Registration does not prove a dataset was unseen or that an experiment never ran outside Mantik. |
| Reuse existing hypotheses where evidence matches; separate case and research validity | The reuse profile captures a server-owned catalogue inventory at attempt start. Targeted inspection and publication independently require an exact declaration match. New labels/origins retain their own publication receipts under the same exact question ID; changed conditions, horizon or methods do not silently update it. Case version/generation determines current review applicability without rewriting historical research. | Reuse is exact structured matching. Semantic similarity, general hypothesis revision and research validity are not inferred. |
| Retain lineage, parameter searches, failures, contradictions and abstentions without selected-winner promotion | Case history includes every retained review, including unpublished abstentions and pending/expired/superseded work. Hypothesis history includes every registered parent/sibling plan, native attempt and declared external job/attempt. Condition gaps, null/refutation matches, failed/incomplete work and mismatches remain visible. Reports provide no winning-plan selector or qualification authority. | Coverage is the complete bounded application ledger, not every experiment a caller could perform elsewhere. Statistical multiple-testing controls and strategy promotion remain future gates. |

See [declarations](../reference/hypothesis-declarations.md),
[publications](../reference/retained-hypotheses.md),
[trial registration](../reference/hypothesis-trials.md),
[condition and combined histories](../reference/trial-assessments.md) and
[external attempts](../reference/trial-workers.md#retain-every-external-attempt).
Source checks include declaration identity and conflicting-source tests in
[`internal/hypothesis`](../../internal/hypothesis),
reuse inspection/binding tests in
[`internal/investigation/hypotheses_test.go`](../../internal/investigation/hypotheses_test.go),
`TestActualNativeAssessment`, `TestConditionGapsAndPredictionMatch`,
`TestNonEvaluableAttempts`, `TestHistoryRetainsEveryAttemptAndNoWinner` and
`TestQuestionHistoryPreservesAllPlansAndAttempts` in
[`internal/assessment`](../../internal/assessment).
Case-history corruption and closure-snapshot tests cover both memory and
PostgreSQL stores; no filtered partial success is returned.

## Reusable isolated workers — issue 17

The three declared protocol criteria are delivered within the fixed fixture
profiles: versioned inputs/environment/resource/output pins; credential-free
isolated execution with retained cancel/timeout/retry identity; and a pinned
Backtesting.py 0.6.6 comparison against all four native synthetic methods.
See [worker protocol and launcher](../reference/research-workers.md),
[external comparison](../reference/backtesting-fixture.md) and
[service-owned declarations and attempt reports](../reference/trial-workers.md).

Actual host launcher evidence remains distinct from imported caller-reported
streams. A stream cannot attest its own process isolation or liveness. The
fixed launcher does not execute arbitrary generated code, supply credentials,
dispatch automatically from the service or certify a general-purpose sandbox.
Successful fixture comparison grants no qualification credit.

## Retained execution evidence

Private evidence stays outside Git. The receipt names below locate retained
runs under the operator's Mantik state directory; source trees and hashes bind
the artifacts that were actually exercised. Later source is not covered merely
because an earlier rehearsal passed.

| Rehearsal receipt | Observed result |
| --- | --- |
| `external-launch-rehearsal-1792108422015833666`, tree `6aad9173346306fed9605138985ca9f4b19bcc99` | Fixed external protocol; all four methods, retained mismatch/malformed results, actual launcher losses, cancellation and real timeout. Final eight container handles inspected stopped. See issue 17's delivered-protocol record and PRs 85–88. |
| `combined-research-history-rehearsal-1792112341232088638` | Three plans, five external job declarations and six external attempt intents, including pending work; combined history reproduced after restart/restore. Final seven containers inspected stopped. PR 91 retains the implementation and verification record. |
| `retained-reuse-rehearsal-1792118760982116174`, tree `ee5062275b3053343cfc9dc4900e5bb62d152958` | Schema 19→20 preserved older attempts. Current-origin reuse, future-declaration exclusion, changed-question rejection, atomic write failure, stale/real-expired retention and restart/restore passed. Four final containers inspected stopped. |
| `case-review-history-rehearsal-1792120129613034263`, tree `4b498294228993c2a983d5bb7d077e2c210f480a` | Fourteen reviews: eight completed, two pending, two expired and two superseded; four abstentions and one rejected tool call retained. Every summary's attempt hash matched targeted readback. Restart/restore were exact; a corrupt clone failed the whole history request. Three app processes exited and the disposable database was stopped. |

The PR 96 restore dump hash is
`bdb2fb55ea54751bb5cc749f8342eb71e6035cb43445b2abdb1df5b35012053d`.
The PR 97 history hash is
`401cb76ce6f0b0f5b6bde644138a1e8c3228b544523796652a32d4b90494b954`;
its restore dump hash is
`d316bb344631ea3044eb4a77dbf2d9515c6a0d90ba348ac0420396d56609b03a`.
Both runs recorded zero provider requests and zero fixture economic effects.
Their private final audits separately checked actual process/container handles.

Failed attempts remain retained. In particular, the first PR 96 rehearsal reused
an existing immutable request ID from its restored backup and correctly received
409; its three containers were inspected stopped. The corrected driver used
fresh request IDs in a separate rehearsal. Earlier launcher/test failures remain
in their own receipts and PR verification records. A later pass does not delete
those failures or expand the tested source scope.

The implementation PRs record local verification, race checks, disposable storage
checks and applicable container builds separately from hosted CI and merges.
The PR 97 head `0e058175512aebd99ab6003781d1fb7d0b6e5bde` passed hosted CI and
was merged through the normal protection checks. This audit does not treat a
PR-head pass as merged-main CI, deployment or provider qualification.

## Historical dependency checkpoint, 2026-10-15

This section records the dependency state at the original audit. The
2026-10-19 refresh below supersedes its open-issue and next-step statements.

Issues 3, 5, 6 and 9 were closed at this audit. Issue 12 remains open for the
extraction work described in the [recorded-news audit](news-acceptance.md).
The recorded extraction path now retains source-only intents and checked
recorded artifacts, with a separate explicit provenance-checked approval path
into news cases. The offline evaluator retains agreement and disagreement with
supplied annotations. These additions do not discover claims by themselves or
prove model accuracy. Event keys remain operator supplied; automatic event
equivalence and source-independence assessment remain absent.

Therefore issue 15 remains open on dependency 12; issues 16 and 17 remain open
on dependency 15. Delivered contract coverage above does not bypass native
dependencies. Reconcile the acceptance checklists and close issues only after
the remaining requirements and dependencies are verified. The M4 milestone also
retains its integrated workflow and generated-candidate requirements.

PRs 99–102 delivered the recorded extraction contract, durable attempt ledger,
explicit candidate admission and offline supplied-annotation evaluator. See the
[updated news checkpoint](news-acceptance.md#recorded-extraction-checkpoint-through-pr-102)
for per-boundary coverage, source-pinned execution evidence and retained failed
attempts. The next extraction unit requires model/adapter selection and a
reviewed corpus, followed by a bounded actual-extractor evaluation. Any provider
rehearsal also needs explicit access, source and request/cost scope. Recorded
artifact checks and invented agreement cases cannot close that quality gap.

Generated-candidate evaluation in issue 18 follows its declared dependencies.
It needs immutable source/hypothesis/dataset/toolchain provenance and a separately
bounded credential-free worker, including failed compilation and retry evidence.
Existing fixed-method launchers do not establish that capability. Portfolio
allocation and realistic economics remain issues 19–21. None of these results
establish prospective paper profitability or authorize Alpaca orders.

## Current contract and dependency refresh, 2026-10-19

At source `69303bc35d64b180109fbbf0288a288203ad5f7d`, Gitea reports #3, #6,
#9 and #12 closed. The earlier dependency preventing #15's closure is therefore
resolved. The supported contracts mapped above remain implemented: three
versioned recorded-review profiles; typed source/method/hypothesis inspection;
application-owned mandatory checks and abstention; sealed declarations; exact
structured reuse; and complete bounded application attempt/lineage readbacks.
This refresh supports closing #15 and #16 for those contracts. Their closure
does not assert automatic research dispatch, general semantic hypothesis
revision, model usefulness or strategy admission.

The fresh native rehearsal
`research-contract-refresh-1792453722397604469` built the CLI from that source,
started a new owned PostgreSQL 17 container and application process, and retained
ten reviews across all three profiles. Its private result and CLI readbacks
verify:

- A targeted source inspection followed by a checked evidence request. An
  unselected marker in the retained original never appeared in the result.
- Malformed model fields remained in the retained artifact chain and produced
  abstention. An unknown `submit_order` tool was rejected; a proposal referencing
  an uninspected claim failed evidence binding. A tool-only stream finalized as
  incomplete abstention. No tool obtained execution or qualification authority.
- A method-bound hypothesis was published with a predeclared native recipe,
  then executed through a retained native attempt. Reuse inspected the sealed
  declaration and produced a second publication of the same question. Changed
  horizon publication was rejected rather than silently modifying it.
- Pending and completed attempts survived a real application restart exactly.
  A later source receipt superseded a pending review, and a real wall-clock
  deadline produced expired retention. All ten review readbacks, complete case
  history and hypothesis research history reproduced exactly after dump/restore
  into a separate database and process.

The restored dump SHA-256 is
`9dfbb164c92dd8d247b5981f75ab36cf2dc7fef381f3f94351e496b29257eaa3`.
The tested binary SHA-256 is
`ea8ee717ac6a2167352abb0ce904ffafb8170e32db0d59c6a2860215c25ff5f3`.
The run recorded zero provider requests and zero fixture economic effects.
Actual original pilot/Switchyard PID/image handles were unchanged; the owned
application was stopped and its disposable database container removed, with a
cleanup receipt retained outside Git.

The first refresh attempt,
`research-contract-refresh-1792453705993833946`, supplied an empty artifact
array to the public completion command. The command correctly rejected it as
an invalid recorded-review command before interpretation. The private harness
was corrected to provide one targeted tool artifact without a final proposal;
the fresh run above exercised incomplete interpretation successfully. The
failed attempt, classification and cleanup receipt remain retained. No
application fix was needed and that attempt is not counted as a pass.

Current-source `make verify`, `make test-race` and `make test-integration`
passed; integration used new disposable containers. The fresh PostgreSQL
rehearsal supplements the earlier source-pinned upgrade and failure evidence;
it does not reclassify that older evidence as a current-source run. Unit and
storage regressions retain coverage of failed attempts, sibling/parent trial
lineage, contradictory/null/refutation conditions, corrupt history rejection
and complete bounded history without a winning-plan selector. Relevant tests
include `TestHistoryRetainsEveryAttemptAndNoWinner`,
`TestQuestionHistoryPreservesAllPlansAndAttempts`,
`TestConditionGapsAndPredictionMatch`, and the investigation checks listed above.

Issue #17 still needs its own dependency/launcher acceptance reconciliation
before closure; the native research rehearsal does not establish credential-free
external process isolation. Issue #18's generated-candidate worker is still
absent. Account allocation (#19), realistic paper economics (#20), automatic
qualification (#21) and the integrated M4 exit gate remain incomplete. New
provider/model requests require their own concrete scope; the earlier approved
extractor diagnostic budgets remain exhausted.

## External worker protocol refresh, 2026-10-19

After #15/#16 closed through PR #135, Gitea's #17 readback showed all three
protocol acceptance criteria checked. Its recorded closure blocker was the
previously open #15 dependency. Dependencies #5 and #15 are now closed. A fresh
launcher rehearsal at source `c8669b450efb388fc7b5133385b79c8d1623b494`
supports closing this protocol issue; arbitrary generated code and automatic
service dispatch remain outside that gate.

`external-launch-rehearsal-1792454271230220804` retained 48 commands and eight
actual worker handles. It ran the pinned Backtesting.py 0.6.6 comparison twice
against all four native synthetic methods, with equal logical result identity
and distinct physical acquisitions. It retained a deliberate metric mismatch
and rejected an adapter-source mismatch. Two real launcher-process losses,
explicit cancellation, a real 30-second host timeout, and recovery of a timely
orphan completion after the deadline passed. Native v1 execution/reconciliation
and cross-protocol rejection passed. All eight handles were inspected stopped
with PID 0; raw streams, pre-start profiles, acquisition/terminal receipts and
failed dispositions remain outside Git.

The host binary SHA-256 was
`ea8ee717ac6a2167352abb0ce904ffafb8170e32db0d59c6a2860215c25ff5f3`.
The fresh native image ID was
`sha256:fe62dd265dd0356350d420edd29cffcbd15278250ddaff34c857979bfe164a07`;
the pinned external image was
`sha256:1f04795ca24887313f102867e69b876d572520fa35c9231e11d839becd6d9c30`.
The launcher inspected image/ownership and resource settings independently of
worker declarations. A host credential canary was not forwarded into workers.
No service API, model/provider or operational account was used. This is protocol
and process-isolation evidence, not a sandbox security certification or financial
qualification. The source for this rehearsal precedes the candidate declaration
CLI; it does not establish candidate execution.

The separate [generated-candidate acceptance audit](candidate-evaluation-acceptance.md)
now maps issue #18 to immutable source/generation/registration/attempt lineage,
fixed isolated Go compile/behavior workers, finite sample properties, cutoff replay
and retained failure/restart/restore evidence. Existing native/Python fixture
workers remain separate profiles; generated source runs only in its explicit
bounded candidate profile. This issue-level fixture evidence does not complete
general allocation (#19), realistic economics (#20), admission (#21) or the
integrated M4 gate. The [paper-soak plan](../plans/paper-soak.md) records the
remaining full-application exit requirements.
