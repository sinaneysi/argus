package app

// The Infrastructure section's JSON API on the test data: reads need a session and have their shape, admin-only choices
// are absent for a viewer, writes pass CSRF, role, 2FA and the demo refusal and drop the same request files as the pages.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var v1InfraReads = []string{"/api/v1/inventory", "/api/v1/capacity", "/api/v1/certs", "/api/v1/hygiene", "/api/v1/checks", "/api/v1/backups", "/api/v1/backups/new"}
var v1InfraWrites = map[string]string{"/api/v1/scan": "/scan", "/api/v1/inventory/refresh": "/inventory/refresh", "/api/v1/inventory/analyse": "/inventory/analyse",
	"/api/v1/capacity/analyse": "/capacity/analyse", "/api/v1/backups/new": "/backups/new", "/api/v1/backups/restore": "/backups/restore",
	"/api/v1/backups/install": "/backups/install", "/api/v1/backups/override": "/backups/override"}

func v1InfraData(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`INSERT INTO vms (id, key, name, env, dc, ips, gone, monitored, up, flags, first_seen, last_seen) VALUES (8001, 'k-8001', 'shop-app-1', 'develop', 'east', '10.0.0.5', 0, 1, 1, '', 1000, 2000),
	   (8002, 'k-8002', 'shop-db-1', 'release', 'east', '10.0.0.6', 0, 1, 1, 'ssh-skipped-db', 1000, 2000)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPIv1InfraReads(t *testing.T) {
	e := iaSetup(t)
	v1InfraData(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	for _, u := range append(append([]string{}, v1InfraReads...), "/api/v1/inventory/item?id=8001", "/api/v1/backups/set?name=nothing") {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal("no session", u, r.status)
		}
		for _, tok := range []string{adm, vw} {
			if r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil); r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
				t.Fatal(u, r.status, r.body)
			}
		}
	}
	get := func(tok, u string) map[string]any {
		t.Helper()
		return v1JSON(t, e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil))
	}
	l := get(adm, "/api/v1/inventory?q=shop-app")
	if rows := l["rows"].([]any); len(rows) != 1 || rows[0].(map[string]any)["id"].(float64) != 8001 || l["list"].(map[string]any)["matched"].(float64) != 1 {
		t.Fatal(v1Str(l))
	}
	if g := get(adm, "/api/v1/inventory?q=shop-app&go=1"); g["goto"] != "/inventory/view?id=8001" {
		t.Fatal(v1Str(g))
	}
	a, v := get(adm, "/api/v1/inventory/item?id=8001"), get(vw, "/api/v1/inventory/item?id=8001")
	if a["actions"].(map[string]any)["analyse"] == nil || v["actions"].(map[string]any)["analyse"] != nil || v["actions"].(map[string]any)["refresh"].(map[string]any)["can"] != true {
		t.Fatal(v1Str(a["actions"]), v1Str(v["actions"]))
	}
	db := get(adm, "/api/v1/inventory/item?id=8002")["actions"].(map[string]any)["analyse"].(map[string]any)
	if db["can"] != false || db["note"] != "not for DB hosts" {
		t.Fatal(v1Str(db))
	}
	for _, u := range []string{"/api/v1/inventory/item?id=99999", "/api/v1/inventory/item?id=x", "/api/v1/backups/plan?id=99"} {
		if r := e.req("GET", u, map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
			t.Fatal(u, r.status)
		}
	}
	if c := get(adm, "/api/v1/checks"); c["total"].(float64) != float64(len(TASKS)) || c["scan"].(map[string]any)["tasks"] != "all" {
		t.Fatal(v1Str(c))
	}
	if n := get(vw, "/api/v1/backups/new"); n["admin"] != false {
		t.Fatal(v1Str(n["admin"]))
	}
}

func reqFile(name string) bool {
	_, err := os.Stat(filepath.Join(SET_IN, "requests", name))
	return err == nil
}

func TestAPIv1InfraWrites(t *testing.T) {
	e := iaSetup(t)
	v1InfraData(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	body := map[string]any{"id": "8001"}
	if r := e.v1Post("/api/v1/inventory/refresh", nil, body, nil); r.status != 401 {
		t.Fatal("session", r.status)
	}
	if r := e.req("GET", "/api/v1/scan", ck(adm), nil, nil); r.status != 405 {
		t.Fatal("GET", r.status)
	}
	if r := e.reqBody("POST", "/api/v1/inventory/refresh", ck(adm), `{"id":"8001"}`, map[string]string{"Content-Type": "application/json"}); r.status != 403 || !strings.Contains(r.body, "csrf") {
		t.Fatal("csrf", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/refresh", ck(adm), body, map[string]string{"Origin": "https://elsewhere.example"}); r.status != 403 {
		t.Fatal("origin", r.status)
	}
	// a viewer refreshes, only an admin analyses; the request file appears, a second click waits
	if r := e.v1Post("/api/v1/inventory/refresh", ck(vw), body, nil); r.status != 200 || !strings.Contains(r.body, "sent=refresh") || !reqFile("vm-refresh-8001.req") {
		t.Fatal("refresh", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/refresh", ck(vw), body, nil); r.status != 429 {
		t.Fatal("twice", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/analyse", ck(vw), body, nil); r.status != 403 {
		t.Fatal("viewer analyse", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/analyse", ck(adm), body, nil); r.status != 200 || !reqFile("vm-explain-8001.req") {
		t.Fatal("analyse", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/analyse", ck(adm), map[string]any{"id": "8002"}, nil); r.status != 403 {
		t.Fatal("db host", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/inventory/refresh", ck(adm), map[string]any{"id": "99999"}, nil); r.status != 404 {
		t.Fatal("404", r.status)
	}
	// Scan all asks for every task of the Checks page; back stays local
	if r := e.v1Post("/api/v1/scan", ck(vw), map[string]any{"tasks": "all", "back": "//elsewhere.example/"}, nil); r.status != 200 || strings.Contains(r.body, "elsewhere") {
		t.Fatal("scan", r.status, r.body)
	}
	for _, task := range SCAN_TASKS["/operations"] {
		if !reqFile(task + ".req") {
			t.Fatal("no request for", task)
		}
	}
	if r := e.v1Post("/api/v1/capacity/analyse", ck(vw), map[string]any{"fs": "0123456789ab"}, nil); r.status != 403 {
		t.Fatal("viewer disk", r.status)
	}
	if r := e.v1Post("/api/v1/capacity/analyse", ck(adm), map[string]any{"fs": "nope"}, nil); r.status != 400 {
		t.Fatal("bad fs", r.status)
	}
	// backups: admins only; a bad string answers in words and the string never comes back
	secret := "mongodb://u:" + "hunter2secret" + "@"
	if r := e.v1Post("/api/v1/backups/new", ck(vw), map[string]any{"action": "test", "uri": secret}, nil); r.status != 403 {
		t.Fatal("viewer new", r.status)
	}
	r := e.v1Post("/api/v1/backups/new", ck(adm), map[string]any{"action": "test", "uri": secret, "databases": "shop"}, nil)
	m := v1JSON(t, r)
	if r.status != 200 || m["errors"] == nil || !strings.HasPrefix(m["redirect"].(string), "/backups/new?") || strings.Contains(r.body, "hunter2") {
		t.Fatal("bad uri", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/backups/new", ck(adm), map[string]any{"action": "save", "draft": "0123456789abcdef"}, nil); !strings.Contains(r.body, "Test the connection first") {
		t.Fatal("save untested", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/backups/override", ck(vw), map[string]any{"action": "undo", "id": "1"}, nil); r.status != 403 {
		t.Fatal("viewer override", r.status)
	}
	add := map[string]any{"action": "add", "set": "shop_db", "check": "STALE", "scope": "until-ok", "reason": "The job moved to the new cluster.", "back": "/backups/set?name=shop_db"}
	if r := e.v1Post("/api/v1/backups/override", ck(adm), add, nil); r.status != 200 || !strings.Contains(r.body, "sent=override") {
		t.Fatal("override", r.status, r.body)
	}
	add["reason"] = "short"
	if r := e.v1Post("/api/v1/backups/override", ck(adm), add, nil); r.status != 400 {
		t.Fatal("short reason", r.status, r.body)
	}
	for _, p := range []string{"/api/v1/backups/restore", "/api/v1/backups/install"} {
		if r := e.v1Post(p, ck(vw), map[string]any{"plan": "1"}, nil); r.status != 403 {
			t.Fatal("viewer", p, r.status)
		}
		if r := e.v1Post(p, ck(adm), map[string]any{"plan": "99"}, nil); r.status != 404 {
			t.Fatal("no plan", p, r.status)
		}
	}
}

func TestAPIv1InfraPolicyAndDemo(t *testing.T) {
	for api, page := range v1InfraWrites {
		if !apiIsPost(api) {
			t.Errorf("%s differs from %s", api, page)
		}
	}
	if POST_ROUTES["/api/v1/backups/override"] != PolicySensitive {
		t.Error("override is sensitive")
	}
	e := iaSetup(t)
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for p := range v1InfraWrites {
		r := e.v1Post(p, map[string]string{auth.COOKIE: adm}, map[string]any{"id": "1"}, nil)
		if r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(p, r.status, r.body)
		}
	}
}

func TestAPIv1InfraMFA(t *testing.T) {
	e := iaSetup(t)
	e.login("admin")
	nomfa, _ := e.loginNoMFA("admin")
	for p := range v1InfraWrites {
		r := e.v1Post(p, map[string]string{auth.COOKIE: nomfa}, map[string]any{"id": "1"}, nil)
		if m := v1JSON(t, r); r.status != 403 || m["error"] != "two-factor authentication required" || m["mfa"] != "stepup" {
			t.Fatal(p, r.status, r.body)
		}
	}
}
