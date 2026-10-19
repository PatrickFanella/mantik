# First bounded OpenAI real-news diagnostic, 2026-10-19

Three sequential requests through Switchyard/OpenCode to `openai/gpt-6.1-sol`
completed with one source-bound revenue claim each. This establishes a narrow
working extraction path for the selected historical excerpts. The strict
comparison found zero exact matches to the agent-drafted annotations. It does
not establish representative news accuracy or trading readiness.

## Scope and source

The owner authorized at most three requests, one per retained Apple FY25 Q2,
Microsoft FY25 Q3 and NVIDIA FY26 Q1 excerpt, with 1,024 output tokens, low
reasoning, a 30-second client deadline and no retries. Source text and draft
expectations were prepared before the evaluated generations. Labels were checked
by the same agent, without independent annotation review.

The initial exact-time preflight stopped without a model call: issuer pages had
only reported publication dates, while SEC acceptance times described different
events. [PR 117](https://git.subcult.tv/subculture-collective/mantik/pulls/117)
added a separate [diagnostic publication format](../reference/switchyard-extraction.md#historical-diagnostic-publication-precision).
The owner continued that work; this run used explicit date-only publication,
actual retained retrieval instants for observation, and current materialization
instants for receipt. No exact publication instant was invented. The original
proposal, stopped preflight and expected labels remain retained unchanged.

The exercised Mantik source is `41f86ead983b4112571216079b43fb888e2365a3`, the
PR 117 merge. Its tree is identical to tested head
`98911e2df3e4429ba4a93d3ab93cadf9a40f0e20`. Exact-head PR CI, local verification,
race checks and the 24-command fake-provider rehearsal passed before merge;
merged-main push CI remained pending at the completion inspection. These are
separate proof states. No existing Mantik application was upgraded.

Fresh preflight confirmed the pinned Switchyard/OpenCode release, active
extraction plugin, existing managed OpenAI OAuth credential type, idle broker and
idle shared executor. The generation limits and retry/tool restrictions were
unchanged. Deployment evidence and forwarding limits remain described in the
[route checkpoint](../reference/switchyard-extraction.md#verified-setup-checkpoint--2026-10-16).

## Observed outcomes

| Excerpt | Client and runtime result | Difference from supplied annotation |
| --- | --- | --- |
| Apple | One HTTP 200, complete response, one checked claim; one completed broker request, extraction session and extractor assistant turn | Claim code only. The assertion, entities, symbols and quote span match. |
| Microsoft | One HTTP 200, complete response, one checked claim; one completed broker request, extraction session and extractor assistant turn | Claim code and assertion wording. The model adds the quarter to the assertion. |
| NVIDIA | One HTTP 200, complete response, one checked claim; one completed broker request, extraction session and extractor assistant turn | Assertion wording and evidence span. The model quotes the title as well as the revenue fragment. |

All three assistant turns used the extractor agent with provider `openai`, model
`gpt-6.1-sol`, variant `low` and finish `stop`. No retry, extra extractor turn,
failure or unknown completion was observed. Each actual source quote and derived
span passed Mantik's checks. The three-request budget is exhausted.

The [offline evaluator](../reference/switchyard-extraction-evaluation.md) retained
all three distinct prepared receipts and all three finished outcomes. Exact
matching reported zero exact cases, zero matched claims, three missing expected
claims and three unexpected actual claims, without abstentions. This comparison
requires the claim code, assertion and evidence to match; local claim IDs are
ignored. Same-agent inspection finds the revenue assertions supported by the
supplied fragments, but no semantic-match credit or independent adjudication
was added. Expectations and the matching rule were not changed after generation.
The report replayed byte-identically.

OpenCode reported totals of 2,851 input, 332 output and 43 reasoning tokens,
with no cache usage reported. Its zero reported cost is not a billing receipt
or proof of zero charge. Runtime observations and tested forwarding do not
supply provider-native enforcement attestation or cryptographic provider origin.
A client timeout would still not prove upstream cancellation; no timeout occurred.

## Retained evidence and limits

Private evidence is under
`~/.local/state/mantik/real-news-openai-diagnostic-20261019/`.
`intent.json` and `frozen-inputs.json` pin source, binary, inputs and draft labels
before generation. Each dispatch has an exclusive intent, client stdout/stderr,
prepared/finished receipts, offline inspection, broker readbacks and executor
session/messages. `completion-result.json`, `completion-report.md` and
`evidence-integrity.json` retain the final audit and file hashes.

The diagnostic extraction contract hash is
`769aa10108c9ae24434f474f78124328996e7937c94ba0c0abf0fd9bbf896946`;
the evaluation report hash is
`266694ee54c1c14b5b41f3fcfc2b243b80c98706c61f4525374ad2d24cd264b8`.
Original sources, model responses, credentials and generated reports stay out of Git.

No service import, research admission, case, operational action or economic
effect was created. Final Docker inspection preserved the identities, PIDs and
immutable images of all three original Mantik pilot applications and the
Switchyard API/OpenCode pair. The curtailed observer remained inactive with PID 0.
This adds no deployment or 24-hour observation proof.

The sample contains three convenient short historical revenue fragments, with
no held-out or negative/abstention cases, unknown pretraining exposure and
agent-draft labels. It cannot establish generalization, source independence,
independent factual truth, extraction accuracy, strategy qualification or profit.
The next research step is a declared claim-code/annotation policy and independent
review before selecting a broader held-out cohort. Retain these disagreements;
do not treat a tuned rerun as independent evidence. Further actual model requests
need a new finite budget. Issue 12 remains open.
