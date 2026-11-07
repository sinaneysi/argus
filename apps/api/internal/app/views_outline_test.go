package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The diff of the page previews must be Python's difflib.unified_diff, line for line (vectors made by Python).
func TestOutlineUnifiedDiffMatchesPython(t *testing.T) {
	data, err := os.ReadFile("testdata/outline_difflib.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ A, B, Out []string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		got := outlineUnifiedDiff(c.A, c.B, "before", "now", 2)
		if strings.Join(got, "\n") != strings.Join(c.Out, "\n") {
			t.Errorf("case %d:\ngot  %q\nwant %q", i, got, c.Out)
		}
	}
}

func TestOutlineConnectAndRefusals(t *testing.T) {
	e := boardSetup(t)
	tok := "OUTLINEsecret_0123456789abcdef"
	if c, _ := OutlinePost(e.ctx(e.view), boardForm("action", "sync"), e.view); c != 403 {
		t.Fatal("viewer")
	}
	for _, f := range [][]string{{"action", "connect", "url", "http://docs", "token", tok}, {"action", "connect", "token", "short"},
		{"action", "connect", "token", tok, "collection", "<x>"}, {"action", "settings", "mode", "loud"},
		{"action", "hosts", "hosts", "ok.host,BAD HOST"}, {"action", "x"}} {
		c, page := OutlinePost(e.ctx(e.admin), boardForm(f...), e.admin)
		if c != 400 || strings.Contains(page, tok) || strings.Contains(page, "<x>") {
			t.Fatal("refusal", f, c)
		}
	}
	c, loc := OutlinePost(e.ctx(e.admin), boardForm("action", "connect", "token", tok, "create_collection", "1"), e.admin)
	if c != 303 || !strings.HasPrefix(loc, "/admin/outline?r=") {
		t.Fatal(c, loc)
	}
	m, st := jiraReadDrop(t, filepath.Join(SET_IN, "outline-requests"), "alex.doe")
	if st.Mode().Perm() != 0o600 || m["token"] != tok || m["url"] != outlineDefaultURL || m["collection"] != "Services" || m["create_collection"] != true {
		t.Fatal(m, st.Mode())
	}

}
