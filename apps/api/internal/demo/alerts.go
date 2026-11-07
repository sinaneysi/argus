package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

type alert struct {
	dc, name, sev, state string
	mins                 float64
	inst                 string
	value                any // int or float, as in the Python list
	summary              string
}

var ALERTS = []alert{
	{"east", "SmsOtpQueueBacklog", "critical", "firing", 290, "prod-pay-01:9100", 412, "sms-otp queue above 200 for 5 min"},
	{"east", "SmsOtpQueueBacklog", "critical", "firing", 288, "prod-pay-02:9100", 388, "sms-otp queue above 200 for 5 min"},
	{"east", "Nginx5xxRateHigh", "critical", "firing", 418, "prod-edge-01:9113", 0.24, "upstream shop_front 5xx rate 24 %"},
	{"east", "Nginx5xxRateHigh", "critical", "firing", 417, "prod-edge-02:9113", 0.23, "upstream shop_front 5xx rate 23 %"},
	{"east", "NodeMemoryHigh", "warning", "firing", 262, "prod-pay-01:9100", 94.1, "memory used above 90 %"},
	{"east", "NodeFilesystemFillsIn7Days", "warning", "firing", 2880, "prod-mon-01:9100", 4.6, "/var/lib/loki full in 4.6 days"},
	{"east", "PostgresReplicationLag", "warning", "pending", 3, "prod-db-02:9187", 12.4, "replica 12 s behind"},
	{"east", "DiskIOSaturation", "warning", "firing", 95, "prod-db-02:9100", 0.81, "vdb busy 81 %"},
	{"east", "CertificateExpiresSoon", "warning", "firing", 7200, "sellers.northwind.example:443", 9, "TLS certificate expires in 9 days"},
	{"east", "BlackboxSlowResponse", "info", "firing", 40, "https://stats.northwind.example", 2.1, "Matomo responds in 2.1 s"},
	{"east", "TestAlertDoNotPage", "critical", "firing", 13 * 1440, "prod-mon-01:9093", 1, "leftover notification test"},
	{"west", "NodeFilesystemAlmostFull", "warning", "firing", 4300, "dev-runner-01:9100", 88, "/ above 85 %"},
	{"west", "ContainerRestarting", "warning", "firing", 55, "dev-swarm-02:8080", 6, "shop-front_dev restarted 6 times in 1 h"},
	{"west", "PrometheusTargetDown", "warning", "firing", 9 * 1440, "prod-backup-02:9100", 0, "node_exporter not scraped"},
	{"west", "HighLoad", "info", "pending", 2, "prod-api-03:9100", 1.4, "load per vCPU 1.4"},
}

func (g *Gen) alertsSnapshot() {
	dcs := []any{}
	for _, dc := range []string{"east", "west"} {
		items := []any{}
		for _, a := range ALERTS {
			if a.dc != dc {
				continue
			}
			items = append(items, om("alertname", a.name, "severity", a.sev, "state", a.state, "activeAt", g.iso(a.mins*M),
				"instance", a.inst, "value", py.Str(a.value), "summary", a.summary,
				"labels", om("alertname", a.name, "severity", a.sev, "instance", a.inst)))
		}
		dcs = append(dcs, om("dc", dc, "ok", true, "alerts", items))
	}
	g.writeJSON("alerts-firing.json", om("fetched_at", g.t(20), "dcs", dcs))
}

func (g *Gen) alerts() {
	g.alertsSnapshot()
	r := g.run("log-rca", 20*M, "WARN", "WARN: 2 problems: sms-otp backlog draining on prod-pay-01/02; nginx 502 on "+
		"www.northwind.example from prod-shop-02:3001 (dead upstream).", runOpts{cost: fp(0.31), dur: ip(180000), turns: ip(22)})
	g.ev(141, 20*M, "finding", r, 0, "log-rca", nil, nil)
	g.ev(145, 20*M, "finding", r, 1, "log-rca", nil, nil)
	for _, a := range ALERTS[:6] {
		g.ins(g.ops, "alerts", "run_id", r, "active_at", g.iso(a.mins*M), "severity", a.sev, "alertname", a.name, "target", a.inst)
	}
}

