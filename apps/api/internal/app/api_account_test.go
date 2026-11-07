package app

// Account as JSON: the Go routes' gate and policy. A must-change session reaches only /account, an enrolment-pending one
// only /account/2fa; the seed and the recovery codes appear only in the action's answer; a stored token never does.

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

const accTestLogin = "Password-12345" // the test accounts' login (server_test.go)

func accCookie(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }

// v1Rotated is the session cookie after an answer that may rotate it.
func v1Rotated(t *testing.T, r srvResp, ck map[string]string) map[string]string {
	t.Helper()
	if c := cookieOf(r, auth.COOKIE); c != "" {
		return accCookie(cookieVal(c))
	}
	return ck
}

func TestAPIv1AccountPolicy(t *testing.T) {
	settingsTwinChecks(t, map[string]string{"/api/v1/account": "/account", "/api/v1/account/jira": "/account/jira", "/api/v1/account/matrix": "/account/matrix"}, map[string]any{"action": "x"}, true)
	for api := range map[string]string{"/api/v1/account/2fa/begin": "/account/2fa/begin", "/api/v1/account/2fa/confirm": "/account/2fa/confirm", "/api/v1/account/2fa/disable": "/account/2fa/disable",
		"/api/v1/account/2fa/recovery": "/account/2fa/recovery", "/api/v1/account/sessions": "/account/sessions"} {
		if !apiIsPost(api) || apiBodyLimit(api) != apiMaxBody {
			t.Fatal(api)
		}
	}
	if PolicyOf("/api/v1/account", &Session{MustChange: 1}) != PolicyExempt || PolicyOf("/api/v1/account", &Session{}) != PolicyNormal {
		t.Fatal("must change")
	}
}

func TestAPIv1AccountDemoRefusesEveryWrite(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for _, p := range []string{"begin", "confirm", "disable", "recovery"} {
		if r := e.v1Post("/api/v1/account/2fa/"+p, accCookie(adm), map[string]any{}, nil); r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(p, r.status, r.body)
		}
	}
	if r := e.v1Post("/api/v1/account/sessions", accCookie(adm), map[string]any{"action": "others"}, nil); r.status != 403 {
		t.Fatal(r.status)
	}
}

