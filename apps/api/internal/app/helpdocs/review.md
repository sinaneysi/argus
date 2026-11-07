---
title: Review
summary: Every decision Argus needs from a person, in one inbox - task proposals, plan approvals, lessons, ideas, chat requests and problem links.
order: 2
---

Review answers one question: **what waits for my decision?** Argus proposes; a person decides. Nothing reaches
Jira, GitLab, a chat or a host until someone accepts it here. **Where:** [Review](/review), the second item in the
sidebar. The number next to it is how many decisions wait (admins).

## The inbox {#inbox}

- **The queue and the panel.** The left side is the queue: every waiting decision, whatever its kind, 25 a page. Each
  row shows the title, its kind, its severity (task proposals), the environment, the affected host or service, how
  long it has waited and, for task proposals and chat requests, when the decision expires. Click a row (or press
  Enter on it) to open it in the review panel beside the queue. The queue keeps its place, filters, sort and page;
  the address names the selected decision, so refresh, back, forward and a shared link all show the same view.
- **Narrow screens** (below about 1200 pixels) show the queue, or the selected decision full width with **Back to
  queue**, which returns to the same filters, page and row.
- **One toolbar** for All and for every kind: search (title, host, service, ID), severity, environment, source and
  sort, then **Apply**; **Clear filters** resets them. Severity exists only on task proposals; source is the intake
  path of a task proposal (QA, incident, CVE, CI/CD, credential, task chat), so choosing one hides the other kinds.
  Environments use the names this installation shows (for example production for release).
- **The kind chips** (All, Task proposals, Action approvals, Lessons, Ideas, Chat requests, Problem links) narrow the
  queue to one kind and keep the search and filters. Each kind's own full list (bulk actions with their confirm step,
  snoozed and decided proposals, approved or retired lessons, accepted and built ideas, settings) is one link away
  under the header; old addresses such as `/proposals?view=decided` and `/ideas?status=built` open it.
- After an action the page comes back to the same queue and says whether Argus applied it. Nothing is opened for
  you next: pick the next decision yourself.

## How Review orders decisions {#order}

The default order is **urgency**, worked out from stored fields only (no score):

1. **Severity**: critical, high, medium, then decisions without a severity, then low and info. Only task proposals
   carry a severity; action approvals, lessons, ideas, chat requests and problem links have none, and an unknown
   severity is not assumed to be low.
2. **Environment**: production (release) first, then staging (pre-release), develop, any other, none.
3. **Decision deadline**: the soonest first; decisions without one after. A task proposal expires 14 days (the
   setting) after it was proposed; a chat request after 24 hours. This is the deadline of the *decision*: an expired
   proposal is recorded as rejected ("expired"), which does not mean the issue went away. No kind stores an
   operational deadline today; the panel says "None recorded".
4. **Waiting time**: the longest waiting first; then kind and ID, so the order never changes on its own.

The other orders are **Oldest first**, **Newest first** and **Expiring first** (the nearest decision deadline, then
urgency). The line under the toolbar always says which order is on.

## The review panel {#panel}

In this order: the title, kind, severity and environment; the affected hosts or services and the deadlines; the
proposed outcome; the evidence and a link to its source; what happens if you accept; the actions; then the details
and history, folded. When the source sent no real evidence (for example only "From Argus."), the panel says **No
evidence recorded** and links the source to check instead. Argus never fills in evidence or a confidence it does not
have; a problem link shows the confidence its suggester stored.

- **Task proposals: Create Jira issue** (or GitLab issue, or task on a local board) creates tracked work only; it runs
  nothing on a host. **Edit, then create** changes the title, assignee, project, priority or labels first.
  **Snooze 24 h** is offered only when the proposal expires more than 24 hours from now: snoozing never extends the
  expiry, so a proposal that would expire while snoozed shows why snooze is missing.
- **Action approvals: Review execution plan** opens the plan review step: the pinned plan version, environment,
  targets, every step (READ or WRITE), validation and rollback (said to be missing when the plan has none) and the
  limits. Only that step has **Approve plan vN & start**. If the plan changes, the new version needs its own review.
  There is no bulk approval of plans.
## Decision kinds {#kinds}

| Kind | The main action | What it does |
|---|---|---|
| Task proposals | **Create Jira issue** (or GitLab issue) | Creates the issue as proposed; **Edit, then create** to change the title, assignee or labels first ([Proposed tasks](/help/delivery#proposed-tasks)). |
| Action approvals | **Review execution plan**, then **Approve plan vN & start** | Starts the task agent on exactly that plan version. If the plan changes, approve the new one. Production plans only write a proposal. |
| Lessons | **Approve lesson** | Lets later runs use the lesson ([Lessons](/help/agent#lessons)). |
| Ideas | **Generate build prompt** | Argus writes a build prompt; nothing is built or deployed ([Ideas](/help/agent#ideas)). |
| Chat requests | **Create task** | Turns a request forwarded in Matrix into a task ([Matrix requests](/help/delivery#matrix-requests)). |
| Problem links | **Link to P-n** | Links the incident or report to the problem ([Problems](/help/operations#problems)). |

Every action goes through the same checks as before: your role, the form's security token and, when the operator
turned it on, a fresh two-factor code. Root re-checks every request before anything happens.

## Who sees what {#roles}

Admins see every kind and act on it. Viewers see ideas and problem links, read-only, and no count in the sidebar.
