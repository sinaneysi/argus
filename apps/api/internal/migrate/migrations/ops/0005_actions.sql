-- The capability executor (ADR-0022 D7): one row per action and one per audited phase, written by argus actd and
-- read by the api. The authoritative record is argus-act/audit/actions.jsonl; these rows are its copy for the UI.
CREATE TABLE IF NOT EXISTS actions (
  action_id TEXT PRIMARY KEY, task_key TEXT NOT NULL, plan_version INTEGER, plan_sha256 TEXT, approver TEXT,
  mode TEXT, state TEXT NOT NULL, reason TEXT, steps INTEGER, started INTEGER, finished INTEGER
);
CREATE TABLE IF NOT EXISTS action_events (
  id INTEGER PRIMARY KEY, action_id TEXT NOT NULL, step INTEGER, capability TEXT, phase TEXT NOT NULL, result TEXT,
  detail TEXT, args TEXT, audit_sha256 TEXT, at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS action_events_action ON action_events (action_id, id);
CREATE INDEX IF NOT EXISTS actions_task ON actions (task_key, started);
