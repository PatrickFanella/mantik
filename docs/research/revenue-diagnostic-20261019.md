# Six-case reported-revenue diagnostic, 2026-10-19

The opt-in `reported-revenue-v1` profile completed six authorized requests: all
three revenue inputs produced one checked revenue claim, and all three non-revenue
inputs produced the expected abstention. Strict comparison reports three exact
cases out of six, all abstentions. Positive cases differ only in assertion
wording. This is a finite diagnostic result, not representative model accuracy.

## Scope and pinned execution

The owner accepted a concrete six-case proposal and its draft expectations before
generation. The limit was six sequential requests, one per case, using
`openai/gpt-6.1-sol` through existing Switchyard/OpenCode, 1,024 requested output
tokens, low reasoning, a 30-second client deadline, and no retries, tools or
fallback. No additional external source reads were required during the run.
All six requests were consumed; this authorization has no remaining model budget.

The exercised source is `b5e0a74433274ac6495364f8676a762beed3ad28`, the merge of
[PR 119](https://git.subcult.tv/subculture-collective/mantik/pulls/119).
Its exact merged-main CI was read back successful before dispatch. The selected
[profile](../reference/revenue-diagnostic-profile.md) has prompt version
`reported-revenue-quotes-v1` and contract
`mantik-revenue-diagnostic-extraction-contract-v1`. Fresh checks confirmed the
pinned deployed API/OpenCode images, unchanged extraction plugin/agent files,
active plugin, managed OpenAI OAuth type, and idle broker/shared executor.

Prepared inputs preserve publication dates explicitly, use actual source retrieval
instants for observation and recorded input materialization instants for receipt.
No exact publication instant was invented. Source/annotation files were hashed
before generation; the final audit verified that they were unchanged.

## Source selection and outcomes

Positive cases select a title and revenue fragment from later issuer releases;
negative cases select non-revenue financial fragments from those same releases.
Only the selected text is supplied to the model, not the full release.

| Source family | Positive outcome | Negative outcome | Strict exact cases |
| --- | --- | --- | --- |
| [Apple FY25 Q3](https://www.apple.com/newsroom/2025/07/apple-reports-third-quarter-results/) | One `reported_revenue` claim | EPS-only fragment: `no_relevant_claims`, zero claims | Negative only |
| [Microsoft FY25 Q4](https://www.microsoft.com/en-us/investor/earnings/fy-2025-q4/press-release-webcast) | One `reported_revenue` claim | Operating-income-only fragment: `no_relevant_claims`, zero claims | Negative only |
| [NVIDIA FY26 Q2](https://nvidianews.nvidia.com/news/nvidia-announces-financial-results-for-second-quarter-fiscal-2026) | One `reported_revenue` claim | EPS-only fragment: `no_relevant_claims`, zero claims | Negative only |

All six HTTP responses were 200 with disposition `response_complete`. Each client
attempt had one new completed broker request, one new extraction session and one
extractor assistant turn with `gpt-6.1-sol`, low reasoning and finish `stop`.
There were no retries, observed tools, failures, profile-policy rejections or
unknown completions. All six offline receipt inspections passed.

The positive claims match their expected code, `reported_unverified` assessment,
entities, symbols and complete title-plus-revenue evidence. Apple and Microsoft
assertions differ from their draft expectations by hyphenation. NVIDIA uses a
different assertion word order and capitalization. These are observed field
differences, not independent semantic adjudication or semantic-match credit.

The unchanged exact evaluator reports six finished cases, three exact cases,
zero exactly matched claims, three missing expected claims and three unexpected
actual claims. It reports three expected abstentions, with no missed or unexpected
abstentions. Positive claim wording remains part of strict matching even though
the profile permits supported paraphrases. The report replayed byte-identically.
Neither expected labels nor matching rules were changed after generation.

OpenCode reported 7,435 input, 392 output and 77 reasoning tokens, with no cache
usage reported. Its zero cost field is not a billing receipt or proof of zero
charge. Runtime observations and tested forwarding do not supply provider-native
enforcement attestation or cryptographic provider provenance.

## Evidence and operational boundary

Private material is retained under
`~/.local/state/mantik/revenue-profile-diagnostic-20261019/`.
It includes the frozen inputs/expectations and binary, exclusive dispatch intents,
client stdout/stderr, prepared/finished captures, inspection, broker and executor
readbacks, deterministic evaluation and completion audit. Full source acquisition
and the accepted draft proposal remain separately retained at
`~/.local/state/mantik/revenue-cohort-proposal-20261019/`.

The contract hash is
`23fea22f3baa600a96fe9522c0e1e18a84def2a5636f17bee2ef3cf5cfb980ba`;
the report hash is
`a8e69f7e7a090cfb33d7bd9327161ed09f48aeca29d5cdd9d08fdf6151128640`.
`completion-result.json` and `evidence-integrity.json` retain counts, per-case
findings and byte hashes. Raw originals, responses and credentials remain outside Git.

No service capture import, research admission, case, operational action, order or
economic effect was created. Final Docker inspection preserved all five original
container identities, PIDs and immutable images. The user-curtailed observer
remained inactive with PID 0. This adds no deployment or 24-hour observation proof.

## Interpretation limits

The six inputs form only three dependent positive/negative source pairs. They
were not used in prior Mantik generation or profile development, but use the
same issuers as development examples and may have appeared in model pretraining.
Labels were agent drafts accepted by the user before generation, not blinded
independent gold annotations. Deliberately selected fragments do not test full
releases, multiple revenue measures, forecasts, ambiguity or broad news coverage.

The results support extraction and abstention behavior for these six inputs
under this pinned profile. They do not establish representative accuracy, a
measured improvement over the different earlier cohort, strategy qualification
or profitability. The earlier three-case strict result and subsequent user
source-support review remain separate, unchanged evidence. Further model calls
require a new finite authorization; no diagnostic capture gains service-admission
or trading authority from this result. Issue 12 remains open.
