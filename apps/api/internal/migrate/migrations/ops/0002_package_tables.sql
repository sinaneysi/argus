-- ops.db: tables that packages created on first use (data sources, discovery, the routing catalogue, the collectors'
-- index) and the accepted backup failures. Copied from internal/sources, internal/sources/discover,
-- internal/sources/catalogue and internal/collect; TestPackageSchemasCovered keeps them in step.
CREATE TABLE IF NOT EXISTS data_sources (
  key TEXT PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL, env TEXT, url TEXT, purpose TEXT, status TEXT, detail TEXT,
  identity TEXT, enabled INTEGER DEFAULT 0, managed TEXT, used_by TEXT, cred_where TEXT, last_check INTEGER,
  created_by TEXT, created_at INTEGER, recorded_at INTEGER);
CREATE TABLE IF NOT EXISTS source_inventory (
  source TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, attrs TEXT, first_seen INTEGER, seen_at INTEGER,
  PRIMARY KEY (source, kind, key));
CREATE TABLE IF NOT EXISTS source_discovery_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL, started_at INTEGER, finished_at INTEGER,
  status TEXT, mode TEXT, trigger TEXT, counts TEXT, calls INTEGER, error TEXT);
CREATE INDEX IF NOT EXISTS source_discovery_runs_src ON source_discovery_runs(source, started_at);
CREATE TABLE IF NOT EXISTS source_routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT, service TEXT NOT NULL, source TEXT NOT NULL, kind TEXT NOT NULL,
  locator TEXT NOT NULL, label TEXT, confidence REAL, origin TEXT NOT NULL, status TEXT NOT NULL, rule TEXT,
  evidence TEXT, evidence_hash TEXT, stale INTEGER DEFAULT 0, created_at INTEGER, updated_at INTEGER, updated_by TEXT,
  UNIQUE (service, source, kind, locator));
CREATE INDEX IF NOT EXISTS source_routes_svc ON source_routes(service, status);
CREATE TABLE IF NOT EXISTS source_unmapped (
  source TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, label TEXT, note TEXT, PRIMARY KEY (source, kind, key));
CREATE TABLE IF NOT EXISTS source_catalogue_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER, services INTEGER, counts TEXT, unmapped INTEGER, note TEXT);
CREATE TABLE IF NOT EXISTS source_route_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER, by_user TEXT, action TEXT, route_id INTEGER, service TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS source_action_results (
  rid TEXT PRIMARY KEY, user TEXT, action TEXT, result TEXT, at INTEGER);
CREATE TABLE IF NOT EXISTS backup_overrides (id INTEGER PRIMARY KEY AUTOINCREMENT, set_name TEXT NOT NULL, check_kind TEXT NOT NULL,
  reason TEXT NOT NULL, by_user TEXT NOT NULL, at INTEGER NOT NULL, expires INTEGER NOT NULL, scope TEXT NOT NULL, days INTEGER,
  snapshot TEXT, rid TEXT UNIQUE, ended_at INTEGER, ended_by TEXT, ended_why TEXT);
