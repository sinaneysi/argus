# Configuration schema

Status: proposal, 2026-10-21. Decision record: [ADR-0001](adr/0001-config-only-adoption.md),
[ADR-0006](adr/0006-environments-as-data.md).

Goal: another company installs Argus by writing **one `.env` file, a secrets directory and a few
data files**, never by editing code, prompts or units.

## 0. What is built (2026-10-30, `internal/config`, `argus config check|show|schema`)

The loader exists in Go. `argus` and `argus-api` both read every setting through it. The rest of this document is
still the target design.

- **Format.** `argus.env` is `KEY=value`, one per line. Blank lines and lines starting with `#` are skipped.
  One pair of matching quotes around a value is removed. Nothing is expanded (`$HOME` stays `$HOME`). A key set
  twice is an error. The parser uses the Go standard library only.
- **File.** `-file`, else `$ARGUS_CONFIG`, else `/etc/argus/argus.env`. The default file may be missing; a file named
  by `-file` or `ARGUS_CONFIG` may not.
- **Precedence.** Built-in default, then the file, then the process environment. This differs from §2 on purpose:
  containers and systemd `Environment=` lines configure Argus through the environment today, so the environment
  must win. Admin runtime settings (§2 step 3) are not read yet. In `ARGUS_LAYOUT=volume` one more layer sits
  between the default and the file: `ARGUS_ETC_DIR/imported.env`, the site and integration settings `argus import`
  carried from an earlier installation (`config.Importable`: names, time zone, `GITLAB_URL`, Matrix homeserver and
  rooms, `JIRA_PROJECT`, ...). Only those keys are read from it, never a secret, a path, a listen address or a sign-in
  setting; a value's source shows as `imported`.
- **Old names.** Each key has one canonical name. An old name is read when the canonical one is not set, with a
  one-line warning `OPS_DB is deprecated, rename it to ARGUS_DB`. When both are set, the old one is ignored with a
  warning. Aliases today: `ORG_NAME` → `COMPANY_NAME`; `OPS_PUBLIC_URL` → `ARGUS_PUBLIC_URL`;
  `OPS_STATE_DIR` and `OPS_DATA_DIR` → `ARGUS_STATE_DIR`; `OPS_DB`, `OPS_BOARD_DB`, `OPS_AUTH_DB` → `ARGUS_DB`,
  `ARGUS_BOARD_DB`, `ARGUS_AUTH_DB`; `OPS_ALERTS` → `ARGUS_ALERTS_FILE`; `OPS_CRED_STATUS` → `ARGUS_CRED_STATUS_FILE`;
  `OPS_SELFCHECK` → `ARGUS_SELFCHECK_FILE`; `OPS_READINESS_DATA` → `ARGUS_READINESS_FILE`; `OPS_CRED_EXPIRY` →
  `ARGUS_CRED_EXPIRY_FILE`; `OPS_BACKUP_PLANS` → `ARGUS_BACKUP_PLANS_DIR`; `OPS_BACKUP_REF` → `ARGUS_BACKUP_REF_DIR`;
  `OPS_REPORTS_DIR` → `ARGUS_REPORTS_DIR`; `OPS_SETTINGS_IN`/`_OUT` → `ARGUS_SETTINGS_IN`/`_OUT`; `OPS_SECRET_IN` →
  `ARGUS_SECRET_IN`; `OPS_STATIC` → `ARGUS_STATIC_DIR`; `OPS_REPORT_BIND` → `ARGUS_BIND`; `OPS_TRUSTED_PROXIES` →
  `ARGUS_TRUSTED_PROXIES`; `OPS_STALE_HOURS` → `ARGUS_STALE_HOURS`; `OPS_ADMIN_USER`, `OPS_ADMIN_INITIAL_PASSWORD`,
  `OPS_ADMINS`, `OPS_IMPORT_HTPASSWD`, `OPS_DEBUG_SQL`, `OPS_NO_QUERY_CACHE` → the same names with `ARGUS_`.
  Names that were never `OPS_*` keep their name (`OIDC_*`, `SESSION_*`, `MFA_*`, `TOTP_KEY_FILE`, `FEATURE_TODAY`,
  `FEATURE_PROBLEMS`, `DEMO_*`, and the site display keys such as `GITLAB_URL`, `OBS_SITES`, `OBS_SITE_<ID>_LABEL`).
- **argus-api.** The web app reads every setting through the loader (2026-10-30). It prints each deprecation
  warning once at start. An invalid configuration stops it before it listens, with every problem listed. A test
  fails when code in `cmd/argus-api`, `internal/app`, `internal/auth` or `internal/oidc` reads the environment
  directly, or names a key that is not in the schema.
- **Defaults changed for argus-api.** The collector files (`ARGUS_ALERTS_FILE`, `ARGUS_CRED_STATUS_FILE`,
  `ARGUS_SELFCHECK_FILE`, `ARGUS_READINESS_FILE`) and the data directory of the capacity, CVE, CI/CD and design pages
  now default to `ARGUS_STATE_DIR` (`/var/lib/argus`). Before, the web app used `/data/...` when the key was unset.
  A container that mounts the state at `/data` sets `ARGUS_STATE_DIR=/data` (the demo image does). An installation
  that set every path, as the demo did, sees no change. `ARGUS_BOARD_DB` defaults to `board/board.db` next to
  `ARGUS_DB`, as the web app always did.
- **Required.** `COMPANY_NAME` always; `ARGUS_PUBLIC_URL` unless `DEMO_MODE=1`. Before, the web app fell back to
  example values; now it refuses to start.
- **Validation.** Every problem is reported at once, one line each: required keys (`COMPANY_NAME`;
  `ARGUS_PUBLIC_URL` unless `DEMO_MODE=1`), absolute paths, https URLs without user, query or fragment, integers and
  ranges, `0|1` booleans, `host:port`, IANA time zones, user names, proxy addresses, and the three `OIDC_*` keys
  together. An unknown `ARGUS_*` key in the file is a warning (a likely typo).
- **Output.** `argus config check` prints every key as `KEY=value  # source` (`default`, `file`, `env`, or
  `env (OPS_DB)` for an alias). Secrets print as `******` when set. Exit 0 = valid, 1 = invalid, 2 = usage error.
  `-q` prints only warnings and errors. `argus config show` prints the same table; `-json` prints one object per
  key with `value`, `source` (`default`, `file`, `env`, `alias` or `unset`) and the `alias` name.
- **Schema file.** [`apps/api/config/schema.json`](../../apps/api/config/schema.json) lists every key, its type, default, range,
  aliases and whether it is secret. `argus config schema` prints it; a test keeps the file current.
- **Example.** [`apps/api/config/argus.example.env`](../../apps/api/config/argus.example.env); a test keeps it valid.
- **Collectors** (`argus checks`, `argus worker`). Each is off until its flag is 1: `ARGUS_COLLECT_ALERTS`,
  `ARGUS_COLLECT_CERTS`, `ARGUS_COLLECT_BACKUPS`. A flag that is on needs its inputs, or `config check` fails:
  `ARGUS_ALERTS_SOURCES` (`name=url`, ...); `ARGUS_CERT_TARGETS` or `ARGUS_CERT_FILES`; `ARGUS_BACKUP_SETS_FILE`
  (JSON, see `apps/api/config/backup-sets.example.json`). Other keys: `ARGUS_ALERTS_TOKEN_FILE`, `ARGUS_ALERTS_CA_FILE`,
  `ARGUS_ALERTS_TLS_VERIFY` (default 1), `ARGUS_ALERTS_TIMEOUT`, `ARGUS_CERT_CA_FILE`, `ARGUS_CERT_WARN_DAYS` (30),
  `ARGUS_CERT_FAIL_DAYS` (14), `ARGUS_CERT_TIMEOUT`. The alerts collector writes `ARGUS_ALERTS_FILE`; the other two
  write a run into `ARGUS_DB`. Nothing else is written.
