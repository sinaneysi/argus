package app

// The Delivery section's JSON API on the demo test data: reads need a session and have their shape, admin-only choices
// are absent for a viewer, writes pass CSRF, role, 2FA and the demo refusal, and drop the same request files as the
// pages. The step-up form (/api/v1/account/stepup) renews a stale session so the refused write goes through.

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

var v1DeliveryReads = []string{"/api/v1/cicd", "/api/v1/cicd/failures", "/api/v1/cicd/stuck", "/api/v1/cicd/audit", "/api/v1/cicd/repo", "/api/v1/vulns", "/api/v1/account/stepup"}

func TestAPIv1DeliveryReads(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	get := func(tok, u string) map[string]any {
		t.Helper()
		r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil)
		if r.status != 200 {
			t.Fatal(u, r.status, r.body)
		}
		return v1JSON(t, r)
	}
	for _, u := range v1DeliveryReads {
		if r := e.req("GET", u, nil, nil, nil); r.status != 401 {
			t.Fatal("no session", u, r.status)
		}
		for _, tok := range []string{adm, vw} {
			if r := e.req("GET", u, map[string]string{auth.COOKIE: tok}, nil, nil); r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
				t.Fatal(u, r.status, r.body)
			}
		}
	}
	o := get(adm, "/api/v1/cicd?view=all")
	rows := o["rows"].([]any)
	if len(rows) == 0 || len(rows[0].(map[string]any)["cells"].([]any)) != 3 || o["envs"] == nil || len(o["tiles"].([]any)) != 5 {
		t.Fatal(v1Str(o))
	}
	pid := int64(rows[0].(map[string]any)["id"].(float64))
	if p := get(adm, "/api/v1/cicd/project?id="+I2S(pid)); len(p["cells"].([]any)) != 3 || len(p["rail"].([]any)) < 8 {
		t.Fatal(v1Str(p))
	}
	f := get(adm, "/api/v1/cicd/failures")
	fr := f["rows"].([]any)
	if len(fr) == 0 || len(f["tabs"].([]any)) != 2 || f["list"].(map[string]any)["path"] != "/cicd/failures" {
		t.Fatal(v1Str(f))
	}
	fid := int64(fr[0].(map[string]any)["id"].(float64))
	if d := get(adm, "/api/v1/cicd/finding?id="+I2S(fid)); d["title"] == "" || d["evidence"] == nil || d["task"] == nil {
		t.Fatal(v1Str(d))
	}
	if r := get(adm, "/api/v1/cicd/failures?view=resolved"); r["view"] != "resolved" || r["list"].(map[string]any)["hidden"].(map[string]any)["view"] != "resolved" {
		t.Fatal(v1Str(r))
	}
	for _, u := range []string{"/api/v1/cicd/project?id=x", "/api/v1/cicd/project?id=999999", "/api/v1/cicd/finding?id=999999", "/api/v1/vulns/group?group=bad", "/api/v1/vulns/group?id=999999"} {
		if r := e.req("GET", u, map[string]string{auth.COOKIE: adm}, nil, nil); r.status != 404 {
			t.Fatal(u, r.status)
		}
	}
	if i := get(adm, "/api/v1/cicd/repo"); i["mode"] != "index" {
		t.Fatal(v1Str(i))
	}
	if b := get(adm, "/api/v1/cicd/repo?project=https://elsewhere.example/a/b"); b["mode"] != "index" || b["error"] == "" {
		t.Fatal(v1Str(b))
	}
	if n := get(adm, "/api/v1/cicd/repo?project=nobody/nothing"); n["mode"] != "repo" || n["report"] != nil || n["check"].(map[string]any)["label"] != "Check this repo" {
		t.Fatal(v1Str(n))
	}
	v := get(adm, "/api/v1/vulns")
	fx := v["fixFirst"].([]any)
	if len(fx) == 0 || len(v["tiles"].([]any)) != 5 || v["coverage"] == nil {
		t.Fatal(v1Str(v))
	}
	href := fx[0].(map[string]any)["href"].(string)
	g := "/api/v1/vulns/group?" + strings.SplitN(href, "?", 2)[1]
	if d := get(adm, g); d["suppress"] == nil || d["canSuppress"] != true || len(d["vulns"].([]any)) == 0 {
		t.Fatal(v1Str(d))
	}
	if d := get(vw, g); d["suppress"] != nil || d["canSuppress"] != false {
		t.Fatal(v1Str(d))
	}
}

