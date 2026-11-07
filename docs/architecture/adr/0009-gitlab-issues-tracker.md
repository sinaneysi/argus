# ADR-0009: GitLab issues as a second task tracker beside Jira

Status: accepted, 2026-10-27 (operator: "both, labelled so I can tell which is which; ask for the issue board's repo
in the Integrations tab").

## Context
The board is two-way with Jira only (board_sync.py, jira_client.py, jira_connect.py, jira_board.py). Some teams keep
their work as GitLab issues next to the code. The operator wants both trackers at once, every task visibly marked with
its tracker, and the GitLab side configured from Settings → Integrations by naming the issue board's project.

## Decision
1. **A tracker is a property of a task**, not of the install: `tasks.tracker` (`jira` | `gitlab`, additive, default
   `jira`; every existing row becomes `jira`), plus `gl_project`, `gl_iid`, `gl_url`. `jira_status` holds the GitLab
   list name for GitLab tasks, so the move/conflict code and the pages need no second status column.
2. **Jira's path stays untouched.** The Jira push skips GitLab tasks; `gitlab_issues.py` (root, inside the same board
   sync run) pushes and pulls them. Requests, outbox, events, comments, notices, the fixer bridge and proposals are
   shared; only the writer differs. A Jira request keeps its old shape (no `tracker` key).
3. **One GitLab connection**: a project access token of the board's project (bot user, scope `api`, Reporter or
   Planner), not per-user tokens. GitLab issues have no per-user write mode to mirror, and a project bot survives
   people leaving (handover goal). Argus names the person in each note instead.
4. **Board mirror by label lists**: list label → column (status words shared with Jira, scoped labels by their last
   part, an admin list map). Open without a list = To do, closed = Done; a move swaps list labels, Done closes,
   leaving Done reopens. GitLab wins conflicts. No list for a column = a clear refusal, never a guess.
5. **The person chooses the tracker** on New task and on Accept (proposed tasks); an admin sets the default.
   Nothing creates an issue by itself (the approval queue rule is unchanged).
6. **Same security model as Jira and Outline**: the token goes app → 0600 request in a 2730 dir → root, stored
   `/etc/argus/gitlab-issues.token` 0600; the app reads only `settings-out/gitlab-issues-status.json`; https,
   host allowlist (default: the host of `GITLAB_URL`), jira_connect's address checks, pinned connection, no redirects,
   a fixed path allowlist. The connect form is a sensitive POST (fresh 2FA).

## Follow-ups
Done 2026-10-27 (branch `feat/gitlab-followups`):
- The task agent works GitLab tasks under the key `gl-<task id>` (Jira keys unchanged): scope = the Argus column the
  agent column's name maps to, assigned to a Argus admin's GitLab account; the issue and its notes are the job's
  untrusted data; results and answers go back as notes through the outbox. It also runs when only GitLab is connected.
- Matrix DM commands take GitLab references (`#45`, `infra#45`, `group/infra#45`); forwarded requests go to the
  default tracker or the one the forwarder answers (`in gitlab` / `in jira`).
- QA intake proposes (never creates) with GitLab alone; a resolved incident comments its GitLab issue.
- A move into a fixer list (or the `argus-fixer` label) made in GitLab starts the fixer under the rules of a Jira move:
  the person who added the label (`resource_label_events`) must be a Argus admin; production, other environments, no
  fix prompt or no fixer host are refused with a note and the issue goes back to To do.

Still not mirrored: board scope by assignee, milestone or iteration lists (label lists and board label scope are).

## Consequences
Two trackers on one board; every card says which. Removing GitLab issues is "Disconnect" (tasks stay, read-only);
rollback of the code leaves additive columns that the old code ignores.