- **Not yet:** `argus config render`, per-module checks (`--module`), secrets from `ARGUS_SECRETS_DIR`, and the
  JSON data files.

### One volume and migrations (ADR-0021, 2026-11-08)

- `ARGUS_LAYOUT=volume` (the `deploy/Dockerfile` image sets it) puts every path default under `ARGUS_STATE_DIR`:
  `auth/auth.db`, `settings/in`, `settings/out`, `secrets/`, `etc/` (sources, kube), `reports/rca`, `keys/totp.key`;
  no htpasswd import. `legacy` (the default) keeps the paths above. A configured key always wins.
- `ARGUS_MIGRATE=auto` (default): `argus-api` and `argus worker` apply `internal/migrate` at start; a read-only
  database is skipped; demo mode never migrates. `off`: only `argus migrate` does.

## 1. Files

| file | owner / mode | holds | read by |
|---|---|---|---|
| `/etc/argus/argus.env` | root:argus 0640 | every **non-secret** key below, `KEY=value`, no shell expansion | every component through the loader (§7); root dispatchers read their own root-owned copy |
| `/etc/argus/secrets/<NAME>` | root 0600, or root:<consumer> 0640 | **one secret per file**, the value only (no `KEY=`) | systemd `LoadCredential=<NAME>:/etc/argus/secrets/<NAME>` → `$CREDENTIALS_DIRECTORY/<NAME>`; never an environment variable of a model run |
| `/etc/argus/data/environments.json` | root 0644 | environments, classes, networks, hostname patterns, alias grammar | loader, guard, ssh-ro, fix dispatcher, action broker, renderers |
| `/etc/argus/data/*.json|txt|tsv` | root 0644 | lists: tiers, customer-facing names, domains, backup sets, data platform, readiness pages, cert pins | the module that owns the feature |
| `/etc/argus/policy.json` | root 0600 | access policy (ACCESS.md) | action broker only |
| `/etc/argus/generated/` | root 0644 | files **rendered** from the above for consumers that cannot read JSON at run time (unit drop-ins, guard markers) | systemd, guard |
| root-written runtime settings (`thresholds.env`, `jira.env`, `outline.json`, `qa-ask.json`, `readiness.json`) | root | values an admin changes in the browser | as today; they override `argus.env` (precedence §2) |

**Why JSON for data, not YAML:** the Python side is standard-library only and the Go side should not
need a YAML dependency; JSON is read identically by both (ADR-0001).

**Paths.** Product paths become `/etc/argus`, `/var/lib/argus`, `/var/log/argus`, `/opt/argus`.
On argus-01 the migration is a symlink per directory (`/etc/argus → /etc/argus` …), so no unit and no
running process changes in the same step. Every path is also a key (§3.1) for adopters with other
layouts.

## 2. Precedence and bootstrap

1. Built-in default (in `config/schema.json`, shipped with the code).
2. `/etc/argus/argus.env` (path overridable only by the process environment `ARGUS_CONFIG`).
3. Root-written runtime settings files (admin page) for the keys marked **admin** below.
4. Process environment, **only** in tests and for keys marked **env-override** (budgets of one run).

Legacy names (`OPS_STATE_DIR`, `OPS_DB`, `GRAFANA_<DC>_URL`, `MATRIX_*`, `CICD_*` …) keep working:
they **are** the canonical names where they already exist. New keys are added only where no key exists.
Legacy `/etc/argus/env` is read as a fallback until step R4 of the roadmap splits it; a secret found
there is accepted with a startup warning.

## 3. Keys

Columns: **type** (`str`, `int`, `float`, `bool` = `0|1`, `list` = comma-separated, `url` = `https://`
unless stated, `path`, `cal` = systemd `OnCalendar`, `dur` = `30s|10m|2h|7d`), **req** = required when
the module is on, **validation** = what `argus-config check` enforces, **reader** = component.

### 3.1 Core

| key | type | default | req | validation | reader |
|---|---|---|---|---|---|
| `COMPANY_NAME` | str | — | yes | 1–60 printable chars | app (titles), prompts (preamble), notify |
| `ARGUS_DISPLAY_NAME` | str | `Argus` | no | 1–30 chars | app, notify, chat replies (`Argus:` prefix) |
| `ARGUS_PUBLIC_URL` | url | — | yes | https, no path except `/`, no user info | notify links (`notify_format.APP`), OIDC redirect (`OPS_PUBLIC_URL` alias), Jira/Outline back-links |
| `ARGUS_HOSTNAME` | str | `hostname -s` | no | RFC 1123 label | self-check, help text, operator commands shown in the app |
| `ARGUS_HOST_IP` | ip | first global address | no | IPv4/IPv6 | source-address binding in ACCESS (SSH cert `source-address`), setup scripts |
| `ARGUS_TZ` | str | `UTC` | no | IANA zone present in `/usr/share/zoneinfo` | timers, quiet hours, day caps, brief, backup schedule display |
| `ARGUS_LOCALE` | str | `en` | no | file `locale/<v>.json` exists | app, notify |
| `ARGUS_CHAT_LOCALE` | list | `en` | no | each has a locale file | QA questions, DM keywords (`yes/no/on behalf of`) |
| `ARGUS_ETC_DIR` | path | `/etc/argus` | no | absolute, root-owned | all |
| `ARGUS_DATA_DIR` | path | `${ARGUS_ETC_DIR}/data` | no | absolute | all |
| `ARGUS_SECRETS_DIR` | path | `${ARGUS_ETC_DIR}/secrets` | no | absolute, root 0700 | renderers of `LoadCredential=` |
| `OPS_STATE_DIR` | path | `/var/lib/argus` | no | absolute | collectors, app mount |
| `ARGUS_LOG_DIR` | path | `/var/log/argus` | no | absolute | audit logs |
| `ARGUS_INSTALL_DIR` | path | `/opt/argus` | no | absolute | units |
| `OPS_DB`, `OPS_AUTH_DB`, `OPS_BOARD_DB` | path | under `OPS_STATE_DIR` | no | absolute | as today |
| `ENVIRONMENTS_FILE` | path | `${ARGUS_DATA_DIR}/environments.json` | **yes** | schema §8; ≥1 environment of class `production` | loader, guard, ssh-ro, fix, broker, CVE scoring |
| `HOSTS_FILE` | path | `${OPS_STATE_DIR}/hosts.json` (generated by inventory) | no | schema | ssh-ro list, broker targets |
| `SERVICE_TIERS_FILE` (`OPS_SERVICE_TIERS`) | path | `${ARGUS_DATA_DIR}/service-tiers.json` | no | JSON | incidents, CVE scoring, readiness |
| `CUSTOMER_FACING_FILE` (`OPS_CUSTOMER_FACING`) | path | `${ARGUS_DATA_DIR}/customer-facing.txt` | no | one FQDN per line | incident paging, CVE exposure |
| `SERVICE_MAP_FILE` | path | `${OPS_STATE_DIR}/service-map.json` | no | JSON | prompts, CVE exposure, verify |
| `DATA_PLATFORM_FILE` (`OPS_DATA_PLATFORM`) | path | `${ARGUS_DATA_DIR}/data-platform.json` | no | JSON | incidents, inventory |

