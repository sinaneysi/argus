package app

import (
	"os"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

// The package's tests run with the marketing module on (the pages they check include SEO); TestModuleSwitch turns it
// off and back.
func TestMain(m *testing.M) {
	ModulesEnv = "marketing"
	os.Exit(m.Run())
}

func TestModuleSwitch(t *testing.T) {
	e := iaSetup(t)
	ModulesEnv = ""
	defer func() { ModulesEnv = "marketing" }()
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	navHas := func() bool { return strings.Contains(e.req("GET", "/api/v1/nav", ck(vw), nil, nil).body, `"/seo"`) }
	seo := func() int { return e.req("GET", "/api/v1/seo", ck(vw), nil, nil).status }
	if navHas() || seo() != 404 {
		t.Fatal("default: marketing must be off (hidden, 404)")
	}
	if e.v1Post("/api/v1/seo/send", ck(adm), map[string]any{}, nil).status != 404 {
		t.Fatal("POST of an off module")
	}
	if r := e.v1Post("/api/v1/admin/modules", ck(vw), map[string]any{"module": "marketing", "on": "1"}, nil); r.status != 403 {
		t.Fatal("viewer switched a module", r.status)
	}
	if r := e.v1Post("/api/v1/admin/modules", ck(adm), map[string]any{"module": "nope", "on": "1"}, nil); r.status != 400 {
		t.Fatal("unknown module", r.status)
	}
	if r := e.v1Post("/api/v1/admin/modules", ck(adm), map[string]any{"module": "marketing", "on": "1"}, nil); r.status != 200 {
		t.Fatal("switch on", r.status, r.body)
	}
	if !navHas() || seo() != 200 {
		t.Fatal("on by settings: shown at once")
	}
	d := v1JSON(t, e.req("GET", "/api/v1/admin", ck(adm), nil, nil))
	if m := d["modules"].([]any)[0].(map[string]any); m["on"] != true || m["source"] != "settings" {
		t.Fatal(v1Str(m))
	}
	// the configuration turns it on; an admin's "off" wins; "config" drops the admin's choice
	ModulesEnv = "marketing"
	e.v1Post("/api/v1/admin/modules", ck(adm), map[string]any{"module": "marketing", "on": "0"}, nil)
	if navHas() || seo() != 404 {
		t.Fatal("admin off wins over the configuration")
	}
	e.v1Post("/api/v1/admin/modules", ck(adm), map[string]any{"module": "marketing", "on": "config"}, nil)
	if on, src := ModuleOn("marketing"); !on || src != "config" {
		t.Fatal(on, src)
	}
}
