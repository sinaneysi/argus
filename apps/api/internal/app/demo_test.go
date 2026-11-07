package app

import (
	"net/url"
	"strings"
	"testing"
)

func TestDemoModeOn(t *testing.T) {
	e := srvSetup(t)
	old, oldPW := DEMO, DEMO_PASSWORD
	DEMO, DEMO_PASSWORD = true, "Password-12345"
	defer func() { DEMO, DEMO_PASSWORD = old, oldPW }()

	tok, csrf := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	for _, p := range []string{"/api/v1/scan", "/api/v1/admin/save", "/api/v1/account", "/api/v1/incidents/act", "/api/v1/board/create", "/api/v1/today/act"} {
		r := e.req("POST", p, ck, url.Values{"csrf": {csrf}, "tasks": {"all"}}, nil)
		if r.status != 403 || !strings.Contains(r.body, "Disabled in the demo") {
			t.Fatalf("%s: %d", p, r.status)
		}
	}
	r := e.req("POST", "/api/v1/theme", ck, url.Values{"csrf": {csrf}, "theme": {"dark"}, "next": {"/"}}, nil)
	if r.status != 200 {
		t.Fatalf("theme: %d", r.status)
	}

}

// Sign-in options: demo off, no demo account and no password goes to a visitor; demo on, the throwaway accounts do.
func TestAPIv1AuthOptionsDemo(t *testing.T) {
	e := srvSetup(t)
	oldD, oldP := DEMO, DEMO_PASSWORD
	t.Cleanup(func() { DEMO, DEMO_PASSWORD = oldD, oldP })
	DEMO, DEMO_PASSWORD = false, "demo-password"
	if r := e.req("GET", "/api/v1/auth/options", nil, nil, nil); !strings.Contains(r.body, `"demo":false`) || strings.Contains(r.body, "demo-password") || strings.Contains(r.body, "demoUsers") {
		t.Fatal(r.body)
	}
	DEMO = true
	if r := e.req("GET", "/api/v1/auth/options", nil, nil, nil); !strings.Contains(r.body, `"demo":true`) || !strings.Contains(r.body, "demoUsers") {
		t.Fatal(r.body)
	}
}
