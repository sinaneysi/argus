# Capability executor: implementation plan

Design: [ADR-0022](adr/0022-capability-executor.md) (proposed). Each step below is one pull request. Each one ships
with `ARGUS_ACTIONS_MODE=off` as the default, so it can merge and deploy without a change in behaviour. Each one has
its own rollback: revert the pull request (migrations only add tables and columns; nothing reads them after a revert).

Gates for every step (AGENTS.md): Go vet and tests with and without `-tags demo`, web lint, typecheck, tests and build,
`npm audit`, the leak check. Steps that touch a page also run `node demo/layout-sweep.mjs` (0 findings) and
`python3 demo/smoke.py`.

## Configuration keys (all steps)

| key | type | default | required | read by | validation |
|---|---|---|---|---|---|
| `ARGUS_ACTIONS_MODE` | `off` \| `dry-run` \| `on` | `off` | no | `actd`, worker (spinUp) | other values: `actd` refuses to start |
| `ARGUS_ACT_DIR` | path | `/var/lib/argus-act` | with mode ≠ off | `actd` | must exist, owner 65534, mode 0700 |
| `ARGUS_ACTIONS_APPROVAL_TTL` | duration | `24h` | no | `actd` | 5m..168h |
| `ARGUS_ACTIONS_PER_DAY` | int | `20` | no | `actd` | 1..200 |
| `ARGUS_ACTIONS_BACKUP_DAYS` | int | `30` | no | `actd` | 1..365 |
| `ARGUS_ACT_HTTP_HOSTS` | comma list of `[internal:]host[:port]`, `*.suffix` allowed | empty (`http.check` off) | for `http.check` | `actd`, worker (catalogue) | no IP literals, no bare `*` |
| `ARGUS_ACT_HTTP_ALLOW_HTTP` | `0` \| `1` | `0` | no | `actd` | plain http for test installs only |
| `ARGUS_ACT_GITLAB_URL` | https URL | the GitLab data source's address | for `gitlab.*` | `actd` | https, no path |
| `ARGUS_ACT_GITLAB_PROJECTS` | comma list of project paths or numeric ids | empty (`gitlab.*` off) | for `gitlab.*` | `actd`, worker | each resolves to one project at start |
| `ARGUS_ACT_KEYCLOAK_URL` | https URL | empty (`keycloak.read` off) | for `keycloak.read` | `actd` | https |
| `ARGUS_ACT_KEYCLOAK_REALMS` | comma list | empty | for `keycloak.read` | `actd`, worker | `^[A-Za-z0-9_.-]{1,64}$` each |
| `ARGUS_ACT_KEYCLOAK_CLIENT_ID` | string | empty | for `keycloak.read` | `actd` | the client secret is `secrets/keycloak/client.enc` |
| `ARGUS_ACT_KEYCLOAK_CLIENT_REALM` | realm name | empty: the only realm on `ARGUS_ACT_KEYCLOAK_REALMS` | with several realms | `actd`, worker | the client's realm; its token endpoint is the only POST allowed |
| `ARGUS_ACT_KEYCLOAK_MAX_READS` | int | `5` | no | `actd` | 1..50 user-returning queries per action |
| `auth_settings` `actions.stopped` | `0` \| `1` | `0` | no | `actd` before every step | set by the admin switch (PR 5) |

A capability whose allow-list is empty is not in the catalogue: the intake cannot plan it and `actd` refuses it.

## PR 0: confine the model's file reach (independent, security)

The CLI's `Read` / `Glob` / `Grep` can read the whole worker container today (ADR-0022 Context). Fix this first; it
is useful without the executor.

- Files: `apps/api/internal/model/model.go` (a default `Disallow` for every cli call:
  `Read(//var/lib/argus/**)`, `Read(//proc/**)`, `Read(//etc/**)` and the same for `Glob`, `Grep`; the exact rule
  syntax is checked against the pinned CLI version), `apps/api/internal/model/cli.go`, `internal/taskagent/runner.go`
  (nothing new; it inherits the default).
- Tests: `model_test.go`: `cliArgs` always carries the deny list, a caller cannot remove it; the fake CLI records the
  arguments.
- Acceptance: on the demo stack, a task whose text asks the model to read `keys/master.key` or `/proc/1/environ` gets
  a denied tool call in the run log, and the result holds no key material (the secret scan finds nothing).
