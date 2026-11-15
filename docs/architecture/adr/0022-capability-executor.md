# ADR-0022: Capability executor: fixed write capabilities run by Go, never by the model

Status: accepted, 2026-11-15 (operator answers O7–O10). A first, small slice of the argus-2 roadmap P5 "Action broker" (ACCESS.md, ADR-0003).
Builds on ADR-0021 (self-contained deployment) and the task agent of ADR-0021 "As built (P3, the task agent)".
Implementation plan: [`../capability-executor-plan.md`](../capability-executor-plan.md).

## Decisions taken with the operator (2026-11-15)

- **O1.** Write power comes from fixed capabilities executed by Go code. The model never gets a shell or a write
  tool. A WRITE step of the approved plan names a capability and its arguments; after an admin approves that exact
  plan version, Go validates and runs it.
- **O2.** First set: `gitlab.ci-variable.get|set|delete` (project CI/CD variables, environment scope `develop` only;
  old value backed up; values never logged, never given to the model, never shown), `http.check` (GET, compare the
  status code; host on an allow-list), `keycloak.read` (read-only admin queries of an identity provider realm).
- **O3.** Never start a pipeline or play a job. Such a step is "for the operator"; the run stops there with the state
  "needs the operator", and a later verify run checks the result.
- **O4.** Production (`release`) and `pre-release`: no capability executes. Proposal only, as today.
- **O5.** Every executed capability writes an audit record. Rollback is a capability call too.
- **O6.** A kill switch disables all capability execution.
- **O7.** GitLab credential: one group access token (role Maintainer, scope `api`, short expiry), stored only in
  `argus-act`. The `ARGUS_ACT_GITLAB_PROJECTS` allow-list and the path guard (D4) still limit it to the listed
  projects and the variables endpoints.
- **O8.** Read-tier capabilities (`gitlab.ci-variable.get`, `http.check`, `keycloak.read`) may run for any
  environment, so a proposal for `pre-release` or `release` can show live state. Write-tier capabilities run only
  for `develop` (O4 stays for writes).
- **O9.** One enabled admin approves a plan version and hash; the approval expires after 24 h.
- **O10.** The separate `actd` service with its own volume and env-only allow-lists is accepted.

## Context

- The task agent plans (intake) and runs (run) board tasks. `board/agent.go` `spinUp` sends production to a
  proposal and every WRITE step on `develop` / `pre-release` to a read-only "write-report"; nothing is executed. The
  run gets only `Read`, `Glob`, `Grep` (`taskagent/runner.go`), and `prompts/task-run.md` says "this run changes
  nothing". The fixer is not ported (`agent-fixer-move` is refused).
- Approval binds to `plan_version` (an integer that grows when the classification or the steps change). It does not
  bind to a hash of the plan.
- The worker container is `read_only`, `cap_drop: ALL`, `no-new-privileges`, runs as uid 65534, and has no SSH and no
  Docker socket. The model CLI runs **inside the worker, as the same uid**, with a clean environment.
