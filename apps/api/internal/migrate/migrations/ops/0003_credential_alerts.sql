-- ops.db: what the credential expiry job raised (internal/credexpiry): one row per credential in an alert band.
CREATE TABLE IF NOT EXISTS credential_alerts (
  id TEXT PRIMARY KEY, band TEXT, since INTEGER, notified_band TEXT, notified_at INTEGER,
  incident_id INTEGER, attached_at INTEGER
);
