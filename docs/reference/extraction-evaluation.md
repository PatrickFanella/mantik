# Recorded extraction evaluation

`news-extraction-evaluate` compares every recorded extractor response in a local
corpus with caller-supplied expected claims or abstentions. It measures exact
agreement with those annotations. It does not call a model, attest who produced
the recordings or annotations, or establish that the annotations are true.

```sh
umask 077
./bin/mantik news-extraction-evaluate --export-fixture > /private/corpus.json
./bin/mantik news-extraction-evaluate --corpus /private/corpus.json \
  > /private/evaluation.json
```

The exported corpus is invented. One case agrees exactly; another changes the
assertion while retaining a valid source quote. Both responses pass extraction
structure checks, but the second disagrees with its expected claim. This
illustrates the limit of quote matching without claiming to measure a real
extractor. Replace the example with a separately reviewed corpus and retained
outputs before drawing conclusions about an extractor.

## Corpus and annotations

The `mantik-recorded-extraction-corpus-v1` object has `schema` and `cases` fields.
Each case contains a unique bounded `id`, a complete source-only `article`, an
operator `event_key`, exact response bytes in canonical `artifact_base64`, and an
`expected` response. The article and expected response use the contracts in
[recorded article extraction](news-extraction.md). Expected output must be a
valid extracted response or deliberate abstention with an explicit claims array.
Application failures such as malformed output are actual results, not permitted
expected abstention reasons. Invalid articles, annotations or envelopes reject
the whole corpus before a report is emitted.

The corpus permits 1–32 cases within 8 MiB of compact JSON; the CLI also bounds
the input file to 8 MiB. Each decoded artifact is at most 128 KiB. Files must be
regular files. Unknown, duplicate and noncanonical JSON fields, noncanonical
base64, repeated case IDs and trailing JSON values are rejected. Raw artifacts
may contain malformed JSON or invalid UTF-8; these remain captured actual
abstentions rather than omitted cases.

Annotations need evidence spans and attributed entities just like extracted
candidates. The validator checks their source binding and structure, not their
semantic correctness. An annotator may be wrong. Keep annotation review,
extractor/model/version provenance, corpus construction, sampling choices and
held-out evaluation discipline in the experiment's retained evidence. This
command does not enforce those procedures or prevent choosing a favorable
corpus. It provides no corpus selector, outcome filter, ranking or threshold.

## Matching and complete results

Matching compares every claim field except its local `id`. Assertion text,
code, uncertainty assessment, entities, symbols and exact evidence spans must
match; order within entities, symbols and spans remains significant. The
operator event key is attached equally to expected and actual claims. Response
claim order is ignored. Matching is one-to-one: duplicate content cannot reuse
one counterpart to inflate agreement. There is no paraphrase, semantic
similarity, partial-span credit or inference of truth.

Each case retains its expected response, full checked actual extraction result,
matched/missing expected IDs, unexpected actual IDs, and an `exact` flag. A case
is exact only when outcome and reason also match and no claims are missing or
unexpected. Thus two abstentions with different reasons disagree even though
neither contributes claim counts.

The report counts all cases and exact cases, expected extracted/abstention
cases, unexpected and missed abstentions, and matched/missing/unexpected claims.
An invalid actual response contributes its resulting abstention and all missing
expected claims; it is never removed from the denominator. If computing exact
claim precision, use `matched / (matched + unexpected)`; recall uses
`matched / (matched + missing)`. A zero denominator is undefined. These are
annotation-agreement counts, not calibrated confidence or semantic accuracy.
A report containing disagreements is a successful command result; invocation or
invalid-corpus failures emit no partial report.

The report includes every original article and raw artifact through its actual
result. Store corpus and reports privately outside Git. A canonical corpus hash
pins all cases, annotations, metadata and exact decoded artifact content; it is
not the input file's byte hash. Article timestamps are normalized to UTC before
hashing. Case and annotation ordering remain part of that hash. Contract and
report hashes use compact Go JSON with the report's own `sha256` empty when
computing its digest. Identical canonical inputs produce identical reports.
These hashes bind bytes and interpretations; they are not signatures or proof
of provider origin.

Authority is `supplied_annotation_agreement_only`, and qualification credit is
false. There is no API connection, database migration, admission, case mutation,
provider request, hypothesis publication or economic effect. The report cannot
replace an extraction attempt or the explicit approval required by
[reviewed extraction admission](reviewed-extraction-admission.md). Issue 12
remains open for measured extractor behavior and its remaining acceptance work.
