# Mantik architecture

## Product model

Mantik runs autonomous paper-trading research under a human-defined mandate.
The terminal is the primary supervision interface. Conversation may inspect
and direct the system through the same application operations.

Information continuously updates the evidence available to Mantik. Selected
changes open or update a decision case, run an established rule, or enact a
previously authorized action. Research supplies hypotheses and methods when
existing library components cannot adequately handle the situation.

## Responsibilities

Research retrieves literature and prior experiments, formulates testable
hypotheses, composes library methods, and can propose new implementations.
The library should include data transforms, signals, strategies, tests,
allocation policies, execution models, and evidence of their applicability.
A published or correctly implemented method is not automatically profitable.

Decision-making combines deterministic methods with LLM investigation where
interpretation is useful. A decision type declares tools, required evidence,
mandatory evaluations, permitted outcomes, and abstention conditions. Portfolio
allocation owns capital decisions across competing proposals.

Operations monitors authorized conditions, enacts applicable actions, records
outcomes, reconciles state, and handles failures. Protective behavior cannot
depend on a working LLM. Operations reports changed conditions back to the
information stream rather than silently changing the decision's authority.

## Structured evaluation

Evaluation exchanges claims, source references, measurements, uncertainty,
tool results, checks, and candidate actions. It does not feed successive prose
reports between stages. Original narrative sources remain available for
bounded inspection and extraction verification.

Finalize the decision before generating its explanation. The explanation
reads retained evidence; it cannot revise the finalized action. A contradiction
found during explanation opens a review case.

## Contracts

- Observation: source identity, event/receipt times, instrument, content, and
  provenance. The bootstrap supports only a typed fixture quote.
- Trigger: a condition, its behavior (`research`, `decide`, or `act`), owner,
  expiry, and repeated-event policy. The bootstrap records threshold triggers.
- Hypothesis: claim, predicted relationship, refutation conditions, and evidence.
- Strategy version: immutable executable method, required inputs, evaluations,
  and safety policy. The bootstrap version is a fixed synthetic fixture.
- Decision case: the situation being resolved and its lifecycle.
- Evaluation result: versioned check outcome and evidence references.
- Decision: finalized action or abstention, reasons, evaluations, constraints,
  validity period, and conditions for later action or reassessment.
- Execution outcome: what operations actually enacted, with decision identity,
  fill assumptions, and resulting economic effects.

The current Go records are intentionally small. Rich source claims, tool
contracts, and multi-instrument identities will extend them through explicit
schema changes.

## Versioned library

`internal/library` defines `mantik-library-v1` entries for strategies and
components. Each entry declares an immutable version ID, implementation
identity, typed input schemas and sources, supported instruments, outputs,
exact dependency versions, evidence class, safety requirements, and a versioned
evaluation recipe. Registry construction rejects incomplete declarations,
duplicate versions, missing dependencies, and dependency cycles. Readbacks are
copies; callers cannot mutate a registered contract.

Every entry declares notional, stop, target, holding-time, and quote-age safety
requirements. The executable fixture additionally binds its complete numeric
configuration and hypothesis to `demo-threshold-v1`; changing those values under
the same ID is an explicit compatibility error. Registration describes a
method's contract, not its qualification or permission to execute generated
code. Additional executable implementations still need their own adapter and
validation.

Recipes separate mandatory checks from optional investigator tools. Required
checks declare exact versions and applicable outputs. `Admit` rejects missing,
failed, duplicate, unknown, or incompatible evaluation results and undeclared
outputs. An optional tool cannot replace a mandatory check. Evaluation results
carry a check version; retained bootstrap results without that field mean `v1`
only. This additive JSON field needs no database migration.

The fixture requires freshness and event-order checks for both entry and exit,
plus notional and protective-level checks for entry. Workflow finalization and
operations use the same declared recipe; operations independently resolves the
immutable strategy and retains its own quote, position, validity, and numeric
safety checks. Failed evaluations still produce structured abstentions with
their original results. They cannot admit an executable action.

Both registered executables remain `synthetic_unqualified`. The service selects
`demo-threshold-v1`; experiment replay can also select the immutable
`demo-half-size-v1` configuration. Contracts can
describe stress, historical, and prospective evidence, but registration and
passing contract tests confer no paper qualification.

## Trigger scope

Anticipated sources include news, social media, specific watched accounts,
copytrading activity, calendar events, prices, liquidity, thesis changes,
portfolio state, executions, research milestones, data-quality changes,
operational failures/recovery, and human direction.

Underlying mechanisms include event receipt, threshold crossing, state
transition, elapsed time, evidence sufficiency, and an expected event missing.
Several observations may update one case. Define deduplication, reopening,
expiration, and combination rules to avoid repeated workflows.

Social claims, declared trades, observed fills, and observed holdings have
different evidence. Copytrading can be a research input, a decision trigger,
or a bounded strategy. It still needs Mantik's sizing and safety policy.

## Current implementation

