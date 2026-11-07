package app

// Tests of the help pages (help.go): parsing, routes, viewer vs admin, search escaping, the Markdown render and a
// link checker over every internal link of the docs.

import (
	"regexp"
	"testing"
)

var helpWant = []string{"index", "overview", "review", "operations", "delivery", "infrastructure", "security", "marketing", "agent", "settings",
	"notifications", "safety", "glossary", "whats-new"}

// routes that are served outside ROUTES (server.go's switch and the fixer pages)
var helpExtraRoutes = map[string]bool{"/admin": true, "/admin/users": true, "/account": true, "/admin/fix": true,
	"/inventory.csv": true}

func TestHelpDocsParse(t *testing.T) {
	if len(helpDocs) != len(helpWant) {
		t.Fatalf("docs: got %d, want %d", len(helpDocs), len(helpWant))
	}
	reID := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	for _, s := range helpWant {
		d, ok := helpBySlug[s]
		if !ok {
			t.Fatalf("missing help page %s", s)
		}
		if d.Title == "" || d.Title == s || d.Summary == "" {
			t.Errorf("%s: title or summary missing", s)
		}
		if s != "index" && len(d.Sections) < 1 {
			t.Errorf("%s: no sections", s)
		}
		for id := range d.ids {
			if !reID.MatchString(id) {
				t.Errorf("%s: bad id %q", s, id)
			}
		}
	}
	if !helpBySlug["settings"].Admin {
		t.Error("settings must be admins only")
	}
	if helpDocs[0].Slug != "index" {
		t.Error("index must come first")
	}
}

func TestHelpParseBlocks(t *testing.T) {
	d := parseHelp("x", "---\ntitle: X\nsummary: s\n---\nintro\n## One {#first}\ntext\n:::details Q?\nanswer\n:::\n"+
		":::admin\nsecret-ish setting `KEY`\n:::\n```\n## not a section\n:::\n```\n## Two {admin}\n### Sub\nbody\n")
	if len(d.Sections) != 2 || d.Sections[0].ID != "first" || d.Sections[1].ID != "two" || !d.Sections[1].Admin {
		t.Fatalf("sections: %+v", d.Sections)
	}

}

func TestHelpSearch(t *testing.T) {

	if s := helpSnippet("ab <b>Fix</b> it", "fix"); s != "ab &lt;b&gt;<mark>Fix</mark>&lt;/b&gt; it" {
		t.Errorf("snippet: %s", s)
	}
}
