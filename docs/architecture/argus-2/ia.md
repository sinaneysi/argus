# Argus 2: information architecture v2 (as built)

Status: **built**, 2026-10-30; sidebar v2 (collapsible groups) built 2026-10-31. Decision: [ADR-0016](../adr/0016-argus-2-platform.md)
D3 and OD-8; sidebar groups and collapse: operator decision 2026-10-31. Design: [README.md](README.md) §3. Code:
`internal/app/ia.go` (groups, links, counts, Needs you, redirects), `internal/app/layout.go` (the shell),
`internal/app/views_review.go`, `views_review_panel.go` (Review inbox), `internal/app/pages_core.go` (`OperationsPage`, `todayOverview`),
`packages/design/static/ia.css`, `review.css`. Tests: `internal/app/ia_test.go`, `review_test.go`.

## Rule

The sidebar has six groups and Marketing (the SEO page, back since 2026-11-07); Settings and Help sit in its footer. A group is a `<details>`: its `<summary>`
(icon, name, a chevron that turns) is the toggle and its pages are links right under it. **Collapse rule:** the server
sets `open` on the group that holds the current page; every other group is closed, and the person can open any of them.
The current link is `aria-current="page"` (`"true"` on its detail pages, which keep their crumb). The first page of a
group leads with **Needs you** (not Home: Today is the plan itself). Every human decision is a row in the **Review
inbox**. Server-rendered Go, strict CSP (`script-src 'self'`), no JavaScript.

**Why no tabs (operator decision 2026-10-31).** IA v2 first shipped seven flat sections whose pages were a tab strip
(`nav.sect`) above the page title: two clicks and a scan of a second navigation row to reach a page. The operator
asked for the pages to be listed in the sidebar itself, in groups that expand and collapse, and for a different
grouping. The tab strips are gone. In-page views that belong to one page stay under its header: CI/CD's Failures,
Stuck and Branches & variables; Settings' tabs; the Review inbox's kind filter (a chip row, not tabs). Check a repo,
formerly a CI/CD chip, is its own link.

## Sidebar

| group | question | links (first = landing) |
|---|---|---|
| **Home** | What needs me now? | Today `/today` · Review inbox `/review` (kind filter below) |
| **Incidents** | What is broken, and why? | Incidents `/incidents` · Alerts `/alerts` · Problems `/problems` (feature on) · RCA library `/rca` |
| **Infrastructure** | What do we run, and is it healthy? | Inventory `/inventory` · Capacity `/capacity` · Certificates `/certs` · Backups `/backups` · Checks `/operations` · Alert hygiene `/hygiene` |
| **Delivery** | What do we ship, and is it safe? | CI/CD `/cicd` (with its own Failures, Stuck, Branches & variables) · Check a repo `/cicd/repo` · Vulnerabilities `/vulns` |
| **Work** | What is being done, and by whom? | Tasks `/board` · Conversations `/qa` · New task `/board/new` (also `/board/ask`) |
| **Agent** | What is the agent doing, and is it getting better? | Runs `/runs` · Knowledge `/knowledge` · Fix runs `/admin/fix` (admins) |
| footer | | Help, Settings (admins, keeps its own tab row), theme, account |

**Counts** (admins only, where a person acts), on the link: Today = open items left today; Review inbox = every
waiting decision; Incidents = open incidents; Checks = scheduled checks not OK or stale. Viewers see no counts. A
group's summary carries the sum of its links' counts, shown only while the group is closed (CSS hides it when open),
so a count is never hidden by a collapsed group.

## Needs you (first page of a group)

The first page of Incidents (Incidents), Infrastructure (Inventory), Delivery (CI/CD), Work (Tasks) and Agent (Runs)
starts with one line: how many things need a person in that group and about how long they take, the items folded in
a `<details>`.

