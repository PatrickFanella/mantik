# Generated-candidate evaluation acceptance

Issue [#18](https://git.subcult.tv/subculture-collective/mantik/issues/18) covers
immutable generated-source candidates, isolated checks and retained failure
history. This audit maps its written criteria to the delivered first Go/synthetic
profile. It supports issue closure after this change merges with exact-head CI;
it does not close the integrated M4 gate or authorize strategy activation.

The application evaluates caller-recorded generated-output fixtures. It does not
claim that a provider generated those bytes, that a declared model label is
verified, or that automatic model-generation dispatch is delivered. No new model
request was needed for the issue's declared generated-fixture verification.

## Requirement mapping

| Written criterion | Implementation and verification | Boundaries |
| --- | --- | --- |
| Immutable implementations tied to source, hypothesis, dataset and toolchain | `internal/candidate`, `candidategeneration`, `candidatelineage` and `generatedcandidate` retain exact source/output and full content pins. Schema 35 atomically commits the registration, exact dataset, link and marker from owner-retained generation/publication evidence. Shared storage and HTTP/CLI checks exercise changed bindings, full-prefix corruption, parent lineage, concurrency, capacity, exact retries, failed insertion rollback and upgrade/reopen. The fresh native run registers and exports four complete provenance chains. | A portable hash is not independent authorship or completeness attestation. Existing manual registrations cannot be retrofitted with a generation link. |
| Interface conformance and isolated example/property/replay/failure checks without provider/service credentials | Fixed `Entry(int64) bool` compile and behavioral profiles use pinned Go 1.26.9 images and owned containers. Current contract tests cover fixed compiler/harness identities, type errors, cancellation, bounded diagnostics and strict results. Fresh native workers exercise matching and mismatched examples, a real compiler type failure, a partial panic, predeclared sample relations and two cutoff-derived fresh workers. Actual Docker inspection checks ownership, image, network, filesystem, credentials and resource limits. | The first profile is single-file Go without imports and a bounded synthetic `DEMO` interface. Finite relations are not universal proofs; replay is availability-cutoff input projection, not unseen research or economic replay. This is not a security certification. |
| Retain failed/incomplete candidates; code validation cannot activate or imply profitability | Six service-issued worker intents retain five outcomes and one unreported request. A mismatch, compile failure and partial panic remain inspectable; failed generator recordings are covered by shared transport/storage tests. Exact retry/reconciliation reuse the same physical handle. Full lineage/capture/history survive actual service restart and isolated logical database restore. Candidate, generation, property, replay and outcome evidence retain their explicit limited authority and false qualification flags. | Failed or pending work is not filtered away. Seven candidate workers produced no operational observations or economic effects. No admission, account selection, strategy activation or real order path was added. |

Dependencies #16 and #17 were read from Gitea and confirmed closed for this audit.
Their recorded research and isolated-worker contracts remain separately scoped.

## Fresh native cohort

The host application/API/CLI code and tests were frozen at staged tree
`939fce63b86f9d8be7f37b3ecf865333de4275dd`, on base
`51565b46ae4420bfd2fce0d8b80eb7cc3861ada0`. Acceptance/roadmap prose was updated
afterward; those documentation changes do not replace the source identity of the
observations below. Host binary SHA-256:
`df1612c3501093b30fa49921a99679eff17668885b229c106e73bf06e47d61c0`.

The cohort used retained behavior-v1 image
`sha256:3176af333797561d2034b8bab033aecdb2e316e281903d44a2261781aaa38e4e`.
This was an existing immutable worker image, not a newly built current-revision
image. The fresh host exercised the compatible fixed protocol against that exact
image; the receipt does not relabel the worker's build provenance.

The run retained four owner-issued generation intents and recordings, four linked
registrations, six worker intents, five service-reported outcomes and one pending
unreported intent. Five service-bound workers produced two completed example
checks, one annotation mismatch, one compiler type failure and one partial panic.
The panic preserved two emitted values; the compiler failure preserved an empty
value list and its build failure evidence.

Property requirements were sealed before native execution. The matching and
annotation-mismatch candidates both satisfied the declared nonincreasing sample
relation; the latter still reported `annotation_match=false`. Partial and compiler
failures produced incomplete property reports and no qualification credit.

Three retained replay frames contained no visible input, the first visible quote,
and all four original quotes. The empty frame supplied no executable artifact.
The two nonempty frames launched distinct fresh workers, preserving the original
source/hypothesis/toolchain and explicitly rebinding the projected dataset. The
full parent exposure list remained visible. This does not prevent source authors
from having seen future data or establish out-of-sample performance.

Each worker's actual configuration had UID/GID 65532, network `none`, read-only
root and `/worker` input, dropped capabilities, no-new-privileges, no restart,
256 MiB memory/swap, one CPU, 64 PIDs and the fixed bounded temporary filesystem.
Service/database credentials and the host credential canary were absent from its
environment. Ownership labels and immutable image/job pins were checked against
its own acquisition record.

After service restart and isolated database restore, all full linked registration
artifacts, worker histories and exact captures remained identical. The original
receiver identity was preserved. This was an isolated logical restore on one host,
not off-host disaster-recovery or general portfolio recovery qualification.

Raw evidence remains outside Git under retained cohort identifier
`generated-candidate-service-rehearsal-1792537591067077659`. Its manifest,
181-command log, structured results, raw streams, backup, completion audit and
terminal-handle verification remain together. All seven worker handles and the
owned PostgreSQL container were independently inspected stopped with PID 0; all
three owned app processes exited successfully. The five original pilot/Switchyard
handles retained their IDs, images, PIDs and start times.

The preceding failed cohort,
`generated-candidate-service-rehearsal-1792537550205875762`, remains retained.
Its driver expected an outdated pending-ID complement after adding a compile-failure
intent. Actual history correctly contained that intent. All six then-owned
container handles were inspected stopped before correcting only the driver
assertion and using fresh resources. No application evidence was rewritten and
no worker/container was reused to turn that failed attempt into a pass.

## Verification and remaining delivery

The changed source passed `mise exec -- make verify`, `make test-race` and the
new disposable PostgreSQL `make test-integration` suite, including shared HTTP/CLI
contracts and full artifact readback after reopening the database. The native
cohort is separate runtime evidence. Exact-head hosted CI and protected merge are
separate delivery gates, not inferred from these results.

The new authenticated commands are `generated-candidate-register`,
`generated-candidate` and `generated-candidates`; see the
[registration contract](../reference/generated-candidate-lineage.md).
They expose the existing backend without changing schema 35 or the legacy
registration wire. Strict requests reject source substitution and ambiguous JSON;
clients rederive complete artifacts and check expected pins before any export.

This acceptance scope does not establish model authorship, sample truth,
profitability, scored qualification or automatic promotion. General allocation,
realistic execution, admission/retirement, social/copy policies, conversational
supervision, attribution and release-specific recovery/readiness remain required
before the selected all-issues soak. Follow the
[paper-soak delivery plan](../plans/paper-soak.md); issue #27 is the eventual
natural window and report. Existing services and the curtailed observer were not
replaced or restarted by this work.