### 3.2 Feature toggles (one per module)

A module that is off: its timers are not installed by the renderer, its app pages show "not enabled
(set `FEATURE_X=1`)", its tiles disappear from the overview and the brief, and nothing else breaks.
A module that is on but missing a required key **fails `argus-config check`** and its units refuse to
start (`ExecStartPre=argus-config check --module <m>`); other modules keep running.

| key | default | needs (when 1) |
|---|---|---|
| `FEATURE_INCIDENTS` | 1 | an alert source (`ALERT_SOURCE`) |
| `FEATURE_QA_TRIAGE` | 0 | chat adapter, `MATRIX_QA_ROOMS` |
| `FEATURE_QA_ASK` | 0 | `FEATURE_QA_TRIAGE`, `ARGUS_CHAT_LOCALE` |
| `FEATURE_LOG_RCA` | 1 | monitoring adapter with logs |
| `FEATURE_RCA_DEEP` | 1 | SCM adapter (MRs) |
| `FEATURE_BACKUPS` | 0 | object-storage adapter, `BACKUP_SETS_FILE` |
| `FEATURE_BACKUP_PLANS` | 0 | `FEATURE_BACKUPS`, `FEATURE_FIXER`, `BACKUP_TEMPLATES_REPO` |
| `FEATURE_CERTS` | 1 | `DOMAINS_FILE` |
| `FEATURE_CAPACITY`, `FEATURE_HYGIENE` | 1 | monitoring adapter |
| `FEATURE_INVENTORY` | 1 | monitoring adapter (node_exporter) |
| `FEATURE_HOST_EVIDENCE` | 0 | ssh-ro set up, `ENVIRONMENTS_FILE` aliases |
| `FEATURE_CICD` | 0 | SCM adapter, `CICD_GROUPS` |
| `FEATURE_CICD_VARS_META` | 0 | secret `ci-vars-meta-token` |
| `FEATURE_BOARD` (Jira) | 0 | tracker adapter |
| `FEATURE_TASK_AGENT` | 0 | `FEATURE_BOARD` |
| `FEATURE_MATRIX_DM` | 0 | chat adapter `matrix`, `FEATURE_BOARD` |
| `FEATURE_OUTLINE` | 0 | docs adapter, `OUTLINE_URL` |
| `FEATURE_SSO` | 0 | `OIDC_*` |
| `FEATURE_FIXER` | 0 | `fix-scope` written by root, fixer user set up |
| `FEATURE_READINESS` | 0 | `READINESS_EVENT_DATE` |
| `FEATURE_CVE` | 0 | at least one `CVE_INVENTORY_SOURCES`, `CVE_FEEDS` |
| `FEATURE_ACTIONS` | 0 | `ACTIONS_POLICY_FILE`, `ACTIONS_MODE` |
| `FEATURE_BRIEF` | 1 | chat adapter |
| `FEATURE_TODAY` | 0 | none (reads what the other modules stored; the brief becomes the day's plan) |

### 3.3 Model

| key | type | default | req | validation | reader |
|---|---|---|---|---|---|
| `MODEL_RUNTIME` | enum | `claude-code` | yes | `claude-code` | run-task.sh, fix-run.sh |
| `MODEL_AUTH` | enum | `api-key` | yes | `api-key` \| `oauth` | run-task.sh (secret `anthropic-api-key` or `claude-oauth-token`) |
| `MODEL_DEFAULT`, `MODEL_FAST` | str | runtime default / `sonnet` | no | non-empty | run-task.sh, tool-less calls |
| `MODEL_DAILY_BUDGET_USD` | float | `20` | no | 0 < v ≤ 1000 | run-task.sh refuses a run when the day's spend (ops.db `runs.cost_usd`) exceeds it |
| `LESSONS_PER_RUN` / `LESSONS_PROMPT_MAX` / `LESSONS_PROMPT_MAX_BYTES` / `LESSON_REVIEW_DAYS` / `LESSONS_MAX_APPROVED` | int | 3 / 8 / 6144 / 90 / 300 | no | >= 0 | `bin/lessons.py` (ADR-0013 P4); the on/off switch is board.db state `lessons_settings` (Admin → Lessons) |
| `<TASK>_BUDGET_USD`, `<TASK>_MAX_TURNS`, `<TASK>_TIMEOUT` | float/int/dur | today's values | no | ranges per task | run-task.sh (exist: `RCA_DEEP_*`, `DISK_EXPLAIN_*`, `VM_EXPLAIN_*`, `TASK_INTAKE_*`, `TASK_RUN_*`, `TASK_CHAT_*` (+ `TASK_CHAT_MODEL`), `OPS_FIX_*`) |
| `TASK_SECRETS` | on\|off | `off` | no | Admin → Task agent wins (board.db state) | task_agent.py, task_secrets.py, argus-secret-exec (ADR-0013 P3; off refuses requests and wipes every task secret) |
| `TASK_SECRET_TTL_MIN`, `TASK_SECRET_MAX_TTL_MIN` | int | `240`, `1440` | no | 1..1440 (D4: 24 h hard cap) | task_secrets.py (root) |
| `TASK_SECRET_MAX`, `TASK_SECRET_MAX_BYTES` | int | `3`, `8192` | no | 1..10, 64..8192 | task_secrets.py (root); the app caps the form at 8192 bytes |
| `ARGUS_CLAUDE_ENV_FILE` | path | `${ARGUS_ETC_DIR}/claude.env` | no | absolute | `argus secrets apply-claude` (root) replaces `CLAUDE_CODE_OAUTH_TOKEN` there from Settings → Claude (backup `.bak-<time>-claude`, last 5); the Claude runs read it |
| `ARGUS_CLAUDE_BIN`, `ARGUS_CLAUDE_RUN_AS` | path / str | `/usr/local/bin/claude`, `argus` | no | absolute / user | the token test call (one `claude -p`, 1 turn, no tools) |
| `ARGUS_CLAUDE_EGRESS_FILE` | path | `${ARGUS_ETC_DIR}/claude-egress.json` | no | absolute | egress-country guard settings `{"guard", "blocked", "fail"}` (missing = on, `IR`, fail closed); written by root from Settings → Claude |
| `ARGUS_CLAUDE_EGRESS_CHECK` | path | `${ARGUS_STATE_DIR}/claude-egress-check.json` | no | absolute | the guard's cached check (10 min, 1 min after a failure), shown on Settings → Claude |
| `ARGUS_CLAUDE_EGRESS_URLS` | list | Cloudflare trace, country.is | no | https | `internal/claudegate`: sources in order; every runner calls `argus claude check` first (exit 78: stopped) |
| `ARGUS_KUBE_DIR` | path | `${ARGUS_ETC_DIR}/kube` | no | absolute | `argus secrets apply-kube` (root) stores `<name>.kubeconfig` there from Settings → Kubernetes (0640 root:`ARGUS_KUBE_AGENT_GROUP` only while read-only, else 0600; `backup/`, last 5) and `clusters.json` |
| `ARGUS_KUBECTL`, `ARGUS_KUBECTL_VERSION`, `ARGUS_KUBECTL_SHA256`, `ARGUS_KUBECTL_URL` | path / str | `/usr/local/lib/argus/kubectl`, `v1.34.13`, its sha256, dl.k8s.io | no | sha256 must match | `argus secrets install-kubectl` (root); a mirror URL for hosts without internet |
| `ARGUS_KUBE_AGENT_GROUP` | str | `argus` | no | group | the agent's group: may read an enabled kubeconfig |
| `ARGUS_KUBE_AUDIT_LOG`, `ARGUS_KUBE_CACHE_DIR` | path | `${ARGUS_STATE_DIR}/kube-ro.log`, `${ARGUS_STATE_DIR}/kube-cache` | no | absolute | `argus tools kube`: one audit line per call; kubectl discovery cache |
| `ARGUS_SOURCES_DIR` | path | `${ARGUS_ETC_DIR}/sources` | no | absolute | `argus sources apply`/`check` (root, Settings → Data sources, ADR-0017): `registry.json` (0644, no secret), `<type>/<name>.cred` (0640 root:`ARGUS_SOURCES_AGENT_GROUP` only while enabled for the agent, else 0600; `backup/`, last 5); status `ARGUS_SETTINGS_OUT/sources-status.json`; `argus sources record` (agent) copies it into `ARGUS_DB` `data_sources` |
| `ARGUS_SOURCES_SERVICES` | path | `${ARGUS_ETC_DIR}/services.json` | no | JSON | the Data sources catalogue (`argus sources catalogue`/`discover`, ADR-0017 P2): `{"prefixes": [...], "services": {"group/repo": {"gitlab": path, "domains": [...]}}}`; missing or empty: the application projects of `cicd_projects` in `ARGUS_DB` |
| `ARGUS_SOURCES_FIND_AUDIT_LOG` | path | `${ARGUS_STATE_DIR}/sources-find.log` | no | absolute | `argus sources find` (ADR-0017 P3): one JSON line per find (a 12-hex hash of the question, service, types, chosen targets, fan-out counts and limits hit, learned count, run id); never the question text or a credential |
| `ARGUS_SOURCES_FIND_MIN_CONF`, `ARGUS_SOURCES_FIND_TOP` | str / int | `0.6`, `3` | no | 0..1, 1..3 | `argus sources find`: a proposed route is a target from this confidence on; how many targets of several are given |
| `ARGUS_SOURCES_FANOUT_MAX_INSTANCES`, `_MAX_CALLS`, `_MAX_SECONDS`, `_MAX_OUTPUT` | int | `5`, `20`, `60`, `6000` | no | 1..20, 1..200, 5..300, 1000..50000 | `argus sources find --type`: hard limits of the fan-out when nothing is routed (instances, HTTP calls, wall seconds, text bytes); the report says "searched N of M instances" |
| `ARGUS_SOURCES_AGENT_GROUP` | str | `argus` | no | group | the agent's group: may read an enabled data source credential |
| `ARGUS_SOURCES_LEGACY_ENV` | path | `${ARGUS_ETC_DIR}/env` | no | absolute | the migration reads `GITLAB_URL`, `GITLAB_RO_TOKEN`, `GRAFANA_<ID>_URL`/`_TOKEN` there (never written); secrets `gitlab-ro-token`, `grafana-<id>-token` in `ARGUS_SECRETS_DIR` win |

### 3.4 Monitoring and alert ingest

| key | type | default | req | validation | reader |
|---|---|---|---|---|---|
| `MONITORING_ADAPTER` | enum | `grafana-proxy` | yes | `grafana-proxy` \| `prometheus` | obs.sh, collectors |
| `OBS_SITES` | list | — | yes | 1–8 ids `[a-z0-9]{2,16}` | obs.sh, capacity, hygiene, inventory, incidents, app filters |
| `OBS_SITE_<ID>_LABEL` | str | upper-case id | no | ≤ 16 chars | notify, incident keys (`<LABEL>|subject|family`) |
| `OBS_SITE_<ID>_SHORT` | str | first 3 letters | no | ≤ 4 chars | `notify_styles` (`AFR`, `AST`) |
| `GRAFANA_<ID>_URL` | url | — | yes (grafana-proxy) | https or http on a private address | obs.sh (exists); `argus sources` mirrors each one with a token as `grafana/<id>` |
| `PROM_URL`, `LOKI_ADDR` | url | — | no | | direct site (exists) |
| `OBS_PRIMARY_SITE` | str | first of `OBS_SITES` | no | in `OBS_SITES` | dedupe order across sites (capacity: "DC-East first") |
| `ALERT_SOURCE` | enum | `matrix-rooms` | yes | `matrix-rooms` \| `alertmanager-webhook` | ops-watch |
| `MATRIX_ALERT_ROOMS` | list | — | if matrix-rooms | room ids `!x:server` | ops-watch |
| `OBS_SITE_<ID>_ALERT_FORMAT` | enum | `alertmanager-default` | no | known parser ids | ops-watch parser per site |
| `MATRIX_KUMA_ROOMS`, `KUMA_MIN_DOWN_SECONDS`, `KUMA_FLAP_COUNT` | | exist | | | ops-watch, triage_collect |
| `WATCH_*` | | exist (README table) | | ranges | ops-watch |

### 3.5 Chat and notifications

| key | type | default | req | validation | reader |
|---|---|---|---|---|---|
| `CHAT_ADAPTER` | enum | `matrix` | yes | `matrix` \| `webhook` \| `none` (later `slack`, `telegram`) | notify, ops-watch, qa, dm |
| `MATRIX_HOMESERVER`, `MATRIX_PAN_URL` | url | — | if matrix | | matrix.py (exist) |
| `MATRIX_SERVER_NAME` | str | host of `MATRIX_HOMESERVER` | no | DNS name | room links, `@user:server` validation |
| `MATRIX_BOT_USER` | str | — | if matrix | `@x:server` | (exists) |
| `MATRIX_NOTIFY_ROOM` | str | — | if matrix | room id | (exists) |
| `MATRIX_QA_ROOMS` (`MATRIX_WATCH_ROOMS`) | list | — | if `FEATURE_QA_TRIAGE` | room ids | triage_collect, qa_issues |
| `MATRIX_QA_ROOM_LABEL` | str | `QA` | no | ≤ 30 chars | app `/qa` (today "ops-qa"), notify |
| `NOTIFY_STYLE`, `NOTIFY_QUIET`, `NOTIFY_GROUP_HOURS`, `NOTIFY_BRIEF` | | exist | | | notify_styles |
| `NOTIFY_LINK_HOSTS` | list | hosts of `ARGUS_PUBLIC_URL`, `GITLAB_URL` | no | DNS names | notify_format link allowlist |
| `NOTIFY_WEBHOOK`, `TELEGRAM_CHAT_ID` | | exist | | | notify.sh |

### 3.6 Tracker (Jira)

Non-secret Jira settings keep living in root's `jira.env` (written by the admin page, **admin**
precedence); `argus.env` gives the defaults.