**Estimates.** When today's plan has open items in the section's areas (`today_items.area`: Infrastructure =
backups, capacity, certs, hygiene; Incidents = incidents, problems; Work = qa; Delivery = cicd, vulns), the estimate is
the sum of their `est_min` (Argus's own estimate). Otherwise a rule of thumb per item (`needsDefaultMin` in `ia.go`):
failing check 15 min, open incident 30, agent task waiting for answers 10, open production CI/CD finding 20, run not OK
3. Review uses a rule of thumb per decision (`REVIEW_KINDS`): proposal 2 min, action approval 5, lesson 2, idea 3,
chat request 1, problem link 1. Every estimate says where it comes from.

## Review

One inbox (`/review`, "Review inbox"; redesign 2026-11-02, operator brief). **Queue + review panel**: the queue is every
waiting decision of the kinds the role may see (each kind capped at 200 rows), filtered and sorted in Go from stored
fields, 25 a page; a kind chip (`?kind=`) narrows it to one kind and keeps the filters. Selecting a row is a link
(`?sel=<kind>:<id>`, e.g. `sel=proposals:16`, `sel=approvals:OPS-238`): the server renders the same queue, filters,
sort and page with the item's panel beside it (CSS grid from 1200px; the panel is sticky) or, narrower, the panel
full width with **Back to queue** (the queue URL plus `#rv-<kind>-<id>`, which restores the position). Rows are real
links (`aria-current="true"` on the selected one, the words "in the panel" as the non-colour cue); the document title
names the selected decision. No JavaScript: refresh, back/forward and shared links work from the URL alone.

**URL state.** `kind`, `q` (title, summary, targets, ID, kind, env), `sev` (`critical|high|medium|low|none`), `env`
(an environment key or `none`), `src` (a proposal path: `qa|page|cve|cicd|cred|chat`), `sort`
(`urgency|oldest|newest|expiring`), `page`, `sel`, and `step=plan` (the plan review step of a selected action approval).
`mode=full` (or a parameter only the old page knows: `view`, `confirm`, `status`, `persona`, `area`, `effort`, `out`, `p`,
`review`, `all`, `sel=page`) embeds the kind's old queue page unchanged (bulk with its confirm step, Snoozed/Decided,
history tabs, settings); its links and GET forms keep `mode=full`. Act forms return to the queue URL without `sel`, so
nothing is selected for the person after a decision.

**Review order** (`rvUrgencyCmp`, unit-tested in `review_test.go`):

1. severity bucket: critical (and `page`) 4, high 3, medium (and `warning`) 2, **unknown 1**, low and info 0. Only task
   proposals carry a severity (`severity`, else the stored `sev_rank`). Action approvals, lessons, ideas, chat
   requests and problem links carry none and rank as unknown: above low (an unknown severity is not assumed low), below
   medium. Problem links do not inherit their item's severity (shown in the panel only).
2. environment: `release` (production) 0, `pre-release` (staging) 1, `develop` 2, another name 3, none 4. Sources:
   the proposal's `env`, the plan's `classification.environment` (`none` = none), the lesson's `env`, the linked
   item's `env`; ideas and chat requests have none.
3. decision deadline, soonest first, none last: a proposal's `created_at + expiry_days` (snoozing does not change it),
   a chat request's `created + 24 h`. No kind stores an operational deadline; the panel says "None recorded".
4. waiting time (`created_at`, `intake_at`, `proposed_at`, `created`, `at`), longest first;
5. kind order, then id: a total order, so the queue never reorders by itself.

Alternatives: oldest first, newest first (by waiting time, then urgency), expiring first (deadline, then urgency).
The page states the active order in one line.

