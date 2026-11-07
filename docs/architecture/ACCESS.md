# Access model: from read-only to scoped production action

Status: proposal, 2026-10-22. Decision records: [ADR-0003](adr/0003-access-policy-engine.md),
[ADR-0007](adr/0007-ssh-certificates-per-action.md).

**Guardrail note.** Today the operator's rules say: production is read-only; every production write
needs the operator's explicit approval *for that one change*; Argus never grants itself access; the
fixer's `fix-scope` ignores `release`. This design does not weaken any of that by itself. It builds the
mechanism through which an approver can approve *one exact change* in Argus, and Argus carries out
*only that change*. Turning on any production capability requires the operator to amend
`GUARDRAILS.md` and to install the target-side executor himself (§9). Until then the shipped policy is
§8.1 (read-only production).

## 1. Principles

1. **Default deny.** No capability exists unless the policy file names it.
2. **The model proposes; the broker acts.** A model run can only write an *action request*. The root
   action broker `argus-actd` validates, collects approvals, executes and verifies. The model never
   holds a write credential, production or not (the free-form develop fixer is the one documented
   exception, class `dev` only, §6.4).
3. **Approve a plan, not an intention.** Every capability has a dry run that produces a plan (the exact
   commands or API calls and the diff). Approval binds to the plan's SHA-256. If the plan changes, the
   approval is void.
4. **Enforce on the target.** A production capability is enforced by the target host (forced command,
   per-capability sudoers), not only by argus-01.
5. **Short-lived, per-action credentials.** The broker mints a credential valid for one action and a
   few minutes (SSH certificate) or uses a token only it can read for an API call from a fixed
   allowlist.
6. **Everything is audited**, hash-chained, and visible in the app and the chat.

## 2. Principals

### 2.1 Humans (roles)

| role | can | today |
|---|---|---|
| `viewer` | read every page | `viewer` |
| `operator` | ack incidents, create tasks, request actions (any capability the policy lets humans request), request scans | new (today part of `admin`) |
| `approver` | approve or reject action requests whose policy names its approver group; cannot approve own request | new |
| `admin` | users, settings, integrations, suppressions, break-glass | `admin` |

Roles come from auth.db or the OIDC role claim (`OIDC_ROLE_MAP`). Approver **groups** (e.g.
`prod-approvers`, `dev-leads`) are lists of usernames or OIDC groups in the policy. Matrix and Jira
identities are linked to Argus users (exists: Matrix IDs, Jira connections) and act with that user's
roles only.

### 2.3 Authentication: sessions and second factor (shipped 2026-10-25, Go app)

- **Sessions:** token in `opsr_session` (Path=/, HttpOnly, Secure, SameSite=Strict), only its SHA-256 in auth.db. Plain:
  `SESSION_MAX_HOURS` 12 / `SESSION_IDLE_HOURS` 2. Keep me signed in: `SESSION_REMEMBER_DAYS` 30 (hard cap
  `SESSION_REMEMBER_ABSOLUTE_DAYS` 30) / `SESSION_REMEMBER_IDLE_DAYS` 7 sliding. New token (and CSRF secret) at every
  sign-in, password re-entry and 2FA step; rotation never extends the 2FA grant or the primary-auth time.
- **TOTP:** RFC 6238 SHA-1/6/30, ±1 step, a step accepted once per factor (one `UPDATE … WHERE last_step < ?`).
  Separate failure budgets: password (user+IP), TOTP and recovery (user+IP and per user), enrolment (per user); all only
  reachable after the password, so anonymous guesses never lock a user's second factor. Secrets: AES-256-GCM, random  <!-- secret-scan: allow -->
  96-bit nonce, AAD = user + factor id, key id stored; `TOTP_KEY_FILE` (rotation: `TOTP_KEY_FILE_OLD` +
  `argus-api mfa rekey`). Recovery codes: 10 × 80 bits, SHA-256 only, single use, a new set replaces the old.
