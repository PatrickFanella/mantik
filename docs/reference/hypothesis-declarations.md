# Pre-test hypothesis declarations

`hypothesis-declare` validates and hashes a structured research declaration. It
establishes the input contract for the hypothesis lifecycle in issue #16. It does
not publish a hypothesis, verify cited evidence, run a trial or prove that the
file existed before an experiment. Application-owned publication and exact-question reuse are described in
[retained hypotheses](retained-hypotheses.md); trials and promotion remain separate
work.

```sh
./bin/mantik hypothesis-declare --export-fixture > /absolute/private/hypothesis.json
./bin/mantik hypothesis-declare --file /absolute/private/hypothesis.json
./bin/mantik hypothesis-declare --export-method demo-half-size-v1
```

`--export-method ID` prints the supported configuration and its exact reference
for use in a declaration; it does not execute the method. Select exactly one
mode: file validation, fixture export or method export.

The invented fixture proposes a positive marked result from a synthetic threshold
method. It is a research question, not an observed result or profitability claim.
The command reads at most 64 KiB, prints a deterministic result and leaves the
input file unchanged. No API, model, provider, database or execution call occurs.

## Declaration contract

Schema `mantik-hypothesis-declaration-v1` requires:

| Field | Meaning and bound |
| --- | --- |
| `label` | Human-selected identifier, 1..80 ASCII letters/digits/underscores/hyphens |
| `evidence_class` | Exactly `synthetic_unqualified` in v1 |
| `instrument` | Exactly `DEMO`, matching the supported native methods |
| `mechanism` | Bounded code and 1..1,024-byte UTF-8 statement proposing the mechanism |
| `prediction` | Condition naming a metric, unit, comparison and integer threshold |
| `null` | A nonempty condition that does not overlap the prediction |
| `refutation` | A nonempty condition that does not overlap the prediction |
| `horizon_seconds` | Explicit horizon, 1..86,400 seconds |
| `methods` | 1..2 distinct exact native strategy configuration references |
| `origins` | 1..8 distinct attributed claim references and recording digests |

Mechanism/condition codes use the same identifier bound as labels. Condition codes
must distinguish prediction, null and refutation. Text cannot have surrounding
whitespace or control characters. Original claim keys use `news-` plus a 64-digit
lowercase SHA-256 identity; recording digests must be 64 lowercase hexadecimal
digits. References are bounded to 256 UTF-8 bytes and are not fetched.

A review's candidate contains claim/refutation codes and evidence keys; it does
not contain this full declaration. The operator supplies the complete declaration
to the [application-owned publication step](retained-hypotheses.md), which checks
the authoritative review and current evidence independently. Offline validation
alone does not publish or verify evidence.

## Condition semantics

V1 uses the existing synthetic experiment metric names: `realized_pnl_cents` and
`marked_pnl_cents` in `cents`, or `observations` and `actions` in `count`.
Prediction, null and refutation must name the same metric and unit. Comparisons
are `lt`, `le`, `eq`, `ne`, `ge` and `gt`, using discrete signed integer values.
Thresholds are limited to ±1,000,000,000,000; count thresholds cannot be negative.
Count conditions are interpreted only over nonnegative integers.

The validator computes each condition's integer intervals. Prediction cannot
overlap either null or refutation, including shared inclusive boundaries. Empty
conditions such as `count < 0` are rejected. Null and refutation may overlap;
there may also be gaps between all three conditions, leaving future observations
inconclusive. Validation does not claim that a scalar measurement is sufficient
to establish a causal mechanism, statistical significance or research validity.

For example, `marked_pnl_cents > 0`, null `<= 0`, and refutation `< 0` are a
consistent declaration. Prediction `>= 0` with null `<= 0` is rejected because
both include zero. The command performs these logical checks without computing
an experiment metric or changing the declared thresholds.

## Exact method scope

The supported references use `schema: native-strategy-config-v1`. They pin the
complete native library entry and numeric strategy configuration for
`demo-threshold-v1` or `demo-half-size-v1`, including declared checks, quantity and
safety conditions. A reference contains its exact ID and the SHA-256 of Go JSON
for `{schema, entry, strategy}`. The validator independently resolves the native
configuration and requires an exact match; changing a hash, ID or pin schema is
rejected. Result readback includes those resolved definitions for inspection.

This configuration pin is distinct from an experiment's full method pin, which
also binds cost/benchmark assumptions. They are not interchangeable. Neither pin
is a compiled binary/image hash. Experiment recipes, source/image identity and
mandatory execution checks remain separate contracts. Reading a pinned safety
configuration does not authorize changing it or executing the strategy.

The current methods are synthetic DEMO fixtures. Real symbols, real-feed classes,
nonexecuting controls, generated methods and additional metrics need their own
reviewed adapter/contracts before admission here. Validation does not silently
map a real observation to the synthetic fixture.

## Identity and attribution limits

Method references and origin references are canonical sets. The validator sorts
detached copies, rejects duplicates and leaves caller-owned data untouched.
`declaration_sha256` hashes the complete canonical declaration using Go JSON.
Changing a label or supporting source therefore changes that exact declaration
identity.

`question_sha256` hashes the same canonical declaration with `label` empty and
`origins` null. The resulting `hypothesis-` ID is an exact matching key for the
research question, instrument, class, conditions, horizon and method pins.
Changing any of those changes the question identity. Alternate labels or source
references for that exact question keep the same question key while retaining
different declaration hashes. Equivalent prose, alternate units or logically
equivalent condition expressions are not semantically normalized.

This key supports service publication reuse checks; it does not merge records, discard
contradictory evidence or select a winning trial. Supplied origins are preserved
as declarations. A valid digest shape does not prove that a source exists,
matches the claim, remains current or supports the proposed mechanism. Result
flags remain `origins_verified: false` and `qualification_credit: false`, with
`authority: research_declaration_only` and a validation scope limited to structure
and native method pins. A deterministic hash alone is not proof of pre-test
registration or an immutable research history.

Strict decoding rejects unknown, duplicate and noncanonical field names, trailing
values, excessive nesting, invalid UTF-8 and oversized input. Model-supplied risk
policy, qualification, result, promotion or execution fields are not accepted.