func TestAPIv1AccountRead(t *testing.T) {
	e := iaSetup(t)
	if r := e.req("GET", "/api/v1/account", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	vw, _ := e.login("view.er")
	r := e.req("GET", "/api/v1/account", accCookie(vw), nil, nil)
	d := v1JSON(t, r)
	if d["user"] != "view.er" || d["must_change"] != false || d["sso"] != nil || len(d["sessions"].([]any)) == 0 || d["mfa"].(map[string]any)["enabled"] != true || strings.Contains(r.body, "<span") {
		t.Fatal(r.body)
	}
	if j := d["jira"].(map[string]any); j["connected"] == nil || j["allowed"] == "" {
		t.Fatal(j)
	}
	m := v1JSON(t, e.req("GET", "/api/v1/account/2fa", accCookie(vw), nil, nil))
	if m["enabled"] != true || m["next"] != "/account" || m["recovery_total"] == nil || m["key"] != nil {
		t.Fatal(m)
	}
	if m := v1JSON(t, e.req("GET", "/api/v1/account/2fa?next=https://evil.example/", accCookie(vw), nil, nil)); strings.Contains(m["next"].(string), "evil") {
		t.Fatal(m)
	}
}

func TestAPIv1AccountGate(t *testing.T) {
	e := iaSetup(t)
	tok, _ := e.login("view.er")
	auth.Con().Exec("UPDATE users SET must_change=1 WHERE username='view.er'")
	ck := accCookie(tok)
	if d := v1JSON(t, e.req("GET", "/api/v1/account", ck, nil, nil)); d["must_change"] != true || d["sessions"] != nil || d["jira"] != nil {
		t.Fatal(d)
	}
	for _, p := range []string{"/api/v1/account/2fa", "/api/v1/nav", "/api/v1/today"} {
		if r := e.req("GET", p, ck, nil, nil); r.status != 403 {
			t.Fatal(p, r.status)
		}
	}
	if r := e.v1Post("/api/v1/account/sessions", ck, map[string]any{"action": "others"}, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	if r := e.v1Post("/api/v1/account", ck, map[string]any{"current": "Password-12345", "new": "Another-Password-1", "new2": "nope"}, nil); r.status != 400 || v1JSON(t, r)["field"] != "new2" {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/account", ck, map[string]any{"current": "wrong", "new": "Another-Password-1", "new2": "Another-Password-1"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	r := e.v1Post("/api/v1/account", ck, map[string]any{"current": "Password-12345", "new": "Another-Password-1", "new2": "Another-Password-1"}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["message"] != "Password changed." {
		t.Fatal(r.status, r.body)
	}
	if r := e.req("GET", "/api/v1/account/2fa", ck, nil, nil); r.status != 200 {
		t.Fatal(r.status)
	}
}

func TestAPIv1AccountEnrolAndCodes(t *testing.T) {
	e := iaSetup(t)
	tok, _ := e.loginNoMFA("view.er")
	ck := accCookie(tok)
	if r := e.v1Post("/api/v1/account/2fa/begin", ck, map[string]any{"password": "wrong"}, nil); r.status != 400 || strings.Contains(r.body, "enrol") {
		t.Fatal(r.status, r.body)
	}
	r := e.v1Post("/api/v1/account/2fa/begin", ck, map[string]any{"password": accTestLogin, "next": "/review"}, nil)
	a := v1JSON(t, r)
	en, ok := a["enrol"].(map[string]any)
	if r.status != 200 || !ok || en["key"] == "" || !strings.HasPrefix(en["uri"].(string), "otpauth://") || en["qr"].(map[string]any)["d"] == "" || a["next"] != "/review" {
		t.Fatal(r.status, r.body)
	}
	if !strings.Contains(r.header.Get("Cache-Control"), "no-store") {
		t.Fatal("cache", r.header.Get("Cache-Control"))
	}
	for _, sc := range r.cookies {
		if strings.Contains(sc, strings.ReplaceAll(en["key"].(string), " ", "")) {
			t.Fatal("seed in a cookie")
		}
	}
	ck = v1Rotated(t, r, ck)
	if r := e.v1Post("/api/v1/account/2fa/confirm", ck, map[string]any{"code": "000000"}, nil); r.status != 400 || !strings.Contains(r.header.Get("Cache-Control"), "no-store") {
		t.Fatal(r.status, r.body)
	}
	secret, err := auth.PendingSecret(auth.GetSession(ck[auth.COOKIE]))
	if err != nil {
		t.Fatal(err)
	}
	code := auth.TOTPAt(secret, auth.Step(NowInt()))
	r = e.v1Post("/api/v1/account/2fa/confirm", ck, map[string]any{"code": code, "next": "/review"}, nil)
	c := v1JSON(t, r)
	if r.status != 200 || len(c["codes"].([]any)) != auth.RECOVERY_CODES || c["next"] != "/review" || !strings.Contains(r.header.Get("Cache-Control"), "no-store") {
		t.Fatal(r.status, r.body)
	}
	ck = v1Rotated(t, r, ck)
	if s := auth.GetSession(ck[auth.COOKIE]); s == nil || s.MFAAt == 0 || !auth.Enabled("view.er") {
		t.Fatal("not on")
	}
	if b := e.req("GET", "/api/v1/account/2fa", ck, nil, nil).body; strings.Contains(b, c["codes"].([]any)[0].(string)) {
		t.Fatal("a code came back")
	}
}

func TestAPIv1AccountSessions(t *testing.T) {
	e := iaSetup(t)
	a, _ := e.login("view.er")
	b, _ := e.loginNoMFA("view.er") // password only
	if r := e.v1Post("/api/v1/account/sessions", accCookie(b), map[string]any{"action": "others"}, nil); r.status != 403 || v1JSON(t, r)["mfa"] != "stepup" {
		t.Fatal(r.status, r.body)
	}
	if auth.GetSession(a) == nil {
		t.Fatal("a password-only session ended a verified one")
	}
	if r := e.v1Post("/api/v1/account/sessions", accCookie(a), map[string]any{"action": "nope"}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
	if r := e.v1Post("/api/v1/account/sessions", accCookie(a), map[string]any{"action": "others"}, nil); r.status != 200 || auth.GetSession(b) != nil {
		t.Fatal(r.status, r.body)
	}
	id := auth.GetSession(a).ID()
	r := e.v1Post("/api/v1/account/sessions", accCookie(a), map[string]any{"action": "revoke", "id": id}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["redirect"] != "/login" || auth.GetSession(a) != nil {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1AccountJiraAndMatrix(t *testing.T) {
	e := iaSetup(t)
	tok, _ := e.login("view.er")
	ck := accCookie(tok)
	for _, b := range []map[string]any{{"action": "connect", "url": "ftp://x", "token": v1FakeCred}, {"action": "connect", "url": "https://j.example", "token": "no"}, {"action": "connect", "url": "https://j.example", "token": v1FakeCred + "1234567890", "mode": "x"}} {
		r := e.v1Post("/api/v1/account/jira", ck, b, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] == nil || m["form"] != "jira-connect" || strings.Contains(r.body, v1FakeCred) {
			t.Fatal(r.status, r.body)
		}
	}
	if r := e.v1Post("/api/v1/account/matrix", ck, map[string]any{"matrix_id": "not an id"}, nil); r.status != 400 || v1JSON(t, r)["field"] != "matrix_id" {
		t.Fatal(r.status, r.body)
	}
	r := e.v1Post("/api/v1/account/matrix", ck, map[string]any{"matrix_id": "@me:northwind.example"}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["redirect"] != "/account?matrix=saved#matrix" {
		t.Fatal(r.status, r.body)
	}
}
