package app

// The Incidents section's JSON API on the test data: every read needs a session and has its shape, admin-only choices
// are absent for a viewer, writes pass CSRF, role, 2FA and the demo refusal, and reports never carry HTML.

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var v1IncReads = []string{"/api/v1/incidents", "/api/v1/incidents/findings", "/api/v1/alerts", "/api/v1/problems", "/api/v1/rca"}
var v1IncWrites = []string{"/api/v1/incidents/act", "/api/v1/incidents/deep", "/api/v1/problems/act"}

func v1IncData(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", DB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`INSERT INTO incidents (id, key, title, subject, family, status, severity, env, dc, first_seen, last_seen, occurrences, customer_facing)
	   VALUES (7001, 'k-7001', 'Checkout 502 <b>spike</b>', 'shop.example.test', 'http-5xx', 'open', 'high', 'release', 'EAST', 1000, 2000, 3, 1)`,
		`INSERT INTO rca_reports (id, incident_id, title, path, created, status, source, content)
	   VALUES (7002, 7001, 'Upstream pool exhausted', 'reports/rca/x.md', 1500, 'open', 'deep', '## Finding
<script>alert(1)</script> pool **full**')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAPIv1IncidentsReads(t *testing.T) {
	e := iaSetup(t)
	v1IncData(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	all := append(append([]string{}, v1IncReads...), "/api/v1/incidents/item?id=7001", "/api/v1/rca/item?id=7002")
	for _, u := range all {
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
	l := get(adm, "/api/v1/incidents?status=all&q=spike")
	rows := l["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"].(float64) != 7001 || l["list"].(map[string]any)["matched"].(float64) != 1 {
		t.Fatal(v1Str(l))
	}
	if get(vw, "/api/v1/incidents")["bulk"] != nil {
		t.Fatal("a viewer gets the bulk bar")
	}
	a, v := get(adm, "/api/v1/incidents/item?id=7001"), get(vw, "/api/v1/incidents/item?id=7001")
	if !strings.Contains(v1Str(a["actions"]), "resolve") || strings.Contains(v1Str(v["actions"]), "resolve") || !strings.Contains(v1Str(v["actions"]), "ack") {
		t.Fatal(v1Str(a["actions"]), v1Str(v["actions"]))
	}
	if a["deep"] == nil || v["deep"] != nil || a["prod"] != true || a["facts"] == nil {
		t.Fatal("deep", a["deep"], v["deep"], a["prod"])
	}
	// a report is markdown text: the frontend renders elements, so the API sends no markup of its own
	rc := get(adm, "/api/v1/rca/item?id=7002")
	if md := v1Str(rc["md"]); !strings.Contains(md, "pool **full**") || strings.Contains(md, "<p>") {
		t.Fatal(md)
	}
	for _, u := range []string{"/api/v1/incidents/item?id=99999", "/api/v1/incidents/item?id=x", "/api/v1/rca/item?id=99999", "/api/v1/problems/item?id=P-99999",
		"/api/v1/incidents/run?id=99999", "/api/v1/incidents/finding?run=99999&idx=0", "/api/v1/incidents/report?f=../../etc/passwd"} {
		if r := e.req("GET", u, map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
			t.Fatal(u, r.status, r.body)
		}
	}
	td := get(adm, "/api/v1/today")
	inc := td["incidents"].(map[string]any)
	if inc["top"] == nil || len(td["checks"].(map[string]any)["hours"].([]any)) != 48 {
		t.Fatal(v1Str(td["incidents"]))
	}
}

func TestAPIv1IncidentsWrites(t *testing.T) {
	e := iaSetup(t)
	v1IncData(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if err := os.MkdirAll(filepath.Join(SET_IN, "incident-actions"), 0o777); err != nil {
		t.Fatal(err)
	}
	ack := map[string]any{"id": "7001", "action": "ack"}
	// no session, GET, no token, foreign origin
	if r := e.v1Post("/api/v1/incidents/act", nil, ack, nil); r.status != 401 {
		t.Fatal("session", r.status)
	}
	if r := e.req("GET", "/api/v1/incidents/act", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 405 {
		t.Fatal("GET", r.status)
	}
	if r := e.reqBody("POST", "/api/v1/incidents/act", map[string]string{auth.COOKIE: adm}, `{"id":"7001","action":"ack"}`, map[string]string{"Content-Type": "application/json"}); r.status != 403 ||
		!strings.Contains(r.body, "csrf") {
		t.Fatal("csrf", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/act", map[string]string{auth.COOKIE: adm}, ack, map[string]string{"Origin": "https://elsewhere.example"}); r.status != 403 {
		t.Fatal("origin", r.status)
	}
	// roles: a viewer acknowledges, only an admin resolves or starts a deep RCA
	if r := e.v1Post("/api/v1/incidents/act", map[string]string{auth.COOKIE: vw}, ack, nil); r.status != 200 || !strings.Contains(r.body, "sent=ack") {
		t.Fatal("viewer ack", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/act", map[string]string{auth.COOKIE: vw}, map[string]any{"id": "7001", "action": "resolve"}, nil); r.status != 403 {
		t.Fatal("viewer resolve", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/deep", map[string]string{auth.COOKIE: vw}, map[string]any{"id": "7001"}, nil); r.status != 403 {
		t.Fatal("viewer deep", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/act", map[string]string{auth.COOKIE: adm}, map[string]any{"id": "7001", "action": "resolve", "note": "fixed"}, nil); r.status != 200 {
		t.Fatal("admin resolve", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/act", map[string]string{auth.COOKIE: adm}, map[string]any{"id": "99999", "action": "ack"}, nil); r.status != 404 {
		t.Fatal("404", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/deep", map[string]string{auth.COOKIE: adm}, map[string]any{"id": "7001"}, nil); r.status != 200 {
		t.Fatal("deep", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/incidents/deep", map[string]string{auth.COOKIE: adm}, map[string]any{"id": "7001"}, nil); r.status != 429 {
		t.Fatal("deep twice", r.status, r.body)
	}
	// problems: a viewer may only suggest a link, arrays stay repeated fields
	if r := e.v1Post("/api/v1/problems/act", map[string]string{auth.COOKIE: vw}, map[string]any{"action": "create", "title": "Pool exhausted"}, nil); r.status != 403 && r.status != 404 && r.status != 409 {
		t.Fatal("viewer create", r.status, r.body)
	}
	q := apiForm(map[string]any{"ref": []any{"incident:1", "incident:2"}, "action": "link"})
	if len(q.List("ref")) != 2 || q.Get("action") != "link" {
		t.Fatal(q.List("ref"))
	}
}

func TestAPIv1IncidentsPolicyAndDemo(t *testing.T) {
	for _, p := range v1IncWrites {
		if !apiIsPost(p) || POST_ROUTES[p] != PolicyNormal {
			t.Errorf("%s policy", p)
		}
	}
	e := iaSetup(t)
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for _, p := range v1IncWrites {
		r := e.v1Post(p, map[string]string{auth.COOKIE: adm}, map[string]any{"id": "1", "action": "ack"}, nil)
		if r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(p, r.status, r.body)
		}
	}
}

func TestAPIv1IncidentsMFA(t *testing.T) {
	e := iaSetup(t)
	e.login("admin") // enrols the account
	nomfa, _ := e.loginNoMFA("admin")
	for _, p := range v1IncWrites {
		r := e.v1Post(p, map[string]string{auth.COOKIE: nomfa}, map[string]any{"id": "1", "action": "ack"}, nil)
		if m := v1JSON(t, r); r.status != 403 || m["error"] != "two-factor authentication required" || m["mfa"] != "stepup" {
			t.Fatal(p, r.status, r.body)
		}
	}
}

func TestAPISegsKeepsOnlySafeLinks(t *testing.T) {
	got := apiSegs(`from <a href="/rca/view?id=1&amp;x=2">RCA #1</a>, not <a href="javascript:alert(1)">this</a> or <a href="//evil.test/">that</a>`)
	if v1Str(got) == "" || got[1].Href != "/rca/view?id=1&x=2" || got[3].Href != "" || got[5].Href != "" {
		t.Fatal(v1Str(got))
	}
}

// Problems turned off: the API has no problem pages and the sidebar has no Problems link.
func TestAPIv1ProblemsOff(t *testing.T) {
	e := srvSetup(t)
	probSeed(t, e, false, true)
	tok, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	for _, u := range []string{"/api/v1/problems", "/api/v1/problems/item?id=1"} {
		if r := e.req("GET", u, ck, nil, nil); r.status != 404 {
			t.Fatal(u, r.status, r.body)
		}
	}
	if r := e.req("GET", "/api/v1/nav", ck, nil, nil); strings.Contains(r.body, `"/problems"`) {
		t.Fatal("Problems in the sidebar")
	}
}

// Problem actions need the request directory: without it a write answers 409 and writes nothing.
func TestAPIv1ProblemActionsNoDir(t *testing.T) {
	e := srvSetup(t)
	probSeed(t, e, true, false)
	tok, csrf := e.login("admin")
	if r := e.req("POST", "/api/v1/problems/act", map[string]string{auth.COOKIE: tok}, url.Values{"csrf": {csrf}, "action": {"accept"}, "link": {"3"}}, nil); r.status != 409 {
		t.Fatal(r.status, r.body)
	}
}
