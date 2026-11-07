---
title: Argus
summary: What Argus itself ran and spent - runs, fix runs, ideas from the three advisers, lessons, knowledge and Outline service docs.
order: 6
---

The Agent group answers "what is the agent doing, and is it getting better?": what it ran, what it cost, what it
learned and the improvements it suggests. Ideas and lessons wait for a decision in [Review](/help/review).

## Agent runs {#agent-runs}

Every run of every task: chat triage, log RCA, incidents, deep RCAs, analyses, reviews and ideas.
**Where:** Agent → [Runs](/runs).

- **Cost per day** for the last 30 days, as a chart.
- **Latest runs:** start time, task, status, duration, cost, turns, permission denials and a one-line summary.
- A run page shows its summary and, for chat triage, every message it handled with the disposition.
- A failure caused by Argus itself (a model error, a blocked tool, a failed notification) is labelled
  Argus-internal. It never pages anyone as a production failure.

## Fix runs {#fix-runs}

Every fix the fixer carried out on develop or pre-release. **Where:** Agent → [Fix runs](/admin/fix)
(admins).

- The list shows the state of the fixer (set up or not, which environments are allowed) and each run.
- A run page shows the problem, the status, the exact prompt the fixer got (with its checksum), the **Final report**,
  the **Backups** it took, the **Verification**, and the **Rollback commands** a person can run if the change must be
  undone.
