package app

// The lists that were still on the show-more cap (?all=1) or a .filters select form, now queue lists (DESIGN.md
// "Queue page", list_query.go): Agent runs (SQL), Capacity → All VMs, Lessons, Check a repo, QA messages and QA issues.
// Search, facet rows with counts, sort, 25 rows a page, a page past the end shows the last one, the empty state that
// says why, no select filter form, and ?all=1 no longer lists everything.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func noSelectFilter(t *testing.T, page, body string) {
	t.Helper()
	for _, f := range regexp.MustCompile(`(?s)<form class="filters[^"]*" method="get".*?</form>`).FindAllString(body, -1) {
		if strings.Contains(f, "<select") {
			t.Fatal(page, "still has a select filter form:", f[:min(len(f), 200)])
		}
	}
}

// No list page builds the old show-more cap any more, except Vulnerabilities (a follow-up: its facet counts need
// indexed SQL on cve_findings first).
func TestListsNoShowMoreCapLeft(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "core.go" || f == "views_cve.go" || f == "api_vulns.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		if regexp.MustCompile(`\bc\.ShowMore\(|[^a-zA-Z]Capped\(`).Match(b) {
			t.Fatal(f, "still uses the show-more cap")
		}
	}
}
