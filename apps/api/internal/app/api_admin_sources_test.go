package app

// Settings → Data sources as JSON: admins only, the Go route's policy and limit, a refused save names the field and never
// echoes the credential, the add and edit blocks follow the query, no stored credential is read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1SourcesPolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/admin/sources": "/admin/sources"}, map[string]any{"action": "check-all"}, false)
}

func TestAPIv1SourcesRead(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/admin/sources", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/admin/sources", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	ck := map[string]string{auth.COOKIE: adm}
	r := e.req("GET", "/api/v1/admin/sources", ck, nil, nil)
	d := v1JSON(t, r)
	if len(d["panels"].([]any)) == 0 || d["block"] != "" || strings.Contains(r.body, "<span") {
		t.Fatal(r.body[:300])
	}
	if p := v1JSON(t, e.req("GET", "/api/v1/admin/sources?add=1", ck, nil, nil)); p["block"] != "picker" || len(p["types"].([]any)) != len(srcTypes) {
		t.Fatal(p["block"])
	}
	if p := v1JSON(t, e.req("GET", "/api/v1/admin/sources?add=grafana", ck, nil, nil)); p["block"] != "add" || p["form"].(map[string]any)["type"] != "grafana" {
		t.Fatal(p["block"])
	}
	if p := v1JSON(t, e.req("GET", "/api/v1/admin/sources?add=kubernetes", ck, nil, nil)); p["block"] != "picker" {
		t.Fatal("a managed type has no form here", p["block"])
	}
	// a stored source: its facts come back, a credential the status file might hold does not
	st := `{"sources":{"grafana/east":{"env":"develop","url":"https://g.northwind.example","purpose":"agent","status":"readonly","cred_last4":"ab12","credential":"not-a-real-value-123","managed":"registry"}}}`
	if err := os.WriteFile(filepath.Join(SET_OUT, "sources-status.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	r = e.req("GET", "/api/v1/admin/sources?edit=grafana/east", ck, nil, nil)
	f := v1JSON(t, r)
	if f["block"] != "edit" || f["form"].(map[string]any)["last4"] != "ab12" || strings.Contains(r.body, "not-a-real-value") {
		t.Fatal(r.body[:300])
	}
}

func TestAPIv1SourcesWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	if err := os.MkdirAll(filepath.Join(SET_IN, "sources"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := func(kv ...string) map[string]any {
		m := map[string]any{"action": "save", "mode": "add", "type": "grafana", "name": "east", "env": "develop", "purpose": "agent", "url": "https://g.northwind.example", "credential": v1FakeCred}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for field, b := range map[string]map[string]any{"name": base("name", "Bad Name"), "env": base("env", "x"), "url": base("url", "http://x")} {
		r := e.v1Post("/api/v1/admin/sources", ck, b, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != field || strings.Contains(r.body, v1FakeCred) {
			t.Fatal(field, r.status, r.body)
		}
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "sources", "*")); len(l) != 0 {
		t.Fatal("a refused save wrote a request", l)
	}
	r := e.v1Post("/api/v1/admin/sources", ck, map[string]any{"action": "check-all"}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/sources?sr=") {
		t.Fatal(r.status, r.body)
	}
}
