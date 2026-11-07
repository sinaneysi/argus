# Argus 2: product definition and information architecture

Status: **proposal**, 2026-10-30. Author: Sam (architecture). Baseline: GitLab `main` `f0d119b` (ADR-0015 P0 on
`agent/argus-p0-2026-10-30`, not yet on main). Decision record: [ADR-0016](../adr/0016-argus-2-platform.md).
Companions: [architecture.md](architecture.md) (target design), [roadmap.md](roadmap.md) (phases, first MRs).

## 1. What Argus is

Argus is a self-hosted DevOps colleague. It **watches** the estate through the company's own tools, **keeps the
daily hygiene green** (certificates, backups, logs, credentials, capacity), **investigates** incidents and
requests like a senior DevOps and software engineer (code-aware, evidence-first), **acts** only through approved,
audited, narrow capabilities, and **gets measurably better** after every task. One install per company; adopting it
means editing configuration, never forking code. Argus 2 is written entirely in Go (one module, two binaries; ADR-0016 D1):
no Python at runtime, server-rendered HTML from Go templates, no client JavaScript.

### Who

| persona | wants | today's pain (observed in the code and the operator's notes) |
|---|---|---|
| **Operator / DevOps lead** (admin) | one screen of "what needs me", decisions in one click, no surprises in production | ~60 pages, 8 nav groups, decisions spread over `/proposals`, `/ideas`, `/admin/lessons`, `/board` (Matrix previews), `/problems?view=suggestions` |
| **Team engineer** (operator role) | hand a task to the agent, chat about it, get a fix or an MR | task chat exists but is board-bound; no task creation from chat; secret box off |
| **Developer / QA** (viewer, chat only) | report in their own chat tool, get an answer or a ticket | only Matrix is read; Mattermost / Rocket.Chat not supported |
| **Adopting company** (installer) | connect its own Grafana / ELK / Splunk / Sentry / GitLab / Jira / chat and AI provider | adapters are designed (ADAPTERS.md) but not built; one AI vendor; 1,613 hard-coded literals (HARDCODE-AUDIT.md) |

### Jobs to be done

1. *When I start my day*, show me what is broken, what is about to break and what waits for my decision, so I can
   clear it in minutes.
2. *When a certificate, backup or log check goes red*, tell me why and give me the one safe action, and do it
   automatically where policy allows it.
3. *When an alert fires or someone reports a problem in chat*, investigate before I read it, using every
   knowledge source we have, and hand me an evidence-backed RCA with the fix.
4. *When I describe a task in a chat*, create the ticket, plan it, ask me only for what it lacks (including a
   credential, safely), and do it within its permissions.
5. *After every task and RCA*, remember what was learned, prove it helps, and stop repeating mistakes.
6. *Every week*, propose improvements worth doing, with evidence, that I can accept into the backlog.
7. *When a provider runs out*, keep working on another one instead of stopping.

## 2. Core modules (9 + platform)

