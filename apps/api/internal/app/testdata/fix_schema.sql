CREATE TABLE alerts (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  active_at TEXT, severity TEXT, alertname TEXT, target TEXT
);
CREATE TABLE chat_messages (
  id INTEGER PRIMARY KEY,
  event_id TEXT NOT NULL UNIQUE,      
  room_id TEXT, room_name TEXT,
  source TEXT,                        
  sender TEXT, sender_name TEXT,      
  ts INTEGER, msgtype TEXT,
  body TEXT,                          
  body_chars INTEGER, redacted INTEGER DEFAULT 0,
  edit_of TEXT, reply_to TEXT,        
  run_id INTEGER, task TEXT,          
  disposition TEXT, reason TEXT,
  problem_idx INTEGER, incident_id INTEGER,
  alert_fp TEXT,                      
  reconstructed INTEGER DEFAULT 0,    
  recorded_at INTEGER
, issue_id INTEGER, thread_root TEXT);
CREATE TABLE fix_runs (
  id TEXT PRIMARY KEY,              -- request id <YYYYmmddHHMMSS>-<8 hex>
  requested_by TEXT, requested_at INTEGER,
  env TEXT,                         -- develop | pre-release (never release)
  hosts TEXT,                       -- JSON list of ssh aliases
  problem_run INTEGER, problem_idx INTEGER, problem_title TEXT,
  prompt_sha256 TEXT, prompt TEXT,  -- the exact prompt the model got (preamble + reviewed prompt)
  started_at INTEGER, finished_at INTEGER,
  status TEXT,                      -- OK | WARN | FAIL | ERROR
  outcome TEXT,                     -- fixed | not_needed | rolled_back | partial | failed | refused | error
  summary TEXT, report TEXT,
  actions TEXT, backups TEXT, verification TEXT, rollback TEXT,   -- JSON lists from the report
  cost_usd REAL, turns INTEGER, denials INTEGER, unit_result TEXT
);
CREATE TABLE incident_events (
  incident_id INTEGER NOT NULL REFERENCES incidents(id),
  at INTEGER NOT NULL,
  kind TEXT NOT NULL,                
  run_id INTEGER, idx INTEGER, task TEXT,
  actor TEXT, text TEXT
);
CREATE TABLE incidents (
  id INTEGER PRIMARY KEY,
  key TEXT NOT NULL,                 
  dc TEXT, subject TEXT, family TEXT,
  title TEXT, env TEXT, severity TEXT, alert_severity TEXT,
  customer_facing INTEGER DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'open', 
  owner TEXT,
  first_seen INTEGER, last_seen INTEGER, occurrences INTEGER DEFAULT 0,
  acked_at INTEGER, acked_by TEXT,
  resolved_at INTEGER, resolved_by TEXT, resolve_note TEXT,
  last_firing INTEGER, clear_since INTEGER,
  thread_root TEXT,                  
  fingerprints TEXT                  
, hub_service TEXT, hub_tier INTEGER, hub_callers INTEGER, sources TEXT, thread_room TEXT, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_ref TEXT, fix_at INTEGER);
CREATE TABLE qa_issues (
  id INTEGER PRIMARY KEY,
  incident_id INTEGER,
  first_run_id INTEGER, last_run_id INTEGER, problem_idx INTEGER,
  title TEXT, environment TEXT, severity TEXT,
  actionability TEXT, confirmed INTEGER DEFAULT 0, suspicious INTEGER DEFAULT 0,
  missing TEXT, questions TEXT, lang TEXT,
  reporter TEXT, reporter_name TEXT, room_id TEXT, ask_event TEXT, thread_root TEXT,
  ask_status TEXT,                   
  ask_note TEXT, question_text TEXT, question_event TEXT, asked_at INTEGER,
  answered_at INTEGER, answer_event TEXT, actionable_at INTEGER,
  first_ts INTEGER, last_ts INTEGER,
  reconstructed INTEGER DEFAULT 0, created_at INTEGER, updated_at INTEGER,
  log TEXT                           
);
CREATE TABLE rca_reports (
  id INTEGER PRIMARY KEY,
  incident_id INTEGER,
  title TEXT NOT NULL,
  path TEXT NOT NULL UNIQUE,         -- reports/rca/<date>-<slug>.md
  mr_url TEXT,
  created INTEGER NOT NULL,          -- unix seconds
  status TEXT NOT NULL,              -- open | merged | closed | local
  content TEXT,                      -- the markdown (capped at 400 kB)
  run_id INTEGER,
  source TEXT,                       -- rca-deep | log-rca | import | file
  cost_usd REAL,
  duration_s INTEGER
, redacted INTEGER DEFAULT 0, requested_by TEXT, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_at INTEGER);
CREATE TABLE runs (
  id INTEGER PRIMARY KEY,
  task TEXT NOT NULL,
  started_at INTEGER NOT NULL,      -- unix seconds
  duration_ms INTEGER,
  status TEXT,                      -- OK | WARN | FAIL | ERROR (first word of the result)
  summary TEXT,                     -- Claude's final answer
  cost_usd REAL,
  turns INTEGER,
  denials INTEGER,
  is_error INTEGER,
  mr_url TEXT,
  log_path TEXT
);
CREATE TABLE triage_problems (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  idx INTEGER, title TEXT, severity TEXT, environment TEXT, sources TEXT,
  summary TEXT, rca TEXT, fix_prompt TEXT, suspicious INTEGER
, fix_options TEXT, fix_state TEXT, fix_flags TEXT);
