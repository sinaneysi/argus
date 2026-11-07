package app

// The SEO page's JSON API: empty sections on a fresh database, the demo data set in full (edges, levels, the summary
// worst first, Search Console, suggestions), and Send dropping the request file the SEO job watches, with CSRF, 2FA
// policy and the demo refusal.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1SeoEmpty(t *testing.T) {
	e := srvSetup(t)
	tok, _ := e.login("view.er")
	ck := map[string]string{auth.COOKIE: tok}
	if r := e.req("GET", "/api/v1/seo", nil, nil, nil); r.status != 401 {
		t.Fatal("no session", r.status)
	}
	m := v1JSON(t, e.req("GET", "/api/v1/seo", ck, nil, nil))
	if m["empty"] != true || m["suggestions"].(map[string]any)["empty"] != true || m["gsc"].(map[string]any)["empty"] != true {
		t.Fatal(v1Str(m))
	}
	if f := m["summary"].(map[string]any)["findings"].([]any); len(f) != 0 {
		t.Fatal(v1Str(f))
	}
	if r := e.v1Post("/api/v1/seo/send", ck, map[string]any{}, nil); r.status != 400 || !strings.Contains(r.body, "No suggestions") {
		t.Fatal("send with no suggestions", r.status, r.body)
	}
}

func TestAPIv1SeoDemo(t *testing.T) {
	e := iaSetup(t)
	tok, _ := e.login("view.er")
	ck := map[string]string{auth.COOKIE: tok}
	m := v1JSON(t, e.req("GET", "/api/v1/seo", ck, nil, nil))
	body := v1Str(m)
	if m["empty"] != false || m["level"].(map[string]any)["text"] != "WARN" || m["refresh"] == nil {
		t.Fatal(body)
	}
	edges := m["edges"].([]any)
	if len(edges) != 3 {
		t.Fatal(len(edges))
	}
	var cache int
	for _, x := range edges {
		ed := x.(map[string]any)
		if ed["cache"] == true {
			cache++
			if ed["hits"] != "75 %" {
				t.Fatal("hits", v1Str(ed))
			}
		}
		if ed["now"] == nil || ed["day"] == nil || ed["week"] == nil {
			t.Fatal("windows", v1Str(ed))
		}
	}
	if cache != 1 {
		t.Fatal("cache edges", cache)
	}
	ch := m["charts"].(map[string]any)
	if ch["share"] == nil || ch["p95"] == nil || len(ch["p95"].(map[string]any)["points"].([]any)) < 600 {
		t.Fatal("charts")
	}
	for _, want := range []string{"Googlebot gets 503 on /category", "Googlebot is slower on prod-edge-02", "Not indexed: https://www.northwind.example/category/phone-cases",
		"could not fetch it (SERVER_ERROR)", "robots.txt blocks it", "Sitemap with issues", "Not an operations problem", "Clicks falling", "Fix the 5xx"} {
		if !strings.Contains(body, want) {
			t.Fatal("missing", want)
		}
	}
	sum := m["summary"].(map[string]any)
	fs := sum["findings"].([]any)
	if sum["fix"].(float64) < 1 || fs[len(fs)-1].(map[string]any)["level"].(map[string]any)["text"] != "INFO" {
		t.Fatal("worst first", v1Str(sum))
	}
	g := m["gsc"].(map[string]any)
	if g["empty"] != false || g["clicks"] == nil || g["position"] == nil || len(g["urls"].([]any)) != 6 {
		t.Fatal(v1Str(g))
	}
	s := m["suggestions"].(map[string]any)
	if s["empty"] != false || len(s["groups"].([]any)) != 4 || len(s["summary"].([]any)) != 3 {
		t.Fatal(v1Str(s))
	}
	sent := v1JSON(t, e.req("GET", "/api/v1/seo?sent=1", ck, nil, nil))
	if sent["refresh"] != nil || !strings.Contains(v1Str(sent["banners"]), "Sent.") {
		t.Fatal(v1Str(sent["banners"]))
	}
	if seoLevel(Row{"total": int64(100), "s5xx": int64(0), "p95": 0.7}, Row{"p95": 0.2}) != "FAIL" ||
		seoLevel(Row{"total": int64(100), "s5xx": int64(1), "p95": 0.2}, nil) != "WARN" || seoLevel(nil, nil) != "OK" {
		t.Fatal("levels")
	}
}

func TestAPIv1SeoSend(t *testing.T) {
	if POST_ROUTES["/api/v1/seo/send"] != PolicyNormal || !apiIsPost("/api/v1/seo/send") {
		t.Fatal("policy")
	}
	e := iaSetup(t)
	tok, _ := e.login("view.er")
	ck := map[string]string{auth.COOKIE: tok}
	if r := e.reqBody("POST", "/api/v1/seo/send", ck, `{}`, map[string]string{"Content-Type": "application/json"}); r.status != 403 || !strings.Contains(r.body, "csrf") {
		t.Fatal("csrf", r.status, r.body)
	}
	if r := e.req("GET", "/api/v1/seo/send", ck, nil, nil); r.status != 405 {
		t.Fatal("GET", r.status)
	}
	r := e.v1Post("/api/v1/seo/send", ck, map[string]any{}, nil)
	if r.status != 200 || v1JSON(t, r)["redirect"] != "/seo?sent=1" {
		t.Fatal(r.status, r.body)
	}
	files, _ := filepath.Glob(filepath.Join(SET_IN, "seo-send", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	if b, _ := os.ReadFile(files[0]); !strings.Contains(string(b), `"by": "view.er"`) || !strings.Contains(string(b), `"at": `) {
		t.Fatal(string(b))
	}
	old := DEMO
	DEMO = true
	defer func() { DEMO = old }()
	if r := e.v1Post("/api/v1/seo/send", ck, map[string]any{}, nil); r.status != 403 || v1JSON(t, r)["error"] != "Disabled in the demo" {
		t.Fatal("demo", r.status, r.body)
	}
	if files, _ := filepath.Glob(filepath.Join(SET_IN, "seo-send", "*.json")); len(files) != 1 {
		t.Fatal("the demo wrote a file", files)
	}
}
