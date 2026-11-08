# ADR-0021: Self-contained deployment

Status: accepted, 2026-11-08. Phase P0 is built in steps (see "Phases"). Builds on ADR-0001 (adoption by configuration
only), ADR-0002 (adapter boundary), ADR-0016 D9 (one install per company), ADR-0017 (data sources) and ADR-0018
(Next.js frontend over the Go JSON API). ADR-0020 is reserved for machine identities on the access broker.

## Decision taken with the operator

- **S1:** "Argus must work without any dependency. If I move the Docker image to a new company, it works after
  setting the data sources."
- **S2:** P0 comes first. The marketing features (search health, crawler traffic, search console, CDN) become an
  optional module.

## Context

Argus started as the second renderer of an earlier installation. Its pages are ported, but most of the data behind
them is not produced by Argus:

- The API reads about 50 state files and about 77 tables. Jobs of the earlier installation write all of them. A fresh
  install shows empty pages.
- The only description of the table structure in this repository is the demo generator's `schema_*.sql`. The real
  databases were created by the earlier installation.
- Saves on Settings pages write request files to `settings-in/`. A root systemd path unit on the host applies them
  (`argus settings apply`, `argus secrets apply-claude`). The board sync is also a host root unit. A container image
  alone cannot do these steps.
- The model runs as a host user, and server access goes through host SSH scripts.

So an image moved to another company starts, but most pages are empty and every save waits for a host job that does
not exist.

## Decisions

**D1. Two images, three services, one volume.** Image `argus` holds the Go binaries (`argus-api` and the multi-call
`argus`: `worker`, `migrate`, the CLI). Image `argus-web` holds the Next.js frontend. A compose file runs three
services: `api`, `worker` (the same `argus` image) and `web`. All state lives in ONE volume mounted at
`/var/lib/argus`:

| path in the volume | holds |
|---|---|
| `ops.db` | evidence database (runs, findings, inventory, CI/CD, ...) |
| `board/board.db` | decisions and tasks |
| `auth/auth.db` | users, sessions, 2FA |
| `*.json` | state files the jobs write and the API reads (same names as before) |
| `settings/` | saved settings (no secret); `settings/in`, `settings/out` while the request-file flow remains |
| `secrets/` | secrets, each encrypted with the master key (D4) |
| `etc/` | data files and data-source registry (`etc/sources`, `etc/kube`) |
| `reports/` | merged reports |
| `keys/` | the 2FA key and the master key file, when not given from outside |

`ARGUS_LAYOUT=volume` selects these defaults (`internal/config` `VolumeDefaults`; every path key still overrides).
`deploy/Dockerfile` (targets `argus`, `web`) and `deploy/compose.yml` are the reference build and compose file. `ARGUS_LAYOUT=legacy` (the default
until cut-over) keeps today's paths so that existing installations do not change.

**D2. One schema, owned by Argus.** The table structure is embedded in the binaries as numbered migrations per
database (`internal/migrate`, `migrations/<db>/NNNN_<name>.sql`). `argus migrate [-status] [-dry-run]` applies them.
`argus-api` and `argus worker` run the same step at start. Rules:

- Idempotent: an applied migration is recorded in `argus_schema_migrations` (version, name, sha256, time) and is
  never applied again. A recorded migration whose sha256 changed is an error.
- Refuses a downgrade: a database that records a version above the newest one the binary knows is not opened for
  writing, and the process stops with a message that names the versions.
- Adopts an existing database: the baseline migration only creates what is missing (`IF NOT EXISTS`) and adds
  missing columns, so a database created by the earlier installation is adopted without a rewrite.
