# Cutoff-visible candidate replay inputs

`candidate-replay-plan` derives a synthetic candidate job using only parent
records visible at an explicit cutoff. It retains the complete original fixture
plan and one exposure row for every parent record, including future records that
are excluded. It does not execute source, alter the parent or grant qualification.

The parent must be a valid [candidate fixture plan](candidate-fixture-tests.md)
covering every record of its exact original synthetic dataset. Unsupported
quality, revisions, duplicates or excessive records cannot be hidden by choosing
a cutoff: parent validation happens before projection.

## Derive an explicit frame

```sh
umask 077
./bin/mantik candidate-replay-plan --plan fixture-plan.json \
  --plan-sha256 "$(jq -r .sha256 fixture-plan.json)" \
  --dataset parent-dataset.json --cutoff 2026-10-01T12:00:05Z \
  > replay-plan.json
```

There is no current-time fallback. Visibility is the later of `available_at` and
`received_at`; records visible exactly at the cutoff are included. Included
records are ordered by visibility, then source key for ties. This can differ
from occurrence order and retained file order. A delayed old observation enters
when it becomes visible; it does not appear retroactively at its occurrence.

`mantik-candidate-replay-plan-v1` contains the full parent plan, UTC cutoff,
exposures in original parent order, replay order, and (when nonempty) a derived
dataset and behavioral job. Each exposure retains its parent index, case ID,
visibility time and `included` or `excluded_future` disposition. Every parent
case remains accounted for, without filtering a frame based on worker results.

The derived dataset preserves selected original records and their exact original
quote strings, mapping/access provenance and timestamps. Its deterministic ID
binds the parent dataset's canonical identity and cutoff. Changing annotations
alone does not rename identical input data. The derived candidate preserves the
original source, hypothesis and toolchain pins and acquires a new dataset binding.
Annotations are copied from their corresponding parent cases into replay order;
a changed annotation changes the fixture/job/replay identities.

A cutoff with no visible inputs produces status `no_visible_inputs`, empty replay
order, and null dataset/job. This is a retained declaration, not a successful
empty worker run. Nonempty frames have status `declared`. Execution and
qualification flags remain false.

## Export verified inputs and execute separately

```sh
replay_sha=$(jq -r .sha256 replay-plan.json)
./bin/mantik candidate-replay-input --plan replay-plan.json \
  --plan-sha256 "$replay_sha" --dataset parent-dataset.json \
  --artifact dataset > replay-dataset.json
./bin/mantik candidate-replay-input --plan replay-plan.json \
  --plan-sha256 "$replay_sha" --dataset parent-dataset.json \
  --artifact job > replay-job.json
./bin/mantik candidate-attempt-run \
  --journal /absolute/private/candidate-attempts --request-id cutoff-005 \
  --job replay-job.json --job-sha256 "$(jq -r .sha256 replay-job.json)" \
  --dataset replay-dataset.json --approved-image-sha256 "$image"
```

Export requires the exact original parent dataset again. It rederives every
exposure, selected record, order, annotation and job, then compares the complete
replay identity. Rehashing a modified replay artifact alone cannot substitute a
different projection. `--artifact plan` emits the fully verified replay artifact
for inspection. Unknown fields, aliases/duplicates, trailing JSON and invalid
pins reject before export.

Dataset export writes the exact compact Go JSON bytes used by the derived
candidate pin, without adding a newline. Do not reformat or append bytes to that
file before launch. Job export can use normal JSON formatting because its
content identity is validated independently. Exporting a job/dataset from an
empty frame exits nonzero without output; exporting its plan remains available.

For multiple cutoffs, retain each frame and launch each executable job as a new
explicit [journal request](candidate-attempt-journal.md). Each frame starts a
fresh worker, with candidate initialization once and state carried between calls
inside that frame. Frames do not share a candidate process, trading positions,
capital, timer state or economic effects. Keep no-input frames and failed or
incomplete worker attempts alongside successful frames.

## Scope and authority

Replay plans are bounded to the parent 1..64 usable revision-1 cases and the
existing 8 MiB dataset bound, plus two behavior-job bounds and 32 KiB metadata.
They have authority `synthetic_candidate_replay_declaration_only`. The standalone
artifact does not verify execution origin, image/runtime isolation or service
binding. Use actual worker receipts for those separate observations.

This establishes availability-cutoff input selection and deterministic ordering
for synthetic candidate checks. The parent artifact is fully available to the
caller preparing the plan, and source/hypothesis creation may already have seen
future data. This does not prove unseen/out-of-sample research, prevent embedded
foreknowledge, simulate realistic fills, replay service economics, qualify a
strategy or establish profitability. The [candidate acceptance audit](../research/candidate-evaluation-acceptance.md)
records the separate generated-source and service-owned lineage evidence. No
provider, service, account or order is contacted by these projection commands.
