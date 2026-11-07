# Argus architecture overview

Status: proposal, 2026-10-21. Author: Sam (architecture). Baseline: `main` at `1338605`, Go port
`feat/go-app` at `ab29344`. Companion documents: [CONFIG.md](CONFIG.md), [ADAPTERS.md](ADAPTERS.md),
[ACCESS.md](ACCESS.md), [CVE.md](CVE.md), [HLD-problems.md](HLD-problems.md), [HARDCODE-AUDIT.md](HARDCODE-AUDIT.md), [ROADMAP.md](ROADMAP.md),
[adr/](adr/).

Argus is an unattended DevOps assistant. It collects evidence deterministically, asks a model to
explain it, and gives people one page per problem with the exact fix. Today it acts only on develop
and pre-release (the fixer). This set of documents makes it (1) installable at another company by
changing configuration only, and (2) able to act on production through narrow, approved, audited
capabilities.

## 1. Component map

```
                        ┌──────────────────────────── argus-01 (one VM) ──────────────────────────────┐
 Sources (read-only)    │                                                                           │
 ─────────────────      │  COLLECTORS (no model; argus unless noted)       STORES                │
 Matrix alert rooms ───────▶ ops-watch (long-poll, queue/)                     ops.db  (SQLite WAL)  │
 Matrix QA room     ───────▶ triage_collect (chat-triage fetch)                auth.db (app)         │
 Prometheus/Loki ×N ───────▶ alerts-snapshot (1 min), insights (capacity,      board.db (root)       │
   via Grafana               hygiene, readiness), vmstate, vm-pulse            state JSON files      │
 GitLab (Reporter) ────────▶ cicd-watch (5 min), cicd-daily, service-map       runs/, queue/,        │
 GitLab (read_api) ────────▶ ci-vars-meta (DynamicUser, root credential)        reports/             │
 MinIO / S3 (ro)    ───────▶ backup-check, backup-plans list (root)            settings-in/ (app→root)│
 TLS endpoints      ───────▶ cert-check (cert_scan)                            settings-out/ (root→app)│
 hosts (argus-ro)───────▶ ssh-ro.sh catalogue (inventory, verify, RCA)       /etc/argus (secrets)│
 Kuma (room + push) ───────▶ selfcheck → kuma-push                             pan.db (pantalaimon)  │
                        │                                                                           │
                        │  MODEL TASKS (claude -p, user argus, run-task.sh, budget+turn caps)   │
                        │   incident · chat-triage · log-rca · rca-deep · rca-auto · disk-explain  │
                        │   vm-explain · backup-check · cert-check · task-intake · task-run        │
                        │   tool-less calls: cicd triage, matrix                                    │
                        │   request translation, fix_regen                                          │
                        │        │ writes JSON → record.py / incidents.py validate → ops.db         │
                        │                                                                           │
                        │  DISPATCHERS / ROOT JOBS (root; root-owned copies, never run the clone)   │
                        │   apply-settings · run-requests · fix-requests · board-sync (+task_agent, │
                        │   jira_board, jira_connect, matrix_requests) · backup-plans/backup_exec/  │
                        │   backup-bundle · outline sync ·                                          │
                        │   state backup · ci-vars-meta                                             │
                        │        │ start                                                            │
                        │  FIXER  ops-fix@<id> (user argus-fix, IPAddressDeny prod, 20 min)      │
                        │        └─ ssh dev-*-fix / pre-*-fix (NOPASSWD sudo on dev/pre hosts)      │
                        │                                                                           │
                        │  APP  argus-api container (uid 65534, read-only rootfs)                  │
                        │   Python app/ today → Go apps/api/ (byte-identical pages, feat/go-app)      │
                        │   reads ops.db/state/settings-out; writes ONLY request files to           │
                        │   settings-in/<kind>/ (root:nogroup 2730, cannot read back)               │
                        │                                                                           │
                        │  NOTIFY  notify.sh → notify_format/notify_styles → Matrix room            │
                        └───────────────────────────────────────────────────────────────────────────┘
 Integrations: Matrix (pantalaimon, E2EE) · Grafana×2 · GitLab (ro / bot / meta) · Jira (PATs) ·
 Outline · Keycloak (OIDC) · MinIO/S3 · Uptime Kuma · Claude (OAuth today) · hosts over SSH.
```

### 1.1 Components and responsibilities

