package app

// Tests of the backup port (views_backup, backup_simple, backup_plan, backup_uri) mirroring tests/test_backup.py and
// tests/test_backup_simple.py: connection strings, clean(), resolve, schedules (vectors from the Python modules),
// secretish, and the pages end to end (CSP, admins only, the string never echoed, 0600 drops, request files).
// Helpers are prefixed backup.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sinaneysi/argus/internal/py"
)

const backupPW = "p@ss:w/rd%?#&x' y"

var backupURI = "mongodb" + "://svc_backup:" + url.PathEscape(backupPW) + "@192.168.73.134:27017/shop?authSource=admin"

func backupLeaks(s string) bool {
	return strings.Contains(s, backupPW) || strings.Contains(s, url.PathEscape(backupPW)) ||
		strings.Contains(s, url.QueryEscape(backupPW))
}

func backupQS(kv ...string) py.QS { return py.NewQS(kv...) }

func TestBackupURIParse(t *testing.T) {
	enc := py.Quote(backupPW, "")
	u := "mongodb" + "://svc_backup:" + enc + "@192.168.73.134:27017/shop?authSource=admin"
	p, err := BUParse(u)
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "mongodb" || p.User != "svc_backup" || p.Password != backupPW || p.DB != "shop" || p.AuthDB != "admin" ||
		len(p.Hosts) != 1 || p.Hosts[0].Host != "192.168.73.134" || p.Hosts[0].Port != 27017 {
		t.Fatalf("%+v", p)
	}
	sum := sha256.Sum256([]byte(u))
	if BUFingerprint(" "+u+"\n") != hex.EncodeToString(sum[:]) {
		t.Fatal("fingerprint")
	}
	for uri, eng := range map[string]string{"mysql://u:p@h/db": "mariadb-mysql", "postgres://u:p@h/db": "postgres",
		"redis://:p@h:6379": "redis", "sqlserver://h:1433;databaseName=x;user=u;password=p": "mssql"} {
		p, err := BUParse(uri)
		if err != nil || p.Engine != eng {
			t.Errorf("%s: %v %v", uri, p, err)
		}
	}
	p, _ = BUParse("sqlserver://h:1433;databaseName=x;user=u;password=p")
	if p.User != "u" || p.Password != "p" || p.DB != "x" || p.Options.Has("user") {
		t.Fatalf("jdbc %+v", p)
	}
	for uri, msg := range map[string]string{"http://example.com": "unsupported scheme 'http'", "x": "not a connection string",
		"mongodb://u:p@[::1:1/x": "IPv6 host without a closing ]", "mongodb://u:p@::1/x": "an IPv6 host must be written in brackets",
		"postgres://u:p@h:0/x": "bad port '0'", "postgres://u:p@h:99999/x": "bad port '99999'", "postgres://u:p@/x": "no host",
		"redis://h/abc": "redis database must be a number", "mongodb+srv://u:p@a:1/x": "mongodb+srv takes one host"} {
		if _, err := BUParse(uri); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%s: %v", uri, err)
		}
	}
}

func TestBackupCleanRefusesWithoutEcho(t *testing.T) {
	_, errs := BSClean(backupQS("uri", backupURI, "databases", "shop", "db_password", "x", "evil", "1"))
	if !In("Unknown field 'db_password'.", errs...) || !In("Unknown field 'evil'.", errs...) {
		t.Fatal(errs)
	}
	_, errs = BSClean(backupQS("databases", "password=Sup3rSecretValue"))
	if len(errs) == 0 || strings.Contains(strings.Join(errs, " "), "Sup3rSecretValue") {
		t.Fatal(errs)
	}
	v, errs := BSClean(backupQS("token", "password=abcdef", "uri", " "+backupURI+" "))
	if len(errs) != 0 || v["uri"] != backupURI {
		t.Fatal("token and uri are not checked for secret values", errs)
	}
	if _, errs := BSClean(backupQS("cron", strings.Repeat("1", 201))); len(errs) != 1 || errs[0] != "cron: too long." {
		t.Fatal(errs)
	}
}

