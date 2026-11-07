package app

// Help as JSON: any signed-in user reads; a viewer gets no admin page, section or block; search hits are text, not HTML.

import (
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
)

func TestAPIv1Help(t *testing.T) {
	e := iaSetup(t)
	adm, _ := e.login("admin")
	vw, _ := e.login("view.er")
	ck := func(tok string) map[string]string { return map[string]string{auth.COOKIE: tok} }
	for _, p := range []string{"/api/v1/help", "/api/v1/help/page?slug=review", "/api/v1/help/all"} {
		if r := e.req("GET", p, nil, nil, nil); r.status != 401 {
			t.Fatal(p, r.status)
		}
	}
	a := v1JSON(t, e.req("GET", "/api/v1/help", ck(adm), nil, nil))
	v := v1JSON(t, e.req("GET", "/api/v1/help", ck(vw), nil, nil))
	if a["home"] == nil || len(a["cards"].([]any)) <= len(v["cards"].([]any)) || strings.Contains(v1Str(v), `"/help/settings"`) {
		t.Fatal(len(a["cards"].([]any)), len(v["cards"].([]any)))
	}
	// an admin-only page: the admin reads it, the viewer does not, and unknown or index slugs do not exist
	if d := v1JSON(t, e.req("GET", "/api/v1/help/page?slug=settings", ck(adm), nil, nil)); d["doc"].(map[string]any)["title"] == "" {
		t.Fatal(v1Str(d))
	}
	if r := e.req("GET", "/api/v1/help/page?slug=settings", ck(vw), nil, nil); r.status != 403 {
		t.Fatal(r.status)
	}
	for _, s := range []string{"index", "nope", "../x", ""} {
		if r := e.req("GET", "/api/v1/help/page?slug="+s, ck(adm), nil, nil); r.status != 404 {
			t.Fatal(s, r.status)
		}
	}
	// :::admin blocks and admin sections stay out of a viewer's page, the search and the printable page
	if strings.Contains(v1Str(v1JSON(t, e.req("GET", "/api/v1/help/all", ck(vw), nil, nil))), `"kind":"admin"`) {
		t.Fatal("viewer got an admin block")
	}
	all := v1Str(v1JSON(t, e.req("GET", "/api/v1/help/all", ck(adm), nil, nil)))
	if !strings.Contains(all, `"kind":"admin"`) || !strings.Contains(all, `"id":"review--`) || strings.Contains(all, "<div") {
		t.Fatal("admin page lacks its blocks or ids")
	}
	for _, d := range helpDocs {
		if d.Admin {
			continue
		}
		page := v1Str(v1JSON(t, e.req("GET", "/api/v1/help/page?slug="+d.Slug, ck(vw), nil, nil)))
		if d.Slug != "index" && strings.Contains(page, `"kind":"admin"`) {
			t.Fatal(d.Slug)
		}
		for _, s := range d.Sections {
			if s.Admin && strings.Contains(page, `"id":"`+s.ID+`"`) {
				t.Fatal(d.Slug, s.ID)
			}
		}
	}
	// search: counted, marked as data, escaped text stays text
	d := v1JSON(t, e.req("GET", "/api/v1/help?q=backup", ck(adm), nil, nil))
	res := d["results"].(map[string]any)
	if res["count"].(float64) == 0 || d["cards"] != nil || strings.Contains(v1Str(d), "<mark") || !strings.Contains(v1Str(d), `"mark":true`) {
		t.Fatal(v1Str(res)[:200])
	}
	long := v1JSON(t, e.req("GET", "/api/v1/help?q="+strings.Repeat("x", 300), ck(adm), nil, nil))
	if len([]rune(long["q"].(string))) != helpMaxQuery || long["results"].(map[string]any)["count"].(float64) != 0 {
		t.Fatal(v1Str(long)[:80])
	}
}
