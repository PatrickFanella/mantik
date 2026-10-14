# Synthetic baseline experiments

The `experiment` command compares two registered fixture methods with two
nonexecuting controls. It runs locally, declares its recipe before execution,
and retains inputs and terminal attempts outside Git. It contacts no service,
provider, model, or allocator. All evidence remains `synthetic_unqualified`.

## Registered methods and controls

| ID | Behavior | Explicit assumptions and safety |
| --- | --- | --- |
| `demo-threshold-v1` | Existing 10-unit threshold entry and protected exit | Entry at or below 100.00; maximum notional 1,000.00; stop 95.00; target 110.00; one-hour holding limit; quotes at most 60 seconds old |
| `demo-half-size-v1` | Same deterministic rule with 5 units | Immutable size and maximum notional 500.00; all other protective conditions match the original fixture |
| `no-action-control-v1` | No actions and zero PnL | An observational component, never an executable strategy or capital instruction |
| `passive-mark-control-v1` | Mark a whole-unit passive holding from the first quote to the last | Uses a notional 1,000.00 cash account; buys the largest whole-unit quantity within that budget, holds residual cash at zero return, and marks at window end. It writes no orders and simulates no protective exits |

Every active method uses `observed_quote_zero_cost_v1`: fills at the observed
quote, including gaps, with zero fees and slippage. The controls use the same
zero-cost observation assumption for comparison. These are deliberately small
fixture models. Four synthetic quotes cannot establish costs, capacity, strategy
value, historical reproduction, or prospective performance.

The service still initializes only `demo-threshold-v1`. A separate memory store
is created for each experiment method; its positions and decisions cannot affect
another method or the service. Operations resolves each exact immutable strategy
configuration and independently enforces its finalized authority. The passive
control is a benchmark calculation; its mark is not an execution outcome.

## Recipe contract

`mantik-experiment-recipe-v1` pins all of the following before evaluation:

- Dataset ID, version, and complete content SHA-256.
- Inclusive window start/end, explicit monotonic cutoffs, and required source keys.
- Active-method and benchmark IDs, full definitions, and hashes covering their
  parameters, library/evaluation contracts, assumptions, costs, safety, and
  literature-support classification.
- A separate initial capital value of 100,000 cents for each comparison.
- The cost-model version and planned metrics: observation count, action count,
  realized PnL in cents, and marked PnL in cents.

The current contract requires both benchmark controls and allows either or both
active methods. It supports only the registered definitions and the fixed capital
and cost policy. Changed parameters, missing benchmark bindings, unsupported
metrics, and incompatible hashes fail declaration validation. Such requests are
not accepted experiments. Exported recipes are copied and sealed; file execution
requires an independently retained expected recipe hash.

Dataset admission then checks integrity and exact identity, full declared input
coverage, window bounds, usable first revisions, freshness, and chronological
observations. Missing, stale, quarantined, corrected, or out-of-window inputs
produce a failed attempt. A correction is not silently substituted into a prior
baseline result. Input-quality admission is stricter than diagnostic replay,
which can expose incomplete frames for inspection.

Each active method runs through the same pinned replay contract. Completed
results retain its frames, finalized decisions, and evaluations. Window completion
does not invent a closing fill: an open position contributes an unrealized mark
at the last quote, while realized PnL stays unchanged. No Sharpe ratio or other
undeclared statistic is calculated.

The default fixture reports marked losses of 60.00 for the original threshold,
30.00 for the half-size method, zero for no action, and 72.00 for the passive
mark. The passive control uses 9 units at 102.00 and residual cash of 82.00.
Different exposure and timing are intentional and visible in the definitions.
The system stores no winning-method, best-result, admission, or promotion pointer.
Every attempt has `qualified: false`.

The source definitions say `synthetic_fixture_only` for literature support.
The [corpus assessment](../research/augr-corpus-assessment.md) supplies future
method candidates, not evidence that these fixtures reproduce any paper.

## Local commands and retention

Build and run the built-in comparison:

```bash
mise exec -- make build
./bin/mantik experiment --attempt demo-1 --out-dir data/experiments
```

To inspect a declaration before running it:

```bash
mkdir -p data
./bin/mantik experiment --export-recipe > data/baseline-recipe.json
recipe_sha=$(python3 -c 'import json; print(json.load(open("data/baseline-recipe.json"))["sha256"])')
./bin/mantik experiment \
  --recipe data/baseline-recipe.json --recipe-sha256 "$recipe_sha" \
  --attempt reviewed-1 --out-dir data/experiments
```

An external `--dataset FILE` requires an explicit pinned recipe. Its dataset
identity must match that recipe. To preserve the pin across runs, retain the
expected hash separately from the recipe file. Export accepts no execution flags.

The output directory contains:

- `recipes/<recipe-sha256>.json`: declaration written before evaluation.
- `inputs/<input-sha256>.json`: the supplied dataset, including original bytes
  and provenance. This hash covers the whole supplied object, including its
  claimed dataset hash, so rejected or tampered inputs remain distinguishable.
- `attempts/<attempt-id>.json`: completed or failed attempt and any completed
  method results retained before a later execution failure.

Files are written through a temporary file, file sync, atomic exclusive hard
link, and directory sync. New files use mode 0600 and new directories 0700.
Identical concurrent retries converge; conflicting content under the same attempt
ID fails without replacing old evidence. Symbolic-link artifact directories or
targets are rejected. Use a local filesystem supporting these operations.

The CLI prints retained failed-attempt JSON and exits nonzero. Invalid JSON,
invalid declarations, missing output arguments, and artifact I/O failures return
errors before claiming a retained terminal attempt. Cancellation after admission
can retain a failed attempt. A process crash can leave a declaration and input
without a terminal attempt; that is incomplete evidence. Retry the same exact
inputs or use a new attempt ID for changed input. Restore and crash recovery
qualification are separate work.

Keep these artifacts in ignored `data/` or another local artifact directory.
Do not commit datasets, results, or generated qualification receipts. There is
no dataset catalogue, hosted experiment scheduler, or automatic paper admission
in this implementation.