- **Enrolment:** fresh primary auth (password again; Keycloak: sign-in within 10 min, else `max_age=0 prompt=login`),
  a 10-minute transaction bound to user + session, activation = transaction + valid code in one UPDATE; replacing an
  active factor needs its current code.
- **Policy:** one middleware after CSRF, default deny, classes per route (`POST_ROUTES`): exempt (sign-in/out,
  theme, enrolment, own sessions), inline (verifies a code itself: turn off, new recovery codes), normal (code within
  `MFA_STEPUP_MINUTES` 720), sensitive (within `MFA_SENSITIVE_MINUTES` 10: settings, users, integrations, suppressions,
  backup Mark as OK, the fixer). Other methods than GET/HEAD/POST are refused (501); GETs never change state (the
  Keycloak start only stores a login state). A test checks every handler and form against the table.
- **`MFA_STEP_UP`** (default `off`, read once at start): off = the 2FA of the sign-in (or a trusted Keycloak acr/amr)
  satisfies normal and sensitive for the whole session; only inline routes ask again; a session without 2FA is still
  sent to enrol / step up. `on` = the time windows above (2026-10-27, operator: one prompt per sign-in for UX).
- **Keycloak:** its second factor counts only with `MFA_TRUST_IDP=1` and an explicit `OIDC_MFA_ACR` or `OIDC_MFA_AMR`
  value, on a verified id_token, with `auth_time` within the step-up window; the grant starts at `auth_time`. The
  Argus account is bound to the Keycloak `sub` (one issuer). Otherwise Keycloak users enrol Argus TOTP.
- **Reset:** admin (sensitive, reason ≥ 10 chars) or `argus-api user reset-2fa` (root): removes the factor, pending
  enrolment and recovery codes, ends all sessions and pending logins. Audit: auth.db `auth_audit` (actor, target,
  session id prefix, method, outcome, reason; never a code) mirrored to root's `settings-out/audit.log`, with a Matrix
  notice for resets and recovery-code use.
- **Client address:** `X-Real-IP` is trusted only from `OPS_TRUSTED_PROXIES` (the two stage edges and loopback).

### 2.2 Agents

| agent principal | runs as | may request | may execute |
|---|---|---|---|
| `agent:triage` (incident, chat-triage, log-rca) | argus | read-only evidence; action *requests* for capabilities marked `agent_may_request` | nothing |
| `agent:rca` (rca-deep, rca-auto) | argus | same | nothing |
| `agent:task` (task-intake / task-run) | argus | same, bound to the approved Jira plan version | nothing |
| `agent:fixer` | argus-fix | — | free-form commands on class `dev`/`staging` aliases in `fix-scope` (§6.4) |
| `agent:cicd-guardian` | argus | `ci.pipeline.cancel`, `ci.job.retry` requests | nothing |
| `agent:cve-mitigator` | argus | `package.upgrade`, `service.restart`, `image.rebuild` requests from a CVE finding | nothing |
| `broker` | root (`argus-actd`) | — | every capability, only after policy + approval |

An agent identity is established by the broker from the **directory** the request arrived in
(`settings-in/actions/agent-<name>/`, writable only by that Unix user) plus the run id, never from a
field in the request.

## 3. Capabilities

A capability = **action × target selector × environment class/name**, with typed parameters.

