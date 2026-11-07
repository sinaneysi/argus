# ADR-0010: GitLab group issue boards

Status: accepted, 2026-10-27 (operator: "the group board"; their team works on the board of the group
`technogroup/devops`, so connecting it as a project failed).

## Context
ADR-0009 mirrors the issue board of one project with that project's access token. A GitLab group board
(`…/groups/<group>/-/boards/<id>`) shows the issues of every project in the group, subgroups included. A project
access token cannot see the group, and every GitLab issue still belongs to exactly one project.

## Decision
1. **A connection has a mode** (`gitlab-issues.json` `mode`: `project` | `group`). Project mode is unchanged.
2. **What the admin pastes decides the mode:** a `/groups/…` URL or path is a group; `group/project` is tried as a
   project first, then as a group; a single word is a group. A group URL with a project token answers "This is a group
   board. Use a group access token".
3. **Token:** a group access token (bot of the group, role Reporter, scope `api`). Root tests `/user`, `/groups/:id`,
   the group's boards and its projects with issues on (`/groups/:id/projects?with_issues_enabled=true&include_subgroups=true`,
   at most 500), and the token's role in the default project.
4. **Lists and issues** come from `/groups/:id/boards/:bid` and `/groups/:id/issues`; every change (labels, close,
   reopen, notes, assignee) goes to the issue's own project (`/projects/:pid/issues/:iid`). Each task keeps its project
   (`tasks.gl_project`) and a shown reference (`tasks.gl_ref`, additive: "infra#45", the project path below the group).
5. **A new issue needs a project:** the connection's **default project** (Settings card, or `default_project` on
   connect; the only project is taken automatically) and a per-task **GitLab project** select next to Create in (New task,
   Create task, Accept). The app offers only the published projects; root checks again that the project belongs to the
   group. No project → the create fails with a reason on the task.
6. **SSRF guard:** `gitlab_client.PATH_RE` gains only `groups/<id>`, `groups/<path>`, `groups/<id>/boards[/<bid>]`,
   `groups/<id>/issues` and `groups/<id>/projects`; nothing else under `/groups`.

## Consequences
- Switching between a project and a group connection keeps every task; issues no longer on the connected board are
  hidden by the nightly reconcile, as before.
- The project list is refreshed when the default project is chosen; a project added to the group later appears after
  the next "Use this project" (or reconnect). Issues of new projects are pulled anyway, since the pull is group-wide.
- The ADR-0009 follow-ups (task agent, Matrix, QA intake, fixer moves made in GitLab) work on group boards too; a
  Matrix reference names the project below the group (`infra#45`), `#45` is the default project's.
