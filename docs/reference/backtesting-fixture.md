# Backtesting.py fixture comparison

`research/backtesting/adapter.py` compares the four built-in synthetic methods
with Backtesting.py 0.6.6. It accepts only the current Go-exported dataset and
recipe; it cannot accept arbitrary strategies, Python code, data or broker
configuration. It uses no service API, database, model or provider.

The adapter's schema is separate from
`mantik-research-worker-job-v1` and `mantik-research-worker-result-v1`. The existing
`worker-launch` and `worker-reconcile` commands continue to accept only the native
Go adapter. The separate `external-worker-*` commands below provide durable local
launch/reconciliation through a versioned external contract. The finite rehearsal
at the end of this document remains a test driver. Issue #17 remains open pending delivery review of the protocol gate. Service
dispatch/import and broader autonomous research integration are separate work.

## Environment and inputs

The dedicated Dockerfile pins Python 3.13.9 and its base-image digest. The
hash-locked `requirements.lock` pins Backtesting.py and all transitive packages;
installation requires matching wheel hashes and permits no source builds.
Execution checks the Python patch version and every installed locked package
version before importing the library. Nothing is installed into the Go service
image. Dependency installation needs network access during the explicit build;
the rehearsal containers run with network `none`.

The adapter requires six flags: `--dataset`, `--recipe`, `--native` and a separate
`--NAME-sha256` for each file's exact bytes. Each input is bounded to 8 MiB.
Malformed UTF-8/JSON, duplicate keys, floating/nonfinite JSON numbers, trailing
values and invalid raw pins are rejected before library execution. The dataset
and recipe must also reproduce their fixed v1 content seals. This adapter
preserves Go's exported JSON field order when reconstructing those seals;
reordered declarations require a new conversion contract rather than an implicit
change to v1 hashing.

The accepted dataset seal is
`b3f9c5ae2e61f45eda833f0ea2af67565c30aa9885df5e747a696c0e41ac446d`;
the recipe seal is
`9da1c82a0bdbdab99dcf61dd3c59cb2b70b75482639b48f953063cb7752a7d8e`.
The native reference must be a completed, unqualified attempt bound to that
recipe and canonical input, with all four ordered method pins and integer
metrics. The adapter compares the supplied reference; it does not independently
certify the native artifact's calculations or attest its origin. The rehearsal
creates that reference with the current compiled Mantik binary and retains its
exact bytes and binary hash.

## Explicit conversion