| # | module | job | builds on (today) | new in Argus 2 |
|---|---|---|---|---|
| M1 | **Today & Review** | the decision surface: what needs me, every pending human decision in one inbox | `/today` (`bin/today.py`), `/proposals`, `/ideas`, `/admin/lessons`, Matrix previews | one **Review** inbox for all decision kinds; `/` and `/today` merged |
| M2 | **Day-2 Operations** | recurring checks with status, freshness, owner, runbook and a safe action | `cert_scan`, `backup-check`, `log-rca`, `hygiene`, `cred_expiry`, `cdn_watch`, `disk_explain`, `capacity` | **Check registry as data**, one result model, "stale" as a state, actions through the broker |
| M3 | **Incidents & Problems** | alert/report → incident → RCA → problem (root cause) | `ops-watch`, `incidents.py`, `problems.py` (ADR-0008), `rca-deep` | signals from every source; RCA cites evidence ids; RCA feeds the learning loop |
| M4 | **Work** | tasks two-way with GitLab issues and Jira, task chat, agent execution, secret box | `board_sync`, `gitlab_issues`, `jira_board`, `task_agent`, `task_chat`, `task_secrets` (ADR-0009/0010/0013) | **New-task chat** (chat → issue → plan → run), `Tracker` adapter interface, secret box on after the TLS hop |
| M5 | **Conversations** | ingest team chat, cluster issues raised there, investigate read-only before anyone asks | `ops-watch`, `triage_collect`, `qa_issues`, `matrix_dm` (Matrix only) | `ChatSource` adapters (Matrix, Mattermost, Rocket.Chat), per-channel policy, pre-emptive read-only investigation |
| M6 | **Brain** | the knowledge sources and the tools the agents use on them | `obs.sh`, `gitlab-ro.sh`, `ssh-ro.sh`, `outline_client`, service notes, lessons | **intent-level tool catalogue** (`metrics.query`, `logs.search`, `errors.top`, `code.search`, ...) over `KnowledgeSource` adapters (Grafana, Prometheus/Loki, Elasticsearch, Splunk, Sentry, GitLab, Outline, Jira); retrieval index |
| M7 | **Agent runtime** | run agents safely, cheaply and without stopping | `run-task.sh`, `claude_gate.py`, per-task budgets, `claude -p` only | job queue with priorities, **provider router with fallback and deferral**, second runtime (`argus-loop`) for non-Claude providers, sandboxes |
| M8 | **Learning** | lessons, retrieval, evaluation of past answers, gated promotion | `lessons.py` (ADR-0013 §4), `/knowledge` | outcome capture, replay **eval harness**, quality metrics, promotion only when the eval does not regress |
| M9 | **Ideas** | improvement agents; operator accepts ideas into the backlog | `ideas.py`, personas in `config/ideas.json`, `/ideas` | ideas become a Review kind; accepted idea → task; impact tracked |
| P | **Platform** | config, adapters, access policy + action broker, audit, onboarding, self-observability | ADR-0001/0002/0003/0006/0007, ACCESS.md, `selfcheck` | built (not just designed): config loader, broker, installer, onboarding checks |

Delivery (CI/CD), Estate (inventory, services) and Security (CVE) are **lenses**: domain-specific views over the
same findings and signals. They keep their pages but stop being separate products.

## 3. Information architecture (navigation v2)

Rule: **a section answers one question**. Every page is one of the four templates in DESIGN_SYSTEM.md §4 (queue,
overview, detail, settings). Every decision a human makes lives in **Review**; everything else links there.

```
Today                       "What needs me now?"           landing page; replaces / and /today
Review           (count)    "What waits for my decision?"   one queue, kind tabs (below)
Operations                  "Is the daily hygiene green?"   Day-2 checks
  Checks                      overview: every check family, status, freshness, next action
  Certificates · Backups · Logs · Credentials · Capacity · Alert hygiene · Edge/CDN    (check family pages)
Incidents                   "What is broken and why?"
  Incidents · Problems · Alerts (live) · RCA library
Work                        "What is being done, by whom?"
  Tasks (board, two-way)  · Conversations (chat-raised issues) · New task (chat)
Delivery & Estate           "What do we run and ship?"
  CI/CD · Inventory · Services · Vulnerabilities
Argus                       "What is the agent doing, and is it getting better?"
  Runs · Knowledge · Quality · Providers & budget
Settings (footer)           General · Users & access · Sources · Trackers · Chat channels · Checks · AI providers ·
                            Policy (read-only view) · Credentials · Backup targets
Help (footer)
```

**Review kinds** (tabs with counts; one row template; explicit verbs per DESIGN_SYSTEM.md §6):