| capability | target | params | executor | rollback |
|---|---|---|---|---|
| `service.restart` | host + unit (unit ∈ policy allowlist/glob) | `unit` | ssh-cap `service-restart` | none needed; verify health, page on fail |
| `service.start` / `service.stop` | host + unit | `unit` | ssh-cap | inverse |
| `nginx.reload` | edge host | — (config must already pass `nginx -t`) | ssh-cap `nginx-reload` | previous config is unchanged by this capability |
| `nginx.config.apply` | edge host | git tag of the nginx repo, file list | ssh-cap `nginx-apply` (backup, copy, `nginx -t`, reload) | restore backup + reload |
| `package.upgrade` | host | list of `name=exact_version` from a CVE finding | ssh-cap `pkg-upgrade` | `apt-get install name=prev_version` from local cache; snapshot of `dpkg --get-selections` |
| `host.reboot` | host | reason, max downtime | ssh-cap `reboot` | none (verify boots and services) |
| `disk.prune` | host | category ∈ {journal, rotated-logs, docker-images, apt-cache} | ssh-cap `prune` | none (only deletes reproducible data) |
| `docker.service.update` | swarm service on develop | `--force` | ssh-cap | previous spec |
| `ci.pipeline.cancel` | project + pipeline | rule id | gitlab-api | none (re-run) |
| `ci.job.retry` / `ci.job.play` | project + job (never deploy jobs of class production) | | gitlab-api | cancel |
| `ci.pipeline.create` | project + ref | | gitlab-api | cancel |
| `ci.variable.set` | project/group + key + scope | value from a secret ref, never inline | gitlab-api | previous value (kept encrypted by the broker) |
| `cert.deploy` | host + cert lineage | | ssh-cap | backup files + reload |
| `fixer.freeform` | `dev-*-fix`/`pre-*-fix` aliases | reviewed prompt | fixer (§6.4) | the fixer's own backups |
| `alert.silence` | site + matcher | duration ≤ policy max | monitoring writer | expire silence |

