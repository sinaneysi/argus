package app

// Settings → Claude as JSON: admins only, the Go routes' policies, a refused paste names the field and never echoes the
// token, the status block comes from the same verdict as the Go page.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1ClaudePolicy(t *testing.T) {
	twins := map[string]string{}
	for _, p := range []string{"/admin/claude-token", "/admin/claude-egress"} {
		twins["/api/v1"+p] = p
	}
	settingsTwinChecks(t, twins, map[string]any{"action": "test"}, false)
}

func TestAPIv1ClaudeRead(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	if r := e.req("GET", "/api/v1/admin/claude", nil, nil, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	if r := e.req("GET", "/api/v1/admin/claude", map[string]string{auth.COOKIE: vw}, nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	st := `{"token":{"CLAUDE_CODE_OAUTH_TOKEN":{"set":true,"last4":"ab12","value":"not-a-real-value-123"}},"test":{"result":"error","message":"401 invalid login","at":1,"by":"sam.roe"}}`
	if err := os.WriteFile(filepath.Join(SET_OUT, "claude-token-status.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	r := e.req("GET", "/api/v1/admin/claude", map[string]string{auth.COOKIE: adm}, nil, nil)
	d := v1JSON(t, r)
	s := d["status"].(map[string]any)
	if strings.Contains(r.body, "not-a-real-value") || strings.Contains(r.body, "<span") || len(s["facts"].([]any)) != 4 || d["token"].(map[string]any)["open"] != true {
		t.Fatal(r.body[:400])
	}
	if !strings.Contains(d["token"].(map[string]any)["line"].(string), "ab12") {
		t.Fatal(d["token"])
	}
}

func TestAPIv1ClaudeWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	ck := map[string]string{auth.COOKIE: adm}
	if err := os.MkdirAll(filepath.Join(SET_IN, "claude-token"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := e.v1Post("/api/v1/admin/claude-token", ck, map[string]any{"action": "replace", "token": v1FakeCred}, nil)
	if m := v1JSON(t, r); r.status != 400 || m["field"] != "token" || strings.Contains(r.body, v1FakeCred) {
		t.Fatal(r.status, r.body)
	}
	for field, b := range map[string]map[string]any{"blocked": {"blocked": "Iran"}, "fail": {"fail": "maybe"}} {
		r := e.v1Post("/api/v1/admin/claude-egress", ck, b, nil)
		if m := v1JSON(t, r); r.status != 400 || m["field"] != field {
			t.Fatal(field, r.status, r.body)
		}
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "claude-token", "*")); len(l) != 0 {
		t.Fatal("a refused save wrote a request", l)
	}
	r = e.v1Post("/api/v1/admin/claude-token", ck, map[string]any{"action": "test"}, nil)
	m := v1JSON(t, r)
	if r.status != 200 || !strings.HasPrefix(m["redirect"].(string), "/admin/claude?ct=") {
		t.Fatal(r.status, r.body)
	}
	d := v1JSON(t, e.req("GET", "/api/v1/admin/claude?"+strings.SplitN(m["redirect"].(string), "?", 2)[1], ck, nil, nil))
	if d["busy"] != true || d["refresh"] == nil || len(d["notices"].([]any)) != 1 {
		t.Fatal(v1Str(d)[:300])
	}
	r = e.v1Post("/api/v1/admin/claude-egress", ck, map[string]any{"blocked": "ir, kp", "fail": "closed", "guard": "1"}, nil)
	if r.status != 200 {
		t.Fatal(r.status, r.body)
	}
}
