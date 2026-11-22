-- ops.db: the CDN log counts of the marketing module (internal/marketing/cdn). The same tables as the earlier
-- installation's CDN job, so an imported ops.db is adopted as it is.
-- cdn_buckets.ok: 1 stored, 0 gap (an index error, or no document while the CDN should ship logs), 2 off by plan
CREATE TABLE IF NOT EXISTS cdn_gbot (ts INTEGER NOT NULL, who TEXT NOT NULL, host TEXT, status TEXT, fw_rule TEXT,
  fw_action TEXT, fw_blocked TEXT, up_status TEXT, cache TEXT, n INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS cdn_gbot_ts ON cdn_gbot(ts);
CREATE TABLE IF NOT EXISTS cdn_buckets (ts INTEGER PRIMARY KEY, total INTEGER, known_google INTEGER, gbot_ua INTEGER,
  ok INTEGER NOT NULL, error TEXT, took_ms INTEGER, at INTEGER);
