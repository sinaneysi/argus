---
title: Incidents and checks
summary: Checks, incidents, deep RCA, the fixer, problems, conversations, alerts, alert hygiene and RCA reports.
order: 2
---

The daily work: what is broken, why, and what to do about it.

## Checks {#checks}

Is the daily hygiene green? **Where:** Infrastructure → [Checks](/operations).

- The line under the title: how many scheduled checks are OK, which need a look, and how many backup sets fail.
- **Needs attention**: failing backup sets (not marked OK) and certificates that expired or expire soon, worst first.
- **Scheduled checks**: one strip per check showing the last 48 hours, one tick per run (OK, WARN, FAIL), and under
  it the agent cost of the last 30 days with a link to Runs.
- **Credentials**, **Sales-event readiness** (only in the weeks around the event) and **Argus health** (Argus's own
  timers, the alert watcher, the chat proxy, disk, backups). Healthy parts are folded; problems stay open.

**Scan all** at the top right re-runs the backup, certificate, log and chat checks. The other Infrastructure pages
are [Inventory](/inventory), [Capacity](/capacity), [Certificates](/certs), [Backups](/backups) and
[Alert hygiene](/hygiene); Inventory, the first, starts with **Needs you** for the whole group.

## Incidents {#incidents}

An incident is **one record per problem**. Every alert job, chat report, log RCA, CI/CD failure and security finding
about the same host and symptom is attached to it, so a repeat does not create a new record.
**Where:** Incidents → [Incidents](/incidents).

### How do I… {#incidents-how}

- **See what is open:** the list opens on active incidents (open and acknowledged). The **Status** chips show
  open, acknowledged, resolved or all; **Severity**, **Environment** and **Source** ({{qa_room}}, Kuma, Alertmanager,
  CI/CD, Matrix request, Security or Argus auto) narrow it further, each chip with its count. Search by number
  (`#12`), title, host or domain, key or owner. Sort by priority, severity, latest finding or newest; 25 per page.
- **Understand one:** open it. The status line and the actions (Acknowledge, Resolve, Create task) come first,
  then **On this page** jumps to each part: the Summary card, the source, RCA reports, the fix cards, the timeline of
  everything attached (the newest 12 events; older ones fold under **Earlier events**), the latest RCA and the record
  (status, severity, environment, first and last seen). An RCA report has the same jump row on narrow screens, where
  its side outline moves away.
- **Say I am on it:** press **Acknowledge**. Anyone can. The owner column shows who.
- **Close it:** admins press **Resolve** and may add a cause or fix note. A resolved incident can be re-opened by an
  admin.
- **Turn it into a task:** **Create task** fills a board task with the title, environment, host, evidence and fix
  prompt.

### Severity and paging {#incidents-severity}

- Severity is critical, high, medium or low. It comes from the alert and the evidence, not from the model's wording.
- Only production incidents on **customer-facing** names page the team. Internal targets and Argus's own failures
  go to the digest.
- A **hub service** (a tier-1 service many others call, such as payment or user management) raises the severity
  one step, never above critical. The incident then says "hub service: N callers".

### When incidents close by themselves {#incidents-auto}

- An incident resolves itself when no matching alert has fired for 6 hours (24 hours for chat-only incidents).
- The same problem coming back within 7 days re-opens the old incident.
- CVE and CI/CD incidents resolve only when the next scan or pipeline shows the problem is gone, never on a timer.

### What it will not do {#incidents-not}

- It never acts on the incident by itself. Acknowledge, resolve and re-open are requests a person sends.
- A {{qa_room}} report that lacks detail does not open an incident until Argus's own evidence confirms it.

:::admin
### Settings for admins {#incidents-admin}

- Settings → General → **Hub services and sales-event readiness**: "Hub services raise incident severity" (on by
  default).
- Auto-resolve time: `INCIDENT_AUTORESOLVE_HOURS` (default 6) in the agent environment.
- The alert watcher only starts an investigation after an alert stays firing: critical 120 s, error 600 s, Kuma
  down 300 s (`WATCH_CRITICAL_SECONDS`, `WATCH_ERROR_SECONDS`, `WATCH_KUMA_SECONDS`). Warnings and flapping
  alerts are counted, never investigated. At most 6 jobs an hour and 40 a day (`WATCH_MAX_JOBS_HOUR`,
  `WATCH_MAX_JOBS_DAY`); the same alert is investigated once per 6 hours.
:::

## Deep RCA {#deep-rca}

A deep RCA is a longer, read-only investigation of one incident: the edge, the backends, the code, the last 7
days, recent deploys and resources. Every claim is marked proven, probable or unproven.
**Where:** an incident page → **RCA reports** → **Investigate deeply** (admins).

