# ADR-0002: Mantik bootstrap boundaries

Status: accepted for the bootstrap
Date: 2026-10-13

## Context

The Augr rebuild discussion chose autonomous operation, terminal supervision,
structured evaluation before narrative explanation, reusable strategy/test
components, and a mix of deterministic rules and LLM investigation. Mantik is
a new repository rather than a cutover of existing Augr services or data.

## Decision

Use the Go worker/PostgreSQL/Docker template. Pin Go 1.26.9. Keep the persistent
service, CLI, and application contracts in Go. Leave a future research-worker
boundary for Python tools; no worker transport is implemented now.

Separate research, decisions, and operations. Start with one deterministic
fixture that exercises observations, triggers, evaluations, finalized actions,
protective exits, and outcome records. Use PostgreSQL for the persistent
service and memory for a dependency-free demonstration.

Commit fixture effects and audit records transactionally. Validate startup
schema/checksum and restrict bootstrap database names to `mantik` or
`mantik_test_*`. Existing Augr databases are outside scope.

Keep evaluation structured. Future explanations read finalized records. No
LLM, real provider, dynamic strategy execution, or live trading enters this
bootstrap.

## Consequences

The first flow is runnable without provider credentials and can be tested for
retry, restart, concurrency, and rollback behavior. Its small aggregate and
zero-cost simulator are development boundaries, not portfolio architecture or
qualification. Independent protective timers, external action delivery,
reconciliation, and broader instrument precision need later contracts.

Rust remains a possible future implementation choice. No measured requirement
has yet justified migrating the Go foundation.
