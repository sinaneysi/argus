CREATE TABLE alerts (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  active_at TEXT, severity TEXT, alertname TEXT, target TEXT
);
CREATE TABLE backup_results (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  name TEXT, status TEXT, newest TEXT, age_h INTEGER, size INTEGER, median_prev7 INTEGER, kinds INTEGER
);
CREATE TABLE cert_groups (
  run_id INTEGER NOT NULL, kind TEXT NOT NULL, gid TEXT, label TEXT, status TEXT, sev TEXT,
  days_left INTEGER, not_after TEXT, issuer TEXT, n_names INTEGER, names TEXT, sans TEXT,
  san_mismatch TEXT, note TEXT);
CREATE TABLE cert_results (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  host TEXT, status TEXT, days_left INTEGER, not_after TEXT, issuer TEXT
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
CREATE TABLE cicd_branches (
  project_id INTEGER NOT NULL, branch TEXT NOT NULL, present INTEGER, sha TEXT, committed_at INTEGER, title TEXT,
  PRIMARY KEY (project_id, branch)
);
CREATE TABLE cicd_deploys (
  id INTEGER PRIMARY KEY, project_id INTEGER, project TEXT, env TEXT, sha TEXT, prev_sha TEXT,
  pipeline_id INTEGER UNIQUE, downstream_id INTEGER, at INTEGER, author TEXT, title TEXT,
  hosts TEXT, target_group TEXT, unit TEXT,
  verify_state TEXT DEFAULT 'pending', verify_pass INTEGER DEFAULT 0, verify_detail TEXT, verified_at INTEGER
);
CREATE TABLE cicd_findings (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, key TEXT NOT NULL UNIQUE,
  project_id INTEGER, project TEXT, env TEXT, ref TEXT, severity TEXT, klass TEXT, rule TEXT,
  title TEXT, detail TEXT, evidence TEXT, proposals TEXT,
  pipeline_id INTEGER, job_id INTEGER, job_name TEXT, sha TEXT, author TEXT, mr TEXT,
  occurrences INTEGER DEFAULT 1, first_seen INTEGER, last_seen INTEGER,
  status TEXT DEFAULT 'open', resolved_at INTEGER, resolve_note TEXT,
  incident_id INTEGER, llm INTEGER DEFAULT 0, notified INTEGER DEFAULT 0
);
CREATE TABLE cicd_pipelines (
  id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, ref TEXT, env TEXT, sha TEXT, status TEXT, source TEXT,
  created_at INTEGER, updated_at INTEGER, finished_at INTEGER, user TEXT,
  deploy_status TEXT, downstream_id INTEGER, prod_deploy TEXT,
  triaged INTEGER DEFAULT 0, prev_status TEXT, seen_at INTEGER
);
CREATE TABLE cicd_projects (
  id INTEGER PRIMARY KEY, path TEXT NOT NULL, grp TEXT, default_branch TEXT, last_activity TEXT,
  kind TEXT,
  consumer INTEGER DEFAULT 0, include_ref TEXT, include_file TEXT,
  includes TEXT,
  ci_target TEXT,
  executor TEXT, protected TEXT, archived INTEGER DEFAULT 0,
  scanned_at INTEGER, poll_cursor TEXT, polled_at INTEGER
);
CREATE TABLE cicd_readiness (
  id INTEGER PRIMARY KEY, project_id INTEGER, path TEXT, ref TEXT, sha TEXT, rules_version INTEGER,
  verdict TEXT,
  blockers INTEGER, warnings INTEGER, passed INTEGER, unknown INTEGER,
  facts TEXT,
  report TEXT, checked_at INTEGER, source TEXT, calls INTEGER, error TEXT DEFAULT ''
);
CREATE TABLE cicd_state (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE cve_affects (
  group_key TEXT NOT NULL, target TEXT NOT NULL, host TEXT NOT NULL, vm_id INTEGER, env TEXT, service TEXT NOT NULL,
  unit TEXT NOT NULL, kind TEXT, exposure TEXT, via TEXT, version TEXT, how TEXT,
  PRIMARY KEY (group_key, target, host, unit)
);
CREATE TABLE cve_audit (
  at INTEGER NOT NULL, actor TEXT, action TEXT, rule_id TEXT, detail TEXT
);
CREATE TABLE cve_blind_spots (
  kind TEXT NOT NULL, subject TEXT NOT NULL, host TEXT, vm_id INTEGER, env TEXT, reason TEXT, seen INTEGER,
  PRIMARY KEY (kind, subject)
);
CREATE TABLE cve_containers (
  host TEXT NOT NULL, vm_id INTEGER, env TEXT, dc TEXT, name TEXT NOT NULL, service TEXT, image TEXT NOT NULL,
  source TEXT, instance TEXT, digest TEXT, analysis TEXT, evidence TEXT, seen INTEGER,
  PRIMARY KEY (host, name, image)
);
CREATE TABLE cve_findings (
  id INTEGER PRIMARY KEY, dkey TEXT NOT NULL UNIQUE, vuln_key TEXT NOT NULL, aliases TEXT, kind TEXT,
  component_name TEXT, ecosystem TEXT, group_key TEXT, env_max_class TEXT, envs TEXT, versions TEXT, targets TEXT,
  n_targets INTEGER, exposure TEXT, tier INTEGER, title TEXT, severity TEXT, cvss REAL, vector TEXT, url TEXT,
  published TEXT, fixed_version TEXT, nofix INTEGER DEFAULT 0, kev INTEGER DEFAULT 0, kev_due TEXT,
  ransomware INTEGER DEFAULT 0, epss REAL, epss_pct REAL, score REAL, priority TEXT, breakdown TEXT,
  first_seen INTEGER, last_seen INTEGER, state TEXT, sla_due INTEGER, suppressed_by TEXT, notified INTEGER DEFAULT 0,
  incident_id INTEGER, task_key TEXT, resolved_at INTEGER
);
CREATE TABLE cve_fixes (
  group_key TEXT PRIMARY KEY, kind TEXT, name TEXT, ecosystem TEXT, env_class TEXT, fixed_version TEXT, md TEXT,
  fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_at INTEGER
);
CREATE TABLE cve_hosts (
  name TEXT PRIMARY KEY, vm_id INTEGER, alias TEXT, env TEXT, os TEXT, os_id TEXT, os_version TEXT, kernel TEXT,
  reboot_required INTEGER, exposure TEXT, tier INTEGER, pkgs_n INTEGER, pkgs_at INTEGER, runtime TEXT, status TEXT,
  error TEXT, seen INTEGER
);
CREATE TABLE cve_service_risk (
  service TEXT PRIMARY KEY, hosts INTEGER, host_list TEXT, envs TEXT, p1 INTEGER, p2 INTEGER, kev INTEGER,
  findings INTEGER, groups_n INTEGER, top_group TEXT, top_component TEXT, exposure TEXT, at INTEGER
);
CREATE TABLE cve_suppressions (
  id TEXT PRIMARY KEY, vuln_key TEXT, component TEXT, target TEXT, env TEXT, reason TEXT, expires TEXT,
  owner TEXT, created_by TEXT, created_at INTEGER, active INTEGER DEFAULT 1, removed_by TEXT, removed_at INTEGER
);
CREATE TABLE cve_targets (
  finding_id INTEGER NOT NULL, target TEXT NOT NULL, vm_id INTEGER, version TEXT, env TEXT,
  PRIMARY KEY (finding_id, target)
);
CREATE TABLE cve_units (
  host TEXT NOT NULL, unit TEXT NOT NULL, state TEXT, binary TEXT, script TEXT, interp TEXT, pkgs TEXT, libpkgs TEXT,
  seen INTEGER, PRIMARY KEY (host, unit)
);
CREATE TABLE disk_reports (
  id INTEGER PRIMARY KEY,
  fs_id TEXT NOT NULL,
  dc TEXT, host TEXT, mount TEXT, alias TEXT, env TEXT,
  created INTEGER NOT NULL,
  requested_by TEXT,
  run_id INTEGER,
  status TEXT,
  cost_usd REAL
);
CREATE TABLE fix_runs (
  id TEXT PRIMARY KEY,
  requested_by TEXT, requested_at INTEGER,
  env TEXT,
  hosts TEXT,
  problem_run INTEGER, problem_idx INTEGER, problem_title TEXT,
  prompt_sha256 TEXT, prompt TEXT,
  started_at INTEGER, finished_at INTEGER,
  status TEXT,
  outcome TEXT,
  summary TEXT, report TEXT,
  actions TEXT, backups TEXT, verification TEXT, rollback TEXT,
  cost_usd REAL, turns INTEGER, denials INTEGER, unit_result TEXT
);
CREATE TABLE idea_events (
  id INTEGER PRIMARY KEY,
  idea_id INTEGER,
  persona TEXT,
  at INTEGER NOT NULL,
  kind TEXT NOT NULL,
  actor TEXT, text TEXT,
  run_id INTEGER, cost_usd REAL, trigger TEXT
);
CREATE TABLE ideas (
  id INTEGER PRIMARY KEY,
  persona TEXT NOT NULL,
  title TEXT NOT NULL, area TEXT NOT NULL,
  problem TEXT, evidence TEXT,
  proposal TEXT, value TEXT, effort TEXT, risk TEXT, prerequisites TEXT, success_metric TEXT,
  changed TEXT,
  revives INTEGER,
  evidence_hash TEXT,
  status TEXT NOT NULL DEFAULT 'new',
  run_id INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  decided_at INTEGER, decided_by TEXT, reason TEXT,
  built_ref TEXT, built_at INTEGER, built_by TEXT,
  build_prompt TEXT, prompt_at INTEGER, prompt_refined INTEGER DEFAULT 0
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
, hub_service TEXT, hub_tier INTEGER, hub_callers INTEGER, sources TEXT, thread_room TEXT, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_ref TEXT, fix_at INTEGER, lazy TEXT);
CREATE TABLE outline_pages (
  service TEXT PRIMARY KEY, grp TEXT, title TEXT NOT NULL,
  body TEXT NOT NULL, body_hash TEXT NOT NULL,
  prev_body TEXT, prev_hash TEXT,
  rendered_at INTEGER, changed_at INTEGER,
  redacted TEXT, blocked TEXT,
  summary TEXT,
  doc_id TEXT, doc_url TEXT, publish_status TEXT, publish_flags TEXT, published_at INTEGER, publish_error TEXT
);
CREATE TABLE outline_servinfo (
  project TEXT PRIMARY KEY, ref TEXT, text TEXT, fetched_at INTEGER, status TEXT
);
CREATE TABLE problem_events (
  problem_id INTEGER NOT NULL REFERENCES problems(id),
  at INTEGER NOT NULL,
  kind TEXT NOT NULL,
  actor TEXT, ref_type TEXT, ref_id TEXT, text TEXT
);
CREATE TABLE problem_links (
  id INTEGER PRIMARY KEY,
  problem_id INTEGER NOT NULL REFERENCES problems(id),
  ref_type TEXT NOT NULL,
  ref_id TEXT NOT NULL,
  link_type TEXT NOT NULL,
  state TEXT NOT NULL,
  is_primary INTEGER DEFAULT 0,
  source TEXT NOT NULL,
  confidence REAL,
  reason TEXT,
  features TEXT,
  "by" TEXT NOT NULL, at INTEGER NOT NULL,
  decided_by TEXT, decided_at INTEGER, note TEXT
);
CREATE TABLE problems (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'new',
  origin TEXT NOT NULL,
  root_cause TEXT, root_cause_label TEXT, workaround TEXT,
  severity TEXT, env TEXT, customer_facing INTEGER DEFAULT 0,
  owner TEXT,
  signature TEXT, fanout INTEGER DEFAULT 0,
  fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_ref TEXT, fix_at INTEGER,
  lazy TEXT,
  verify_state TEXT, verify_since INTEGER, verify_detail TEXT,
  thread_root TEXT, thread_room TEXT,
  merged_into INTEGER,
  recurrences INTEGER DEFAULT 0,
  created_at INTEGER NOT NULL, created_by TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  first_seen INTEGER,
  investigating_at INTEGER, known_error_at INTEGER, fix_started_at INTEGER,
  resolved_at INTEGER, resolved_by TEXT, resolve_note TEXT, closed_at INTEGER
);
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
  path TEXT NOT NULL UNIQUE,
  mr_url TEXT,
  created INTEGER NOT NULL,
  status TEXT NOT NULL,
  content TEXT,
  run_id INTEGER,
  source TEXT,
  cost_usd REAL,
  duration_s INTEGER,
  redacted INTEGER DEFAULT 0
, requested_by TEXT, fix_options TEXT, fix_prompt TEXT, fix_state TEXT, fix_flags TEXT, fix_source TEXT, fix_at INTEGER, lazy TEXT);
CREATE TABLE runs (
  id INTEGER PRIMARY KEY,
  task TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  duration_ms INTEGER,
  status TEXT,
  summary TEXT,
  cost_usd REAL,
  turns INTEGER,
  denials INTEGER,
  is_error INTEGER,
  mr_url TEXT,
  log_path TEXT
);
CREATE TABLE sw_components (
  id INTEGER PRIMARY KEY, target_kind TEXT NOT NULL, target TEXT NOT NULL, env TEXT, kind TEXT NOT NULL,
  ecosystem TEXT, name TEXT NOT NULL, version TEXT NOT NULL, purl TEXT, cpe TEXT, source TEXT, is_dev INTEGER DEFAULT 0,
  vm_id INTEGER, exposure TEXT, tier INTEGER, extra TEXT, first_seen INTEGER, last_seen INTEGER,
  misses INTEGER DEFAULT 0, closed INTEGER DEFAULT 0,
  UNIQUE (target_kind, target, env, kind, ecosystem, name, version)
);
CREATE TABLE today_events (
  id INTEGER PRIMARY KEY, plan_id INTEGER, item_id INTEGER, at INTEGER NOT NULL, kind TEXT NOT NULL, actor TEXT, text TEXT
);
CREATE TABLE today_items (
  id INTEGER PRIMARY KEY,
  plan_id INTEGER NOT NULL,
  ref TEXT NOT NULL,
  kind TEXT NOT NULL, area TEXT, env TEXT,
  title TEXT, do_this TEXT, why_now TEXT, details TEXT,
  link TEXT, link_label TEXT, close_link TEXT, prompt_url TEXT,
  members TEXT, sig TEXT,
  needs_approval INTEGER DEFAULT 0, needs_window INTEGER DEFAULT 0, own_action INTEGER DEFAULT 0,
  est_min INTEGER, est_base INTEGER, est_basis TEXT,
  score REAL, urgency REAL, impact REAL, confidence REAL,
  slot TEXT NOT NULL DEFAULT 'later', rank INTEGER, later_reason TEXT,
  status TEXT NOT NULL DEFAULT 'planned',
  carried INTEGER DEFAULT 0, pinned INTEGER DEFAULT 0, auto_done INTEGER DEFAULT 0,
  started_at INTEGER, done_at INTEGER, actual_min INTEGER, defer_until TEXT, note TEXT,
  created_at INTEGER, updated_at INTEGER,
  UNIQUE (plan_id, ref)
);
CREATE TABLE today_learn (kind TEXT PRIMARY KEY, n INTEGER, ratio REAL, updated_at INTEGER);
CREATE TABLE today_plans (
  id INTEGER PRIMARY KEY,
  day TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, version INTEGER DEFAULT 1,
  budget_min INTEGER, max_items INTEGER, max_risky INTEGER,
  planned_min INTEGER, n_today INTEGER, n_later INTEGER,
  trigger TEXT, by TEXT, refined INTEGER DEFAULT 0, cost_usd REAL DEFAULT 0, note TEXT
);
CREATE TABLE today_settings (k TEXT PRIMARY KEY, v TEXT, by TEXT, at INTEGER);
CREATE TABLE triage_actionability (
  run_id INTEGER NOT NULL, idx INTEGER NOT NULL,
  actionability TEXT,
  confirmed INTEGER DEFAULT 0,
  missing TEXT, questions TEXT,
  continues_issue INTEGER,
  note TEXT,
  PRIMARY KEY (run_id, idx)
);
CREATE TABLE triage_problems (
  run_id INTEGER NOT NULL REFERENCES runs(id),
  idx INTEGER, title TEXT, severity TEXT, environment TEXT, sources TEXT,
  summary TEXT, rca TEXT, fix_prompt TEXT, suspicious INTEGER
, fix_options TEXT, fix_state TEXT, fix_flags TEXT, lazy TEXT);
CREATE TABLE vm_changes (
  vm_id INTEGER NOT NULL REFERENCES vms(id), at INTEGER NOT NULL,
  field TEXT NOT NULL, old TEXT, new TEXT
);
CREATE TABLE vm_reports (
  id INTEGER PRIMARY KEY,
  vm_id INTEGER NOT NULL,
  name TEXT, env TEXT, alias TEXT, mode TEXT,
  created INTEGER NOT NULL,
  requested_by TEXT,
  trigger TEXT,
  run_id INTEGER,
  status TEXT,
  cost_usd REAL
);
CREATE TABLE vm_state (
  vm_id INTEGER PRIMARY KEY, at INTEGER, ssh_at INTEGER, prom_at INTEGER,
  cpu TEXT, memory TEXT, io TEXT, disk TEXT, network TEXT, reasons TEXT,
  svc_total INTEGER, svc_app INTEGER, svc_healthy INTEGER, svc_degraded INTEGER, svc_restarting INTEGER,
  svc_down INTEGER, svc_stopped INTEGER, dead_upstreams INTEGER, summary TEXT, ssh_error TEXT
);
CREATE TABLE vm_unit_samples (
  vm_id INTEGER NOT NULL, at INTEGER NOT NULL, unit TEXT NOT NULL, active TEXT,
  mem INTEGER, anon INTEGER, peak INTEGER, cpu_usec INTEGER, cpu_pct REAL, restarts INTEGER, since INTEGER,
  oom_kill INTEGER
);
CREATE TABLE vms (
  id INTEGER PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT, ips TEXT, env TEXT, dc TEXT, os TEXT, os_id TEXT, os_version TEXT, kernel TEXT, arch TEXT,
  vcpu INTEGER, ram_bytes INTEGER, disk_total INTEGER, disk_used INTEGER, mounts TEXT,
  boot_time INTEGER, virt TEXT, hypervisor TEXT, monitored INTEGER, up INTEGER, last_scrape INTEGER,
  prom_instance TEXT, prom_job TEXT, ssh_alias TEXT, ssh_ok INTEGER, docker INTEGER, containers INTEGER,
  services_running INTEGER, domains TEXT, ntp_sync TEXT, timezone TEXT, flags TEXT, facts TEXT,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL, gone INTEGER DEFAULT 0
);
CREATE INDEX backup_results_name ON backup_results(name, run_id);
CREATE INDEX cert_groups_run ON cert_groups(run_id, kind);
CREATE INDEX chat_messages_fp ON chat_messages(alert_fp, ts);
CREATE INDEX chat_messages_inc ON chat_messages(incident_id);
CREATE INDEX chat_messages_issue ON chat_messages(issue_id);
CREATE INDEX chat_messages_run ON chat_messages(run_id);
CREATE INDEX chat_messages_src ON chat_messages(source, ts);
CREATE INDEX cicd_deploys_at ON cicd_deploys(at);
CREATE INDEX cicd_deploys_proj ON cicd_deploys(project_id, env, at);
CREATE INDEX cicd_findings_kind ON cicd_findings(kind, status, last_seen);
CREATE INDEX cicd_findings_proj ON cicd_findings(project_id, status);
CREATE INDEX cicd_pipelines_proj ON cicd_pipelines(project_id, ref, id);
CREATE INDEX cicd_pipelines_status ON cicd_pipelines(status, created_at);
CREATE INDEX cicd_projects_consumer ON cicd_projects(consumer, polled_at);
CREATE INDEX cicd_readiness_path ON cicd_readiness(path, id);
CREATE INDEX cicd_readiness_proj ON cicd_readiness(project_id, id);
CREATE INDEX cve_affects_host ON cve_affects(host);
CREATE INDEX cve_affects_service ON cve_affects(service);
CREATE INDEX cve_affects_vm ON cve_affects(vm_id);
CREATE INDEX cve_containers_image ON cve_containers(image);
CREATE INDEX cve_containers_vm ON cve_containers(vm_id);
CREATE INDEX cve_findings_group ON cve_findings(group_key);
CREATE INDEX cve_findings_state ON cve_findings(state, priority, score);
CREATE INDEX cve_targets_vm ON cve_targets(vm_id);
CREATE INDEX disk_reports_fs ON disk_reports(fs_id, created);
CREATE INDEX idea_events_idea ON idea_events(idea_id, at);
CREATE INDEX idea_events_kind ON idea_events(kind, persona, at);
CREATE INDEX ideas_status ON ideas(status, created_at);
CREATE INDEX incident_events_inc ON incident_events(incident_id, at);
CREATE INDEX incident_events_run ON incident_events(run_id);
CREATE INDEX incidents_key ON incidents(subject, family);
CREATE INDEX incidents_status ON incidents(status, last_seen);
CREATE INDEX problem_events_p ON problem_events(problem_id, at);
CREATE UNIQUE INDEX problem_links_live ON problem_links(problem_id, ref_type, ref_id, link_type)
  WHERE state IN ('suggested','accepted');
CREATE UNIQUE INDEX problem_links_primary ON problem_links(ref_type, ref_id)
  WHERE is_primary = 1 AND state = 'accepted';
CREATE INDEX problem_links_ref ON problem_links(ref_type, ref_id, state);
CREATE INDEX problem_links_state ON problem_links(state, at);
CREATE UNIQUE INDEX problems_origin ON problems(origin) WHERE origin <> 'manual';
CREATE INDEX problems_status ON problems(status, updated_at);
CREATE INDEX qa_issues_inc ON qa_issues(incident_id);
CREATE INDEX qa_issues_ts ON qa_issues(last_ts);
CREATE INDEX rca_reports_incident ON rca_reports(incident_id, created);
CREATE INDEX runs_task_time ON runs(task, started_at);
CREATE INDEX sw_components_name ON sw_components(ecosystem, name, version);
CREATE INDEX today_items_ref ON today_items(ref, plan_id);
CREATE INDEX vm_changes_vm ON vm_changes(vm_id, at);
CREATE INDEX vm_reports_vm ON vm_reports(vm_id, created);
CREATE INDEX vm_unit_samples_vm ON vm_unit_samples(vm_id, unit, at);
CREATE TABLE seo_snapshots (
  ts INTEGER NOT NULL, edge TEXT NOT NULL, host TEXT NOT NULL, offset_d INTEGER NOT NULL,
  total INTEGER, verified INTEGER, s2xx INTEGER, s3xx INTEGER, s4xx INTEGER, s5xx INTEGER,
  p50 REAL, p95 REAL, hits INTEGER, PRIMARY KEY (ts, edge, host, offset_d)
);
CREATE TABLE seo_errors (ts INTEGER NOT NULL, status TEXT, upstream TEXT, prefix TEXT, n INTEGER, alias TEXT, location TEXT);
CREATE TABLE gsc_daily (date TEXT PRIMARY KEY, clicks INTEGER, impressions INTEGER, ctr REAL, position REAL, fetched INTEGER);
CREATE TABLE gsc_urls (ts INTEGER NOT NULL, url TEXT NOT NULL, verdict TEXT, coverage TEXT, last_crawl TEXT, crawled_as TEXT,
  robots TEXT, fetch_state TEXT, PRIMARY KEY (ts, url));
CREATE TABLE gsc_sitemaps (path TEXT PRIMARY KEY, last_downloaded TEXT, pending INTEGER, errors INTEGER, warnings INTEGER,
  submitted INTEGER, fetched INTEGER);
CREATE TABLE seo_suggestions (day TEXT NOT NULL, rule TEXT NOT NULL, rank INTEGER NOT NULL, page TEXT, query TEXT, title TEXT,
  evidence TEXT, action TEXT, score REAL, PRIMARY KEY (day, rule, rank));
CREATE TABLE seo_summary (day TEXT PRIMARY KEY, text TEXT, cost_usd REAL, model TEXT, error TEXT, at INTEGER);
