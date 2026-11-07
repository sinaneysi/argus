package app

// Settings → General and Users and access as JSON: admins only, the same policy as the Go routes, CSRF, a refused save
// names its field, a temporary password only in the answer to its action, no stored secret anywhere.

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1AdminPolicy(t *testing.T) {
	for api := range map[string]string{"/api/v1/admin/save": "/admin", "/api/v1/admin/claude": "/admin/claude", "/api/v1/admin/users/act": "/admin/users", "/api/v1/admin/users/matrix": "/admin/users/matrix"} {
		if !apiIsPost(api) || POST_ROUTES[api] != PolicySensitive {
			t.Fatal(api)
		}
		if apiBodyLimit(api) != 8192 {
			t.Fatal(api, "body limit")
		}
	}
}

func TestAPIv1AdminGeneral(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	for _, p := range []string{"/api/v1/admin", "/api/v1/admin/users"} {
		if r := e.req("GET", p, nil, nil, nil); r.status != 401 {
			t.Fatal(p, r.status)
		}
		if r := e.req("GET", p, ck(vw), nil, nil); r.status != 403 {
			t.Fatal(p, r.status)
		}
	}
	d := v1JSON(t, e.req("GET", "/api/v1/admin", ck(adm), nil, nil))
	if len(d["schedules"].([]any)) != len(ADMIN_TASKS) || len(d["thresholds"].([]any)) != len(LIMITS) || len(d["rcaAuto"].([]any)) != len(RCA_AUTO) || d["sso"] == nil {
		t.Fatal(v1Str(d)[:300])
	}
	if strings.Contains(v1Str(d), "<span") {
		t.Fatal("html in the answer")
	}
	for _, p := range []string{"/api/v1/admin/save", "/api/v1/admin/claude", "/api/v1/admin/users/act", "/api/v1/admin/users/matrix"} {
		if r := e.req("POST", p, ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
			t.Fatal("no csrf", p, r.status)
		}
		if r := e.v1Post(p, ck(vw), map[string]any{"user": "view.er", "action": "add"}, nil); r.status != 403 {
			t.Fatal("viewer", p, r.status, r.body)
		}
	}
	// a refused save names the field and writes nothing
	before, _ := os.ReadFile(settingsPath())
	r := e.v1Post("/api/v1/admin/save", ck(adm), map[string]any{"sched:" + ADMIN_TASKS[0][0]: "never"}, nil)
	after, _ := os.ReadFile(settingsPath())
	if r.status != 400 || !strings.Contains(r.body, `"field":"sched:`) || string(before) != string(after) {
		t.Fatal(r.status, r.body)
	}
	if r := e.v1Post("/api/v1/admin/claude", ck(adm), map[string]any{"claude": "maybe"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1AdminUsers(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	d := v1JSON(t, e.req("GET", "/api/v1/admin/users", ck, nil, nil))
	s := v1Str(d)
	if len(d["rows"].([]any)) == 0 || d["mfa"] == nil || regexp.MustCompile(`(?i)"(hash|password|secret|totp)[a-z_]*":`).MatchString(s) {
		t.Fatal(s[:300])
	}
	for _, x := range d["rows"].([]any) {
		u := x.(map[string]any)
		if u["you"] == true && len(u["actions"].([]any)) != 0 {
			t.Fatal("actions on your own account", u)
		}
	}
	// the temporary password comes once, in the answer to the action; the list never carries it
	r := e.v1Post("/api/v1/admin/users/act", ck, map[string]any{"action": "add", "user": "new.user", "role": "viewer"}, nil)
	m := v1JSON(t, r)
	msg := m["message"].(string)
	if m["ok"] != true || !strings.Contains(msg, "shown only now: ") {
		t.Fatal(r.body)
	}
	pw := msg[strings.LastIndex(msg, " ")+1:]
	if got := e.req("GET", "/api/v1/admin/users", ck, nil, nil); strings.Contains(got.body, pw) {
		t.Fatal("the list carries the temporary password")
	}
	if !strings.Contains(r.header.Get("Cache-Control"), "no-store") {
		t.Fatal("cacheable")
	}
	if x := v1JSON(t, e.v1Post("/api/v1/admin/users/act", ck, map[string]any{"action": "add", "user": "new.user"}, nil)); x["ok"] != false || x["kind"] != "fail" {
		t.Fatal(x)
	}
	if x := v1JSON(t, e.v1Post("/api/v1/admin/users/act", ck, map[string]any{"action": "disable", "user": "admin"}, nil)); x["message"] != "Not allowed." {
		t.Fatal(x)
	}
	if r := e.v1Post("/api/v1/admin/users/matrix", ck, map[string]any{"user": "nobody.here", "matrix_id": ""}, nil); r.status != 400 {
		t.Fatal(r.status)
	}
}
