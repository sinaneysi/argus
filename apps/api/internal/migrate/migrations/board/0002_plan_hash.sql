-- The capability executor's board wiring (ADR-0022 D4, D5, amendment 1). agent_tasks gains the plan hash (computed
-- at intake), the approved hash, the api's signature of the approval, the action id, the signed continue after an
-- operator step and the time a manual rollback was requested. agent_actions holds the step results of an action
-- (from actions/out) for the task page and the comment. A column that the board sync already added is skipped.
ALTER TABLE agent_tasks ADD COLUMN plan_sha256 TEXT;
ALTER TABLE agent_tasks ADD COLUMN approved_sha256 TEXT;
ALTER TABLE agent_tasks ADD COLUMN approval_sig TEXT;
ALTER TABLE agent_tasks ADD COLUMN action_id TEXT;
ALTER TABLE agent_tasks ADD COLUMN action_state TEXT;
ALTER TABLE agent_tasks ADD COLUMN continue_step INTEGER;
ALTER TABLE agent_tasks ADD COLUMN continue_by TEXT;
ALTER TABLE agent_tasks ADD COLUMN continue_at INTEGER;
ALTER TABLE agent_tasks ADD COLUMN continue_sig TEXT;
ALTER TABLE agent_tasks ADD COLUMN rollback_started INTEGER;
CREATE TABLE IF NOT EXISTS agent_actions (
  action_id TEXT NOT NULL, step INTEGER NOT NULL, key TEXT NOT NULL, capability TEXT, phase TEXT, status TEXT,
  summary TEXT, detail TEXT, backup_ref TEXT, audit_sha256 TEXT, at INTEGER,
  PRIMARY KEY (action_id, step)
);
CREATE INDEX IF NOT EXISTS agent_actions_key ON agent_actions (key, action_id, step);