| layer | component | runs as | trigger | reads | writes |
|---|---|---|---|---|---|
| collector | `bin/ops-watch.py` | argus | `ops-watch.service` (always on) | Matrix alert + Kuma rooms via pantalaimon | `queue/*.json`, `watch-state.json`, `watch-stats.json`, `chat_messages` |
| collector | `bin/alerts-snapshot.sh` → `scripts/obs.sh` | argus | timer 1 min | Prometheus alerts per site (Grafana proxy) | `alerts-firing.json` |
| collector | `bin/capacity.py`, `bin/hygiene.py`, `bin/readiness.py` (`insights.sh`) | argus | timer 08:30 | PromQL per site, inventory | `capacity.json`, `hygiene.json`, `readiness.json` |
| collector | `bin/inventory.py`, `bin/vmstate.py`, vm-pulse | argus | timers | Prometheus, ssh-ro `snapshot` | ops.db `vms`, `vm_*` |
| collector | `bin/cicd_estate.py` (`watch`, `scan`, `audit`), `cicd_verify.py`, `cicd_changes.py` | argus | timers 5 min / 08:05 | GitLab Reporter API (`gitlab_ro.py`), ssh-ro | ops.db `cicd_*`, incidents |
| collector | `bin/ci_vars_meta.py` | DynamicUser | timer 07:40 | GitLab `read_api` (root credential) | `/var/lib/ops-ci-meta/meta.json` (names/flags, no values) |
| collector | `scripts/backup-check.sh`, `scripts/cert-check.sh` (`cert_scan.py`) | argus | timers 07:00 / 07:30 | MinIO (ro alias), TLS handshakes | runs TSV → ops.db |
| collector | `bin/selfcheck.sh`, `bin/cred-check.sh` | argus | 5 min / hourly | units, pantalaimon, app, credential probes | `selfcheck.json`, `cred-status.json` |
| collector | `bin/service-map.py`, `bin/service-notes.py`, `bin/data_platform.py` | argus / operator workstation | weekly / manual | nginx role repo, context repo | `config/service-map.json`, `service-notes.json`, `data-platform.json` |
| model | `bin/run-task.sh <task>` + `prompts/<task>.md` | argus | timers, `.path` units, request files | the scripts above (allowlisted in `.claude/settings.json`) | problems JSON, RCA markdown, MR on `agent/*` |
| core | `bin/record.py`, `bin/incidents.py`, `bin/qa_issues.py`, `bin/rca_reports.py` | argus | after each run / `.path` | run outputs | ops.db `runs`, `triage_problems`, `incidents`, `qa_issues`, `rca_reports` |
| notify | `bin/notify.sh`, `notify_format.py`, `notify_styles.py`, `matrix.py` | argus | after runs | structured data (never free model text beyond one sentence) | Matrix notify room (refuses watched rooms) |
| dispatcher | `apply-settings`, `run-requests`, `fix-requests` | root | `.path` on `settings-in/*` | request files from the app | timer drop-ins, `thresholds.env`, unit starts, `settings-out/*` |
| dispatcher | `board_sync.py` (+ `task_agent.py`, `jira_board.py`, `jira_connect.py`, `matrix_requests.py`) | root | timer 2 min + path | Jira (PATs), request files, `matrix-dm/out` | `board.db`, Jira, `task-agent/jobs`, `settings-out/*` |
| dispatcher | `backup_plans.py`, `backup_exec.py`, `backup-bundle.sh`, `backup-install@` | root | path + timer | backup requests, stored URIs | MR in backup-templates, install over the fixer path |
| dispatcher | `outline_sync.py`, `backup-state.sh` | root | timers/paths | rendered pages, state | Outline, backups |
| actor | fixer `ops-fix@<id>` → `fix-run.sh` → `claude -p` | argus-fix | `fix-requests` | reviewed prompt, `fix.env` | changes on develop / pre-release hosts over ssh + sudo |
| app | `app/server.py` + `views_*.py` (Python) → `apps/api/` (Go) | uid 65534 in a container | HTTP behind the edge | ops.db, state, `settings-out` (read-only mounts) | `settings-in/<kind>/*.json` only |

### 1.2 Timers (today, Northwind values; all become `SCHEDULE_<TASK>` keys, see CONFIG.md §6)

| unit | schedule | | unit | schedule |
|---|---|---|---|---|
| backup-check | 07:00 | | cicd-watch | every 5 min |
| cert-check | 07:30 | | cicd-daily | 08:05 Europe/Amsterdam |
| chat-triage | every 30 min | | ci-vars-meta | 07:40 |
| log-rca | 03,09,15,21:10 Europe/Amsterdam | | outline | 07:40 Europe/Amsterdam |
| alerts snapshot | every 1 min | | board-sync | every 2 min + path |
| insights (capacity/hygiene/readiness) | 08:30 Europe/Amsterdam | | selfcheck | every 5 min |
| state backup | 03:15 | | credcheck | hourly |
| brief (digest) | 09:00 | | design-check | 30 min + path |
| vmstate / vm-pulse / rca-auto | timers | | incidents tick | timer + path |

## 2. Data flows

1. **Alert → incident → RCA.** Alertmanager / Kuma post to Matrix → `ops-watch` parses the
   site-specific message format, dedupes by fingerprint, applies thresholds → `queue/<ts>.json` →
   `argus-incident.path` → `run-task.sh incident` (model, read-only tools) → problems JSON →
   `record.py` → `incidents.py` (key `SITE|subject|family`) → notify (page only for production +
   customer-facing) → app `/incidents`.
