# Hypothesis-linked trial registration

The service retains an immutable experiment plan linked to a specific hypothesis
publication before future trial execution. Registration loads the publication
from application storage; the caller cannot supply a publication, source pin,
result or qualification flag. This is a bounded part of issue #16. It does not
execute a trial, admit a dataset, collect prices or evaluate outcomes.

## Commands

Prepare a sealed recipe using the [experiment contract](experiments.md), then
register it against an existing [publication](retained-hypotheses.md):

```sh
./bin/mantik hypothesis-trial-register --api-url http://127.0.0.1:40880 \
  --receiver-id RECEIVER_ID --id TRIAL_ID --publication PUBLICATION_ID \
  --recipe /absolute/private/recipe.json --recipe-sha256 RECIPE_SHA256
./bin/mantik hypothesis-trial --api-url http://127.0.0.1:40880 --id TRIAL_ID
./bin/mantik hypothesis-trials --api-url http://127.0.0.1:40880 --id HYPOTHESIS_ID
```

Use a stable explicit trial request ID and the pinned receiver identity. Existing
`MANTIK_API_TOKEN` authentication applies. `--parent PARENT_TRIAL_ID` optionally
links a new plan to an existing trial for the same exact hypothesis. Publication,
trial and parent IDs use 1..128 ASCII letters, digits, underscores or hyphens.
A parent must already exist; a trial cannot parent itself. Registration binds the
parent's immutable receipt hash. Parent and child may cite different publications
of the same exact question, preserving each publication's attribution.

The registration receipt includes the full recipe, hypothesis/declaration
identity, publication ID and hash, optional parent ID/hash, server registration
time, and its own SHA-256. `hypothesis-trials` returns up to 32 compact summaries
sorted by registration time and ID. Inspect an individual trial for its full
recipe. An existing hypothesis with no trials returns an empty history; an
unknown hypothesis returns 404. No original articles or raw model transcripts
are included in these readbacks.

## Binding rules

The service independently validates the retained publication's sealed declaration
and the complete experiment recipe using `internal/experimentplan`. Recipe pins
include benchmark and cost assumptions; hypothesis method references pin native
strategy configurations. Their hashes use different contracts and are not
interchangeable. Registration resolves both and checks exact configuration and
library-entry equality. Every strategy in the recipe must be one of the
hypothesis's applicable methods. A plan may select a nonempty subset of those
methods; both existing nonexecuting benchmark controls remain mandatory.

The recipe's window duration must equal the hypothesis's declared horizon exactly.
Cutoffs, dataset identity/digest, required input keys, methods, benchmarks, capital,
cost model and planned metrics retain the existing v1 experiment validation.
There is no implicit horizon extension, parameter change, missing benchmark or
real-symbol substitution. For the built-in recipe, declare a matching horizon
and both applicable native methods before publication, or supply a reviewed
recipe that matches the already-published declaration. Registration never edits
the publication to make an incompatible recipe fit.

The server time must be valid and no earlier than the publication, parent or any
previous trial registration. Exact retries return the original receipt without
consulting the current clock. Reusing an ID with a different publication, parent
or sealed recipe conflicts. A new ID retains a separate plan even when its recipe
matches an earlier one. V1 retains at most 32 registrations per database, across
all hypotheses. It never evicts history; exact retries remain available at capacity.

A closed decision case does not adjudicate research validity. Historical
publications remain usable for planning tests after the originating case closes
or receives new evidence. Registration pins that historical declaration and
attribution; it does not claim that the old review is currently applicable.
Inspect the linked review and case separately when assessing current evidence.

`status` remains `registered`, authority `research_trial_declaration_only`,
`qualification_credit: false`, and `dataset_availability_verified: false`.
Dataset dates may precede registration: those are declared replay observation
times, not evidence of when the research idea was conceived or first tested.
Registration records admission before any future execution through this trial's
application-owned protocol. It does not prove that an existing dataset was unseen,
that no prior experiment ran, or that the standalone `experiment` CLI used this
registration. Result ingestion, execution ownership, interruption/cancellation,
failed-attempt history, condition evaluation and promotion remain separate work.

## HTTP and persistence

Authenticated `POST /v1/hypothesis-trials` accepts:

```json
{
  "receiver_id": "RECEIVER_ID",
  "request_id": "TRIAL_ID",
  "publication_id": "PUBLICATION_ID",
  "parent_id": "OPTIONAL_EXISTING_TRIAL_ID",
  "recipe": {}
}
```

Omit `parent_id` for a root plan. `recipe` must contain a complete sealed
`mantik-experiment-recipe-v1` object, not the empty example. The envelope is
bounded to 1 MiB plus 4 KiB, with a canonical recipe bound of 1 MiB. Strict service
decoding rejects unknown, duplicate and incorrectly cased field names, trailing
JSON, excessive nesting and invalid UTF-8. Existing pinned library entries retain
their capitalized Go JSON field names, such as `ID` and `ContractVersion`; these
are checked exactly rather than renaming the v1 wire representation.

`GET /v1/hypothesis-trials/{id}` returns one full registration.
`GET /v1/hypotheses/{id}/trials` returns compact planned lineage. Routes accept
no query parameters. Invalid input returns 422; missing publication, parent,
trial or hypothesis 404; receiver, identity, method, horizon, parent, clock or
capacity conflicts 409; persistence failure 503. Receipt hashes use Go JSON of
the complete typed object with its own `sha256` field empty. Parent/publication
hashes bind their complete typed receipts, including original registration or
publication times.

Migration 14 adds `mantik_hypothesis_trials` with publication and parent foreign
keys. Trial writes serialize through the retained receiver row, independently of
execution state. Capacity, parent ordering, retry checks and insertion share one
transaction. Failed persistence does not acknowledge a registration. Memory
storage uses the same contract under its mutex. Existing publications/reviews,
news cases and execution state are preserved. Back up before upgrading an existing
service. Older binaries reject schema 14; rollback requires a compatible database
backup and its matching binary/image. This change does not upgrade existing pilot
services automatically.
