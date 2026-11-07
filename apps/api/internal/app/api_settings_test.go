package app

// The Settings twins: each has its Go route's policy and body limit, and refuses a missing session, a viewer, a missing
// CSRF token, a stale 2FA and the demo before anything is written.

import (
	"net/url"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func settingsTwinChecks(t *testing.T, twins map[string]string, body map[string]any, viewerAllowed bool) {
	t.Helper()
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	nomfa, _ := e.loginNoMFA("admin")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	for api, page := range twins {
		if !apiIsPost(api) {
			t.Fatal(api, "policy")
		}
		want, ok := postLimits[page]
		if !ok {
			want = apiMaxBody
		}
		if apiBodyLimit(api) != want {
			t.Fatal(api, "body limit", apiBodyLimit(api), want)
		}
		if r := e.v1Post(api, nil, body, nil); r.status != 401 {
			t.Fatal(api, "no session", r.status)
		}
		if r := e.req("POST", api, ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 || !strings.Contains(r.body, "csrf") {
			t.Fatal(api, "no csrf", r.status, r.body)
		}
		if !viewerAllowed {
			if r := e.v1Post(api, ck(vw), body, nil); r.status != 403 {
				t.Fatal(api, "viewer", r.status, r.body)
			}
		}
		if r := e.v1Post(api, ck(nomfa), body, nil); r.status != 403 || v1JSON(t, r)["mfa"] != "stepup" {
			t.Fatal(api, "2fa", r.status, r.body)
		}
	}
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	for api := range twins {
		if r := e.v1Post(api, ck(adm), body, nil); r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
			t.Fatal(api, "demo", r.status, r.body)
		}
	}
}