| key | type | default | validation |
|---|---|---|---|
| `TRACKER_ADAPTER` | enum | `jira` \| `none` | |
| `JIRA_URL`, `JIRA_PROJECT`, `JIRA_BOARD_ID`, `JIRA_MODE`, `JIRA_CA_FILE` | | exist | |
| `JIRA_MATRIX_PROJECT` | str | `${JIRA_PROJECT}` (today hard-coded `DEV`) | `[A-Z][A-Z0-9]+` |
| `JIRA_AGENT_COLUMN` | str | `In Progress` | non-empty |
| `JIRA_FIXER_STATUS` | str | `Ready for fixer` | non-empty |
| `JIRA_LABEL_PREFIX` | str | `argus-` | `[a-z0-9-]+` |
| `JIRA_QA_INTAKE_MAX_DAY`, `JIRA_MATRIX_MAX_DAY` | int | `10`, `30` | 0–200 |

### 3.7 SCM / CI

| key | type | default | req | validation | reader |
|---|---|---|---|---|---|
| `SCM_ADAPTER` | enum | `gitlab` | yes | `gitlab` (later `github`) | gitlab_ro.py, open-mr.sh, cicd_* |
| `GITLAB_URL` | url | — | yes | https | (exists) |
| `ARGUS_REPO` (`GITLAB_PROJECT`) | str | — | yes | `group/project` | MRs, design comments, self-update |
| `CICD_GROUPS` | list | — | if `FEATURE_CICD` | group paths | (exists) |
| `CICD_ENGINE_PROJECT` | str | — | if `FEATURE_CICD` | `group/project` | cicd_estate (include detection) |
| `CICD_ENGINE_PROJECT_ID` | int | resolved from the path | no | > 0 | readiness, cicd_write rules, guard |
| `CICD_ENV_BRANCHES` | map | `develop=develop,pre-release=pre-release,release=release` | no | env names exist in `environments.json` | cicd_* |
| `CICD_DEPLOY_JOBS` | list | `deploy` | no | job names | readiness, verify, triage, cicd_write (never retried) |
| `CICD_LOCK_RESOURCE_GROUP` | str | empty (no lock) | no | | stuck-pipeline detector |
| `CICD_UNIT_PREFIX` | str | empty | no | `[a-z0-9-]*` | verify, inventory, ssh-ro `unitinfo` globs |
| `CICD_APP_DIR_ROOT` | path | `/srv` | no | absolute | `deployedsha`, verify |
| `BACKUP_TEMPLATES_REPO` | str | — | if `FEATURE_BACKUP_PLANS` | `group/project` | backup plans |
| `CICD_*` thresholds | | exist | | | |