- Rollback: revert.

## PR 1: contract, validation, refusals, audit, `http.check`, the `actd` service

- Files (new): `apps/api/internal/capability/{capability.go,schema.go,registry.go,refuse.go}` (D3, D6),
  `internal/capability/httpcheck.go`, `internal/capability/guard.go` (method + path `RoundTripper`),
  `apps/api/internal/actd/{actd.go,audit.go,status.go,store.go}` (the request loop on `actions/in`, the results on
  `actions/out`, the step results for idempotency, the hash chain, the heartbeat), `apps/api/cmd/argus/actd.go`
  (`argus actd`, `argus actd credential set|list`, `argus actd verify-audit`),
  `internal/migrate/migrations/ops/0005_actions.sql` (`actions`, `action_events`).
- Files (changed): `cmd/argus/main.go` (subcommand), `deploy/compose.yml` (service `actd`, profile `actions`, volume
  `argus-act`), `deploy/argus.example.env` (the keys above, commented), `docs/architecture/CONFIG.md`,
  `internal/selfcheck` (reads `actions/actd-status.json` when the mode is not `off`: heartbeat age, chain state).
- Tests:
  - schema: unknown key, wrong type, too long, pattern, secret field never in `Redacted()`.
  - refusals, one table test per D6 rule, with fixture `board.db` / `auth.db`: mode off, `actions.stopped=1`,
    classification `release`, `pre-release`, `none`; version mismatch; hash mismatch; approver not admin / disabled;
    approval too old; host not on the list; IP literal; redirect to another host; 11 steps; daily cap; legacy layout.
  - `http.check` against `httptest` servers: status match and mismatch, timeout, no body in the result.
  - guard: any method or path outside the template is refused before a connection.
  - audit: chain verifies; an edited line, a removed line and a reordered line are detected; a broken chain stops
    execution; secret fields are `<redacted>`.
  - idempotency: the same request twice gives one call and the same stored result.
- Acceptance: `docker compose --profile actions up -d` on the demo; a hand-written request file in `actions/in` for a
  fixture task with `http.check` runs once, writes `actions/out`, two audit lines and `actions` rows;
  `argus actd verify-audit` says OK; `ARGUS_ACTIONS_MODE=off` refuses it with a reason; the self-check shows `actd`.
- Rollback: stop the profile; revert. The `argus-act` volume can stay (nothing reads it).

## PR 2: board wiring (plan hash, capability steps, operator state)

