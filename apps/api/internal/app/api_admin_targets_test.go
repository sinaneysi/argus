package app

// Settings → Backup targets and Credentials as JSON: admins only, the Go route's policy and limit, a refused add names the
// field and never echoes a key, a stored key never reaches an answer, the credentials answer is metadata only.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1BackupTargetsPolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/admin/backup-targets": "/admin/backup-targets"}, map[string]any{"action": "test", "name": "dev-minio"}, false)
}

func v1Admins(t *testing.T, e *srvEnv, path string) map[string]string {
	t.Helper()
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", path, nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", path, map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	return map[string]string{auth.COOKIE: adm}
}

func TestAPIv1BackupTargetsRead(t *testing.T) {
	e := iaSetup(t)
	ck := v1Admins(t, e, "/api/v1/admin/backup-targets")
	doc := `{"age_recipients":2,"targets":[{"name":"dev-minio","type":"minio","endpoint":"https://m.example:9000","bucket":"b1","prefix":"p","envs":["develop"],
	"provision":true,"access_key":"` + v1FakeCred + `","secret_key":"` + v1FakeCred + `","plans":1,"test":{"result":"ok","at":1800000000,"list":"ok","delete":"denied"}},
	{"name":"no-test","bucket":"b2"}]}`
	if err := os.WriteFile(filepath.Join(SET_OUT, "backup-targets.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.req("GET", "/api/v1/admin/backup-targets", ck, nil, nil)
	d := v1JSON(t, r)
	if strings.Contains(r.body, v1FakeCred) || strings.Contains(r.body, "<span") {
		t.Fatal(r.body)
	}
	rows := d["rows"].([]any)
	a, b := rows[0].(map[string]any), rows[1].(map[string]any)
	if len(rows) != 2 || d["age_recipients"].(float64) != 2 || a["plans"].(float64) != 1 || a["envs"] != "develop" || !strings.Contains(a["ilm"].(string), `--prefix "p/"`) {
		t.Fatal(r.body)
	}
	if a["badge"].(map[string]any)["text"] != "per-job users" || b["badge"].(map[string]any)["text"] != "not tested" || b["envs"] != "all environments" {
		t.Fatal(r.body)
	}
	if err := os.Remove(filepath.Join(SET_OUT, "backup-targets.json")); err != nil {
		t.Fatal(err)
	}
	if d := v1JSON(t, e.req("GET", "/api/v1/admin/backup-targets", ck, nil, nil)); len(d["rows"].([]any)) != 0 || d["age_recipients"].(float64) != 0 {
		t.Fatal(d)
	}
}

func TestAPIv1BackupTargetsWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	dir := filepath.Join(SET_IN, "backup-secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := func(k, v string) map[string]any {
		b := map[string]any{"action": "add", "name": "dev-minio", "endpoint": "https://m.example:9000", "bucket": "backup-dev", "access_key": "abc", "secret_key": "not-a-real-value-123", "envs": []any{"develop", "x"}}
		b[k] = v
		return b
	}
	for field, b := range map[string]map[string]any{
		"name": good("name", "Bad Name"), "endpoint": good("endpoint", "ftp://x"), "bucket": good("bucket", "B"), "access_key": good("access_key", ""),
		"secret_key": good("secret_key", "short"), "retention_days": good("retention_days", "999"),
	} {
		r := e.v1Post("/api/v1/admin/backup-targets", ck, b, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != field || m["form"] != "target-add" || strings.Contains(r.body, v1FakeCred) {
			t.Fatal(field, r.status, r.body)
		}
	}
	if l, _ := filepath.Glob(filepath.Join(dir, "*")); len(l) != 0 {
		t.Fatal("a refused add wrote a request", l)
	}
	if r := e.v1Post("/api/v1/admin/backup-targets", ck, map[string]any{"action": "nope", "name": "dev-minio"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	r := e.v1Post("/api/v1/admin/backup-targets", ck, good("prefix", "argus"), nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/backup-targets?req=") || strings.Contains(r.body, v1FakeCred) {
		t.Fatal(r.status, r.body)
	}
	l, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(l) != 1 {
		t.Fatal(l)
	}
	st, _ := os.Stat(l[0])
	raw, _ := os.ReadFile(l[0])
	if st.Mode().Perm() != 0o600 || !strings.Contains(string(raw), `"envs":["develop"]`) && !strings.Contains(string(raw), `"envs": ["develop"]`) {
		t.Fatal(st.Mode(), "the envs did not arrive as a list without the unknown one")
	}
}

func TestAPIv1CredentialsRead(t *testing.T) {
	e := iaSetup(t)
	ck := v1Admins(t, e, "/api/v1/admin/credentials")
	p := CredExpiryPath()
	_ = os.Remove(p)
	if d := v1JSON(t, e.req("GET", "/api/v1/admin/credentials", ck, nil, nil)); d["checked"] != false || d["scan"].(map[string]any)["meta"] != "Never checked" {
		t.Fatal(d)
	}
	doc := `{"checked_at":1800000000,"thresholds":{"warn_days":21},"next":{"name":"a","days_left":4},"certificates":{"link":"https://evil.example/"},"items":[
	{"id":"a","name":"Jira bot","kind_label":"API token","used_by":"Board","where":"/etc/x","status":"crit","band":"crit","days_left":4,"expires_at":"2027-01-01","identity":"bot","value":"` + v1FakeCred + `","howto":["Rotate it."]},
	{"id":"b","name":"Key","kind_label":"API key","status":"rotate","band":"unknown","rotate_due":"2026-01-01","rotate_days":180,"note":"policy"},
	{"id":"c","name":"Matrix","kind_label":"token","status":"ok","band":"never","howto":["Log in."]},
	{"id":"d","name":"Gone","kind_label":"token","status":"absent","howto":["No."]},
	{"id":"e","name":"Bad","kind_label":"token","status":"rejected","detail":"401","expires_at":"2027-01-01","error":"no answer"}]}`
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.req("GET", "/api/v1/admin/credentials", ck, nil, nil)
	d := v1JSON(t, r)
	if strings.Contains(r.body, v1FakeCred) || strings.Contains(r.body, "<span") || strings.Contains(r.body, "evil.example") {
		t.Fatal(r.body)
	}
	bad, ok, absent := d["bad"].([]any), d["ok"].([]any), d["absent"].([]any)
	if len(bad) != 3 || len(ok) != 1 || len(absent) != 1 || d["certs"] == nil {
		t.Fatal(r.body)
	}
	x := bad[0].(map[string]any)["expires"].(map[string]any)
	if x["date"] != "2027-01-01" || x["pill"].(map[string]any)["text"] != "4 d" || x["pill"].(map[string]any)["kind"] != "fail" {
		t.Fatal(x)
	}
	if y := bad[1].(map[string]any)["expires"].(map[string]any); y["rotate_now"] != true || !strings.Contains(y["rotate"].(string), "due 2026-01-01") {
		t.Fatal(y)
	}
	if z := bad[2].(map[string]any); z["expires"].(map[string]any)["pill"].(map[string]any)["text"] != "rejected" || z["detail"] != "401; check failed: no answer" {
		t.Fatal(z)
	}
	if h := d["howto"].([]any); len(h) != 2 || h[0].(map[string]any)["kind"] != "API token" {
		t.Fatal(h)
	}
	if th := d["thresholds"].(map[string]any); th["warn"].(float64) != 21 || th["rotate"].(float64) != 180 {
		t.Fatal(th)
	}
	if err := os.MkdirAll(filepath.Join(SET_IN, "requests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(SET_IN, "requests", credTask+".req"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if d := v1JSON(t, e.req("GET", "/api/v1/admin/credentials", ck, nil, nil)); d["scan"].(map[string]any)["busy"] != true || d["refresh"] == nil {
		t.Fatal(d)
	}
}