The primary [Backtesting.py API documentation](https://kernc.github.io/backtesting.py/doc/backtesting/backtesting.html)
describes OHLC data, `trade_on_close` and `finalize_trades`. The pinned
[0.6.6 release](https://pypi.org/project/backtesting/0.6.6/) supplies the library
version and package hashes. Backtesting.py is licensed AGPL-3.0-or-later; keep
its license notices with any distributed image.

The conversion `quote_to_flat_ohlc_with_copied_boundary_bars_v1` uses each of
Mantik's four quotes as a flat OHLC bar in dollars, indexed by quote receipt
time. These are synthetic representations of point quotes, not market bars.
It explicitly adds one copy of the first quote before the window for library
warmup and one copy of the final quote after the window for order settlement.
Only the four original quotes count as observations. The strategy submits no
decision on the settlement copy. No new market price is invented.

Each method has a separate 1,000-dollar account. Commission and spread are zero,
margin is one, `trade_on_close=True`, `exclusive_orders=True`, and
`finalize_trades=False`. Threshold methods use the declared entry threshold,
whole-unit quantity, stop and target; their market exit uses the observed quote
crossing the protective boundary. This fixture exercises the price-trigger path,
not a general implementation of Mantik's deadlines, routing or safety engine.

The passive control opens nine simulated units at 102 dollars and leaves them
open, marking them at 94 dollars with residual cash unchanged. Mantik's native
passive control is mark-only and records zero actions. Its action count is
explicitly excluded from comparison; the library's one action and open trade
remain in the artifact. Other metrics are compared independently for every
method. A mismatch remains a complete comparison with deltas and false match
flags; it never becomes a selected winner or a qualification result.

Money is converted back to integer cents only when it is within one millionth
of a cent of an integer. Non-cent amounts are rejected rather than silently
rounded into matching results. This tolerance handles floating-point arithmetic
in the library, not execution costs or a profitability tolerance.

## Result and failure contract

`mantik-backtesting-fixture-comparison-v1` records the fixed tool identity,
Python/library versions, adapter and requirements hashes, three raw artifact
hashes, recipe/dataset seals, conversion settings, ordered metrics and simulated
trade ledgers. Each comparable metric contains the native value, external value,
delta and match flag. Noncomparable metrics are listed explicitly.

The envelope hash is SHA-256 over compact UTF-8 JSON before adding `sha256`.
Output is bounded to 64 KiB plus a newline; library diagnostics go to stderr.
`status: compared` describes a completed comparison, including mismatches.
`all_comparable_metrics_match` is true only if every comparable metric matches.
Library/environment failures produce `status: failed`, a bounded error code and
any method comparisons already yielded. Invalid input produces no envelope.
Exit codes are 0 for all comparable metrics matching, 1 for a mismatch/library
failure, and 2 for rejected input or an output-bound failure.

Authority is `synthetic_external_fixture_comparison_only` and qualification
credit is always false. Image and isolation verification remain false inside
the result: host evidence must establish the actual process settings. Matching
fixture results do not establish general engine equivalence, statistical
qualification, realistic fills, profitability or execution authority.

## Durable external worker commands

`mantik-external-fixture-worker-job-v1` pins a caller-chosen job ID, the fixed
adapter/tool, Python and library versions, approved immutable image, adapter
source and requirements hashes, full fixed recipe, three exact input artifacts,
and a compact native-reference header with all four method/metric pins. Native
replay bodies remain in the separately retained `native.json`, rather than being
duplicated into the declaration. The job also fixes the resource profile,
`single_comparison_stdout_64k_local_logs_v1` transport, `comparison.json` output,
authority `external_fixture_worker_declaration_only`, false qualification credit
and a hash of the Go JSON declaration with its own hash field empty.

Use the existing export/reference commands or the retained rehearsal's input
files. The input directory must contain `dataset.json`, `recipe.json` and
`native.json`; these are read as bounded regular files. Source and requirements
pins are explicit declarations. Host approval of the image is still necessary;
a reported source hash does not attest the image's computations.

```sh
MANTIK_BT_ADAPTER="$(sha256sum research/backtesting/adapter.py | cut -d ' ' -f1)"
MANTIK_BT_LOCK="$(sha256sum research/backtesting/requirements.lock | cut -d ' ' -f1)"
./bin/mantik external-worker-job --id external-fixture-1 \
  --image-sha256 "$MANTIK_BT_IMAGE" \
  --adapter-sha256 "$MANTIK_BT_ADAPTER" \
  --requirements-sha256 "$MANTIK_BT_LOCK" \
  --input-dir "$MANTIK_BT_INPUTS" > "$MANTIK_BT_JOB"
./bin/mantik external-worker-launch --job "$MANTIK_BT_JOB" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_BT_JOB")" \
  --input-dir "$MANTIK_BT_INPUTS" --out-dir "$MANTIK_BT_ATTEMPT" \
  --approved-image-sha256 "$MANTIK_BT_IMAGE"
./bin/mantik external-worker-reconcile --out-dir "$MANTIK_BT_ATTEMPT" \
  --approved-image-sha256 "$MANTIK_BT_IMAGE"
./bin/mantik external-worker-result --job "$MANTIK_BT_JOB" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_BT_JOB")" \
  --file "$MANTIK_BT_ATTEMPT/stdout.json"
```

`external-worker-result` validates and returns the exact compact Python bytes,
preserving their hash. It does not reformat them as Go JSON. The validator checks
artifact/source pins, environment declarations, conversion settings, ordered
method identities, required integer metrics, trade-ledger shape, the passive
action exclusion, each native/external value, delta and match flag, the overall
match disposition, authority and the original compact-byte hash. It does not
independently certify the library's calculations or a supplied native reference.
Equivalent declarations may share a comparison's content hash; the durable host
receipt binds that artifact to a particular sealed job and physical acquisition.

The launcher reuses the [native launcher's ownership and recovery controls](research-workers.md#explicit-local-host-launcher).
It validates all three inputs against the sealed job before creating resources,
retains them and the job in a new private directory, then syncs
`mantik-external-worker-acquisition-v1` before Docker creation. A kernel lock,
actual ownership labels, pinned image and pre-start profile receipt protect the
physical attempt. No automatic recreation or re-execution occurs.

The fixed Python entrypoint is `/usr/local/bin/python -I /adapter/adapter.py`.
Only exact file/hash arguments are supplied. It uses the same non-root,
read-only, network-disabled, capability, memory/CPU/PID and no-restart settings
as native workers. The complete environment must equal the inspected pinned
Python image's public PATH/version/build-key/build-hash values plus the fixed
bytecode/unbuffered and single-thread numerical-library settings. Additional
variables, commands, namespaces, mounts, devices or ports fail verification.
`-I` ignores Python environment configuration; numerical-library thread limits
remain applicable. No caller credentials or environment values are forwarded.

The external resource declaration fixes 30 host seconds, 256 MiB, one CPU,
64 PIDs, 8 MiB per input and a 64 KiB comparison plus newline. Host enforcement
uses actual process/container state and the acquisition deadline, independently
of the adapter. No cooperative deadline claim is added to the Python artifact.
The approved fixed adapter emits one compact comparison on stdout and redirects
library diagnostics to stderr. Capture uses the bounded local logging profile:
at most 16 MiB retained stdout and 64 KiB stderr, with result validation enforcing
the smaller comparison limit. This permits oversized/invalid stdout to remain
inspectable as incomplete evidence. As with native capture, `output_complete`
means the bounded log read finished, not that the daemon never rotated earlier
diagnostics. This transport is not approved for arbitrary noisy external tools.

`mantik-external-worker-host-receipt-v1` binds the job/acquisition/image/container,
actual finish and exit, host deadline, profile and capture disposition, stream
hashes, valid comparison identity when available, collection time and its own
hash. Authority is `synthetic_external_host_launch_receipt_only`; qualification
credit remains false. A valid matching comparison with exit 0 is `completed`.
A valid complete comparison with differing metrics and exit 1 is `mismatch`.
A valid library failure with exit 1 is `failed`. Missing/invalid output, changed
source pins or inconsistent exit status remain `incomplete`, with raw evidence
retained. Launch exits nonzero for every status except `completed`.

Reconciliation validates retained job/input identities and actual ownership,
then returns a terminal receipt unchanged or recovers a finished result.
An owned running worker before deadline returns a running error. `--cancel`
explicitly stops that owned running attempt; an overdue running attempt is
stopped after inspection. A worker that finished on time remains recoverable
after the deadline. Missing handles, changed ownership/profile or an externally
restarted terminal handle remain unresolved/conflicted. Cross-using native and
external commands is rejected. A deliberate retry needs a new directory and
preserves the original comparison identity; there is no automatic retry policy.

The shared launcher refactor preserves native v1 JSON schemas, PATH-only profile,
entrypoint, result rules and receipts. No database/schema, service scheduler,
trial admission, provider or operational account is changed.

## Reproduce the retained rehearsal

Use a unique local image tag and a new private output directory. Read the actual
Docker image ID; build-log config/layer hashes are not interchangeable with it.

```sh
mise exec -- make verify
docker build -t mantik-backtesting-review-1 research/backtesting
MANTIK_BT_IMAGE="$(docker image inspect --format '{{.Id}}' mantik-backtesting-review-1)"
python3 research/backtesting/rehearse.py \
  --mantik ./bin/mantik --image-sha256 "$MANTIK_BT_IMAGE" \
  --out-dir "$HOME/.local/state/mantik/backtesting-review-1"
```

The driver exports the built-in dataset/recipe, runs the native reference in
independent memory, retains the input artifacts, and runs eleven owned containers.
It checks actual pre-start image/profile settings and final stopped state.
Containers use UID 65532, a read-only root/input mount, network `none`, dropped
capabilities, no-new-privileges, 256 MiB memory/swap, one CPU and 64 PIDs. The
Docker client uses a private empty configuration and the local Unix socket.
No host credentials or service socket is mounted. Image-defined Python and
single-thread numerical-library environment settings are separate from the
native launcher's PATH-only profile.

The driver waits at most 30 seconds per execution; on failure it inspects labels
and image before stopping only its own remaining containers. It retains command
outputs, pre-start/final inspection, stdout/stderr, failed attempts and final
handle records. It does not delete containers or reuse output directories.
A passing receipt is written only after all owned handles are stopped with
PID 0. Library stdout/stderr are retained and size-checked by the driver; this
finite test capture is not a general durable external-worker transport.

Checks include byte-identical repeated comparisons, all four metrics/ledgers,
a deliberate one-cent native mismatch, wrong raw pins, changed fixture content,
duplicate JSON, claimed qualification, missing methods, null metrics, failed
native reference and reordered method identity. `make verify` runs dependency-free
transport/comparison unit tests; the explicit container rehearsal verifies the
real pinned library. PostgreSQL/schema and deployed services are unchanged.