- Files: `internal/migrate/migrations/board/0002_plan_hash.sql` (`agent_tasks.plan_sha256`, `approved_sha256`,
  `action_id`; `agent_actions` for the step results the page shows), `internal/board/agent.go`:
  - `validatePlan`: steps keep `capability` and `args` when `capability.Validate` passes; mode `operator`; a WRITE
    step with an invalid capability keeps `capability: ""` (today's write-report path) and the reason.
  - `plan_sha256` at ingest; `agent-start` checks `plan_sha256` as well as `plan_version`.
  - `spinUp`: new path `execute` (classification `develop`, mode ≠ off, every WRITE step has a valid capability);
    `release` / `pre-release` paths unchanged.
  - `dispatchRun`: `execute` writes `actions/in/<action-id>.json`; `agentTick` reads `actions/out` and then writes the
    run job with `capability_results`; state `operator` and request `agent-continue`; request `agent-rollback`.
  - `finish`: the capability table appended by Go.
- Security requirements from the PR 1 review (ADR-0022 amendment 1):
  - approval signature: the api signs (task key, plan version, plan sha256, approver, approved at, sha256 of the
    sealed inputs) with a key only the api holds. `actd` gets the public key in its own volume and refuses any step
    without a valid signature. `approved_sha256` alone is not enough, because the worker can write `board.db`.
  - the board migration adds `approved_sha256`. Until then `actd` refuses every step.
- Tests (`agent_test.go`): `spinUp` table (every env × mode × step mix); the hash changes when one argument changes;
  approval with a stale hash is refused; an operator step stops the action and `agent-continue` resumes at the next
  step; a `release` plan with a valid capability step is still a proposal; the comment table comes from
  `actions/out` even when the model's report says something else.
- Acceptance: demo fixtures: a develop task with two `http.check` steps and an operator step between them ends in
  `operator`, then `agent-continue` ends `done`, with one audit record per phase.
- Rollback: revert; the new columns stay unused.

## PR 3: `gitlab.ci-variable.get|set|delete`, sealed inputs

- Files: `internal/capability/gitlabvar.go`, `internal/actd/seal.go` (X25519 key pair, `actions/actd.pub`, open),
  `apps/api/internal/board` (sealed inputs pass through `agent-start` into the action request, never opened),
  `apps/api/internal/app` (the approve route seals each `input:<NAME>` to `actd.pub`), `apps/web/app/(app)/board/task/page.tsx`
  and `board/actions.ts` (write-only input fields on the approval form, `autocomplete=off`, never echoed).
- Tests: a GitLab stub (`httptest`, also in `demo/clean-install/stubs.py`) records every call:
  - scope `develop` only: `*`, empty, `pre-release`, `release`, `Develop` refused before a call.
  - project not on the list refused; a numeric id and a path of the same project are one project.
  - set: create, update, unchanged (no call), backup written before the update, verify mismatch triggers rollback.
  - delete: backup, delete, verify 404; rollback recreates the value and the attributes.
  - the value never appears in: the result, `actions/out`, the audit, `actions` rows, logs (a test greps them all),
    the run job file, the board comment.
  - precondition: the variable changed between plan and apply → refused, nothing written.
  - the guard refuses `POST /api/v4/projects/:id/pipeline` and every jobs path even when a capability asks for it.
- Acceptance: against a stage GitLab project on the allow-list, with a project token in `argus-act`: set, verify,
  manual rollback, delete, rollback; the GitLab audit events match `actions.jsonl`.
- Rollback: empty `ARGUS_ACT_GITLAB_PROJECTS` (the capability leaves the catalogue), then revert.

## PR 4: `keycloak.read`

- Files: `internal/capability/keycloak.go` (client-credentials token, cached until 30 s before expiry; the four
  queries with fixed result fields; users carry `first_name_set` / `last_name_set` booleans, never the names), the guard templates, `demo/clean-install/stubs.py` (identity provider stub).
- Tests: realm not on the list refused; every query's result has only its fixed fields (a stub returns e-mail,
  attributes and a bind credential; none of them pass); search length cap; 50-row cap; the token is never in a
  result or log; a `PUT` / `POST` / `DELETE` under the realm is refused by the guard.
- Acceptance: against a test realm: the four queries return the expected rows; the audit has the queries.
- Rollback: empty `ARGUS_ACT_KEYCLOAK_URL`.

## PR 5: prompts and the UI

- Files: `internal/taskagent/prompts/task-intake.md`, `task-run.md` (ADR-0022 D9), `internal/board/agent.go` (the
  `capabilities` catalogue in the job file, built from the registry and the allow-lists), the task page (the plan
  shows each step's capability and arguments, secret fields as "entered at approval"; the hash prefix next to the
  version; the capability table with results and audit ids; the `operator` state with Continue; Roll back per
  write step with re-authentication), `apps/web/app/(app)/admin/board/` (Settings → Task agent: the mode, read
  only; the switch "Stop all actions"; the allow-lists, read only; the `actd` heartbeat), `packages/design/DESIGN.md`
  §10 checklist followed.
- Tests: prompt snapshot tests (`runner_test.go`); `validatePlan` on recorded model outputs (good, a literal secret
  value → refused, a pipeline step as WRITE → turned into a write-report, injection text asking for `*` scope →
  refused); web tests for the write-only field and the state labels; layout sweep 0 findings.
- Acceptance: on the demo, a real model intake of a synthetic task "rotate variable X on develop and check the health
  page" plans `gitlab.ci-variable.set` with `value_ref`, an operator step "start the develop pipeline" and an
  `http.check`; approval, execution, `operator`, Continue, `done`; the report and the comment hold no value.
- Rollback: revert; `ARGUS_ACTIONS_MODE=off` alone already restores today's behaviour.

## Order and switch-on

1. PR 0, PR 1, PR 2 merge with the mode `off`.
2. The operator sets `ARGUS_ACTIONS_MODE=dry-run` and the `actions` profile on the pilot for one week: every action
   only plans. Check: the dry-run comments match what a person would do.
3. PR 3, PR 4, PR 5. The operator sets the token and the allow-lists, then `on`.
