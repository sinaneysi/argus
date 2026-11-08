# Roadmap

Product direction: [ADR-0016](architecture/adr/0016-argus-2-platform.md) (Go only, server-rendered pages,
strict CSP, one Go module with an `argus-web` binary and a multi-call `argus` binary). The detailed phase plan is
[architecture/argus-2/roadmap.md](architecture/argus-2/roadmap.md).

## What this repository starts with (2026-10-30)

- **Kept:** the Go web app (`cmd/argus-web`, `internal/`), its tests, the vendored modules, the design system
  (`packages/design`), the public demo (`demo/`), the generic architecture documents and ADRs 0001-0004, 0006-0010
  and 0016, the Argus 2 design set.
- **Renamed:** the product name to Argus everywhere (identifiers, `ARGUS_*` environment keys, the Go module
  `github.com/sinaneysi/argus`, the binary `argus-web`, paths `/opt/argus`, `/var/lib/argus`, `/etc/argus`, the
  system user and unit prefix `argus`). The web app's old `OPS_*` keys (`OPS_DB`, `OPS_STATIC`, ...) became
  `ARGUS_*` keys with the config loader; the old names still work as aliases.
- **Defaults:** the compiled-in site defaults are neutral examples in every build (`internal/app/site_defaults.go`);
  every installation sets its own values through the environment.
- **Dropped (to be ported to Go, job by job, with shadow parity per ADR-0016 D1):**

| dropped | what it did | size in the reference installation | Go target |
|---|---|---|---|
| Python collectors and dispatchers (`bin/*.py`, `bin/*.sh`) | alerts snapshot, incidents, QA intake, CI/CD watch and triage, backups, certificates, CVE feeds and matching, capacity, inventory, board sync (GitLab/Jira), notifications, documentation publisher (Outline service pages), model runs (`run-task`), fix runs, ideas, lessons, self-check | ~44k lines, 119 files | `argus` multi-call binary: `worker`, `checks`, `ingest`, `dispatch`. Incidents port requirement: log-RCA findings attach only to the primary alert host; hosts mentioned as already covered ("Known incident #N") are not candidates. Documentation publisher port requirement: publish under a configured parent document; refuse if the parent is missing (or trashed, archived, in another collection) and report it in the status; never fall back to the collection root; re-find lost pages by title only among the parent's children |
| Shell wrappers (`scripts/*.sh`) | the read-only host catalogue (`ssh-ro`), observability queries, backup and certificate checks | ~3.2k lines | `argus tools` + the action broker (ADR-0016 D7) |
| Python web app (`app/`) | the first renderer; five libraries the jobs still used (fix handoff, lazy reading, backup plan/URI parsers) | ~14k lines | already replaced by `argus-web`; the libraries exist in Go in `internal/app` |
| Model prompts (`prompts/*.md`) | task prompts for the model runs | 18 files | the provider router and prompt store (ADR-0016 D6, D8) |
| Python tests (`tests/`) | tests of the above | ~29k lines | with each port |
| systemd units (`systemd/`) | 112 timers and services of the jobs | 112 files | `examples/systemd/argus-*.{service,timer}` with the `argus` binary |
| Golden parity vectors | `md_golden.json`, `fixer_markers_golden.json` (Python/Go byte-parity of real reports) | 2 files | not needed once Python is gone |
| ADR-0005, 0011-0015, hardcode audit, internal roadmap | decisions about the reference installation (its CI/CD, mirrors, rename, leak audit) | - | - |

- **Still Python:** only development tools that are never shipped or run on an Argus host: `demo/crawl.py` (crawls
  every page of a running demo) and `demo/parity.sh` (runs the old Python generator from the git history to compare).

## Done

1. **`argus` multi-call binary skeleton and config loader** (2026-10-30). `cmd/argus` with `version`,
   `config check` and `demo`; `worker`, `checks`, `ingest` and `dispatch` are listed as not implemented yet.
   `internal/config` reads defaults, `/etc/argus/argus.env` and the environment, accepts the old `OPS_*` names with a
   deprecation warning, reports every problem at once and masks secrets. Example: `config/argus.example.env`.
   `argus-web` was unchanged at this step.
2. **Generator ported to Go** (2026-10-30). `argus demo generate` (`internal/demo`) writes the same data as
   `demo/generate.py` did: identical SQLite rows, byte-identical JSON, same demo users. The Python file is gone. The
   demo is now two Go images, both `scratch` and uid 65534: `argus:app` (21 MB) and `argus:data` (11 MB, was 75 MB).
