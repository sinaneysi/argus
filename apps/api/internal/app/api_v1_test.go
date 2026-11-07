package app

// The JSON API of the Next.js frontend (api.go, api_today.go, api_review.go) on the demo data set: sign-in with the
// double-submit CSRF cookie and the 2FA code step, /me with role and permissions, the sidebar, the data status,
// Today, the Review queue, panel and plan step, acting through the kinds' handlers with their roles and 2FA classes,
// and the demo refusal.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

const v1CSRF = "c0ffee-c0ffee-c0ffee-c0ffee"

func v1JSON(t *testing.T, r srvResp) map[string]any {
	t.Helper()
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type %q (%d %s)", ct, r.status, r.body)
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("not JSON: %v %s", err, r.body)
	}
	return m
}

// v1Post posts a JSON body with the double-submit cookie and header.
func (e *srvEnv) v1Post(path string, cookies map[string]string, body map[string]any, hdr map[string]string) srvResp {
	e.t.Helper()
	b, _ := json.Marshal(body)
	ck := map[string]string{CSRF_COOKIE: v1CSRF}
	for k, v := range cookies {
		ck[k] = v
	}
	h := map[string]string{"Content-Type": "application/json", "X-CSRF-Token": v1CSRF}
	for k, v := range hdr {
		h[k] = v
	}
	return e.reqBody("POST", path, ck, string(b), h)
}

// v1Form posts a form with the double-submit cookie and field: what a signed-out write sends.
func (e *srvEnv) v1Form(path string, cookies map[string]string, form url.Values, hdr map[string]string) srvResp {
	e.t.Helper()
	ck := map[string]string{CSRF_COOKIE: v1CSRF}
	for k, v := range cookies {
		ck[k] = v
	}
	form.Set("csrf", v1CSRF)
	return e.req("POST", path, ck, form, hdr)
}

