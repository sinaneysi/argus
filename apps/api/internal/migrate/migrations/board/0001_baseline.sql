CREATE TABLE agent_inputs (key TEXT PRIMARY KEY, instructions TEXT, answers TEXT, post_jira INTEGER DEFAULT 1,
  by TEXT, at INTEGER);
CREATE TABLE agent_log (id INTEGER PRIMARY KEY, key TEXT, at INTEGER, kind TEXT, text TEXT);
CREATE TABLE agent_tasks (
  key TEXT PRIMARY KEY, state TEXT NOT NULL DEFAULT 'planning', phase TEXT,
  assignee TEXT, argus_user TEXT, context_hash TEXT, job_started INTEGER,
  plan_version INTEGER DEFAULT 0, plan TEXT, intake_at INTEGER, intake_cost REAL,
  approved_version INTEGER, approved_by TEXT, approved_at INTEGER,
  run_started INTEGER, run_finished INTEGER, run_cost REAL, result TEXT, fix_request TEXT,
  error TEXT, updated INTEGER, plan_entered_at INTEGER, spent_usd REAL DEFAULT 0,
  retry_at INTEGER, limit_since INTEGER, error_detail TEXT
);
CREATE TABLE board_issues (
  view TEXT NOT NULL,
  key TEXT NOT NULL, rank INTEGER NOT NULL, col INTEGER,
  summary TEXT, status_id TEXT, status TEXT, type TEXT, priority TEXT,
  assignee TEXT, assignee_name TEXT, labels TEXT, updated INTEGER,
  PRIMARY KEY (view, key)
);
CREATE TABLE board_moves (
  rid TEXT PRIMARY KEY, by TEXT, key TEXT, col INTEGER, transition TEXT, at INTEGER
);
CREATE TABLE board_qf (qf_id INTEGER NOT NULL, key TEXT NOT NULL, PRIMARY KEY (qf_id, key));
CREATE TABLE comments (
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, jira_id TEXT UNIQUE,
  author TEXT, body TEXT, created_at INTEGER, pending INTEGER DEFAULT 0
);
CREATE TABLE events (
  id INTEGER PRIMARY KEY, task_id INTEGER, at INTEGER NOT NULL, kind TEXT NOT NULL, actor TEXT, text TEXT
);
CREATE TABLE lessons (
  id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, service TEXT, env TEXT, kind TEXT, tags TEXT,
  norm_hash TEXT UNIQUE, state TEXT NOT NULL,
  source_key TEXT, source_run INTEGER, evidence TEXT, proposed_at INTEGER, decided_by TEXT, decided_at INTEGER,
  uses INTEGER DEFAULT 0, last_used INTEGER, unhelpful INTEGER DEFAULT 0, supersedes INTEGER,
  review TEXT
);
CREATE TABLE matrix_previews (
  pid TEXT PRIMARY KEY, hash TEXT, by TEXT, by_mxid TEXT, dm_room TEXT, dm_event TEXT, data TEXT, title TEXT,
  summary TEXT, requester TEXT, project TEXT, fwd_name TEXT, assignee TEXT, assignee_name TEXT, choices TEXT,
  users_error TEXT, state TEXT, task_id INTEGER, created INTEGER, updated INTEGER
);
CREATE TABLE matrix_requests (
  id TEXT PRIMARY KEY, kind TEXT, hash TEXT, task_id INTEGER, by TEXT, requester TEXT, room_name TEXT,
  dm_room TEXT, dm_event TEXT, answered INTEGER DEFAULT 0, created INTEGER
);
CREATE TABLE notices (id INTEGER PRIMARY KEY, at INTEGER NOT NULL, kind TEXT, task_id INTEGER, actor TEXT, text TEXT);
CREATE TABLE outbox (
  id INTEGER PRIMARY KEY, task_id INTEGER NOT NULL, kind TEXT NOT NULL,
  payload TEXT, created_at INTEGER NOT NULL, attempts INTEGER DEFAULT 0, next_at INTEGER DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'queued',
  last_error TEXT, done_at INTEGER
);
CREATE TABLE proposals (
  id INTEGER PRIMARY KEY, path TEXT NOT NULL, source_kind TEXT NOT NULL, source_id TEXT NOT NULL, source_url TEXT,
  title TEXT NOT NULL, description TEXT, evidence TEXT, fix_prompt TEXT, env TEXT, host TEXT, priority TEXT,
  severity TEXT, sev_rank INTEGER DEFAULT 0, labels TEXT DEFAULT '[]', assignee_suggestion TEXT, why TEXT,
  board_col INTEGER, hash TEXT, state TEXT NOT NULL DEFAULT 'pending',
  snooze_until INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, refreshed INTEGER DEFAULT 0,
  decided_at INTEGER, decided_by TEXT, reason TEXT, task_id INTEGER
);
CREATE TABLE state (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE task_chat (id INTEGER PRIMARY KEY, key TEXT NOT NULL, seq INTEGER NOT NULL,
  author_kind TEXT NOT NULL, author TEXT, kind TEXT NOT NULL, body TEXT NOT NULL, turn_id INTEGER, created INTEGER NOT NULL,
  UNIQUE(key, seq));
CREATE TABLE task_chat_state (key TEXT PRIMARY KEY, summary TEXT, budget_usd REAL, spent_usd REAL,
  budget_raised_by TEXT, updated INTEGER);
CREATE TABLE task_chat_turns (id INTEGER PRIMARY KEY, key TEXT NOT NULL, state TEXT NOT NULL,
  upto_seq INTEGER, requested_by TEXT, started INTEGER, finished INTEGER, cost REAL, run_id INTEGER, error TEXT);
CREATE TABLE task_secrets (id INTEGER PRIMARY KEY, key TEXT NOT NULL, name TEXT NOT NULL, purpose TEXT,
  hosts TEXT, state TEXT NOT NULL, requested_at INTEGER, provided_by TEXT, provided_at INTEGER, expires_at INTEGER,
  uses INTEGER DEFAULT 0, rotate_recommended INTEGER DEFAULT 0, wiped_at INTEGER, wipe_reason TEXT, UNIQUE(key, name));
CREATE TABLE task_seen (task_id INTEGER NOT NULL, principal TEXT NOT NULL, at INTEGER,
  PRIMARY KEY (task_id, principal));
CREATE TABLE tasks (
  id INTEGER PRIMARY KEY,
  jira_key TEXT UNIQUE,
  title TEXT NOT NULL,
  description TEXT, evidence TEXT, fix_prompt TEXT,
  status TEXT NOT NULL DEFAULT 'todo',
  jira_status TEXT,
  assignee TEXT, assignee_name TEXT,
  priority TEXT, labels TEXT DEFAULT '[]',
  env TEXT, host TEXT,
  source_kind TEXT, source_id TEXT, source_url TEXT,
  created_by TEXT,
  requested_by TEXT,
  created_at INTEGER, updated_at INTEGER, jira_updated INTEGER, synced_at INTEGER,
  fix_request TEXT, fix_state TEXT, fix_by TEXT, fix_at INTEGER,
  gone INTEGER DEFAULT 0
, jira_url TEXT, tracker TEXT NOT NULL DEFAULT 'jira', gl_project INTEGER, gl_iid INTEGER, gl_url TEXT, gl_ref TEXT);
CREATE INDEX agent_log_key ON agent_log(key, at);
CREATE INDEX comments_task ON comments(task_id, created_at);
CREATE INDEX events_task ON events(task_id, at);
CREATE INDEX lessons_state ON lessons(state, service);
CREATE INDEX matrix_previews_by ON matrix_previews(by, state);
CREATE INDEX matrix_requests_task ON matrix_requests(task_id);
CREATE INDEX outbox_state ON outbox(state, id);
CREATE UNIQUE INDEX proposals_open ON proposals(source_kind, source_id) WHERE state IN ('pending','snoozed');
CREATE INDEX proposals_state ON proposals(state, updated_at);
CREATE INDEX task_chat_turns_state ON task_chat_turns(state, id);
CREATE UNIQUE INDEX tasks_gl ON tasks(gl_project, gl_iid) WHERE gl_iid IS NOT NULL;
CREATE UNIQUE INDEX tasks_source ON tasks(source_kind, source_id) WHERE source_id IS NOT NULL;
CREATE INDEX tasks_status ON tasks(status, updated_at);