- A run starts only from [Take action](/help/operations#take-action) or Ready for fixer on a task. One at a time, at
  most 10 a day.

If the page shows "Operator steps", the operator has not granted the fixer access yet. Argus never grants it to
itself.

## Ideas {#ideas}

Three advisers read Argus's own data and each propose up to five evidence-based improvements once a week. You
explore them, accept or reject, and an accepted idea comes with a build prompt for Claude Code.
**Where:** [Review](/review?kind=ideas) → Ideas. The Review count in the menu includes new ideas. The page says when the feature is
off.

| Adviser | Field | Weekly |
|---|---|---|
| Kai, senior DevOps | CI/CD, deploys, reliability, toil, backups, capacity, security hygiene | Monday |
| Quinn, QA specialist | QA intake and triage, release gates, smoke and regression checks | Wednesday |
| Nico, NOC engineer | Alert noise, detection and response, on-call, incident communication | Friday |

Each idea has a problem, evidence with links to Argus pages, the proposal, value, effort (S, M, L), risk,
prerequisites and a success metric.

### How do I… {#ideas-how}

- **Find an idea:** the tabs (Open, New, Under review, Accepted, Built, Rejected, All) show their counts; search
  matches the title, problem, proposal, value and evidence; the Adviser, Area and Effort chips narrow the list (each
  count already honours the other filters); sort by Priority (new first, then under review, accepted, built,
  rejected), Newest, or Smallest effort. 25 ideas a page.
- **Mark under review** (admins): you are exploring it.
- **Accept & write build prompt** (admins): the **Build prompt for Claude Code** appears on the row within a minute. It includes the
  goal, scope, where in the code, the guardrails, tests, the deploy and the rollback. **Download .md** saves it.
- **Reject** (admins): a reason is required and remembered. A rejected idea comes back only if it says what changed
  and has new evidence.
- **Mark built** (admins): with a commit, a merge request (`!123`) or a link.
- **Generate now** (admins): runs one adviser now, at most once a day each.

The 09:00 brief mentions new ideas; nobody is pinged.

### What it will not do {#ideas-not}

- Advisers only read; they cannot run commands or change anything.
- A duplicate of an open, accepted or built idea is dropped.

:::admin
### Settings for admins {#ideas-admin}

Switch: `FEATURE_IDEAS=1` (default 0). Schedule `IDEAS_SCHEDULE` (default Mon, Wed, Fri at 10:30 and 15:30 {{tz}}
checks), cost per run `IDEAS_BUDGET_USD` (0.60), all Ideas spend per day `IDEAS_DAILY_USD` (2.00), ideas per run
`IDEAS_MAX_PER_RUN` (5). The optional prompt polish `IDEAS_REFINE` is off by default. Personas and areas live in
`config/ideas.json`.
:::

## Outline service docs {#outline}

One Outline page per service, built and kept current by Argus, plus a **Services overview** index. No model is
used. **Where:** Settings → [Integrations](/admin/outline) (admins). **Preview by default:** nothing is sent to
Outline until an admin chooses Publish.

Each page has: summary, owner (or "needs owner"), tier, environments (hosts, unit, port, domains, last deploy),
dependencies, deploy, gotchas, open issues and runbook links to Argus pages.

- **Preview:** the page list shows each service and its diff since the previous build.
- **Publish:** creates and updates the pages daily and on **Sync now**. Only pages whose content changed are updated.
- Argus owns only the text between its markers on each page. If a person edited inside them, their text stays, the
  page is flagged, and Argus's version is added below as a "Argus proposed update".
- Scope: five pilot services, or **All services** (about 125).

### What it will not do {#outline-not}

- Never deletes a page.
- Every page goes through the secret scan; a page that still looks like it holds a secret is blocked.

:::admin
### Connecting Outline {#outline-admin}

Paste the Outline address, an API key (Outline: Settings → API → New API key) and the collection name; tick
**Create it** the first time. Root tests the key and stores it; the app never reads it back. Only allowed Outline
hosts are accepted. **Disconnect** deletes the key on the server; revoke it in Outline too.
:::

## Lessons {#lessons}

What Argus learns from its task runs, after a person checked it.
**Where:** [Review](/review?kind=lessons) → Lessons (admins) and Agent → [Knowledge](/knowledge) (everyone).

- After planning or working a task, Argus may propose up to 3 **lessons**: a short, reusable fact about this estate
  (a port, a quirk, a root cause, the order of a fix), with the service, environment and the evidence.
- A lesson that contains anything that looks like a password, token or key is thrown away, never stored. Copies of a
  lesson Argus already knows are dropped; a changed version shows as **update of lesson N**.
- **Nothing is used until an admin approves it.** On Lessons: **Approve**, **Edit and approve**, **Reject**, or for an
  approved lesson **Back to review** and **Retire**. Rejected and retired lessons are kept for the record.
- Later task runs read the approved lessons that match the task (service, environment, kind, words), at most 8, as
  background only. A lesson can never approve or add a step: the approved plan is the only authority.
- A lesson unused for 90 days, or marked "did not help" by two runs, comes back to review as **retire?**.
- **What Argus knows** shows every approved lesson grouped by service; it changes with every review.
- Admins can turn lessons off in Review → Lessons → Settings: nothing is proposed or loaded, and the lessons stay.

## Lessons {#lessons}

What Argus learns from its task runs, after a person checked it.
**Where:** [Review](/review?kind=lessons) → Lessons (admins) and Agent → [Knowledge](/knowledge) (everyone).

- After planning or working a task, Argus may propose up to 3 **lessons**: a short, reusable fact about this estate
  (a port, a quirk, a root cause, the order of a fix), with the service, environment and the evidence.
- A lesson that contains anything that looks like a password, token or key is thrown away, never stored. Copies of a
  lesson Argus already knows are dropped; a changed version shows as **update of lesson N**.
- **Nothing is used until an admin approves it.** On Lessons: **Approve**, **Edit and approve**, **Reject**, or for an
  approved lesson **Back to review** and **Retire**. Rejected and retired lessons are kept for the record.
- Later task runs read the approved lessons that match the task (service, environment, kind, words), at most 8, as
  background only. A lesson can never approve or add a step: the approved plan is the only authority.
- A lesson unused for 90 days, or marked "did not help" by two runs, comes back to review as **retire?**.
- **What Argus knows** shows every approved lesson grouped by service; it changes with every review.
- Admins can turn lessons off in Review → Lessons → Settings: nothing is proposed or loaded, and the lessons stay.

## FAQ {#faq}

:::details What does "Argus-internal" mean on a run?
The run failed because of Argus, not because something in production is broken. It is shown in Argus health and
the digest, never as a production page.
:::

