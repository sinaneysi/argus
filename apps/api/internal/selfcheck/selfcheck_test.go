package selfcheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const now = int64(1800000000)
const day = int64(86400)

// The rules are the earlier self-check's rules; these cases are its unit tests (test_cert_selfcheck.py).

func TestJobRule(t *testing.T) {
	job := func(f func(*Job)) string {
		j := Job{Name: "cert-check", Enabled: true, Every: 24 * time.Hour, LastEnd: time.Unix(now-3600, 0)}
		if f != nil {
			f(&j)
		}
		return JobRule(j, now, now-30*day).Status
	}
	for name, c := range map[string]struct {
		f    func(*Job)
		want string
	}{
		"ok":                              {nil, "OK"},
		"3 days, daily":                   {func(j *Job) { j.LastEnd = time.Unix(now-3*day, 0) }, "FAIL"},
		"failed":                          {func(j *Job) { j.LastError = "exit 1" }, "WARN"},
		"not configured":                  {func(j *Job) { j.Enabled, j.Reason = false, "not configured: connect Alertmanager" }, "OK"},
		"never ran":                       {func(j *Job) { j.LastEnd = time.Time{} }, "FAIL"}, // the worker started 30 days ago
		"every minute, missed 20 minutes": {func(j *Job) { j.Every, j.LastEnd = time.Minute, time.Unix(now-1200, 0) }, "FAIL"},
		"every minute, 30 s ago":          {func(j *Job) { j.Every, j.LastEnd = time.Minute, time.Unix(now-30, 0) }, "OK"},
		"first run going":                 {func(j *Job) { j.LastEnd, j.Running, j.LastStart = time.Time{}, true, time.Unix(now-60, 0) }, "OK"},
	} {
		if got := job(c.f); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	if c := JobRule(Job{Name: "x", Enabled: true, Every: time.Hour}, now, now-60); c.Status != "WARN" || c.Detail != "never ran" {
		t.Errorf("fresh worker: %+v", c)
	}
}

func TestAgeDiskOverall(t *testing.T) {
	for _, c := range []struct {
		age  int64
		want string
	}{{100, "OK"}, {400, "WARN"}, {-1, "FAIL"}} {
		if got := Age("x", c.age, 300, 900, "", "").Status; got != c.want {
			t.Errorf("Age(%d) = %s, want %s", c.age, got, c.want)
		}
	}
	const G = 1 << 30
	for _, c := range []struct {
		free uint64
		want string
	}{{50 * G, "OK"}, {10 * G, "WARN"}, {G * 3 / 10, "FAIL"}} {
		if got := Disk("/v", c.free, 100*G).Status; got != c.want {
			t.Errorf("Disk(%d) = %s, want %s", c.free, got, c.want)
		}
	}
	if d := Disk("/v", 50*G, 100*G).Detail; d != "50.0 GiB free (50 %)" {
		t.Errorf("disk detail %q", d)
	}
	if Overall([]Check{{Status: "OK"}, {Status: "WARN"}}) != "WARN" || Overall(nil) != "OK" {
		t.Error("overall")
	}
	if Age("x", 7200, 300, 900, "made", "").Detail != "made 2h ago" || Human(-1) != "never" || Human(59) != "59s" {
		t.Error("human")
	}
}

func TestReasonInternal(t *testing.T) {
	for _, c := range []struct {
		r    Run
		want string
	}{
		{Run{Status: "ERROR", Summary: "ERROR: no result, see log"}, "no result from Claude"},
		{Run{Status: "FAIL", IsError: 1}, "Claude run error"},
		{Run{Status: "FAIL", Denials: 2, Summary: "FAIL: permission denied for mc"}, "2 sandbox denials"},
		{Run{Status: "FAIL", Denials: 1, Summary: "FAIL: 3 backup sets stale"}, ""},
		{Run{Status: "FAIL", Summary: "FAIL: 504s on checkout"}, ""},
	} {
		if got := Reason(c.r); got != c.want {
			t.Errorf("Reason(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
	runs := []Run{{Task: "log-rca", Status: "ERROR"}, {Task: "backup-check", Status: "OK", Summary: "OK"}}
	c := Internal("Argus", runs, map[string]int64{"cert-check": now - 60, "old": now - 2*day}, now)
	if c.Status != "WARN" || !strings.Contains(c.Detail, "log-rca: no result from Claude") ||
		!strings.Contains(c.Detail, "notify failed: cert-check") || strings.Contains(c.Detail, "old") {
		t.Errorf("internal: %+v", c)
	}
	if Internal("Argus", nil, nil, now).Status != "OK" {
		t.Error("internal ok")
	}
}

func TestPageOnlyAfterAnHour(t *testing.T) {
	var since *int64
	paged := false
	var seq []string
	for _, x := range []struct {
		t  int64
		st string
	}{{0, "FAIL"}, {1800, "FAIL"}, {3600, "FAIL"}, {3900, "FAIL"}, {4200, "OK"}, {4500, "OK"}} {
		var a string
		since, paged, a = PageDecision(since, paged, x.st, now+x.t, 3600)
		seq = append(seq, a)
	}
	if strings.Join(seq, ",") != ",,page,,recovered," {
		t.Errorf("sequence %q", seq)
	}
	s := now
	if _, _, a := PageDecision(&s, false, "WARN", now+7200, 3600); a != "" {
		t.Error("WARN never pages")
	}
}

func TestCredentials(t *testing.T) {
	if c := Credentials(nil, -1); len(c) != 1 || c[0].Status != "WARN" {
		t.Errorf("no file: %+v", c)
	}
	var doc map[string]any
	json.Unmarshal([]byte(`{"checked_at": 1, "attention": 2, "thresholds": {"warn_days": 30, "crit_days": 7}, "items": [
	 {"name": "GitLab token", "status": "expiring", "level": "WARN", "detail": "expires in 12 d"},
	 {"name": "Jira token", "status": "absent", "level": "OK"},
	 {"name": "TLS shop.example.com", "status": "expired", "level": "FAIL", "detail": "expired 2 d ago"}]}`), &doc)
	c := Credentials(doc, 3600)
	if len(c) != 3 || c[0].Status != "OK" || c[0].Detail != "checked 1h ago, 2 need attention (warn at 30 d, incident at 7 d)" ||
		c[1].Status != "WARN" || c[2].Status != "FAIL" || c[2].Name != "TLS shop.example.com" {
		t.Errorf("credentials: %+v", c)
	}
	if c := Credentials(doc, 51*3600); c[0].Status != "FAIL" {
		t.Errorf("stale: %+v", c[0])
	}
}

func TestCredStatusSourcesBackup(t *testing.T) {
	var a, b, s map[string]any
	json.Unmarshal([]byte(`{"items": [{"name": "GitLab", "ok": true}, {"name": "Grafana west", "ok": false}, {"name": "Matrix notify", "ok": false}]}`), &a)
	if c := CredStatus(a, 60); c.Status != "OK" || c.Detail != "updated 1m ago, 1 not OK (Grafana west)" {
		t.Errorf("items: %+v", c)
	}
	json.Unmarshal([]byte(`{"creds": {"b": {"ok": false}, "a": {"ok": true}}}`), &b)
	if c := CredStatus(b, 4*3600); c.Status != "WARN" || !strings.HasSuffix(c.Detail, "1 not OK (b)") {
		t.Errorf("creds: %+v", c)
	}
	json.Unmarshal([]byte(`{"sources": {"gitlab/main": {"status": "ok"}, "jira/main": {"status": "failed"}, "matrix/x": {"status": "unchecked"}, "legacy": {}}}`), &s)
	if c, ok := Sources(s); !ok || c.Status != "WARN" || c.Detail != "1 of 3 OK, 1 not checked yet: jira/main failed" {
		t.Errorf("sources: %+v", c)
	}
	if _, ok := Sources(nil); ok {
		t.Error("no sources: no check")
	}
	var bk map[string]any
	json.Unmarshal([]byte(`{"ts": 1799990000, "encrypted": true, "offhost": "ok", "restore_test": {"ts": 1799000000, "ok": false}}`), &bk)
	if c := Backup(bk, now); c.Status != "WARN" || c.Detail != "made 2h ago, encrypted, off-host ok, restore-test FAILED 11d ago" {
		t.Errorf("backup: %+v", c)
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ops.db")
	sdb, err := sql.Open("sqlite", "file:"+db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdb.Exec(`CREATE TABLE runs (id INTEGER PRIMARY KEY, task TEXT NOT NULL, started_at INTEGER NOT NULL, status TEXT, summary TEXT, denials INTEGER, is_error INTEGER);
INSERT INTO runs (task, started_at, status, summary) VALUES ('log-rca', 1799999000, 'ERROR', 'ERROR: no result');`); err != nil {
		t.Fatal(err)
	}
	sdb.Close()
	clock := now
	var sent []string
	pushes := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		if r.URL.Path != "/api/push/abcdef123" || r.URL.Query().Get("status") != "up" {
			t.Errorf("push %s", r.URL)
		}
		pushes++
	}))
	defer srv.Close()
	failing := true
	o := &Options{App: "Argus", StateDir: dir, Out: filepath.Join(dir, "selfcheck.json"), DBs: []string{db, filepath.Join(dir, "missing.db")},
		AppURL: srv.URL + "/healthz", PushURL: srv.URL + "/api/push/abcdef123", Client: srv.Client(), PageAfter: 3600,
		Now:     func() time.Time { return time.Unix(clock, 0) },
		Started: time.Unix(now-60, 0),
		Jobs: func() []Job {
			j := Job{Name: "board-sync", Enabled: true, Every: 2 * time.Minute, LastEnd: time.Unix(clock-30, 0)}
			if failing {
				j.LastEnd = time.Unix(clock-3600, 0)
			}
			return []Job{j, {Name: "selfcheck", Enabled: true}, {Name: "incidents", Reason: "not configured: connect Alertmanager"}}
		},
		Key:    func() Check { return Check{Name: "master key", Group: Group, Status: "OK", Detail: "from key file"} },
		Notify: func(text string) error { sent = append(sent, text); return nil },
	}
	ctx := context.Background()
	for _, step := range []int64{0, 1800, 3600, 300} {
		clock += step
		if _, err := o.Run(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var doc Doc
	b, _ := os.ReadFile(o.Out)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Status != "FAIL" || !doc.Paged || doc.FailingSince == nil || *doc.FailingSince != now || doc.Kuma != "skipped (FAIL)" {
		t.Errorf("doc: %+v", doc)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "FAIL: Argus-internal: Argus has been failing for 1h: board-sync last run 1h ago") {
		t.Errorf("sent %q", sent)
	}
	names := map[string]string{}
	for _, c := range doc.Checks {
		names[c.Name] = c.Status
	}
	if names["selfcheck"] != "" || names["incidents"] != "OK" || names["api /healthz"] != "OK" || names["database ops.db"] != "OK" ||
		names["master key"] != "OK" || names["Argus-internal failures"] != "WARN" || names["database missing.db"] != "" {
		t.Errorf("checks: %v", names)
	}
	failing = false
	clock += 300
	doc, _ = o.Run(ctx)
	if doc.Status != "WARN" || doc.Paged || doc.FailingSince != nil || doc.Kuma != "pushed" || pushes != 1 {
		t.Errorf("recovered: %+v pushes %d", doc, pushes)
	}
	if len(sent) != 2 || !strings.HasPrefix(sent[1], "OK: Argus-internal: Argus recovered after 1h") {
		t.Errorf("sent %q", sent)
	}
	o.PushURL = "http://example.com/x"
	if r := o.Push(ctx, "OK", "x"); !strings.HasPrefix(r, "failed (not an https") || strings.Contains(r, "example") {
		t.Errorf("push guard: %q", r)
	}
	if st, _ := os.Stat(o.Out); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
}