`cmd/mantik` assembles signal handling and CLI commands. `internal/cli` offers
`serve`, `demo`, `status`, `observe`, `replay`, and `experiment`.
`internal/service` exposes health,
readiness, status, and fixture ingestion. `internal/workflow` evaluates and
finalizes decisions; `internal/operations` rechecks authority and simulates
fills. `internal/storage` provides memory and PostgreSQL implementations.

`internal/replay` seals provenance-bearing datasets and runs the registered
fixture through a separate memory store at explicit cutoffs. The CLI's `replay`
command exports the synthetic dataset or reads a file pinned by SHA-256. See
[dataset contracts](reference/datasets.md) for correction and quality admission
rules. Dataset artifacts are files outside Git; the service database schema is
unchanged.

`internal/experimentplan` owns the versioned experiment recipe, method catalogue,
content pins, bounded recipe reader and declaration validation. Its application
dependencies are only `domain` and `library`; validating a declared plan cannot
load a dataset, start replay or access storage. This lets future storage-backed
trial registration use the same validation contract without importing the runner.

`internal/experiment` constructs default recipes from validated datasets, runs
independent memory replays, and retains declarations, supplied inputs and
successes or failures as immutable local artifacts. Its existing recipe/method
exports remain aliases and forwarding functions for the shared contracts. The
v1 JSON field order, method hashes, recipe identities, validation rules and CLI
behavior are preserved. Passive and no-action controls remain nonexecuting
components. See [experiment contracts](reference/experiments.md).

`internal/trial` uses the shared declaration package to bind immutable trial
registrations to retained hypothesis publications and optional parent plans.
Storage retains full recipes and server admission times separately from news
cases and execution state. Pre-execution [attempt intents](reference/trial-attempts.md)
retain plan/source pins, pending work and explicit cancellation without dispatching
a worker. The [native execution adapter](reference/native-trial-runs.md) retains
separate run ownership, exact fixture input and full native outcomes, executing
only in independent memory stores after acquisition commits.
`internal/assessment` derives [condition comparisons](reference/trial-assessments.md)
from one consistent snapshot of retained intent, plan, publication and native
result. It preserves benchmarks and absent/failed results without changing
research or operational state. External research workers, statistical hypothesis
evaluation and promotion remain separate work. A per-plan assessment history
pins a bounded metadata index, checks each targeted result against it, and
rejects the entire read if the final index changes. It loads result bodies one
at a time and returns every retained attempt without a winner projection.
The hypothesis-wide view pins every plan's index before reading results and
rechecks the whole registered catalogue and all indices afterward. It validates
parent receipt ancestry, includes plans with no attempts and retains per-plan
histories without pooling financial metrics or selecting a winner.
See [trial registration](reference/hypothesis-trials.md).

`internal/workerplan` declares a versioned offline job/result boundary using the
shared experiment-plan contract, without depending on storage or an execution
engine. `internal/researchworker` supplies a native-fixture adapter with raw and
canonical input pins, bounded artifacts and a cooperative context. The
[worker protocol](reference/research-workers.md) leaves image/isolation
attestation false. `internal/workerlaunch` provides an explicit local Docker
launcher with pre-start acquisition/profile receipts, bounded artifact capture,
ownership checks and reconciliation without automatic re-execution. It supplies
no service dispatch. `research/backtesting` provides a separate fixed-fixture
Python adapter and finite container rehearsal. It binds exact dataset, recipe
and native-reference artifacts, records conversion assumptions and compares
Backtesting.py results without importing them into service trials. Its Python
environment/result contract is separate from the native worker v1 contract.
`internal/externalplan` validates its sealed job and comparison, including raw
source/artifact pins and metric deltas. The shared launcher uses explicit compiled
native/Python profiles with separate acquisition/host-receipt schemas and shared
ownership/deadline/recovery controls. Service dispatch/import remain unfinished. See the
[external fixture comparison](reference/backtesting-fixture.md).