// reqBody sends a raw body (the JSON API).
func (e *srvEnv) reqBody(method, path string, cookies map[string]string, body string, hdr map[string]string) srvResp {
	e.t.Helper()
	r, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	var parts []string
	for k, v := range cookies {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	if len(parts) > 0 {
		r.Header.Set("Cookie", strings.Join(parts, "; "))
	}
	for k, v := range hdr {
		if v != "" {
			r.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return srvResp{resp.StatusCode, srvLoc(resp, path, b), resp.Header.Values("Set-Cookie"), string(b), resp.Header}
}

func cookieOf(r srvResp, name string) string {
	for _, c := range r.cookies {
		if strings.HasPrefix(c, name+"=") {
			return c
		}
	}
	return ""
}

func cookieVal(set string) string {
	v, _, _ := strings.Cut(set[strings.Index(set, "=")+1:], ";")
	return v
}

func TestAPIv1SignIn(t *testing.T) {
	e := srvSetup(t)
	// the double-submit cookie: set once, readable, Secure, SameSite=Strict
	r := e.req("GET", "/api/v1/csrf", nil, nil, nil)
	set := cookieOf(r, CSRF_COOKIE)
	if r.status != 200 || set == "" || strings.Contains(set, "HttpOnly") || !strings.Contains(set, "Secure") ||
		!strings.Contains(set, "SameSite=Strict") || v1JSON(t, r)["csrf"] != cookieVal(set) {
		t.Fatal("csrf cookie", r.status, set)
	}
	if r := e.req("GET", "/api/v1/csrf", map[string]string{CSRF_COOKIE: v1CSRF}, nil, nil); cookieOf(r, CSRF_COOKIE) != "" ||
		v1JSON(t, r)["csrf"] != v1CSRF {
		t.Fatal("csrf cookie kept")
	}
	creds := map[string]any{"username": "view.er", "password": "Password-12345", "next": "/review"}
	// CSRF: missing header, wrong header, header without the cookie
	for _, c := range []struct{ ck, hd map[string]string }{
		{map[string]string{CSRF_COOKIE: v1CSRF}, map[string]string{"X-CSRF-Token": ""}},
		{map[string]string{CSRF_COOKIE: v1CSRF}, map[string]string{"X-CSRF-Token": "x" + v1CSRF}},
		{map[string]string{CSRF_COOKIE: ""}, nil},
	} {
		b, _ := json.Marshal(creds)
		h := map[string]string{"Content-Type": "application/json", "X-CSRF-Token": v1CSRF}
		for k, v := range c.hd {
			h[k] = v
		}
		if r := e.reqBody("POST", "/api/v1/auth/login", c.ck, string(b), h); r.status != 403 || v1JSON(t, r)["error"] != "csrf token missing or wrong" {
			t.Fatal("login csrf", r.status, r.body)
		}
	}
	if r := e.v1Post("/api/v1/auth/login", nil, map[string]any{"username": "view.er", "password": "nope"}, nil); r.status != 401 ||
		cookieOf(r, auth.COOKIE) != "" {
		t.Fatal("wrong password", r.status)
	}
	if r := e.v1Post("/api/v1/auth/login", nil, creds, map[string]string{"Origin": "https://evil.example"}); r.status != 403 {
		t.Fatal("cross-site login", r.status)
	}
	r = e.v1Post("/api/v1/auth/login", nil, creds, nil)
	m := v1JSON(t, r)
	sc := cookieOf(r, auth.COOKIE)
	if r.status != 200 || m["state"] != "ok" || m["next"] != "/review" || !strings.Contains(sc, "HttpOnly") ||
		!strings.Contains(sc, "Secure") || !strings.Contains(sc, "SameSite=Lax") {
		t.Fatal("login", r.status, r.body, sc)
	}
	tok := cookieVal(sc)
	me := v1JSON(t, e.req("GET", "/api/v1/me", map[string]string{auth.COOKIE: tok}, nil, nil))
	if me["username"] != "view.er" || me["role"] != "viewer" || me["permissions"].(map[string]any)["review.decide"] != false {
		t.Fatal("me", me)
	}
	// sign-out ends the session
	r = e.v1Post("/api/v1/auth/logout", map[string]string{auth.COOKIE: tok}, nil, nil)
	if r.status != 200 || !strings.Contains(cookieOf(r, auth.COOKIE), "Max-Age=0") || auth.GetSession(tok) != nil {
		t.Fatal("logout", r.status)
	}
	if r := e.req("GET", "/api/v1/me", map[string]string{auth.COOKIE: tok}, nil, nil); r.status != 401 || v1JSON(t, r)["error"] != "login required" {
		t.Fatal("me after logout", r.status)
	}
}

func TestAPIv1SignInWithCode(t *testing.T) {
	e := srvSetup(t)
	tok, _ := e.loginNoMFA("admin")
	sec := srvEnrol(t, tok)
	auth.Logout(tok)
	r := e.v1Post("/api/v1/auth/login", nil, map[string]any{"username": "admin", "password": "Password-12345"}, nil)
	pend := cookieOf(r, MFA_COOKIE)
	if m := v1JSON(t, r); r.status != 200 || m["state"] != "mfa" || pend == "" || cookieOf(r, auth.COOKIE) != "" {
		t.Fatal("password step", r.status, r.body)
	}
	ck := map[string]string{MFA_COOKIE: cookieVal(pend)}
	if m := v1JSON(t, e.req("GET", "/api/v1/auth/mfa", ck, nil, nil)); m["pending"] != true || m["username"] != "admin" {
		t.Fatal("pending", m)
	}
	if r := e.v1Post("/api/v1/auth/mfa", ck, map[string]any{"code": "000000"}, nil); r.status != 401 || v1JSON(t, r)["state"] != "mfa" {
		t.Fatal("wrong code", r.status)
	}
	code := auth.TOTPAt(sec, auth.Step(NowInt())+1) // the current step was used by the enrolment
	r = e.v1Post("/api/v1/auth/mfa", ck, map[string]any{"code": code}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["state"] != "ok" || m["next"] != "/today" || cookieOf(r, auth.COOKIE) == "" ||
		!strings.Contains(cookieOf(r, MFA_COOKIE), "Max-Age=0") {
		t.Fatal("code step", r.status, r.body)
	}
	s := auth.GetSession(cookieVal(cookieOf(r, auth.COOKIE)))
	if s == nil || s.MFAAt == 0 || s.MFAVia != "totp" {
		t.Fatal("session after the code", s)
	}
	// the pending login is gone
	if r := e.v1Post("/api/v1/auth/mfa", ck, map[string]any{"code": code}, nil); r.status != 401 || v1JSON(t, r)["state"] != "expired" {
		t.Fatal("reuse", r.status)
	}
}

func TestAPIv1ReadsAndShapes(t *testing.T) {
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
	for _, u := range []string{"/api/v1/nav", "/api/v1/status", "/api/v1/today", "/api/v1/review", "/api/v1/review/item?sel=x:1"} {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal("no session", u, r.status)
		}
	}
	if r := e.req("GET", "/api/v1/nope", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
		t.Fatal("404", r.status)
	}
	if r := e.req("GET", "/api/v1/review/act", map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 405 {
		t.Fatal("405", r.status)
	}
	me := get(adm, "/api/v1/me")
	if me["role"] != "admin" || me["permissions"].(map[string]any)["review.decide"] != true {
		t.Fatal("me admin", me)
	}
	// the sidebar: six groups and Marketing; counts only for admins
	nav := get(adm, "/api/v1/nav")
	var names []string
	counted := false
	for _, g := range nav["groups"].([]any) {
		gm := g.(map[string]any)
		names = append(names, gm["name"].(string))
		for _, l := range gm["links"].([]any) {
			if l.(map[string]any)["count"] != nil {
				counted = true
			}
		}
	}
	if strings.Join(names, ",") != "Home,Incidents,Infrastructure,Delivery,Work,Agent,Marketing" || !counted {
		t.Fatal("nav", names, counted)
	}
	if strings.Contains(e.req("GET", "/api/v1/nav", map[string]string{auth.COOKIE: vw}, nil, nil).body, `"count"`) {
		t.Fatal("viewer counts")
	}
	if st := get(adm, "/api/v1/status"); st["dataAsOf"].(float64) <= 0 || st["state"] == "" {
		t.Fatal("status", st)
	}
	td := get(adm, "/api/v1/today")
	if td["on"] != true || td["plan"] == nil || len(td["open"].([]any)) == 0 || td["review"].(map[string]any)["total"].(float64) <= 0 {
		t.Fatal("today", td)
	}
	it := td["open"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "title", "href", "doThis", "whyNow", "estMin", "details"} {
		if _, ok := it[k]; !ok {
			t.Fatal("today item field", k)
		}
	}
	// Review: the same order and filters as /review
	rv := get(adm, "/api/v1/review")
	items := rv["items"].([]any)
	if len(items) != reviewPageSize || items[0].(map[string]any)["sel"] != "proposals:16" || !strings.HasPrefix(rv["order"].(string), "Sorted by urgency") {
		t.Fatal("review queue", len(items), items[0])
	}
	if rv := get(adm, "/api/v1/review?env=release&kind=approvals"); !strings.Contains(v1Str(rv["items"]), `"approvals:OPS-193"`) ||
		strings.Contains(v1Str(rv["items"]), `"approvals:OPS-238"`) {
		t.Fatal("env filter")
	}
	rv = get(adm, "/api/v1/review?sel=proposals:16")
	sel := rv["selected"].(map[string]any)
	if sel["row"].(map[string]any)["sel"] != "proposals:16" || len(sel["facts"].([]any)) == 0 || len(sel["actions"].([]any)) == 0 {
		t.Fatal("selected", sel)
	}
	if rv := get(adm, "/api/v1/review?sel=proposals:99999"); rv["selGone"] != true || rv["selected"] != nil {
		t.Fatal("gone")
	}
	step := get(adm, "/api/v1/review/item?sel=approvals:OPS-238&step=plan")
	if step["plan"].(map[string]any)["version"].(float64) != 2 || !strings.Contains(v1Str(step["actions"]), `"action":"approve"`) {
		t.Fatal("plan step", step)
	}
	// viewers read the kinds they may see, without actions
	vr := get(vw, "/api/v1/review")
	if strings.Contains(v1Str(vr["kinds"]), `"proposals"`) || vr["admin"] != false {
		t.Fatal("viewer kinds")
	}
	if r := e.req("GET", "/api/v1/review/item?sel=proposals:16", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 404 {
		t.Fatal("viewer proposal", r.status)
	}
}

func v1Str(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAPIv1Acting(t *testing.T) {
	e := iaSetup(t)
	os.MkdirAll(filepath.Join(SET_IN, "today-actions"), 0o755)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	nomfa, _ := e.loginNoMFA("admin")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	// the session's own token is accepted as well as the double submit
	r := e.v1Post("/api/v1/review/act", ck(adm), map[string]any{"sel": "proposals:16", "action": "nope"}, nil)
	if r.status != 400 {
		t.Fatal("bad action", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/review/act", ck(adm), map[string]any{"sel": "proposals:16", "action": "snooze"},
		map[string]string{"X-CSRF-Token": "wrong-wrong-wrong-wrong"}); r.status != 403 {
		t.Fatal("csrf", r.status)
	}
	if r := e.v1Post("/api/v1/review/act", ck(vw), map[string]any{"sel": "ideas:1", "action": "accept"}, nil); r.status != 403 ||
		v1JSON(t, r)["error"] != "admins only" {
		t.Fatal("viewer", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/review/act", ck(nomfa), map[string]any{"sel": "proposals:16", "action": "snooze"}, nil); r.status != 403 ||
		v1JSON(t, r)["mfa"] != "stepup" { // enrolled (by the other session), not verified on this one
		t.Fatal("2fa", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/review/act", ck(adm), map[string]any{"sel": "proposals:99999", "action": "snooze"}, nil); r.status != 404 {
		t.Fatal("gone", r.status)
	}
	if r := e.v1Post("/api/v1/review/act", ck(adm), map[string]any{"sel": "approvals:OPS-238", "action": "approve", "plan_version": "1"}, nil); r.status != 409 {
		t.Fatal("stale plan version", r.status, r.body)
	}
	r = e.v1Post("/api/v1/review/act", ck(adm), map[string]any{"sel": "proposals:16", "action": "snooze", "return": "/review?kind=proposals"}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["ok"] != true || !strings.HasPrefix(m["redirect"].(string), "/review") {
		t.Fatal("snooze", r.status, r.body)
	}
	// Today: the existing request-file handler
	td := v1JSON(t, e.req("GET", "/api/v1/today", ck(adm), nil, nil))
	id := td["open"].([]any)[0].(map[string]any)["id"].(float64)
	r = e.v1Post("/api/v1/today/act", ck(adm), map[string]any{"action": "done", "item": id}, nil)
	if m := v1JSON(t, r); r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/today?sent=done") {
		t.Fatal("today done", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/today/act", ck(adm), map[string]any{"action": "defer", "item": id, "days": "99"}, nil); r.status != 400 ||
		v1JSON(t, r)["error"] != "Defer for 1 to 30 days." {
		t.Fatal("today rule", r.status, r.body)
	}
	if r := e.v1Post("/api/v1/today/act", ck(vw), map[string]any{"action": "done", "item": id}, nil); r.status != 403 {
		t.Fatal("today viewer", r.status)
	}
}

func TestAPIv1DemoRefusesWrites(t *testing.T) {
	e := iaSetup(t)
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	adm, _ := e.login("admin")
	for _, p := range []string{"/api/v1/review/act", "/api/v1/today/act"} {
		r := e.v1Post(p, map[string]string{auth.COOKIE: adm}, map[string]any{"sel": "proposals:16", "action": "snooze"}, nil)
		if r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal("demo", p, r.status, r.body)
		}
	}
	// sign-in, sign-out and the theme still work
	if r := e.v1Post("/api/v1/auth/login", nil, map[string]any{"username": "view.er", "password": "Password-12345"}, nil); r.status != 200 {
		t.Fatal("demo login", r.status)
	}
	if r := e.v1Post("/api/v1/theme", nil, map[string]any{"theme": "dark"}, nil); r.status != 200 || !strings.Contains(cookieOf(r, THEME_COOKIE), "=dark") {
		t.Fatal("demo theme", r.status)
	}
}

func TestAPIv1PolicyTable(t *testing.T) {
	for p := range apiAuthPOSTs {
		if POST_ROUTES[p] != PolicyExempt {
			t.Errorf("%s is not exempt", p)
		}
	}
	for _, k := range REVIEW_KINDS {
		if _, ok := POST_ROUTES[apiReviewPolicyRoute(k.Key)]; !ok {
			t.Errorf("kind %s route %q has no policy", k.Key, apiReviewPolicyRoute(k.Key))
		}
	}
	for _, p := range []string{"/api/v1/today/act", "/api/v1/review/act"} {
		if !apiIsPost(p) || POST_ROUTES[p] != PolicyNormal {
			t.Errorf("%s policy", p)
		}
	}
}

// The sidebar: six groups and Marketing with exactly their links, in order, for an admin; a viewer has the same groups without the
// admin-only links; the footer has Settings for admins only.
func TestAPIv1NavGroupsAndRoles(t *testing.T) {
	e := iaSetup(t)
	nav := func(user string) (map[string][]string, []string, []string) {
		tok, _ := e.login(user)
		m := v1JSON(t, e.req("GET", "/api/v1/nav", map[string]string{auth.COOKIE: tok}, nil, nil))
		out, order, foot := map[string][]string{}, []string{}, []string{}
		for _, g := range m["groups"].([]any) {
			gm := g.(map[string]any)
			order = append(order, gm["name"].(string))
			for _, l := range gm["links"].([]any) {
				out[gm["name"].(string)] = append(out[gm["name"].(string)], l.(map[string]any)["label"].(string))
			}
		}
		for _, l := range m["footer"].([]any) {
			foot = append(foot, l.(map[string]any)["label"].(string))
		}
		return out, order, foot
	}
	got, order, foot := nav("admin")
	if len(order) != len(iaWantGroup) || strings.Join(foot, ",") != "Help,Settings" {
		t.Fatal(order, foot)
	}
	for i, w := range iaWantGroup {
		if order[i] != w.name || strings.Join(got[w.name], ",") != strings.Join(w.links, ",") {
			t.Fatalf("%s: %v, want %v", w.name, got[w.name], w.links)
		}
	}
	vg, vorder, vfoot := nav("view.er")
	if len(vorder) != len(iaWantGroup) || strings.Join(vfoot, ",") != "Help" {
		t.Fatal(vorder, vfoot)
	}
	for _, l := range vg["Agent"] {
		if l == "Fix runs" {
			t.Fatal("a viewer sees Fix runs")
		}
	}
}

// The session gate of the JSON API: no session or a forged cookie is 401, a viewer is refused the admin reads, a forced
// password change blocks everything but the account, and the download routes keep their own 401.
func TestAPIv1SessionGate(t *testing.T) {
	e := iaSetup(t)
	if r := e.req("GET", "/api/v1/runs", nil, nil, nil); r.status != 401 || !strings.Contains(r.body, "login required") {
		t.Fatal("anonymous", r.status)
	}
	if r := e.req("GET", "/cicd/summary.json", nil, nil, nil); r.status != 401 || !strings.Contains(r.body, "login required") {
		t.Fatal("download", r.status)
	}
	vt, _ := e.login("view.er")
	ck := map[string]string{auth.COOKIE: vt}
	if r := e.req("GET", "/api/v1/runs", map[string]string{auth.COOKIE: "x" + vt}, nil, nil); r.status != 401 {
		t.Fatal("forged session", r.status)
	}
	for _, u := range []string{"/api/v1/admin", "/api/v1/admin/users"} {
		if r := e.req("GET", u, ck, nil, nil); r.status != 403 {
			t.Fatal("viewer", u, r.status)
		}
	}
	auth.Con().Exec("UPDATE users SET must_change=1 WHERE username='view.er'")
	if r := e.req("GET", "/api/v1/runs", ck, nil, nil); r.status != 403 || !strings.Contains(r.body, "password change required") {
		t.Fatal("must_change", r.status, r.body)
	}
	if r := e.req("GET", "/api/v1/account", ck, nil, nil); r.status != 200 {
		t.Fatal("must_change account", r.status)
	}
}
