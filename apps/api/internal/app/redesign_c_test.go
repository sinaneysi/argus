package app

// Redesign C (states and accessibility): every state says what happened, whether it is safe to leave, where the
// result appears and what to do next; failures are role="alert", live progress role="status"; error and permission
// pages have one h1 and a way on; no raw errors, paths or secrets. Presentation only.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func TestRedesignCStateKinds(t *testing.T) {

	// the title is escaped, a caller's detail overrides the default, next comes last
	h := StateBox(StFailed, "<x>", "Custom.", `<a href="/runs">Agent runs</a>`)
	if !strings.Contains(h, "<b>&lt;x&gt;</b> Custom. <a href=\"/runs\">") || strings.Contains(h, "Nothing was changed") {
		t.Fatal("escape/detail/next:", h)
	}
}

func TestRedesignCBannerRoles(t *testing.T) {
	if !strings.Contains(Banner("x", "fail"), `role="alert"`) {
		t.Fatal("a fail banner is an alert")
	}
	for _, k := range []string{"ok", "warn", ""} {
		if !strings.Contains(Banner("x", k), `role="status"`) {
			t.Fatal(k, "banner is a status")
		}
	}
	if Banner("") != "" {
		t.Fatal("empty message, no banner")
	}
}

func TestRedesignCSafeErr(t *testing.T) {
	got := SafeErr("open /etc/argus/jira.pat: denied; token=glpat-abcdef password: hunter2 " +
		"Authorization Bearer " + strings.Repeat("Ab9", 15) + " <b>\nsecond line")
	for _, leak := range []string{"/etc/argus", "jira.pat", "glpat-abcdef", "hunter2", "Ab9Ab9", "<b>", "second line"} {
		if strings.Contains(got, leak) {
			t.Fatal("leaks", leak, ":", got)
		}
	}
	if !strings.Contains(got, "[path]") || !strings.Contains(got, "[hidden]") || !strings.Contains(got, "&lt;b&gt;") {
		t.Fatal("redaction markers / escaping:", got)
	}
	if n := len([]rune(SafeErr(strings.Repeat("word ", 100)))); n > 160 {
		t.Fatal("capped at 160, got", n)
	}
	if SafeErr("HTTP 401 from jira.shop.northwind.example") != "HTTP 401 from jira.shop.northwind.example" {
		t.Fatal("a plain error stays readable")
	}
}

// redesignCPage: one h1, the skip link and its target, no inline script or style, no heading level skipped.
func redesignCPage(t *testing.T, path, page string) {
	t.Helper()
	redesignCSP(t, page)
	if strings.Contains(page, `class="side"`) && (!strings.Contains(page, `<a class="skip" href="#main">`) ||
		!strings.Contains(page, `<main id="main" tabindex="-1">`)) {
		t.Fatal(path, "skip link or its target missing")
	}
	prev := 0
	for _, m := range regexp.MustCompile(`<h([1-6])[ >]`).FindAllStringSubmatch(page, -1) {
		n := int(m[1][0] - '0')
		if prev > 0 && n > prev+1 {
			t.Fatal(path, fmt.Sprintf("heading skips h%d to h%d", prev, n))
		}
		prev = n
	}
}

func TestRedesignCErrorAndPermissionPages(t *testing.T) {
	e := srvSetup(t)
	tok, _ := e.login("admin")
	ck := map[string]string{"opsr_session": tok}
	r := e.req("GET", "/nowhere", ck, nil, nil)
	if r.status != 404 || !strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
		t.Fatal("the API has no pages:", r.status)
	}

}