func (g *Gen) hygiene() {
	fs := []*py.OMap{
		om("kind", "test", "alertname", "TestAlertDoNotPage", "severity", "critical", "dc", "east", "instance", "prod-mon-01:9093",
			"age_days", 13, "never_resolved", true, "suggestion", "Delete the test rule from monitoring/rules/test.yml"),
		om("kind", "stale", "alertname", "PrometheusTargetDown", "severity", "warning", "dc", "west", "instance", "prod-backup-02:9100",
			"age_days", 9, "never_resolved", true, "suggestion", "Install node_exporter on prod-backup-02 or drop the target"),
		om("kind", "stale", "alertname", "CertificateExpiresSoon", "severity", "warning", "dc", "east",
			"instance", "sellers.northwind.example:443", "age_days", 5, "never_resolved", false,
			"suggestion", "Fix the DNS-01 renewal hook (incident #152)"),
		om("kind", "dead", "alertname", "up == 0", "dc", "west", "instance", "10.30.30.110:9100", "job", "node-infra",
			"age_days", 7, "suggestion", "Remove the target from prometheus.yml or fix the exporter"),
		om("kind", "dead", "alertname", "up == 0", "dc", "east", "instance", "10.20.30.99:9187", "job", "postgres-old",
			"age_days", 7, "suggestion", "Remove the target from prometheus.yml or fix the exporter"),
		om("kind", "flapping", "alertname", "ContainerRestarting", "severity", "warning", "dc", "west", "instance", "dev-swarm-02:8080",
			"per_day", 18, "fired", 126, "resolved", 125, "window_days", 7, "suggestion", "Raise `for:` to 10m and fix the dev healthcheck"),
		om("kind", "noisy", "alertname", "BlackboxSlowResponse", "severity", "info", "dc", "east", "instance", "https://stats.northwind.example",
			"per_day", 9, "fired", 63, "resolved", 63, "window_days", 7, "suggestion", "Lower to a dashboard panel; nobody acts on it"),
		om("kind", "noisy", "alertname", "HighLoad", "severity", "info", "dc", "west", "instance", "prod-api-03:9100",
			"per_day", 6, "fired", 41, "resolved", 41, "window_days", 7, "suggestion", "Use load per vCPU > 2 for 15m"),
	}
	counts := om()
	list := []any{}
	for _, k := range []string{"test", "stale", "dead", "flapping", "noisy"} {
		n := 0
		for _, f := range fs {
			if f.S("kind") == k {
				n++
			}
		}
		counts.Set(k, n)
	}
	for _, f := range fs {
		list = append(list, f)
	}
	days := []any{}
	for d := 7; d > 0; d-- {
		days = append(days, g.day(float64(d)))
	}
	g.writeJSON("hygiene.json", om("generated_at", g.t(2*H+15*M), "counts", counts, "findings", list, "stats_days", days))
}

type backupSet struct {
	name, newest string
	size, kinds  int
	story        string
}

var BACKUP_SETS = []backupSet{
	{"pg-prod_orders", "pg/orders/{d}/orders-{d}.dump.age", 148, 3, "OK"},
	{"pg-prod_payments", "pg/payments/{d}/payments-{d}.dump.age", 42, 3, "OK"},
	{"pg-prod_users", "pg/users/{d}/users-{d}.dump.age", 19, 3, "OK"},
	{"mongo-prod_catalog", "mongo/catalog/{d}/catalog.archive.age", 64, 2, "OK"},
	{"mongo-prod_reviews", "mongo/reviews/{d}/reviews.archive.age", 9, 2, "STALE"},
	{"redis-prod_sessions", "redis/sessions/{d}/dump.rdb.age", 2, 1, "OK"},
	{"minio-media_images", "media/images/{d}/manifest.json", 812, 1, "OK"},
	{"matomo-prod_db", "matomo/{d}/matomo.sql.gz", 23, 1, "SMALL"},
	{"pg-stg_all", "pg/stg/{d}/all.dump.age", 31, 2, "OK"},
	{"files-prod_invoices", "files/invoices/{d}/invoices.tar.age", 6, 1, "OK"},
	{"opensearch-prod_snapshots", "os/snapshots/{d}/snap-{d}", 120, 1, "MISSING"},
}

