# Reported-revenue diagnostic profile

`news-extract-switchyard --profile reported-revenue-v1` selects a separate,
versioned task for one historical issuer revenue report. It accepts only the
[diagnostic publication format](switchyard-extraction.md#historical-diagnostic-publication-precision),
requests exactly 1,024 output tokens and low reasoning through
`openai/gpt-6.1-sol`, and retains one attempt without retries. It is opt-in;
omitting the flag preserves the existing general extractor and its wire bytes.
No actual request is authorized merely by selecting this profile.

```sh
umask 077
./bin/mantik news-extract-switchyard \
  --article /private/diagnostic-article.json \
  --event-key issuer-revenue-review \
  --endpoint http://127.0.0.1:11434 \
  --model openai/gpt-6.1-sol --reasoning-effort low --max-tokens 1024 \
  --profile reported-revenue-v1 \
  --output-dir /private/new-revenue-attempt

./bin/mantik news-switchyard-inspect --dir /private/new-revenue-attempt
./bin/mantik news-switchyard-evaluate --manifest /private/revenue-manifest.json
```

Keep receipts and reports private. This profile cannot import a capture, approve
research evidence or create an operational action. Existing exact-time source
inputs are rejected before generation when this profile is selected. Local-model
generation does not accept the profile flag. There is no model or profile fallback.

## Structural contract and semantic target

The model still returns the existing quote-only response shape. An extracted
response must contain exactly one claim, code `reported_revenue` and assessment
`reported_unverified`. The application rejects other codes, assessments or claim
counts; it does not replace synonyms with the canonical code or keep a subset.
Normal source/claim and unique exact-quote checks also apply. Explicit deliberate
abstention remains available with no claims and the existing source insufficiency,
no-relevant-claims or ambiguity reasons.

The versioned prompt targets these additional semantic rules:

- Extract one clear historical issuer revenue report. Forecasts, estimates,
  expenses, profit, prices and recommendations do not count as reported revenue.
  Abstain if multiple issuers, periods or measures prevent a single clear report.
- Attribute the assertion to the issuer and preserve the stated amount, units,
  reporting period and growth qualifiers. Do not infer a currency, period, year,
  ticker or rate from metadata or outside knowledge.
- Select one to four unique exact quotes that collectively support all assertion
  details. Include the title when it establishes the issuer or period; either a
  separate exact title quote or a contiguous title-plus-fragment quote is allowed.
  Keep source whitespace and units. Do not remove necessary context to shorten a
  quote. Assertion paraphrases are permitted when their meaning and qualifiers
  are preserved.

Those semantic rules are instructions and review criteria. The structural
validator does not prove an amount is revenue, an assertion follows from its
quotes, or a selected span contains all necessary context. Tests deliberately
retain a structurally valid unsupported assertion to make this limit explicit.
Source-support review remains separate from structural acceptance.

## Versioned evidence and replay

The profile is retained in configuration as `profile: reported-revenue-v1`.
Its source contract and receipt pins distinguish it from earlier captures:

| Pin | Value |
| --- | --- |
| Receipt schema | `mantik-switchyard-revenue-diagnostic-v1` |
| Inspection schema | `mantik-switchyard-revenue-inspection-v1` |
| Prompt version | `reported-revenue-quotes-v1` |
| Extraction contract | `mantik-revenue-diagnostic-extraction-contract-v1` |
| Receipt authority | `switchyard_revenue_diagnostic_candidates_only` |

The extraction contract limits claims to one and adds a `profile_policy` check.
The exact system prompt, selected source bytes and ordinary gateway generation
settings are bound by the prepared request hash. The source/contract, model bytes,
quote derivation and result replay are checked offline before accepting receipts.
A renamed profile, changed prompt version, mismatched contract or relabeled
receipt is rejected. Profile selection is not sent as an extra gateway option;
the existing bounded Switchyard/OpenCode route receives the usual supported
request fields and generation limits.

A model response that passes quote anchoring but violates structural profile
policy has disposition `profile_policy_rejected`. Original model content and
its anchoring evidence remain retained, while no extracted claim set is emitted.
Its application result is an unavailable-extractor abstention, not a model-declared
`no_relevant_claims` abstention. HTTP failures and unknown completion retain their
own dispositions. No failure triggers a retry or changes the original files.

Offline evaluation validates expected annotations against this same profile,
including expectations for prepared-only unknown completion. An unsupported code
or assessment in the supplied annotation rejects the entire invocation. All
finished failures and unknown outcomes remain visible. A cohort must use one
contract; profile and general-extractor captures must have separate reports.
Strict claim comparison is unchanged: accepted paraphrases or different valid
quote spans may still be exact mismatches. No semantic-match score, qualification
threshold or source-truth authority is added.

## Relation to the first diagnostic

The [October 19 diagnostic](../research/openai-diagnostic-20261019.md) used the
general profile. Its three strict mismatches, frozen expectations and original
outputs are preserved. Subsequent user review accepted source support for all
three claims, including NVIDIA's different wording and title-inclusive span;
that was post-generation source-support review, not blinded gold annotation or
representative accuracy measurement. The review receipt remains private beside
the diagnostic evidence.

This profile specifies future behavior; it does not retrofit those three outputs
or establish an improvement. Implementation first passed fixture checks. A later, separately authorized
[six-case diagnostic](../research/revenue-diagnostic-20261019.md) completed three
revenue extractions and three expected abstentions with this pinned profile.
Its positive claims still differ in wording under exact comparison. Both the
earlier three-request and the later six-request budgets are exhausted; another
actual run needs a new finite request budget. Define and review future expected labels and evidence rules
before generation, preserve a held-out/abstention sample, retain every attempt,
and keep source support, exact agreement, provider provenance, strategy
qualification and profitability as separate claims.
