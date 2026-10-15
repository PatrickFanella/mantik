# Bounded recorded-model news review

`research-review` runs a bounded investigation against one audited local news
event snapshot. It implements the first review contract for issue #15 using a
private array of recorded model artifacts. It does not call a model provider,
dispatch a service worker, close a service case, publish a hypothesis or authorize
operations.

```sh
./bin/mantik research-review --export-contract
./bin/mantik news-events --journal /absolute/private/news-journal --as-of 2026-10-15T18:00:00Z
./bin/mantik research-review --journal /absolute/private/news-journal \
  --event NEWS_EVENT_GROUP_ID --as-of 2026-10-15T18:00:00Z \
  --model-fixture /absolute/private/model-artifacts.json > /absolute/private/review-result.json
```

The fixture file is a JSON array of 1..16 model response objects, at most 128 KiB.
Use private paths and retain the input fixture alongside the resulting output.
The CLI reads the journal and fixture without changing their content. Output
includes `mode: recorded_model_fixture`; it is not provider behavior or an
assessment of model quality. No review ledger or restartable investigation is
created by this command.

## Contract and tools

Type `news-review-v1` uses schema `mantik-news-review-v1` and an audited
`mantik-news-events-v1` source. The application owns its fixed tool allowlist,
outcomes, required evidence, mandatory checks and completion limits. A model
cannot supply a replacement contract, check record, risk policy, qualification
class or execution permission.

The runner audits the entire journal at an explicit receipt cutoff and pins one
event group by its logical journal, evidence class, group ID, cutoff and canonical
group SHA-256. Later evidence cannot change that pinned view. The first model
frame contains only this subject, the contract, claim keys, lineage states and
claim codes. It contains no full article or source assertion text.

The only tool is `inspect_news_claim`, taking one exact key from that snapshot:

```json
{"kind":"tool","tool":{"name":"inspect_news_claim","claim_key":"NEWS_CLAIM_KEY"}}
```

A successful result includes that claim's typed assertion, uncertainty assessment,
entities/instruments, exact selected excerpts, source/article/revision identity,
recording and original digests, source times and retained record reference. It
never includes an `original` article field. A short article may fit inside a
selected excerpt; the tool still follows the recorded 1..4 span and 2,048-byte
per-span limits. A key outside the snapshot returns a typed rejection. An unknown
tool terminates the run with an authority-boundary abstention.

Subsequent frames receive only typed tool results, not earlier raw model
artifacts, prose reports or full articles. Frames are detached copies; mutation
of a model's frame cannot alter the application's contract, evidence or checks.
Source assertions and excerpts remain untrusted evidence, not instructions.

## Proposals and independent checks

Permitted outcomes are `abstain`, `request_evidence` and `propose_hypothesis`.
Every proposal declares 1..4 distinct reason codes and `uncertainty: unverified`.
Non-abstention requires 1..4 distinct claim keys successfully inspected during
that run. The model cannot cite an uninspected source merely because it saw the
claim's key in the initial frame.

```json
{"kind":"proposal","proposal":{"outcome":"request_evidence","reason_codes":["independence_not_assessed"],"claim_keys":["NEWS_CLAIM_KEY"],"uncertainty":"unverified"}}
```

`propose_hypothesis` additionally requires one candidate with kind
`research_hypothesis`, distinct bounded `claim_code` and `refutation_code`, and
exactly the proposal's evidence keys. Candidate evidence must be current in the
pinned lineage and assessed `reported_unverified`. Superseded, disputed or
retracted claims can inform an evidence request, but cannot support a hypothesis
candidate in v1. A candidate is an unqualified research declaration, not an
executable method or approved trading strategy.

The application constructs versioned check records for `model_schema`,
`completion`, `tool_budget`, `evidence_binding` and `authority_boundary`. Each
reports `passed`, `failed` or `not_run`; an unevaluated check does not receive a
passing result. A valid explicit abstention may omit evidence and a candidate.
Model JSON cannot supply check results. Unknown, duplicate or noncanonical field
names, invalid variants, trailing values, excessive nesting and oversized
artifacts produce a structured abstention.

## Limits and finalization

The runner permits at most eight model turns and four tool calls. Each artifact
is at most 16 KiB. It supplies a 30-second context deadline, or inherits an earlier
caller deadline, and checks cancellation before and after every model call.
Malformed/incomplete output, unavailable models, timeout, budget exhaustion,
ineligible evidence and attempted authority changes finalize as abstentions.
Invalid journal or subject selection fails before a model is called.

The Go `Model` interface is cooperative: implementations must honor cancellation.
A callback that never returns cannot be forcibly stopped by this runner. The
runner does not detach an abandoned goroutine. Any future provider adapter must
also bound transport response size and enforce network deadlines, with separate
provider verification before activation. Recorded fixtures and cancellation-aware
tests do not prove termination of an arbitrary provider client or process.

Final output contains the pinned subject, tool records, model artifact hashes,
byte counts, bounded valid-UTF-8 raw artifacts, original checked proposal when
accepted, application checks and finalization time. Oversized or invalid-UTF-8
artifact bodies are omitted from output; retain the original fixture privately.
Raw artifacts are audit data after finalization and are never fed back as
interpretation context. An invalid proposal remains in that audit data, not in
the checked proposal field. The result ID hashes the finalized record before
adding the ID; reruns have their own finalization time and result identity.

Every result is `finalized` with `authority: research_only_unqualified` and
`qualification_credit: false`. This result schema lacks the strategy, safety,
quantity, validity and action required by operations. Existing fixture operations
reject an attempted conversion. No explanation generator is included; any future
explanation must consume a finalized record without changing its outcome.

Service-case/version binding, retained review attempts, worker leases/restarts,
provider clients, hypothesis publication, evaluation recipes and additional
decision types remain separate implementation units.
