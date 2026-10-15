# Compiled method library readback

`GET /v1/library` and `mantik library` expose the running service's compiled
synthetic method definitions. They do not report active strategy admission,
research-attempt history, a portfolio, live results or profitability. Inspection
does not execute a method, run an experiment, read local research artifacts or
activate a strategy.

## Catalogue contract

The authenticated endpoint returns:

- `schema`: `mantik-method-catalog-v1`.
- `contract_version`: `mantik-library-v1`.
- `scope`: `compiled_synthetic_methods`.
- `qualification_credit`: false.
- `methods`: content-pinned definitions, currently four entries.

The catalogue includes the original threshold fixture, its half-size variant,
the no-action control and the passive-mark control. The last two are
nonexecuting comparison components, not broker strategies. Each pin includes an
exact method ID, SHA-256 and full definition. Definitions retain their typed
inputs, instruments, outputs, dependencies, implementation identifier, safety
requirements, mandatory evaluation recipe, configured strategy/safety values,
cost model, assumptions, benchmark contract and literature-support declaration.

These are the same pins used by the built-in experiment recipe. Existing method
serialization, content hashes, recipe ordering and recipe identity are preserved.
In particular, the nested `entry` contract retains its existing exported field
names rather than changing the serialization of already sealed research
artifacts. The new catalogue wrapper uses the JSON names above.

All current methods retain `synthetic_unqualified` evidence, zero fees/slippage
and explicit fixture/control limitations. Configured safety and required checks
are declarations, not proof that a method passed an evaluation or that a
particular finalized decision is currently applicable. A matching SHA-256 is a
content identity, not evidence of quality or permission to execute.

The endpoint validates the compiled registry and returns fresh values from this
service build. It needs no database snapshot, event ledger, provider credentials
or filesystem artifact enumeration. It can remain available when the service's
database snapshot is unavailable; therefore success is not database or worker
readiness. The original service execution remains bound to its selected fixture;
being listed here does not change that selection.

The catalogue schema permits 1..16 methods; this build supplies four. Definitions
must have distinct identities, valid library contracts/dependencies, synthetic
classification and matching hashes. The CLI checks this declaration before
rendering. It rejects an incompatible schema, contract, scope, qualification
claim or definition/hash mismatch instead of substituting client-local methods.
This check does not assert that unfamiliar method IDs have executable
implementations in the client.

The endpoint accepts no query parameters. Unknown parameters return HTTP 400,
missing authentication HTTP 401, and a compiled catalogue validation failure
HTTP 503. Other existing service authorization rules apply.

## CLI

```sh
./bin/mantik library --api-url http://127.0.0.1:40880
./bin/mantik library --format json
./bin/mantik library --id demo-threshold-v1
./bin/mantik library --id passive-mark-control-v1
```

Set `MANTIK_API_TOKEN` as for the other service commands. The table displays
method identity, kind, implementation, evidence class and cost model, quoting
stored text. JSON returns the complete catalogue. `--id` selects one pin from
the service response and always prints structured JSON, including its exact
content hash and declared checks/safety. An unknown ID fails; it does not resolve
a method from the client's local registry. A failed service read also fails
without a cached or client-local fallback.

This supplies the library inspection boundary for issue #22. Interactive library
navigation, a service-owned research-attempt catalogue and portfolio supervision
remain separate units. Account allocation and realistic paper execution are not
implemented by this readback. See [experiment contracts](experiments.md) for
explicit execution and retained-attempt behavior.
