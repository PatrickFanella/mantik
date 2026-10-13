# Dataset and replay contracts

Mantik can export a sealed synthetic dataset and replay it at explicit cutoffs.
This is a research operation with a separate memory store. It neither writes
to the running service nor contacts a provider. The current adapter accepts
only fixture quotes and `synthetic_unqualified` datasets. A replay result has
no qualification credit.

## Dataset identity and provenance

`mantik-dataset-v1` contains a dataset ID, version, evidence class, records, and
a SHA-256 content identity. Each record retains:

- Logical source key and revision number.
- Occurrence, source availability, and local receipt times.
- Exact original source bytes as a JSON string and their SHA-256 identity.
- Source symbol, normalized instrument, and mapping version.
- Source reference, declared license, and retrieving actor.
- Quality status (`usable`, `quarantined`, or `missing`) and a reason for gaps.

Access provenance records declarations; it does not verify provider rights.
Original bytes remain in the dataset for targeted inspection. A missing record
can retain an empty original and its corresponding hash. Quarantined originals
can be malformed, but their metadata, identity, and reason are still required.
Usable quotes must match their retained original times and source symbol and
pass the implemented quote schema.

The Go `replay.Seal` operation validates and copies a dataset before assigning
its content identity. The hash covers the whole manifest, including provenance
and quality declarations. Repeated identical logical-key/revision records are
allowed; changed content or metadata under the same revision conflicts. Reads
reject changed source bytes, changed manifest content, incompatible schemas,
unknown fields, and trailing JSON. Changing content creates a new fingerprint;
retain earlier artifacts and use a new dataset version when publishing a change.

## Clock and decision behavior

Every run pins the dataset fingerprint, `demo-threshold-v1`,
`mantik-library-v1`, and `demo-evaluation` recipe version `v1`. Unsupported
strategy or recipe pins fail explicitly. The replay clock starts empty and
advances through nonzero, strictly increasing cutoffs supplied by the caller.
It never falls back to wall-clock time.

A record becomes visible at the later of source availability and local receipt.
Records are processed forward in that order, with logical key and revision as
stable tie-breakers. A cutoff includes records visible exactly at that time.
Future records are excluded from the frame's evidence and decisions. Each frame
retains a copied snapshot, including finalized decisions and evaluation results.
Adding intermediate cutoffs does not change the final economic decisions.

The original observation stays intact. The execution input uses the normalized
instrument, an ID derived from logical key/revision, and its effective visibility
time as receipt time. This makes delayed availability count toward quote age.
Late or out-of-order quotes retain the workflow's structured abstentions.
Identical revisions are reported as duplicates and create no extra actions.

Corrections (revision greater than one) are retained with
`correction_review_required`. The current adapter cannot safely retract an
earlier finalized decision, so it preserves prior economics and blocks further
admission. Missing and quarantined records also block further admission once
visible. A gap or correction blocks all new actions at the same timestamp,
regardless of key order. Later frames report `admission_blocked`; a new reviewed
dataset/run is required to resolve that state. Existing positions in an
incomplete research replay are not evidence of protective runtime recovery.

Dataset reads are limited to 8 MiB and 10,000 records. Runs accept up to 10,000
cutoffs and cap serialized frames at 32 MiB. Reduce the dataset or cutoff count
when that output limit is reached. Replay results reference original content
hashes; retain the pinned dataset alongside them.

## CLI export and file replay

Build with `mise exec -- make build`, then export the built-in fixture into
ignored local storage:

```bash
mkdir -p data
./bin/mantik replay --export-dataset > data/demo-dataset.json
dataset_sha=$(python3 -c 'import json; print(json.load(open("data/demo-dataset.json"))["sha256"])')
./bin/mantik replay \
  --dataset data/demo-dataset.json \
  --dataset-sha256 "$dataset_sha" \
  --cutoffs 2026-01-05T15:01:00Z,2026-01-05T15:03:00Z \
  > data/demo-replay.json
```

For subsequent experiments, retain the expected fingerprint independently of
the file. The CLI requires that pin for file replay and rejects a replaced
dataset even when the replacement has a valid internal hash. Omitting
`--dataset` selects the built-in fixture. Export cannot be combined with replay
flags. Store generated datasets and results outside Git; `data/` is ignored.

Real data adapters, historical admission, provider entitlements, and a dataset
catalogue are separate work. This format establishes their provenance and replay
boundary without granting access to existing Augr records or services.
