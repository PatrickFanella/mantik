# Candidate properties over retained samples

`candidate-property-plan` seals an explicit list of properties against a
[behavioral job](candidate-behavior.md). `candidate-property-evaluate` checks those
relations over every eligible pair of retained worker outputs. Both commands
operate on local files; they do not run candidate source, contact a provider,
launch workers or grant qualification.

The initial checks concern only the supplied prices and outputs:

| Property | Eligible pairs | Required relation |
| --- | --- | --- |
| `same_price_same_signal` | Every pair with equal prices | Both returned Booleans are equal. |
| `entry_nonincreasing_with_price` | Every pair with distinct prices | A true signal at the higher price requires a true signal at the lower price. |
| `entry_nondecreasing_with_price` | Every pair with distinct prices | A true signal at the lower price requires a true signal at the higher price. |

These are optional caller-declared requirements, not universal strategy rules.
Select properties appropriate to the hypothesis. Constant signals can satisfy
both monotonic directions, and agreement on finitely many samples does not prove
a property over all prices or all candidate states.

## Seal the requirements

Write a JSON array of one to three distinct supported property names, such as:

```json
["same_price_same_signal", "entry_nonincreasing_with_price"]
```

```sh
umask 077
./bin/mantik candidate-property-plan --job behavior-job.json \
  --job-sha256 "$(jq -r .sha256 behavior-job.json)" \
  --dataset dataset.json --properties properties.json > property-plan.json
```

`mantik-candidate-property-plan-v1` retains the full behavior job, ordered property
names and a content hash. A changed requirement changes the plan identity. The
job retains its candidate, hypothesis, exact dataset, annotations, toolchain and
harness pins. Unknown/duplicate properties, empty lists, null entries, malformed
JSON and changed dataset bytes reject before sealing. Property name arrays are
bounded to 4096 bytes; plans to the behavior-job bound plus 8192 bytes.

Seal the property plan before execution when using it prospectively, then run
the unchanged job through the behavioral worker or
[local attempt journal](candidate-attempt-journal.md). The property plan does not
prove when it was created or that a caller did not select requirements after
seeing outputs. It does not modify the worker's job identity or make independent
claims about the annotations.

## Evaluate retained output

```sh
./bin/mantik candidate-property-evaluate --plan property-plan.json \
  --plan-sha256 "$(jq -r .sha256 property-plan.json)" \
  --dataset dataset.json --file /absolute/private/attempt/stdout.json \
  > property-report.json
```

The evaluator requires a valid behavior worker result for the exact job and
reverifies the original dataset and every case's identity/price. It keeps the
full typed worker result and SHA-256 of the original result artifact bytes. A
whitespace change can preserve the worker result's logical identity while
changing this exact-byte artifact identity.

Every eligible pair appears once in retained case order, with both case IDs,
prices, available values and a pair status: `holds`, `violated` or
`missing_output`. Price comparisons use integer ordering, without subtraction,
conversion to floating point or sorting away observation order. Check counters
retain eligible, checked, missing and violated pair counts. The 64-case behavior
contract bounds each property to at most 2016 pairs and the plan to three checks.

Checks report `passed`, `violated`, `incomplete` or `not_applicable`. A property
with no eligible pair is `not_applicable`, not a vacuous pass. Otherwise a failed worker or
missing output makes a check incomplete while retaining all checkable violations.
Overall status is incomplete if the worker failed or any property is incomplete
or inapplicable; otherwise it is violated if any relation fails, and passed only
when every declared sample property was checked and held.

`all_sample_properties_satisfied` describes that property status.
`annotation_match` separately retains the worker's example comparison. All example
annotations can match while a declared relation fails; a relation can hold while
example annotations disagree. Neither result is substituted for the other.

CLI exit zero means a report was produced, including violated or incomplete
reports. Invalid pins, absent/malformed worker results or changed artifacts exit
nonzero without a report. A host timeout with no valid worker result must remain
in its original attempt history; do not omit it from a cohort or manufacture a
passing property report.

## Authority and remaining work

`mantik-candidate-property-report-v1` has authority
`retained_worker_sample_property_comparison_only`, coverage
`retained_samples_only`, execution verification false and qualification credit
false. The input worker result alone does not attest its execution origin or
current host state. Keep the original job, property plan, worker output and host
receipt/journal evidence together. This report has no service import, admission
or activation path.

These finite comparisons add sample relation checks to the existing example and
failure checks. They do not establish universal properties, fresh-process
repeatability, chronological/point-in-time replay, independent gold labels,
complete candidate search history, realistic trading behavior or profitability.
Issue #18 remains open for its remaining generated-source, replay and
service-owned lineage criteria.