- **Gap this ADR must not widen:** the CLI's `Read` / `Grep` are not confined to the working folder. A model run can
  read every file the worker can read: the volume (`keys/master.key`, sealed secrets) and probably
  `/proc/1/environ`. Today that exposes read credentials. A write credential in the same place would be readable by
  the model. So the write credential must not live where the worker can read it.
  Closed by PR 0 (plan): every CLI call carries `--restricted` and Read deny rules for the volume, `/proc`, `/sys`,
  `/etc`, `/dev`, and `argus model-exec` applies Landlock before the CLI starts (only the working folder, HOME,
  programs and CA files; other processes' `/proc` entries closed). Without Landlock a call fails closed unless
  `ARGUS_MODEL_SANDBOX=off`. The write credential still stays out of the worker (D1): defence in depth.

## Decision

**D1. One executor process, `argus actd`, in its own optional service.** It runs from the same `argus` image
(`entrypoint: ["/app/argus", "actd"]`, compose profile `actions`, the same hardening anchor, no published port). It
mounts the shared volume `argus-data` and a second volume `argus-act` (`/var/lib/argus-act`) that **no other service
mounts**. Every capability runs in `actd`, the read-only ones too, so there is one validator, one audit and one kill
switch. Without the `actions` profile, the install has the three services of ADR-0021 D1 and nothing changes. This
amends ADR-0021 D1 (an optional fourth service) and keeps S1 (Docker only, no host job).

**D2. Credentials only in `argus-act`.** Layout (owner 65534, files 0600, folders 0700):

| path | holds |
|---|---|
| `keys/act.key` | the executor's AES-256 key (`ARGUS_ACT_KEY` wins when set); created at first start |
| `keys/act-x25519.key`, `/var/lib/argus/actions/actd.pub` | the executor's sealing key pair; the public half is in the shared volume |
| `secrets/<capability-family>/<name>.enc` | the GitLab write token, the identity provider client secret (`internal/secret` format, sealed with `act.key`) |
| `backups/<action-id>/<step>.enc` | old values, sealed, additional data = action id, step, project, key, scope |
| `audit/actions.jsonl` | the authoritative audit chain (D7) |

A credential enters only through `docker compose exec -T actd argus actd credential set <name>` (value on stdin,
never an argument). Not the web UI, not the api, not the worker. `actd` writes names, last four characters and
check results to `actions/actd-status.json`, never a value.

**D3. The capability contract (`internal/capability`, stdlib only).** One Go package, used by the worker to validate
the plan and by `actd` to validate again before it runs anything:

```go
type Capability interface {
    Name() string                 // "gitlab.ci-variable.set"
    Tier() Tier                   // Read | Write
    Schema() Schema               // the fields: type, required, pattern, max length, secret, allow-list key
    Validate(args Args, env Env) error                 // schema + allow-lists + env rule; no network
    Plan(ctx, args) (Plan, error)                      // read-only: current state, what would change, precondition digest
    Apply(ctx, args, plan Plan) (Result, BackupRef, error) // refuses when the precondition digest changed
    Verify(ctx, args, plan Plan) (Result, error)
    Rollback(ctx, ref BackupRef) (Result, error)       // nil for Read-tier capabilities
}
```

- **Arguments** are a flat JSON object, at most 2 KB, unknown keys refused. A field marked `secret` never appears
  in the plan, the audit, the result or a log. A secret value is never a literal in the plan: the plan holds a
  reference `"value_ref": "input:<NAME>"`, and the admin types the value at approval (D4).
- **Environment:** a capability declares the environments where it may run. In this slice every capability runs
  only when the task's classification is `develop` (O4). `gitlab.ci-variable.*` also requires the argument
  `environment_scope` to be exactly `develop`.
- **Idempotency:** the action id is `<task key>.v<plan version>.<plan sha256[:12]>`; the step id is `<action id>/<n>`.
  `actd` stores each step's result; a repeated request for a finished step returns the stored result and does not
  call the target again. `Apply` is a no-op ("unchanged") when `Plan` found the target already in the wanted state.
- **First set:**

| capability | tier | arguments | plan / apply / verify / rollback |
|---|---|---|---|
| `http.check` | Read | `url` (https, host on `ARGUS_ACT_HTTP_HOSTS`), `expect_status` (int or list), `timeout_sec` ≤ 30 | one GET, no credentials, no custom headers, no redirect to another host; result: status, latency, matched. No body. |
| `gitlab.ci-variable.get` | Read | `project` (on `ARGUS_ACT_GITLAB_PROJECTS`), `key` (`^[A-Za-z0-9_]{1,255}$`), `environment_scope` = `develop`, optional `matches` = `input:<NAME>` | result: exists, variable type, protected, masked, raw, and `matches` true/false when asked. Never the value, its length or a hash. |
| `gitlab.ci-variable.set` | Write | as `get`, plus `value_ref`, `protected`, `masked`, `raw`, `variable_type` | plan: created / updated / unchanged (attributes only) and a digest of the current value; apply: seal the old value to `backups/`, then create or update with `filter[environment_scope]=develop`; verify: read back and compare inside `actd` (constant time); rollback: restore the old value and attributes, or delete when it did not exist |
| `gitlab.ci-variable.delete` | Write | `project`, `key`, `environment_scope` = `develop` | plan: exists or not; apply: back up, then delete with the scope filter; verify: 404; rollback: create again from the backup |
| `keycloak.read` | Read | `realm` (on `ARGUS_ACT_KEYCLOAK_REALMS`), `query` = `users` (search, max 50) \| `user` \| `ldap-mappers` \| `client-scopes`, `search` (max 100 chars) | GET only under `/admin/realms/<realm>/`; result fields fixed per query (user name, enabled, federation link, required actions, group names,
`first_name_set` / `last_name_set` booleans (never the names); mapper name, type, non-secret config; scope name, protocol, mapper names). No e-mail, no attributes, no credential fields. |

Each family's HTTP client wraps a guard `RoundTripper` that allows only fixed method + path templates (GitLab:
`GET|POST|PUT|DELETE /api/v4/projects/:id/variables[/:key]`; identity provider: the token endpoint and `GET`
under the realm). A pipeline, job or any other endpoint cannot be called even by a bug in a capability.

