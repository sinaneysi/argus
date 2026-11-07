package app

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/demo"
)

// catDemoDB puts the demo's ops.db (synthetic Northwind catalogue) where the app reads it.
func catDemoDB(t *testing.T) {
	t.Helper()
	gen := t.TempDir()
	if err := demo.Generate(filepath.Join(gen, "data"), filepath.Join(gen, "auth"), 1793318400, demo.DefaultSeed); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(filepath.Join(gen, "data", "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, _ := os.Create(DB)
	io.Copy(out, in)
	out.Close()
}

func catRequests(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(SET_IN, "source-actions", "*.json"))
	return m
}

// apiReq is one API call: (status, decoded JSON object).
func apiReq(t *testing.T, e *srvEnv, method, path string, ck map[string]string, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	for k, v := range ck {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("%s %s: content type %q (%d)", method, path, ct, resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("%s %s: not JSON: %v", method, path, err)
	}
	return resp.StatusCode, m
}

func TestCatalogueAPIRead(t *testing.T) {
	e, ck, csrf := srcSetup(t)
	// anonymous: JSON 401, never a redirect
	for _, p := range []string{catAPI, catAPI + "/routes", catAPI + "/unmapped", catAPI + "/runs", "/api/v1/session"} {
		if code, m := apiReq(t, e, "GET", p, nil, "", nil); code != 401 || m["error"] != "login required" {
			t.Fatalf("anonymous %s: %d %v", p, code, m)
		}
	}
	if code, m := apiReq(t, e, "GET", "/api/v1/nope", ck, "", nil); code != 404 || m["error"] != "no such endpoint" {
		t.Fatalf("404: %d %v", code, m)
	}
	if code, _ := apiReq(t, e, "GET", catAPI+"/discover", ck, "", nil); code != 405 {
		t.Fatalf("GET on a POST endpoint: %d", code)
	}
	if code, m := apiReq(t, e, "GET", "/api/v1/session", ck, "", nil); code != 200 || m["user"] != "admin" || m["role"] != "admin" || m["csrf"] != csrf {
		t.Fatalf("session: %d %v", code, m)
	}
	// empty: no table yet
	if code, m := apiReq(t, e, "GET", catAPI, ck, "", nil); code != 200 || m["verdict"].(map[string]any)["kind"] != "none" {
		t.Fatalf("empty summary: %d %v", code, m)
	}
	catDemoDB(t)
	code, m := apiReq(t, e, "GET", catAPI, ck, "", nil)
	v := m["verdict"].(map[string]any)
	c := m["counts"].(map[string]any)
	if code != 200 || v["kind"] != "warn" || !strings.Contains(v["head"].(string), "elasticsearch/logs") || c["stale"] != float64(1) ||
		c["rejected"] != float64(1) || c["proposed"].(float64) < 10 || c["unmapped"].(float64) < 5 || len(m["sources"].([]any)) != 4 {
		t.Fatalf("summary: %d %v", code, m)
	}
	// routes: filters, facets, paging
	code, m = apiReq(t, e, "GET", catAPI+"/routes?q=cart&type=kubernetes", ck, "", nil)
	rs := m["routes"].([]any)
	if code != 200 || len(rs) != 2 || rs[0].(map[string]any)["service"] != "shop/cart" || rs[0].(map[string]any)["status"] != "confirmed" {
		t.Fatalf("routes filter: %d %v", code, m)
	}
	if f := m["facets"].(map[string]any)["type"].(map[string]any); f["grafana"] != float64(1) || f["kubernetes"] != float64(2) {
		t.Fatalf("facets: %v", f)
	}
	code, m = apiReq(t, e, "GET", catAPI+"/routes?status=stale", ck, "", nil)
	if code != 200 || len(m["routes"].([]any)) != 1 || m["routes"].([]any)[0].(map[string]any)["service"] != "shop/reviews" {
		t.Fatalf("stale: %v", m)
	}
	code, m = apiReq(t, e, "GET", catAPI+"/routes?per=5&page=2", ck, "", nil)
	pg := m["page"].(map[string]any)
	if code != 200 || len(m["routes"].([]any)) != 5 || pg["page"] != float64(2) || pg["per"] != float64(5) || pg["total"].(float64) < 40 {
		t.Fatalf("paging: %v", pg)
	}
	if code, m = apiReq(t, e, "GET", catAPI+"/routes?page=999&per=200", ck, "", nil); m["page"].(map[string]any)["page"] != float64(1) {
		t.Fatalf("page clamp: %v", m["page"])
	}
	for _, q := range []string{"?type=bogus", "?status=x", "?service=..%2Fx%3B"} {
		if code, m := apiReq(t, e, "GET", catAPI+"/routes"+q, ck, "", nil); code != 400 || m["error"] == "" {
			t.Fatalf("bad %s: %d", q, code)
		}
	}
	code, m = apiReq(t, e, "GET", catAPI+"/unmapped?type=grafana", ck, "", nil)
	um := m["unmapped"].([]any)
	if code != 200 || len(um) != 6 || !strings.Contains(string(mustJSON(um)), "ambiguous: dashboard title fits shop/checkout, shop/pricing") {
		t.Fatalf("unmapped: %d %v", code, um)
	}
	code, m = apiReq(t, e, "GET", catAPI+"/runs?source=gitlab/main", ck, "", nil)
	runs := m["runs"].([]any)
	if code != 200 || len(runs) != 2 || runs[0].(map[string]any)["mode"] != "incremental" {
		t.Fatalf("runs: %v", runs)
	}
	if code, _ := apiReq(t, e, "GET", catAPI+"/runs?source=..", ck, "", nil); code != 400 {
		t.Fatal("bad source accepted")
	}
	// a viewer reads too
	vtok, _ := e.login("view.er")
	if code, _ := apiReq(t, e, "GET", catAPI+"/routes", map[string]string{"opsr_session": vtok}, "", nil); code != 200 {
		t.Fatalf("viewer GET: %d", code)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestCatalogueAPIWrite(t *testing.T) {
	e, ck, csrf := srcSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "source-actions"), 0o755)
	catDemoDB(t)
	h := map[string]string{"X-CSRF-Token": csrf}
	code, m := apiReq(t, e, "POST", catAPI+"/routes/confirm", ck, `{"id": 12}`, h)
	if code != 202 || m["state"] != "queued" || len(catRequests(t)) != 1 {
		t.Fatalf("confirm: %d %v", code, m)
	}
	rid := m["rid"].(string)
	b, _ := os.ReadFile(catRequests(t)[0])
	var req map[string]any
	json.Unmarshal(b, &req)
	if req["action"] != "confirm" || req["id"] != float64(12) || req["user"] != "admin" || req["role"] != "admin" {
		t.Fatalf("request: %v", req)
	}
	if _, m = apiReq(t, e, "GET", catAPI+"/action?rid="+rid, ck, "", nil); m["state"] != "queued" {
		t.Fatalf("queued: %v", m)
	}
	os.Remove(catRequests(t)[0])
	if _, m = apiReq(t, e, "GET", catAPI+"/action?rid="+rid, ck, "", nil); m["state"] != "working" {
		t.Fatalf("working: %v", m)
	}
	db, _ := sql.Open("sqlite", DB)
	db.Exec("INSERT INTO source_action_results VALUES (?, 'admin', 'confirm', 'refused: no such route', 1)", rid)
	db.Close()
	if _, m = apiReq(t, e, "GET", catAPI+"/action?rid="+rid, ck, "", nil); m["state"] != "refused" || m["result"] != "no such route" {
		t.Fatalf("refused: %v", m)
	}
	if code, _ = apiReq(t, e, "GET", catAPI+"/action?rid=xyz", ck, "", nil); code != 400 {
		t.Fatal("bad rid")
	}
	// form bodies with the token in the body work too
	for _, c := range []struct{ path, body string }{
		{"/routes/edit", `{"id": 3, "locator": "{app=\"cart\"}", "note": "checked by hand"}`},
		{"/discover", `{"source": "grafana/east"}`},
		{"/routes/reject", `{"id": "4"}`}} {
		if code, m = apiReq(t, e, "POST", catAPI+c.path, ck, c.body, h); code != 202 {
			t.Fatalf("%s: %d %v", c.path, code, m)
		}
	}
	req2, _ := http.NewRequest("POST", e.ts.URL+catAPI+"/routes/confirm", strings.NewReader(url.Values{"csrf": {csrf}, "id": {"5"}}.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(&http.Cookie{Name: "opsr_session", Value: ck["opsr_session"]})
	if r, err := http.DefaultClient.Do(req2); err != nil || r.StatusCode != 202 {
		t.Fatalf("form body: %v %v", r, err)
	}
	if n := len(catRequests(t)); n != 4 {
		t.Fatalf("requests %d", n)
	}
	// validation, CSRF, origin, method: nothing written
	for _, c := range []struct {
		path, body string
		hdr        map[string]string
		code       int
		err        string
	}{
		{"/routes/confirm", `{"id": "x"}`, h, 400, "id is a route id"},
		{"/routes/confirm", `{"id": 1.5}`, h, 400, "id is a route id"},
		{"/routes/edit", `{"id": 3, "locator": "a|b"}`, h, 400, "locator must be"},
		{"/routes/edit", `{"id": 3, "locator": ""}`, h, 400, "locator must be"},
		{"/discover", `{"source": "../x"}`, h, 400, "source is type/name"},
		{"/discover", `[1]`, h, 400, "not a JSON object"},
		{"/routes/confirm", `{"id": 1}`, nil, 403, "csrf token missing or wrong"},
		{"/routes/confirm", `{"id": 1}`, map[string]string{"X-CSRF-Token": "nope"}, 403, "csrf token missing or wrong"},
		{"/routes/confirm", `{"id": 1}`, map[string]string{"X-CSRF-Token": csrf, "Origin": "https://evil.example"}, 403, "cross-site"},
		{"", `{}`, h, 405, "use GET"},
		{"/routes/drop", `{"id": 1}`, h, 404, "no such endpoint"},
	} {
		code, m := apiReq(t, e, "POST", catAPI+c.path, ck, c.body, c.hdr)
		if code != c.code || !strings.Contains(m["error"].(string), c.err) {
			t.Fatalf("%s %s: %d %v", c.path, c.body, code, m)
		}
	}
	if code, m = apiReq(t, e, "POST", catAPI+"/routes/confirm", ck, strings.Repeat(" ", 9000)+"{}", h); code != 413 {
		t.Fatalf("too large: %d", code)
	}
	if code, _ = apiReq(t, e, "POST", catAPI+"/routes/confirm", nil, `{"id":1}`, nil); code != 401 {
		t.Fatalf("anonymous POST: %d", code)
	}
	// a viewer may not write
	vtok, vcsrf := e.login("view.er")
	vck := map[string]string{"opsr_session": vtok}
	if code, m = apiReq(t, e, "POST", catAPI+"/discover", vck, `{"source": "grafana/east"}`, map[string]string{"X-CSRF-Token": vcsrf}); code != 403 || m["error"] != "admins only" {
		t.Fatalf("viewer POST: %d %v", code, m)
	}
	// an admin without recent 2FA: step-up required (sensitive policy)
	ntok, ncsrf := e.loginNoMFA("admin")
	old := auth.MFAActions
	auth.MFAActions = true
	defer func() { auth.MFAActions = old }()
	if code, m = apiReq(t, e, "POST", catAPI+"/discover", map[string]string{"opsr_session": ntok}, `{"source": "grafana/east"}`,
		map[string]string{"X-CSRF-Token": ncsrf}); code != 403 || m["error"] != "two-factor authentication required" || m["mfa"] == "" {
		t.Fatalf("no 2FA: %d %v", code, m)
	}
	if n := len(catRequests(t)); n != 4 {
		t.Fatalf("a refused POST wrote a request: %d", n)
	}
	// the demo refuses every write; reads still work
	oldDemo := DEMO
	DEMO = true
	defer func() { DEMO = oldDemo }()
	if code, m = apiReq(t, e, "POST", catAPI+"/discover", ck, `{"source": "grafana/east"}`, h); code != 403 || m["error"] != "Disabled in the demo" || len(catRequests(t)) != 4 {
		t.Fatalf("demo POST: %d %v", code, m)
	}
	if code, m = apiReq(t, e, "GET", "/api/v1/session", ck, "", nil); code != 200 || m["demo"] != true {
		t.Fatalf("demo session: %v", m)
	}
}