**Panel** (order of the brief): title, kind, severity, environment; affected targets, decision and operational
deadlines, waiting since; proposed outcome; evidence and source (`rvEvidenceMissing`: empty, a placeholder such as
"From Argus." is shown as **No evidence recorded** with the source link, in the warning tint; kinds without an evidence
field, plans and chat requests, say so in a neutral box); what happens after
accepting; actions (one filled primary; Reject or Discard folded with the kind's reason rule); details and history
folded. Snooze (proposals, fixed 24 h in `bin/proposals.py`) is offered only when the expiry is more than 24 h away;
the panel always says the expiry keeps running while snoozed.

| kind | primary action | handler (unchanged) | who |
|---|---|---|---|
| Task proposals | Create Jira issue / GitLab issue / task (Edit, then create; Snooze 24 h; Reject) | `POST /proposals/act` | admins |
| Action approvals | Review execution plan → plan step → Approve plan v*N* & start | `POST /board/agent/spinup` (plan version pinned) | admins |
| Lessons | Approve lesson (Edit, then approve on the full list; Reject; Retire when back for review) | `POST /admin/lessons/act` | admins |
| Ideas | Generate build prompt (Mark under review; Reject with a reason) | `POST /ideas/act` | admins act, viewers read |
| Chat requests | Create task (Discard) | `POST /board/mxpreview` | admins (their own previews) |
| Problem links | Link to P-*n* (Reject) | `POST /problems/act` (only when root created `problem-actions`) | admins act, viewers read |

Every form carries the session CSRF token and the same fields as before; the MFA policy (`POST_ROUTES`), role checks
and the demo write refusal run unchanged. Board-family forms carry `return=<queue URL>` (`BoardReturn` allows `/review`),
problem links `back=<queue URL>`, so the person lands back in the same queue with the request banner. There is no bulk
plan approval.

**Not yet:** knowledge promotion (prompt and routing promotions, ADR-0016 D8) joins Review when the eval harness
exists; it has no handler today, so it has no kind.

## Old URLs (`REDIRECTS`, `REMOVED`)

| old | new | answer |
|---|---|---|
| `/` (Overview) | `/today` | 303 (the landing page; the target depends on nothing, but `/` is not a page any more) |
| `/proposals` | `/review?kind=proposals` | 301, query kept (a parameter only the old page knows opens its full list) |
| `/ideas` | `/review?kind=ideas` | 301, query kept |
| `/admin/lessons` | `/review?kind=lessons` | 301, query kept |
| `/problems?view=suggestions` | `/review?kind=links` | 301, query kept (without `view`) |
| `/admin/design`, `/admin/design/view`, `/admin/design/review`, `/admin/design/shot` | removed (OD-8) | 404 |

ADR-0016 plans 90 days of 301, then 410; the switch is a change to `RedirectFor`.

## Page map (old → new)

| before (8 nav groups, ~60 pages) | IA v2 (sidebar v2, 2026-10-31) |
|---|---|
| Overview `/` | merged into **Home → Today** (planner off: health summary; scheduled checks → Infrastructure → Checks) |
| Today `/today` | **Home → Today** |
| Proposed tasks, Ideas, Lessons, Matrix previews on `/board`, Problems → Suggestions | **Home → Review inbox** kinds |
| Incidents, Problems, Alerts, RCA reports | **Incidents** links |
| QA reports | **Work → Conversations** |
| Task board, Ask Argus, New task | **Work** links |
| Inventory, Capacity, Backups, Certificates, scheduled checks, Alert hygiene | **Infrastructure** links |
| CI/CD (+ sub-pages), Check a repo, Vulnerabilities | **Delivery** links (Check a repo its own link) |
| Agent runs, Fix runs, What Argus knows | **Agent** links (Runs, Knowledge, Fix runs) |
| SEO ("Marketing" group) | **Marketing → SEO** (back 2026-11-07, operator decision; it reads the SEO jobs' tables, Argus runs no SEO job) |
| Design reviews | **removed** (OD-8) |
| Legacy detail pages (`/incidents/run`, `/incidents/rca`, `/incidents/problem`, `/incidents/problems`) | kept as detail pages of Incidents |

Interim (2026-10-30 → 31): seven flat sections (Today, Review, Operations, Incidents, Work, Delivery & Estate, Argus)
with tab strips; renamed Operations → Infrastructure, Delivery & Estate → Delivery, Argus → Agent, Review → Review
inbox (in Home). Top-level destinations: 21 links in 6 groups; URLs, redirects and removals unchanged.