The opt-in `news-method-review-v1` investigation profile exposes compiled
native method references and targeted read-only contract inspection. Its
application-owned `method_binding` check requires candidates to cite methods
successfully inspected in that run. Neutral library pin construction preserves
the existing `native-strategy-config-v1` hashes. Schema 19 lets new service attempts pin their complete selected review profile
and method inventory. Older unpinned attempts keep v1; changed stored profiles
fail before model invocation. Method-aware publication also binds the declared
method set to the inspected candidate. No provider or worker activation is
introduced. See [recorded review profiles](reference/research-review.md#method-aware-local-review-profile).

The local `news-reuse-review-v1` profile derives a bounded declaration inventory
from a validated retained hypothesis catalogue export. Models see only refs
until targeted inspection; checked reuse proposals bind the catalogue and
inventory hashes, current evidence class/instrument and inspected method set.
Neutral pre-test declaration types live in `question`; `hypothesis` preserves
its aliases, wire schemas and hashes while owning publication lineage. This
avoids making investigation depend on publication or storage. The new local
profile is rejected by retained service review start until catalogue pinning
and a storage compatibility fence are implemented. See
[hypothesis inspection](reference/research-review.md#inspect-existing-hypotheses-in-a-recorded-review).

`internal/workertrial` binds a receiver-fenced external job declaration to an
exact completed native trial and its registered recipe. Schema 17 atomically
retains the declaration and three original input texts. The service exposes
targeted export for explicit local launch; declaration does not start a worker,
attest host state or import a result. Schema 18 adds independent external attempt
intents, immutable caller-reported outcomes and exact stdout/stderr texts.
Comparison validation binds source/input pins and arithmetic; it does not verify
that a process ran or establish financial qualification. Complete per-job history
includes pending intents and all reported dispositions, without selecting a
winner. `internal/researchhistory` joins this metadata with native condition
assessments and rechecks every captured catalogue before returning a trial or
hypothesis response. No replay bodies or output streams are loaded for external
history. See [trial worker handoff](reference/trial-workers.md) and
[combined research history](reference/trial-assessments.md#combined-native-and-external-history).

A fresh quote at or below 100.00 can open the fixture case if its notional and
protective levels pass. The decision permits 10 units, at most 1,000.00
notional, a 95.00 stop, a 110.00 target, a one-hour holding limit, and quotes
at most 60 seconds old. A fresh quote crossing a protective level or arriving
after the holding deadline closes the position at its observed price.

Stale or out-of-order observations produce abstentions. Malformed observations
are rejected. A closed case does not reopen. Price and money values use integer
cents; other instruments require precision contracts before admission.

Each ingestion atomically stores its observation, receipt, events, and updated
aggregate. The state row lock serializes multiple processes. Duplicate IDs with
identical content return the retained receipt; changed content conflicts.
Status uses a consistent read transaction. Bootstrap migrations run under an
advisory lock and verify their checksum and schema version at startup.

Persistent definitions route fixture events, thresholds, state changes, and
combined evidence into bounded research or decision cases. Repeat policies
control coalescing and reopening. Routing records do not dispatch workers or
authorize execution. Finalized entry decisions retain exact stop, target, and
holding-deadline definitions in the same transaction as their execution records.
Schema versions 2 and 3 upgrade retained open positions using the original
finalized decision and fence incompatible older writers. See [trigger contracts](reference/triggers.md)
for evidence windows, deduplication, API commands, and compatibility boundaries.

The service's independent timer worker leases due definitions and commits their
routing and protective effects atomically. Restart recovers overdue or expired
claims with generation fencing. A backward UTC change pauses scheduler work
until the durable clock watermark catches up. Holding-deadline exits require a
usable retained quote at execution time; otherwise an operational exception
preserves the open position without inventing a fill. Expected-event conditions
record missing evidence without relying on a new input or model. See
[timer contracts](reference/timers.md) for the clock, lease, and evidence rules.

Persistent ingestion and protective timers now atomically queue finalized
actions instead of enacting them inside their source transaction. A separate
worker reconciles each stable idempotency key, rechecks current applicability,
records submission intent, and commits the synthetic adapter effect separately
from acknowledgment. Leases fence crashed workers. Accepted, partial, and unknown
results remain distinct from completed effects. Local replay retains synchronous
fixture execution. See [action contracts](reference/actions.md).

The adapter remains synthetic. Real providers need their own verified delivery
and reconciliation guarantees; a database transaction cannot make an external
order atomic. Schema version 4 preserves historical outcomes and initializes an
empty queue without resubmitting them.

Operational records preserve worker cycle observations and exception identity,
capability, severity, evidence, recovery attempts, and terminal outcomes.
Readiness exposes process, storage, feed coverage, action delivery, and protective
monitoring separately; a fixture scope does not imply real feed coverage.
Authenticated recovery rechecks conditions, reconciles existing effects, or
reevaluates an overdue safety obligation with current evidence. It cannot widen
validity, steal live leases, or force a fill. Schema version 5 preserves older
audit payloads while adding worker state and exception metadata. See
[operational contracts](reference/operations.md).

## Next capabilities

- Additional library implementations and reproducible experiment recipes.
- Real-feed availability policies built on declared missing-event obligations.
- Historical dataset adapters and research-attempt history, including failures.
- A real feed adapter with provenance and time-aware routing.
- LLM tools and research workers behind explicit application interfaces.
- Multi-case portfolio allocation and paginated/event-stream readbacks.
- Isolated generated-code evaluation and admission.
- TUI supervision and explanations derived from finalized records.

The intended future live execution adapter is Alpaca, after paper qualification
and a separate live-admission review. Current operations remain paper-only. A
live adapter must preserve allocator ownership, finalized-decision safety,
environment/account isolation, and reconciliation of unknown or partial effects.
Paper decisions and fixture effect records cannot authorize live orders. See
[the planned live stage](roadmap.md#planned-m8-alpaca-live-admission-and-bounded-rollout).
A healthy service or successful fixture creates no real-market qualification
evidence.
