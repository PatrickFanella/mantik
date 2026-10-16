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
proof.** The inspected Switchyard OpenCode generation path does not forward
these settings. A client deadline also does not establish that a broker stopped
upstream work or billing. Before actual provider requests, verify the deployed
route, credentials/billing path, parameter enforcement and bounded authorization.
No paid-provider execution is established by the fixture tests for this adapter.
The public Switchyard `/v1/models` implementation lists Ollama tags only; absence
there does not establish whether an OpenAI route is configured.

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
and cohort evaluation reject Switchyard receipt schemas. Provider capture
evaluation and reviewed admission need their own explicit compatibility work;
do not relabel these receipts as Ollama evidence. Both execution attestation
and qualification credit remain false in inspection. No extraction result is
profitability evidence or permission to trade.
