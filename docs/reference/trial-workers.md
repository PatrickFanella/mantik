# External jobs bound to retained native trials

The service can retain an external comparison job against one exact completed
native trial run. This connects a registered hypothesis/recipe and its native
reference to the [durable local external worker](backtesting-fixture.md#durable-external-worker-commands).
The HTTP service does not launch containers or accept comparison results in this
unit. Declaration, local execution and later result admission are separate acts.

## Declare and export

First register a trial, retain an attempt and execute its native fixture through
the existing [native trial commands](native-trial-runs.md). Read that run's full
receipt hash. Then declare the reviewed external image and source identities:

```sh
./bin/mantik trial-worker-declare --api-url "$MANTIK_API_URL" \
  --receiver-id "$MANTIK_RECEIVER_ID" --id external-comparison-1 \
  --attempt native-attempt-1 --native-run-sha256 "$MANTIK_NATIVE_RUN_SHA" \
  --image-sha256 "$MANTIK_BT_IMAGE" \
  --adapter-sha256 "$MANTIK_BT_ADAPTER" \
  --requirements-sha256 "$MANTIK_BT_LOCK"

umask 077
MANTIK_EXPORT_DIR="$(mktemp -d)"
mkdir "$MANTIK_EXPORT_DIR/inputs"
./bin/mantik trial-worker --api-url "$MANTIK_API_URL" \
  --id external-comparison-1 --job-only > "$MANTIK_EXPORT_DIR/job.json"
for MANTIK_INPUT_NAME in dataset.json recipe.json native.json; do
  ./bin/mantik trial-worker-input --api-url "$MANTIK_API_URL" \
    --id external-comparison-1 --name "$MANTIK_INPUT_NAME" --raw \
    > "$MANTIK_EXPORT_DIR/inputs/$MANTIK_INPUT_NAME"
done
./bin/mantik external-worker-launch --job "$MANTIK_EXPORT_DIR/job.json" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_EXPORT_DIR/job.json")" \
  --input-dir "$MANTIK_EXPORT_DIR/inputs" \
  --out-dir "$MANTIK_EXPORT_DIR/physical-attempt-1" \
  --approved-image-sha256 "$MANTIK_BT_IMAGE"
```

Service commands use the existing `MANTIK_API_TOKEN`. The local launcher scrubs
its Docker client's environment and forwards no token to the worker. Image and
source pins are declarations; explicit local image approval and actual host
inspection remain necessary. The service does not attest a caller-supplied image.

`trial-worker --id ID` returns the retained declaration; `--job-only` returns its
sealed external job. `trial-worker-input --name NAME` returns a targeted artifact
wrapper and digest. `--raw` returns the exact original bytes without adding a
newline. The CLI checks the input against a separately validated immutable
declaration before printing it. No partial automatic file export or execution is
hidden inside an inspection command.

## Source and identity contract

`mantik-external-trial-job-v1` retains the complete request, trial/hypothesis IDs,
registration hash, the source run's deadline disposition, declaration time, full
sealed external job and its own Go JSON hash. The fixed status is `declared`;
authority is `research_external_job_declaration_only`, qualification credit is
false, and `execution_started` and `host_state_known` are false. These are
historical declaration statements. Manual local execution does not rewrite them
or make the service aware of a host process.

The request pins receiver, logical request ID, source service attempt ID, exact
native-run receipt hash, external image, adapter source and requirements hashes.
Under the receiver lock the service validates the registered plan, completed
native run, receiver/registration/recipe relations, source input/result byte
hashes and native artifact identity. It builds the external job from those exact
retained inputs. Clients cannot supply a recipe, input, result, runtime, resource
override, command or authority flag through this API.

Only the existing fixed Backtesting.py fixture contract is supported. Other
recipes/datasets remain unsupported even when a native run is complete. Missing
sources return not-found; running, failed and incomplete native sources cannot
become external comparison references. A completed native run that finished late
remains comparable; its false `native_deadline_met` is preserved and it gains no
qualification credit. Historical trial evidence remains usable after its news
case closes; no current decision applicability is implied.

An exact request retry returns the original declaration before resampling the
clock or rereading parent artifacts, including at capacity. Reusing an ID with
different source/image/code pins conflicts. New declaration time cannot precede
the source finish or any retained external declaration. A receiver retains at
most 32 declarations, globally across trials. Capacity evicts nothing and is
independent of the existing native-attempt capacity. No list of selected winners
or automatic retry policy is introduced.

## Persistence and recovery

Schema 17 adds `mantik_external_trial_jobs` and
`mantik_external_trial_job_inputs`. The metadata and all three exact input texts
commit in one transaction. Native replay bodies stay in `native.json`; the job
contains only their compact method/metric header. Each input is bounded to 8 MiB
and is verified against its sealed job before targeted readback. Capturing the
recipe text too avoids regenerating different bytes after an upgrade.

The receiver-row lock serializes capacity and exact retry decisions. Parent
native runs and artifacts are immutable once complete; the declaration reads
them under that transaction. No model, provider, Docker call or external process
runs while the lock is held. A failed input insert rolls back metadata and every
other input. Memory-store results are detached copies. Application/database
restart and isolated restore retain the same request, job and artifact hashes.

Schema-16 binaries reject schema 17. Deployment requires a verified backup and
planned restore rollback; this development change does not migrate existing
pilot databases. The existing native assessment histories describe native
attempts and are not amended to pretend that a declared external job executed.
External result/host-receipt admission and a complete combined research-attempt
history remain subsequent work under the broader research lifecycle.

## API and verification

| Method and path | Contract |
| --- | --- |
| `POST /v1/external-trial-jobs` | Bounded 4 KiB command with the seven request fields above |
| `GET /v1/external-trial-jobs/ID` | One immutable declaration |
| `GET /v1/external-trial-jobs/ID/inputs/NAME` | One of `dataset.json`, `recipe.json`, `native.json` |

All paths use bearer authentication and reject query parameters. Invalid or
unsupported commands return 422, wrong receiver or identity/source/clock/capacity
conflicts 409, unknown sources/declarations 404, and persistence failures 503.
There is no HTTP launch, completion, cancellation or result-import route.

Verification covers real native source creation, concurrent declarations,
receiver/source/code-pin conflicts, detached readback, exact retries without clock
sampling, bounded capacity without eviction, running/failed source rejection,
late-completed source provenance, atomic input-write failure, restart retention,
schema-16 upgrade, changed-migration rejection, authenticated HTTP and exact CLI
bundle export. An isolated runtime rehearsal verifies local external execution
from the service-exported bundle, retained declaration identity after that
execution, application restart, backup/restore and old-binary refusal. Synthetic
execution does not establish profitability, source truth or operational authority.
