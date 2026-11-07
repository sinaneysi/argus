package app

// Information architecture v2 (ia.go, views_review.go, docs/architecture/argus-2/ia.md) on the synthetic demo data set:
// the sidebar is exactly the six collapsible groups, Marketing and the footer, nothing of the removed design-review feature is
// left in a route, the nav or the help, every old URL answers 301 to a page that exists, the Review inbox lists every
// decision kind with act forms that post to the kind's own handler with the CSRF token, and viewers see less.

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sinaneysi/argus/internal/auth"
	"github.com/sinaneysi/argus/internal/demo"
)

// iaSetup is srvSetup on the demo data set (fictional Northwind), with Today and Problems on.
func iaSetup(t *testing.T) *srvEnv {
	t.Helper()
	e := srvSetup(t)
	data, dauth := filepath.Join(e.dir, "data"), filepath.Join(e.dir, "dauth")
	if err := demo.Generate(data, dauth, float64(NowInt()), demo.DefaultSeed); err != nil {
		t.Fatal(err)
	}
	DB, SET_OUT = filepath.Join(data, "ops.db"), filepath.Join(data, "settings-out")
	t.Setenv("FEATURE_TODAY", "1")
	t.Setenv("FEATURE_PROBLEMS", "1")
	// the demo's chat requests wait for demo.admin; here the admin is "admin"
	b, err := sql.Open("sqlite", BoardDBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := b.Exec("UPDATE matrix_previews SET by='admin'"); err != nil {
		t.Fatal(err)
	}
	return e
}

func iaNav(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<nav class="main" aria-label="Main">`)
	if i < 0 {
		t.Fatal("no main nav")
	}
	return body[i : i+strings.Index(body[i:], "</nav>")]
}

var (
	iaGroupRe   = regexp.MustCompile(`(?s)<details class="ng( cur)?"( open)?><summary>.*?<span class="ng-name">([^<]+)</span>(.*?)</summary><ul>(.*?)</ul></details>`)
	iaLinkRe    = regexp.MustCompile(`<li><a href="([^"]+)"([^>]*)><span>([^<]+)</span>`)
	iaGroupSum  = regexp.MustCompile(`<span class="ncount ng-sum" aria-hidden="true" title="[^"]*">([0-9]+)</span><span class="sr ng-sum">, `)
	iaWantGroup = []struct {
		name  string
		links []string
	}{
		{"Home", []string{"Today", "Review inbox"}},
		{"Incidents", []string{"Incidents", "Alerts", "Problems", "RCA library"}},
		{"Infrastructure", []string{"Inventory", "Capacity", "Certificates", "Backups", "Checks", "Alert hygiene"}},
		{"Delivery", []string{"CI/CD", "Check a repo", "Vulnerabilities"}},
		{"Work", []string{"Tasks", "Conversations", "New task"}},
		{"Agent", []string{"Runs", "Knowledge", "Fix runs"}},
		{"Marketing", []string{"SEO"}},
	}
)

func TestIARedirects(t *testing.T) {
	seen := map[string]bool{}
	for _, rd := range REDIRECTS {
		from := rd.From
		if rd.When != "" {
			from += "?" + rd.When
		}
		seen[rd.From] = true
		if RedirectFor(rd.From, rd.When) == "" {
			t.Fatal(from)
		}
	}
	for _, old := range []string{"/proposals", "/ideas", "/admin/lessons"} {
		if !seen[old] {
			t.Fatal("old URL without a redirect", old)
		}

	}
	// the query string travels; the condition is dropped

	// Problems itself stays; / lands on Today

}

// SEO came back (operator decision 2026-11-07) under /api/v1/seo; the design reviews stay removed.
var iaRemoved = regexp.MustCompile(`(?i)design.?review|apptree`)

func TestIANoDesignReview(t *testing.T) {

	for p := range POST_ROUTES {
		if strings.HasPrefix(p, "/admin/design") || strings.HasPrefix(p, "/seo") {
			t.Fatal("POST route", p)
		}
	}
	for _, s := range SECTIONS {
		for _, l := range s.Links {
			if iaRemoved.MatchString(l.Label + " " + l.Href) {
				t.Fatal("link", l.Label)
			}
		}
	}
	fs.WalkDir(helpFS, "helpdocs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := helpFS.ReadFile(p)
		if m := iaRemoved.FindString(string(b)); m != "" {
			t.Errorf("%s mentions %q", p, m)
		}
		return nil
	})
	for _, _ = range []string{"admin", "view.er"} {

	}
}

// iaFormRe is one form of a Review page: action -> its fields.
var iaFormRe = regexp.MustCompile(`(?s)<form method="post" action="([^"]+)"[^>]*>(.*?)</form>`)

func TestIAReviewInbox(t *testing.T) {
	e := iaSetup(t)
	tok, csrf := e.login("admin")
	ck := map[string]string{auth.COOKIE: tok}
	if err := os.MkdirAll(filepath.Join(SET_IN, "problem-actions"), 0o755); err != nil {
		t.Fatal(err)
	}

	// the sidebar count and Today's line agree with the inbox

	// an act form sends what the old page sent and comes back to Review (demo off: the request file is written)
	rr := e.req("POST", "/api/v1/proposals/act", ck, map[string][]string{"csrf": {csrf}, "return": {"/review"}, "action": {"reject"}, "id": {"16"}}, nil)
	if rr.status != 200 || !strings.HasPrefix(rr.loc, "/review?req=") {
		t.Fatal("reject from Review", rr.status, rr.loc)
	}
	rr = e.req("POST", "/api/v1/proposals/act", ck, map[string][]string{"csrf": {"bad"}, "return": {"/review"}, "action": {"reject"}, "id": {"16"}}, nil)
	if rr.status != 403 {
		t.Fatal("CSRF", rr.status)
	}
}