func (g *Gen) backups() {
	o := g.ops
	for d := 13; d >= 0; d-- {
		var bad []string
		for _, b := range BACKUP_SETS {
			if b.story != "OK" && d < 3 {
				bad = append(bad, b.name)
			}
		}
		st, sum := "OK", fmt.Sprintf("OK: all %d sets fresh and within size.", len(BACKUP_SETS))
		if len(bad) > 0 {
			st, sum = "WARN", fmt.Sprintf("WARN: %d of %d sets need a look: ", len(bad), len(BACKUP_SETS))+strings.Join(bad, ", ")+"."
		}
		rid := g.run("backup-check", float64(d)*D+6*H, st, sum, runOpts{cost: fp(0.05), dur: ip(52000), turns: ip(7)})
		for _, b := range BACKUP_SETS {
			day := strings.ReplaceAll(g.day(float64(d)*D+7*H), "-", "")
			status := "OK"
			if d < 3 {
				status = b.story
			}
			r := g.r.randint(4, 9) // the dict.get default draws in any case
			var age any = r
			switch status {
			case "STALE":
				if d < 3 {
					age = 52 + (2-d)*24
				} else {
					age = 7
				}
			case "MISSING":
				age = nil
			}
			var sz any = int64(float64(int64(b.size)*GB) * g.r.uniform(0.97, 1.03))
			med := int64(b.size) * GB
			if status == "SMALL" {
				sz = int64(float64(int64(b.size)*GB) * 0.18)
			}
			var newest any = strings.ReplaceAll(b.newest, "{d}", day)
			if status == "MISSING" {
				sz, age, newest = nil, nil, nil
			}
			g.ins(o, "backup_results", "run_id", rid, "name", b.name, "status", status, "newest", newest,
				"age_h", age, "size", sz, "median_prev7", med, "kinds", b.kinds)
		}
	}
	g.exec(o, "UPDATE runs SET summary=? WHERE id=(SELECT max(id) FROM runs WHERE task='backup-check')",
		"WARN: 3 of 11 sets need a look.\n\n- **mongo-prod_reviews**: newest backup 52 h old (the job on prod-mongo-02 "+
			"fails since the 10-21 password rotation).\n- **matomo-prod_db**: newest dump 18 % of the 7-day median "+
			"(4.1 GB vs 23 GB): the dump stopped at the `log_link_visit_action` table.\n- **opensearch-prod_snapshots**: no "+
			"snapshot found under `os/snapshots/` (repository not registered after the 10-20 upgrade).")
	ref := "backup-plans/ref"
	g.writeJSON(ref+"/backup-discovery.json", om(
		"generated", g.day(6*D),
		"minio", om("host", "prod-backup-01 (10.20.30.110)", "disk_pct", 71, "disk_used_tb", 2.8, "disk_total_tb", 3.9,
			"issue", "grows 41 GB a day"),
		"families", L(
			om("id", "A", "name", "Postgres pg_dump to MinIO", "engine", "postgres", "how", "nightly pg_dump -Fc, age-encrypted, mc cp",
				"defects", sl("D2"), "issues", sl("OPS-180")),
			om("id", "B", "name", "Mongo archives", "engine", "mongodb", "how", "mongodump --archive from the secondary, age",
				"defects", sl("D1"), "issues", sl("OPS-191")),
			om("id", "C", "name", "Files and media", "engine", "files", "how", "restic / mc mirror", "defects", L(), "issues", L()),
			om("id", "D", "name", "Matomo (legacy cron)", "engine", "mariadb-mysql", "how", "mysqldump | gzip in a cron, not encrypted",
				"defects", sl("D3", "D4"), "issues", sl("OPS-188")),
			om("id", "E", "name", "Unknown origin", "engine", "?", "how", "objects appear nightly; host and job unknown",
				"defects", L(), "issues", L())),
		"defects", L(
			om("n", 1, "text", "mongo backup user lost its role after the 10-21 password rotation; the job exits 0", "where", "prod-mongo-02 cron",
				"issue", "OPS-191", "risk", "high"),
			om("n", 2, "text", "pg_dump runs on the primary at 01:00, during the batch window", "where", "prod-db-01", "issue", "OPS-180", "risk", "medium"),
			om("n", 3, "text", "mysqldump without --single-transaction: the dump stops on a lock timeout", "where", "prod-matomo-01",
				"issue", "OPS-188", "risk", "high"),
			om("n", 4, "text", "Matomo dumps are not encrypted", "where", "prod-matomo-01", "issue", "OPS-188", "risk", "high")),
		"gaps", L(
			om("n", 1, "what", "Redis (cart and rate limits) on prod-redis-01", "engine", "redis", "env", "release", "why", "only sessions are dumped", "issue", "OPS-193"),
			om("n", 2, "what", "RabbitMQ definitions on prod-queue-01", "engine", "files", "env", "release", "why", "no export", "issue", ""),
			om("n", 3, "what", "OpenSearch snapshots", "engine", "files", "env", "release", "why", "repository missing since 10-20", "issue", "OPS-195")),
		"sets", L(
			om("set", "pg-prod_orders", "family", "A", "encrypted", "yes", "suffixes", sl(".dump.age"), "total_gb", 1040, "objects", 14, "per_day", 1, "script_known", true),
			om("set", "pg-prod_payments", "family", "A", "encrypted", "yes", "suffixes", sl(".dump.age"), "total_gb", 294, "objects", 14, "per_day", 1, "script_known", true),
			om("set", "mongo-prod_catalog", "family", "B", "encrypted", "yes", "suffixes", sl(".archive.age"), "total_gb", 448, "objects", 14, "per_day", 1, "script_known", true),
			om("set", "mongo-prod_reviews", "family", "B", "encrypted", "yes", "suffixes", sl(".archive.age"), "total_gb", 63, "objects", 12, "per_day", 1, "script_known", true),
			om("set", "minio-media_images", "family", "C", "encrypted", "partly", "suffixes", sl(".jpg", ".webp", ".json"), "total_gb", 812, "objects", 220000, "per_day", 3100, "script_known", true),
			om("set", "matomo-prod_db", "family", "D", "encrypted", "no", "suffixes", sl(".sql.gz"), "total_gb", 161, "objects", 14, "per_day", 1, "script_known", true),
			om("set", "legacy_exports", "family", "E", "encrypted", "no", "suffixes", sl(".csv.gz"), "total_gb", 12, "objects", 380, "per_day", 2, "script_known", false),
			om("set", "tmp_reports", "family", "E", "encrypted", "unknown", "suffixes", sl(".xlsx"), "total_gb", 3, "objects", 911, "per_day", 9, "script_known", false)),
		"buckets", sl("backup", "media"), "schedules", L()))
	g.writeJSON(ref+"/backup-templates/catalog.json", om(
		"version", "1.4.0", "about", "Backup job templates (demo catalogue).",
		"common_keys", sl("JOB_NAME", "ENGINE", "MODE", "DEST", "ENCRYPT", "AGE_RECIPIENTS_FILE", "COMPRESS", "LOCAL_KEEP_DAYS", "NOTES"),
		"engines", om(
			"postgres", om("label", "PostgreSQL", "default_port", 5432, "modes", sl("native", "docker"),
				"keys", sl("DB_HOST", "DB_PORT", "DB_USER", "DATABASES", "PG_DUMP_FORMAT"), "secret_file", "pgpass",
				"secret_format", "<host>:<port>:*:<user>:<password>", "raw_ext", "dump",
				"restore", "engines/postgres/restore.sh --latest backup/<bucket>/<prefix> --dry-run",
				"secret_sources", sl("file", "uri", "none"), "secret_file_uri", "connection.uri"),
			"mongodb", om("label", "MongoDB", "default_port", 27017, "modes", sl("native", "docker"),
				"keys", sl("MONGO_HOSTS", "REPLICA_SET", "READ_PREFERENCE", "AUTH_DB", "DB_USER", "DATABASES"),
				"secret_file", "mongo.yaml", "secret_format", "password: '<password>'", "raw_ext", "archive",
				"restore", "engines/mongodb/restore.sh --latest backup/<bucket>/<prefix> --dry-run",
				"secret_sources", sl("file", "uri", "none"), "secret_file_uri", "connection.uri"),
			"redis", om("label", "Redis", "default_port", 6379, "modes", sl("native"), "keys", sl("DB_HOST", "DB_PORT"),
				"secret_file", "redis.pass", "secret_format", "<password>", "raw_ext", "rdb",
				"restore", "engines/redis/restore.sh --latest backup/<bucket>/<prefix> --dry-run",
				"secret_sources", sl("file", "none")),
			"files", om("label", "Files", "default_port", 0, "modes", sl("native"), "keys", sl("PATHS", "EXCLUDE"),
				"secret_file", "", "secret_format", "", "raw_ext", "tar",
				"restore", "engines/files/restore.sh --latest backup/<bucket>/<prefix> --dry-run", "secret_sources", sl("none"))),
		"exit_codes", om(), "client_images", om(), "test_exit_codes", om()))
	plans := []*py.OMap{
		om("id", 1, "name", "orders-nightly", "engine", "postgres", "env", "release", "server", "prod-db-02 (replica)",
			"schedule_label", "daily 02:30", "bucket", "backup", "prefix", "pg/orders", "status", "merged",
			"mr", om("state", "merged", "url", GITLAB+"/ops/backup-templates/-/merge_requests/41"),
			"restore_tests", L(om("result", "ok", "date", g.day(9*D), "by", "sam.roe", "note", "restored to stg-db-01 in 41 min"))),
		om("id", 2, "name", "reviews-mongo", "engine", "mongodb", "env", "release", "server", "prod-mongo-02 (secondary)",
			"schedule_label", "daily 03:10", "bucket", "backup", "prefix", "mongo/reviews", "status", "proposal",
			"mr", om("state", "opened", "url", GITLAB+"/ops/backup-templates/-/merge_requests/47"), "restore_tests", L()),
		om("id", 3, "name", "matomo-db", "engine", "mariadb-mysql", "env", "release", "server", "prod-matomo-01",
			"schedule_label", "daily 04:00", "bucket", "backup", "prefix", "matomo", "status", "proposal",
			"mr", om("state", "opened", "url", GITLAB+"/ops/backup-templates/-/merge_requests/48"),
			"restore_tests", L(om("result", "partial", "date", g.day(20*D), "by", "casey.loe", "note", "3 tables missing"))),
		om("id", 4, "name", "staging-all", "engine", "postgres", "env", "pre-release", "server", "stg-db-01",
			"schedule_label", "daily 01:00", "bucket", "backup", "prefix", "pg/stg", "status", "merged",
			"mr", om("state", "merged", "url", GITLAB+"/ops/backup-templates/-/merge_requests/39"),
			"restore_tests", L(om("result", "ok", "date", g.day(4*D), "by", "fixer", "note", "drill passed"))),
	}
	states := om()
	for _, p := range plans {
		id := p.Get("id").(int)
		p.Set("created", g.t(float64(40-id*5)*D))
		p.Set("by", "sam.roe")
		p.Set("databases", "")
		p.Set("retention_days", 14)
		p.Set("files", om("PLAN.md", fmt.Sprintf("# Backup plan %s\n\nEngine %s on %s, %s, to %s/%s, age-encrypted.\n",
			p.S("name"), p.S("engine"), p.S("server"), p.S("schedule_label"), p.S("bucket"), p.S("prefix"))))
		g.writeJSON(fmt.Sprintf("backup-plans/plans/%d.json", id), p)
		states.Set(fmt.Sprint(id), om("state", "idle"))
	}
	g.writeJSON("settings-out/backup-plans-status.json", om("at", g.t(10*M), "plans", states))
	g.writeJSON("settings-out/backup-targets.json", om("at", g.t(3*H), "targets", L(
		om("name", "backup-east", "bucket", "backup", "prefix", "", "endpoint", "https://s3.east.northwind.example",
			"envs", sl("release"), "provision", true, "can_delete", false, "plans", 4,
			"test", om("result", "ok", "at", g.t(3*H), "list", "ok", "write", "ok", "delete", "denied (write-only)")),
		om("name", "backup-west", "bucket", "backup-dr", "prefix", "", "endpoint", "https://s3.west.northwind.example",
			"envs", sl("release"), "provision", false, "can_delete", false,
			"test", om("result", "ok", "at", g.t(3*H), "list", "ok", "write", "ok", "delete", "denied (write-only)")),
		om("name", "staging", "bucket", "backup-stg", "prefix", "", "endpoint", "https://s3.east.northwind.example",
			"envs", sl("pre-release", "develop"), "provision", false, "can_delete", true,
			"test", om("result", "ok", "at", g.t(26*H), "list", "ok", "write", "ok", "delete", "ok"))),
		"age_recipients", 2))
	g.writeJSON("settings-out/backup-drafts.json", om("drafts", om()))
}

