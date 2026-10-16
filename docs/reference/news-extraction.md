# Recorded article extraction

`news-extract` runs one recorded extractor response against a source-only article
and returns checked claim candidates or an abstention. This is an offline
development adapter for issue 12. It does not call a provider, discover claims by
itself, measure extraction accuracy or admit records to the service.

```sh
umask 077
dir="$(mktemp -d)"
./bin/mantik news-extract --export-article-fixture > "$dir/article.json"
./bin/mantik news-extract --export-model-fixture > "$dir/model.json"
./bin/mantik news-extract --export-contract > "$dir/contract.json"
./bin/mantik news-extract --article "$dir/article.json" \
  --model-fixture "$dir/model.json" --event-key demo-revenue-event \
  > "$dir/extraction.json"
```

Both exported fixtures are invented and unqualified. Input files must be regular
files; articles are bounded to 2 MiB of JSON with an original body of at most
128 KiB, and extractor responses to 128 KiB. Export flags are mutually exclusive
and cannot be combined with extraction arguments. No API URL or credentials are
accepted. Preserve results outside Git: they contain the complete original and
exact extractor bytes, including malformed artifacts.

## Source before claims

The `mantik-news-article-v1` input carries source/article identity, revision kind
and optional predecessor reference, publisher/URL, publication/availability/
receipt times, evidence class, access/raw references, original text and its
SHA-256. It has no claims or extraction-method fields. The existing operator
recording API does not accept this schema.

The application checks the same source rules used by news normalization:
bounded UTF-8 body and matching digest, causal timestamps, unqualified evidence
class, bounded HTTPS URL without credentials, and revision-reference shape.
Article hashes use compact Go JSON after UTC time normalization. Source metadata
is detached before invoking an extractor. Changing a distributed source's ID or
availability changes its source hash even when the article text is identical.

A predecessor hash remains a supplied reference here. Offline extraction does
not fetch or prove the predecessor exists. Existing journal/service admission
has its own retained-chain checks; those checks cannot be inferred from this
result. The original operator-recorded schema, accepted extraction method,
normalization hashes and admission behavior are preserved.

## Extractor contract and artifacts

`mantik-news-extraction-contract-v1` permits one response, at most 16 claims,
128 KiB of response bytes and a 30-second cooperative deadline. The internal
model interface receives a detached contract, article and article hash. Reading
the original is necessary at extraction time, before claim spans exist. The
later investigation stage continues to use targeted excerpts. Extractor adapters
must honor context and bound transport reads; this interface does not terminate
an uncooperative callback. The CLI uses only the recorded local response.

The response object contains `outcome`, `reason_code` and `claims`. Each claim
contains `id`, `code`, `assertion`, `assessment`, `entities`, `symbols` and
`evidence`. It cannot supply event keys, source metadata, timestamps, checks,
risk policy, qualification or narrative. Unknown, duplicate and noncanonical
field names, trailing values, invalid UTF-8 and excessive nesting are rejected.

Successful output uses outcome `extracted` and reason `claims_extracted`.
Claims need distinct IDs, explicit `reported_unverified`, `disputed` or
`retracted` assessment, attributed entities, optional explicit instruments and
one to four exact source spans. Span coordinates are UTF-8 byte offsets with
an exclusive end; quotes must match exactly, remain within the original and
not split a character. Each span is at most 2048 bytes. Retraction status must
agree with the source revision kind. One invalid claim rejects the whole
candidate set; there is no partial successful extraction.

The operator supplies `--event-key`. The application attaches it after checking
the response. This is a declared grouping choice, not inferred event equivalence,
syndication detection or corroboration. Duplicate distribution retains its own
source attribution. The extractor cannot choose a routing key by returning one.

Explicit abstention uses an empty claims array and reason `insufficient_source`,
`no_relevant_claims` or `ambiguous_source`. Malformed output, invalid bindings,
EOF, callback failure and deadline/cancellation produce application abstentions
with specific reason codes and failed/not-run checks. Error messages returned
by callbacks are not copied into results. No failure path emits partial claims.

## Result and authority

The CLI emits mode `recorded_model_fixture` and a `mantik-news-extraction-v1`
result. It retains the canonical article, article/contract hashes, operator event
key, outcome/reason, claims, ordered independent checks, artifact metadata and
report hash. Checks cover source binding, completion, model schema, claim
bindings and the authority boundary. Explicit abstention leaves claim checks
`not_run`; it does not claim evidence validation passed.

Artifact metadata contains byte count, SHA-256, `capture_complete` and
`raw_base64`. Base64 preserves exact invalid UTF-8, duplicate fields and malformed
JSON without normalizing them. For an oversized internal callback response,
only its byte count and digest are retained and capture is explicitly incomplete.
The CLI rejects an oversized file before creating an extraction result. Invalid
source files and arguments likewise fail with no result; they are not retained
attempts. No append-only attempt journal is created by printing a result.

The report hash is over compact Go JSON with its `sha256` empty. Identical
recorded inputs produce identical receipts; no processing timestamp is added.
Callback timeout/error results describe that invocation, not proof of provider
liveness or forced termination. Authority is `claim_candidates_only`, and
qualification credit is false. Candidate assertions and entity/instrument
attribution remain unverified interpretations even when excerpt checks pass.
An exact quote does not establish that the proposed assertion follows from it.

The result is neither a `mantik-news-recording-v1` object nor an admission
receipt. It cannot be passed to `news-retain` or `news-submit` as a recording.
Durable extraction-attempt retention, reviewed conversion/admission with exact
provenance, automatic dispatch, provider access and measured extractor quality
remain subsequent work. Issue 12 and its dependent issues remain open. No
database migration, existing service upgrade or operation is performed.

Verification covers exact original/artifact retention, repeatable receipts,
Unicode spans, whole-result rejection, source/callback alias isolation,
malformed/authority artifacts, cooperative transport timeout, corrections,
retractions, duplicate/late sources and CLI failure output. Existing recording,
journal and storage checks verify compatibility after sharing source validation.