2. **QA chat → issue → Jira.** QA room messages → `chat-triage` → per-message disposition →
   `qa_issues.py` asks follow-up questions (only send to a watched room) → incident → QA intake
   creates a Jira issue (root, owner's PAT).
3. **Operator action request.** App button → `settings-in/<kind>/<id>.json` → `.path` → root dispatcher
   re-validates (user enabled, role, scope, caps, rate) → starts a unit → result in `settings-out/` +
   audit log + Matrix.
4. **Fix.** Admin reviews a fix prompt → `fix-requests` (root) → `ops-fix@<id>` (argus-fix, IP filter)
   → model with sudo on `dev-*-fix`/`pre-*-fix` aliases → report → `fix_runs`.
5. **CI/CD.** GitLab read API → `cicd_findings` / `cicd_deploys` → incidents → proposals with the exact
   API call (`cicd_write.py`, execution disabled).
6. **Secrets.** Entered once in the browser → `settings-in/<kind>-connect/` (0600, write-only dir) →
   root tests and stores under `/etc/argus/` (root 0600) → app sees status only.

## 3. Trust boundaries

| # | principal | trusted for | must never | enforced by |
|---|---|---|---|---|
| T0 | **operator (root on argus-01)** | installing root copies, writing scope files, granting host access | — | Unix |
| T1 | **root dispatchers** | validating requests, holding write credentials, starting units | run code from the argus clone; trust a request field without re-validation | root-owned copies in `/usr/local/sbin`, `/usr/local/lib/argus-*`; drop dirs 2730 |
| T2 | **app (uid 65534)** | rendering, authenticating users, writing request files | read secrets, read back request files, call integrations | container (read-only rootfs, no `/etc/argus` mount), CSRF, sessions, roles `viewer`/`admin` |
| T3 | **argus (collectors + model runs)** | read-only evidence, writing ops.db, opening MRs on `agent/*` of its own repo | read `/etc/argus`, ssh raw, write any host, write GitLab outside `agent/*` | `.claude/settings.json` allow/deny, guard hook `--agent` (fails closed on secret paths), systemd caps |
| T4 | **the model** | reasoning over evidence | be trusted; its text is data | only structured output is consumed; `record.py` validation; notify uses data not prose |
| T5 | **untrusted text** (chat, logs, alerts, Jira, MR comments) | evidence | instruct | prompt rule 8, `secret_scan.py`, fixed templates for outbound questions |
| T6 | **argus-ro on hosts** | read-only evidence | change anything | **client-side only** today: `ssh-ro.sh` catalogue on argus-01; the target account accepts any command (see finding F2) |
| T7 | **argus-fix** | changes on develop / pre-release | reach production | `fix-scope` (root), alias + network checks, `IPAddressDeny`, guard `--fixer`; on targets `NOPASSWD: ALL` |
| T8 | **ci-vars-meta DynamicUser** | variable metadata | expose values | own credential, writes names/flags only |

## 4. Findings that shape the design

- **F1. Environment classification is copied in five places** with different shapes: guard `RELEASE`/
  `PRE_RELEASE`/`DEVELOP` regexes, `fix-requests.py` `NETS` + a copy of `RELEASE` (a test keeps them
  equal), `ops-fix@.service` `IPAddressAllow/Deny`, `ssh-ro.sh alias_env()`, `admin/setup-fixer-access.sh`.
  This is the single most important company-specific fact and must become one data file
  (`environments.json`) rendered into each consumer (ADR-0006).
- **F2. Read-only host access is enforced on argus-01, not on the target.** `argus-ro` keys have no
  forced command; anyone with the key can run any command as that user. Acceptable for reads by a
  non-root account; **not** acceptable as the pattern for production writes. Production capabilities
  must be enforced on the target (forced command + per-capability sudoers), see ACCESS.md §6.
- **F3. The fixer is a free-form model with `NOPASSWD: ALL`.** Correct for develop, wrong for
  production at any approval level: an approver approves a prompt, not the commands the model will
  choose. Production actions are capabilities with fixed executors and a plan that is approved by hash.
- **F4. No hard-coded secrets were found** in code, prompts or units (all secrets come from
  `/etc/argus/*`); 1,613 company-specific literals were (HARDCODE-AUDIT.md).
- **F5. The integrations are already half-abstracted.** `obs.sh` takes a site id and reads
  `GRAFANA_<SITE>_URL/TOKEN`; OIDC is env-driven; Jira/Outline keep URLs in root-written files. The
  vendor coupling that remains is in the call sites (Matrix-specific room semantics, Jira status
  names, GitLab job names, the two Alertmanager message formats).
- **F6. The app is a renderer.** It never calls an integration; it reads files and writes request
  files. Therefore the adapter work lives entirely in `bin/` (Python) and does not touch the Go port.
  Only *display* configuration (company name, public URL, site labels, room label, locale) reaches the
  app (ADR-0005).