type certT struct {
	label, issuer string
	days          int
	names         []string
}

var CERTS = []certT{
	{"*.northwind.example (wildcard)", "Example Trust RSA CA 2026", 41, sl("www", "api", "checkout", "pay", "account", "img", "cdn", "admin", "hooks", "wh")},
	{"sellers.northwind.example", "Let's Encrypt R11", 9, sl("sellers")},
	{"stats.northwind.example", "Let's Encrypt R10", 54, sl("stats")},
	{"*.stg.northwind.example (wildcard)", "Let's Encrypt R11", 63, sl("www.stg", "api.stg", "checkout.stg", "pay.stg")},
	{"*.dev.northwind.example (wildcard)", "Let's Encrypt R10", 22, sl("www.dev", "api.dev", "grafana.dev", "argus.dev")},
	{"status.northwind.example", "Let's Encrypt R11", 77, sl("status")},
	{"partners.northwind.example", "Let's Encrypt R10", 4, sl("partners")},
}

func (g *Gen) certs() {
	o := g.ops
	rid := g.oneInt(o, "SELECT max(id) FROM runs WHERE task='cert-check'")
	for _, c := range CERTS {
		status := "FAIL"
		if c.days >= 30 {
			status = "OK"
		} else if c.days >= 14 {
			status = "WARN"
		}
		sev := "warn"
		if status == "OK" {
			sev = "ok"
		}
		after := time.Unix(g.now+int64(c.days)*86400, 0).UTC().Format("2006-01-02 15:04:05")
		var fq []string
		for _, n := range c.names {
			fq = append(fq, n+"."+DOMAIN)
		}
		note := ""
		if c.days < 14 {
			note = "renewal failed: DNS-01 TXT record not found"
		}
		g.ins(o, "cert_groups", "run_id", rid, "kind", "cert", "gid", h12(c.label), "label", c.label, "status", status, "sev", sev,
			"days_left", c.days, "not_after", after, "issuer", c.issuer, "n_names", len(fq), "names", jd(fq), "sans", jd(fq), "san_mismatch", "[]",
			"note", note)
		for _, n := range fq {
			g.ins(o, "cert_results", "run_id", rid, "host", n, "status", status, "days_left", c.days, "not_after", after, "issuer", c.issuer)
		}
	}
	g.ins(o, "cert_groups", "run_id", rid, "kind", "unreachable", "gid", h12("legacy"), "label", "legacy.northwind.example", "status", "FAIL",
		"sev", "warn", "days_left", nil, "not_after", nil, "issuer", nil, "n_names", 1, "names", jd(sl("legacy.northwind.example")),
		"sans", "[]", "san_mismatch", "[]", "note", "connection refused (decommissioned 10-02?)")
	g.exec(o, "UPDATE runs SET status='FAIL', summary=? WHERE id=?",
		"FAIL: 2 certificates need a renewal now.\n\n- **partners.northwind.example** expires in 4 days (same hook).\n- **sellers.northwind.example** expires in 9 days; certbot's "+
			"DNS-01 renewal fails since 10-19 (TXT record `_acme-challenge.sellers` not created: the DNS API token of the "+
			"hook expired).\n- `*.dev.northwind.example` 22 days, renews automatically at 30.\n- `legacy.northwind.example` "+
			"unreachable: still listed in the domains file.", rid)
}