### 3.8 SSO, users

| key | type | default | req | validation |
|---|---|---|---|---|
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CA_FILE` | | exist | if `FEATURE_SSO` | issuer https, discovery reachable at check time (warning only) |
| `OIDC_ROLE_CLAIM` | str | `roles` | no | JSON path `a.b.c` |
| `OIDC_ROLE_MAP` | map | `argus-admin=admin,argus-viewer=viewer` | no | values ⊂ roles of ACCESS.md §2 |
| `OPS_ADMINS` | list | **empty** (today `alex.doe`) | no | usernames |
| `OPS_ADMIN_USER` | str | — | yes unless `FEATURE_SSO=1` | username |

### 3.9 Docs, storage, host access

| key | type | default | validation | reader |
|---|---|---|---|---|
| `DOCS_ADAPTER` | enum | `outline` \| `none` | | outline_* |
| `OUTLINE_URL` | url | — (today hard-coded `docs.dev.northwind.example` default) | https | outline admin page default, allowlist seed |
| `OUTLINE_COLLECTION` | str | `Services` | | |
| `OBJECT_STORAGE_ADAPTER` | enum | `minio-mc` | `minio-mc` \| `s3` | backup-check |
| `BACKUP_CHECK_S3_ALIAS` | str | `backup` | `[a-z0-9-]+`; secret `mc-host-<alias>` | backup-check.sh, cred-check |
| `BACKUP_SETS_FILE` (`BACKUP_SETS_TSV`) | path | `${ARGUS_DATA_DIR}/backups.tsv` | TSV columns | backup-check |
| `ARGUS_BACKUP_S3_ALIAS`, `ARGUS_BACKUP_S3_BUCKET`, `ARGUS_BACKUP_S3_PREFIX` | str | `opsbackup`, —, `argus/` | | backup-state.sh |
| `SSH_RO_JUMP` | str | empty | ssh alias | setup scripts, ssh config renderer |
| `CONTAINER_MIRROR` | str | empty | registry host | renderer rewrites image refs |

### 3.10 CVE (see CVE.md)

| key | type | default | validation |
|---|---|---|---|
| `CVE_INVENTORY_SOURCES` | list | `os,kernel` | ⊂ `os,kernel,images,dockerfiles,lockfiles,runtimes,services,gitlab` |
| `CVE_FEEDS` | list | `osv,kev,epss` | ⊂ `osv,nvd,kev,epss,debian,ubuntu,rhel,eol` |
| `CVE_FEEDS_MODE` | enum | `online` | `online` \| `mirror` \| `offline` |
| `CVE_FEEDS_MIRROR_URL` | url | — | required when `mirror` |
| `CVE_OFFLINE_BUNDLE_DIR` | path | `${OPS_STATE_DIR}/cve/inbox` | required when `offline` |
| `CVE_FEEDS_EGRESS_HOSTS` | list | the feed hosts | DNS names; rendered into the fetcher's IP/egress allowlist |
| `SCHEDULE_CVE_FEEDS`, `SCHEDULE_CVE_INVENTORY`, `SCHEDULE_CVE_MATCH` | cal | `daily 05:10`, `daily 05:40`, after both | |
| `CVE_SLA_DAYS` | map | `P1=2,P2=7,P3=30,P4=90` | ints |
| `CVE_PAGE_MIN_PRIORITY` | enum | `P1` | `P1..P4` \| `none` |
| `CVE_DIGEST_MIN_PRIORITY` | enum | `P3` | |
| `CVE_EPSS_HIGH` | float | `0.10` | 0–1 |
| `CVE_SUPPRESSION_MAX_DAYS` | int | `180` | 1–365 |
| `CVE_LOCKFILE_REFS` | map | `production=<release branch from CICD_ENV_BRANCHES>` | |
| `CVE_MAX_NEW_INCIDENTS_DAY` | int | `10` | 0–100 |

### 3.11 Actions (see ACCESS.md)

| key | type | default | validation |
|---|---|---|---|
| `ACTIONS_MODE` | enum | `off` | `off` \| `dry-run` \| `enforce` |
| `ACTIONS_POLICY_FILE` | path | `${ARGUS_ETC_DIR}/policy.json` | root 0600, schema ACCESS.md §4, no capability of class `production` without an approval rule |
| `ACTION_SSH_USER` | str | `argus-act` | |
| `ACTION_CERT_TTL` | dur | `5m` | 1m–30m |
| `ACTION_AUDIT_LOG` | path | `${ARGUS_LOG_DIR}/actions.jsonl` | |
| `ACTION_APPROVAL_TTL` | dur | `4h` | 10m–72h |
| `ACTION_BREAKGLASS_TTL` | dur | `60m` | 10m–4h |

### 3.12 Problems (see HLD-problems.md §8, ADR-0008)

Read by `bin/problems.py` and `bin/problem_suggest.py`; the app reads only `FEATURE_PROBLEMS`. **admin** keys may be
overridden by the root-written `/etc/argus/problems.json`.

| key | type | default | validation |
|---|---|---|---|
| `FEATURE_PROBLEMS` | bool | `0` until ROADMAP D3, then `1` | |
| `PROBLEMS_SOURCES` | list | `incident,qa_issue,cve_group,cicd_finding,capacity,vm_report,finding` | ⊂ issue ref types |
| `PROBLEMS_SUGGEST` (admin), `SCHEDULE_PROBLEMS_SUGGEST` | bool, cal | `1`, `*:0/10` | |
| `PROBLEMS_SUGGEST_LOOKBACK_DAYS` | int | `14` | 1–90 |
| `PROBLEMS_SUGGEST_WINDOW_MIN` (admin) | int | `120` | 5–10080 |
| `PROBLEMS_SUGGEST_MIN_CONFIDENCE` (admin) | float | `0.60` | 0.30–0.99 |
| `PROBLEMS_SUGGEST_MAX_PER_ITEM` | int | `3` | 1–10 |
| `PROBLEMS_FANOUT_MAX` | int | `20` | 2–1000 |
| `PROBLEMS_REJECT_DAYS` | int | `30` | 1–365 |
| `PROBLEMS_AUTO_ACCEPT` (admin) | bool | `0` | |
| `PROBLEMS_AUTO_ACCEPT_CONFIDENCE` (admin) | float | `0.95` | 0.90–0.99 |
| `PROBLEMS_AUTO_ACCEPT_CLASSES` (admin) | list | `dev,staging` | ⊂ environment classes |
| `PROBLEMS_RCA_CONTEXT`, `PROBLEMS_PROMPT_MAX` | bool, int | `1`, `15` | 0–40 |
| `PROBLEMS_VERIFY_HOURS` (admin) | int | `24` | 1–336 |
| `PROBLEMS_AUTO_RESOLVE` (admin) | bool | `0` | |
| `PROBLEMS_REOPEN_DAYS`, `PROBLEMS_CLOSE_DAYS` | int | `30`, `14` | 1–365 |
| `PROBLEMS_NOTIFY_THREAD` (admin) | bool | `1` | |
| `PROBLEMS_JIRA`, `PROBLEMS_FOLD_PROPOSALS` (admin) | bool | `1`, `1` | the problem path never auto-creates |
| `PROBLEMS_BACKFILL_SOURCES` | list | `rca-deep` | ⊂ `rca-deep,log-rca` |
| `PROBLEMS_CLUSTER_MIN` | int | `3` | 2–50 |

### 3.13 Today (the daily plan, `bin/today.py`)

Read by `bin/today.py` (argus) and `bin/brief.py`; the app reads only `FEATURE_TODAY` (or `today-status.json`
`enabled`). Kinds, base estimates and defaults are data in `config/today.json`; the operator's open decisions in
`config/today-decisions.json`. **admin** keys are changed on `/today` → Plan settings (stored in ops.db `today_settings`,
they win over the env).

| key | type | default | validation |
|---|---|---|---|
| `FEATURE_TODAY` | bool | `0` | |
| `TODAY_FOCUS_MIN` (admin) | int | `180` | 15–600 minutes of focus time a day |
| `TODAY_MAX_ITEMS` (admin) | int | `8` | 1–20 |
| `TODAY_MAX_RISKY` (admin) | int | `1` | 0–3 production changes that need a window, a day |
| `TODAY_PLAN_AT`, `SCHEDULE_TODAY` | str, cal | `07:30`, `*-*-* 07:30 + every 30 min` | `HH:MM` |
| `TODAY_OPERATOR` | str | first of `OPS_ADMINS` | Jira user whose In Progress tasks enter the plan |
| `TODAY_JIRA_STATUSES` | list | `In Progress` | Jira status names |
| `TODAY_REFINE`, `TODAY_REFINE_BUDGET_USD` | bool, float | `0`, `0.05` | ≤ 0.05 a day; shortens text only |
| `TODAY_CONFIG`, `TODAY_DECISIONS` | path | `config/today.json`, `config/today-decisions.json` | JSON |

### 3.14 Credential expiry (`bin/cred_expiry.py`)

Read by root's `cred_expiry.py check` (process env, then the non-secret keys of `/etc/argus/env`) and written into
`settings-out/credentials.json` (`thresholds`), so selfcheck, today, brief and the app use the same values. Which
credentials exist is data: `config/credentials.json` (id, name, kind, `env_file` + `var` or `file`/`glob`, used by,
`check` = gitlab|grafana|jira|matrix|outline|none, URL key, optional `rotate_days` / `valid_days`, how-to key); root reads
its own copy (`CRED_CONFIG`), never the agent's clone.

| key | type | default | validation |
|---|---|---|---|
| `CRED_WARN_DAYS` | int | `30` | 1–365; self-check WARN, digest, Proposed task |
| `CRED_CRIT_DAYS` | int | `7` | 0–90, below `CRED_WARN_DAYS`; incident (high) + Matrix notice |
| `CRED_TODAY_DAYS` | int | `14` | 0–365; Today item (10 min) |
| `CRED_ROTATE_DAYS` | int | `180` | 7–3650; rotation policy for a credential without an expiry date |
| `CRED_CONFIG`, `CRED_ETC`, `CRED_OUT` | path | `config/credentials.json`, `/etc/argus`, `settings-out/credentials.json` | |
| `SCHEDULE_CRED_EXPIRY` | cal | `*-*-* 07:20` | |

## 4. Secrets (names only; one file each under `ARGUS_SECRETS_DIR`)

| secret file | today | consumer | rotation |
|---|---|---|---|
| `anthropic-api-key` / `claude-oauth-token` | `CLAUDE_CODE_OAUTH_TOKEN` in env + `fix.env` | run-task.sh, fix-run.sh | org key with spend cap (TOOLS-14) |
| `matrix-access-token`, `matrix-pan-token` | `MATRIX_ACCESS_TOKEN`, `MATRIX_PAN_TOKEN` | matrix.py, ops-watch | on bot re-login |
| `grafana-<site>-token` | `GRAFANA_<DC>_TOKEN` | obs.sh | 1 year |
| `gitlab-ro-token` | `GITLAB_RO_TOKEN` | gitlab_ro.py | expiry set |
| `gitlab-bot-token` | `GITLAB_TOKEN` | open-mr.sh, rca_reports, design post (root) | expiry set |
| `gitlab-write-token` | `CICD_WRITE_TOKEN` (unset) | **action broker only** | 30 days |
| `ci-vars-meta-token` | `/etc/argus/ci-vars-meta.token` | ci_vars_meta (LoadCredential) | |
| `mc-host-<alias>` | `MC_HOST_backup`, `MC_HOST_opsbackup` | backup-check, backup-state | |
| `jira.pat`, `jira/users/<u>.pat` | same | board_sync (root) | user-managed |
| `outline.token` | same | outline_sync (root) | |
| `oidc-client-secret` | `OIDC_CLIENT_SECRET` | app (mounted file, not env) | |
| `ops-admin-initial-password` | `OPS_ADMIN_INITIAL_PASSWORD` | app first start | used once |
| `kuma-push-url` | `KUMA_PUSH_URL` | kuma-push.sh | |
| `backup.pass` | same | backup-state.sh | escrow |
| `nvd-api-key` | — | CVE feed fetcher | |
| `ssh-ca-key` | — | action broker (root 0400; ideally an HSM/agent) | yearly, with target re-trust |

Rule: a model run (argus / argus-fix) receives **only** its model credential and, for argus,
the read-only tokens its scripts need, as `LoadCredential=` files read by the scripts — never as
environment variables the model could print (today `.claude/settings.json` denies `env`/`printenv`;
credentials files remove the need for that deny).

## 5. Thresholds

Every module-level constant listed under *threshold* in HARDCODE-AUDIT.md becomes
`<COMPONENT>_<NAME>` with today's value as default, for example `FIX_MAX_PER_DAY=10`,
`FIX_MAX_PROMPT_BYTES=16384`, `RUNREQ_*` (exist), `INCIDENT_AUTORESOLVE_HOURS=6`,
`CERT_WARN_DAYS=30`, `CERT_FAIL_DAYS=14`, `CERT_PAGE_DAYS=7`, `CAPACITY_FULL_DAYS=14`,
`BOARD_DONE_DAYS=14`, `QA_ASK_MAX_DAY=10`. Admin-editable ones keep root's `thresholds.env` precedence.

## 6. Schedules

`SCHEDULE_<TASK>` (systemd `OnCalendar`, validated with `systemd-analyze calendar`), rendered by root's
apply-settings into `/etc/systemd/system/<timer>.d/schedule.conf` — the mechanism the admin page already
uses. All timers get `Timezone` from `ARGUS_TZ` (written as `OnCalendar=… <TZ>`).

| key | Northwind today | product default |
|---|---|---|
| `SCHEDULE_BACKUP_CHECK` | `*-*-* 07:00` | `*-*-* 07:00` |
| `SCHEDULE_CERT_CHECK` | `*-*-* 07:30` | `*-*-* 07:30` |
| `SCHEDULE_CHAT_TRIAGE` | `*:0/30` | `*:0/30` |
| `SCHEDULE_LOG_RCA` | `*-*-* 03,09,15,21:10` | `*-*-* 00/6:10` |
| `SCHEDULE_INSIGHTS` | `*-*-* 08:30` | `*-*-* 06:30` |
| `SCHEDULE_BRIEF` | `09:00` | `09:00` |
| `SCHEDULE_CICD_DAILY` | `*-*-* 08:05` | `*-*-* 06:05` |
| `SCHEDULE_OUTLINE` | `*-*-* 07:40` | `*-*-* 07:40` |
| `SCHEDULE_STATE_BACKUP` | `*-*-* 03:15` | `*-*-* 03:15` |
| `SCHEDULE_CI_VARS_META` | `*-*-* 07:40` | `*-*-* 07:40` |

## 7. The loader (one contract, two languages)

- `config/schema.json` (in the repository): every key with type, default, required-when, validation,
  secret flag, module, readers. **Single source** for the Python loader (`bin/argus_config.py`,
  stdlib), the Go loader (`apps/api/internal/config`, after the cutover; ADR-0005), the renderer, the
  `argus-config check` CLI and this document's tables (generated in a later step).
- `argus-config check [--module m]`: parses, applies defaults, validates, prints
  `config: OK (14 modules on, 9 off)` or one line per error, exit 1:
  `CONFIG ERROR: OBS_SITES is required by FEATURE_CAPACITY=1 (set it in /etc/argus/argus.env)`.
- `argus-config show`: effective values, secrets shown as `set (file …, 0600 root)` / `missing`.
- `argus-config render`: writes `/etc/argus/generated/` (guard markers JSON, fixer IP filter drop-in,
  timer drop-ins, ssh-ro alias table) — root only, atomic, with a diff on stdout.
- Parity test: both loaders produce the same normalised JSON for `tests/config/*.env` fixtures.

## 8. `environments.json` schema

Annotated (the real file is plain JSON without comments):

```json
{
  "version": 1,
  "environments": [
    {
      "name": "develop",                      // canonical name, used in scope files and policy
      "aliases": ["dev", "stage"],            // accepted in chat, Jira, alert labels
      "class": "dev",                         // dev | staging | production   (policy and paging use the class)
      "label": "Develop",
      "cidrs": ["192.168.73.134/32"],        // addresses that belong to this env (targets)
      "deny_cidrs": [],                       // carved out (e.g. the bastion)
      "hostname_patterns": ["^SRV-NW-01-"],   // anchored regexes
      "domain_suffixes": [".dev.northwind.example"],
      "domains": [],
      "monitoring_labels": {"env": ["stage"]},// Prometheus/Loki label values that mean this env
      "ro_alias": "^(stage3|edge12[12])-ro$|^dev-[a-z0-9-]+-ro$",   // ssh-ro alias grammar
      "fix_alias": "^dev-[a-z0-9-]+-fix$",
      "ci_branch": "develop"
    }
  ],
  "default_class_for_unknown": "production"   // unknown target = treated as production (fail safe)
}
```

Validation: names unique; every CIDR parses; no CIDR in two environments unless carved by `deny_cidrs`;
at least one `production`; `default_class_for_unknown` must be `production`; regexes compile and are
anchored. Rendered into: guard markers (`generated/guard-markers.json`, read by
`argus_guard.py`; the guard **fails closed** for `--agent`/`--fixer` when it is missing), fixer
`IPAddressAllow/Deny` drop-in, `fix-requests` networks, ssh-ro alias→env table.

## 9. Example: `.env` for a fictional company "Acme"

```sh
# /etc/argus/argus.env — Acme Corp. Non-secret only. Secrets: /etc/argus/secrets/<name>.
COMPANY_NAME=Acme
ARGUS_DISPLAY_NAME=Argus
ARGUS_PUBLIC_URL=https://argus.ops.acme.example
ARGUS_TZ=Europe/Berlin
ARGUS_LOCALE=en
ARGUS_CHAT_LOCALE=en,de

# modules
FEATURE_INCIDENTS=1
FEATURE_QA_TRIAGE=1
FEATURE_QA_ASK=0
FEATURE_LOG_RCA=1
FEATURE_RCA_DEEP=1
FEATURE_BACKUPS=1
FEATURE_BACKUP_PLANS=0
FEATURE_CERTS=1
FEATURE_CAPACITY=1
FEATURE_HYGIENE=1
FEATURE_INVENTORY=1
FEATURE_HOST_EVIDENCE=1
FEATURE_CICD=1
FEATURE_CICD_VARS_META=0
FEATURE_BOARD=1
FEATURE_TASK_AGENT=0
FEATURE_MATRIX_DM=0
FEATURE_OUTLINE=0
FEATURE_SSO=1
FEATURE_FIXER=1
FEATURE_READINESS=0
FEATURE_CVE=1
FEATURE_ACTIONS=1
FEATURE_BRIEF=1

# model
MODEL_RUNTIME=claude-code
MODEL_AUTH=api-key
MODEL_DAILY_BUDGET_USD=15

# monitoring: two Prometheus/Loki stacks behind Grafana
MONITORING_ADAPTER=grafana-proxy
OBS_SITES=fra,ams
OBS_SITE_FRA_LABEL=FRA
OBS_SITE_AMS_LABEL=AMS
GRAFANA_FRA_URL=https://grafana.fra.acme.example
GRAFANA_AMS_URL=https://grafana.ams.acme.example
ALERT_SOURCE=alertmanager-webhook
OBS_SITE_FRA_ALERT_FORMAT=alertmanager-default
OBS_SITE_AMS_ALERT_FORMAT=alertmanager-default

# chat
CHAT_ADAPTER=matrix
MATRIX_HOMESERVER=https://matrix.acme.example
MATRIX_PAN_URL=http://127.0.0.1:8009
MATRIX_BOT_USER=@argus:acme.example
MATRIX_NOTIFY_ROOM=!argusNotify:acme.example
MATRIX_QA_ROOMS=!qaReports:acme.example
MATRIX_QA_ROOM_LABEL=QA reports
MATRIX_KUMA_ROOMS=

# tracker
TRACKER_ADAPTER=jira
JIRA_PROJECT=OPS
JIRA_AGENT_COLUMN=Doing
JIRA_FIXER_STATUS=Ready for automation

# SCM / CI
SCM_ADAPTER=gitlab
GITLAB_URL=https://gitlab.acme.example
ARGUS_REPO=platform/argus
CICD_GROUPS=shop,platform
CICD_ENGINE_PROJECT=platform/ci-templates
CICD_ENV_BRANCHES=dev=main,staging=staging,prod=production
CICD_DEPLOY_JOBS=deploy
CICD_LOCK_RESOURCE_GROUP=
CICD_UNIT_PREFIX=acme-
CICD_APP_DIR_ROOT=/srv/apps

# SSO
OIDC_ISSUER=https://id.acme.example/realms/acme
OIDC_CLIENT_ID=argus
OIDC_ROLE_CLAIM=resource_access.argus.roles
OIDC_ROLE_MAP=argus-admin=admin,argus-operator=operator,argus-approver=approver,argus-viewer=viewer
OPS_ADMINS=

# storage
OBJECT_STORAGE_ADAPTER=s3
BACKUP_CHECK_S3_ALIAS=backups
ARGUS_BACKUP_S3_ALIAS=argusbackup
ARGUS_BACKUP_S3_BUCKET=ops-tools
ARGUS_BACKUP_S3_PREFIX=argus/

# CVE
CVE_INVENTORY_SOURCES=os,kernel,images,dockerfiles,lockfiles,services
CVE_FEEDS=osv,kev,epss,nvd,eol
CVE_FEEDS_MODE=online
CVE_SLA_DAYS=P1=3,P2=14,P3=45,P4=120
CVE_PAGE_MIN_PRIORITY=P1

# actions
ACTIONS_MODE=dry-run
ACTION_CERT_TTL=5m
```

Acme's `environments.json` names `dev` (class dev, `10.20.0.0/16`), `staging` (class staging,
`10.30.0.0/16`), `prod` (class production, `10.40.0.0/16`, `^prod-`, `.acme.example`). Secrets:
`anthropic-api-key`, `matrix-access-token`, `matrix-pan-token`, `grafana-fra-token`,
`grafana-ams-token`, `gitlab-ro-token`, `gitlab-bot-token`, `jira.pat`, `oidc-client-secret`,
`mc-host-backups`, `nvd-api-key`.

## 10. Northwind's current values, mapped

| key | Northwind value | where it is today |
|---|---|---|
| `COMPANY_NAME` | `Northwind` | `app/server.py:659,694,1476`, prompts |
| `ARGUS_PUBLIC_URL` | `https://argus.dev.northwind.example` | `bin/notify_format.py:43`, `OPS_PUBLIC_URL` |
| `ARGUS_HOSTNAME` / `ARGUS_HOST_IP` | `argus-01` / `192.168.73.118` | CLAUDE.md, views, setup scripts |
| `ARGUS_TZ` | `Europe/Amsterdam` | `bin/run-requests.py:397`, timers |
| `ARGUS_CHAT_LOCALE` | `fa,en` | `bin/qa_issues.py`, `bin/matrix_dm.py` |
| `OBS_SITES` | `dc-east,dc-west` | `bin/capacity.py:43`, `bin/notify_styles.py:46`, `bin/incidents.py` |
| `OBS_SITE_dc-east_ALERT_FORMAT` / `…_dc-west_…` | `am-matrix-emoji-v1` / `am-matrix-bold-v1` | `bin/ops-watch.py:78` |
| `GRAFANA_dc-east_URL`, `GRAFANA_dc-west_URL` | Grafana of `*.prod.northwind.example` / `*.observability.pay.northwind.example` | `/etc/argus/env` (already keys) |
| `MATRIX_SERVER_NAME` | `chat.northwind.example` | `app/views_matrix.py:36`, pantalaimon.conf |
| `MATRIX_QA_ROOM_LABEL` | `ops-qa` | `app/views_qa.py`, `app/server.py:75` |
| `JIRA_MATRIX_PROJECT` | `DEV` | `bin/matrix_requests.py:35` |
| `JIRA_AGENT_COLUMN`, `JIRA_FIXER_STATUS` | `In Progress`, `Ready for fixer` | `bin/board_sync.py`, `app/views_board.py` |
| `GITLAB_URL` | `https://glab.northwind.example` | 6+ files |
| `ARGUS_REPO` | `ops/argus` | `GITLAB_PROJECT` |
| `CICD_ENGINE_PROJECT` / `_ID` | `northwind/deployment` / `210` | `bin/cicd_estate.py`, `bin/readiness.py`, guard |
| `CICD_DEPLOY_JOBS` | `deploy_systemd,trigger_deploy` | `bin/cicd_*.py`, guard |
| `CICD_LOCK_RESOURCE_GROUP` | `trigger-deploy` | `bin/cicd_estate.py` |
| `CICD_UNIT_PREFIX` / `CICD_APP_DIR_ROOT` | `northwind-` / `/data/services` | `bin/cicd_verify.py:9`, ssh-ro |
| `CICD_ENV_BRANCHES` | `develop=develop,pre-release=pre-release,release=release` | `bin/cicd_*.py` |
| `BACKUP_TEMPLATES_REPO` | `ops/backup-templates` | `app/views_backup.py:24` |
| `OIDC_ISSUER` | `https://sso.dev.northwind.example/realms/master` | `/etc/argus/argus-web.env` |
| `OUTLINE_URL` | `https://docs.dev.northwind.example` | `app/views_outline.py:23`, `bin/outline_sync.py:22` |
| `BACKUP_CHECK_S3_ALIAS` | `backup` (`agent-ro` user) | `scripts/backup-check.sh` |
| `ARGUS_BACKUP_S3_*` | `opsbackup`, `backup-tools`, `argus/` | `bin/backup-state.sh` |
| `READINESS_EVENT_NAME/DATE` | `Black Friday` / `2026-11-27` | `bin/service_tiers.py:23` |
| `OPS_ADMINS` | `alex.doe` | `app/compose.yml:18`, `app/server.py:46` |
| `environments.json` | develop: `192.168.73.134/.121/.122`, `^SRV-NW-01-`, `.dev.northwind.example`; pre-release: `10.21.63.0/24`, `10.21.93.0/24`, `^nw-shop-pre-`; release: `10.21.0.0/16` minus pre, `10.26.230.0/24`, `10.27.230.0/24`, `172.20.120.0/24`, `172.20.124.0/24`, `192.168.30.0/24`, `203.0.113.0/23`, `192.0.2.0/24`, `^af-`, `^SRV-NW-0[2-8]-`, `^main-(shop|admin|…)`, `shop.northwind.example`, `.cdn.northwind.example`, `shop-new.northwind.example` | guard, fix-requests, ops-fix@, ssh-ro, setup-fixer-access |
