---
title: Settings
summary: For admins - General, Claude, Kubernetes, Users and access, Jira sync, Backup targets, Integrations, Credentials, the important switches and limits, where data lives, and rollback.
order: 7
audience: admin
---

Settings is at the bottom of the sidebar, for admins only. Its tabs are General, Claude, Kubernetes, Users and access, Jira sync, Backup
targets, Integrations and Credentials. The app never applies a setting itself: it writes a request, and a root job on the server
checks it again, applies it, logs it, and posts the change to Matrix.

## General {#general}

**Where:** Settings → [General](/admin).

- **Claude usage:** On (default) or Off, with one button. Argus's model runs use the operator's Claude subscription.
  Off stops every Claude call: triage, deep RCA, storage and VM analyses, ideas, task runs, the fixer,
  translations and Lazy readings are skipped and recorded as skipped, never as failures (no incident, no Matrix FAIL
  notice). Checks still collect their facts. Today, Checks, the brief and the self-check say **Claude paused**.
  Root can also run `sudo argus settings claude on|off` on {{host}} (no word prints it).
- **Schedules:** how often chat triage, log RCA, the backup check and the certificate check run, and whether each is
  enabled, with the next run.
- **Thresholds:** Kuma minutes down before a monitor counts (default 5), Kuma downs that count as flapping (3),
  certificate WARN days (30) and FAIL days (14).
