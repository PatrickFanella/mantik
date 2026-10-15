# Recorded news claim boundary

`news-normalize` validates operator-recorded structured claims against a supplied
article's exact source text. It is an offline research boundary for issue #12,
with no provider, model, database, routing, execution or portfolio access.

Normalization does not collect articles, discover claims, prove assertions true
or reopen cases. The [private news journal](news-journal.md) adds explicit local
retention, verified predecessors and targeted inspection. Without that retention
step, the caller must preserve input/original material privately; a normalized
reference alone is not proof that its target exists or can be retrieved.
Related-event routing and correction/retraction case policies remain separate
units.

## Recording contract

The JSON schema is `mantik-news-recording-v1`. It requires source/article IDs,
revision, kind, publisher, HTTPS article URL, publication/availability/receipt
times, unqualified evidence class, access reference, raw reference, original
UTF-8 source text and its SHA-256. The extractor is explicitly
`operator_recorded_v1`; this command does not run an LLM or assert machine
extraction accuracy. URLs and references are metadata and are never fetched.

Publication must be no later than availability, which must be no later than
receipt. Availability is a supplied source observation, not an inferred time.
Normalization converts these instants to UTC without changing the original text.
Source identity/access metadata remains a declaration requiring separate
provider/custody evidence before real-data qualification.

Allowed evidence classes are `synthetic_unqualified`,
`recorded_market_unqualified` and `prospective_market_unqualified`, matching the
existing research-input vocabulary. None grants execution or qualification
credit. No news input is submitted to the shared routing interface by this unit.

Each claim declares:

- A unique local claim ID, supplied event key, structured claim code and bounded
  operator assertion.
- Explicit assessment: `reported_unverified`, `disputed` or `retracted`.
- Attributed entity IDs/names and any explicit instrument symbols.
- Exact source evidence spans with UTF-8 byte offsets and matching quoted text.

Excerpt validation proves byte binding only. It does not prove semantic
entailment, truth, completeness, entity resolution, or instrument eligibility.
A claim may have additional context outside its selected excerpts; retain the
original material for targeted inspection. Uncertainty is not converted into a
fabricated probability or confidence score.

Event keys are supplied grouping declarations, not inferred equivalence. Every
normalized claim has `independence=not_assessed`. Identical or syndicated source
text remains separately attributed and must not be counted as independent
corroboration. This unit does not merge sources, accumulate votes or decide that
a shared event key is correct.

## Revisions and content identity

An `initial` recording must be revision one with no predecessor. A `correction`
or `retraction` must declare the immediately preceding revision of the same
source/article and its canonical recording SHA-256. Retraction recordings require
all claims to have the `retracted` assessment; other recording kinds reject that
assessment. Predecessor existence, content and evidence-class compatibility are
not established until a later durable-retention boundary verifies that chain.

Normalization emits `mantik-news-claims-v1`, a canonical recording digest,
source metadata, revision declaration and bound claims. Each bound claim
preserves its canonical recording digest, timestamps, original-source digest, raw reference, entities,
instruments, assessment and exact excerpts. The complete original text stays in
the caller's recording and is not repeated in the normalized output.

A claim key binds source ID, article ID, revision, claim ID and evidence class.
The canonical recording digest additionally covers its complete typed recording,
including source text, excerpts, assertions and declarations. Equivalent time
instants normalize identically; original text and array order are preserved.
Changed content under the same claim identity produces a different recording
digest and must become a conflict at a future retention fence, not overwrite an
older record. A digest identifies content; it is not verification of its source
or meaning.

## Bounds and parsing

| Field | Bound |
| --- | --- |
| Recording JSON | 2 MiB, one object, UTF-8 |
| Original text | 1..128 KiB |
| Claims | 1..16 |
| Entity identities per claim | 1..16, unique |
| Symbols per claim | 0..16, unique |
| Excerpts per claim | 1..4, distinct |
| One excerpt | 1..2048 bytes, complete UTF-8 characters |
| Source/article/claim/event/code/entity IDs | 1..128 ASCII letters, digits, underscores or hyphens |
| Assertion | 1..512 bytes of bounded text |
| Revision | 1..1,000,000 |

The reader rejects unknown, duplicate and noncanonical JSON field names,
trailing values, excessive nesting, malformed input, digest mismatches and
incompatible declarations. Field names use the exact lowercase contract names;
case variants cannot silently override source identities. Errors do not echo
source text. Original-source SHA-256 applies to the decoded UTF-8 `original`
string; the canonical recording digest is separate from the input JSON file's
byte hash.

## CLI rehearsal

```sh
./bin/mantik news-normalize --export-fixture > /tmp/mantik-news-fixture.json
./bin/mantik news-normalize --file /tmp/mantik-news-fixture.json
```

The exported text is invented and explicitly `synthetic_unqualified`, with an
`example.invalid` URL. It supplies no market evidence. For actual material,
keep source recordings and normalized outputs outside Git in an approved
private location; this command does not authorize provider collection or imply
rights to retain a source. It needs no API token and makes no HTTP request.

Missing/invalid input fails without outputting a successful normalization.
`--export-fixture` cannot be combined with a recording path. The same valid
recording produces the same normalized result, with copied data that does not
alias caller-owned claims or lineage pointers.