func TestAPIv1DeliveryWrites(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	if r := e.v1Post("/api/v1/cicd/repo/check", nil, map[string]any{"project": "shop/web"}, nil); r.status != 401 {
		t.Fatal(r.status)
	}
	for _, p := range []string{"/api/v1/cicd/repo/check", "/api/v1/vulns/suppress"} {
		if r := e.req("POST", p, ck(adm), url.Values{"x": {"1"}}, nil); r.status != 403 {
			t.Fatal("no csrf", p, r.status)
		}
		if r := e.req("GET", p, ck(adm), nil, nil); r.status != 405 {
			t.Fatal(p, r.status)
		}
	}
	r := e.v1Post("/api/v1/cicd/repo/check", ck(adm), map[string]any{"project": "shop/web"}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["ok"] != true || !strings.Contains(m["redirect"].(string), "sent=1") {
		t.Fatal(r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(SET_IN, "requests", repoReqName("shop/web")+".req")); err != nil {
		t.Fatal(err)
	}
	if r := e.v1Post("/api/v1/cicd/repo/check", ck(adm), map[string]any{"project": "https://elsewhere.example/a/b"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	body := map[string]any{"action": "add", "vuln_key": "CVE-2026-0001", "component": "openssl", "reason": "Not reachable in this build of the shop", "expires": "2026-12-01"}
	if r := e.v1Post("/api/v1/vulns/suppress", ck(vw), body, nil); r.status != 403 {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/vulns/suppress", ck(adm), body, nil)
	if m := v1JSON(t, r); r.status != 200 || m["redirect"] != "/vulns" {
		t.Fatal(r.status, r.body)
	}
	if l, _ := filepath.Glob(filepath.Join(SET_IN, "cve-suppressions", "*.json")); len(l) != 1 {
		t.Fatal(l)
	}
	if r := e.v1Post("/api/v1/vulns/suppress", ck(adm), map[string]any{"action": "add", "component": "openssl", "reason": "short", "expires": "2026-12-01"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
}

func TestAPIv1DeliveryMFAAndStepup(t *testing.T) {
	e := iaSetup(t)
	nomfa, _ := e.loginNoMFA("admin")
	sec := srvEnrol(t, nomfa)
	ck := map[string]string{auth.COOKIE: nomfa}
	r := e.v1Post("/api/v1/cicd/repo/check", ck, map[string]any{"project": "shop/web"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != false {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/vulns/suppress", ck, map[string]any{"action": "remove", "id": "0123456789ab"}, nil)
	if m := v1JSON(t, r); r.status != 403 || m["mfa"] != "stepup" || m["sensitive"] != true {
		t.Fatal(r.status, r.body)
	}
	st := v1JSON(t, e.req("GET", "/api/v1/account/stepup?sensitive=1&next="+url.QueryEscape("/vulns?x=1"), ck, nil, nil))
	if st["state"] != "stepup" || st["next"] != "/vulns?x=1" || st["why"] == "" {
		t.Fatal(v1Str(st))
	}
	if r := e.v1Post("/api/v1/account/stepup", ck, map[string]any{"code": "000000", "next": "/vulns"}, nil); r.status != 400 {
		t.Fatal(r.status, r.body)
	}
	r = e.v1Post("/api/v1/account/stepup", ck, map[string]any{"code": auth.TOTPAt(sec, auth.Step(NowInt())+1), "next": "//evil.example/"}, nil)
	if m := v1JSON(t, r); r.status != 200 || m["redirect"] != "/" {
		t.Fatal(r.status, r.body)
	}
	nt := cookieOf(r, auth.COOKIE)
	if nt == "" || nt == nomfa {
		t.Fatal("no rotated session", r.header)
	}
	if r := e.v1Post("/api/v1/cicd/repo/check", map[string]string{auth.COOKIE: cookieVal(nt)}, map[string]any{"project": "shop/web"}, nil); r.status != 200 {
		t.Fatal(r.status, r.body)
	}
}
