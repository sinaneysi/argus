-- googlebot.db: the crawler traffic database of the marketing module (ADR-0021 D6). Its own file, not ARGUS_DB: every
-- crawled URL for 90 days is several GB. Migrated by the module's job when it opens the file, not at start.
-- gw_buckets  one row per finished bucket and edge group: ok (1) or gap (0), lines read, cost
-- gw_agg      bucket x edge host x site host x who x crawler x path class x upstream kind: requests, status counts,
--             latency sum, a mergeable log-scale histogram, >= 10 s, bytes, cache status counts
-- gw_url_day  day x site host x normalised URL (real Googlebot only); gw_url_var: raw query variants per day
-- gw_url      per URL, all time: the last crawl and the last 200
-- gw_errors   bucket x edge group x status x upstream x first path segment: 5xx to a Googlebot user agent
-- gw_alerts   detector findings, one open row per rule and key; gw_state, gw_events: interval, ranges, run cost
CREATE TABLE IF NOT EXISTS gw_buckets (ts INTEGER NOT NULL, width INTEGER NOT NULL, edge TEXT NOT NULL, ok INTEGER NOT NULL,
  lines INTEGER, bytes INTEGER, took_ms INTEGER, error TEXT, at INTEGER, PRIMARY KEY (ts, edge));
CREATE TABLE IF NOT EXISTS gw_agg (ts INTEGER NOT NULL, width INTEGER NOT NULL, edge TEXT NOT NULL, host TEXT NOT NULL,
  who TEXT NOT NULL, crawler TEXT NOT NULL, pclass TEXT NOT NULL, upstream TEXT NOT NULL, req INTEGER NOT NULL,
  s2xx INTEGER, s301 INTEGER, s302 INTEGER, s304 INTEGER, s3xx INTEGER, s4xx INTEGER, s403 INTEGER, s404 INTEGER,
  s410 INTEGER, s429 INTEGER, s5xx INTEGER, s499 INTEGER, s503 INTEGER, s504 INTEGER,
  lat_sum REAL, ge10s INTEGER, bytes INTEGER, hist TEXT, cache TEXT);
CREATE INDEX IF NOT EXISTS gw_agg_ts ON gw_agg(ts);
CREATE TABLE IF NOT EXISTS gw_url_day (day TEXT NOT NULL, host TEXT NOT NULL, url TEXT NOT NULL, pclass TEXT,
  hits INTEGER, smartphone INTEGER, desktop INTEGER, image INTEGER, other INTEGER,
  s2xx INTEGER, s3xx INTEGER, s4xx INTEGER, s5xx INTEGER, bytes INTEGER, lat_sum REAL, lat_max REAL, cache_hit INTEGER,
  first_ts INTEGER, last_ts INTEGER, PRIMARY KEY (day, host, url));
CREATE TABLE IF NOT EXISTS gw_url_var (day TEXT NOT NULL, host TEXT NOT NULL, url TEXT NOT NULL, qhash TEXT NOT NULL,
  PRIMARY KEY (day, host, url, qhash));
CREATE TABLE IF NOT EXISTS gw_url (host TEXT NOT NULL, url TEXT NOT NULL, pclass TEXT, last_ts INTEGER, last_status TEXT,
  last_200_ts INTEGER, PRIMARY KEY (host, url));
CREATE TABLE IF NOT EXISTS gw_errors (ts INTEGER NOT NULL, width INTEGER NOT NULL, edge TEXT NOT NULL, status TEXT NOT NULL,
  upstream TEXT NOT NULL, prefix TEXT NOT NULL, n INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS gw_errors_ts ON gw_errors(ts);
CREATE TABLE IF NOT EXISTS gw_alerts (id INTEGER PRIMARY KEY, rule TEXT NOT NULL, key TEXT NOT NULL, severity TEXT,
  first_ts INTEGER, last_ts INTEGER, resolved_ts INTEGER, observed REAL, baseline REAL, detail TEXT, notified INTEGER DEFAULT 0);
CREATE INDEX IF NOT EXISTS gw_alerts_open ON gw_alerts(rule, key, resolved_ts);
CREATE TABLE IF NOT EXISTS gw_state (k TEXT PRIMARY KEY, v TEXT);
CREATE TABLE IF NOT EXISTS gw_events (ts INTEGER, kind TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS gw_sitemap (url TEXT PRIMARY KEY, pclass TEXT, sitemap TEXT, lastmod INTEGER, seen INTEGER);
CREATE TABLE IF NOT EXISTS gw_manual_daily (day TEXT PRIMARY KEY, req INTEGER, s5xx INTEGER, s499 INTEGER, s504 INTEGER,
  ge10s INTEGER, avg_ms REAL, p50 REAL, p95 REAL, src TEXT);
