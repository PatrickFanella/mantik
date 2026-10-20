# Candidate fixture plans and recorded comparisons

`candidate-test-plan` seals expected entry-signal values for every record in a
pinned synthetic dataset. `candidate-test-evaluate` compares a caller-reported
recording against that exact plan. Neither command compiles or executes source,
starts a worker, contacts a service or grants qualification.

This defines the input/expectation/result boundary for later behavioral workers.
It does not implement candidate behavioral execution or attest that recorded
outputs came from the candidate. A panicking candidate can have a matching
caller-reported recording; the report still has `execution_verified=false`.

## Seal all fixture cases

Use the retained [candidate declaration](candidate-source.md), exact original
dataset artifact and a JSON Boolean array, such as `[false,true,false,true]`.
The values are explicit caller annotations in retained dataset order. They are
not independently assessed gold labels or profitable entry decisions.

```sh
umask 077
./bin/mantik candidate-test-plan --candidate candidate.json \
  --candidate-sha256 "$(jq -r .sha256 candidate.json)" \
  --dataset dataset.json --expectations expectations.json > test-plan.json
```

`mantik-candidate-fixture-test-plan-v1` retains the full candidate, fixed
`go-entry-price-bool-v1` interface and ordered cases. Each case binds the derived
execution observation identity, exact integer price and expected Boolean. The
dataset's raw/canonical hashes remain pinned by the candidate. The plan hash
is SHA-256 of compact Go JSON with `id` and `sha256` empty; its ID is
`candidate-test-` followed by that hash. A changed annotation changes the plan.

The first contract accepts 1..64 unique usable revision-1 records from the
existing synthetic `DEMO` dataset contract. It includes every retained record
in its existing order, without claiming chronological or point-in-time replay.
Duplicate, quarantined, missing, revised or excessive records reject the whole
plan instead of silently selecting usable or favorable cases. Expectations must
cover every record; `null`, omitted or non-Boolean entries are rejected.
Annotations are bounded to 4096 bytes; plans to 512 KiB plus 16,384 bytes.

Seal the plan before invoking a future worker. Its content identity prevents
changing expectations under the same pin, but does not prove when a caller
created it or that data was unseen. Service-owned pre-test intents, all-attempt
search history and statistical admission remain separate work.

## Caller-reported recordings

`mantik-candidate-fixture-recording-v1` requires an `attempt_id`, exact plan and
candidate hashes, status/reason, and an explicit `outputs` array. Each output has
`case_id` and an explicit Boolean `value`. At most 128 outputs and 32 KiB of JSON
are accepted. Unknown/duplicate/case-aliased fields, trailing data, invalid UTF-8
and missing/null values reject before comparison.

Statuses are `reported_completed`, `reported_failed` or `unknown`.
`reported_completed` requires reason `completed`; failures/unknowns retain a
bounded reason code. A failed/unknown recording can retain partial outputs or
an empty array. For example, this creates an honest no-execution recording:

```sh
jq '{schema:"mantik-candidate-fixture-recording-v1",
     attempt_id:"manual-unknown-example", plan_sha256:.sha256,
     candidate_sha256:.candidate.sha256, status:"unknown",
     reason_code:"not_executed", outputs:[]}' test-plan.json > unknown.json
./bin/mantik candidate-test-evaluate --plan test-plan.json \
  --plan-sha256 "$(jq -r .sha256 test-plan.json)" \
  --dataset dataset.json --recording unknown.json > comparison.json
```

The dataset is required again during evaluation. The evaluator verifies its
exact bytes and canonical content and reconstructs every case's identity/price.
A caller cannot substitute different prices by rehashing only the plan. A
recording bound to another candidate or changed expectations is rejected.

## Complete comparison, without winner selection

`mantik-candidate-fixture-comparison-v1` retains every supplied output, the
recording's exact-byte SHA-256, and one row per planned case. Rows are `match`,
`mismatch`, `missing` or `duplicate`. Duplicate values are all preserved, including
contradictions. Unrecognized case identities remain in `unmatched`. No expected
or reported case is filtered to improve the displayed match count.

`matched`, `mismatched` and `missing` count case rows; `duplicates` counts extra
outputs for known identities. Complete reported runs have status `compared`,
including disagreements. Missing, duplicated, unmatched, failed or unknown
recordings have status `incomplete`. `all_annotations_match` is true only for a
reported-completed run with exactly one output for every planned case, no
unmatched outputs and no mismatches. A failed/unknown run remains incomplete
even when every supplied Boolean matches.

Successful CLI exit means a comparison was produced, not that the candidate
passed. Read status, all counters and `all_annotations_match` explicitly.
Malformed artifacts, wrong pins and changed dataset bytes exit nonzero without
a report. Retain original recordings and reports privately, including rejected
inputs. This offline tool does not allocate global attempt IDs or enforce a
durable service retry ledger; reusing a caller's label cannot attest history.

Plan authority is `caller_declared_fixture_annotations_only`; comparison
authority is `caller_reported_fixture_annotation_comparison_only`. Execution
verification and qualification credit remain false. Equal annotations do not
prove source correctness, Go execution, forecasting accuracy, realistic fills,
safety, strategy admission or profitability. #18 remains incomplete pending
the remaining property/failure checks and authoritative candidate/attempt
lineage. [Behavioral workers](candidate-behavior.md) can now execute the fixed
synthetic examples under a separate job and host receipt; these offline
recordings retain their caller-reported authority.