- A read-only database (a pilot that mounts another installation's data read-only) is not migrated; the API logs one
  line and serves what is there.
- The demo generator creates its databases with the same migrations, so the demo, the tests and a real install have
  one schema.

**D3. The worker is the scheduler.** `argus worker` runs every ported job in-process on its interval (replaces the
systemd timers). A job is enabled only when the data sources it needs are configured (D5); a job without its sources
is listed as "not configured", not as failed. One job runs at a time per name; a run that overlaps the next tick is
skipped and recorded. Each run records start, end, result and a one-line reason in `ops.db` (`runs`), the same table
the pages already read.

As built (P0): a job that is not ready is looked at again after 15 s, not after its interval, so a source connected
in Settings is used at once. Request consumers in the volume layout: Settings → General, Claude, Data sources,
Kubernetes (`kube-requests`), catalogue actions (`source-actions`), board requests and the board sync (Jira from the
Data sources entry; the board works locally without one), and hourly discovery (`sources-discover`) once a source is
enabled for the agent. No Go consumer yet for the per-user Jira connections and the Outline publisher; their
requests wait and their pages say so.

As built (P1): the board sync also runs the GitLab issues tracker (ADR-0009, ADR-0010): the Settings -> Integrations
requests (`gitlab-issues`, consumed by the worker within seconds), and push and pull of GitLab tasks (create, move by
list labels, comment, assign by the user map, the issues pull with a nightly reconcile). The token is either pasted
on the card (stored in `etc/gitlab-issues.token`, sealed with the master key) or, when the card's token is left
empty, the credential of the Settings -> Data sources GitLab entry with purpose `tracker`. A Jira data source with a
user name signs in with basic auth.

**D4. Settings and secrets are saved and applied inside the volume.** In `ARGUS_LAYOUT=volume`:

- the API validates a save and writes it to `settings/<area>.json` (atomic, 0600). A secret is encrypted with
  `ARGUS_MASTER_KEY` (32 bytes, base64) or, when it is not set, the key file `keys/master.key` (created at the first
  start, 0600) and written to `secrets/<area>/<name>.enc`. A secret is never shown back (the page shows the last four
  characters), never logged, never in a status file.
- The worker sees the change (a change counter in `ops.db` plus a scan every few seconds) and applies it within
  seconds: the Go `settingsapply` and the Claude secret apply run inside the worker. The result goes to
  `state/settings-status.json`, the file the Settings pages already read.
- As built: `internal/secret` seals each value with AES-256-GCM under the master key, bound to its name (additional
  data), stored as `argus-enc:v1:<base64>`: data-source credentials (`etc/sources/<type>/<name>.cred`), the Claude
  token's value in the Claude env file (and its backups) and the Matrix token file. Key: `ARGUS_MASTER_KEY`, else
  `ARGUS_MASTER_KEY_FILE` (volume default `keys/master.key`, created 0600 at the first start of the api or the worker
  when the volume holds no secret yet). With secrets present and no key, the api and the worker refuse to start
  (`migrate.OnStart`) and say which key to set; a value is never in that message. `argus secrets rekey` seals
  plaintext values and moves values sealed with `ARGUS_MASTER_KEY_OLD` to the current key; `argus secrets new-key`
  prints a fresh key. A request file still carries a pasted secret for the seconds until the worker consumes it
  (0600, deleted on read), as in the legacy flow.
- As built: Settings → Writing style saves `settings/in/writing-style.json` (the text, or a reset; plain UTF-8 text or
  Markdown, at most 16 KB). The worker job `writing-style-apply` validates it and writes `etc/writing-style.md` and the
  live copy `settings/out/writing-style.md` (status `writing-style-applied.json`, a line in `audit.log`); a reset
  removes both. The legacy applier (`argus settings apply`, `argus-settings.path`) does the same. `internal/style`
  reads it: `style.Current()` is the custom text, else the default embedded in the binary. **Every model run (P3
  task agent, RCA, proposals, chat; P4 runner) must build its prompt with `style.Prepend`.**
- No host root job and no `settings-in/` / `settings-out/` hand-off in this mode. The file mode stays available
  behind `ARGUS_LAYOUT=legacy` until the pilot is cut over.

**D5. Data-source registry drives features.** Every page, API route and job declares the data sources it needs
(`gitlab`, `jira`, `alertmanager` or `prometheus`, `loki`, `kubernetes`, `matrix`, `outline`, `sentry`, `claude`,
`teleport`, ...). Settings → Data sources lists every supported type, which are configured and which features each
one enables. A page whose source is missing shows a "Connect X to enable" state built from the existing empty-state
pattern (DESIGN.md §10): never an error and never a blank page. The API answers such a route with HTTP 200 and
`{"notConfigured": {"sources": [...]}}`.

  As built (P0): `internal/needs` holds the kinds (`gitlab`, `jira`, `alertmanager` (Alertmanager or Prometheus),
  `grafana`, `kubernetes`, `outline`, `elasticsearch`, `sentry`, `matrix`, `claude`, `teleport`) and the needs of
  pages (`needs.Pages`, by path prefix, the longest wins), API GET routes (`needs.APIs`) and worker jobs
  (`needs.Jobs`); a need is satisfied by any one of its kinds. A kind is configured when Settings → Data sources holds
  an entry of its type, when the configuration sets its key explicitly (automation), or, for Jira and Claude, when
  their settings file holds the key (presence only; a value is never read back). `GET /api/v1/needs[?page=]` gives the
  frontend each kind's state; a gated page calls `notConfigured(path, title)` (`components/NotConfigured.tsx`) first,
  and a test keeps the page list and the frontend in step. The gate is active only in `ARGUS_LAYOUT=volume` outside
  the demo: a legacy installation keeps its host jobs and host configuration, so its pages stay as they are.

  Settings → Data sources in the volume (P0): the worker applies the page's requests (`sources-requests`, every 3 s,
  the same `internal/sources` code as root's `argus sources apply`) and re-checks every source every 6 hours
  (`sources-check`). The page lists every kind with its state (tested OK, failing with the check's reason,
  configured outside the page, not connected) and the pages it enables. New types for Argus's own jobs:
  `alertmanager` (Alertmanager `/api/v2/alerts` or Prometheus `/api/v1/alerts`, login optional; the alerts collector
  reads every entry with its own token, so `ARGUS_ALERTS_SOURCES` is optional), `jira` (purpose tracker) and `matrix`
  (purpose notify). `ARGUS_SOURCES_ALLOW_HTTP=1` lets plain http through, for test installations with fake services.

**D6. Optional modules.** A module groups pages, API routes, jobs and a sidebar group. `marketing` (search health,
crawler traffic, search console, CDN) is the first one and is off by default. `ARGUS_MODULES` (comma-separated)
switches modules on; an admin may switch a module on or off in Settings → General (stored in `auth.db`
`auth_settings` as `module.<key>`), which wins over `ARGUS_MODULES` and applies at once, without a restart; "Use
the configured default" drops the admin's choice. A module that is off is hidden from the navigation, its API
routes answer 404 and its jobs do not run.

**D7. Company specifics only in configuration.** Names, domains, groups, rooms, environments and labels come from
`argus.env`, data files or Settings; compiled-in defaults are neutral examples (already true for the site values,
ADR-0001). New code must not add a company fact anywhere else.

**D8. A clean-install test is a gate.** `demo/clean-install/` starts the compose file with an EMPTY volume and stub
HTTP servers for GitLab, Jira, Alertmanager and Matrix (synthetic data), signs in, crawls every page and submits
every safe action, and reports per page: `works`, `not-configured` (the D5 state) or `broken`. The current list of
broken pages is committed as the backlog (`docs/CLEAN-INSTALL.md`); the gate fails only when a page gets worse than
that list.

## Phases

| phase | what | state |
|---|---|---|
| P0 | this ADR; migrations + `argus migrate` + volume layout; clean-install test; worker scheduler + in-volume settings/secrets; data-source registry + "Connect X" states + Settings → Data sources; marketing module switch | in progress |
| P1 | `argus import <dir>`: one-time import of an earlier installation's databases, state files, settings and secrets into the volume (dry run first, a report of what was skipped); GitLab issue tasks in the board sync | in progress |
| P2 | port the evidence jobs (alerts, incidents, CI/CD watch, inventory, capacity, credentials) to worker jobs | in progress |
| P3 | port the decision jobs (board, proposals, review, notices, chat intake) | documented |
| P4 | the model inside the worker: the runner, the prompt store, the budget and egress guards, no host user; every prompt through `style.Prepend` | documented |
| P5 | host access only through access-broker machine identities (ADR-0019, ADR-0020), no host SSH scripts | documented |
| P6 | the optional modules' jobs (marketing first) and the removal of `ARGUS_LAYOUT=legacy` after the cut-over | documented |

As built (P1): `argus import [-dry-run] [-force] [-etc DIR] [-env FILE]... [-reports DIR] [-totp-legacy-label L]
<state dir>` copies `ops.db`, `board/board.db` and `auth/auth.db` consistently (`VACUUM INTO` from a scratch copy; the
source is only read) and migrates them; copies the `*.json` state files, `settings-in/` (without pending request
folders) and `settings-out/`, and the reports; copies the 2FA key and re-encrypts the 2FA secrets written under the
earlier label (`TOTP_LEGACY_LABEL` is not needed afterwards); turns the integrations of the env files and the etc
folder (GitLab read-only token, the GitLab issues connection, Jira, Outline, Matrix, Alertmanager / Prometheus,
Grafana sites, Kubernetes clusters, the Claude token) into Settings -> Data sources entries and volume files sealed
with the master key (created when missing), then seals every secret of the volume. It prints names only, refuses a
volume that already holds data unless `-force`, and a second forced run gives the same result. The non-secret site and
integration keys of the env files (and `JIRA_PROJECT` of `jira.env`, a time zone from a `TZ`-like key) go to
`etc/imported.env`, which the volume layout reads below `argus.env` and the environment; a GitLab issues connection
on another host than `GITLAB_URL` gets `etc/gitlab-issues-allowed-hosts` with both. Other databases of the state
folder are reported as skipped with their size; `-with-googlebot` copies the crawler log database. `-settings-only`
writes only the carried settings, for a volume imported by an earlier version. With a Jira data source the board sync
takes only the address and the credential from it: the Jira settings come from `etc/jira.env` (owned by the user the
worker runs as), and the configured `JIRA_PROJECT` wins only when it is set explicitly.

As built (P2): `internal/incidents` and `argus incidents tick|attach|backfill` port the incident records job
(parity with the earlier job on synthetic fixtures: `internal/incidents/testdata/golden.json`). In the volume the worker
applies the incident page's requests (`incident-actions`) within seconds and auto-resolves every 5 minutes once an
alerts source is configured (job `incidents`, "not configured" without one); a changed incident with a Matrix thread
gets a note in that thread (notify room only). Site names come from `OBS_SITES`, host names in text from
`ARGUS_INCIDENT_HOST_SUFFIXES` / `_PREFIXES`, customer-facing subjects from `ARGUS_CUSTOMER_FACING`, symptom words in the team's language from `ARGUS_INCIDENT_FAMILY_WORDS`; the own, GitLab and
chat hosts are never subjects. A log-RCA finding attaches only to the primary alert's host; hosts named as covered by a
known incident ("Known incident #N") are never candidates. Not ported: hub service tiers, the prompt history.

As built (P3/P4, the model runtime): `internal/model` is the one way a job calls a model. `ARGUS_MODEL_PROVIDER=cli`
(the default) runs the Claude CLI that the `argus` image now holds (`/usr/local/bin/claude`, the pinned native musl
build; `--build-arg CLAUDE_CODE_VERSION=` leaves it out) with the token of Settings -> Claude, a clean environment, a
throwaway HOME and config folder in `/tmp` and no saved session; `api` sends one Messages API request with the key of
a Settings -> Data sources entry of type `anthropic` (its check looks at the key's shape only and never calls the API,
so no call leaves a blocked egress country). Every call: `style.Prepend`, the Claude usage switch and the egress
guard (a skip, not a failure), a budget, turn and time cap (`ARGUS_MODEL_MAX_BUDGET_USD`, `ARGUS_MODEL_MAX_MINUTES`;
exit 124 at the time limit), the secret scan over the error, the record and the log, a `runs` row in `ops.db` and a
log file in `ARGUS_MODEL_LOG_DIR`. `argus claude run` makes one call to check the provider. The clean-install test
uses the api provider against the stub's `/anthropic/v1/messages`; the unit tests use a fake CLI.

As built (P2): `internal/selfcheck` ports the self-check job: the worker job `selfcheck` (every 5 minutes, always on)
checks every worker job against its interval (a job without its source is "not configured", never a failure), the
API's health address (`ARGUS_SELFCHECK_API_URL`), the alerts snapshot while its collector runs, the credential
status and expiry files once they are written, the volume's free space, the databases' integrity (`PRAGMA
quick_check`, hourly), the master key (present, opens every sealed secret; plaintext secrets are a warning), the
data sources whose last check failed, the state backup (when `backup-status.json` exists), own failures of the last
24 h and the Claude usage switch. It writes `selfcheck.json`. After `ARGUS_SELFCHECK_PAGE_AFTER` (default 1 h) of
FAIL it sends one notice through `internal/notify` and one at recovery; while not failing it pings
`ARGUS_DEADMAN_PUSH_URL` (an external push monitor, the dead-man switch). Differences from the earlier job: worker
jobs replace systemd timers; the chat bridge, Matrix proxy and incident queue checks are gone (not Argus parts); the database,
key and data-source checks are new; a missing backup status is not a failure (no backup job yet).

As built (P2): `internal/credexpiry` ports the credential expiry job. Worker jobs: `cred-expiry` (daily) and
`cred-requests` ("Check now" of Settings -> Credentials, `settings-in/requests/cred-expiry.req`, state in
`settings-out/run-requests.json`) check every Settings -> Data sources entry with a credential and Argus's own
credentials (the Claude token, the Matrix token file, the GitLab issues token, the sign-in client secret, the push
monitor address) with one read-only call where the service can tell (GitLab, Grafana, Jira, Matrix, Outline). Only
metadata goes to `credentials.json` (scrubbed for every value read); expired, rejected and close (`ARGUS_CRED_CRIT_DAYS`)
credentials get an incident (`CRED|<id>|cred-expiry`, table `credential_alerts`, migration ops 0003) and one notice
per band; the incident resolves when the credential is OK again. Thresholds: `ARGUS_CRED_WARN_DAYS`, `_CRIT_DAYS`,
`_ROTATE_DAYS`, `_TODAY_DAYS`. `credcheck` (hourly) writes `cred-status.json` from the data sources' last checks, the
Claude login and the notify switch. Differences from the earlier jobs: the list comes from the data sources, not from
a hand-kept file; a credential's set date is its file's change time (no env-file backups); neutral how-to steps; no
`credentials` snapshot table, rotation proposals or Today items yet; credcheck makes no calls of its own (the
`sources-check` job does).

## Consequences

- An image moved to another company starts with empty databases of the right shape, a working sign-in, Settings
  pages that save, and pages that either work or say which source to connect.
- Existing installations keep their paths and their host jobs until they switch to `ARGUS_LAYOUT=volume`. The pilot
  that mounts another installation's data read-only keeps working (D2 read-only rule).
- The demo generator and a real install share one schema; a column used by the code must exist in a migration, and
  the clean-install test finds a query against a missing column.
- The master key is now a secret to back up together with the volume. Losing it makes the stored secrets
  unreadable (they must be entered again); the databases and settings stay usable.

## Alternatives rejected

- **Keep the host root units and document them.** Breaks S1: the image does not work alone.
- **An external database server.** One more dependency to run; SQLite in one volume is enough for one company.
- **Schema created by each package at first use (today's `CREATE TABLE IF NOT EXISTS` blocks).** Scattered, without a
  version, no downgrade check; the packages' blocks stay only as a guard until they are moved into migrations.