Never capabilities (refused by the broker's code, not just the policy): database writes on a
production-class target, `deploy` jobs of class production (CI engine project, `CICD_DEPLOY_JOBS`),
firewall/DNS changes, user/key management, anything on the argus-01 host's own `/etc/argus`.

## 4. Policy file

`/etc/argus/policy.json`, root 0600, edited by the operator (or a reviewed MR to a private config repo
that root installs). The broker validates it at start and on change; an invalid policy = broker refuses
all actions and the self-check goes FAIL.

```json
{
  "version": 1,
  "groups": {
    "prod-approvers": ["alex.doe", "oidc:group:sre-leads"],
    "dev-leads":      ["oidc:group:devops"]
  },
  "windows": {
    "prod-maint": {"tz": "Europe/Amsterdam", "days": ["sat","sun","mon","tue","wed"], "from": "02:00", "to": "05:00",
                    "freeze": [{"from": "2026-11-20", "to": "2026-11-30", "reason": "Black Friday"}]}
  },
  "rules": [
    {
      "id": "cve-dev-auto",
      "capability": "package.upgrade",
      "env": {"class": ["dev"]},
      "targets": {"hosts": "env:*"},
      "requesters": ["agent:cve-mitigator", "role:operator"],
      "approval": {"mode": "none"},
      "limits": {"per_day": 20, "per_target_per_day": 2, "max_targets_per_action": 5, "serial": true},
      "params": {"packages_from": "cve-finding", "allow_kernel": false},
      "dry_run": "required",
      "rollback": "auto",
      "notify": "digest"
    }
  ]
}
```

Rule fields:

| field | meaning |
|---|---|
| `capability` | one id from §3 (or a glob such as `ci.*`) |
| `env` | `class` and/or `name` from `environments.json`; unknown targets are class `production` |
| `targets` | `hosts` (`env:*`, `group:<name>` from `HOSTS_FILE` roles, explicit aliases), `units` (globs), `projects` |
| `requesters` | `agent:<name>`, `role:<role>`, `user:<name>` |
| `approval.mode` | `none` · `one` (one approver from `approval.groups`) · `two` (two distinct approvers, neither the requester, at least one from `approval.groups`) |
| `approval.window` | window id; outside it the action waits (queued, visible) or is refused (`on_outside: queue|refuse`) |
| `approval.ttl` | approval expires (default `ACTION_APPROVAL_TTL`) |
| `limits` | `per_day`, `per_target_per_day`, `max_targets_per_action`, `max_fraction_of_group` (e.g. 0.34), `serial`, `canary` (first N targets, then pause for `canary_soak`), `cooldown` |
| `dry_run` | `required` (always) — `skip` is not accepted for class `production` |
| `rollback` | `auto` (on failed verify) · `manual` (show commands) |
| `notify` | `silent` · `digest` · `card` · `page` |

Evaluation: first matching rule wins; no match = deny with the reason shown. The broker logs the rule id
with every decision.

## 5. Lifecycle of an action

```
request ─▶ validate ─▶ policy match ─▶ dry run (plan + diff, plan_hash) ─▶ approval(s) ─▶ window
   │          │ refuse                   │ refuse (precondition fails)      │ reject/expire   │ queue
   ▼          ▼                          ▼                                  ▼                 ▼
 audit      audit                      audit                              audit             execute
                                                                                              │
                                    rollback ◀── verify fails ◀── apply (per target, serial) ◀┘
                                       │                               │ ok
                                       ▼                               ▼
                                  audit + card                   audit + card + next target
```

1. **Request** (`settings-in/actions/<principal>/<id>.json`): capability, env, targets, params,
   justification, evidence links (incident / CVE finding / Jira key), requested_by.
2. **Validate**: schema, sizes, principal from the directory, targets exist in `HOSTS_FILE`, every target's
   environment computed from `environments.json` (not from the request), params against the capability's
   own validator (e.g. package versions must equal the CVE finding's fixed versions).
3. **Dry run**: the executor's `plan()` on each target (read-only): what would change (`apt-get -s`,
   `nginx -t` with the new file, the GitLab object's current state). Result + `plan_hash` stored.
4. **Approval**: in the app (`/actions/<id>`: plan, diff, evidence, policy rule, blast radius; Approve/Reject
   with CSRF, re-authentication within 10 min for production class), or in a Matrix DM as
   `approve A-123 <first 8 of plan_hash>` from a linked approver. Two-person = two distinct users.
5. **Window and limits** checked at execution time again.
6. **Execute**: per target, serial, canary first; the broker mints the credential (§7) for that target and
   that action only.
7. **Verify** (`verify()`): unit active, port listening, health URL, error rate on the edge via
   monitoring for N minutes; failed → `rollback()` → verify rollback → stop the remaining targets → page.
8. **Audit**: every step appended to `ACTION_AUDIT_LOG` (JSON lines, each with `prev_sha256`) and ops.db
   `actions`, `action_events`; a card in the notify room; a comment on the linked Jira issue.

Idempotency: request id = idempotency key; a retried request with the same id returns the stored result.

## 6. Executors

### 6.1 `ssh-cap` (host capabilities)

On every target that the policy can reach, the operator installs (with `admin/setup-action-access.sh`,
dry-run first, rollback built in — same pattern as `setup-fixer-access.sh`):

- user `argus-act` (locked password, no shell login other than the forced command);
- `/usr/local/sbin/argus-cap` (root-owned): the **only** command the user can run, via sshd
  `ForceCommand` for that user and the certificate's `force-command`. It reads the capability name and a
  JSON parameter document from stdin, validates them again with fixed regexes, and runs one fixed script
  from `/usr/local/lib/argus-cap/<capability>` (`service-restart`, `nginx-reload`, `pkg-upgrade`, …);
- `/etc/sudoers.d/argus-act`: `argus-act ALL=(root) NOPASSWD: /usr/local/lib/argus-cap/*` — and,
  per host, only the capability scripts the operator enabled there (the list in
  `/etc/argus-cap/enabled`, root-owned);
- a local allowlist file per capability (`/etc/argus-cap/units.allow`, package repositories allowed for
  upgrades), so even a compromised broker cannot restart an arbitrary unit;
- every capability script supports `--plan` (dry run), `--apply`, `--verify`, `--rollback <action-id>`,
  writes backups under `/var/lib/argus-cap/<action-id>/`, logs to syslog with the action id.

Target-side enforcement is the point: the argus-01 side can be wrong, and the target still runs only the
scripts the operator installed and enabled there (fixes OVERVIEW F2/F3 for writes).

### 6.2 `gitlab-api` (CI capabilities)

The broker holds `gitlab-write-token` (group access token, Maintainer, `api`, 30-day expiry, rotated by
the operator). Each capability maps to exactly one endpoint template (from `cicd_write.py`'s proposal
format) and a precondition check (e.g. cancel only when `upcoming_jobs` shows the pipeline holding the
lock and the release deploy is `manual` — the operator's existing rule). The model never sees the token;
`CICD_WRITE=1` in the environment of argus is removed.

### 6.3 `local` (argus-01)

Unit starts on argus-01 (today's `run-requests`, `apply-settings`) become capabilities `argus.task.run`,
`argus.settings.apply` with `approval: none` for admins — the existing dispatchers are the executors;
the broker only adds policy and audit. Migration without behaviour change.

### 6.4 `fixer` (free-form, develop / pre-release only)

Kept as capability `fixer.freeform` because it is useful on develop. Constraints are hard-coded in the
broker, not only in the policy: environment class must be `dev` or `staging`; `fix-scope` still root
written; network filter rendered from `environments.json`; `production` targets refused before policy
evaluation. It is **never** generalised to production: on production the approver could not know which
commands will run.

### 6.5 GitLab token scopes per capability

| capability | token | role / scope |
|---|---|---|
| reads | `gitlab-ro-token` (argus) | Reporter, `read_api` + `read_repository` |
| MRs on Argus's repo | `gitlab-bot-token` (argus) | project token, Developer, push `agent/*` only (protected branches) |
| variable metadata | `ci-vars-meta-token` (DynamicUser) | group Maintainer, `read_api` |
| `ci.*` | `gitlab-write-token` (broker only) | group Maintainer, `api`; project 210 refused in code |

## 7. Credential brokering

- **SSH:** an SSH certificate authority key on argus-01, root 0400 (`ssh-ca-key`; preferably held by an
  `ssh-agent` started by root, or a hardware token). Per action and target the broker generates a fresh
  key pair in memory and signs it:
  `ssh-keygen -s ca -I <action-id> -n argus-act -V +5m -O force-command="/usr/local/sbin/argus-cap"
  -O source-address=<ARGUS_HOST_IP> -O no-port-forwarding -O no-agent-forwarding -O no-x11-forwarding -O no-pty`.
  Targets trust the CA only for `argus-act` (`TrustedUserCAKeys` + `AuthorizedPrincipalsFile`), so no
  standing key for that account exists anywhere. sshd logs the certificate key id = action id.
  Revocation: `RevokedKeys` file pushed by the setup script; the 5-minute validity makes it rarely needed.
- **APIs:** tokens readable only by root, used only by the broker for one allowlisted call; expiry on
  every token; the credential check reports days left.
- **The model** receives no write credential; its environment has no token it could exfiltrate for
  writes. The argus read tokens move to credential files (CONFIG.md §4).

### 7.1 Temporary task secrets (ADR-0013 P3, shipped behind `TASK_SECRETS=off`)

| who | can | cannot |
|---|---|---|
| admin (web, MFA sensitive) | provide a secret for a develop / pre-release task: name, value, hosts, TTL ≤ 24 h | provide one for a release task (D3); read a value back |
| operator, viewer | see the metadata (name, hosts, who, expiry, uses, wiped) | provide or read a value (D2) |
| app (uid 65534) | write once into `/run/argus/secret-in/` (tmpfs, `root:nogroup 0730`) | read, list or delete what it wrote |
| root (`argus-secret-reaper`, board sync) | store AES-256-GCM on `/run/argus` with a per-task key; wipe | write a value to disk, a log, board.db or the backup |
| argus, inside `argus-task-run@<KEY>` only | run `curl` (GET/HEAD) through `argus-secret-exec`, the value as a credential file of a `DynamicUser` unit that reaches only the declared hosts; output redacted | see the value (env, argv, files, `/run/credentials`, `/run/argus` are denied), use it from a chat turn, intake, another task or the fixer |

**One-time operator step (not installed by `argus-deploy`: `/etc/sudoers.d/` is outside its allowed prefixes):**

```sh
# as root on argus-01, after the MR is deployed (argus-secret-exec installed, reaper timer enabled)
printf '%s\n' 'argus ALL=(root) NOPASSWD: /usr/local/sbin/argus-secret-exec' > /etc/sudoers.d/argus-secret-exec
chmod 0440 /etc/sudoers.d/argus-secret-exec && visudo -cf /etc/sudoers.d/argus-secret-exec
# rollback: rm /etc/sudoers.d/argus-secret-exec   (and Admin -> Task agent: Task secrets off)
```

The helper takes any arguments but refuses everything except the checks above (caller cgroup, running approved plan
naming the credential or its host, live and unexpired secret, hosts outside the production ranges, read-only curl).
Then turn it on in Admin → Task agent only after the edge → argus-01 hop is confirmed TLS or a trusted segment with
body logging off (ADR-0013 threat "Edge hop"). Audit: `settings-out/audit.log` (provided, refused, used, wiped:
metadata only) and board.db `task_secrets`.

## 8. Policies

### 8.1 Today (shipped default, equivalent to current behaviour)

```json
{
  "version": 1,
  "groups": {"admins": ["role:admin"]},
  "rules": [
    {"id": "argus-local", "capability": "argus.*", "env": {"name": ["argus-01"]},
     "requesters": ["role:admin", "role:operator"], "approval": {"mode": "none"},
     "limits": {"per_day": 40}, "notify": "silent"},
    {"id": "fixer-dev", "capability": "fixer.freeform", "env": {"class": ["dev"]},
     "requesters": ["role:admin"], "approval": {"mode": "one", "groups": ["admins"], "self_ok": true},
     "limits": {"per_day": 10, "concurrent": 1}, "notify": "card"},
    {"id": "fixer-pre", "capability": "fixer.freeform", "env": {"class": ["staging"]},
     "requesters": ["role:admin"], "approval": {"mode": "one", "groups": ["admins"], "self_ok": true},
     "limits": {"per_day": 10, "concurrent": 1}, "notify": "card", "enabled_by": "fix-scope"}
  ]
}
```

Production: no rule, therefore deny. `self_ok: true` reproduces today's "the admin who reviewed the
prompt approves it"; it is refused by the validator for class `production`.

### 8.2 Next step: CVE mitigation and CI hygiene

```json
{
  "version": 1,
  "groups": {
    "prod-approvers": ["user:alex.doe"],
    "approvers": ["role:approver", "role:admin"]
  },
  "windows": {
    "prod-maint": {"tz": "Europe/Amsterdam", "days": ["sat","sun","mon","tue","wed"], "from": "02:00", "to": "05:00",
                    "freeze": [{"from": "2026-11-20", "to": "2026-11-30", "reason": "sales event"}]}
  },
  "rules": [
    {"id": "cve-dev", "capability": "package.upgrade", "env": {"class": ["dev"]},
     "requesters": ["agent:cve-mitigator", "role:operator"], "approval": {"mode": "none"},
     "limits": {"per_day": 20, "per_target_per_day": 2, "max_targets_per_action": 5, "serial": true},
     "params": {"packages_from": "cve-finding", "allow_kernel": false},
     "dry_run": "required", "rollback": "auto", "notify": "digest"},

    {"id": "cve-pre", "capability": "package.upgrade", "env": {"class": ["staging"]},
     "requesters": ["agent:cve-mitigator", "role:operator"],
     "approval": {"mode": "one", "groups": ["approvers"], "ttl": "24h"},
     "limits": {"per_day": 10, "max_targets_per_action": 5, "serial": true, "canary": 1, "canary_soak": "15m"},
     "params": {"packages_from": "cve-finding", "allow_kernel": false},
     "dry_run": "required", "rollback": "auto", "notify": "card"},

    {"id": "cve-prod", "capability": "package.upgrade", "env": {"class": ["production"]},
     "requesters": ["agent:cve-mitigator", "role:operator"],
     "approval": {"mode": "one", "groups": ["prod-approvers"], "window": "prod-maint",
                  "on_outside": "queue", "ttl": "72h", "reauth": true},
     "limits": {"per_day": 6, "max_targets_per_action": 3, "max_fraction_of_group": 0.34,
                "serial": true, "canary": 1, "canary_soak": "30m", "cooldown": "2h"},
     "params": {"packages_from": "cve-finding", "allow_kernel": false, "require_kev_or_priority": "P1"},
     "dry_run": "required", "rollback": "auto", "notify": "page"},

    {"id": "restart-after-upgrade-prod", "capability": "service.restart", "env": {"class": ["production"]},
     "requesters": ["broker:cve-prod"], "approval": {"mode": "inherit"},
     "targets": {"units": ["northwind-*", "nginx"]}, "limits": {"serial": true}, "rollback": "manual"},

    {"id": "kernel-prod", "capability": "host.reboot", "env": {"class": ["production"]},
     "requesters": ["role:operator"],
     "approval": {"mode": "two", "groups": ["prod-approvers", "approvers"], "window": "prod-maint"},
     "limits": {"per_day": 2, "max_targets_per_action": 1}, "notify": "page"},

    {"id": "dev-lock-cancel", "capability": "ci.pipeline.cancel", "env": {"class": ["dev"]},
     "requesters": ["agent:cicd-guardian"], "approval": {"mode": "none"},
     "params": {"rule": "older-develop-holder"}, "limits": {"per_day": 20}, "notify": "digest"}
  ]
}
```

`approval.mode: inherit` lets a follow-up capability (restart after upgrade) run under the approval of
the parent action and its plan, never on its own.

## 9. What the operator must do before any production rule is enabled

1. Amend `GUARDRAILS.md` (context repo) and Argus's `CLAUDE.md`: "production writes only through the
   Argus action broker, per approved plan, per policy rule `<id>`".
2. Run `admin/setup-action-access.sh --dry-run` then for real on the chosen hosts (enables only the listed
   capability scripts there).
3. Install the CA public key on those hosts; keep the CA private key on argus-01 only.
4. Set `ACTIONS_MODE=dry-run` for at least one week: every request is planned and audited, nothing runs.
5. Set `ACTIONS_MODE=enforce` with only the class `dev` rules; then add rules one by one.

## 10. Break-glass

- Who: `admin` role, with a reason (≥ 20 chars) and a re-authentication.
- What: the requested action skips the **approval** step only; validation, dry run, limits, target-side
  enforcement, verify, rollback and audit still apply. Capped by `breakglass_ceiling` in the policy
  (default: `service.restart`, `nginx.reload`, `disk.prune`, `alert.silence`; never `package.upgrade`
  on more than one host, never `ci.variable.set`).
- Time-boxed: `ACTION_BREAKGLASS_TTL` (60 min), pages the notify room at start and end, opens a
  post-review task in the tracker automatically.

## 11. Audit trail

- `actions.jsonl`: `{ts, action_id, step, principal, capability, env, targets, rule_id, plan_hash,
  approvers, result, prev_sha256, sha256}`; the chain head is written to the state backup daily and to
  the monitoring logs (Loki) so tampering on argus-01 is detectable.
- ops.db `actions` / `action_events` for the app (`/actions`, filters by env, capability, principal).
- Target syslog has the same action id (certificate key id and `argus-cap` log line).
- Monthly report (brief): actions by capability/env, approvals, rollbacks, denials.
