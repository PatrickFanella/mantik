# Backtesting.py fixture comparison

`research/backtesting/adapter.py` compares the four built-in synthetic methods
with Backtesting.py 0.6.6. It accepts only the current Go-exported dataset and
recipe; it cannot accept arbitrary strategies, Python code, data or broker
configuration. It uses no service API, database, model or provider.

This unit establishes the external library's fixture semantics before adding
durable launcher integration. Its schema is separate from
`mantik-research-worker-job-v1` and `mantik-research-worker-result-v1`. The existing
`worker-launch` and `worker-reconcile` commands continue to accept only the native
Go adapter. The finite rehearsal below is a test driver; it is not a scheduler or
a crash-recoverable dispatcher. Issue #17 remains open for a versioned external
worker/transport contract and its lifecycle integration.

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
