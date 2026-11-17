# Retrospective option research

The owner approved a bounded retrospective interpretation of #101 on November
16, 2026: use archived 2024-onward prices, preserve their actual acquisition
timestamps, and label historical availability as an assumption with no
qualification credit. This is separate from the existing point-in-time
option store and from operational paper acceptance.

`mise exec -- go run ./research/options --help` describes the offline command.
The [research procedure and dated results](../../research/options/README.md)
define its pinned input, source-page/object provenance, fixed close-plus-15-minute
policy, selected-session coverage, cash-book scenarios and complete event trace.
Every report retains original object acquisition/availability clocks alongside
the detached scenario clocks in simulated chain/fill events. Nothing writes
those scenario clocks back to the store.

Six bounded SPY scenarios cover February samples in 2024, 2025 and 2026. The
2025 covered call and 2026 cash-secured put completed simulated cash-book
round trips. Other scenarios contain limit refusals, and the 2024 covered call
retains an open covered liability and its reserve at the horizon. No result
was discarded or relabeled successful. This is a selected historical-price
exercise, not continuous portfolio history or complete historical chain
membership. All outputs are `retrospective_indicative_unqualified`, with no
trial/gate, broker or qualification authority.

See [option recipes](option-library.md) for literature and policy provenance,
[option structures](option-structures.md) for unchanged cash/risk/reserve rules,
and [option data](option-data.md) for the original acquisition-time contract.
The $20m scenario cash and paid share collateral are explicit simulation
inputs; they are unrelated to the owner's $2,000 capped paper book. No real
option execution, assignment, expected return or profitability is established.
