# Local extraction evaluation

`news-local-evaluate` compares a complete cohort of retained local extraction
captures with supplied source annotations. It reads prepared/finished receipts
and independently replays their inspection before comparing any checked result.
It makes no provider or service request, performs no admission and writes no
capture files. A cohort with disagreements is a successful command result.

```sh
umask 077
./bin/mantik news-local-evaluate --manifest /private/cohort/manifest.json \
  > /private/cohort/evaluation.json
```

Use a private absolute manifest path, with a private current-user-owned parent
directory. The manifest and all capture files must be bounded regular files
owned by the current user, with no group/other access. Receipt/manifest symlinks
and FIFOs are rejected. Capture directories use the same confined loader as
`news-local-inspect` and `news-local-import`.

## Manifest and annotations

The manifest schema is `mantik-local-extraction-evaluation-manifest-v1`. Each
case declares a unique `id`, an absolute `capture_dir`, and an `expected` response:

```json
{
  "schema": "mantik-local-extraction-evaluation-manifest-v1",
  "cases": [
    {
      "id": "article-1",
      "capture_dir": "/private/captures/article-1",
      "expected": {
        "outcome": "abstain",
        "reason_code": "ambiguous_source",
        "claims": []
      }
    }
  ]
}
```

The example abstention is not an annotation recommendation. Review the source
and replace it with the appropriate complete expected response. Expectations
use the [recorded extraction response](news-extraction.md) contract, including
attributed entities, uncertainty and exact byte spans. They must be valid claims
or deliberate abstentions, never application-generated failures such as
`extractor_unavailable`. Source binding and structure are checked even for an
unknown completion. Annotation truth or reviewer identity is not attested.

The manifest permits 1–32 cases within 8 MiB. Each local receipt is at most
8 MiB; the complete materialized corpus is at most 64 MiB. The CLI checks the
cumulative bound as it loads cases. Unknown, duplicate or mis-cased JSON fields,
trailing values, invalid annotations, unsupported receipt versions and
inconsistent captures reject the entire invocation without a partial report.
The command has no provider, API, model-selection, outcome-filter or pass-threshold
flags. It does not generate expected labels from model output.

Prepare and review annotations independently of the evaluated generation.
Retain sampling rationale, permitted source/access references, review records,
model/prompt versions, cohort construction and any held-out split separately.
A convenient or favorable set of supplied annotations is not a representative
accuracy benchmark. A cohort may include multiple model/prompt configurations;
inspect each retained configuration before attributing aggregate counts to a
particular extractor.

## Outcomes, unknown completion and comparison

Finished captures preserve their checked local result, including HTTP/transport
failures, malformed/truncated output, rejected quotes and explicit abstention.
A failed local call is not reinterpreted as an empty recorded response with a
different reason. It remains in the finished comparison denominator, with its
original disposition and all available bytes.

Prepared-only captures have `completion_unknown`. Their expected annotation is
retained and validated, but `comparison` is absent. The evaluator does not invent
an actual abstention, missing claim or exact result for an unobserved completion.
All such cases remain visible in `cases`, total `counts.cases`, `unknown_cases`,
`unknown_expected_extracted`, `unknown_expected_abstentions` and
`unknown_expected_claims`.

`counts.compared` applies only to finished cases and uses the existing exact,
one-to-one claim comparison. Every claim field except local ID must match;
claim order is ignored, while order inside entities/symbols/spans is significant.
Outcome and reason must also agree for an exact case. No paraphrase or semantic
similarity credit is assigned. A structurally valid assertion with a matching
quote can still disagree with the supplied annotation.

The invariants are:

- `cases = finished_cases + unknown_cases`.
- `compared.cases = finished_cases`.
- Disposition counts cover every input, including `completion_unknown`.
- `cases = distinct_prepared_receipts + repeated_prepared_cases`.

An identical prepared/finished snapshot cannot appear twice, even under different
case IDs or paths. Different snapshots sharing the same prepared receipt, such
as prepared-only and finished evidence, may both be retained. They increment
`repeated_prepared_cases` and must not be counted as independent generations.
Distinct hashes also do not establish statistical source independence.

For finished comparisons, exact claim precision is `matched / (matched +
unexpected)` and recall is `matched / (matched + missing)`; a zero denominator is
undefined. Unknown claims are reported separately and are not silently counted
as observed failures. Always publish unknown and disposition counts alongside
any finished-only ratio. `compared.exact_cases / cases` describes the share of
all supplied snapshots with demonstrated exact agreement; it is not model
accuracy, and repeated prepared receipts remain dependent observations. The
report returns counts, not an accuracy score or a qualification threshold.

## Evidence and authority

The output schema is `mantik-local-extraction-evaluation-v1`. Each case contains
the complete prepared/finished receipt snapshot, its supplied annotation, the
inspection and, for finished evidence, the exact comparison with checked actual
result. Keep the report private: original source, request, model content and
HTTP diagnostics remain embedded in their receipt byte records.

The materialized corpus schema is `mantik-local-extraction-corpus-v1`.
`corpus_sha256` hashes its ordered cases, receipts and supplied annotations.
Manifest file paths and whitespace are excluded, so moving an unchanged capture
or reformatting its manifest does not change the report. Case order remains part
of the hash. Contract and report hashes use compact Go JSON, with the report's
own `sha256` empty while hashing. Changing an annotation changes corpus/report
hashes without changing the retained actual extraction. Identical inputs replay
byte-identically.

Authority is `supplied_annotation_agreement_only`; `execution_attested` and
`qualification_credit` are false. Consistent receipts do not prove provider
origin, when a request occurred, source truth, semantic relevance, annotation
correctness or profitability. Evaluation does not replace the separate explicit
approval required by [local-capture admission](reviewed-local-admission.md).
A reviewed real-news corpus and bounded actual-extractor cohort remain required
before making claims about performance on real news.
