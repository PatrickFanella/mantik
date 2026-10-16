# Local model extraction

`news-extract-local` makes one finite request to the local Switchyard-compatible
chat endpoint. It retains a prepared request before sending it and a separate
finished receipt afterward, then checks the returned content through the existing
[extraction contract](news-extraction.md). It discovers candidates from the source;
it does not admit news, publish a hypothesis or grant trading authority.

```sh
umask 077
./bin/mantik news-extract --export-article-fixture > /private/article.json
./bin/mantik news-extract-local --article /private/article.json \
  --endpoint http://10.0.0.50:11434 \
  --model ollama/hermes3:8b-llama3.1-q5_K_M \
  --event-key declared-event --max-tokens 1024 \
  --output-dir /private/new-extraction-attempt > /private/local-result.json
```

The article above is invented. The example model was present in the operator's
local inventory when the adapter was developed; it is an explicit selection,
not an automatic default or a model-quality recommendation. A mutable model name
is not a weights digest. Retain the endpoint's model inventory/digest separately
when evaluating an actual model, and record any routing or model changes.

## Explicit local transport

The command requires an explicit endpoint, model, event key, article and new
absolute output directory. Origins must have a literal private or loopback IP,
an explicit valid port, HTTP or HTTPS, and no credentials, path prefix, query or
fragment. Hostnames are rejected, avoiding DNS changes after validation. Models
must use the explicit `ollama/NAME` routing prefix; cloud-marked names are
rejected. These checks constrain the requested route. They do not attest the
endpoint's implementation, internal routing, model weights or execution host.
Keep raw upstream Ollama private and use the established compatibility ingress.

The optional `MANTIK_LOCAL_MODEL_TOKEN` environment variable supplies a bearer
credential for an endpoint that requires it. The adapter excludes it from the
captured request and its own error messages. Without a token, Switchyard receives the documented
`X-Switchyard-Client: mantik-extraction` attribution header; this is attribution,
not proof of client authentication. Do not reuse another application's token.
The adapter neither changes endpoint configuration nor creates client grants.

The request uses `/v1/chat/completions`, `stream:false`, temperature zero,
JSON-object output and 64–4096 requested output tokens (default 1024). The
source article is a separate user message. A versioned system instruction
specifies the quote-only claim/abstention schema and tells the model to treat source text
as untrusted data. No tools, images, remote URLs to fetch or expected evaluation
annotations are supplied. Token and JSON-format requests depend on endpoint
compliance; they do not prove deterministic generation or prompt-injection
resistance.

The client disables environment proxies, redirects and connection reuse. It
makes one POST with no application retry, a two-second connection timeout and
the existing 30-second extraction deadline. Cancellation bounds the caller's
wait, not proof that a remote model or broker stopped work. A timed-out/lost
response must not trigger a blind retry. There is no continuous worker or
automatic model download.

## Exact quote anchoring

Local receipt schema `mantik-local-news-extraction-v2` and prompt
`source-quotes-v2` use a separate model-response protocol. The response keeps
`outcome`, `reason_code` and `claims`; claims keep `id`, `code`, `assertion`,
`assessment`, `entities`, `symbols` and `evidence`. Each evidence object contains
only `quote`. The model cannot supply byte offsets. Unknown fields, including
legacy `start_byte` or `end_byte`, reject the whole derivation; there is no
fallback to caller-provided coordinates.

The application finds each nonempty, bounded exact quote in `article.original`.
It derives a UTF-8 byte start and exclusive end only when there is exactly one
occurrence, including overlapping matches when testing ambiguity. It does not
trim whitespace, normalize Unicode, choose the first repeated match or paraphrase
text. A missing or ambiguous quote rejects every claim in that response, with
no partial candidate artifact. A longer unique exact quote may disambiguate a
repeated phrase, but the application does not invent one.

Successful derivation preserves all other model-supplied claim fields and runs
the original article/claim validator. Duplicate excerpts, missing attribution,
invalid uncertainty and revision inconsistencies still fail. Explicit supported
abstentions derive to the unchanged extraction response shape. Assertions and
entity/instrument attribution remain unverified interpretations; exact anchoring
does not prove that an assertion follows from its quote.

The `mantik-exact-quote-anchoring-v1` receipt binds source hash, operator event
key, exact model-artifact hash and derived candidate-artifact hash, plus status,
reason and its own digest. Replaying those inputs deterministically reproduces
the derivation. It binds byte provenance, not model identity or source truth.
The standalone recorded extractor and service-owned recorded attempts retain
their existing offset-bearing protocol and interpretation.

## Retained request and result

The output directory must not exist. It is created with mode 0700; receipt files
use exclusive creation, mode 0600 and file/directory synchronization. Existing
paths and final-file symlinks are refused. A failed request does not remove its
directory. Store these private files outside Git: they include the full article,
prompt, raw HTTP response and model content.

`prepared.json` is synced before network access. It contains the exact request
body in base64, its byte count and hash, endpoint/model/output-token bound, prompt
version, event key, source/contract hashes and start time. It has status
`prepared` and disposition `not_sent`, describing the durable pre-send stage.
After interruption that file alone cannot establish whether a request was sent
or completed. The request contains no authorization header or token.

`finished.json` is a separate receipt with status `finished`, completion time,
HTTP status, transport disposition, captured response and the checked extraction
result. The raw HTTP capture is bounded to 512 KiB. If truncated or read-failed,
`complete` is false and its byte count and SHA-256 describe only the retained
prefix; they are not a claim about the complete unseen response. Transport
failure before headers has no response capture. The two files are retained even
when the checked result abstains. If final persistence fails, the command emits
no result and does not retry; retain all files and inspect the prepared evidence.
These files are not a resumable service-owned attempt ledger.

A complete supported envelope has one assistant choice, the exact requested model
and `finish_reason:"stop"`. Model changes, ambiguous JSON, unknown fields,
multiple choices, tools, refusal, truncation and incomplete HTTP capture fail
closed. Complete malformed candidate JSON remains a checked artifact abstention.
The receipt retains decoded assistant content as `model_artifact`, separate
from the raw HTTP envelope. Successful anchoring produces a separate canonical
candidate artifact; the checked result retains those derived bytes and their
hash. The anchoring receipt links both artifacts to the source and event key.
On anchoring rejection, `disposition` is `quote_anchoring_rejected`, with a
specific anchoring reason and no derived artifact; the generic extraction
result abstains with `extractor_unavailable` because the adapter supplied no
candidate bytes. Original model and HTTP bytes remain available. Interrupted
completions may retain received content without a completed anchoring result. Receipt hashes use
compact Go JSON with their own `sha256` empty. Times and generation output vary,
so repeated local requests do not promise identical receipts.

Authority is `local_model_claim_candidates_only`, and qualification credit is
false. The source's unqualified evidence class, attribution and exact spans
still require independent checks; a valid candidate is not established truth.
Use [offline evaluation](extraction-evaluation.md) with separately reviewed
expected responses to assess the retained artifacts. The local receipt is not
accepted as a service extraction attempt or an admission receipt. Service
integration, durable remote-dispatch recovery, automatic event grouping and
measured extraction quality remain separate work.


Existing v1 request/response receipts are historical evidence and are not
rewritten, upgraded or reinterpreted by this command. New invocations require
fresh directories and use v2. A v1 model's incorrect offsets must not be silently
repaired and counted as a new model result. A new prompt/version requires its
own retained request and evaluation evidence.