| kind | verb on accept | today at |
|---|---|---|
| Task proposal | **Accept & create issue** (tracker chosen by routing) | `/proposals` |
| Action approval | **Approve plan `<hash8>`** (broker, step-up 2FA) | fixer forms, task Start |
| Lesson | **Approve lesson** / Edit & approve | `/admin/lessons` |
| Idea | **Accept & create task** | `/ideas` |
| Chat reply preview | **Send reply** | Matrix previews on `/board` |
| Problem link suggestion | **Link to problem P-n** | `/problems?view=suggestions` |
| Knowledge change (prompt/routing promotion) | **Promote** (eval result shown) | new |

### Cut, merge, move (from ~60 GET pages to ~30)

| today | Argus 2 | why |
|---|---|---|
| `/` Overview + `/today` | **Today** | two landing pages answer the same question |
| `/proposals`, `/ideas`, `/admin/lessons`, Matrix previews on `/board`, `/problems?view=suggestions` | **Review** tabs | five places to make decisions → one inbox, one count |
| `/alerts`, `/hygiene` | Incidents → Alerts; Operations → Alert hygiene | hygiene is a recurring check, live alerts are incident signals |
| `/incidents/run`, `/incidents/rca`, `/incidents/problem`, `/incidents/problems` | tabs of `/incidents/view` and `/problems/view` (301 redirects) | legacy sub-pages duplicate the detail page |
| `/rca`, `/rca/view` | Incidents → RCA library (list) + RCA tab on incident/problem | an RCA belongs to its incident |
| `/qa`, `/runs/view` (QA source) | Work → Conversations | QA chat is one chat source among several |
| `/runs`, `/admin/fix`, `/admin/fix*` | Argus → Runs (filter: kind = check/investigation/task/fix) | one run model |
| `/knowledge`, `/admin/lessons` (list) | Argus → Knowledge (lessons, sources status, retrieval stats) | approval moves to Review |
| `/certs`, `/backups*` (5), `/capacity` | Operations family pages; backup "New backup" and plans stay as sub-pages of Backups | they are checks with actions |
| the SEO page and its own nav group | **back** as Marketing → SEO (operator 2026-11-07); the SEO jobs stay outside Argus | the operator wants the page |
| the design reviews of Argus itself | **removed** (ADR-0016 OD-8); the code-based designer agent stays a development tool | self-development tooling, not operations |
| `/cicd` + 6 sub-pages + `/cicd/repo` | Delivery → CI/CD with tabs (Overview, Failures, Stuck, Audit, Repos) | keep content, collapse navigation |
| `/inventory`, `/inventory/view`, service map | Delivery & Estate → Inventory, Services | — |
| `/board/ask` | Work → New task (chat) | the ask flow becomes the new-task chat |
| `/admin/outline*` | Settings → Sources (Outline is a knowledge source) | integrations are sources |

Navigation rules: at most 7 sidebar groups + footer; at most 7 items per group; counts only where a human must
act (Review, failing checks, open incidents), never duplicated in two places; one open group at a time (today's
`NAV_OPEN` behaviour kept). Old URLs answer `301` to their new home for 90 days (`apps/api/internal/app/redirects.go`,
new), then `410`.

### Page contract (what every Argus 2 page must show)

1. Title + one-line purpose. 2. Decision-critical summary (≤ 3 numbers). 3. Filters as GET parameters. 4. Rows with
the separate dimensions **severity · category · environment · state · source** (DESIGN_SYSTEM.md §7). 5. One next
action per row with its tier badge (`read`, `auto`, `needs approval`, `never`). 6. Freshness ("checked 4 min ago",
"stale since 07:30") on every status. 7. Evidence on demand (`<details>`), never on first sight.

## 4. Non-goals for Argus 2

- No shared multi-tenant SaaS: one install per company (ADR-0016 D9).
- No production write without a human approving the exact plan hash (guardrail unchanged; ADR-0016 D7).
- No new nav group for a single page; no client-side JavaScript in this programme (ADR-0016 D2).
- No automatic ticket creation from chat or agents: agents propose, humans accept (standing operator rule).
