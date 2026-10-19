# OpenAI extraction through Switchyard

`news-extract-switchyard` retains one extraction request to the selected
`openai/gpt-6.1-sol` route on a private Switchyard-compatible chat endpoint.
The broker owns provider credentials; Mantik does not need an OpenAI API key.
This command is separate from the Ollama-only `news-extract-local` adapter.

```sh
umask 077
./bin/mantik news-extract-switchyard \
  --article /private/article.json \
  --event-key issuer-results \
  --endpoint http://127.0.0.1:11434 \
  --model openai/gpt-6.1-sol \
  --reasoning-effort low \
  --max-tokens 1024 \
  --output-dir /private/new-attempt

./bin/mantik news-switchyard-inspect --dir /private/new-attempt \
  > /private/inspection.json
```

The endpoint must be an explicit private or loopback literal IP origin with a
port. The model must be exactly `openai/gpt-6.1-sol`; reasoning must be `low`.
There is no fallback model, automatic retry, tool request or service admission.
The optional `MANTIK_SWITCHYARD_MODEL_TOKEN` authenticates to the gateway and is
never embedded in receipts. `MANTIK_LOCAL_MODEL_TOKEN` is not used by this path.

## Request and bounds

The request uses the gateway's `/v1/chat/completions` compatibility contract:
`max_tokens` (64–4096), `reasoning_effort: low`, `stream: false` and
`response_format: {"type":"json_object"}`. It omits temperature. The existing
`source-quotes-v2` prompt treats source text as untrusted and requests exact
quoted spans. Mantik derives byte offsets and validates all claims itself.