**D4. Approval binds to the plan hash, and secret inputs are sealed to the executor.**

- At intake the worker computes `plan_sha256` = SHA-256 of the canonical JSON (sorted keys) of `{classification,
  plan}` and stores it with `plan_version` (board migration). The task page shows the first 12 characters.
- Spin up sends `plan_version` **and** `plan_sha256`. Both must equal the stored ones; `approved_sha256` is stored with
  `approved_version`, `approved_by`, `approved_at`.
- For each `input:<NAME>` in the plan, the approval form shows a write-only field. The web/api seals the value to
  `actions/actd.pub` (X25519 ECDH + HKDF-SHA256 + AES-256-GCM, additional data = action id + step + name; stdlib
  `crypto/ecdh`, `crypto/hkdf`). Only the ciphertext passes through the request file, the worker and board.db. The
  model's `Read` of those files gets ciphertext only.

**D5. Data flow.**

```
intake (model, read-only) ─▶ validatePlan: steps {mode: read-only|WRITE|operator, capability, args}
   ─▶ capability.Validate (worker) ─▶ plan_sha256 ─▶ admin approves version + hash (+ sealed inputs)
   ─▶ worker writes actions/in/<action-id>.json (0600): task key, version, hash, approver, steps, sealed inputs
   ─▶ actd: refusal rules (D6) ─▶ per step in order: Validate again ─▶ Plan ─▶ Apply ─▶ Verify
          (verify fails ─▶ Rollback ─▶ verify rollback ─▶ stop)  ─▶ audit (D7) per phase
   ─▶ actions/out/<action-id>.json: redacted results only
   ─▶ worker: run job file gets capability_results ─▶ run model writes the report (read-only, as today)
   ─▶ board comment: Go appends the capability table (step, capability, result, audit id), built from
      actions/out, not from the model's text
```

- `actd` reads `board.db` and `auth.db` read-only to confirm, from the databases and not from the request: the task's
  classification, `approved_version = plan_version`, `approved_sha256 = sha256(stored plan)`, and that the approver
  is an enabled admin.
- An `operator` step (start a pipeline, play a job, anything without a capability) stops the run: steps after it
  wait, the task state is `operator` ("needs the operator"), the comment lists the exact action for the operator.
  `agent-continue` (an admin, same plan version and hash) runs the remaining steps, normally the verify steps.
