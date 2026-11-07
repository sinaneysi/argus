---
title: Work and CI/CD
summary: The task board and Jira, task proposals, Matrix requests, the task agent, and the CI/CD guardian with Check a repo.
order: 3
---

The **Work** section is where work becomes tasks; **Delivery → CI/CD** is where Argus watches the
pipelines that ship it. Task proposals wait for a decision in [Review](/help/review).

## Task board {#task-board}

The team's tasks, kept in step with Jira. Jira is the source of truth: when Jira and Argus disagree, Jira wins.
**Where:** Work → [Tasks](/board).

### What you see {#board-view}

- **With a Jira board set** (the usual case), the page mirrors that Jira board: its columns and their order, the
  statuses in each column, the column limits, the board filter, Jira's rank order and its quick filters as chips.
  **Only my issues** shows your own (the default for the connection owner). On a scrum board you choose
  **Active sprint** or **Backlog**.
- **Without Jira**, Argus keeps a local board with the columns To do, In progress, Review and Done (Ready for fixer
  shows only while a task is still in it).
- A column shows 15 cards (Done: 10) and ends with "Show all".
- Card tags tell you more: "fix prompt" (the task carries Argus's evidence and prompt), "from Matrix: <name>",
  "Ready for fixer".
- Not mirrored, because Jira's public API does not offer them: swimlanes, the card layout, and Jira's "hide completed
  issues older than". Argus hides done tasks older than 14 days in the last column.
- **Jira and GitLab issues together.** Every card and task page names its tracker in words: **Jira** with the key
  (`{{jira}}-123`), **GitLab** with the issue number (`#45`), or **Local** for a task that waits for Jira. With GitLab issues
  connected, tabs above the board switch between **Jira** (the Jira board), **GitLab issues** and **All tasks**, and
  the **Tracker** filter does the same on the task view. See [GitLab issues](#gitlab-issues).

### How do I… {#board-how}

- **Connect my Jira:** Account → **Connect Jira**. Paste the Jira address and a personal access token, choose
  read-only or read and write, then choose your projects. Argus never asks for your password.
- **Create a task:** **New task** on the board, or **Create task** on an incident, an RCA fix option, a VM
  analysis or a storage analysis (title, environment, host, evidence and fix prompt are filled in; one task per
  source). With GitLab issues connected, **Create in** chooses Jira or GitLab issues.
- **Move a card:** **Move** on the card or the task page. Argus runs the Jira transition into that column with
  your token. If there are several transitions it asks which; if there is none it shows Jira's reason.
- **Comment, assign to me:** on the task page. Comments go to Jira.
- **Refresh now:** asks the sync to run at once. It also runs every 2 minutes and fully every night.
- **Have the fixer run a task** (admins, develop and pre-release only): move it to **In Progress**; Argus plans it
  and you press **Spin up agent** on the task page (see [Task agent](#task-agent)). The old **Ready for fixer** column,
  status and `argus-fixer` label no longer start anything unless an admin sets the fixer trigger back to "column".

### What it will not do {#board-not}

- A read-only Jira connection cannot move or comment; the page says why instead of showing the buttons.
- A production task is never sent to the fixer. A fixer request for production is refused, with a comment, and the
  task is moved back.
- Agents only propose. A task Argus makes waits in the first column until a person moves it.

:::admin
### Settings for admins {#board-admin}

Settings → **Jira sync**: the shared connection (address, token, read-only or read and write, project, optional
board id or pasted board URL, component for new issues, fixer approvers, optional internal CA), allowed Jira hosts,
**Sync now** and **Full reconcile**, the sync status and failed requests. Jira older than 8.14 (no personal access
tokens) is refused. Done-column window: `BOARD_DONE_DAYS` (default 14).
:::

## GitLab issues {#gitlab-issues}

A second tracker beside Jira. Both work at the same time: Jira tasks keep everything above, GitLab tasks live as issues
on one GitLab issue board: a project's board, or a **group board** with the issues of every project in the group.
**Where:** Work → [Tasks](/board?tracker=gitlab).

- **Columns:** each list of the board is a column, by its label name (Doing → In progress, Review → Review, a scoped
  label such as `Status::QA` by its last part). An open issue in no list is **To do**, a closed issue is **Done**.
- **Moving a card** swaps the list label in GitLab; Done closes the issue, moving out of Done reopens it.
- **Comments** go to the issue as notes, starting with `[Argus · your name]`; developers' notes come back as
  comments. **Assign to me** assigns the GitLab user of your name (an admin can map names).
- **Both ways, every 2 minutes.** When GitLab and Argus disagree, GitLab wins.
- **The fixer list** in GitLab is an ordinary list now: put the issue in **In Progress** and spin up the agent
  instead (the old trigger returns only with the fixer trigger set to "column").
- **Argus's task agent** works GitLab tasks too: put the issue in **In Progress** and assign it to yourself. In
  Matrix, name a GitLab issue as `#45` or `infra#45` (`start #45`, `status infra#45`); answer a forwarded request with
  `in gitlab` or `in jira` to choose where it goes.
- **Group board:** cards show the project and number, for example **GitLab infra#45**. When you create a GitLab task,
  **GitLab project** chooses where the issue goes; it starts on the default project.
- Argus writes with one access token (a bot of the project or group), so the issue history shows that bot; Argus
  names the person in the note.

:::admin
Settings → [Integrations](/admin/outline#gitlab) → **GitLab issues**: the issue board (project or group), the board,
the token, for a group board the default project, the default tracker for new tasks, a list map and a user map. See
[Integrations](/help/settings#integrations).
:::

## Proposed tasks {#proposed-tasks}

The approval queue. **Nothing goes to Jira without approval**: every automatic path only proposes a task here, and an
admin decides. **Where:** [Review](/review?kind=proposals) → Task proposals (admins; the Review count in the menu includes them).

Automatic paths that propose: QA and monitoring intake, page-level production incidents, security alerts (CVE) and
CI/CD findings.

### How do I… {#proposals-how}

- **Find one:** search by title, host, source ID, label or the "why"; narrow by severity, source and environment; sort
  by priority, newest, expires first or recently updated. 25 a page.
- **Accept & create Jira issue:** check or edit the title, assignee, project, priority and labels on the row. It is
  created with your read-and-write Jira connection, else the shared one (or in GitLab issues, when that is the default).
- **Reject:** give an optional reason. The same source is not proposed again for 7 days, unless its severity rises.
- **Snooze 24 h:** it comes back tomorrow. Snoozing does not extend the 14-day expiry, so a proposal that expires
  within 24 hours offers no snooze.
- **Several at once:** tick the rows (or "Select all on this page"); a bar appears at the foot of the list. Choose the
  action, check the confirm page (how many, what happens, which are no longer waiting), then confirm. The banner
  reports the result, including any that were skipped.
- **See history:** the Snoozed and Decided tabs. Decided (the last 14 days) has its own search and Outcome (accepted,
  rejected, expired), Severity, Source and Environment chips, newest decision first, 25 a page.

A proposal nobody decides expires after 14 days (recorded as rejected, "expired"). Today, the Review count and the 09:00
brief say how many tasks wait for your OK; nobody is pinged per proposal.

:::admin
### Settings for admins {#proposals-admin}

**Approval settings** on the same page: per path, "Create in Jira without approval" (off by default; leave it off
unless the team agreed), how many days a rejected source stays quiet, and after how many days an undecided proposal
expires. The QA intake threshold and its daily cap are on Settings → Jira sync → Task agent and QA intake.
:::

## Matrix requests {#matrix-requests}

Turn a chat message into a Jira task without leaving Matrix. You forward the message to Argus's bot in a direct
message; Argus writes an English title and summary, shows you a preview, and creates the issue only when you say
yes. **Who:** Argus admins whose Matrix ID is set (Account → Matrix ID, or Settings → Users).

### How do I… {#matrix-how}

1. Start a direct chat with Argus's bot account and forward the message, or paste its "Copy link".
2. Argus finds who asked: from the link, from a first or last line `from @user` / `from <name>`{{chat_from_alt}},
   or it asks "Who requested this?" and takes your next message (within 30 minutes).
3. You get a **preview**: title, a short summary, requester, project, type, labels, the proposed assignee and up to
   8 people you can assign, numbered.
4. Reply to the preview:
   - `yes` (or `y`{{chat_yes_alt}}, ✅, 👍) creates it; `no` (or `n`{{chat_no_alt}}, ❌, 👎) discards it.
   - `assign 3`, `assign @username`, `assign <part of a name>` or `unassigned` changes the assignee.
   - `yes 3` or `yes @username` creates it with that assignee.
   - `title: <new title>` changes the title.
5. Argus replies with the new key and links to Argus and Jira.

Your pending previews also appear at the top of the board as **Waiting for your OK (Matrix)**, with an assignee
choice, Create and Discard.

### Commands in the DM {#matrix-commands}

| Command | What it does |
|---|---|
| `start {{jira}}-123` | Moves the issue to the agent column; the task agent plans it and asks its questions in the DM. |
| `status {{jira}}-123` | Shows where the task agent is. |
| `cancel {{jira}}-123` | Stops the agent with a comment; the issue stays. |
| `approve {{jira}}-123 plan v2` | Starts exactly that plan version (an old version is refused). |

### What it will not do {#matrix-not}

- Only the forwarder can confirm. A preview expires after 24 hours ("Preview expired — forward again").
- The bot joins only DMs from allowed admins. It ignores other invites and never talks in other rooms.
- Secrets in the message are removed on arrival. At most 30 issues a day.
- If Jira refuses the assignee, the issue is created unassigned and the reply quotes Jira's error.

## Task agent {#task-agent}

Argus works Jira and GitLab tasks assigned to an admin. **In Progress is the agent column:** a card that stays
there for 2 minutes gets a plan automatically, and the agent starts only when an admin presses **Spin up agent**.
**Where:** the plan on the card, and the **Argus works this task** panel at the top of a task page.

- **Scope:** issues in the agent column of the mirrored board (default "In Progress"), assigned to an Argus admin
  with a read-and-write Jira connection.
- **Intake (read-only, a few minutes):** environment and kind of work, a plan with read-only and WRITE steps and exact
  targets, up to 5 blocking questions, what it needs from you, an estimate and its confidence. It plans again when the
  description, your answers or your instructions change. Each plan has a version (v1, v2…).
- **Answer and instruct:** with the task chat on, in **Chat with Argus** on the task page (below); with it off,
  **Save answers & re-plan** (instructions up to 8,000 characters).
- **Chat with Argus:** one thread per task. Everyone who can open the task reads it; operators and admins post (up to
  4,000 characters, one message per 10 seconds). Each message gets one short, read-only reply; messages sent while
  Argus is replying, or while the task's own plan or run is in progress, are answered together afterwards. The page
  refreshes itself only while a reply is pending. Answers to Argus's questions and changes of plan in the chat make
  Argus plan again (a new plan version). A message never approves a change: only **Spin up agent** does. Never paste
  a credential: it is removed from the message. While Claude usage is off the message is stored and the thread says
  **Claude paused**. Nothing from the chat goes to Matrix or Jira.
- **The card** shows the environment, the kind of work, the plan summary, the WRITE steps with their targets, open
  questions, the estimate and what the task has cost so far. While Claude usage is off it says **Claude paused** and
  nothing is planned.
- **Spin up agent** (admins only): approves exactly the plan version on screen; if the plan changed, review the new
  version first. The button says what will happen: **writes on develop (targets)** hands the WRITE steps to the fixer
  with you as the approver; on production it is **Write proposal for the operator**: nothing is executed, the task is
  blocked for the operator. A plan with WRITE steps and no environment is refused: tell Argus the environment in the
  instructions. A read-only plan reports, and may open a merge request in Argus's own repository.
- **Stop:** moving the issue out of the column, a new assignee, or **Stop** ends the job.
- Results come back as a `[Argus]` comment. Only a person moves the issue ("Move to Done when a run succeeds" is
  off by default).

Limits: one agent job and one chat reply at a time; a full run costs at most $5 and 60 minutes, three a day; a chat
reply at most $0.15, 30 a task and 100 across Argus a day; $8 per task in total (planning, chat and runs).

:::admin
### Credentials for a task {#task-secrets}

If a step needs an API token, Argus asks for it with a card under **Credentials** (name, purpose, hosts, expiry); an
admin can also give one without being asked. Use **Provide secret**: name (like `API_KEY`), the value, the hosts it
may be used against and the expiry (4 h by default, 24 h at most). Never paste a credential into the chat or Matrix.
Only admins provide credentials, and never for production (release) tasks. Argus's agent never sees the value: its
run uses it for read-only requests to those hosts only, and the value is removed from what the command prints. It is
wiped when the task ends, is stopped or leaves the column, at expiry, when Claude usage is off, and on a restart; the
page then says "Revoke or rotate this credential now". Prefer short-lived, narrowly scoped tokens.

### Credentials for a task {#task-secrets}

If a step needs an API token, Argus asks for it with a card under **Credentials** (name, purpose, hosts, expiry); an
admin can also give one without being asked. Use **Provide secret**: name (like `API_KEY`), the value, the hosts it
may be used against and the expiry (4 h by default, 24 h at most). Never paste a credential into the chat or Matrix.
Only admins provide credentials, and never for production (release) tasks. Argus's agent never sees the value: its
run uses it for read-only requests to those hosts only, and the value is removed from what the command prints. It is
wiped when the task ends, is stopped or leaves the column, at expiry, when Claude usage is off, and on a restart; the
page then says "Revoke or rotate this credential now". Prefer short-lived, narrowly scoped tokens.

### Settings for admins {#task-agent-admin}

Settings → Jira sync → **Task agent and QA intake**: which admins the agent works for, the QA intake severity
(production always counts), new QA proposals per day (0-50), "Move to Done when a run succeeds", **Task chat**
(off by default; off brings back the answers and instructions form) and the **fixer
trigger** (off, or column for the old Ready-for-fixer behaviour). **Retire the fixer column** lists the tasks still in
Ready for fixer; **Move these N to In Progress** moves them all with your token (running fix runs finish).
:::

## CI/CD {#ci-cd}

The CI/CD guardian watches every service that deploys through the shared pipeline to develop, pre-release and
release. It reads GitLab with a read-only account. **Where:** Delivery → [CI/CD](/cicd).

### What you see {#cicd-view}

- **Green by environment:** for every app (or package, with the Packages toggle), the newest pipeline on each
  environment branch in the last 30 days: success, failed, waiting, canceled or skipped, no pipeline, no branch. Each
  count filters the matrix. Pipelines skipped with `[skip ci]` never count over one that ran. The **Readiness**
  column shows the result of Check a repo.
- **Needs attention:** open failures, stuck pipelines and release checks (a release deploy waiting at manual, no
  release deploy in 30 days, release HEAD not deployed).
- [Failures](/cicd/failures): every open failure with its class: code, package, lockfile, variable, registry,
  runner, ssh, deploy, health, lock, flaky or unknown. Known patterns are matched by rules; the rest get one short
  model call (capped per day). Tabs Open and Resolved; search by title, project, job, author, MR, commit or branch;
  narrow by Environment and Class; sort by priority (release first), most recent or most frequent; 25 a page.
  **Create task…** opens the board's form, filled in.
- [Stuck pipelines](/cicd/stuck): pipelines waiting more than 10 minutes for the deploy lock or a runner, with the
  lock holder and the rule for cancelling.
- [Branches and variables](/cicd/audit): missing or stale environment branches, commits that skipped the lower
  environment, unprotected branches, the include ref, and the variable audit.
- A project page lists its pipelines per environment, its deploys and its open findings.

After a deploy, Argus checks the hosts at +5 and +15 minutes (units, ports, edge 5xx, alerts, the deployed commit).
A regression opens "deploy of … followed by …" with a rollback proposal. Recent deploys are also added to every
incident investigation that touches the same hosts.

### What it will not do {#cicd-not}

- **No writes.** Every fix is a proposal with the exact API call; the button shows "Proposed — needs write token"
  and is disabled.
- Never plays, retries or runs a production deploy.
- CI variable checks need a separate read token for variable metadata; without it they show "unknown".

### How Argus itself is deployed {#argus-deploys}

Argus's own code follows the same rule as the services: **merge request, green checks, merge, deploy**. Every merge
request runs the Go tests, the Python tests (with the demo leak check) and a secret scan of the added lines. When an MR
is merged into main, the pipeline deploys that commit on the Argus host and posts "Argus deployed … (merged by …)"
in the notify room. A failed deploy puts the previous version back by itself and says so. Admins pause deploys with the
CI/CD variable `ARGUS_DEPLOY=off`. Details for operators: ADR-0010.

## Check a repo {#check-a-repo}

Paste a GitLab project and Argus says whether it can run on the shared pipeline.
**Where:** Delivery → [Check a repo](/cicd/repo). Anyone signed in can use it.

1. Paste the project link, a `group/project` path or a project id, and press **Check this repo**.
2. Wait about half a minute; the page reloads itself.
3. Read the verdict: **Ready**, **Ready with warnings** or **Not ready**. Blockers come first. Each check shows its
   evidence (file and line), why it matters and the exact fix.
4. Use **How to fix it** and the prompt: a write-access agent makes the repo changes on a branch
   `fix/ci-readiness` and opens a merge request. GitLab settings stay with the operator.

A report is reused for the same commit and rules for 7 days; **Re-check** forces a new one. Limits: one check per
project per minute, 50 a day. Only links to the company GitLab are accepted, and never one with a user name or token
in it.

:::admin
### Settings for admins {#cicd-admin}

- The rules are versioned data: `config/cicd-requirements.json` (`CICD_REQUIREMENTS` points at another copy).
- Triage model: `CICD_LLM=0` turns it off; `CICD_LLM_MAX_DAY` (10) caps it. Stuck threshold: `CICD_STUCK_MINUTES`
  (10). All Argus processes share an hourly GitLab cap (`CICD_MAX_PER_HOUR`).
- Writes stay off until the operator provides a write token **and** sets `CICD_WRITE=1`; even then production deploys
  and variables stay with the operator.
:::

## FAQ {#faq}

:::details I cannot move a card.
Your own Jira connection is missing or read-only. Connect Jira in Account and choose read and write.
:::

:::details Why did Argus not create the Jira issue for an incident?
Argus only proposes. An admin accepts it in Review inbox → Task proposals.
:::

:::details The board shows fewer issues than Jira.
The mirror follows the Jira board's own filter and sub-query, and the projects your connection can see. Done issues
older than 14 days are hidden in the last column.
:::

:::details Why does develop show many "canceled or skipped" apps?
A `[skip ci]` commit makes GitLab skip the pipeline. Argus ignores skipped pipelines when a real one exists; an app
whose only recent pipelines were skipped shows as skipped.
:::
