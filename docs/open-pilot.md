# Open Pilot Readiness

Open Pilot can safely work in this repository when issues are specific, scoped, and verifiable.

## Required labels

Use both labels for the portable pickup contract:

- `ready-for-agent`
- `agent:queued`

Draft, bug, feature, and documentation forms add `status:needs-review` without pickup labels. After review, add both pickup labels or use the ready-for-pickup form. Some deployments accept `ready-for-agent` alone or assignment to `open-pilot`; treat either as pickup approval.

## Bootstrap labels

Run this after creating a repository from the template:

```bash
open-pilot labels bootstrap OWNER/REPO
```

## Issue requirements

Every agent-ready issue should include:

- Goal
- Context
- Acceptance Criteria
- Test Command
- Out of Scope
- Queue Checklist

## Test command guidance

Prefer the narrowest deterministic command that proves the requested change.

Examples:

```bash
make verify
test -f docs/reference/example.md
go test ./...
pnpm --dir web run test
uv run pytest
cargo test --all
docker compose config --quiet
```

## PR review and merge

Open Pilot PR review expects the PR verification command to pass. If the deployed Open Pilot instance publishes commit statuses, merge gates should require the Open Pilot-owned status context before merging.
