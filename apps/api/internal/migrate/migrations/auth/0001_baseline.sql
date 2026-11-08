-- auth.db: users, sessions, sign-in failures, single sign-on state, 2FA, the audit trail, chat IDs.
-- Same tables as internal/auth (SCHEMA, MFA_SCHEMA and the additive session columns) and internal/app (matrix_ids).
CREATE TABLE IF NOT EXISTS users (username TEXT PRIMARY KEY, hash TEXT NOT NULL, role TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0, must_change INTEGER NOT NULL DEFAULT 0, created_at INTEGER);
CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL,
  created INTEGER NOT NULL, last_seen INTEGER NOT NULL, csrf TEXT NOT NULL, id_token TEXT,
  remember INTEGER NOT NULL DEFAULT 0, mfa_at INTEGER, mfa_via TEXT, ip TEXT, ua TEXT, auth_at INTEGER);
CREATE TABLE IF NOT EXISTS failures (key TEXT PRIMARY KEY, count INTEGER NOT NULL, until INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS user_meta (username TEXT PRIMARY KEY, source TEXT NOT NULL DEFAULT 'local', sub TEXT,
  last_login INTEGER);
CREATE TABLE IF NOT EXISTS oidc_states (state_hash TEXT PRIMARY KEY, nonce TEXT NOT NULL, verifier TEXT NOT NULL,
  next TEXT NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS mfa (username TEXT PRIMARY KEY, secret_enc TEXT, factor_id TEXT, enabled INTEGER NOT NULL DEFAULT 0,
  enabled_at INTEGER, last_step INTEGER NOT NULL DEFAULT 0, last_used INTEGER, pending_enc TEXT, pending_fid TEXT, pending_sid TEXT,
  pending_at INTEGER, pending_base TEXT);
CREATE TABLE IF NOT EXISTS mfa_recovery (username TEXT NOT NULL, code_hash TEXT NOT NULL, created INTEGER NOT NULL,
  used_at INTEGER, PRIMARY KEY (username, code_hash));
CREATE TABLE IF NOT EXISTS mfa_pending (token_hash TEXT PRIMARY KEY, username TEXT NOT NULL, created INTEGER NOT NULL,
  remember INTEGER NOT NULL DEFAULT 0, next TEXT NOT NULL DEFAULT '/', id_token TEXT, source TEXT NOT NULL DEFAULT 'local',
  attempts INTEGER NOT NULL DEFAULT 0, auth_at INTEGER);
CREATE TABLE IF NOT EXISTS oidc_extra (state_hash TEXT PRIMARY KEY, remember INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS mfa_reenrol (username TEXT PRIMARY KEY, set_at INTEGER NOT NULL, actor TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS auth_audit (id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, actor TEXT NOT NULL,
  action TEXT NOT NULL, target TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '', ip TEXT NOT NULL DEFAULT '',
  sid TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '', outcome TEXT NOT NULL DEFAULT 'ok', reason TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS matrix_ids (username TEXT PRIMARY KEY, matrix_id TEXT UNIQUE, updated INTEGER);