- It takes up to about 30 minutes and costs up to $4.
- The report appears on the incident and under [RCA reports](/rca) as soon as the run ends.
- Limits: one per incident per hour, six a day, one at a time. The button explains when it is not available.

### Automatic deep RCA {#deep-rca-auto}

Argus can start a deep RCA by itself for a qualifying incident: a new page-level production incident on a
customer-facing name, an incident still open after N hours, an incident seen N times in 24 hours, or a {{qa_room}}
report about production. It uses the same limits as the button and keeps one daily slot free for people. An incident
gets at most one automatic run a day, again only if its level goes up. CI/CD incidents never start one.

:::admin
### Settings for admins {#deep-rca-admin}

Settings → General → **Automatic deep RCA**: on/off, each of the four triggers, open hours (default 2), repeat
count (default 3), runs per day (default 6), cost per day (default $20), and quiet hours for the page trigger
(default 23:00 to 08:00 {{tz}}).
:::

## Take action (the fixer) {#take-action}

On **develop and pre-release only**, an admin can let Argus carry out a fix after reviewing it.
**Where:** a finding, problem or incident page on develop or pre-release → **Take action** (admins). On a task:
**Ready for fixer**. Runs are listed under [Fix runs](/help/agent#fix-runs).

### How do I… {#take-action-how}

1. Open the finding and press **Take action**.
2. Read the whole prompt. Edit it if needed (up to 16 KB). Argus shows the fixed preamble it adds in front.
3. Pick the target hosts of that environment.
4. Tick **I reviewed this prompt and the target hosts** and press **Run fix**.
5. Follow the run on its page: the final report, the backups it took, the verification and the rollback commands.

### What it will not do {#take-action-not}

- Never production. A prompt that names a production host, address or name is refused. The fixer's network also
  blocks production addresses.
- Only the environments the operator listed. If Take action says it is not enabled or not set up, the operator has
  not granted that access; Argus never grants it to itself.
- One fix at a time, at most 10 a day, about 20 minutes each.
- Only fixes on hosts over SSH. It does not push to GitLab or change CI variables.

## Problems {#problems}

A **problem** (`P-123`) is the root cause behind several issues. One problem can explain incidents, QA issues,
vulnerability groups, CI/CD findings, capacity and VM findings, and single findings. The page answers "which root
causes are open, and what do they explain?".
**Where:** Incidents → [Problems](/problems). This page appears only while the feature is switched on.

### How do I… {#problems-how}

- **Create one:** on an incident (or another item) choose **Create problem**. It starts as investigating, with that
  item linked.
- **Link items:** **Link to problem…** on an item page, or select several rows on a list and **Link selected to**.
  Pick the link type: caused by, explains, fixed by, related to, duplicate of, blocks.
- **Review suggestions:** Argus suggests links by matching hosts, upstreams, services, domains, vulnerability
  groups, CI rules and the problem's RCA. Open [Review → Problem links](/review?kind=links) and **Accept link**
  or **Reject** (the reason is remembered). Narrow by confidence band or item type, or search. To decide several at
  once, tick them: a bar appears with **Accept links…** and **Reject…**, and a confirm page lists what will happen
  before anything is sent.
- **Find a problem:** the tabs (Open, Suggestions, Resolved, Closed, All) show their counts; search matches `P-12`, the
  title, the root cause and the owner; the Stage, Severity and Environment chips narrow the list; sort by worst
  first, recently updated, longest open or most linked. 25 a page.
- **Move it forward:** **Change status**: new → investigating → known error → fix in progress → resolved → closed.
  "Known error" needs the root cause in a few words. **Resolve** needs a note on the fix and how it was verified,
  and can also resolve the linked open incidents.
- **Merge** two problems that are the same: **Merge into…** with the other P- number.

The primary cause of an item shows as a banner on that item's page.

### What it will not do {#problems-not}

- Suggestions are never accepted automatically unless an admin turned that on.
- Viewers can link, but their links are stored as suggestions for an admin to accept.
- CVE and CI/CD incidents linked to a resolved problem still resolve only on evidence.
- A problem closes by itself some days after it was resolved; a recurrence re-opens it and is counted.

:::admin
### Settings for admins {#problems-admin}

- Switch: `FEATURE_PROBLEMS=1` (default 0). Other keys start with `PROBLEMS_`: `PROBLEMS_SUGGEST_MIN_CONFIDENCE`
  (0.60), `PROBLEMS_REJECT_DAYS` (30), `PROBLEMS_CLOSE_DAYS` (14), `PROBLEMS_AUTO_ACCEPT` (0; when on, only very
  confident links on develop and staging).
- The controls hide themselves while the request folder on the server is missing.
- A one-off backfill of problems from past deep RCAs exists as a dry run; it changes nothing until it is applied
  on the server.
:::

## Conversations {#qa-reports}

Every {{qa_room}} message, and what Argus did with it. Messages about the same problem become one **QA issue**.
**Where:** Work → [Conversations](/qa).

### How do I… {#qa-how}

- **See open issues:** the page opens on issues. The figures and chips filter them: open issues, **Needs info**
  (Argus asked the reporter), actionable (an incident is open for it) and resolved. "Not issues today" counts the
  greetings and chat that were not problems.
- **Trace a report:** each issue shows the chain: message → triage run → issue → incident → RCA → Jira task →
  fix run, with links. "Open in Matrix" jumps to the original message.
- **See every message:** **All messages** lists each one with Argus's disposition: problem, known incident,
  duplicate, not a problem, suspicious, unreadable or unclassified. Filter by source, sender and disposition.

### Follow-up questions {#qa-questions}

When a report misses what Argus needs (the page, the environment, the time, the error text), Argus replies once in
the reporter's thread in {{qa_room}}, prefixed "Argus:", in the reporter's language.

- At most one question per issue and 10 a day. Never about a suspicious, reconstructed or old message.
- Never to the DevOps team: Argus admins with a Matrix ID never get a question, and their replies count as answers,
  not as new reports.
- No question once a person has already answered the report (in its thread, or a DevOps reply in the room).
- A screenshot question only when the report has an image. A report that is only a link gets at most a question about
  the error, in the reporter's language.
- A question that would contain an address, internal name, port, URL or secret is replaced by a fixed template.
- The reporter's answer in the thread is attached, and the next triage uses it.
- No answer in 24 hours: the issue shows "Needs info, no answer" and Argus does not ask again.

### What it will not do {#qa-not}

- Argus never posts in {{qa_room}} except these threaded questions.
- A report that lacks detail does not page anyone and does not open an incident unless evidence confirms it.

:::admin
### Settings for admins {#qa-admin}

Settings → General → **QA follow-up questions**: on (default) or off, **Dry run** (the question is shown on
Conversations and not sent), and **Read images in QA messages** (off by default): screenshots in {{qa_room}} go to the
triage model, which reads the error text and page from them. They are deleted when the run ends.

Root on {{host}} can change the same settings without the browser:
`sudo argus-apply-settings qa off` (or `on`, `dry-run`, `send`, `images-on`, `images-off`; no word prints them).
The change is logged and posted to Matrix, and this page shows it.
:::

## Alerts {#alerts}

The alerts firing or pending right now in both data centres, refreshed every minute. No model is involved.
**Where:** Incidents → [Alerts](/alerts).

- The figures count firing alerts by severity; each figure filters the list.
- Narrow by state, severity and data centre (chips with counts), search by alert name, instance or summary, and
  sort by severity, newest or longest active; 25 per page.
- An alert Argus investigated links to its RCA.

## Alert hygiene {#alert-hygiene}

Alerts that nobody fixes or silences, from a daily report. **Where:** Infrastructure → [Alert hygiene](/hygiene).

Sections: test alerts still firing, alerts firing for more than 7 days, dead scrape targets, flapping alerts and
noisy warnings. Each table shows five rows and folds the rest. A suggested change is shown once per section, or per
alert where it differs. This page only reports; it changes no alert rule.

## RCA reports {#rca-reports}

Every root-cause analysis Argus wrote: deep RCAs, alert RCAs and the merged reports in the repository. A report
shows here as soon as the run ends, before its merge request is merged. **Where:** Incidents → [RCA library](/rca).

- Open a report for the Summary, the full analysis, **How to fix it** and the prompt.
- **Fix options as tasks** turns an option into a board task.
- "Redacted" means the secret scan replaced a token or password before the report was stored.
- All single findings of chat triage and log RCA are on [Findings and RCA reports](/incidents/problems).

## FAQ {#faq}

:::details Why did an alert not create an incident?
Warnings, info alerts and flapping alerts are only counted. Critical alerts must stay firing for 2 minutes, errors
for 10 minutes and Kuma monitors for 5 minutes before Argus investigates. The same alert is investigated at most
once per 6 hours, and the watcher has an hourly and daily cap.
:::

:::details Why is Investigate deeply greyed out?
It is admins only, and limited to one per incident per hour, six a day and one at a time. The text next to the
button says which limit applies.
:::

:::details Can Argus fix a production incident?
No. It writes the options and a prompt. A person runs the change with the operator's approval.
:::

:::details What is the difference between a finding, an incident and a problem?
A finding is one result of one run. An incident collects the findings about the same host and symptom. A problem is
the root cause behind one or more incidents and other items.
:::
