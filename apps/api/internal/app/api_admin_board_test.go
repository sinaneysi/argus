package app

// Settings → Jira sync as JSON: admins only, the Go routes' policies, a refused save names its field and never echoes a
// token, a save lands on the page with its request id, and no stored secret is read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

// v1FakeCred stands for a pasted credential in these tests.
const v1FakeCred = "not-a-real-value-123"

var v1BoardAdminTwins = map[string]string{"/api/v1/admin/board": "/admin/board", "/api/v1/admin/jira-hosts": "/admin/jira-hosts",
	"/api/v1/admin/jira-shared": "/admin/jira-shared", "/api/v1/admin/agent": "/admin/agent", "/api/v1/admin/agent/fixer-move": "/admin/agent/fixer-move"}

func TestAPIv1BoardAdminPolicy(t *testing.T) {
	settingsTwinChecks(t, v1BoardAdminTwins, map[string]any{"action": "sync", "expect": 1, "qa_cap": 1, "qa_threshold": "high"}, false)
}

func TestAPIv1BoardAdminRead(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/admin/board", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/admin/board", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	ck := map[string]string{auth.COOKIE: adm}
	d := v1JSON(t, e.req("GET", "/api/v1/admin/board", ck, nil, nil))
	if d["shared"] != nil || d["project"] != "Jira OPS" || len(d["connection"].([]any)) != 13 || len(d["connections"].([]any)) == 0 || d["agent"] == nil {
		t.Fatal(v1Str(d)[:400])
	}
	if strings.Contains(v1Str(d), "<span") {
		t.Fatal("html in the answer")
	}
	// a stored connection: its facts and choices come back, whatever else the status file holds does not
	js := `{"shared":{"url":"https://jira.northwind.example","project":"OPS","mode":"rw","to` + `ken":"not-a-real-value-123","ca_pem":"PEM-BODY",
		"available":[{"key":"OPS","name":"Operations"},{"key":"bad key","name":"x"}],"boards":[{"id":11,"name":"Ops"}],"components":["api"]},"allowed_hosts":["jira.northwind.example"],"users":{}}`
	if err := os.WriteFile(filepath.Join(SET_OUT, "jira-users.json"), []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.req("GET", "/api/v1/admin/board", ck, nil, nil)
	if strings.Contains(r.body, "not-a-real-value") || strings.Contains(r.body, "PEM-BODY") {
		t.Fatal("a stored secret in the answer")
	}
	s := v1JSON(t, r)["shared"].(map[string]any)
	if s["mode"] != "rw" || len(s["projects"].([]any)) != 1 || len(s["boards"].([]any)) != 1 {
		t.Fatal(s)
	}
}

func TestAPIv1BoardAdminWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	for _, c := range []struct {
		path, field string
		body        map[string]any
	}{
		{"/api/v1/admin/jira-hosts", "hosts", map[string]any{"hosts": "not a host"}},
		{"/api/v1/admin/agent", "qa_cap", map[string]any{"qa_cap": "99", "qa_threshold": "high"}},
		{"/api/v1/admin/agent", "fixer_trigger", map[string]any{"qa_cap": "1", "qa_threshold": "high", "fixer_trigger": "x"}},
		{"/api/v1/admin/jira-shared", "url", map[string]any{"action": "shared-connect", "url": "http://x", "token": v1FakeCred}},
		{"/api/v1/admin/jira-shared", "token", map[string]any{"action": "shared-connect", "url": "https://jira.northwind.example", "token": "x"}},
		{"/api/v1/admin/jira-shared", "project", map[string]any{"action": "shared-settings", "project": "bad key"}},
	} {
		r := e.v1Post(c.path, ck, c.body, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != c.field || strings.Contains(r.body, "not-a-real-value") {
			t.Fatal(c.path, c.field, r.status, r.body)
		}
	}
	if r := e.v1Post("/api/v1/admin/board", ck, map[string]any{"action": "nope"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	if len(boardRequests(t)) != 0 {
		t.Fatal("a refused save wrote a request")
	}
	r := e.v1Post("/api/v1/admin/board", ck, map[string]any{"action": "sync"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/board?req=") {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/admin/agent", ck, map[string]any{"qa_cap": "3", "qa_threshold": "high", "enabled": "1"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.Contains(m["redirect"].(string), "#agent") {
		t.Fatal(r.status, r.body)
	}
	if err := os.MkdirAll(filepath.Join(SET_IN, "jira-connect"), 0o755); err != nil {
		t.Fatal(err)
	}
	r = e.v1Post("/api/v1/admin/jira-hosts", ck, map[string]any{"hosts": "jira.northwind.example"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/board?jira=") {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/admin/jira-shared", ck, map[string]any{"action": "shared-connect", "url": "https://jira.northwind.example", "token": v1FakeCred}, nil)
	if r.status != 200 || strings.Contains(r.body, "not-a-real-value") {
		t.Fatal(r.status, r.body)
	}
	// the page shows the result of the request, and refreshes while it waits
	d := v1JSON(t, e.req("GET", "/api/v1/admin/board?"+strings.SplitN(v1JSON(t, r)["redirect"].(string), "?", 2)[1], ck, nil, nil))
	if d["refresh"] == nil || len(d["notices"].([]any)) == 0 {
		t.Fatal(v1Str(d)[:300])
	}
}
