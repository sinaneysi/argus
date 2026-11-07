package app

// The Agent section's JSON API on the demo test data: reads need a session and have their shape, the fix pages are for
// admins only, and the Take action write passes CSRF, role, recent 2FA and the demo refusal and drops the same request
// file as the Go page.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1AgentReads(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	get := func(tok, u string) map[string]any {
		t.Helper()
		r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil)
		if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
			t.Fatal(u, r.status, r.body)
		}
		return v1JSON(t, r)
	}
	for _, u := range []string{"/api/v1/runs", "/api/v1/runs/item?id=1", "/api/v1/knowledge", "/api/v1/fix", "/api/v1/fix/run?id=x", "/api/v1/fix/review"} {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal("no session", u, r.status)
		}
	}
	o := get(vw, "/api/v1/runs")
	rows := o["rows"].([]any)
	if len(rows) == 0 || len(o["days"].([]any)) != 30 || len(o["facets"].([]any)) != 2 || o["list"].(map[string]any)["path"] != "/runs" {
		t.Fatal(v1Str(o))
	}
	if f := get(vw, "/api/v1/runs?task=chat-triage&status=OK&sort=oldest"); f["list"].(map[string]any)["filtered"] != true {
		t.Fatal(v1Str(f))
	} else {
		for _, x := range f["rows"].([]any) {
			if x.(map[string]any)["task"] != "chat-triage" {
				t.Fatal(x)
			}
		}
	}
	if f := get(vw, "/api/v1/runs?q=no-such-words-here"); len(f["rows"].([]any)) != 0 || f["list"].(map[string]any)["total"] == float64(0) {
		t.Fatal(v1Str(f))
	}
	chat := get(vw, "/api/v1/runs?task=chat-triage")["rows"].([]any)
	seen := false
	for _, x := range chat {
		d := get(vw, "/api/v1/runs/item?id="+I2S(int64(x.(map[string]any)["id"].(float64))))
		if d["showMessages"] != true || len(d["rail"].([]any)) != 9 {
			t.Fatal(v1Str(d))
		}
		if len(d["messages"].([]any)) > 0 && len(d["findings"].([]any)) > 0 {
			seen = true
			if strings.Contains(v1Str(d), "<a ") {
				t.Fatal("HTML in the API", v1Str(d))
			}
			break
		}
	}
	if !seen {
		t.Fatal("no run with messages and findings")
	}
	for _, u := range []string{"/api/v1/runs/item?id=x", "/api/v1/runs/item?id=9999999", "/api/v1/runs/item"} {
		if r := e.req("GET", u, map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 404 {
			t.Fatal(u, r.status)
		}
	}
	k := get(vw, "/api/v1/knowledge")
	if k["count"].(float64) == 0 || !strings.Contains(k["md"].(string), "## ") || k["admin"] != false || len(k["services"].([]any)) == 0 {
		t.Fatal(v1Str(k))
	}
	if get(adm, "/api/v1/knowledge")["admin"] != true {
		t.Fatal("admin flag")
	}
	for _, u := range []string{"/api/v1/fix", "/api/v1/fix/run?id=20261024103011-a1b2c3d4", "/api/v1/fix/review?run=1&idx=0"} {
		if r := e.req("GET", u, map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
			t.Fatal("viewer", u, r.status)
		}
	}
	l := get(adm, "/api/v1/fix")
	fr := l["rows"].([]any)
	if len(fr) == 0 || l["state"] != "set up" || len(l["notices"].([]any)) != 1 {
		t.Fatal(v1Str(l))
	}
	d := get(adm, "/api/v1/fix/run?id="+url.QueryEscape(fr[0].(map[string]any)["id"].(string)))
	rep, _ := d["report"].(map[string]any)
	if rep == nil || rep["prompt"] == "" || rep["rollback"] == nil || d["wait"] != nil {
		t.Fatal(v1Str(d))
	}
	if r := e.req("GET", "/api/v1/fix/run?id=20200101000000-00000000", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
	if r := e.req("GET", "/api/v1/fix/review?run=999999&idx=0", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal(r.status, r.body)
	}
}

// fixProblem is a develop or pre-release finding with a prompt, and a production one.
func fixProblem(t *testing.T) (ready, prod Row) {
	t.Helper()
	for _, p := range Q("SELECT run_id, idx, environment FROM triage_problems ORDER BY run_id") {
		if fixerProblem(I2S(p.I("run_id")), I2S(p.I("idx"))).T("fix_prompt") {
			if p.S("environment") == "release" {
				prod = p
			} else if ready == nil {
				ready = p
			}
		}
	}
	if ready == nil || prod == nil {
		t.Fatal("the demo data has no fixable problem")
	}
	return ready, prod
}

func TestAPIv1FixReview(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	p, prod := fixProblem(t)
	src := "run=" + I2S(p.I("run_id")) + "&idx=" + I2S(p.I("idx"))
	d := v1JSON(t, e.req("GET", "/api/v1/fix/review?"+src, ck(adm), nil, nil))
	if d["ready"] != true || len(d["hosts"].([]any)) == 0 || d["prompt"] == "" || !strings.Contains(d["back"].(map[string]any)["href"].(string), "&idx=") {
		t.Fatal(v1Str(d))
	}
	o := v1JSON(t, e.req("GET", "/api/v1/fix/review?run="+I2S(prod.I("run_id"))+"&idx="+I2S(prod.I("idx")), ck(adm), nil, nil))
	if o["ready"] != false || o["prompt"] != nil || !strings.Contains(v1Str(o), "never acts on production") {
		t.Fatal(v1Str(o))
	}
	host := d["hosts"].([]any)[0].(string)
	body := map[string]any{"run": I2S(p.I("run_id")), "idx": I2S(p.I("idx")), "prompt": "Restart the worker on the host.", "host": []any{host}, "reviewed": "1"}
	if r := e.v1Post("/api/v1/fix/review", nil, body, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("POST", "/api/v1/fix/review", ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
		t.Fatal("no csrf", r.status)
	}
	if r := e.v1Post("/api/v1/fix/review", ck(vw), body, nil); r.status != 403 {
		t.Fatal("viewer", r.status, r.body)
	}
	for k, want := range map[string]string{"reviewed": "Tick", "host": "Pick at least one"} {
		b := map[string]any{}
		for x, v := range body {
			if x != k {
				b[x] = v
			}
		}
		if r := e.v1Post("/api/v1/fix/review", ck(adm), b, nil); r.status != 400 || !strings.Contains(r.body, want) {
			t.Fatal(k, r.status, r.body)
		}
	}
	files := func() []string {
		l, _ := filepath.Glob(filepath.Join(SET_IN, "fix-requests", "*.json"))
		return l
	}
	if len(files()) != 0 {
		t.Fatal(files())
	}
	r := e.v1Post("/api/v1/fix/review", ck(adm), body, nil)
	m := v1JSON(t, r)
	if r.status != 200 || m["ok"] != true || !strings.HasPrefix(m["redirect"].(string), "/admin/fix/run?id=") {
		t.Fatal(r.status, r.body)
	}
	l := files()
	if len(l) != 1 {
		t.Fatal(l)
	}
	raw, _ := os.ReadFile(l[0])
	if !strings.Contains(string(raw), host) || !strings.Contains(string(raw), "Restart the worker") {
		t.Fatal(string(raw))
	}
	w := v1JSON(t, e.req("GET", "/api/v1"+strings.Replace(m["redirect"].(string), "/admin", "", 1), ck(adm), nil, nil))
	if w["refresh"] != true || w["wait"].(map[string]any)["kind"] != "busy" {
		t.Fatal(v1Str(w))
	}
}

func TestAPIv1FixReviewPolicy(t *testing.T) {
	if !apiIsPost("/api/v1/fix/review") || POST_ROUTES["/api/v1/fix/review"] != PolicySensitive {
		t.Fatal("policy")
	}
	if apiBodyLimit("/api/v1/fix/review") != postLimits["/admin/fix/review"] {
		t.Fatal("body limit")
	}
	e := iaSetup(t)
	nomfa, _ := e.loginNoMFA("admin")
	srvEnrol(t, nomfa)
	r := e.v1Post("/api/v1/fix/review", map[string]string{auth.COOKIE: nomfa}, map[string]any{"run": "1", "idx": "0"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != true {
		t.Fatal(r.status, r.body)
	}
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	r = e.v1Post("/api/v1/fix/review", map[string]string{auth.COOKIE: adm}, map[string]any{"run": "1", "idx": "0"}, nil)
	if r.status != 403 || !strings.Contains(r.body, "Disabled in the demo") {
		t.Fatal(r.status, r.body)
	}
}