3. **`argus-web` reads its settings through `internal/config`** (2026-10-30). Both binaries share one loader and one
   schema. The `ARGUS_*` names are primary; the old `OPS_*` names and `ORG_NAME` still work and print one warning
   each at start. An invalid configuration stops `argus-web` before it listens, with every problem listed. New:
   `argus config show [-json]` (value and source of every key, secrets masked) and `config/schema.json`
   (`argus config schema`). A test fails when the web app reads the environment directly. A crawl of 4000 demo pages
   gave the same HTML as before, apart from timestamps.

4. **First collectors in Go** (2026-10-30, `internal/collect`). `argus checks <alerts|certs|backups|all|list>` runs
   them once; `argus worker` runs the enabled ones on intervals. Each is off until `ARGUS_COLLECT_<NAME>=1`.
   - `alerts`: reads Alertmanager (`/api/v2/alerts`) or Prometheus (`/api/v1/alerts`) per site, optional bearer token
     from a file, TLS checks on, no redirects. Writes `alerts-firing.json` atomically.
   - `certs`: TLS handshake to `host:port` targets and PEM files. Expiry, issuer, SANs, name mismatch, chain trust.
     Records a `cert-check` run in `ARGUS_DB`.
   - `backups`: files and directories in a JSON sets file. Age, size, size against the median of the last 7 checks.
     Records a `backup-check` run in `ARGUS_DB`.
   - Tests run each collector against local fixtures and render the output on the existing pages.
   - Hardened example units: `examples/systemd/argus-checks@.service` and three timers.
   - Later: S3-compatible object listing for backups (no client is vendored yet), Matrix alert rooms and the
     Alertmanager webhook as alert sources, shadow parity against the reference installation.

5. **Information architecture v2** (2026-10-30, ADR-0016 D3). The sidebar is seven sections (Today, Review,
   Operations, Incidents, Work, Delivery & Estate, Argus) with Settings and Help in the footer; a section's pages are
   tabs and its landing tab leads with "Needs you" and an estimate. **Review** is one inbox for every decision (task
   proposals, action approvals, lessons, ideas, chat requests, problem links), acting through the existing handlers.
   Today absorbed the Overview; old URLs answer 301. SEO and design reviews are removed from the product (OD-8). Map:
   [argus-2/ia.md](architecture/argus-2/ia.md).

6. **Task page: live agent progress, failure block, Plan again** (2026-11-03, web side). The agent panel shows the
   job's own steps (Queued → Reading the task → Gathering context → Writing the plan → Done; the run: Reading the
   plan → Working → Writing the report), "Now: <activity>", elapsed time, tool calls and "last activity N s ago",
   refreshing itself only while a job runs. A failed intake or run shows a plain cause by class (login rejected,
   egress, timeout, could not start, no steps, no usable output) with what to do, then the exact redacted message,
   Claude's `is_error`, the exit code, the run and the time. "Plan again" (admins, CSRF) writes an `agent-replan`
   board request. The job side is not in Go yet: see the task-agent port requirement below.

