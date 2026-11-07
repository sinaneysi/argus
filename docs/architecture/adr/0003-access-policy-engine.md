# ADR-0003: An access policy engine with a root action broker

Status: proposed, 2026-10-22.

## Context
Today every write path has its own root dispatcher with hand-written checks: `run-requests`,
`apply-settings`, `fix-requests`, board sync, backup plans, `cicd_write.execute()` (disabled). The fixer
is a model with `NOPASSWD: ALL` on develop/pre-release hosts. The operator's goal is scoped production
action (first CVE mitigation), while the guardrails require explicit approval of each production change.

## Decision
- One root daemon/oneshot `argus-actd` (the **broker**) evaluates a root-owned `policy.json`:
  capability × target × environment (class from `environments.json`) × requester → approval mode
  (none / one / two-person / window), limits (rate, blast radius, serial, canary), dry run, rollback,
  notification.
- Every capability has an executor with `plan / apply / verify / rollback`; approval binds to the plan's
  hash; the broker re-checks policy, window and limits at execution time.
- Agents (model runs) can only *request*; their identity is the request directory, not a field.
- Production capabilities are enforced on the target: forced command `argus-cap` + per-capability
  sudoers + local allowlists, installed and enabled per host by the operator (ADR-0007).
- Existing dispatchers become executors behind the broker without behaviour change (policy §8.1
  reproduces today).
- Hard-coded refusals in the broker code (not policy): production DB writes, production deploy jobs,
  firewall/DNS, access management, free-form fixer on production.
- `ACTIONS_MODE=off|dry-run|enforce` for rollout.

## Alternatives rejected
- **Give the fixer production with approval of the prompt:** the approver approves an intention, not the
  commands; injection through evidence could steer it. Rejected.
- **OPA/Rego or Cedar:** expressive, but an extra runtime and language to review for a dozen rules; the
  policy shape is small enough for a JSON schema and ~300 lines of evaluator with exhaustive tests. Can be
  revisited if rules exceed ~100.
- **Ansible/AWX as the executor:** heavy, a second control plane with its own credentials; the capability
  scripts are a few dozen lines each.

## Consequences
- One audit trail and one approval UI for every write, today's included.
- New capabilities are: a target script, an executor binding, a validator, a policy rule — each
  reviewable on its own.
- The broker becomes a high-value component: root, minimal, stdlib-only, no code from the agent's clone,
  its own tests and its own install step.
