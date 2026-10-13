# Labels

This template is Open Pilot-ready after labels are bootstrapped:

```bash
open-pilot labels bootstrap OWNER/REPO
```

## Issue lifecycle labels

| Label | Meaning |
| --- | --- |
| `ready-for-agent` | Pickup approval; some deployments accept this label alone. |
| `agent:queued` | Final human approval; Open Pilot may pick up the issue. |
| `agent:running` | Open Pilot is implementing the issue. |
| `agent:pr-opened` | Open Pilot opened an implementation PR. |
| `agent:failed` | Open Pilot could not complete the task. |
| `needs-review` | A human should review the opened PR. |
| `needs-human` | Automation is blocked and human input is needed. |

## PR lifecycle labels

| Label | Meaning |
| --- | --- |
| `agent:reviewing` | Open Pilot is reviewing or verifying the PR. |
| `agent:approved` | Open Pilot review passed. |
| `agent:changes-pushed` | Open Pilot pushed review or CI-fix changes. |
| `agent:merge-blocked` | Merge gates failed or unsafe conditions were detected. |
| `agent:merged` | Open Pilot merged the PR. |

## Normal Open Pilot flow

1. Write an issue using a draft, bug, feature, or documentation form.
2. The form adds `status:needs-review` without pickup labels.
3. Review the scope, acceptance criteria, and test command.
4. Add both `ready-for-agent` and `agent:queued`, or submit the reviewed ready-for-pickup form.
5. Open Pilot implements the issue and opens a PR.
6. Open Pilot reviews/verifies the PR.
7. PR receives `agent:approved` or `agent:merge-blocked`.
8. Human or automation merges after configured merge gates pass.

## Queueing rules

Do not add `agent:queued` until:

- the issue is small enough for one focused PR
- acceptance criteria are checkable
- the test command runs non-interactively from the repo root
- secrets and production actions are out of scope or explicitly handled
- labels have been bootstrapped in the repository