7. **Data sources P2: discovery and the routing catalogue** (2026-11-03, ADR-0017 "P2 (as built)"). `argus sources
   discover` lists what every read-only source holds (GitLab, Kubernetes, Grafana Loki/Prometheus/dashboards, Sentry,
   Elasticsearch/OpenSearch) within budgets; a deterministic matcher proposes routes per service with a reason;
   admins confirm, reject or edit them through the JSON API `/api/v1/sources/catalogue` (the server-rendered
   Catalogue page of PR #25 was removed: its UI belongs to the Next.js frontend, Next item 5); the agent reads `argus sources
   routes <service>` and `argus sources unmapped`. The Outline two-way table waits for the publisher port (below).

8. **Notifier and alerts parity** (2026-11-08). `internal/notify` + `argus notify [-json] <source>` and `argus notify
   format text|settings|ratelimit`: the reference formatter's card (level emoji, title, one sentence, five facts, link,
   grey meta), its cleaning of untrusted text and its secret scan, byte-identical on synthetic fixtures; sent to Matrix
   (`ARGUS_NOTIFY_MATRIX=1`, only to `MATRIX_NOTIFY_ROOM`, refused for watched/alert rooms, `m.mentions` empty), else
   Telegram, else a webhook. `argus settings apply` posts the settings-change notice through it. The alerts snapshot
   now matches the reference job: severity "none" and instance = instance, else job, else "-" when the label is
   missing, activeAt as the source gave it, the source's order, the same field order and indentation. Not ported yet:
   the notice styles A/B/C (threads, edited cards, digest) and the run notices that read a recorded run.

9. **Task board sync with Jira** (2026-11-08). `internal/board` + `argus board sync [-full] [-dry-run]` (root;
   `examples/systemd/argus-board-sync.{service,timer,path}`, off until `ARGUS_BOARD_SYNC=1`): consumes the board
   request files (create, move, comment, assign, sync, settings, board-move), pushes the outbox to Jira (create,
   transition by workflow name, label, comment, assign; Jira wins a conflict), pulls changed issues and a nightly
   full reconcile, writes `board-status.json`. Same request files, `board.db` rows, Jira calls and status file as
   the reference job on a synthetic scenario against its fake Jira (four runs: creates, refusals, moves, comments,
   assignment, a rejected priority, a rate limit, a deleted issue, a refused token). `-dry-run` logs the writes and
   changes nothing. The paths and the label prefix are configurable. Not ported yet (their requests are refused with
   a reason): GitLab issue tasks, per-user Jira connections, the Jira agile board mirror and its moves, the fixer
   column, approval proposals, lessons, the task agent and its chat, Matrix requests, the board notices job.

## Self-contained deployment (ADR-0021, operator decision 2026-11-08)

Goal: the images work at another company after the data sources are set; nothing on the host is needed. Phases:

| phase | what | state |
|---|---|---|
| **P0** | ADR-0021 and this plan; embedded migrations + `argus migrate` + the one-volume layout (`ARGUS_LAYOUT=volume`), the demo on the same migrations; the clean-install test and gate (`demo/clean-install/`, backlog `docs/CLEAN-INSTALL.md`); the worker as in-process scheduler with in-volume settings and secrets (no host root jobs); the data-source registry with "Connect X to enable" states and Settings → Data sources; the `marketing` module switch (off by default) | in progress |
| P1 | `argus import <dir>`: one-time import of an earlier installation's data into the volume | planned |
| P2 | evidence jobs as worker jobs (alerts, incidents, CI/CD watch, inventory, capacity, credentials) | in progress: incidents, inventory + VM state + pulse, capacity |
| P3 | decision jobs as worker jobs (board, proposals, review, notices, chat intake); every model prompt through `style.Prepend` | planned |
| P4 | the model runner inside the worker (no host user); the runner prepends the writing style to every prompt | planned |
| P5 | host access only through access-broker machine identities (ADR-0019, ADR-0020) | planned |
| P6 | jobs of the optional modules (marketing: search health, crawler traffic, search console, CDN); `ARGUS_LAYOUT=legacy` removed after the cut-over | planned |

P0 built so far: ADR-0021 (PR #11); `internal/migrate`, `argus migrate`, `ARGUS_LAYOUT=volume`, `deploy/` (PR #12);
the clean-install test and backlog `docs/CLEAN-INSTALL.md` (PR #13); `argus worker` as the scheduler in the volume
layout (`internal/worker`): the collectors, Settings -> General (`settingsapply`) and Settings -> Claude requests
(`claudetoken`) applied by the worker within 3 s, job states in `worker-status.json`; the data-source registry of
features (`internal/needs`): "Connect X to enable this page" on pages, `{"notConfigured"}` on API routes, and worker
jobs that name the missing source. Secrets in the volume are encrypted
with the master key (PR #18: `internal/secret`, `argus secrets rekey`). In the volume layout the worker also consumes
the Kubernetes, catalogue-action and board requests, runs discovery hourly once a source is enabled for the agent,
and runs the board sync with the Jira entry of Settings → Data sources (PR #20). Still open in P0: the per-user Jira
connections (`jira-connect`), the Outline publisher (`outline-requests`) and the GitLab issues tracker
(`gitlab-issues`) have no Go consumer yet (their requests wait); kubeconfigs are not sealed with the master key. The marketing module switch is built (PR #15): `ARGUS_MODULES=marketing` or
Settings → General; off by default, on in the demo.

Writing style (2026-11-08): `internal/style` holds the default style (embedded `default.md`, about 80% ASD-STE100
and a fixed layout); an admin edits it in Settings → Writing style (`/admin/writing-style`,
`GET /api/v1/settings/writing-style`). The worker applies the request to `etc/writing-style.md` within 3 s (legacy:
root's `argus settings apply`). **REQUIRED for every model run** that P3 and P4 port (task agent, RCA, proposals,
ideas, chat): build the prompt with `style.Prepend(prompt)` (`style.Current()` is the custom text, else the default).
Tasks the board creates from incidents, RCA and CI/CD findings already start with a goal line, facts and checkbox steps.

Model runtime (2026-11-08, P3/P4): `internal/model` with the providers `cli` (the Claude CLI in the `argus` image,
the Settings -> Claude token) and `api` (an `anthropic` data source), `ARGUS_MODEL_PROVIDER`; every call through
`style.Prepend`, the usage switch, the egress guard, limits, the secret scan and a `runs` record; `argus claude run`.
Every ported model job calls it (ADR-0021 "As built (P3/P4)").

Marketing is an optional module from P0 on: hidden from the navigation and the API unless `ARGUS_MODULES` names it.

## Next

1. More collectors: credential expiry; S3 listing for backups. Host estate: the host shell part of the
   inventory and VM state (facts, snapshots, unit memory samples, live nginx maps, the VM refresh request) waits for
   the access broker (ADR-0019, ADR-0020).
2. Knowledge promotion as a Review kind, with the eval harness (ADR-0016 D8).
3. **Task-agent port requirement** (the intake/run dispatcher and runner; the web side above already reads these):
   - Handle the board request `agent-replan` exactly like `agent-answers`: admins only; refuse with a clear reason when
     the task is not in the agent column for an agent-enabled user, a job is already running for it, Claude usage is
     off, the egress guard blocks, the task budget is spent or the daily planning cap is reached; keep the saved
     instructions and answers; a second request while one is queued changes nothing; log "Plan again requested by …".
   - Progress writes: while a model job runs, write `<data>/task-agent/out/<key>.<intake|run>.progress.json` atomically
     (0644): `phase`, `started`, `step` (`prepare`, `reading`, `gathering`/`working`, `writing`, `recording`), `tools`,
     `last_tool`, `activity`, `updated`, at most once a second. Read Claude's `stream-json` events; the run log keeps
     only the final result object (the same as `--output-format json`). The activity line comes only from the tool name
     and validated sub-command words of the read-only tools: never arguments, output, prompt text or a secret.
   - Error classes and detail: store the last failure as `agent_tasks.error_detail` JSON (`phase`, `class`, `raw`
     redacted to 300 characters, `is_error`, `exit_code` with 124 = time limit, `run_id`, `at`). Classes, in this
     order: `limit` (stored `claude-limit:` prefix), `auth` (organisation disabled, subscription access, invalid API
     key / bearer / token, OAuth, expired or revoked token, unauthorised, 401, not logged in, `/login`), `egress` (403,
     request not allowed, unsupported country or region, connection refused/reset, DNS errors), `timeout`, `start`,
     `no-steps`, `output`, `other`. The web side mirrors them in `agentErrClass`.
   - Automatic re-plan after a token replacement: when the Claude token was replaced and its test passed (ok or usage
     limit), re-queue the intake of every task in scope whose last failure is `auth` or `egress` and happened before
     the replacement, once per replacement; log "Re-planning after the Claude token was replaced". Generic failures
     stay failed.
4. **Data sources in the service pages (Outline two-way), with the documentation publisher port** (ADR-0017 D7, R1):
   - Each service page gets a section between `<!-- argus:sources:start -->` and `<!-- argus:sources:end -->`; when the
     markers are gone, the section under the heading `## Data sources` is used, else the section is appended.
   - One table, columns `Source | Type | Where | Status | Why`: confirmed routes first, then the best proposals (at most
     4 per source and kind, 40 rows), the reason with the confidence for discovered rows, "; not seen by the last
     discovery" for stale ones.
   - Two-way: rows Argus wrote and nobody changed follow the DB; a row a person edited or added is kept verbatim and fed
     back as `confirmed` (`rejected` when they wrote rejected in Status), origin `human`; a row a person deleted
     becomes `rejected` with a tombstone so it is not proposed again; feedback is applied once per page version.
   - A table that does not parse is an error on the service (shown by the catalogue API and in the publisher's
     status), the DB copy is kept, nothing is overwritten. Preview mode never writes.
5. **Catalogue UI in the Next.js frontend** (operator decision 2026-11-03: Argus moves to a Next.js frontend over the Go
   JSON API; this supersedes ADR-0016 D2's "server-rendered Go UI; no Next.js", to be recorded in a new ADR). The
   Data sources catalogue screen is built there on `/api/v1/sources/catalogue` (summary, routes with service/type/status
   filters and paging, unmapped, discovery runs, action state; POST confirm/reject/edit and Discover now with the
   `X-CSRF-Token` from `/api/v1/session`): the status, the last discovery per source with Discover now, routes per
   service with status badges, origin, confidence and reason, the unmapped list.