func TestBackupSecretishMatchesPython(t *testing.T) {
	cases := map[string]bool{"password=hunter22": true, "PASSWORD : abcd": true, "mypassword=abcdef": false, "x token:abc": false,
		"AKIAABCDEFGHIJKLMNOP": true, "xAKIAABCDEFGHIJKLMNOP": false, "https://u:pw1@h": true, "https://u:***@h": false,
		"https://u:ab@h": false, "glpat-abcdefghijklmnopqrst": true, "MC_HOST_x=1": true, "-----BEGIN RSA PRIVATE KEY": true,
		"plain text": false, "/ping/12345678-1234-": true, "secret = éééé": true, "ſecret=abcd": true}
	for s, want := range cases {
		if BPSecretish(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}

var backupVMs = []Row{{"name": "SRV-NW-01-34-stage3", "env": "develop", "ips": "192.168.73.134"},
	{"name": "pre-db-01", "env": "pre-release", "ips": "10.21.63.21"},
	{"name": "pre-db-02", "env": "pre-release", "ips": "10.21.63.22"},
	{"name": "pre-db-03", "env": "pre-release", "ips": "10.21.63.23"}}

func backupNoDNS(string) []string { return nil }

func TestBackupResolve(t *testing.T) {
	for host, lookup := range map[string]func(string) []string{"192.168.73.134": backupNoDNS, "SRV-NW-01-34-stage3": backupNoDNS,
		"stage3.dev.example": func(string) []string { return []string{"192.168.73.134"} }} {
		p, _ := BUParse("postgres://u:p@" + host + ":5432/shop")
		r := BSResolve(p, backupVMs, nil, lookup)
		if r.Kind != "single" || len(r.Members) != 1 || r.Members[0].S("name") != "SRV-NW-01-34-stage3" || r.Error != "" {
			t.Errorf("%s: %+v", host, r)
		}
	}
	p, _ := BUParse("mongodb://u:p@pre-db-01:27017,pre-db-02:27017,pre-db-03:27017/?replicaSet=rs0")
	r := BSResolve(p, backupVMs, nil, backupNoDNS)
	if r.Kind != "replica-set" || len(r.Members) != 3 || BSDefaultMember(r, nil) != "pre-db-03" ||
		BSDefaultMember(r, py.NewOMap("primary", "pre-db-03")) != "pre-db-02" {
		t.Fatalf("%+v", r)
	}
	p, _ = BUParse("mysql://u:p@10.21.63.100:3306/shop")
	r = BSResolve(p, backupVMs, py.NewOMap("10.21.63.100", py.NewOMap("members", []any{"pre-db-01", "pre-db-02"})), backupNoDNS)
	if r.Kind != "vip" || len(r.Members) != 2 || r.Error != "" {
		t.Fatalf("vip %+v", r)
	}
	for uri, msg := range map[string]string{"postgres://u:p@10.99.0.1/db": "not in the Inventory",
		"mongodb+srv://u:p@cluster.example/db": "mongodb+srv", "redis://:p@169.254.169.254:6379/0": "not in the Inventory",
		"mysql://u:p@127.0.0.1/x":                                "not in the Inventory",
		"mongodb://u:p@192.168.73.134,10.21.63.21/?replicaSet=x": "different environments"} {
		p, _ := BUParse(uri)
		if r := BSResolve(p, backupVMs, nil, backupNoDNS); !strings.Contains(r.Error, msg) {
			t.Errorf("%s: %q", uri, r.Error)
		}
	}
}

func TestBackupSchedulesMatchPython(t *testing.T) {
	now := time.Date(2026, 10, 20, 16, 0, 0, 0, BPTZ)
	want := map[string][]string{
		"30m":                      {"Tue 20 Oct 16:30", "Tue 20 Oct 17:00", "Tue 20 Oct 17:30", "Tue 20 Oct 18:00", "Tue 20 Oct 18:30"},
		"hourly":                   {"Tue 20 Oct 17:00", "Tue 20 Oct 18:00", "Tue 20 Oct 19:00", "Tue 20 Oct 20:00", "Tue 20 Oct 21:00"},
		"12h":                      {"Wed 21 Oct 03:00", "Wed 21 Oct 15:00", "Thu 22 Oct 03:00", "Thu 22 Oct 15:00", "Fri 23 Oct 03:00"},
		"daily":                    {"Wed 21 Oct 03:00", "Thu 22 Oct 03:00", "Fri 23 Oct 03:00", "Sat 24 Oct 03:00", "Sun 25 Oct 03:00"},
		"cron */7 2-4 * jan-mar *": {"Fri 01 Jan 02:00", "Fri 01 Jan 02:07", "Fri 01 Jan 02:14", "Fri 01 Jan 02:21", "Fri 01 Jan 02:28"},
		"cron 0 0 29 2 *":          nil,
		"cron 5 1 * * sun,7":       {"Sun 25 Oct 01:05", "Sun 01 Nov 01:05", "Sun 08 Nov 01:05", "Sun 15 Nov 01:05", "Sun 22 Nov 01:05"},
	}
	for k, w := range want {
		key, cron, _ := strings.Cut(k, " ")
		if got := BSNextRuns(key, cron, now); strings.Join(got, "|") != strings.Join(w, "|") {
			t.Errorf("%s: %v", k, got)
		}
	}
	errs := map[string]string{"61 * * * *": "minute: 61 is outside 0-59",
		"0 3 1 * 1":   "day-of-month and day-of-week together mean OR in cron but AND in systemd: use only one of them",
		"* * *":       "a cron expression has 5 fields: minute hour day-of-month month day-of-week",
		"1-x * * * *": "minute: 'x' is not a number", "*/0 * * * *": "minute: bad step '/0'",
		"":                       "enter a cron expression (5 fields: minute hour day-of-month month day-of-week)",
		strings.Repeat("a", 101): "the expression is too long", "5-1 * * * *": "minute: 5-1 is outside 0-59",
		"1,,2 * * * *": "minute: empty item"}
	for c, msg := range errs {
		if _, _, _, err := BSScheduleOf("cron", c); err == nil || err.Error() != msg {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func TestBackupStripInstall(t *testing.T) {
	in := "intro\n## Install\nrun x\n\n## Rollback\nr\n## Install\nlast"
	if got := backupStripInstall(in); got != "intro\n## Rollback\nr\n## Install\nlast" {
		t.Fatalf("%q", got)
	}
}

// --- end to end ------------------------------------------------------------------------------------------------

type backupEnv struct {
	*srvEnv
	admin, adminCSRF, viewer, viewerCSRF string
}

func backupSetup(t *testing.T) *backupEnv {
	e := srvSetup(t)
	d := e.dir
	for _, s := range []string{"in/backup-requests", "in/backup-secrets", "backup-plans/plans", "ref"} {
		os.MkdirAll(filepath.Join(d, s), 0o755)
	}
	oldPlans, oldRef := BackupPlansDir, BackupRefDir
	BackupPlansDir, BackupRefDir = filepath.Join(d, "backup-plans"), filepath.Join(d, "ref")
	t.Cleanup(func() { BackupPlansDir, BackupRefDir = oldPlans, oldRef })
	disc := `{"generated": "2026-10-20 <x>", "minio": {"host": "bk", "disk_pct": 83, "disk_used_tb": 4.1, "disk_total_tb": 5},
 "defects": [{"n": 1, "text": "no <encryption>", "where": "tools", "risk": "high", "issue": "STOR-17"}],
 "gaps": [{"n": 1, "what": "Ops Mongo app_db", "engine": "mongodb", "env": "release", "why": "none"}],
 "sets": [{"set": "tools_x", "encrypted": "no", "suffixes": [".gz"], "total_gb": 12.5, "family": "A", "script_known": true},
          {"set": "e_y", "encrypted": "yes", "objects": 3, "per_day": 1.5, "total_gb": 2, "family": "E"}],
 "families": [{"id": "A", "name": "fam", "how": "cron", "engine": "pg", "defects": [1]}]}`
	os.WriteFile(filepath.Join(d, "ref", "backup-discovery.json"), []byte(disc), 0o644)
	db, err := sql.Open("sqlite", filepath.Join(d, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE vms (id INTEGER PRIMARY KEY, name TEXT, env TEXT, ips TEXT, gone INTEGER DEFAULT 0)")
	for _, v := range backupVMs {
		db.Exec("INSERT INTO vms (name, env, ips) VALUES (?,?,?)", v["name"], v["env"], v["ips"])
	}
	db.Close()
	targets := `{"at": 1, "age_recipients": 1, "targets": [{"name": "develop-minio", "bucket": "backup-develop", "prefix": "argus",
  "envs": ["develop", "pre-release"], "retention_days": 14, "endpoint": "https://minio.dev.example:9000", "plans": 0,
  "test": {"result": "ok", "delete": "denied: write-only (good)"}}]}`
	os.WriteFile(filepath.Join(d, "out", "backup-targets.json"), []byte(targets), 0o644)
	plan := map[string]any{"id": 1, "kind": "simple", "name": "mongo-shop-stage3", "env": "develop", "engine": "mongodb",
		"server": "SRV-NW-01-34-stage3", "mode": "native", "bucket": "backup-develop", "prefix": "argus/mongo-shop-stage3",
		"schedule_label": "daily 03:00", "oncalendar": "*-*-* 03:00:00", "retention_days": 14, "status": "planned",
		"files":         map[string]any{"job.env": "JOB_NAME=x <y>\n", "PLAN.md": "# Backup plan x\n> **PROD**\n## Install\nx\n## Restore\nr"},
		"restore_tests": []any{map[string]any{"date": "2026-10-20", "result": "ok", "object": "o<b>"}}}
	b, _ := json.Marshal(plan)
	os.WriteFile(filepath.Join(d, "backup-plans", "plans", "1.json"), b, 0o644)
	prod := map[string]any{"id": 2, "kind": "simple", "name": "prod", "env": "release", "engine": "mongodb", "server": "db-01",
		"mode": "native", "bucket": "b", "prefix": "p"}
	b, _ = json.Marshal(prod)
	os.WriteFile(filepath.Join(d, "backup-plans", "plans", "2.json"), b, 0o644)
	be := &backupEnv{srvEnv: e}
	be.admin, be.adminCSRF = e.login("admin")
	be.viewer, be.viewerCSRF = e.login("view.er")
	return be
}

var backupUnsafe = regexp.MustCompile(`<script|\sstyle=|<style|javascript:|\son[a-z]+=`)

func (e *backupEnv) clean(r srvResp) {
	e.t.Helper()
	if r.header.Get("Content-Security-Policy") != CSP || backupUnsafe.MatchString(r.body) || backupLeaks(r.body) {
		e.t.Fatal("unsafe page")
	}
}

func (e *backupEnv) files(sub string) []string {
	ents, _ := os.ReadDir(filepath.Join(e.dir, "in", sub))
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

func TestBackupPagesAndCoverage(t *testing.T) {
	srvNeedsGroup(t, "board")
	backupSetup(t)

	if p := BackupPrefill("1-1"); p == nil || !strings.HasPrefix(p.S("title"), "Fix backup defect: Defect 1: no <encryption>") {
		t.Fatal("prefill coverage", p)
	}
	if BackupPrefill("9-9") != nil || BackupPrefill("1") == nil {
		t.Fatal("prefill")
	}
}

func TestBackupTestConnectionDropsOnce0600(t *testing.T) {
	e := backupSetup(t)
	ck := map[string]string{"opsr_session": e.admin}
	form := url.Values{"csrf": {e.adminCSRF}, "action": {"test"}, "uri": {backupURI}, "databases": {"shop"}, "target": {"develop-minio"}}
	r := e.req("POST", "/api/v1/backups/new", ck, form, nil)
	if r.status != 200 || backupLeaks(r.loc) || !regexp.MustCompile(`^/backups/new\?.*draft=[0-9a-f]{16}`).MatchString(r.loc) {
		t.Fatal(r.status, r.loc)
	}
	drops := e.files("backup-secrets")
	if len(drops) != 1 || !strings.HasPrefix(drops[0], "admin-") {
		t.Fatal(drops)
	}
	p := filepath.Join(e.dir, "in", "backup-secrets", drops[0])
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatal("mode", st.Mode())
	}
	var got map[string]any
	data, _ := os.ReadFile(p)
	json.Unmarshal(data, &got)
	if got["kind"] != "test" || got["uri"] != backupURI || got["sha"] != BUFingerprint(backupURI) || got["member"] != "SRV-NW-01-34-stage3" {
		t.Fatal(got)
	}
	// bad strings, unknown and internal hosts are refused before any drop; the string is never echoed
	for _, u := range []string{"http://x", "postgres://u:p@10.99.0.1/db", "redis://:p@169.254.169.254:6379/0"} {
		r := e.req("POST", "/api/v1/backups/new", ck, url.Values{"csrf": {e.adminCSRF}, "action": {"test"}, "uri": {u}}, nil)
		if r.status != 200 || strings.Contains(r.body, "u:p@") || strings.Contains(r.body, ":p@") {
			t.Fatal(u, r.status)
		}
	}
	if len(e.files("backup-secrets")) != 1 {
		t.Fatal("dropped a refused string")
	}
	// viewers neither test nor save; a wrong CSRF token is refused
	r = e.req("POST", "/api/v1/backups/new", map[string]string{"opsr_session": e.viewer},
		url.Values{"csrf": {e.viewerCSRF}, "action": {"test"}, "uri": {backupURI}}, nil)
	if r.status != 403 || backupLeaks(r.body) {
		t.Fatal("viewer")
	}
	r = e.req("POST", "/api/v1/backups/new", ck, url.Values{"csrf": {"wrong"}, "action": {"test"}, "uri": {backupURI}}, nil)
	if r.status != 403 || len(e.files("backup-secrets")) != 1 {
		t.Fatal("csrf")
	}
}

func TestBackupSaveBoundToTheTestedString(t *testing.T) {
	e := backupSetup(t)
	ck := map[string]string{"opsr_session": e.admin}
	draft := strings.Repeat("3", 16)
	rec := map[string]any{"drafts": map[string]any{draft: map[string]any{"user": "admin", "state": "ok", "sha": BUFingerprint(backupURI),
		"token": strings.Repeat("a", 40), "member": "SRV-NW-01-34-stage3", "engine": "mongodb", "env": "develop",
		"uri_redacted": "mongodb://svc_backup:***@192.168.73.134:27017/shop", "test": map[string]any{"result": "ok", "version": "7.0.14"},
		"members": []any{"SRV-NW-01-34-stage3"}, "topology": "single"}}}
	b, _ := json.Marshal(rec)
	os.WriteFile(filepath.Join(e.dir, "out", "backup-drafts.json"), b, 0o644)

	form := url.Values{"csrf": {e.adminCSRF}, "action": {"save"}, "draft": {draft}, "token": {strings.Repeat("b", 40)},
		"sha": {BUFingerprint(backupURI)}, "schedule": {"daily"}, "target": {"develop-minio"}}
	r := e.req("POST", "/api/v1/backups/new", ck, form, nil)
	if r.status != 200 || !strings.Contains(r.body, "not the one that was tested") || len(e.files("backup-requests")) != 0 {
		t.Fatal("wrong token accepted")
	}
	form.Set("token", strings.Repeat("a", 40))
	r = e.req("POST", "/api/v1/backups/new", ck, form, nil)
	reqs := e.files("backup-requests")
	if r.status != 200 || len(reqs) != 1 {
		t.Fatal("save", r.status, reqs)
	}
	data, _ := os.ReadFile(filepath.Join(e.dir, "in", "backup-requests", reqs[0]))
	if strings.Contains(string(data), `"uri"`) || backupLeaks(string(data)) ||
		!strings.HasPrefix(string(data), `{"kind": "simple", "form": {"schedule": "daily", "target": "develop-minio", "draft": "`+draft) {
		t.Fatal(string(data))
	}
}

func TestBackupRestoreInstallAndTargets(t *testing.T) {
	e := backupSetup(t)
	ck := map[string]string{"opsr_session": e.admin}
	r := e.req("POST", "/api/v1/backups/restore", ck, url.Values{"csrf": {e.adminCSRF}, "plan": {"1"}, "date": {"2026-10-21"}, "result": {"ok"},
		"object": {"o"}, "notes": {"n"}}, nil)
	if r.status != 200 || !strings.HasSuffix(r.loc, "#restore") || len(e.files("backup-requests")) != 1 {
		t.Fatal("restore", r.status)
	}
	if r := e.req("POST", "/api/v1/backups/restore", ck, url.Values{"csrf": {e.adminCSRF}, "plan": {"1"}, "date": {"2026-10-21"},
		"result": {"ok"}, "notes": {"password: hunter22"}}, nil); r.status != 400 {
		t.Fatal("secret note")
	}
	if r := e.req("POST", "/api/v1/backups/install", ck, url.Values{"csrf": {e.adminCSRF}, "plan": {"1"}}, nil); r.status != 200 {
		t.Fatal("install")
	}
	if r := e.req("POST", "/api/v1/backups/install", ck, url.Values{"csrf": {e.adminCSRF}, "plan": {"2"}}, nil); r.status != 400 {
		t.Fatal("install prod")
	}
	form := url.Values{"csrf": {e.adminCSRF}, "action": {"add"}, "name": {"dev2"}, "endpoint": {"https://minio.dev.example:9000"},
		"bucket": {"backup-dev"}, "access_key": {"writer"}, "secret_key": {"writer-secret-key-1"}, "envs": {"develop", "prod"}}
	r = e.req("POST", "/api/v1/admin/backup-targets", ck, form, nil)
	if r.status != 200 || !regexp.MustCompile(`^/admin/backup-targets\?req=[0-9a-f]{16}$`).MatchString(r.loc) {
		t.Fatal(r.status, r.loc)
	}
	drops := e.files("backup-secrets")
	data, _ := os.ReadFile(filepath.Join(e.dir, "in", "backup-secrets", drops[0]))
	if !strings.Contains(string(data), `"envs": ["develop"]`) || !strings.Contains(string(data), "writer-secret-key-1") {
		t.Fatal(string(data))
	}
	if strings.Contains(r.body, "writer-secret-key-1") {
		t.Fatal("the key came back")
	}
	if r := e.req("POST", "/api/v1/admin/backup-targets", ck, url.Values{"csrf": {e.adminCSRF}, "action": {"add"}, "name": {"Bad Name"}}, nil); !strings.Contains(r.body, "Name: 2-31") {
		t.Fatal("bad name")
	}

}
