---
title: Is it safe?
summary: Exactly what Argus can and cannot change, per environment, and how that is enforced.
order: 9
---

Short answer: **Argus reads everywhere and changes nothing on production.** On develop and pre-release it changes
things only after an admin reviewed the exact fix, and only where the operator allowed it.

## Per environment {#per-environment}

| | develop | pre-release | release (production) |
|---|---|---|---|
| Read metrics, logs, alerts | yes | yes | yes |
| Read hosts (fixed list of read-only commands) | where the operator allowed it | where the operator allowed it | where the operator allowed it |
| Read-only login to a database host | no | no | no |
| Connect to a database | only to test a new backup, on its host | only to test a new backup, on its host | never |
| Fix on a host | admin-reviewed fixer, if allowed | admin-reviewed fixer, if allowed | never |
| Install a backup job | Install now (admin) | Install now (admin) | never; the operator runs the bundle |
| Deploy, retry or cancel a pipeline | no (proposal) | no (proposal) | never |
| Change a CI variable | no | no | no |

## What Argus can change {#can}

- Its own database, reports and pages.
- A merge request in its own repository, on a branch that a person merges.
- A merge request in the backup templates repository when a backup plan is saved.
- On develop and pre-release, through the fixer: what the reviewed prompt says, with backups taken first and rollback
  commands in the report.
- Jira: an issue, a move or a comment when a person accepts a proposed task, says yes in Matrix, moves a card or
  comments (with that person's token, or the shared connection); and a `[Argus]` comment with the result of a fixer
  or task-agent run.
- Outline: its own block on each service page, only in publish mode.
- One message per question in a {{qa_room}} thread, messages in its own notify room, and replies in direct messages
  to admins who forwarded a request.

## What Argus cannot change {#cannot}

- Anything on a production host, database or deploy.
- GitLab settings, CI variables or pipelines (no write token is configured).
- Alert rules, nginx, DNS or firewalls.
- Its own permissions. The operator grants every access; Argus cannot widen it.

## How it is enforced {#enforced}

- **Accounts:** read-only accounts for hosts, GitLab, metrics and storage. The fixer uses a separate account that
  exists only on develop and pre-release hosts.
- **Fixed commands:** host evidence goes through a wrapper with a fixed catalogue of read-only commands, strict
  argument checks, secret redaction and an output cap. Every call is logged, refused ones too.
- **A guard on every tool call:** a check before each agent action denies production writes, production database
  writes and production deploys.
- **Network:** the fixer's service cannot reach production addresses.
- **Requests, not actions:** the web app only writes request files. Root jobs on the server validate each one again
  (who, role, limits, target) before anything happens.
- **Secrets:** tokens are written once and never shown; reports, pages and messages pass a secret scan.
- **Temporary task credentials:** when a develop or pre-release task needs an API token, an admin gives it on the task
  page (**Provide secret**, never the chat or Matrix; never for production tasks). It is kept only in memory on
  Argus's server, encrypted with a key for that task, and wiped when the task ends, at its expiry (4 h by default,
  24 h at most), when Claude usage is switched off or on a restart. Argus's agent never sees it: one read-only
  command at a time runs with it, can reach only the hosts you named, and the value is removed from what it prints.
  Afterwards, revoke or rotate the token: the page reminds you ([Task agent](/help/delivery#task-agent)).
- **Temporary task credentials:** when a develop or pre-release task needs an API token, an admin gives it on the task
  page (**Provide secret**, never the chat or Matrix; never for production tasks). It is kept only in memory on
  Argus's server, encrypted with a key for that task, and wiped when the task ends, at its expiry (4 h by default,
  24 h at most), when Claude usage is switched off or on a restart. Argus's agent never sees it: one read-only
  command at a time runs with it, can reach only the hosts you named, and the value is removed from what it prints.
  Afterwards, revoke or rotate the token: the page reminds you ([Task agent](/help/delivery#task-agent)).
- **Caps:** every model run has a cost and time limit, and every action a daily limit.

## Sign-in, two-factor and sessions {#sign-in}

**Keep me signed in** on the sign-in page (local and Keycloak) keeps you signed in on that browser for 30 days at most,
and signs you out after 7 days without use. Without it, a session lasts 12 hours, or 2 hours without use.

**Two-factor authentication (2FA)** uses an authenticator app (Google Authenticator, Microsoft Authenticator,
1Password, Aegis and others). Reading pages never needs it. **Every change does**: actions, the fixer, starting a task,
deep RCA, accepting proposals, problem links, backups, scans, settings, users, suppressions and "Mark as OK".

- **Set it up:** [Account](/account) → **Set up two-factor**. Type your password (Keycloak users: sign in again if
  your last sign-in is older than 10 minutes), scan the QR code, type the 6-digit code, and save the 10 recovery codes
  shown once. The first time you try a change without 2FA, Argus offers the same setup and sends you back afterwards.
- **After that:** you type a code once, at sign-in, and every change in that session works without another code.
  Only turning off your own 2FA and making new recovery codes ask for a code again. Is that safe? A session without
  2FA still cannot change anything, every form carries a CSRF token, and every change is in the audit log.
- **Lost the phone:** type a recovery code instead of the app's code (each works once), then set up the new phone in
  Account → Manage two-factor. No recovery codes left: ask an admin to **Reset 2FA** (Settings → Users and access); it
  signs you out everywhere. Sign in again with your password (or Keycloak): Argus opens the 2FA setup first and no
  other page until you confirm a new app. The admin cannot set it up for you.
- **Your sessions:** Account lists where you are signed in (browser, IP, last seen) with **Sign out this session** and
  **Sign out everywhere else**. Changing your password also signs out every other session.
- Codes are checked within 30 seconds either side; a code works once. Five wrong codes from one address lock that
  for 15 minutes.

:::admin
Switches (server environment): `MFA_REQUIRED_FOR_ACTIONS` (1), `MFA_STEP_UP` (off: the sign-in code covers the
session; on: a change needs a code from the last `MFA_STEPUP_MINUTES` (720), settings, users, suppressions, "Mark as
OK" and the fixer one from the last `MFA_SENSITIVE_MINUTES` (10)), `MFA_REQUIRED_FOR_LOGIN` (0; also the button in Users and access), `MFA_TRUST_IDP` (0) with
`OIDC_MFA_ACR` / `OIDC_MFA_AMR` (Keycloak's second factor counts only for the exact values set there, and only for an
`auth_time` within the step-up window), `SESSION_MAX_HOURS` (12), `SESSION_IDLE_HOURS` (2), `SESSION_REMEMBER_DAYS` (30),
`SESSION_REMEMBER_IDLE_DAYS` (7), `SESSION_REMEMBER_ABSOLUTE_DAYS` (30, the hard cap), `TOTP_KEY_FILE`.
Break-glass on {{host}} as root: `docker exec argus-api /app/argus-api user reset-2fa <user> <reason>` (the same forced
setup at the next sign-in; audited as `cli:root`, posted to Matrix), `... user signout <user>`, `... mfa status`.
:::

## FAQ {#faq}

:::details Can a viewer make Argus do something risky?
No. Viewers can re-run read-only checks, refresh a VM, check a repo, acknowledge an incident and suggest a problem
link. Everything that changes something needs an admin, and production needs the operator.
:::

:::details What if a fix prompt mentions a production host by mistake?
The review page refuses it, and the fixer's own guard and network block it again.
:::