- A WRITE step without a valid capability keeps today's path: the run reports it for a person (`write-report`).
- `ARGUS_ACTIONS_MODE=dry-run`: `actd` runs only `Plan` for each step and the comment shows what would change.

**D6. Refusal rules, hard-coded in `actd` and checked before every step** (not configurable):

1. Mode `off`, the kill switch (D8), or a broken audit chain.
2. A Write-tier step whose classification is not `develop`, or any step whose capability environment rule fails;
   Write-tier steps for `release` and `pre-release` are refused before any other check. Read-tier steps may run
   for every environment (O8).
3. Approval: not approved, version or hash mismatch, approver not an enabled admin, approval older than
   `ARGUS_ACTIONS_APPROVAL_TTL`.
4. Unknown capability, an argument outside the schema, a target outside its allow-list, `environment_scope` other
   than `develop` (also `*`, empty, `pre-release`, `release`).
5. A precondition changed between `Plan` and `Apply` (the current value's digest differs).
6. Limits: more than 10 capability steps in a plan, more than `ARGUS_ACTIONS_PER_DAY` actions a day, one action at a
   time, 30 s per call.
7. The legacy layout (`ARGUS_LAYOUT=legacy`): `actd` does not start.

A refusal is audited and shown on the task page with its reason. A refused step stops the action.

**D7. Audit.** One record per phase (plan, apply, verify, rollback, refused):
`{ts, action_id, step, task_key, approver, plan_version, plan_sha256, capability, args (secret fields =
"<redacted>"), phase, result, detail (secret-scanned, 300 chars), backup_ref, prev_sha256, sha256}`.

- Authoritative: `argus-act/audit/actions.jsonl` (append, fsync, hash chain). `actd` checks the chain at start and
  every hour; a broken chain stops execution and is a self-check failure.
- For the UI: ops.db tables `actions` and `action_events` (ops migration), written by `actd`, read by the api.
- The chain head goes to `actions/actd-status.json` with the heartbeat, so the backup of `argus-data` holds it too.
- Rollback data: `backup_ref = backup:<action-id>/<step>`. Backups are kept `ARGUS_ACTIONS_BACKUP_DAYS` (30).
- A manual rollback is the request `agent-rollback {action_id, step}` (an admin, re-authentication): `actd` runs the
  capability's `Rollback` with that reference, under D6, and audits it as phase `rollback`.

**D8. Kill switch, three layers.** (1) `ARGUS_ACTIONS_MODE=off|dry-run|on`, default `off`, read at start. (2) The
admin switch "Stop all actions" in Settings → Task agent, `auth_settings` key `actions.stopped`, read by `actd`
before every step, so it acts within one step. (3) `docker compose stop actd`. Any layer that says stop wins.

**D9. Prompts.**

- `task-intake.md`: the task file gets `capabilities` (name, tier, schema, the allow-lists, the env rule). A WRITE
  step must name one capability and its arguments; a step that needs a pipeline or a job is `mode: operator`;
  verify steps use `http.check` or `gitlab.ci-variable.get`; a secret value is never written, only
  `value_ref: input:<NAME>`; outside `develop`, WRITE steps stay a proposal.
- `task-run.md`: "This run changes nothing" becomes: "You change nothing. Argus ran the approved capability steps
  before this run; their results are in `capability_results`. Report them as they are. Never repeat a step, never
  guess a value, never claim a change that is not in `capability_results`." Production stays a proposal.
- `task-chat.md`: unchanged (chat never executes).

## Amendment 1 (2026-11-15, security review of PR 1)

As built in PR 1, these points change D2, D6 and D7:

- **Executor key:** only `argus-act/keys/act.key`. `ARGUS_ACT_KEY` is dropped, because `argus.env` is shared by
  every service.
- **Audit chain:** an HMAC-SHA-256 chain keyed from `act.key`. The members keep the D7 names `prev_sha256` / `sha256`.
  The head, the record count and the size are also kept in `argus-act/audit/anchor.json` (HMAC-tagged). A missing,
  unreadable or non-matching anchor while records exist is a broken chain. The whole chain is verified at start and
  every hour. Before each step a cheap check runs: same size, same last line, same anchor.
- **Approval:** `actd` refuses (rule 3) when `agent_tasks.approved_sha256` does not exist, until PR 2 adds it. It
  also refuses unless the task state is `approved` or `running`. The task is read again before every step.
- **Steps:** an operator step names no capability. A Write-tier capability runs only in a WRITE step. Step numbers
  must be positive and unique.
- **Interrupted apply:** a marker is written before each write-tier Apply. A retry that finds the marker and no
  stored result is refused (`interrupted`, rule 5): a person checks the target first.
- **Daily cap:** dry-run actions count towards `ARGUS_ACTIONS_PER_DAY`.
- **`http.check`:** allow-list entries are `[internal:]host[:port]`. Without a port, only 443 (and 80 with plain
  http) is allowed. After resolution, loopback, link-local, private, shared (100.64/10), unique local, multicast and
  unspecified addresses are refused unless the entry starts with `internal:`. Query parameters with credential-like
  names are refused. Every step's arguments pass the secret scan.
- **One executor:** `New` takes an exclusive lock on the executor volume, so `-once` and the daemon never run at the
  same time.

Design items for PR 2 (not built in PR 1):

- **A. Approval authenticity.** The worker runs as the same uid as the api, and `board.db` and `auth.db` are
  writable by it, so `approved_*` can be forged. The api signs each approval (task key, plan version, plan sha256,
  approver, time) with a key that only the api holds. `actd` gets the public key, verifies the signature and
  refuses a step without one. Until PR 2 lands, `actd` refuses every step: without `approved_sha256` the approval
  check fails.
- **B. Sealed inputs.** The hash of `sealed_inputs` is part of the signed approval (A), so a sealed input cannot be
  replaced after approval.

## Relation to other decisions

- **ADR-0003 / ACCESS.md:** this is the broker's first slice. `actd`, plan/apply/verify/rollback, approval by plan
  hash, the audit chain and `ACTIONS_MODE` come from there. Not yet: `policy.json` (the policy is fixed in code plus
  the allow-lists), approver groups, two-person approval, windows, Matrix approval. `actd` runs as uid 65534 in a
  container, not as root, because nothing in this slice needs root.
- **ADR-0007:** host capabilities over SSH certificates are out of this slice. They come later as more capabilities
  behind the same contract.
- **ADR-0019 / ADR-0020:** the identity provider and GitLab are API calls, not broker sessions. When machine
  identities (ADR-0020) exist, a capability family may take its credential from the broker instead of `argus-act`.
- **ADR-0021:** amends D1 (optional fourth service); reuses the D4 sealing format with a key of its own.

## Alternatives rejected

- **A shell or write tools for the model, with a narrow prompt.** The approver approves an intention, not the
  commands. Tracker text can steer the model (prompt injection). The model would hold the credential.
- **The executor inside the worker.** Simplest, but the model runs in the worker as the same uid and can read its
  files; a write token there is readable by the model.
- **The executor inside the api.** The api is internet-facing; the same volume problem.
- **Secret values as literals in the plan.** The model would author them and the plan page would show them (O2).
- **Per-project access tokens.** Smaller blast radius, but one token to create and rotate per project; the operator
  chose one group token (O7), limited in code by the project allow-list and the path guard.

## Consequences

- With the `actions` profile off or `ARGUS_ACTIONS_MODE=off` (the default), behaviour is today's.
- One more volume to back up (`argus-act`). Losing `act.key` makes the backups unreadable; the credentials must be
  set again.
- A new capability is: a Go type in `internal/capability`, its allow-list key, its guard paths, tests, an intake
  catalogue entry. Review one capability at a time.
- The model's file reach in the worker stays a separate risk; the plan's first step confines it.