- **Automatic deep RCA:** see [Deep RCA](/help/operations#deep-rca-auto).
- **Hub services and sales-event readiness:** hub services raise severity (on); the event name and date (default
  Black Friday, 2026-11-27); the freeze reminder from D-7; the readiness brief from D-30 to D+2. Reports only.
- **QA follow-up questions:** on, off, or dry run; **Read images in QA messages** on or off (off by default). Root
  can also run `sudo argus settings qa on|off|dry-run|send|images-on|images-off` on {{host}}.
- **Sign-in:** "SSO only" (password sign-in refused except for the break-glass account). It has no effect until
  Keycloak is configured.
- **Credentials:** status only, checked hourly. Values are never shown. Healthy ones fold.
- **Change log:** the last 15 changes and who made them.

Press **Save and apply**. It is applied within seconds. If a value is out of range, nothing is saved: your entries
stay on the page, the field is marked with what is wrong, and the message at the top jumps to it. **Discard changes**
puts the form back as it was loaded. On **Users and access**, **Reset password…** and **Disable…** open a short
confirmation with what happens before the button that does it.

## Claude {#claude}

**Where:** Settings → [Claude](/admin/claude). Admins only; every change needs a recent 2FA step.

The page opens on one answer: **is Argus's AI working right now?** The status block combines the usage switch, the
token and its last test, the location guard and the last real Claude run (a run where the model answered; runs skipped
while AI was off or stopped do not count). It says **Working**, **Check**, **Not working**, **Stopped**, **Paused**
(usage limit) or **Off**, why in one sentence, and the one thing to do when something is wrong (for example
**Replace the token** when Claude refuses it). A run that worked after a failed test counts as working.

Below it, three sections in this order:

- **Claude token:** the login Argus's Claude runs use (an OAuth token from `claude setup-token`, lasts one year),
  kept by root in the file `ARGUS_CLAUDE_ENV_FILE` names (default `/etc/argus/claude.env`). Only its state shows: set
  or not, its last 4 characters, who replaced it and when, and the last test with Claude's error when it failed. The
  token is never shown, logged or kept by the app.
  - **Test current token** asks Claude for one short answer with the token in place. Nothing changes.
  - **Replace the token** (folded; open when the token is missing or failing): on a computer signed in to the right
    Claude account run `claude setup-token`, paste what it prints (it starts with `sk-ant-oat01-`) and choose
    **Replace and test**. Root on {{host}} (`argus secrets apply-claude`, run by `argus-claude-token.path`) backs up
    the file (`<file>.bak-<time>-claude`, the last 5 kept), puts the token in, and runs the test as
    `ARGUS_CLAUDE_RUN_AS`. If the test fails, the previous token is restored at once and the page says why. Under
    **Advanced**, **Keep the new token even if the test fails** is only for when you know the token is right and
    Claude itself is down. The audit log records who, when, the variable and the last 4 characters, never the token.
  - While root applies a request the page shows **Applying on {{host}}…** and refreshes itself every 5 seconds; then a
    short message says what happened and what to do next.
  - Only the OAuth token is offered. An Anthropic API key would take precedence over the subscription login and is
    billed per use.
- **Location guard** (the egress country guard): before every Claude run (`argus claude check`) Argus looks up the
  country its Claude traffic leaves from, with the same proxy settings Claude itself gets (Cloudflare's trace, then
  country.is if that fails; `ARGUS_CLAUDE_EGRESS_URLS`), and does not start the run when the country is on the blocked
  list (default **IR**). The provider does not serve that region, and calls from it can get the account disabled. The
  run is skipped with the reason, never retried in a loop, and the Checks and Today pages show **AI stopped**. One
  lookup is reused for 10 minutes (1 minute after a failed one). **Check now** refreshes it; **Test current token** and
  **Replace and test** check it first, so a token is never tested from a blocked country. **Guard settings** (folded):
  the on/off toggle (on by default), the blocked countries, and what happens if the lookup fails (**Stop the run**, the
  default, or **Let the run start**); applied by root and logged. This is a guard, not a bypass: it never changes the
  route the traffic takes or hides the location; it only stops AI and says why.
- **Claude usage:** the same On/Off switch as on General.

## Writing style {#writing-style}

**Where:** Settings → [Writing style](/admin/writing-style). Admins only; every change needs a recent 2FA step.

The style of every text Argus generates: task descriptions, plans, RCA reports, proposals, notices and chat answers.
The default ships with Argus: about 80% ASD-STE100 (one idea per sentence, short sentences, active voice, imperative
steps), a one-line goal, a facts table, numbered sections, one action per checkbox task with a "Reason:" sub-bullet,
and a final **Warnings** list. Facts, commands, paths and error strings stay exact; a secret is named, never written.

- **Edit:** change the text and choose **Save**. Plain text or Markdown, up to 16 KB. The worker applies it within
  seconds (`etc/writing-style.md` in the volume; in the file layout root's `argus settings apply` writes
  `ARGUS_ETC_DIR/writing-style.md`). A refused text keeps the style in use.
- **Reset to default:** deletes the custom text; the default is used again.
- **Who uses it:** every model run gets the style before its prompt (task runs, RCA, proposals, ideas, chat). Tasks
  Argus creates from incidents, RCA and CI/CD findings start with a goal line, the facts and checkbox steps.

## Data sources {#sources}

**Where:** Settings → [Data sources](/admin/sources). Admins only; every change needs a recent 2FA step.

Every system Argus reads, grouped by type: GitLab, Grafana, Kubernetes, Outline, Elasticsearch/OpenSearch and Sentry.
Add as many of each type as you have (ten Grafanas, two GitLabs); each has a name that is unique per type, an
environment (develop, pre-release, release, or shared for one system that serves every environment, such as GitLab), an
address and a purpose:

- **Agent reads:** the agent reads through it. The login must be read-only; one that can write is **Refused** (an admin
  can override on develop and pre-release under Advanced, never on release or shared).
- **Issue tracker** (GitLab): the board's GitLab issues; needs scope `api`.
- **Publishes docs** (Outline): Argus writes the service docs there; needs a member login.

**Add data source** → pick the type → fill its form → **Save and check**. The token goes to root once and is never
shown again (the list shows its last four characters). Root (`argus sources apply`) checks the login within seconds,
and every source again every 6 hours (`argus sources check`): **Read-only** (verified read-only), **OK** (works; its
purpose needs writes), **Refused: can write**, **Login failed**, **Unreachable**, **Not checked**. Each check only
reads: GitLab asks for the token's scopes, Grafana for the role (must be Viewer), Outline for the user's role,
Elasticsearch for its privileges, Sentry for the token's scopes. Checks use https only, follow no redirect and never
call a loopback or link-local address. Kubernetes clusters are added on the [Kubernetes](#kubernetes) tab and listed
here.

The integrations Argus already had appear here on their own (the GitLab read-only token, the GitLab issues tracker,
every Grafana site with a token, Outline, the Kubernetes clusters). Jobs still read their credentials where they always
did, so those rows say where: change them on Integrations or Kubernetes, or ask the operator for the ones in the
configuration. The agent sees the list (never a credential) with `argus sources list` and picks the right instance
instead of trying all of them. Next: Argus learns which service lives in which source and keeps that table on each
service's Outline page.

## Kubernetes {#kubernetes}

**Where:** Settings → [Kubernetes](/admin/kube). Admins only; every change needs a recent 2FA step.

Give the agent read-only access to a Kubernetes cluster, for RCA and tasks (pods, logs, events, ingresses). The page
opens on one answer: **can the agent read our clusters, and only read?** Then one panel per cluster, the form to add
or replace one, and how to make a read-only login.

- **Add a cluster:** a name (lowercase, for example `dev`; the agent calls it by this name), the environment
  (develop, pre-release or release) and the kubeconfig, pasted or chosen as a file (YAML or JSON, up to 64 KB), and
  optionally the context to use. **Save and check** sends it once to root on {{host}}; the app never keeps or shows it.
- **What is refused:** exec and auth-provider plugins, file paths (`client-certificate`, `client-key`,
  `certificate-authority`, `tokenFile`) and impersonation (`as`). Only inline credentials are stored, so nothing runs on
  {{host}}. Root keeps only the chosen context, its cluster and its user.
- **The check:** root asks the cluster what the login may do (`kubectl auth can-i --list` plus cluster-wide questions,
  `version`, `get ns`; run as an unprivileged user, 15 seconds each). **Read-only** (get, list, watch, no Secrets) is
  enabled for the agent. **Can write** or **Reads Secrets** is stored but refused for the agent; on develop and
  pre-release an admin can tick **Enable it even if it can write or read Secrets** under **Advanced**. **Release never
  takes the override.** A cluster that does not answer is refused until **Check again** passes. A later check that finds
  more rights disables it again.
- **Where it lives:** `ARGUS_KUBE_DIR/<name>.kubeconfig` (default `/etc/argus/kube`) (root-owned; readable by the agent's group only while
  enabled). Saving the same name again backs up the previous file to its `backup/` folder (the last 5 kept);
  **Remove** backs it up too. The audit log records who, which cluster, the server, the user and the verdict, never
  the kubeconfig.
- **kubectl:** root installs a pinned kubectl (`ARGUS_KUBECTL_VERSION`, `ARGUS_KUBECTL_SHA256`, `ARGUS_KUBECTL_URL`; `argus secrets install-kubectl`) and
  checks it at every save. If {{host}} cannot download it, the page says **kubectl is not installed** and every check and agent call is
  refused until it is there.
- **Make a read-only login** (on the page, folded): a ServiceAccount `argus-ro` with a ClusterRole that can get, list
  and watch pods, logs, services, deployments, jobs, ingresses, events and the like, never Secrets (it names resources
  instead of `*`), its token Secret, and the `kubectl config` commands that build a kubeconfig with the token inline.

**What the agent can run:** `argus tools kube <cluster> get|describe|logs|top|events|api-resources|version|can-i`.
Logs are capped (`--tail` up to 500, `--since` up to 6h); output is redacted and capped; every call is in
`ARGUS_KUBE_AUDIT_LOG`. Never Secrets, never ConfigMap contents (names only), never exec, cp, port-forward,
proxy, edit, apply, delete, scale, rollout, drain, cordon or `--raw`.

## Users and access {#users}

**Where:** Settings → [Users and access](/admin/users).

- Add a local user, reset a password (the new one is temporary and shown only once), change the role, disable or
  enable an account.
- The table shows each user's role, source (local or Keycloak), **2FA** (on, and when it was last used), last login,
  Matrix ID and state.
- **Actions** on a user's row opens one menu. Each risky action (reset password, make admin, disable, reset 2FA) then
  opens its own confirm step that says what happens; confirm it there, or cancel.
- **Reset 2FA** (with a reason): removes the user's authenticator and recovery codes and signs them out everywhere.
  You do not set up their new app. At their next sign-in (password or Keycloak) Argus sends them straight to the
  2FA setup and opens no other page until they confirm a new app; they can still sign out. **Sign out everywhere**
  ends all their sessions. Both are in the security events below the table and posted to Matrix (resets).
- **Require 2FA for all users at next login:** users without 2FA must set it up before anything else. Off by default:
  then they can read and are asked at their first change. See [Sign-in and two-factor](/help/safety#sign-in).
- Changes on this tab need two-factor authentication: the code typed at sign-in is enough (with `MFA_STEP_UP=on`,
  a code from the last 10 minutes).
- Set a user's **Matrix ID** here (or they set it in Account). Only admins with a Matrix ID can send Argus tasks
  from Matrix.
- Keycloak users get their role from Keycloak (client role `admin` or `viewer`).

## Jira sync {#jira}

**Where:** Settings → [Jira sync](/admin/board). See [Task board](/help/delivery#board-admin) for the fields.
Also here: **Task agent and QA intake**, "Propose tasks for page-level incidents", and the sync status (last good
sync, Jira calls, failed or dropped requests, retries).

Each user can also connect their own Jira in Account; Argus uses that token for that user's changes.

## Backup targets {#backup-targets}

**Where:** Settings → [Backup targets](/admin/backup-targets). See
[Backup targets](/help/infrastructure#backup-targets).

## Integrations {#integrations}

**Where:** Settings → [Integrations](/admin/outline). Outline service docs, see
[Outline service docs](/help/agent#outline), and **GitLab issues**, the task tracker beside Jira
([GitLab issues](/help/delivery#gitlab-issues)).

**Connect GitLab issues:**

1. Open the issue board in GitLab and copy its URL. A **group board** (`…/groups/<group>/-/boards`) shows the issues
   of every project in the group; a project board only that project's.
2. Where the board lives (the group or the project) → **Settings → Access tokens** → add a token named `argus`, role
   **Reporter**, scope **api**, with an expiry date. It is a bot, so nothing depends on one person's account. Do not use
   a personal token; Argus warns when it sees one. A group board needs a **group** access token.
3. In Argus, paste the board URL and the token and choose **Test and save**. Argus checks the token, the project or
   group and its boards from {{host}}. For a group board, choose the **Default project for new issues**.
4. With several boards, choose the one Argus mirrors. The **Board lists** table shows which list is which column.
5. Optional: **New tasks go to** Jira (the default) or GitLab issues; a **list map** such as `QA=review; Blocked=progress`;
   a **user map** such as `alex=alex.doe` for assignees.

The token is stored on {{host}} for the board sync only (`gitlab-issues.token` in the configuration folder; sealed
with the master key in the one-volume layout) and never shown again. Leave the token empty to use the GitLab data
source with purpose **tracker** (Settings → Data sources).
**Disconnect** deletes it; revoke it in GitLab too. Argus calls only the allowed GitLab hosts (by default the host of
`GITLAB_URL`), over https, without following redirects.

## Credentials {#credentials}

**Where:** Settings → [Credentials](/admin/credentials). Is any token or key Argus uses near its expiry date?

- Once a day (07:20) and on **Check now**, every credential checks itself on the server: a GitLab token reads its own
  expiry, Grafana and Jira report what they allow, a Matrix token answers "who am I". Only the result reaches this page:
  name, kind, who uses it, where it lives (the file and the variable name), expires, created, last used. **A value is
  never shown, stored or sent.**
- **Expires** shows the date and the days left: green, amber at 30 days or less, red at 7 days or less, **expired**,
  **rejected** (the service refused the token: revoked or wrong). Without an expiry date it says **no expiry info** or
  **never expires**, with a "rotate by" date (every 180 days by default).
- What happens: at 30 days a warning on Argus health, a line in the daily digest and a proposed task in [Review](/review?kind=proposals)
  "Rotate …" (never a Jira issue by itself); at 14 days an item on [Today](/today) (10 minutes); at 7 days, or expired
  or rejected, an incident and a quiet Matrix notice (no ping). Expired or rejected also turns Argus health red.
- **How to rotate** has the exact steps per kind, with the file to write and, for GitLab group tokens, the rotate API
  call. Put the new value straight into its file on the server as root, then press **Check now**.
- TLS certificates are not here: see [Certificates](/help/infrastructure#certificates).

## Switches and limits {#switches}

These live in the agent's environment file on the server, not in the app. Change them with the operator.

| Setting | Default | What it does |
|---|---|---|
| `FEATURE_PROBLEMS` | 0 | Problems pages and records. |
| `FEATURE_IDEAS` | 0 | The weekly advisers and Ideas decisions. |
| `FEATURE_CVE` | 0 | The vulnerability scan. |
| `MATRIX_NOTIFY_ENABLED` | 0 | Any message to the Matrix notify room. |
| `NOTIFY_STYLE` | classic | `C` = pages at once, everything else in the digest. |
| `NOTIFY_DIGEST_HOURS` | 9,17 | Digest times, {{tz}}. |
| `NOTIFY_QUIET` | 23-8 | Quiet hours: only a production page notifies. |
| `INCIDENT_AUTORESOLVE_HOURS` | 6 | Hours without a matching alert before an incident resolves. |
| `WATCH_MAX_JOBS_HOUR`, `WATCH_MAX_JOBS_DAY` | 6, 40 | Alert investigations per hour and day. |
| `CICD_LLM`, `CICD_LLM_MAX_DAY` | on, 10 | The CI/CD triage model call and its daily cap. |
| `CICD_WRITE` | 0 | CI/CD writes. Needs a write token too; stays off today. |
| `PROBLEMS_AUTO_ACCEPT` | 0 | Accept very confident problem links automatically. |
| `IDEAS_DAILY_USD` | 2.00 | All Ideas spend in a day. |
| `BOARD_DONE_DAYS` | 14 | Done tasks shown in the last board column. |
| `MFA_REQUIRED_FOR_ACTIONS` | 1 | Every change needs 2FA ([details](/help/safety#sign-in)). |
| `MFA_STEP_UP` | off | off: the code typed at sign-in covers every change of the session; on: the two windows below apply. |
| `MFA_STEPUP_MINUTES`, `MFA_SENSITIVE_MINUTES` | 720, 10 | With `MFA_STEP_UP=on`: how recent the last code must be for a change, and for settings, users, suppressions, Mark as OK and the fixer. |
| `SESSION_REMEMBER_DAYS`, `SESSION_REMEMBER_IDLE_DAYS` | 30, 7 | "Keep me signed in": the longest session, and the idle limit. |

Costs are capped per run as well: deep RCA $4, task agent run $5, task intake $0.50, storage analysis $1,
Matrix translation $0.10, CI/CD triage $0.15, Ideas $0.60. Infrastructure → Checks shows the 30-day total.

## Where data lives {#data}

| What | Where |
|---|---|
| History: runs, incidents, RCAs, findings, CVEs, problems, ideas | `ops.db` in the agent's data folder on the server |
| Tasks, proposals, Matrix previews | `board.db`, written only by root's board sync |
| Users and sessions | `auth.db` (password hashes only, session token hashes only, 2FA secrets encrypted with a key file only the app container can read, recovery code hashes) |
| Accepted failures | `ops.db` table `backup_overrides` (who, why, until when), written by `bin/overrides.py` |
| App requests | `settings-in/` (the app writes; root reads and validates) |
| Results for the app | `settings-out/` (root writes; the app reads) |
| Secrets (Jira, Outline, backup keys, tokens) | root-only files under `/etc/argus`; the app never mounts them |

A nightly backup at 03:15 keeps 14 encrypted archives of the databases, settings and state; a monthly restore test is
shown in Argus health. The archive's passphrase must also be kept outside the server.

## Rollback basics {#rollback}

- **The web app:** each deploy keeps the previous image. Rolling back is starting the previous image, or resetting
  the code to the previous commit and rebuilding.
- **A feature:** most features have a switch (above) or their own timers; turning them off stops them. Their tables
  are additive and can stay.
- **A setting:** change it back on the same page; the change log shows the old value.
- Every change to the live server is recorded with its backup and rollback in the operator's context files.

## FAQ {#faq}

:::details I saved a setting but nothing changed.
Root applies it within seconds (`argus settings apply`, run by `argus-settings.path`). If the save was rejected, the
page shows "Last save was rejected" with the reason. "Nothing applied from this page yet" means the root job is not
installed on {{host}}.
:::

:::details Can I see a token or password here?
No. Credentials are shown as status only, and tokens are written once and never read back by the app.
:::

## Access broker

Set `ARGUS_ACCESS_BROKER` to `teleport` or `boundary`, with `ARGUS_TELEPORT_PROXY` and `ARGUS_ACCESS_SSH_USER`, or
`ARGUS_BOUNDARY_ADDR` and `ARGUS_BOUNDARY_SCOPE`. VM pages and Settings -> Kubernetes then show the commands to connect.
Argus only shows them: the broker decides what each person may reach, and it records the session.