[OpenAI's model documentation](https://developers.openai.com/api/docs/models/gpt-6.1-sol)
lists Chat Completions without tools, structured outputs and `low` reasoning as
supported. These capabilities do not establish equivalent gateway behavior.
This adapter targets Switchyard's compatibility request, not a direct OpenAI
request; it does not assert provider-native parameter forwarding.

The client allows one HTTP attempt, waits at most 30 seconds, disables redirects
and environment proxies, and captures at most 512 KiB of response bytes. It
persists prepared intent before sending, then retains the finished outcome and
available error bytes. A failed final write leaves the prepared intent; inspect
it before considering another request. The output directory must be new.

**The requested token limit and reasoning effort are not provider enforcement
proof.** Switchyard's bounded extraction profile accepts exactly 1,024 tokens,
low reasoning, JSON-object output and no tools or temperature. Other token
limits in Mantik's client range are refused by that profile. Its dedicated
OpenCode agent and plugin enforce those settings and disable provider retries;
the broker disables its own retries. A fixed session title suppresses the
separate small-model title request before source text is admitted.

The pinned OpenCode 2.0.22 release rehearsal exercises the complete Mantik →
Switchyard → OpenCode path against a fake Responses provider. Success and
injected failure each produced one request to `gpt-6.1-sol`, with
`max_output_tokens: 1024`, low reasoning and no tools. These fixtures establish
the tested route's behavior with a fake provider. Before actual requests,
verify the deployed revision, credentials/billing path and bounded
authorization. A client deadline does not establish that a broker stopped
upstream work or billing. Fixture tests do not establish real provider execution.
The public Switchyard `/v1/models` implementation lists Ollama tags only; absence
there does not establish whether an OpenAI route is configured.

## Verified setup checkpoint — 2026-10-16

Switchyard PRs [147](https://git.subcult.tv/subculture-collective/switchyard/pulls/147),
[148](https://git.subcult.tv/subculture-collective/switchyard/pulls/148) and
[149](https://git.subcult.tv/subculture-collective/switchyard/pulls/149) are merged.
The API and shared OpenCode sidecar on Kvant run release source
`9bfc56d01a841e0c3623036a513a8a5bf6c3e4aa`, after exact-head PR checks,
merged-main CI and isolated release-image rehearsals passed. The model endpoint
is `http://10.0.0.50:11434`; OpenCode uses its existing managed OpenAI OAuth
connection. Mantik holds no provider credential.

One synthetic live extraction completed on 2026-10-16 at 20:38:50 UTC. The
client made one attempt, the broker retained one completed request, and the
OpenCode session recorded the `switchyard-news-extractor` agent with
`openai/gpt-6.1-sol`, low reasoning and a normal `stop` finish. Mantik validated
one claim against the exact 67-byte synthetic source span; offline inspection
also passed. Private receipts and rollback evidence are retained under
`~/.local/state/switchyard/deployments/mantik-openai-20261016T200314Z/` on Kvant.

The live result proves this connection can complete a checked synthetic
extraction. It does not independently attest the provider's token ceiling,
billing or all hidden network attempts. No real-news cohort, service admission,
trading action or profitability qualification was performed. Reviewed admission is available through the distinct
[Switchyard approval path](reviewed-switchyard-admission.md); [offline evaluation](switchyard-extraction-evaluation.md) compares retained
captures with supplied annotations, including failures and unknown completion.

## Retained evidence and inspection

Receipt schema `mantik-switchyard-news-extraction-v1` uses the shared capture
fields, with authority `switchyard_model_claim_candidates_only`. It preserves
the exact requested configuration, source/contract hashes, raw HTTP bytes,
model artifact, quote derivation and checked result. Credentials are excluded.
HTTP errors, malformed envelopes, route mismatches, incomplete completion and
invalid source quotes remain visible failures. A completion must echo the exact
requested route; an unexpected model name is not silently normalized.

`news-switchyard-inspect` uses the same bounded private-file reader as local
inspection. It replays request, source, response, anchoring and result checks
without networking or writes. Its schema is
`mantik-switchyard-extraction-inspection-v1`, with authority
`retained_switchyard_bytes_consistency_only`. Prepared-only evidence reports
`completion_unknown`. Inspection proves consistency of supplied retained bytes,
not provider execution, effective reasoning/token settings or source truth.

Existing local receipts retain their schemas and serialized configuration when
the new optional reasoning field is absent. Local inspection, import, admission
and cohort evaluation reject Switchyard receipt schemas.
[Switchyard capture import](switchyard-captures.md) retains validated copies
in a distinct caller-reported service ledger.
[Reviewed admission](reviewed-switchyard-admission.md) uses explicit hashes
and separate Switchyard provenance. [Offline cohort evaluation](switchyard-extraction-evaluation.md) preserves
finished failures and unknown outcomes separately;
do not relabel these receipts as Ollama evidence. Both execution attestation
and qualification credit remain false in inspection. No extraction result is
profitability evidence or permission to trade.


## Historical diagnostic publication precision

`news-extract-switchyard` also accepts the source-only schema
`mantik-news-diagnostic-article-v1` for historical initial articles with
`evidence_class: recorded_market_unqualified`. It uses the same command and
provider limits. This input is for private extraction/inspection/evaluation;
service import and research admission reject it.

Keep the existing article identity, source bytes/hash, access reference and
exact observation/receipt timestamps. Replace the exact `published_at` field
with one of these explicit objects:

```json
{"publication":{"precision":"date_only","date":"2025-05-01"}}
```

```json
{"publication":{"precision":"unknown"}}
```

Omit `published_at` entirely, including null or a year-one sentinel. Date-only
requires a valid `YYYY-MM-DD` calendar date; unknown forbids a date. The
`available_at` field records the actual observation instant for this diagnostic,
not an inferred historical first-availability instant. `received_at` records
receipt and must be at or after that observation. Retain how these instants
were observed in the private source evidence. A date-only publication is checked
against the earliest possible instant in UTC+14 solely to reject impossible
future dates; the application never assigns that instant to publication.
No publication instant, source timezone or historical trading eligibility is
inferred. Diagnostics cannot represent corrections/retractions or prospective
or synthetic evidence classes.

Diagnostic receipts use `mantik-switchyard-news-diagnostic-v1`, with
`switchyard_diagnostic_claim_candidates_only` authority. Offline inspection uses
`mantik-switchyard-diagnostic-inspection-v1`. The source/contract hashes bind the
publication precision; the diagnostic extraction contract is
`mantik-news-diagnostic-extraction-contract-v1`, with
`diagnostic_claim_candidates_only` authority. Quote anchoring and source/claim
checks still apply. Retain failures and unknown completion as usual.

`news-switchyard-inspect` and `news-switchyard-evaluate` replay these receipts
without network access. An evaluation cohort must use one extraction contract;
mixing diagnostic and exact-time captures fails without a partial report.
Separate reports preserve their distinct contract hashes. Supplied annotations
still provide no source-truth or qualification authority.

Existing `mantik-news-article-v1` inputs continue to require exact publication,
availability and receipt instants, omit the new `publication` field, and retain
their existing wire bytes and hashes. Local-model generation, service-owned
recorded attempts and persistent Switchyard imports accept only the existing
exact-time inputs/receipts. No database upgrade or operational rule change is
part of this diagnostic format. Do not convert a diagnostic into an admissible
record by inventing an exact publication timestamp.
