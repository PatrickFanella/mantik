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

The default `news-review-v1` profile permits only `inspect_news_claim`, taking
one exact key from that snapshot:

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

[Retained service reviews](retained-reviews.md) separately bind case versions
and preserve recorded-model attempts and stale completions. The local command
does not write that ledger. Automatic dispatch, worker leases, provider clients,
hypothesis publication, evaluation recipes and additional decision types remain
separate implementation units.

## Method-aware local review profile

`research-review --type news-method-review-v1` selects a separate application-owned
contract with schema `mantik-news-method-review-v1`. The default remains
`news-review-v1`. The local recorded
fixture runner supports this profile; [retained service reviews](retained-reviews.md#explicit-retained-review-profiles)
can also select it explicitly at start while preserving older v1 attempts. It does not
publish a hypothesis, register a trial, execute a method or call a provider.

```sh
./bin/mantik research-review --type news-method-review-v1 --export-contract
./bin/mantik research-review --type news-method-review-v1 \
  --journal /absolute/private/news-journal --event NEWS_EVENT_GROUP_ID \
  --as-of 2026-10-15T18:00:00Z \
  --model-fixture /absolute/private/method-review-artifacts.json
```

The initial model frame adds only the two built-in native strategy references:
schema, ID and exact method SHA-256. It includes no full method configurations
until requested. These pins use the same `native-strategy-config-v1` byte
contract as hypothesis declarations. Pin construction now lives in the neutral
library module; existing declaration/configuration hashes are unchanged.

The new tool takes an exact advertised ID and SHA-256:

```json
{"kind":"tool","tool":{"name":"inspect_native_method","method_id":"demo-threshold-v1","method_sha256":"EXPECTED_NATIVE_METHOD_SHA256"}}
```

A successful typed result includes that reference and the compiled native method
configuration: library inputs, instruments, outputs, safety requirements,
mandatory evaluation definitions, strategy parameters and safety limits. Reading
these definitions does not run their evaluations or produce passing evaluation
results. Unknown IDs or wrong hashes return typed rejections without a method
body. A method call cannot include a nonempty claim key; a claim-inspection call
cannot include nonempty method arguments. No tool can supply replacement code,
change configuration, write files or operate a worker.

A `propose_hypothesis` candidate must add a `methods` array containing one or two
distinct references in the same `{schema,id,sha256}` shape. Every reference must
have been successfully inspected during that run. Missing, duplicate,
uninspected, incompatible-schema and changed-hash references finalize an
abstention with `method_not_eligible`. The application adds mandatory
`method_binding` to the original five checks; model JSON cannot supply its
result. The existing current-news-claim eligibility checks still apply.
Explicit abstention and evidence requests need no method candidate.

Method binding establishes the identity of proposed research methods. It does
not establish their relevance to the claim, produce a complete pre-test
hypothesis declaration, qualify their evidence or grant execution permission.
The result retains `research_only_unqualified` authority and false qualification
credit. Subsequent hypothesis publication still requires the separately
validated declaration and an applicable retained service review with the same
method set; a local result cannot be imported as that review.

Both profiles share the existing eight-turn, four-tool and 30-second cooperative
limits. Two method inspections plus one claim inspection leave room for a
proposal within that budget. Invalid model schema, attempted authority changes,
missing source evidence, unavailable/incomplete models and timeouts retain the
same finalized-abstention rules. Tool-result and inventory frames are detached
copies; model mutation cannot change safety limits, checks, pins or the next
frame. Full articles, earlier raw model artifacts and narrative explanations do
not enter interpretation.

New method-call and candidate fields remain forbidden in legacy v1, even when
their supplied values are empty. Unknown profile IDs fail before a model is
called. Verification covers golden published method hashes, targeted contract
inspection, two-method candidates, frame-mutation isolation, wrong/missing/
duplicate pins, tool authority/argument rejection, legacy field rejection,
budgets, cancellation and exact CLI fixture preservation. Recorded behavior is
not provider verification, profitability evidence or operational qualification.

## Inspect existing hypotheses in a recorded review

The local `news-reuse-review-v1` profile adds targeted hypothesis inspection to
the method-aware contract. Export a complete catalogue from the service first:

```sh
umask 077
./bin/mantik hypotheses --api-url "$MANTIK_API_URL" \
  > /absolute/private/hypotheses.json
./bin/mantik research-review --type news-reuse-review-v1 --export-contract
./bin/mantik research-review --type news-reuse-review-v1 \
  --journal /absolute/private/news-journal --event NEWS_EVENT_GROUP_ID \
  --as-of 2026-10-15T18:00:00Z \
  --hypothesis-catalogue /absolute/private/hypotheses.json \
  --model-fixture /absolute/private/reuse-review-artifacts.json
```

The catalogue must be a regular file of at most 4 MiB. The application validates
its complete schema, counts, canonical ordering, receiver, sealed declarations,
compiled method pins and snapshot hash before calling the recorded model.
Unknown or duplicate JSON fields and trailing values fail before evaluation.
This is an operator-selected local snapshot; its hash does not authenticate the
export, prove it is the latest catalogue, or make it a retained service review.
The application makes a separate inventory of at most 32 distinct declaration
references. Identical declarations from repeated publications share a tool
reference; their original publication receipts remain in the catalogue.

The initial frame contains compact question ID/declaration-hash pairs with
evidence class, instrument, mechanism code and horizon so a model can choose
what to inspect. It also carries native method references, the inventory/catalogue
hashes and catalogue receiver. Mechanism statements, conditions and attribution
stay behind targeted inspection.
Full declarations appear only after this targeted tool request:

```json
{"kind":"tool","tool":{"name":"inspect_hypothesis","hypothesis_id":"hypothesis-EXPECTED_QUESTION_SHA256","declaration_sha256":"EXPECTED_DECLARATION_SHA256"}}
```

An exact inventory match returns a typed `hypothesis_ref` and sealed
`hypothesis`: declared mechanism, prediction, null/refutation conditions, horizon,
methods and original attribution. It does not return trial results or raw news
articles. Unknown question IDs and changed declaration hashes return typed
rejections without a declaration body. Tool arguments cannot mix claim, method
and hypothesis identifiers. Model artifacts cannot supply inventory contents,
tool results or passing check outcomes.

For this profile, a `propose_hypothesis` candidate must include:

```json
"reuse": {
  "id": "hypothesis-EXPECTED_QUESTION_SHA256",
  "declaration_sha256": "EXPECTED_DECLARATION_SHA256"
}
```

That exact declaration must have been inspected during the run. The candidate's
mechanism/refutation codes and complete inspected method set must match it.
Every selected current news claim must also carry the same evidence class and
instrument as the declaration. The application adds a seventh mandatory check,
`hypothesis_binding`; mismatches finalize an abstention with
`hypothesis_not_eligible`. Existing news eligibility and method checks still
apply. An empty catalogue permits abstention or requests for evidence, without
inventing a reusable question. New questions use the other review profiles.

The result schema is `mantik-news-reuse-review-v1`. Every finalized result,
including automatic abstentions, binds `hypothesis_inventory_sha256`,
`hypothesis_catalogue_sha256` and `hypothesis_receiver_id` into its result ID.
The same eight-turn, four-tool and 30-second cooperative limits apply. Inspecting
one claim, two methods and one hypothesis fits within that tool budget.

These checks establish identity and structural evidence compatibility. They do
not establish semantic relevance, corroboration, profitability or research
validity. The declaration's existing conditions remain unchanged; the proposal
does not add its current claims to a publication or update the hypothesis.
Inspect the separate combined research history before judging prior results.

This profile runs recorded local fixtures only. Service review start rejects
it until storage can retain an application-owned catalogue pin and enforce its
schema fence. Existing `news-review-v1` and `news-method-review-v1` contracts and
hashes remain unchanged; both reject the new model fields even when empty or
null. There are no provider calls, automatic publication, trials or orders.
